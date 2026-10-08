package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"infrapilot/internal/db"
	"infrapilot/internal/testutil"
	"infrapilot/migrations"
)

const prismaMigrationsDir = "../../../control-plane/prisma/migrations"

// latestVersion is the highest goose migration version in migrations.FS.
func latestVersion(t *testing.T) int64 {
	t.Helper()
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("list migrations: %v (%d files)", err, len(files))
	}
	var latest int64
	for _, f := range files {
		prefix, _, _ := strings.Cut(f, "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			t.Fatalf("migration %s: %v", f, err)
		}
		latest = max(latest, v)
	}
	return latest
}

// applyPrisma recreates what `prisma migrate deploy` leaves behind: every
// frozen Node migration.sql applied in order, plus a finished
// _prisma_migrations row for each.
func applyPrisma(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	entries, err := os.ReadDir(prismaMigrationsDir)
	if err != nil {
		t.Fatalf("Node migrations directory not available: %v", err)
	}
	_, err = pool.Exec(ctx, `CREATE TABLE "_prisma_migrations" (
		"id" VARCHAR(36) PRIMARY KEY NOT NULL,
		"checksum" VARCHAR(64) NOT NULL,
		"finished_at" TIMESTAMPTZ,
		"migration_name" VARCHAR(255) NOT NULL,
		"logs" TEXT,
		"rolled_back_at" TIMESTAMPTZ,
		"started_at" TIMESTAMPTZ NOT NULL DEFAULT now(),
		"applied_steps_count" INTEGER NOT NULL DEFAULT 0)`)
	if err != nil {
		t.Fatalf("create _prisma_migrations: %v", err)
	}
	n := 0
	for _, e := range entries { // os.ReadDir returns entries sorted by name
		if !e.IsDir() {
			continue
		}
		sqlText, err := os.ReadFile(filepath.Join(prismaMigrationsDir, e.Name(), "migration.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sqlText)); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(sqlText)
		_, err = pool.Exec(ctx, `INSERT INTO "_prisma_migrations"
			(id, checksum, finished_at, migration_name, applied_steps_count)
			VALUES (gen_random_uuid()::text, $1, now(), $2, 1)`, hex.EncodeToString(sum[:]), e.Name())
		if err != nil {
			t.Fatalf("record %s: %v", e.Name(), err)
		}
		n++
	}
	if n == 0 {
		t.Fatalf("no Prisma migrations in %s", prismaMigrationsDir)
	}
}

func gooseVersions(t *testing.T, pool *pgxpool.Pool) []int64 {
	t.Helper()
	rows, err := pool.Query(t.Context(), `SELECT version_id FROM goose_db_version ORDER BY id`)
	if err != nil {
		t.Fatalf("read goose_db_version: %v", err)
	}
	v, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("read goose_db_version: %v", err)
	}
	return v
}

func maxVersion(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var v int64
	if err := pool.QueryRow(t.Context(),
		`SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v); err != nil {
		t.Fatalf("read goose version: %v", err)
	}
	return v
}

func assertNoGooseTable(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(t.Context(),
		`SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("goose_db_version exists, want none")
	}
}

func assertErrContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want it to contain %q", err, want)
	}
}

func TestConnectWithPrismaSchemaParam(t *testing.T) {
	url, _ := testutil.FreshDB(t, adminURL)
	pool, err := db.Connect(t.Context(), url+"&schema=public&connection_limit=5")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var schema string
	if err := pool.QueryRow(t.Context(), `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if schema != "public" {
		t.Fatalf("current_schema() = %q, want public", schema)
	}
}

func TestMigrateEmptyDBThenNoOp(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if got, want := maxVersion(t, pool), latestVersion(t); got != want {
		t.Fatalf("version = %d, want %d", got, want)
	}
	before := gooseVersions(t, pool)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if after := gooseVersions(t, pool); !slices.Equal(before, after) {
		t.Fatalf("second Migrate changed goose rows: %v -> %v", before, after)
	}
}

func TestBaselineThenMigrateKeepsData(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	ctx := t.Context()
	if _, err := pool.Exec(ctx,
		`INSERT INTO "User" (id, email, password) VALUES ('u1', 'a@example.com', 'x')`); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkBaseline(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if got, want := maxVersion(t, pool), latestVersion(t); got != want {
		t.Fatalf("version = %d, want %d", got, want)
	}
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM "User" WHERE id = 'u1'`).Scan(&email); err != nil {
		t.Fatalf("User row lost: %v", err)
	}
}

