package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"infrapilot/migrations"
)

// prismaMigrations lists every migration in control-plane/prisma/migrations.
// The Node backend is frozen, so its schema is frozen at exactly these; the
// goose baseline (migration 1) is equivalent to all of them applied.
var prismaMigrations = []string{
	"20260126184745_init",
	"20260128182835_outbox",
	"20260206121329_deployment_runtime",
	"20260206212249_service_container_port",
	"20260207090347_remove_service_container_port",
	"20260313204608_add_service_type_and_volume",
	"20260319171639_add_nextjs_react",
	"20260321094512_add_auth_fields",
	"20260321104547_add_password_field",
	"20260410091445_add_canvas_positioning",
}

// baselineLockID is the pg_advisory_xact_lock key that serialises concurrent
// `migrate baseline` runs against the same database.
const baselineLockID int64 = 0x1F2A_B0A5_E11E

// querier is the read API shared by *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func gooseDB(pool *pgxpool.Pool) (*sql.DB, error) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return nil, fmt.Errorf("goose dialect: %w", err)
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
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Status prints applied and pending migrations.
func Status(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if err := goose.StatusContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("migration status: %w", err)
	}
	return nil
}

// MarkBaseline records migration 1 as applied on a database whose schema was
// already created by Prisma, so `migrate up` does not try to create it again.
// It refuses unless every Prisma migration finished, so a partially migrated
// database is never marked as matching the baseline.
func MarkBaseline(ctx context.Context, pool *pgxpool.Pool) error {
	// Read-only pre-check, so a refused baseline does not leave a goose
	// version table behind. The checks are repeated under the lock below.
	done, err := baselineApplied(ctx, pool)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := checkPrismaSchema(ctx, pool); err != nil {
		return err
	}

	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	if _, err := goose.EnsureDBVersionContext(ctx, sqlDB); err != nil {
		return fmt.Errorf("goose version table: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin baseline transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, baselineLockID); err != nil {
		return fmt.Errorf("baseline lock: %w", err)
	}
	done, err = baselineApplied(ctx, tx)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := checkPrismaSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, true)`); err != nil {
		return fmt.Errorf("record baseline: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit baseline: %w", err)
	}
	return nil
}

// baselineApplied reports whether goose already records version 1 or later.
func baselineApplied(ctx context.Context, q querier) (bool, error) {
	var applied bool
	err := q.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("goose version table: %w", err)
	}
	if !applied {
		return false, nil
	}
	err = q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id >= 1 AND is_applied)`).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("goose version: %w", err)
	}
	return applied, nil
}

// checkPrismaSchema verifies that the Prisma schema exists and that its
// migration history is complete, with nothing failed or still in progress.
func checkPrismaSchema(ctx context.Context, q querier) error {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT to_regclass('"Deployment"') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("check existing schema: %w", err)
	}
	if !exists {
		return errors.New("no existing schema found: run `migrate up` instead of `baseline`")
	}
	if err := q.QueryRow(ctx, `SELECT to_regclass('"_prisma_migrations"') IS NOT NULL`).Scan(&exists); err != nil {
		return fmt.Errorf("check prisma migrations table: %w", err)
	}
	if !exists {
		return incompleteErr("missing", prismaMigrations)
	}

	finished, err := migrationNames(ctx, q,
		`SELECT migration_name FROM "_prisma_migrations" WHERE finished_at IS NOT NULL AND rolled_back_at IS NULL`)
	if err != nil {
		return err
	}
	if missing := missingPrismaMigrations(finished); len(missing) > 0 {
		return incompleteErr("missing", missing)
	}
	// A failed or in-progress migration has no finished_at. One that was
	// resolved with `prisma migrate resolve --rolled-back` is not pending.
	unfinished, err := migrationNames(ctx, q,
		`SELECT migration_name FROM "_prisma_migrations" WHERE finished_at IS NULL AND rolled_back_at IS NULL`)
	if err != nil {
		return err
	}
	if len(unfinished) > 0 {
		names := make([]string, 0, len(unfinished))
		for n := range unfinished {
			names = append(names, n)
		}
		slices.Sort(names)
		return incompleteErr("unfinished", names)
	}
	return nil
}

func migrationNames(ctx context.Context, q querier, query string) (map[string]bool, error) {
	rows, err := q.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read prisma migrations: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read prisma migrations: %w", err)
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set, nil
}

// missingPrismaMigrations returns the entries of prismaMigrations not in
// applied, in migration order.
func missingPrismaMigrations(applied map[string]bool) []string {
	var missing []string
	for _, m := range prismaMigrations {
		if !applied[m] {
			missing = append(missing, m)
		}
	}
	return missing
}

func incompleteErr(kind string, names []string) error {
	return fmt.Errorf("prisma migrations incomplete (%s: %s): run prisma migrate deploy with the Node backend first",
		kind, strings.Join(names, ", "))
}
