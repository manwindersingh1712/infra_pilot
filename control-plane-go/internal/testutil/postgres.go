// Package testutil holds helpers shared by tests: throwaway containers, fixtures and fakes.
package testutil

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"infrapilot/internal/db"
)

// StartPostgres runs postgres:16 in Docker and applies the migrations.
func StartPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("cp_test"),
		tcpostgres.WithUsername("cp"),
		tcpostgres.WithPassword("cp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("start postgres container: %w", err)
	}
	terminate := func() { _ = ctr.Terminate(context.Background()) }

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		terminate()
		return nil, nil, err
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		terminate()
		return nil, nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		terminate()
		return nil, nil, fmt.Errorf("migrate test db: %w", err)
	}
	return pool, func() { pool.Close(); terminate() }, nil
}

// RunWithPostgres is a TestMain body. It starts Postgres unless -short is set,
// stores the pool in *pool, runs the tests and exits.
func RunWithPostgres(m *testing.M, pool **pgxpool.Pool) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	p, cleanup, err := StartPostgres(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	*pool = p
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// NeedDB skips the test under -short and otherwise empties every table.
func NeedDB(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	if pool == nil {
		t.Skip("integration test: needs Docker; run without -short")
	}
	_, err := pool.Exec(context.Background(),
		`TRUNCATE "ServiceConnection", "OutboxEvent", "Deployment", "EnvVar", "Service", "Project", "User" CASCADE`)
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
	return pool
}
