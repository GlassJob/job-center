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

type JobHandler struct {
	Jobs  *service.Jobs
	Store *mysql.Store
}

func (h *JobHandler) List(c *gin.Context) {
	serviceID := c.Param("serviceId")
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Jobs.List(c.Request.Context(), serviceID, c.Query("keyword"), c.Query("status"), c.Query("enabled"), page, limit)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "service not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "list jobs failed")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *JobHandler) Get(c *gin.Context) {
	v, err := h.Jobs.Get(c.Request.Context(), c.Param("serviceId"), c.Param("jobId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "get job failed")
		return
	}
	response.OK(c, v)
}

func (h *JobHandler) Create(c *gin.Context) {
	serviceID := c.Param("serviceId")
	var in service.CreateJobInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	out, err := h.Jobs.Create(c.Request.Context(), serviceID, in)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "service not found")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, "job code already exists")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "create job failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "job.create",
		Permission:   "job.job.manage",
		ResourceType: "job",
		ResourceID:   out.ID,
		ClientIP:     c.ClientIP(),
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *JobHandler) Patch(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	var in service.PatchJobInput
	if err := c.ShouldBindJSON(&in); err != nil {
		response.Fail(c, http.StatusBadRequest, 40000, "invalid body")
		return
	}
	before, _ := h.Jobs.Get(c.Request.Context(), serviceID, jobID)
	out, err := h.Jobs.Patch(c.Request.Context(), serviceID, jobID, in)
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "patch job failed")
		return
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       "job.update",
		Permission:   "job.job.manage",
		ResourceType: "job",
		ResourceID:   jobID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out,
		Result:       "SUCCESS",
	})
	response.OK(c, out)
}

func (h *JobHandler) Delete(c *gin.Context) {
	serviceID := c.Param("serviceId")
	jobID := c.Param("jobId")
	hard := parseHardQuery(c)
	before, _ := h.Jobs.Get(c.Request.Context(), serviceID, jobID)
	out, err := h.Jobs.Delete(c.Request.Context(), serviceID, jobID, hard)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "job not found")
		return
	}
	if errors.Is(err, service.ErrConflict) {
		response.Fail(c, http.StatusConflict, 40900, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "delete job failed")
		return
	}
	action := "job.disable"
	if hard {
		action = "job.delete"
	}
	_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
		ClientID:     middleware.ClientID(c.Request.Context()),
		OperatorID:   middleware.OperatorID(c.Request.Context()),
		OperatorName: middleware.OperatorName(c.Request.Context()),
		Action:       action,
		Permission:   "job.job.manage",
		ResourceType: "job",
		ResourceID:   jobID,
		ClientIP:     c.ClientIP(),
		Before:       before,
		After:        out.Job,
		Result:       "SUCCESS",
	})
	if hard {
		response.OK(c, gin.H{"id": jobID, "hard": true, "deleted": true})
		return
	}
	response.OK(c, gin.H{"id": jobID, "hard": false, "deleted": false, "job": out.Job})
}
