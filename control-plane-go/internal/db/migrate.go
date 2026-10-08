package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/pressly/goose/v3/lock"

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

// migrationLockID is the Postgres session advisory lock key shared by
// `migrate up` and `migrate baseline`, so concurrent runs of either against
// the same database are serialised.
const migrationLockID int64 = 0x1F2A_B0A5_E11E

// versionTable is goose's version table (its default name).
const versionTable = "goose_db_version"

// ErrNotBaselined is returned by Migrate on a database that already has the
// Prisma schema but no goose baseline: running migration 1 there would fail
// halfway, so the operator must run `migrate baseline` first.
var ErrNotBaselined = errors.New("existing Prisma schema is not baselined: run `migrate baseline` first")

// newMigrationLocker returns the session locker on migrationLockID. It polls
// every second for up to 5 minutes. A new one is built per operation because
// its retry budget is stateful and is never reset between calls.
func newMigrationLocker() (lock.SessionLocker, error) {
	l, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID), lock.WithLockTimeout(1, 300))
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	return l, nil
}

// withMigrationLock takes the migration session lock on a dedicated
// connection from pool, runs fn with that connection, and releases the lock.
// Every operation that writes migration state (Migrate, migrateTo,
// MarkBaseline) runs inside it, so they are serialised across processes.
func withMigrationLock(ctx context.Context, pool *pgxpool.Pool,
	fn func(ctx context.Context, conn *sql.Conn) error) (retErr error) {
	locker, err := newMigrationLocker()
	if err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration lock connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := locker.SessionLock(ctx, conn); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer func() {
		if err := locker.SessionUnlock(context.WithoutCancel(ctx), conn); err != nil && retErr == nil {
			retErr = fmt.Errorf("migration unlock: %w", err)
		}
	}()
	return fn(ctx, conn)
}

// withProvider runs fn with a goose Provider over pool's connections.
//
// The Provider has no session locker on purpose. goose does not take its
// locker for every operation (Up and UpTo first call HasPending, which creates
// goose_db_version without the lock), so callers that write must hold
// withMigrationLock instead. A Provider locker on the same key would also
// deadlock against the lock our own connection already holds.
func withProvider(pool *pgxpool.Pool, fn func(p *goose.Provider) error) error {
	sqlDB := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithDisableGlobalRegistry(true))
	if err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("goose provider: %w", err)
	}
	defer func() { _ = p.Close() }()
	return fn(p)
}

// migrateLocked holds the migration lock, refuses an un-baselined Prisma
// schema, and then runs fn with a Provider. The Provider works on other pool
// connections while the lock connection stays held, so the pool needs at
// least two.
func migrateLocked(ctx context.Context, pool *pgxpool.Pool, fn func(p *goose.Provider) error) error {
	if pool.Config().MaxConns < 2 {
		return errors.New("migrations need a pool with at least 2 connections")
	}
	return withMigrationLock(ctx, pool, func(ctx context.Context, conn *sql.Conn) error {
		prisma, err := tableExists(ctx, conn, `"Deployment"`)
		if err != nil {
			return err
		}
		if prisma {
			done, err := baselineApplied(ctx, conn)
			if err != nil {
				return err
			}
			if !done {
				return ErrNotBaselined
			}
		}
		return withProvider(pool, fn)
	})
}

// Migrate applies all pending migrations. It returns ErrNotBaselined on a
// Prisma-created database that `migrate baseline` has not marked yet.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrateLocked(ctx, pool, func(p *goose.Provider) error {
		if _, err := p.Up(ctx); err != nil {
			return fmt.Errorf("apply migrations: %w", err)
		}
		return nil
	})
}

// migrateTo applies pending migrations up to and including version. Only the
// schema drift test uses it, to compare migration 1 with the Prisma schema.
func migrateTo(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	return migrateLocked(ctx, pool, func(p *goose.Provider) error {
		if _, err := p.UpTo(ctx, version); err != nil {
			return fmt.Errorf("apply migrations to %d: %w", version, err)
		}
		return nil
	})
}

// Status returns every migration with its state, in version order. It holds
// the migration lock because goose may create goose_db_version.
func Status(ctx context.Context, pool *pgxpool.Pool) ([]*goose.MigrationStatus, error) {
	var statuses []*goose.MigrationStatus
	err := withMigrationLock(ctx, pool, func(ctx context.Context, _ *sql.Conn) error {
		return withProvider(pool, func(p *goose.Provider) error {
			var err error
			statuses, err = p.Status(ctx)
			if err != nil {
				return fmt.Errorf("migration status: %w", err)
			}
			return nil
		})
	})
	return statuses, err
}

