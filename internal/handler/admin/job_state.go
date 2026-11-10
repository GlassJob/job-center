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

type JobStateHandler struct {
	States *service.JobStates
	Store  *mysql.Store
}

func adminUpdatedBy(c *gin.Context) string {
	op := middleware.OperatorID(c.Request.Context())
	if op == "" {
		op = middleware.ClientID(c.Request.Context())
	}
	if op == "" {
		return "admin"
	}
	return "admin:" + op
}

func (h *JobStateHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.States.List(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Query("keyword"), page, limit)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "list states failed")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *JobStateHandler) Get(c *gin.Context) {
	v, err := h.States.Get(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"), c.Param("stateId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "state not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "get state failed")
		return
	}
	response.OK(c, v)
}

func (h *JobStateHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.CreateJobStateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	out, err := h.States.Create(c.Request.Context(), serviceID, jobID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "key already exists")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "create state failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "state.create",
		Permission:   "job.state.manage",
		ResourceType: "job_state",
		ResourceID:   out.ID,
		ClientIP:     c.ClientIP(),
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *JobStateHandler) Patch(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	stateID := c.Param("stateId")
	var in service.PatchJobStateInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	before, _ := h.States.Get(c.Request.Context(), serviceID, jobID, stateID)
	out, err := h.States.Patch(c.Request.Context(), serviceID, jobID, stateID, in, adminUpdatedBy(c))
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "version mismatch")
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "state not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "patch state failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "state.update",
		Permission:   "job.state.manage",
		ResourceType: "job_state",
		ResourceID:   stateID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *JobStateHandler) Delete(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	stateID := c.Param("stateId")
	before, _ := h.States.Get(c.Request.Context(), serviceID, jobID, stateID)
	err := h.States.Delete(c.Request.Context(), serviceID, jobID, stateID)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "state not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "delete state failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "state.delete",
		Permission:   "job.state.manage",
		ResourceType: "job_state",
		ResourceID:   stateID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		Result:       "SUCCESS",
	})
	response.OK(c, gin.H{"id": stateID, "deleted": true})
}