func TestBaselineTwiceIsNoOp(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	for i := range 2 {
		if err := db.MarkBaseline(t.Context(), pool); err != nil {
			t.Fatalf("baseline %d: %v", i+1, err)
		}
	}
	if got := gooseVersions(t, pool); !slices.Equal(got, []int64{0, 1}) {
		t.Fatalf("goose rows = %v, want [0 1]", got)
	}
}

func TestBaselineEmptyDBRefused(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	assertErrContains(t, db.MarkBaseline(t.Context(), pool), "no existing schema")
	assertNoGooseTable(t, pool)
}

func TestBaselineMissingPrismaMigrationRefused(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	if _, err := pool.Exec(t.Context(),
		`DELETE FROM "_prisma_migrations" WHERE migration_name = '20260410091445_add_canvas_positioning'`); err != nil {
		t.Fatal(err)
	}
	assertErrContains(t, db.MarkBaseline(t.Context(), pool), "missing")
	assertNoGooseTable(t, pool)
}

func TestBaselineUnfinishedPrismaMigrationRefused(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	// A migration Prisma started but never finished (failed or in progress).
	if _, err := pool.Exec(t.Context(), `INSERT INTO "_prisma_migrations" (id, checksum, migration_name)
		VALUES (gen_random_uuid()::text, 'x', '20990101000000_in_progress')`); err != nil {
		t.Fatal(err)
	}
	assertErrContains(t, db.MarkBaseline(t.Context(), pool), "unfinished")
	assertNoGooseTable(t, pool)
}

func TestBaselineUnknownPrismaMigrationRefused(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	if _, err := pool.Exec(t.Context(), `INSERT INTO "_prisma_migrations" (id, checksum, finished_at, migration_name)
		VALUES (gen_random_uuid()::text, 'x', now(), '20990101000000_extra')`); err != nil {
		t.Fatal(err)
	}
	err := db.MarkBaseline(t.Context(), pool)
	assertErrContains(t, err, "unknown: 20990101000000_extra")
	assertErrContains(t, err, "diverged")
	assertNoGooseTable(t, pool)
}

func TestStatusAllApplied(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.Status(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(statuses)) != latestVersion(t) {
		t.Fatalf("got %d statuses, want %d", len(statuses), latestVersion(t))
	}
	for _, s := range statuses {
		if s.State != goose.StateApplied {
			t.Errorf("%s: state %s", s.Source.Path, s.State)
		}
	}
}

func runConcurrently(t *testing.T, n int, fn func(ctx context.Context) error) {
	t.Helper()
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { errs[i] = fn(t.Context()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: %v", i, err)
		}
	}
}

func TestConcurrentBaselines(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	runConcurrently(t, 4, func(ctx context.Context) error { return db.MarkBaseline(ctx, pool) })
	if got := gooseVersions(t, pool); !slices.Equal(got, []int64{0, 1}) {
		t.Fatalf("goose rows = %v, want [0 1]", got)
	}
}

func TestConcurrentMigrates(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	runConcurrently(t, 4, func(ctx context.Context) error { return db.Migrate(ctx, pool) })
	if got, want := maxVersion(t, pool), latestVersion(t); got != want {
		t.Fatalf("version = %d, want %d", got, want)
	}
	want := []int64{0}
	for v := int64(1); v <= latestVersion(t); v++ {
		want = append(want, v)
	}
	if got := gooseVersions(t, pool); !slices.Equal(got, want) {
		t.Fatalf("goose rows = %v, want %v", got, want)
	}
}

