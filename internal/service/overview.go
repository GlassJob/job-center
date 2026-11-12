package service

import (
	"context"

	"github.com/GlassJob/job-center/internal/model"
	"github.com/GlassJob/job-center/internal/repository/mysql"
)

type Overview struct {
	store *mysql.Store
}

func NewOverview(store *mysql.Store) *Overview {
	return &Overview{store: store}
}

func (s *Overview) Get(ctx context.Context) (*model.Overview, error) {
	return s.store.GetOverview(ctx)
}
