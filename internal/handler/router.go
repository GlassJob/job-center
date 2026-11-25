package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/GlassJob/job-center/internal/config"
	"github.com/GlassJob/job-center/internal/gateway"
	"github.com/GlassJob/job-center/internal/handler/admin"
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

// App HTTP 路由与可复用的运行时依赖（供 scheduler 共用同一 Executions/Hub）。
type App struct {
	Engine           *gin.Engine
	Hub              *gateway.Hub
	Executions       *service.Executions
	Schedules        *service.Schedules
	SchedulerHandler *admin.SchedulerHandler
	OverviewHandler  *admin.OverviewHandler
}

func NewRouter(d Deps) *App {
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
	instances := service.NewInstances(d.Store)
	audits := service.NewAudits(d.Store)
	overview := service.NewOverview(d.Store)
	exec := service.NewExecutor(d.Store, d.Pepper, d.NodeID, d.Skew)
	hub := gateway.NewHub()
	executions := service.NewExecutions(d.Store, hub, d.WebSocket.LeaseTTLDuration())
	gw := gateway.NewServer(hub, exec, executions, d.WebSocket, d.Log)
	svcH := &admin.ServiceHandler{Services: services, Store: d.Store}
	cliH := &admin.ClientHandler{Clients: clients, Store: d.Store}
	credH := &admin.CredentialHandler{Credentials: creds, Store: d.Store}
	jobH := &admin.JobHandler{Jobs: jobs, Store: d.Store}
	stateH := &admin.JobStateHandler{States: states, Store: d.Store}
	schH := &admin.ScheduleHandler{Schedules: schedules, Store: d.Store}
	instH := &admin.InstanceHandler{Instances: instances}
	auditH := &admin.AuditHandler{Audits: audits}
	exH := &admin.ExecutionHandler{Executions: executions, Store: d.Store}
	ovH := &admin.OverviewHandler{Overview: overview}
	schStatusH := &admin.SchedulerHandler{Store: d.Store}

	adminAPI := r.Group("/v1/admin")
	{
		adminAPI.GET("/overview", middleware.Auth(authCfg, "job.service.view"), ovH.Get)
		adminAPI.GET("/scheduler", middleware.Auth(authCfg, "job.schedule.view"), schStatusH.Get)
		adminAPI.POST("/scheduler/regenerate", middleware.Auth(authCfg, "job.schedule.manage"), schStatusH.RegenerateAll)
		adminAPI.GET("/triggers", middleware.Auth(authCfg, "job.schedule.view"), schH.ListTriggers)
		adminAPI.GET("/jobs", middleware.Auth(authCfg, "job.job.view"), jobH.ListGlobal)
		adminAPI.GET("/executions", middleware.Auth(authCfg, "job.job.view"), exH.List)
		adminAPI.GET("/instances", middleware.Auth(authCfg, "job.instance.view"), instH.ListGlobal)

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

		adminAPI.GET("/services/:serviceId/instances", middleware.Auth(authCfg, "job.instance.view"), instH.List)
		adminAPI.GET("/services/:serviceId/instances/:instanceId", middleware.Auth(authCfg, "job.instance.view"), instH.Get)

		adminAPI.GET("/services/:serviceId/jobs", middleware.Auth(authCfg, "job.job.view"), jobH.List)
		adminAPI.POST("/services/:serviceId/jobs", middleware.Auth(authCfg, "job.job.manage"), jobH.Create)
		adminAPI.GET("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.view"), jobH.Get)
		adminAPI.PATCH("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.manage"), jobH.Patch)
		adminAPI.DELETE("/services/:serviceId/jobs/:jobId", middleware.Auth(authCfg, "job.job.manage"), jobH.Delete)

		adminAPI.GET("/services/:serviceId/jobs/:jobId/executions", middleware.Auth(authCfg, "job.job.view"), exH.ListByJob)
		adminAPI.POST("/services/:serviceId/jobs/:jobId/executions", middleware.Auth(authCfg, "job.job.manage"), exH.Create)
		adminAPI.GET("/services/:serviceId/jobs/:jobId/executions/:executionId", middleware.Auth(authCfg, "job.job.view"), exH.Get)
		adminAPI.GET("/services/:serviceId/jobs/:jobId/executions/:executionId/logs", middleware.Auth(authCfg, "job.job.view"), exH.ListLogs)
		adminAPI.POST("/services/:serviceId/jobs/:jobId/executions/:executionId/cancel", middleware.Auth(authCfg, "job.job.manage"), exH.Cancel)

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
		adminAPI.GET("/services/:serviceId/jobs/:jobId/schedules/:scheduleId/triggers", middleware.Auth(authCfg, "job.schedule.view"), schH.ListScheduleTriggers)
		adminAPI.POST("/services/:serviceId/jobs/:jobId/schedules/:scheduleId/regenerate", middleware.Auth(authCfg, "job.schedule.manage"), schH.Regenerate)

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

	r.GET("/v1/executor/ws", gw.GinHandler())
	return &App{Engine: r, Hub: hub, Executions: executions, Schedules: schedules, SchedulerHandler: schStatusH, OverviewHandler: ovH}
}
