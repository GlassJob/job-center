package executor

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/GlassJob/job-center/pkg/protocol"
	"github.com/gin-gonic/gin"
)

const HeaderCredential = "X-JC-Credential"
const HeaderSessionID = "X-JC-Session-Id"
const HeaderSessionEpoch = "X-JC-Session-Epoch"
const HeaderInstanceID = "X-JC-Instance-Id"

type Handler struct {
	Executor *service.Executor
}

func (h *Handler) Register(c *gin.Context) {
	token := credentialToken(c)
	if token == "" {
		response.Fail(c, http.StatusUnauthorized, 40100, "missing X-JC-Credential")
		return
	}
	var req protocol.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	out, err := h.Executor.Register(c.Request.Context(), token, req, c.ClientIP())
	if errors.Is(err, service.ErrUnauthorized) {
		response.Fail(c, http.StatusUnauthorized, 40100, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "register failed")
		return
	}
	response.OK(c, out)
}

func (h *Handler) Heartbeat(c *gin.Context) {
	token, instanceID, sessionID, epoch, ok := sessionHeaders(c)
	if !ok {
		return
	}
	var req protocol.HeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	out, err := h.Executor.Heartbeat(c.Request.Context(), token, instanceID, sessionID, epoch, req)
	writeSessionErr(c, err)
	if err != nil {
		return
	}
	response.OK(c, out)
}

func (h *Handler) Unregister(c *gin.Context) {
	token, instanceID, sessionID, epoch, ok := sessionHeaders(c)
	if !ok {
		return
	}
	var req protocol.UnregisterRequest
	_ = c.ShouldBindJSON(&req)
	err := h.Executor.Unregister(c.Request.Context(), token, instanceID, sessionID, epoch, req)
	writeSessionErr(c, err)
	if err != nil {
		return
	}
	response.OK(c, gin.H{"ok": true, "instance_status": "DRAINING"})
}

func credentialToken(c *gin.Context) string {
	tok := strings.TrimSpace(c.GetHeader(HeaderCredential))
	if tok != "" {
		return tok
	}
	authz := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
		return strings.TrimSpace(authz[7:])
	}
	return ""
}

func sessionHeaders(c *gin.Context) (token, instanceID, sessionID string, epoch uint64, ok bool) {
	token = credentialToken(c)
	if token == "" {
		response.Fail(c, http.StatusUnauthorized, 40100, "missing X-JC-Credential")
		return
	}
	instanceID = strings.TrimSpace(c.GetHeader(HeaderInstanceID))
	sessionID = strings.TrimSpace(c.GetHeader(HeaderSessionID))
	epochRaw := strings.TrimSpace(c.GetHeader(HeaderSessionEpoch))
	if instanceID == "" || sessionID == "" || epochRaw == "" {
		response.Fail(c, http.StatusBadRequest, 40000, "missing session headers")
		return
	}
	v, err := strconv.ParseUint(epochRaw, 10, 64)
	if err != nil || v == 0 {
		response.Fail(c, http.StatusBadRequest, 40000, "bad session epoch")
		return
	}
	return token, instanceID, sessionID, v, true
}

func writeSessionErr(c *gin.Context, err error) {
	if err == nil {
		return
	}
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		response.Fail(c, http.StatusUnauthorized, 40100, err.Error())
	case errors.Is(err, service.ErrSessionStale):
		response.Fail(c, http.StatusConflict, 40900, "session replaced")
	case errors.Is(err, service.ErrNotFound):
		response.Fail(c, http.StatusNotFound, 40400, "instance not found")
	default:
		response.Fail(c, http.StatusInternalServerError, 50000, "request failed")
	}
}
