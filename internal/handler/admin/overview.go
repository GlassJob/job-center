package admin

import (
	"net/http"

	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type OverviewHandler struct {
	Overview *service.Overview
}

func (h *OverviewHandler) Get(c *gin.Context) {
	v, err := h.Overview.Get(c.Request.Context())
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取概览失败")
		return
	}
	response.OK(c, v)
}
