package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type AuditHandler struct {
	Audits *service.Audits
}

func (h *AuditHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Audits.List(
		c.Request.Context(),
		c.Query("client_id"),
		c.Query("operator_id"),
		c.Query("action"),
		c.Query("resource_type"),
		c.Query("resource_id"),
		c.Query("result"),
		c.Query("created_from"),
		c.Query("created_to"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询审计失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *AuditHandler) Get(c *gin.Context) {
	v, err := h.Audits.Get(c.Request.Context(), c.Param("auditId"))
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "审计记录不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取审计失败")
		return
	}
	response.OK(c, v)
}
