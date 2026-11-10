package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/pkg/idgen"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

const maxStateValueBytes = 64 * 1024

type JobStates struct {
	store *mysql.Store
}

func NewJobStates(store *mysql.Store) *JobStates {
	return &JobStates{store: store}
}

type CreateJobStateInput struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type PatchJobStateInput struct {
	Value   map[string]any `json:"value"`
	Version *uint64        `json:"version"` // 必填：CAS 期望版本
}

func (s *JobStates) ensureJob(ctx context.Context, serviceID, jobID string) error {
	_, err := s.store.GetJob(ctx, serviceID, jobID)
	if errors.Is(err, mysql.ErrNotFound) {
		return fmt.Errorf("%w: job", ErrNotFound)
	}
	return err
}

func validateStateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" || utf8.RuneCountInString(key) > 191 {
		return fmt.Errorf("%w: key", ErrInvalidInput)
	}
	return nil
}

func validateStateValue(v map[string]any) error {
	if v == nil {
		return fmt.Errorf("%w: value required object", ErrInvalidInput)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("%w: value", ErrInvalidInput)
	}
	if len(b) > maxStateValueBytes {
		return fmt.Errorf("%w: value too large", ErrInvalidInput)
	}
	return nil
}

func (s *JobStates) List(ctx context.Context, serviceID, jobID, keyword string, page, limit int) ([]model.JobStateView, int64, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, 0, err
	}
	list, total, err := s.store.ListJobStates(ctx, jobID, keyword, page, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.JobStateView, 0, len(list))
	for i := range list {
		out = append(out, list[i].View())
	}
	return out, total, nil
}

func (s *JobStates) Get(ctx context.Context, serviceID, jobID, id string) (*model.JobStateView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	st, err := s.store.GetJobState(ctx, jobID, id)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v := st.View()
	return &v, nil
}

func (s *JobStates) Create(ctx context.Context, serviceID, jobID string, in CreateJobStateInput, updatedBy string) (*model.JobStateView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(in.Key)
	if err := validateStateKey(key); err != nil {
		return nil, err
	}
	if err := validateStateValue(in.Value); err != nil {
		return nil, err
	}
	taken, err := s.store.ExistsJobStateKey(ctx, jobID, key, "")
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("%w: key already exists", ErrConflict)
	}
	if updatedBy == "" {
		updatedBy = "admin"
	}
	st := &model.JobState{
		ID:        idgen.New(),
		JobID:     jobID,
		Key:       key,
		Value:     in.Value,
		Version:   1,
		UpdatedBy: updatedBy,
	}
	if err := s.store.CreateJobState(ctx, st); err != nil {
		if errors.Is(err, mysql.ErrConflict) {
			return nil, ErrConflict
		}
		return nil, err
	}
	got, err := s.store.GetJobState(ctx, jobID, st.ID)
	if err != nil {
		return nil, err
	}
	v := got.View()
	return &v, nil
}

func (s *JobStates) Patch(ctx context.Context, serviceID, jobID, id string, in PatchJobStateInput, updatedBy string) (*model.JobStateView, error) {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return nil, err
	}
	if in.Version == nil {
		return nil, fmt.Errorf("%w: version required for CAS", ErrInvalidInput)
	}
	if err := validateStateValue(in.Value); err != nil {
		return nil, err
	}
	if updatedBy == "" {
		updatedBy = "admin"
	}
	st, err := s.store.PatchJobStateCAS(ctx, jobID, id, *in.Version, in.Value, updatedBy)
	if errors.Is(err, mysql.ErrNotFound) {
		return nil, ErrNotFound
	}
	if errors.Is(err, mysql.ErrConflict) {
		return nil, fmt.Errorf("%w: version mismatch", ErrConflict)
	}
	if err != nil {
		return nil, err
	}
	v := st.View()
	return &v, nil
}

func (s *JobStates) Delete(ctx context.Context, serviceID, jobID, id string) error {
	if err := s.ensureJob(ctx, serviceID, jobID); err != nil {
		return err
	}
	if err := s.store.DeleteJobState(ctx, jobID, id); err != nil {
		if errors.Is(err, mysql.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	return nil
}
