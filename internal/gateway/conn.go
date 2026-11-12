package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/GlassJob/job-center/pkg/protocol"
	"github.com/coder/websocket"
)

type Conn struct {
	id         string
	ws         *websocket.Conn
	peerIP     string
	hub        *Hub
	exec       *service.Executor
	executions *service.Executions
	cfg        config.WebSocketConfig
	log        *slog.Logger

	mu       sync.Mutex
	sess     *service.Session
	closed   bool
	replaced bool
	normal   bool

	sendCh chan []byte
}

func newConn(ws *websocket.Conn, peerIP string, hub *Hub, exec *service.Executor, executions *service.Executions, cfg config.WebSocketConfig, log *slog.Logger) *Conn {
	id := idgen.New()
	return &Conn{
		id:         id,
		ws:         ws,
		peerIP:     peerIP,
		hub:        hub,
		exec:       exec,
		executions: executions,
		cfg:        cfg,
		log:        log.With("connection_id", id),
		sendCh:     make(chan []byte, cfg.SendQueueSize),
	}
}

func (c *Conn) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer c.cleanup(ctx)

	go c.writeLoop(ctx)

	authDeadline := time.Now().Add(c.cfg.AuthTimeoutDuration())
	for {
		readTimeout := c.cfg.PingTimeoutDuration()
		if c.session() == nil {
			rem := time.Until(authDeadline)
			if rem <= 0 {
				c.close(protocol.CloseAuthTimeout, protocol.ErrAuthTimeout)
				return
			}
			if rem < readTimeout {
				readTimeout = rem
			}
		}
		rctx, rcancel := context.WithTimeout(ctx, readTimeout)
		typ, data, err := c.ws.Read(rctx)
		rcancel()
		if err != nil {
			if c.session() == nil && time.Now().After(authDeadline) {
				c.close(protocol.CloseAuthTimeout, protocol.ErrAuthTimeout)
			}
			return
		}
		if typ != websocket.MessageText {
			c.close(websocket.StatusUnsupportedData, "binary frames not supported")
			return
		}
		if int64(len(data)) > c.cfg.MaxMessageBytes {
			c.close(websocket.StatusMessageTooBig, "message too large")
			return
		}
		if err := c.handle(ctx, data); err != nil {
			if errors.Is(err, service.ErrSessionStale) {
				c.close(protocol.CloseSessionReplaced, protocol.ErrSessionReplaced)
				return
			}
			c.log.Warn("handle message failed", "err", err)
		}
	}
}

func (c *Conn) session() *service.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *Conn) handle(ctx context.Context, raw []byte) error {
	env, err := protocol.DecodeEnvelope(raw)
	if err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid envelope", false, "")
	}
	if !protocol.KnownTypes[env.Type] {
		return c.sendError(protocol.ErrProtocolUnknownType, "unknown type "+env.Type, false, env.MessageID)
	}
	if !protocol.ExecutorToCenter[env.Type] {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "type not allowed from executor", false, env.MessageID)
	}
	if c.session() == nil && env.Type != protocol.TypeAuthRequest {
		_ = c.reply(protocol.TypeAuthResponse, env.MessageID, protocol.AuthResponse{
			OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthRequired, Message: "authenticate first"},
		})
		c.close(protocol.CloseAuthFailed, protocol.ErrAuthRequired)
		return nil
	}

	switch env.Type {
	case protocol.TypeAuthRequest:
		return c.onAuth(ctx, env)
	case protocol.TypeServiceRegister:
		return c.onRegister(ctx, env)
	case protocol.TypeServiceHeartbeat:
		return c.onHeartbeat(ctx, env)
	case protocol.TypeServiceUnregister:
		return c.onUnregister(ctx, env)
	case protocol.TypeTaskExecuteAck, protocol.TypeTaskStarted, protocol.TypeTaskProgress,
		protocol.TypeTaskLog, protocol.TypeTaskCheckpoint, protocol.TypeTaskCompleted, protocol.TypeTaskFailed,
		protocol.TypeTaskCancelAck, protocol.TypeTaskCancelled, protocol.TypeTaskSyncRunning:
		return c.onTask(ctx, env)
	default:
		return c.sendError(protocol.ErrNotImplemented, "unsupported message type", false, env.MessageID)
	}
}

