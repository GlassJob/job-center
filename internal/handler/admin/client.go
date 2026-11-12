package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/handler/middleware"
	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type ClientHandler struct {
	Clients *service.Clients
	Store   *mysql.Store
}

func (h *ClientHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Clients.List(c.Request.Context(), c.Query("keyword"), c.Query("status"), page, limit)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询客户端失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ClientHandler) Get(c *gin.Context) {
	v, err := h.Clients.Get(c.Request.Context(), c.Param("clientId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "客户端不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取客户端失败")
		return
	}
	response.OK(c, v)
}

func (h *ClientHandler) Create(c *gin.Context) {
	var in service.CreateClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	createdBy := middleware.OperatorID(c.Request.Context())
	if createdBy == "" {
		createdBy = middleware.ClientID(c.Request.Context())
	}
	out, err := h.Clients.Create(c.Request.Context(), in, createdBy)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "客户端名称已存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "创建客户端失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "client.create",
		Permission:   "job.client.manage",
		ResourceType: "client",
		ResourceID:   out.ID,
		ClientIP:     c.ClientIP(),
		After:        out.ClientView,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ClientHandler) Patch(c *gin.Context) {
	id := c.Param("clientId")
	var in service.PatchClientInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	before, _ := h.Clients.Get(c.Request.Context(), id)
	out, err := h.Clients.Patch(c.Request.Context(), id, in)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "客户端不存在")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "客户端名称已存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "更新客户端失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "client.update",
		Permission:   "job.client.manage",
		ResourceType: "client",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ClientHandler) Rotate(c *gin.Context) {
	id := c.Param("clientId")
	before, _ := h.Clients.Get(c.Request.Context(), id)
	out, err := h.Clients.Rotate(c.Request.Context(), id)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "客户端不存在")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "轮换冲突")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "轮换客户端失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "client.rotate",
		Permission:   "job.client.manage",
		ResourceType: "client",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.ClientView,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ClientHandler) Revoke(c *gin.Context) {
	id := c.Param("clientId")
	before, _ := h.Clients.Get(c.Request.Context(), id)
	revokedBy := middleware.OperatorID(c.Request.Context())
	if revokedBy == "" {
		revokedBy = middleware.ClientID(c.Request.Context())
	}
	out, err := h.Clients.Revoke(c.Request.Context(), id, revokedBy)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "客户端不存在")
		return
	}
	if errors.Is(err, service.ErrForbidden) {
		response.Fail(c, http.StatusForbidden, 40300, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "吊销客户端失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "client.revoke",
		Permission:   "job.client.manage",
		ResourceType: "client",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ClientHandler) Delete(c *gin.Context) {
	id := c.Param("clientId")
	hard := parseHardQuery(c)
	before, _ := h.Clients.Get(c.Request.Context(), id)
	revokedBy := middleware.OperatorID(c.Request.Context())
	if revokedBy == "" {
		revokedBy = middleware.ClientID(c.Request.Context())
	}
	out, err := h.Clients.Delete(c.Request.Context(), id, revokedBy, hard)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "客户端不存在")
		return
	}
	if errors.Is(err, service.ErrForbidden) {
		response.Fail(c, http.StatusForbidden, 40300, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "删除客户端失败")
		return
	}
	action := "client.revoke"
	if hard {
		action = "client.delete"
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       action,
		Permission:   "job.client.manage",
		ResourceType: "client",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.Client,
		Result:       "SUCCESS",
	})
	if hard {
		response.OK(c, gin.H{"id": id, "hard": true, "deleted": true})
		return
	}
	response.OK(c, gin.H{"id": id, "hard": false, "deleted": false, "client": out.Client})
}
