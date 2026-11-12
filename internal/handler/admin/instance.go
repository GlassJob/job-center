package admin

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/GlassJob/job-center/internal/handler/response"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type InstanceHandler struct {
	Instances *service.Instances
}

func (h *InstanceHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Instances.List(
		c.Request.Context(),
		c.Param("serviceId"),
		c.Query("keyword"),
		c.Query("status"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "服务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询实例失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *InstanceHandler) ListGlobal(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	list, total, err := h.Instances.ListGlobal(
		c.Request.Context(),
		c.Query("service_id"),
		c.Query("keyword"),
		c.Query("status"),
		page,
		limit,
	)
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "服务不存在")
		return
	}
	if errors.Is(err, service.ErrInvalidInput) {
		response.Fail(c, http.StatusBadRequest, 40000, err.Error())
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "查询实例失败")
		return
	}
	response.OK(c, gin.H{"list": list, "total": total})
}

func (h *InstanceHandler) Get(c *gin.Context) {
	v, err := h.Instances.Get(c.Request.Context(), c.Param("serviceId"), c.Param("instanceId"))
	if errors.Is(err, service.ErrNotFound) {
		response.Fail(c, http.StatusNotFound, 40400, "实例不存在")
		return
	}
	if err != nil {
		response.Fail(c, http.StatusInternalServerError, 50000, "获取实例失败")
		return
	}
	response.OK(c, v)
}