// schemaQueries describe a schema as sorted text rows, excluding the two
// migration bookkeeping tables.
var schemaQueries = map[string]string{
	"columns": `SELECT format('%s.%s %s %s datetime_precision=%s char_max=%s numeric=%s,%s nullable=%s default=%s',
			table_name, column_name, data_type, udt_name,
			coalesce(datetime_precision::text, '-'), coalesce(character_maximum_length::text, '-'),
			coalesce(numeric_precision::text, '-'), coalesce(numeric_scale::text, '-'),
			is_nullable, coalesce(column_default, '<none>'))
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name NOT IN ('_prisma_migrations', 'goose_db_version')`,
	"indexes": `SELECT indexdef FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND tablename NOT IN ('_prisma_migrations', 'goose_db_version')`,
	"constraints": `SELECT format('%s.%s %s', c.conrelid::regclass, c.conname, pg_get_constraintdef(c.oid))
		FROM pg_constraint c
		JOIN pg_class r ON r.oid = c.conrelid
		WHERE r.relnamespace = current_schema()::regnamespace
		  AND r.relname NOT IN ('_prisma_migrations', 'goose_db_version')`,
	"enums": `SELECT format('%s %s', t.typname, string_agg(e.enumlabel, ',' ORDER BY e.enumsortorder))
		FROM pg_enum e JOIN pg_type t ON t.oid = e.enumtypid
		WHERE t.typnamespace = current_schema()::regnamespace
		GROUP BY t.typname`,
}

func describeSchema(t *testing.T, pool *pgxpool.Pool, query string) []string {
	t.Helper()
	rows, err := pool.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("describe schema: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("describe schema: %v", err)
	}
	slices.Sort(got)
	return got
}

// schemaDiff compares the schemas of the Prisma and goose databases and
// returns, per kind, the rows only one side has.
func schemaDiff(t *testing.T, prisma, gooseDB *pgxpool.Pool) map[string][]string {
	t.Helper()
	diffs := map[string][]string{}
	for _, kind := range []string{"columns", "indexes", "constraints", "enums"} {
		p := describeSchema(t, prisma, schemaQueries[kind])
		g := describeSchema(t, gooseDB, schemaQueries[kind])
		if len(p) == 0 {
			t.Fatalf("%s: Prisma schema is empty", kind)
		}
		var diff []string
		for _, r := range p {
			if !slices.Contains(g, r) {
				diff = append(diff, "- prisma only: "+r)
			}
		}
		for _, r := range g {
			if !slices.Contains(p, r) {
				diff = append(diff, "+ goose only:  "+r)
			}
		}
		if len(diff) > 0 {
			diffs[kind] = diff
		}
	}
	return diffs
}

// prismaAndGooseDBs returns a Prisma-only database and one migrated by goose
// to version 1 only.
func prismaAndGooseDBs(t *testing.T) (prisma, gooseDB *pgxpool.Pool) {
	t.Helper()
	_, prisma = testutil.FreshDB(t, adminURL)
	applyPrisma(t, prisma)
	_, gooseDB = testutil.FreshDB(t, adminURL)
	if err := db.MigrateTo(t.Context(), gooseDB, 1); err != nil {
		t.Fatal(err)
	}
	return prisma, gooseDB
}

// TestBaselineMatchesPrismaSchema is the drift guard: goose migration 1 must
// create exactly the schema the frozen Prisma migrations create.
func TestBaselineMatchesPrismaSchema(t *testing.T) {
	prisma, gooseDB := prismaAndGooseDBs(t)
	for kind, diff := range schemaDiff(t, prisma, gooseDB) {
		t.Errorf("%s differ between Prisma and goose 00001:\n%s", kind, strings.Join(diff, "\n"))
	}
}

// TestSchemaDiffCatchesTypeModifier proves the drift guard sees a type
// modifier change (TIMESTAMP(3) vs TIMESTAMP(0)), not only the type name.
func TestSchemaDiffCatchesTypeModifier(t *testing.T) {
	prisma, gooseDB := prismaAndGooseDBs(t)
	if _, err := gooseDB.Exec(t.Context(),
		`ALTER TABLE "Deployment" ALTER COLUMN "createdAt" TYPE timestamp(0)`); err != nil {
		t.Fatal(err)
	}
	diff := schemaDiff(t, prisma, gooseDB)["columns"]
	if !slices.ContainsFunc(diff, func(r string) bool { return strings.Contains(r, "Deployment.createdAt") }) {
		t.Fatalf("drift guard missed TIMESTAMP(3) -> TIMESTAMP(0) on Deployment.createdAt; column diff = %v", diff)
	}
}

