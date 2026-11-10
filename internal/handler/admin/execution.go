package admin

import (
	"errors"
	"fmt"
	"net/http"

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

func (h *ExecutionHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.TriggerExecutionInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	triggeredBy := fmt.Sprintf("admin:%s", middleware.OperatorID(c.Request.Context()))
	out, err := h.Executions.ManualTrigger(c.Request.Context(), serviceID, jobID, triggeredBy, in)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "trigger execution failed")
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
		response.Fail(c, http.StatusNotFound, 40400, "execution not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "get execution failed")
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
		response.Fail(c, http.StatusNotFound, 40400, "execution not found")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "cancel execution failed")
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