func (c *Conn) onAuth(ctx context.Context, env *protocol.Envelope) error {
	if c.session() != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "already authenticated", false, env.MessageID)
	}
	var req protocol.AuthRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid auth.request data", false, env.MessageID)
	}
	sess, resp, err := c.exec.AuthenticateHMAC(ctx, req, c.peerIP)
	if err != nil {
		c.log.Warn("auth failed", "err", err)
		_ = c.reply(protocol.TypeAuthResponse, env.MessageID, protocol.AuthResponse{
			OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "auth failed"},
		})
		c.close(protocol.CloseAuthFailed, protocol.ErrAuthInvalidCredential)
		return nil
	}
	if resp == nil {
		resp = &protocol.AuthResponse{OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrAuthInvalidCredential, Message: "auth failed"}}
	}
	if !resp.OK {
		_ = c.reply(protocol.TypeAuthResponse, env.MessageID, resp)
		code := websocket.StatusCode(protocol.CloseAuthFailed)
		reason := protocol.ErrAuthInvalidCredential
		if resp.Error != nil {
			reason = resp.Error.Code
			if resp.Error.Code == protocol.ErrAuthServiceDisabled {
				code = protocol.CloseServiceDisabled
			}
			if resp.Error.Code == protocol.ErrProtocolUnsupported {
				code = protocol.CloseUnsupported
			}
		}
		c.close(code, reason)
		return nil
	}

	resp.HeartbeatIntervalSeconds = int(c.cfg.HeartbeatIntervalDuration().Seconds())
	resp.LeaseTTLSeconds = int(c.cfg.LeaseTTLDuration().Seconds())
	resp.MaxMessageBytes = c.cfg.MaxMessageBytes
	if resp.ServerTime == "" {
		resp.ServerTime = protocol.FormatTime(time.Now())
	}

	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	c.log = c.log.With("service_code", sess.ServiceCode, "instance_id", sess.InstanceID, "session_epoch", sess.SessionEpoch)

	if old := c.hub.Bind(sess.InstanceID, c); old != nil && old != c {
		old.notifyReplaced()
	}
	return c.reply(protocol.TypeAuthResponse, env.MessageID, resp)
}

func (c *Conn) onRegister(ctx context.Context, env *protocol.Envelope) error {
	var req protocol.RegisterRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.register data", false, env.MessageID)
	}
	ack, err := c.exec.RegisterWithSession(ctx, c.session(), req, c.peerIP, false)
	if err != nil {
		if errors.Is(err, service.ErrSessionStale) {
			return err
		}
		return c.sendError("REGISTER_FAILED", err.Error(), true, env.MessageID)
	}
	return c.reply(protocol.TypeServiceRegisterAck, env.MessageID, ack)
}

func (c *Conn) onHeartbeat(ctx context.Context, env *protocol.Envelope) error {
	sess := c.session()
	if sess == nil || !sess.Registered {
		return c.sendError(protocol.ErrInstanceNotRegistered, "register first", false, env.MessageID)
	}
	var req protocol.HeartbeatRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.heartbeat data", false, env.MessageID)
	}
	ack, err := c.exec.HeartbeatWithSession(ctx, sess, req)
	if err != nil {
		if errors.Is(err, service.ErrSessionStale) {
			return err
		}
		if errors.Is(err, service.ErrNotFound) {
			return c.sendError(protocol.ErrInstanceNotRegistered, "instance not registered", false, env.MessageID)
		}
		return c.sendError("HEARTBEAT_FAILED", err.Error(), true, env.MessageID)
	}
	if c.executions != nil {
		c.executions.RenewLeasesForInstance(ctx, sess.InstanceID)
	}
	return c.reply(protocol.TypeServiceHeartbeatAck, env.MessageID, ack)
}

func (c *Conn) onUnregister(ctx context.Context, env *protocol.Envelope) error {
	var req protocol.UnregisterRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.unregister data", false, env.MessageID)
	}
	if err := c.exec.UnregisterWithSession(ctx, c.session()); err != nil {
		if errors.Is(err, service.ErrSessionStale) {
			return err
		}
		return c.sendError("UNREGISTER_FAILED", err.Error(), true, env.MessageID)
	}
	c.mu.Lock()
	c.normal = true
	c.mu.Unlock()
	return nil
}

