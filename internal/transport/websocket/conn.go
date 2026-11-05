package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/GlassJob/job-center/internal/application/apperr"
	appexec "github.com/GlassJob/job-center/internal/application/execution"
	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/infrastructure/idgen"
	"github.com/GlassJob/job-center/internal/protocol"
	"github.com/coder/websocket"
)

type Conn struct {
	id     string
	ws     *websocket.Conn
	peerIP string
	hub    *Hub
	reg    *registration.Service
	exec   *appexec.Service
	cfg    *config.Config
	val    *protocol.Validator
	log    *slog.Logger

	mu       sync.Mutex
	sess     *registration.Session
	closed   bool
	replaced bool
	normal   bool

	sendCh chan []byte
}

func newConn(ws *websocket.Conn, peerIP string, hub *Hub, reg *registration.Service, exec *appexec.Service, cfg *config.Config, val *protocol.Validator, log *slog.Logger) *Conn {
	id := idgen.NewULID()
	return &Conn{
		id: id, ws: ws, peerIP: peerIP, hub: hub, reg: reg, exec: exec, cfg: cfg, val: val,
		log: log.With("connection_id", id),
		sendCh: make(chan []byte, cfg.WebSocket.SendQueueSize),
	}
}

func (c *Conn) Run(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer c.cleanup(ctx)

	go c.writeLoop(ctx)

	authDeadline := time.Now().Add(c.cfg.WebSocket.AuthTimeout.D())
	for {
		readTimeout := c.cfg.WebSocket.PingTimeout.D()
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
		if int64(len(data)) > c.cfg.WebSocket.MaxMessageBytes {
			c.close(websocket.StatusMessageTooBig, protocol.ErrProtocolMessageTooLarge)
			return
		}
		if err := c.handle(ctx, data); err != nil {
			var ae *apperr.Error
			if errors.As(err, &ae) && ae.Kind == apperr.KindUnauthenticated {
				_ = c.sendAuthFail(ae)
				code := websocket.StatusCode(protocol.CloseAuthFailed)
				if ae.Code == protocol.ErrAuthServiceDisabled {
					code = protocol.CloseServiceDisabled
				}
				c.close(code, ae.Code)
				return
			}
			if errors.Is(err, registration.ErrSessionStale) {
				c.close(protocol.CloseSessionReplaced, protocol.ErrSessionReplaced)
				return
			}
			c.log.Warn("handle message failed", "err", err)
		}
	}
}

func (c *Conn) session() *registration.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *Conn) handle(ctx context.Context, raw []byte) error {
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid json", false, "")
	}
	if env.Type == "" {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "missing type", false, "")
	}
	if !protocol.KnownTypes[env.Type] {
		return c.sendError(protocol.ErrProtocolUnknownType, "unknown type "+env.Type, false, env.MessageID)
	}
	if !protocol.ExecutorToCenter[env.Type] {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "type not allowed from executor", false, env.MessageID)
	}
	if c.val != nil {
		if err := c.val.Validate(env.Type, raw, false); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, err.Error(), false, env.MessageID)
		}
	}
	if c.session() == nil && env.Type != protocol.TypeAuthRequest {
		return apperr.New(apperr.KindUnauthenticated, protocol.ErrAuthRequired, "authenticate first")
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
	case protocol.TypeTaskExecuteAck:
		return c.onTaskResult(ctx, env, "execute_ack")
	case protocol.TypeTaskStarted:
		return c.onTaskResult(ctx, env, "started")
	case protocol.TypeTaskProgress:
		return c.onTaskResult(ctx, env, "progress")
	case protocol.TypeTaskLog:
		return c.onTaskResult(ctx, env, "log")
	case protocol.TypeTaskCompleted:
		return c.onTaskResult(ctx, env, "completed")
	case protocol.TypeTaskFailed:
		return c.onTaskResult(ctx, env, "failed")
	case protocol.TypeTaskCancelled:
		return c.onTaskResult(ctx, env, "cancelled")
	case protocol.TypeTaskCancelAck:
		return c.onCancelAck(ctx, env)
	default:
		return c.sendError(protocol.ErrNotImplemented, "unsupported message type", false, env.MessageID)
	}
}