// MarkBaseline records migration 1 as applied on a database whose schema was
// already created by Prisma, so `migrate up` does not try to create it again.
// It refuses unless the Prisma migration history is exactly the frozen one and
// fully finished, so a partially migrated or diverged database is never
// marked as matching the baseline. It holds the same lock as Migrate and does
// all its checks and writes in one transaction, so a refusal leaves no goose
// version table behind.
func MarkBaseline(ctx context.Context, pool *pgxpool.Pool) error {
	store, err := database.NewStore(database.DialectPostgres, versionTable)
	if err != nil {
		return fmt.Errorf("goose store: %w", err)
	}
	return withMigrationLock(ctx, pool, func(ctx context.Context, conn *sql.Conn) error {
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin baseline transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()

		done, err := baselineApplied(ctx, tx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if err := checkPrismaSchema(ctx, tx); err != nil {
			return err
		}
		exists, err := tableExists(ctx, tx, versionTable)
		if err != nil {
			return err
		}
		if !exists {
			if err := store.CreateVersionTable(ctx, tx); err != nil {
				return fmt.Errorf("create goose version table: %w", err)
			}
			if err := store.Insert(ctx, tx, database.InsertRequest{Version: 0}); err != nil {
				return fmt.Errorf("record goose version 0: %w", err)
			}
		}
		if err := store.Insert(ctx, tx, database.InsertRequest{Version: 1}); err != nil {
			return fmt.Errorf("record baseline: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit baseline: %w", err)
		}
		return nil
	})
}

// querier is the read API shared by *sql.Tx, *sql.Conn and *sql.DB, used by
// the baseline checks.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// tableExists reports whether name resolves to a table on the search_path.
func tableExists(ctx context.Context, q querier, name string) (bool, error) {
	var exists bool
	if err := q.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		return false, fmt.Errorf("check table %s: %w", name, err)
	}
	return exists, nil
}

// baselineApplied reports whether goose already records version 1 or later.
func baselineApplied(ctx context.Context, q querier) (bool, error) {
	exists, err := tableExists(ctx, q, versionTable)
	if err != nil || !exists {
		return false, err
	}
	var applied bool
	err = q.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id >= 1 AND is_applied)`).Scan(&applied)
	if err != nil {
		return false, fmt.Errorf("goose version: %w", err)
	}
	return applied, nil
}

// checkPrismaSchema verifies that the Prisma schema exists and that its
// migration history is exactly the frozen one, with nothing failed or still
// in progress.
func checkPrismaSchema(ctx context.Context, q querier) error {
	exists, err := tableExists(ctx, q, `"Deployment"`)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("no existing schema found: run `migrate up` instead of `baseline`")
	}
	exists, err = tableExists(ctx, q, `"_prisma_migrations"`)
	if err != nil {
		return err
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
	if unknown := unknownPrismaMigrations(finished); len(unknown) > 0 {
		return incompleteErr("unknown", unknown)
	}
	// A failed or in-progress migration has no finished_at. One that was
	// resolved with `prisma migrate resolve --rolled-back` is not pending.
	unfinished, err := migrationNames(ctx, q,
		`SELECT migration_name FROM "_prisma_migrations" WHERE finished_at IS NULL AND rolled_back_at IS NULL`)
	if err != nil {
		return err
	}
	if len(unfinished) > 0 {
		return incompleteErr("unfinished", sortedKeys(unfinished))
	}
	return nil
}

func migrationNames(ctx context.Context, q querier, query string) (_ map[string]bool, retErr error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read prisma migrations: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil && retErr == nil {
			retErr = fmt.Errorf("read prisma migrations: %w", err)
		}
	}()
	set := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("read prisma migrations: %w", err)
		}
		set[n] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read prisma migrations: %w", err)
	}
	return set, nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// unknownPrismaMigrations returns the finished migrations that are not in
// prismaMigrations, sorted.
func unknownPrismaMigrations(finished map[string]bool) []string {
	var unknown []string
	for _, n := range sortedKeys(finished) {
		if !slices.Contains(prismaMigrations, n) {
			unknown = append(unknown, n)
		}
	}
	return unknown
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
	if kind == "unknown" {
		return fmt.Errorf("prisma migrations diverged from the frozen Node schema (%s: %s): "+
			"this database has migrations the goose baseline does not match; refusing to baseline",
			kind, strings.Join(names, ", "))
	}
	return fmt.Errorf("prisma migrations incomplete (%s: %s): run prisma migrate deploy with the Node backend first",
		kind, strings.Join(names, ", "))
}
