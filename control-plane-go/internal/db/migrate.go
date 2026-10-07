package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"infrapilot/migrations"
)

func gooseDB(pool *pgxpool.Pool) (*sql.DB, error) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return nil, err
	}
	return stdlib.OpenDBFromPool(pool), nil
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return goose.UpContext(ctx, sqlDB, ".")
}

// Status prints applied and pending migrations.
func Status(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return goose.StatusContext(ctx, sqlDB, ".")
}

// MarkBaseline records migration 1 as applied on a database whose schema was
// already created by Prisma, so `migrate up` does not try to create it again.
func MarkBaseline(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	version, err := goose.EnsureDBVersionContext(ctx, sqlDB)
	if err != nil {
		return err
	}
	if version >= 1 {
		return nil
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('"Deployment"') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("no existing schema found: run `migrate up` instead of `baseline`")
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, true)`); err != nil {
		return fmt.Errorf("record baseline: %w", err)
	}
	return nil
}
