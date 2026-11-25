package admin

import (
	"net/http"

	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/scheduler"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type OverviewHandler struct {
	Overview  *service.Overview
	Scheduler *scheduler.Scheduler
}

func disabledSchedulerStatus() scheduler.Status {
	loop := scheduler.LoopStatus{Phase: scheduler.PhaseDisabled}
	return scheduler.Status{
		Enabled:  false,
		Claim:    loop,
		Generate: loop,
		Queued:   loop,
	}
}

func (h *OverviewHandler) Get(c *gin.Context) {
	v, err := h.Overview.Get(c.Request.Context())
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取概览失败")
		return
	}
	sch := disabledSchedulerStatus()
	if h.Scheduler != nil {
		sch = h.Scheduler.Status()
	}
	response.OK(c, struct {
		model.Overview
		Scheduler scheduler.Status `json:"scheduler"`
	}{Overview: *v, Scheduler: sch})
}
