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
		response.Fail(c, http.StatusNotFound, 40400, "任务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询调度失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ScheduleHandler) Get(c *gin.Context) {
	v, err := h.Schedules.Get(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Param("scheduleId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "调度不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取调度失败")
		return
	}
	response.OK(c, v)
}

func (h *ScheduleHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.CreateScheduleInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	out, err := h.Schedules.Create(c.Request.Context(), serviceID, jobID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "任务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "调度编码已存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "创建调度失败")
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
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	before, _ := h.Schedules.Get(c.Request.Context(), serviceID, jobID, scheduleID)
	out, err := h.Schedules.Patch(c.Request.Context(), serviceID, jobID, scheduleID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "版本冲突，请刷新后重试")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "调度不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "更新调度失败")
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

func (h *ScheduleHandler) ListTriggers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Schedules.ListTriggers(
		c.Request.Context(),
		c.Query("schedule_id"),
		c.Query("job_id"),
		c.Query("service_id"),
		c.Query("status"),
		c.Query("fire_from"),
		c.Query("fire_to"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, err.Error())
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询触点失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ScheduleHandler) ListScheduleTriggers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Schedules.ListTriggersBySchedule(
		c.Request.Context(),
		c.Param("serviceId"),
		c.Param("jobId"),
		c.Param("scheduleId"),
		c.Query("status"),
		c.Query("fire_from"),
		c.Query("fire_to"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "调度不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询触点失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ScheduleHandler) Regenerate(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	scheduleID := c.Param("scheduleId")
	out, err := h.Schedules.Regenerate(c.Request.Context(), serviceID, jobID, scheduleID)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "调度不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "重生触点失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "schedule.regenerate",
		Permission:   "job.schedule.manage",
		ResourceType: "schedule",
		ResourceID:   scheduleID,
		ClientIP:     c.ClientIP(),
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
		response.Fail(c, http.StatusNotFound, 40400, "调度不存在")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "删除调度失败")
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
