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

type ScheduleHandler struct {
	Schedules *service.Schedules
	Store     *mysql.Store
}

func (h *ScheduleHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Schedules.List(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Query("keyword"), c.Query("enabled"), page, limit)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "list schedules failed")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ScheduleHandler) Get(c *gin.Context) {
	v, err := h.Schedules.Get(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Param("scheduleId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "schedule not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "get schedule failed")
		return
	}
	response.OK(c, v)
}

func (h *ScheduleHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.CreateScheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	out, err := h.Schedules.Create(c.Request.Context(), serviceID, jobID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "schedule code already exists")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "create schedule failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "schedule.create",
		Permission:   "job.schedule.manage",
		ResourceType: "schedule",
		ResourceID:   out.ID,
		ClientIP:     c.ClientIP(),
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ScheduleHandler) Patch(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	scheduleID := c.Param("scheduleId")
	var in service.PatchScheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	before, _ := h.Schedules.Get(c.Request.Context(), serviceID, jobID, scheduleID)
	out, err := h.Schedules.Patch(c.Request.Context(), serviceID, jobID, scheduleID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "version mismatch")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "schedule not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "patch schedule failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "schedule.update",
		Permission:   "job.schedule.manage",
		ResourceType: "schedule",
		ResourceID:   scheduleID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ScheduleHandler) Delete(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	scheduleID := c.Param("scheduleId")
	hard := parseHardQuery(c)
	before, _ := h.Schedules.Get(c.Request.Context(), serviceID, jobID, scheduleID)
	out, err := h.Schedules.Delete(c.Request.Context(), serviceID, jobID, scheduleID, adminUpdatedBy(c), hard)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "schedule not found")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "delete schedule failed")
		return
	}
	action := "schedule.disable"
	if hard {
		action = "schedule.delete"
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       action,
		Permission:   "job.schedule.manage",
		ResourceType: "schedule",
		ResourceID:   scheduleID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.Schedule,
		Result:       "SUCCESS",
	})
	if hard {
		response.OK(c, gin.H{"id": scheduleID, "hard": true, "deleted": true})
		return
	}
	response.OK(c, gin.H{"id": scheduleID, "hard": false, "deleted": false, "schedule": out.Schedule})
}