// TestMigrateWaitsForLockBeforeTouchingDB: while another session holds the
// migration lock, Migrate must not create goose_db_version (or anything else).
func TestMigrateWaitsForLockBeforeTouchingDB(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	ctx := t.Context()
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, db.MigrationLockID); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			_, _ = holder.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, db.MigrationLockID)
		}
	}()

	done := make(chan error, 1)
	go func() { done <- db.Migrate(ctx, pool) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("Migrate returned while the lock was held: %v", err)
		default:
		}
		var exists bool
		if err := holder.QueryRow(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("goose_db_version was created while another session held the migration lock")
		}
		time.Sleep(200 * time.Millisecond)
	}

	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock($1)`, db.MigrationLockID); err != nil {
		t.Fatal(err)
	}
	locked = false
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Migrate after unlock: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Migrate did not finish within 10s of the unlock")
	}
	if got, want := maxVersion(t, pool), latestVersion(t); got != want {
		t.Fatalf("version = %d, want %d", got, want)
	}
}

func TestMigrateRefusesUnbaselinedPrisma(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	applyPrisma(t, pool)
	ctx := t.Context()
	if err := db.Migrate(ctx, pool); !errors.Is(err, db.ErrNotBaselined) {
		t.Fatalf("Migrate on un-baselined Prisma DB: err = %v, want ErrNotBaselined", err)
	}
	assertNoGooseTable(t, pool)

	// The state a failed `up` used to leave: a version table with only v0.
	if _, err := pool.Exec(ctx, `CREATE TABLE goose_db_version (
		id serial PRIMARY KEY, version_id bigint NOT NULL,
		is_applied boolean NOT NULL, tstamp timestamp NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO goose_db_version (version_id, is_applied) VALUES (0, true)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); !errors.Is(err, db.ErrNotBaselined) {
		t.Fatalf("Migrate with a v0-only version table: err = %v, want ErrNotBaselined", err)
	}

	if err := db.MarkBaseline(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if got := gooseVersions(t, pool); !slices.Equal(got, []int64{0, 1, 2}) {
		t.Fatalf("goose rows = %v, want [0 1 2]", got)
	}
}

func TestConcurrentBaselineAndMigrate(t *testing.T) {
	for run := range 5 {
		t.Run(strconv.Itoa(run+1), func(t *testing.T) {
			_, pool := testutil.FreshDB(t, adminURL)
			applyPrisma(t, pool)
			ctx := t.Context()
			if _, err := pool.Exec(ctx,
				`INSERT INTO "User" (id, email, password) VALUES ('u1', 'a@example.com', 'x')`); err != nil {
				t.Fatal(err)
			}

			start := make(chan struct{})
			var baseErrs, upErrs [2]error
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Go(func() { <-start; baseErrs[i] = db.MarkBaseline(ctx, pool) })
				wg.Go(func() { <-start; upErrs[i] = db.Migrate(ctx, pool) })
			}
			close(start)
			wg.Wait()
			for i, err := range baseErrs {
				if err != nil {
					t.Errorf("MarkBaseline %d: %v", i, err)
				}
			}
			for i, err := range upErrs {
				if err != nil && !errors.Is(err, db.ErrNotBaselined) {
					t.Errorf("Migrate %d: %v (want nil or ErrNotBaselined)", i, err)
				}
			}
			if err := db.Migrate(ctx, pool); err != nil {
				t.Fatalf("final Migrate: %v", err)
			}
			if got := gooseVersions(t, pool); !slices.Equal(got, []int64{0, 1, 2}) {
				t.Fatalf("goose rows = %v, want [0 1 2]", got)
			}
			var email string
			if err := pool.QueryRow(ctx, `SELECT email FROM "User" WHERE id = 'u1'`).Scan(&email); err != nil {
				t.Fatalf("User row lost: %v", err)
			}
		})
	}
}

func TestStatusOnEmptyDBDoesNotWrite(t *testing.T) {
	_, pool := testutil.FreshDB(t, adminURL)
	ctx := t.Context()
	statuses, err := db.Status(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(statuses)) != latestVersion(t) {
		t.Fatalf("got %d statuses, want %d", len(statuses), latestVersion(t))
	}
	for _, s := range statuses {
		if s.State != goose.StatePending {
			t.Errorf("%s: state %s, want pending", s.Source.Path, s.State)
		}
	}
	assertNoGooseTable(t, pool)

	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock($1)`, db.MigrationLockID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = holder.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, db.MigrationLockID)
	}()
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := db.Status(sctx, pool); err != nil {
		t.Fatalf("Status with the migration lock held elsewhere: %v", err)
	}
	assertNoGooseTable(t, pool)
}