func (c *Conn) onTaskResult(ctx context.Context, env protocol.Envelope, kind string) error {
	if c.exec == nil {
		return c.sendError(protocol.ErrNotImplemented, "execution service not wired", false, env.MessageID)
	}
	sess := c.session()
	if sess == nil || !sess.Registered {
		return c.sendError(protocol.ErrInstanceNotRegistered, "register first", false, env.MessageID)
	}
	var ack *protocol.TaskResultAck
	var err error
	switch kind {
	case "execute_ack":
		var m protocol.TaskExecuteAck
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid execute_ack", false, env.MessageID)
		}
		ack, err = c.exec.OnExecuteAck(ctx, sess.InstanceID, m)
	case "started":
		var m protocol.TaskStarted
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid started", false, env.MessageID)
		}
		ack, err = c.exec.OnStarted(ctx, sess.InstanceID, m)
	case "progress":
		var m protocol.TaskProgress
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid progress", false, env.MessageID)
		}
		ack, err = c.exec.OnProgress(ctx, sess.InstanceID, m)
	case "log":
		var m protocol.TaskLog
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid log", false, env.MessageID)
		}
		ack, err = c.exec.OnLog(ctx, sess.InstanceID, m)
	case "completed":
		var m protocol.TaskCompleted
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid completed", false, env.MessageID)
		}
		ack, err = c.exec.OnCompleted(ctx, sess.InstanceID, m)
	case "failed":
		var m protocol.TaskFailed
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid failed", false, env.MessageID)
		}
		ack, err = c.exec.OnFailed(ctx, sess.InstanceID, m)
	case "cancelled":
		var m protocol.TaskCancelled
		if err := json.Unmarshal(env.Data, &m); err != nil {
			return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid cancelled", false, env.MessageID)
		}
		ack, err = c.exec.OnCancelled(ctx, sess.InstanceID, m)
	}
	if err != nil {
		ae := apperr.As(err)
		return c.sendError(ae.Code, ae.Message, ae.Retryable, env.MessageID)
	}
	if ack != nil {
		return c.reply(protocol.TypeTaskResultAck, env.MessageID, ack)
	}
	return nil
}

func (c *Conn) onCancelAck(ctx context.Context, env protocol.Envelope) error {
	if c.exec == nil {
		return c.sendError(protocol.ErrNotImplemented, "execution service not wired", false, env.MessageID)
	}
	sess := c.session()
	if sess == nil {
		return c.sendError(protocol.ErrAuthRequired, "authenticate first", false, env.MessageID)
	}
	var m protocol.TaskCancelAck
	if err := json.Unmarshal(env.Data, &m); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid cancel_ack", false, env.MessageID)
	}
	if err := c.exec.OnCancelAck(ctx, sess.InstanceID, m); err != nil {
		ae := apperr.As(err)
		return c.sendError(ae.Code, ae.Message, ae.Retryable, env.MessageID)
	}
	return nil
}

