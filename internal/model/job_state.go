package model

import "time"

type JobState struct {
	ID                   string         `gorm:"column:id;primaryKey"`
	JobID                string         `gorm:"column:job_id"`
	Key                  string         `gorm:"column:key"`
	Value                map[string]any `gorm:"column:value;serializer:json"`
	Version              uint64         `gorm:"column:version"`
	UpdatedBy            string         `gorm:"column:updated_by"`
	UpdatedByExecutionID string         `gorm:"column:updated_by_execution_id"`
	UpdatedAt            time.Time      `gorm:"column:updated_at"`
}

func (JobState) TableName() string { return "job_states" }

type JobStateView struct {
	ID                   string         `json:"id"`
	JobID                string         `json:"job_id"`
	Key                  string         `json:"key"`
	Value                map[string]any `json:"value"`
	Version              uint64         `json:"version"`
	UpdatedBy            string         `json:"updated_by"`
	UpdatedByExecutionID string         `json:"updated_by_execution_id,omitempty"`
	UpdatedAt            time.Time      `json:"updated_at"`
}

func (s *JobState) View() JobStateView {
	val := s.Value
	if val == nil {
		val = map[string]any{}
	}
	return JobStateView{
		ID:                   s.ID,
		JobID:                s.JobID,
		Key:                  s.Key,
		Value:                val,
		Version:              s.Version,
		UpdatedBy:            s.UpdatedBy,
		UpdatedByExecutionID: s.UpdatedByExecutionID,
		UpdatedAt:            s.UpdatedAt,
	}
}
