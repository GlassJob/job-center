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

type CredentialHandler struct {
	Credentials *service.Credentials
	Store       *mysql.Store
}

func (h *CredentialHandler) List(c *gin.Context) {
	serviceID := c.Param("serviceId")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Credentials.List(c.Request.Context(), serviceID, c.Query("keyword"), c.Query("status"), page, limit)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "服务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询凭据失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *CredentialHandler) Get(c *gin.Context) {
	v, err := h.Credentials.Get(c.Request.Context(), c.Param("serviceId"), c.Param("credentialId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "凭据不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取凭据失败")
		return
	}
	response.OK(c, v)
}

func (h *CredentialHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	var in service.CreateCredentialInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	createdBy := middleware.OperatorID(c.Request.Context())
	if createdBy == "" {
		createdBy = middleware.ClientID(c.Request.Context())
	}
	out, err := h.Credentials.Create(c.Request.Context(), serviceID, in, createdBy)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "服务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "凭据名称已存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "创建凭据失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "credential.create",
		Permission:   "job.credential.manage",
		ResourceType: "credential",
		ResourceID:   out.ID,
		ClientIP:     c.ClientIP(),
		After:        out.CredentialView,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *CredentialHandler) Patch(c *gin.Context) {
	serviceID := c.Param("serviceId")
	id := c.Param("credentialId")
	var in service.PatchCredentialInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	before, _ := h.Credentials.Get(c.Request.Context(), serviceID, id)
	out, err := h.Credentials.Patch(c.Request.Context(), serviceID, id, in)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "凭据名称已存在")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "凭据不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "更新凭据失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "credential.update",
		Permission:   "job.credential.manage",
		ResourceType: "credential",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *CredentialHandler) Rotate(c *gin.Context) {
	serviceID := c.Param("serviceId")
	id := c.Param("credentialId")
	before, _ := h.Credentials.Get(c.Request.Context(), serviceID, id)
	out, err := h.Credentials.Rotate(c.Request.Context(), serviceID, id)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "凭据不存在")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "轮换冲突")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "轮换凭据失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "credential.rotate",
		Permission:   "job.credential.manage",
		ResourceType: "credential",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.CredentialView,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *CredentialHandler) Delete(c *gin.Context) {
	serviceID := c.Param("serviceId")
	id := c.Param("credentialId")
	hard := parseHardQuery(c)
	before, _ := h.Credentials.Get(c.Request.Context(), serviceID, id)
	revokedBy := middleware.OperatorID(c.Request.Context())
	if revokedBy == "" {
		revokedBy = middleware.ClientID(c.Request.Context())
	}
	out, err := h.Credentials.Delete(c.Request.Context(), serviceID, id, revokedBy, hard)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "凭据不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "删除凭据失败")
		return
	}
	action := "credential.revoke"
	if hard {
		action = "credential.delete"
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       action,
		Permission:   "job.credential.manage",
		ResourceType: "credential",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.Credential,
		Result:       "SUCCESS",
	})
	if hard {
		response.OK(c, gin.H{"id": id, "hard": true, "deleted": true})
		return
	}
	response.OK(c, gin.H{"id": id, "hard": false, "deleted": false, "credential": out.Credential})
}
