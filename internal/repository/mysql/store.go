package mysql

import (
	"database/sql"

	"gorm.io/gorm"
)

type Store struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) DB() *gorm.DB { return s.db }

func (s *Store) SQLDB() (*sql.DB, error) { return s.db.DB() }
