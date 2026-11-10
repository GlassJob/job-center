package gateway

import (
	"log/slog"
	"net/http"

	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/GlassJob/job-center/pkg/protocol"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// Server Executor WebSocket Gateway（协议 1.1）。
type Server struct {
	hub        *Hub
	exec       *service.Executor
	executions *service.Executions
	cfg        config.WebSocketConfig
	log        *slog.Logger
}

func NewServer(hub *Hub, exec *service.Executor, executions *service.Executions, cfg config.WebSocketConfig, log *slog.Logger) *Server {
	if hub == nil {
		hub = NewHub()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{hub: hub, exec: exec, executions: executions, cfg: cfg, log: log}
}

func (s *Server) Hub() *Hub { return s.hub }

// GinHandler 挂载到 Gin：GET /v1/executor/ws
func (s *Server) GinHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.accept(c.Writer, c.Request, c.ClientIP())
	}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.accept(w, r, remoteIP(r.RemoteAddr))
	})
}

func (s *Server) accept(w http.ResponseWriter, r *http.Request, peerIP string) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{protocol.Subprotocol},
		InsecureSkipVerify: true, // 开发环境；生产由前置代理校验 Origin
	})
	if err != nil {
		s.log.Warn("websocket accept failed", "err", err)
		return
	}
	if ws.Subprotocol() != protocol.Subprotocol {
		_ = ws.Close(protocol.CloseUnsupported, "Sec-WebSocket-Protocol must be "+protocol.Subprotocol)
		return
	}
	ws.SetReadLimit(s.cfg.MaxMessageBytes)
	if peerIP == "" {
		peerIP = remoteIP(r.RemoteAddr)
	}
	c := newConn(ws, peerIP, s.hub, s.exec, s.executions, s.cfg, s.log)
	c.Run(r.Context())
}