func (c *Conn) onTask(ctx context.Context, env *protocol.Envelope) error {
	sess := c.session()
	if sess == nil || !sess.Registered {
		return c.sendError(protocol.ErrInstanceNotRegistered, "register first", false, env.MessageID)
	}
	if c.executions == nil {
		return c.sendError(protocol.ErrNotImplemented, "execution service not wired", false, env.MessageID)
	}
	var ack *protocol.TaskResultAck
	switch env.Type {
	case protocol.TypeTaskExecuteAck:
		var m protocol.TaskExecuteAck
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid execute_ack", false, env.MessageID)
		}
		ack = c.executions.OnExecuteAck(ctx, sess.InstanceID, m)
	case protocol.TypeTaskStarted:
		var m protocol.TaskStarted
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid started", false, env.MessageID)
		}
		ack = c.executions.OnStarted(ctx, sess.InstanceID, m)
	case protocol.TypeTaskProgress:
		var m protocol.TaskProgress
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid progress", false, env.MessageID)
		}
		ack = c.executions.OnProgress(ctx, sess.InstanceID, m)
	case protocol.TypeTaskLog:
		var m protocol.TaskLog
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid log", false, env.MessageID)
		}
		ack = c.executions.OnLog(ctx, sess.InstanceID, m)
	case protocol.TypeTaskCheckpoint:
		var m protocol.TaskCheckpoint
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid checkpoint", false, env.MessageID)
		}
		ack = c.executions.OnCheckpoint(ctx, sess.InstanceID, m)
	case protocol.TypeTaskCompleted:
		var m protocol.TaskCompleted
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid completed", false, env.MessageID)
		}
		ack = c.executions.OnCompleted(ctx, sess.InstanceID, m)
	case protocol.TypeTaskFailed:
		var m protocol.TaskFailed
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid failed", false, env.MessageID)
		}
		ack = c.executions.OnFailed(ctx, sess.InstanceID, m)
	case protocol.TypeTaskCancelAck:
		var m protocol.TaskCancelAck
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid cancel_ack", false, env.MessageID)
		}
		ack = c.executions.OnCancelAck(ctx, sess.InstanceID, m)
	case protocol.TypeTaskCancelled:
		var m protocol.TaskCancelled
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid cancelled", false, env.MessageID)
		}
		ack = c.executions.OnCancelled(ctx, sess.InstanceID, m)
	case protocol.TypeTaskSyncRunning:
		var m protocol.TaskSyncRunning
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid sync_running", false, env.MessageID)
		}
		return c.reply(protocol.TypeTaskSyncRunningAck, env.MessageID, c.executions.SyncRunning(ctx, sess.InstanceID, m))
	}
	if ack != nil {
		return c.reply(protocol.TypeTaskResultAck, env.MessageID, ack)
	}
	return nil
}

func (c *Conn) reply(typ, reqID string, data any) error {
	b, _, err := protocol.NewMessage(typ, reqID, "", data)
	if err != nil {
		return err
	}
	return c.enqueue(b)
}

func (c *Conn) sendError(code, msg string, retryable bool, ref string) error {
	return c.reply(protocol.TypeSystemError, ref, protocol.SystemError{Code: code, Message: msg, Retryable: retryable})
}

func (c *Conn) enqueue(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("connection closed")
	}
	select {
	case c.sendCh <- b:
		return nil
	default:
		go c.close(websocket.StatusPolicyViolation, "send queue full")
		return errors.New("send queue full")
	}
}

func (c *Conn) writeLoop(ctx context.Context) {
	interval := c.cfg.PingIntervalDuration()
	if interval <= 0 {
		interval = 20 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-c.sendCh:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, c.cfg.WriteTimeoutDuration())
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.cfg.PingTimeoutDuration())
			err := c.ws.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (c *Conn) notifyReplaced() {
	c.mu.Lock()
	c.replaced = true
	c.mu.Unlock()
	b, _, err := protocol.NewMessage(protocol.TypeSystemNotice, "", "", protocol.SystemNotice{
		Code: protocol.ErrSessionReplaced, Message: "a newer connection authenticated with the same instance_id",
	})
	if err == nil {
		_ = c.enqueue(b)
	}
	time.AfterFunc(150*time.Millisecond, func() {
		c.close(protocol.CloseSessionReplaced, protocol.ErrSessionReplaced)
	})
}

func (c *Conn) close(code websocket.StatusCode, reason string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if code == websocket.StatusNormalClosure {
		c.normal = true
	}
	c.mu.Unlock()
	_ = c.ws.Close(code, truncateReason(reason))
}

func truncateReason(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}

func (c *Conn) cleanup(ctx context.Context) {
	c.mu.Lock()
	sess := c.sess
	replaced := c.replaced
	c.closed = true
	c.mu.Unlock()
	if sess != nil {
		c.hub.Unbind(sess.InstanceID, c)
		if !replaced {
			if err := c.exec.Disconnect(context.WithoutCancel(ctx), sess); err != nil {
				c.log.Warn("disconnect update failed", "err", err)
			}
		}
	}
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
