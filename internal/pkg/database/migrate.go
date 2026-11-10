package database

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// MigrateUp 使用 golang-migrate 应用 embed 的 *.up.sql。
func MigrateUp(dsn string, fsys fs.FS) error {
	n, err := NormalizeDSN(dsn, true)
	if err != nil {
		return err
	}
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrate source: %w", err)
	}
	db, err := OpenPing(n)
	if err != nil {
		return err
	}
	defer db.Close()

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		return fmt.Errorf("migrate mysql driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "mysql", driver)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