func (c *Conn) onAuth(ctx context.Context, env protocol.Envelope) error {
	var req protocol.AuthRequest
	if err := json.Unmarshal(env.Data, &req); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid auth.request data", false, env.MessageID)
	}
	ver, ok := pickVersion(req.ProtocolVersions)
	if !ok {
		_ = c.reply(protocol.TypeAuthResponse, env.MessageID, protocol.AuthResponse{
			OK: false, Error: &protocol.ErrorBody{Code: protocol.ErrProtocolUnsupported, Message: "no common protocol version"},
		})
		c.close(protocol.CloseUnsupported, protocol.ErrProtocolUnsupported)
		return nil
	}
	sess, err := c.reg.Authenticate(ctx, registration.AuthInput{
		Request: req, PeerIP: c.peerIP, ConnectionID: c.id, ProtocolVersion: ver,
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
	c.log = c.log.With("service_code", sess.ServiceCode, "instance_id", sess.InstanceID, "session_epoch", sess.Epoch)

	if old := c.hub.Bind(sess.InstanceID, c); old != nil && old != c {
		old.notifyReplaced()
	}

	return c.reply(protocol.TypeAuthResponse, env.MessageID, protocol.AuthResponse{
		OK: true, SessionID: c.id, SessionEpoch: sess.Epoch, ProtocolVersion: ver,
		ServerTime:               protocol.FormatTime(time.Now()),
		HeartbeatIntervalSeconds: int(c.cfg.Instance.HeartbeatInterval.D().Seconds()),
		LeaseTTLSeconds:          int(c.cfg.WebSocket.LeaseTTL.D().Seconds()),
		MaxMessageBytes:          c.cfg.WebSocket.MaxMessageBytes,
		LogBatchMaxEntries:       c.cfg.WebSocket.LogBatchMaxEntries,
	})
}

func pickVersion(versions []string) (string, bool) {
	for _, v := range versions {
		if v == protocol.Version || v == "1" || v == "1.0" {
			return protocol.Version, true
		}
	}
	return "", false
}

func (c *Conn) onRegister(ctx context.Context, env protocol.Envelope) error {
	var d protocol.RegisterData
	if err := json.Unmarshal(env.Data, &d); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.register data", false, env.MessageID)
	}
	ack, err := c.reg.Register(ctx, c.session(), d)
	if err != nil {
		if errors.Is(err, registration.ErrSessionStale) {
			return err
		}
		ae := apperr.As(err)
		return c.reply(protocol.TypeServiceRegisterAck, env.MessageID, protocol.RegisterAck{
			OK: false, Error: &protocol.ErrorBody{Code: ae.Code, Message: ae.Message, Retryable: ae.Retryable},
		})
	}
	return c.reply(protocol.TypeServiceRegisterAck, env.MessageID, ack)
}

func (c *Conn) onHeartbeat(ctx context.Context, env protocol.Envelope) error {
	var hb protocol.Heartbeat
	if err := json.Unmarshal(env.Data, &hb); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.heartbeat data", false, env.MessageID)
	}
	ack, err := c.reg.Heartbeat(ctx, c.session(), hb)
	if err != nil {
		if errors.Is(err, registration.ErrSessionStale) {
			return err
		}
		if errors.Is(err, registration.ErrNotRegistered) {
			return c.sendError(protocol.ErrInstanceNotRegistered, "instance is not registered; send service.register", false, env.MessageID)
		}
		ae := apperr.As(err)
		return c.sendError(ae.Code, ae.Message, ae.Retryable, env.MessageID)
	}
	return c.reply(protocol.TypeServiceHeartbeatAck, env.MessageID, ack)
}

func (c *Conn) onUnregister(ctx context.Context, env protocol.Envelope) error {
	var u protocol.Unregister
	if err := json.Unmarshal(env.Data, &u); err != nil {
		return c.sendError(protocol.ErrProtocolInvalidMessage, "invalid service.unregister data", false, env.MessageID)
	}
	if err := c.reg.Unregister(ctx, c.session(), u); err != nil {
		ae := apperr.As(err)
		return c.sendError(ae.Code, ae.Message, ae.Retryable, env.MessageID)
	}
	c.mu.Lock()
	c.normal = true
	c.mu.Unlock()
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
	body := protocol.SystemError{Code: code, Message: msg, Retryable: retryable}
	if ref != "" {
		body.RefMessageID = &ref
	}
	return c.reply(protocol.TypeSystemError, ref, body)
}

func (c *Conn) sendAuthFail(ae *apperr.Error) error {
	return c.reply(protocol.TypeAuthResponse, "", protocol.AuthResponse{
		OK: false, Error: &protocol.ErrorBody{Code: ae.Code, Message: ae.Message},
	})
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
	t := time.NewTicker(c.cfg.WebSocket.PingInterval.D())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-c.sendCh:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, c.cfg.WebSocket.WriteTimeout.D())
			err := c.ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.cfg.WebSocket.PingTimeout.D())
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
	normal := c.normal
	c.closed = true
	c.mu.Unlock()
	if sess != nil {
		c.hub.Unbind(sess.InstanceID, c)
		if !replaced {
			if _, err := c.reg.Disconnect(context.WithoutCancel(ctx), sess, normal); err != nil {
				c.log.Warn("disconnect update failed", "err", err)
			}
		}
	}
}

func remoteIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, "[") {
			return addr
		}
		return addr
	}
	return host
}
