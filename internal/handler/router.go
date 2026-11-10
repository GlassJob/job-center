package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/gateway"
	"github.com/GlassJob/job-center/internal/handler/admin"
	"github.com/GlassJob/job-center/internal/handler/executor"
	"github.com/GlassJob/job-center/internal/handler/middleware"
	"github.com/GlassJob/job-center/internal/pkg/auth"
	"github.com/GlassJob/job-center/internal/repository/mysql"
	"github.com/GlassJob/job-center/internal/service"
	"github.com/gin-gonic/gin"
)

type Deps struct {
	Store     *mysql.Store
	Pepper    string
	Skew      time.Duration
	NodeID    string
	WebSocket config.WebSocketConfig
	Log       *slog.Logger
}

func NewRouter(d Deps) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), gin.Logger())
	r.GET("/health/live", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/health/ready", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	authCfg := middleware.AuthConfig{
		Store:  d.Store,
		Pepper: d.Pepper,
		Skew:   d.Skew,
		Nonces: auth.NewNonceCache(10 * time.Minute),
	}
	services := service.NewServices(d.Store)
	clients := service.NewClients(d.Store, d.Pepper)
	creds := service.NewCredentials(d.Store, d.Pepper)
	jobs := service.NewJobs(d.Store)
	states := service.NewJobStates(d.Store)
	schedules := service.NewSchedules(d.Store)
	audits := service.NewAudits(d.Store)
	exec := service.NewExecutor(d.Store, d.Pepper, d.NodeID, d.Skew)
	gw := gateway.NewServer(exec, d.WebSocket, d.Log)
	svcH := &admin.ServiceHandler{Services: services, Store: d.Store}
	cliH := &admin.ClientHandler{Clients: clients, Store: d.Store}
	credH := &admin.CredentialHandler{Credentials: creds, Store: d.Store}
	jobH := &admin.JobHandler{Jobs: jobs, Store: d.Store}
	stateH := &admin.JobStateHandler{States: states, Store: d.Store}
	schH := &admin.ScheduleHandler{Schedules: schedules, Store: d.Store}
	auditH := &admin.AuditHandler{Audits: audits}
	execH := &executor.Handler{Executor: exec}

	adminAPI := r.Group("/v1/admin")
	{
		adminAPI.GET("/services", middleware.Auth(authCfg, "job.service.view"), svcH.List)
		adminAPI.GET("/services/:serviceId", middleware.Auth(authCfg, "job.service.view"), svcH.Get)
		adminAPI.POST("/services", middleware.Auth(authCfg, "job.service.manage"), svcH.Create)
		adminAPI.PATCH("/services/:serviceId", middleware.Auth(authCfg, "job.service.manage"), svcH.Patch)
		adminAPI.DELETE("/services/:serviceId", middleware.Auth(authCfg, "job.service.manage"), svcH.Delete)

		adminAPI.GET("/services/:serviceId/credentials", middleware.Auth(authCfg, "job.credential.view"), credH.List)
		adminAPI.POST("/services/:serviceId/credentials", middleware.Auth(authCfg, "job.credential.manage"), credH.Create)
		adminAPI.GET("/services/:serviceId/credentials/:credentialId", middleware.Auth(authCfg, "job.credential.view"), credH.Get)
		adminAPI.PATCH("/services/:serviceId/credentials/:credentialId", middleware.Auth(authCfg, "job.credential.manage"), credH.Patch)
		adminAPI.POST("/services/:serviceId/credentials/:credentialId/rotate", middleware.Auth(authCfg, "job.credential.manage"), credH.Rotate)
		adminAPI.DELETE("/services/:serviceId/credentials/:credentialId", middleware.Auth(authCfg, "job.credential.manage"), credH.Delete)

		adminAPI.GET("/services/:serviceId/jobs", middleware.Auth(authCfg, "job.job.view"), jobH.List)
		adminAPI.POST("/services/:serviceId/jobs", middleware.Auth(authCfg, "job.job.manage"), jobH.Create)
		adminAPI.GET("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.view"), jobH.Get)
		adminAPI.PATCH("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.manage"), jobH.Patch)
		adminAPI.DELETE("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.manage"), jobH.Delete)

		adminAPI.GET("/services/:serviceId/jobs/:jobId/states", middleware.Auth(authCfg, "job.state.view"), stateH.List)
		adminAPI.POST("/services/:serviceId/jobs/:jobId/states", middleware.Auth(authCfg, "job.state.manage"), stateH.Create)
		adminAPI.GET("/services/:serviceId/jobs/:jobId/states/:stateId", middleware.Auth(authCfg, "job.state.view"), stateH.Get)
		adminAPI.PATCH("/services/:serviceId/jobs/:jobId/states/:stateId", middleware.Auth(authCfg, "job.state.manage"), stateH.Patch)
		adminAPI.DELETE("/services/:serviceId/jobs/:jobId/states/:stateId", middleware.Auth(authCfg, "job.state.manage"), stateH.Delete)

		adminAPI.GET("/services/:serviceId/jobs/:jobId/schedules", middleware.Auth(authCfg, "job.schedule.view"), schH.List)
		adminAPI.POST("/services/:serviceId/jobs/:jobId/schedules", middleware.Auth(authCfg, "job.schedule.manage"), schH.Create)
		adminAPI.GET("/services/:serviceId/jobs/:jobId/schedules/:scheduleId", middleware.Auth(authCfg, "job.schedule.view"), schH.Get)
		adminAPI.PATCH("/services/:serviceId/jobs/:jobId/schedules/:scheduleId", middleware.Auth(authCfg, "job.schedule.manage"), schH.Patch)
		adminAPI.DELETE("/services/:serviceId/jobs/:jobId/schedules/:scheduleId", middleware.Auth(authCfg, "job.schedule.manage"), schH.Delete)

		adminAPI.GET("/clients", middleware.Auth(authCfg, "job.client.view"), cliH.List)
		adminAPI.GET("/clients/:clientId", middleware.Auth(authCfg, "job.client.view"), cliH.Get)
		adminAPI.POST("/clients", middleware.Auth(authCfg, "job.client.manage"), cliH.Create)
		adminAPI.PATCH("/clients/:clientId", middleware.Auth(authCfg, "job.client.manage"), cliH.Patch)
		adminAPI.POST("/clients/:clientId/rotate", middleware.Auth(authCfg, "job.client.manage"), cliH.Rotate)
		adminAPI.POST("/clients/:clientId/revoke", middleware.Auth(authCfg, "job.client.manage"), cliH.Revoke)
		adminAPI.DELETE("/clients/:clientId", middleware.Auth(authCfg, "job.client.manage"), cliH.Delete)

		adminAPI.GET("/audits", middleware.Auth(authCfg, "job.audit.view"), auditH.List)
		adminAPI.GET("/audits/:auditId", middleware.Auth(authCfg, "job.audit.view"), auditH.Get)
	}

	execAPI := r.Group("/v1/executor")
	{
		execAPI.GET("/ws", gw.GinHandler())
		execAPI.POST("/register", execH.Register)
		execAPI.POST("/heartbeat", execH.Heartbeat)
		execAPI.POST("/unregister", execH.Unregister)
	}
	return r
}
