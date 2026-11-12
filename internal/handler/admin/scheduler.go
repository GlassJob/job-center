package admin

import (
	"net/http"

	"github.com/GlassJob/job-center/internal/handler/middleware"
	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/scheduler"
	"github.com/gin-gonic/gin"
)

type SchedulerHandler struct {
	Scheduler *scheduler.Scheduler
	Store     *mysql.Store
}

func (h *SchedulerHandler) Get(c *gin.Context) {
	if h.Scheduler == nil {
		response.Fail(c, http.StatusServiceUnavailable, 50300, "调度器未启用")
		return
	}
	response.OK(c, h.Scheduler.Status())
}

func (h *SchedulerHandler) RegenerateAll(c *gin.Context) {
	if h.Scheduler == nil {
		response.Fail(c, http.StatusServiceUnavailable, 50300, "调度器未启用")
		return
	}
	out := h.Scheduler.RegenerateAll(c.Request.Context())
	if h.Store != nil {
		_ = h.Store.InsertAudit(c.Request.Context(), mysql.AuditRow{
			ClientID:     middleware.ClientID(c.Request.Context()),
			OperatorID:   middleware.OperatorID(c.Request.Context()),
			OperatorName: middleware.OperatorName(c.Request.Context()),
			Action:       "scheduler.regenerate",
			Permission:   "job.schedule.manage",
			ResourceType: "scheduler",
			ResourceID:   "",
			ClientIP:     c.ClientIP(),
			After:        out,
			Result:       "SUCCESS",
		})
	}
	response.OK(c, out)
}
