package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/handler/middleware"
	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type ExecutionHandler struct {
	Executions *service.Executions
	Store      *mysql.Store
}

func (h *ExecutionHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Executions.List(
		c.Request.Context(),
		c.Query("keyword"),
		c.Query("service_id"),
		c.Query("job_id"),
		c.Query("status"),
		c.Query("instance_id"),
		c.Query("created_from"),
		c.Query("created_to"),
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
		response.Fail(c, http.StatusInternalServerError, 50000, "查询执行失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ExecutionHandler) ListByJob(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Executions.ListByJob(
		c.Request.Context(),
		c.Param("serviceId"),
		c.Param("jobId"),
		c.Query("keyword"),
		c.Query("status"),
		c.Query("instance_id"),
		c.Query("created_from"),
		c.Query("created_to"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "任务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询执行失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *ExecutionHandler) ListLogs(c *gin.Context) {
	after, _ := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	list, err := h.Executions.ListLogs(
		c.Request.Context(),
		c.Param("serviceId"),
		c.Param("jobId"),
		c.Param("executionId"),
		c.Query("attempt_id"),
		after,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "执行不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询执行日志失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": len(list)})
}

func (h *ExecutionHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.TriggerExecutionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "请求体无效")
		return
	}
	triggeredBy := fmt.Sprintf("admin:%s", middleware.OperatorID(c.Request.Context()))
	out, err := h.Executions.ManualTrigger(c.Request.Context(), serviceID, jobID, triggeredBy, in)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "任务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "触发执行失败")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "execution.trigger",
		Permission:   "job.job.manage",
		ResourceType: "execution",
		ResourceID:   out.ExecutionID,
		ClientIP:     c.ClientIP(),
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *ExecutionHandler) Get(c *gin.Context) {
	v, err := h.Executions.Get(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Param("executionId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "执行不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取执行失败")
		return
	}
	response.OK(c, v)
}

func (h *ExecutionHandler) Cancel(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	executionID := c.Param("executionId")
	var in service.CancelExecutionInput
	_ = c.ShouldBindJSON(&in)
	requestedBy := fmt.Sprintf("admin:%s", middleware.OperatorID(c.Request.Context()))
	out, err := h.Executions.Cancel(c.Request.Context(), serviceID, jobID, executionID, requestedBy, in)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "执行不存在")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "取消执行失败："+err.Error())
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "execution.cancel",
		Permission:   "job.job.manage",
		ResourceType: "execution",
		ResourceID:   out.ExecutionID,
		ClientIP:     c.ClientIP(),
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}
