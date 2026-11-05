package websocket

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/GlassJob/job-center/internal/application/registration"
	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/protocol"
	"github.com/coder/websocket"
)

type Server struct {
	hub *Hub
	reg *registration.Service
	cfg *config.Config
	val *protocol.Validator
	log *slog.Logger
}

func NewServer(reg *registration.Service, cfg *config.Config, val *protocol.Validator, log *slog.Logger) *Server {
	return &Server{hub: NewHub(), reg: reg, cfg: cfg, val: val, log: log}
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serveWS)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{protocol.Subprotocol},
		InsecureSkipVerify: true, // 开发环境；生产由前置 nginx 处理源
	})
	if err != nil {
		s.log.Warn("websocket accept failed", "err", err)
		return
	}
	if ws.Subprotocol() != protocol.Subprotocol {
		_ = ws.Close(protocol.CloseUnsupported, "Sec-WebSocket-Protocol must be "+protocol.Subprotocol)
		return
	}
	ws.SetReadLimit(s.cfg.WebSocket.MaxMessageBytes)
	c := newConn(ws, remoteIP(r.RemoteAddr), s.hub, s.reg, s.cfg, s.val, s.log)
	c.Run(r.Context())
}

// Mount 把 /v1/executor/ws 挂到 mux。
func (s *Server) Mount(mux *http.ServeMux) {
	mux.Handle("/v1/executor/ws", s.Handler())
}

// WaitIdle is a hook for tests.
func (s *Server) Context() context.Context { return context.Background() }
