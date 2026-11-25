package model

import "time"

// Overview 管理端运行概览聚合。
type Overview struct {
	ServiceCount        int64                       `json:"service_count"`
	JobCount            int64                       `json:"job_count"`
	EnabledJobCount     int64                       `json:"enabled_job_count"`
	InstanceCounts      OverviewInstanceCounts      `json:"instance_counts"`
	ExecutionToday      OverviewExecutionToday      `json:"execution_today"`
	ExecutionDays       []OverviewExecutionDay      `json:"execution_days"`
	Upcoming            []OverviewUpcomingItem      `json:"upcoming"`
	RecentFailed        []OverviewFailedItem        `json:"recent_failed"`
	UnhealthyInstances  []OverviewUnhealthyInstance `json:"unhealthy_instances"`
}

// OverviewExecutionDay 一个 UTC 日的执行计数。
type OverviewExecutionDay struct {
	Date    string `json:"date"`
	Total   int64  `json:"total"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// OverviewUnhealthyInstance 非在线执行器摘要。
type OverviewUnhealthyInstance struct {
	ID              string     `json:"id"`
	InstanceKey     string     `json:"instance_key"`
	Status          string     `json:"status"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	ServiceID       string     `json:"service_id"`
	ServiceCode     string     `json:"service_code"`
	ServiceName     string     `json:"service_name"`
}

type OverviewInstanceCounts struct {
	Registering int64 `json:"REGISTERING"`
	Online      int64 `json:"ONLINE"`
	Suspect     int64 `json:"SUSPECT"`
	Offline     int64 `json:"OFFLINE"`
	Draining    int64 `json:"DRAINING"`
}

type OverviewExecutionToday struct {
	Total    int64 `json:"total"`
	Running  int64 `json:"running"`
	Success  int64 `json:"success"`
	Failed   int64 `json:"failed"`
	Canceled int64 `json:"canceled"`
}

type OverviewUpcomingItem struct {
	ScheduleID     string     `json:"schedule_id"`
	ScheduleCode   string     `json:"schedule_code"`
	ScheduleName   string     `json:"schedule_name"`
	CronExpression string     `json:"cron_expression,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	NextFireAt     *time.Time `json:"next_fire_at,omitempty"`
	JobID          string     `json:"job_id"`
	JobCode        string     `json:"job_code"`
	JobName        string     `json:"job_name"`
	ServiceID      string     `json:"service_id"`
	ServiceCode    string     `json:"service_code"`
	ServiceName    string     `json:"service_name"`
}

type OverviewFailedItem struct {
	ExecutionID  string     `json:"execution_id"`
	Status       string     `json:"status"`
	ErrorCode    string     `json:"error_code,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	JobID        string     `json:"job_id"`
	JobCode      string     `json:"job_code"`
	JobName      string     `json:"job_name"`
	ServiceID    string     `json:"service_id"`
	ServiceCode  string     `json:"service_code"`
	ServiceName  string     `json:"service_name"`
}
