package admin

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/GlassJob/job-center/internal/handler/middleware"
	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type ServiceHandler struct {
	Services *service.Services
	Store    *mysql.Store
}

func (h *ServiceHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Services.ListServices(c.Request.Context(), c.Query("keyword"), page, limit)
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "list services failed")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ServiceHandler) Get(c *gin.Context) {
	d, err := h.Services.GetService(c.Request.Context(), c.Param("serviceId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "service not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "get service failed")
		return
	}
	response.OK(c, d)
}

func (h *ServiceHandler) Create(c *gin.Context) {
	var in service.CreateServiceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	svc, err := h.Services.CreateService(c.Request.Context(), in)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "service_code already exists")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "create service failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "service.create",
		Permission:   "job.service.manage",
		ResourceType: "service",
		ResourceID:   svc.ID,
		ClientIP:     c.ClientIP(),
		After:        svc,
		Result:       "SUCCESS",
	})
	response.OK(c, svc)
}

func (h *ServiceHandler) Patch(c *gin.Context) {
	id := c.Param("serviceId")
	var in service.PatchServiceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	before, _ := h.Services.GetService(c.Request.Context(), id)
	svc, err := h.Services.PatchService(c.Request.Context(), id, in)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "service not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "patch service failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "service.update",
		Permission:   "job.service.manage",
		ResourceType: "service",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        svc,
		Result:       "SUCCESS",
	})
	response.OK(c, svc)
}

func parseHardQuery(c *gin.Context) bool {
	v := strings.TrimSpace(strings.ToLower(c.Query("hard")))
	return v == "1" || v == "true" || v == "yes"
}

func (h *ServiceHandler) Delete(c *gin.Context) {
	id := c.Param("serviceId")
	hard := parseHardQuery(c)
	before, _ := h.Services.GetService(c.Request.Context(), id)
	out, err := h.Services.DeleteService(c.Request.Context(), id, hard)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "service not found")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "delete service failed")
		return
	}
	action := "service.disable"
	if hard {
		action = "service.delete"
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       action,
		Permission:   "job.service.manage",
		ResourceType: "service",
		ResourceID:   id,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.Service,
		Result:       "SUCCESS",
	})
	if hard {
		response.OK(c, gin.H{"id": id, "hard": true, "deleted": true})
		return
	}
	response.OK(c, gin.H{"id": id, "hard": false, "deleted": false, "service": out.Service})
}