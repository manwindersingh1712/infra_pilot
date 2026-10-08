// Package testutil holds helpers shared by tests: throwaway containers, fixtures and fakes.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"infrapilot/internal/db"
)

// StartEmptyPostgres runs postgres:16 in Docker and returns the URL of its
// (empty, unmigrated) database. The cleanup func terminates the container.
func StartEmptyPostgres(ctx context.Context) (string, func(), error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("cp_test"),
		tcpostgres.WithUsername("cp"),
		tcpostgres.WithPassword("cp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return "", nil, fmt.Errorf("start postgres container: %w", err)
	}
	terminate := func() { _ = ctr.Terminate(context.Background()) }
	adminURL, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		terminate()
		return "", nil, err
	}
	return adminURL, terminate, nil
}

// StartPostgres runs postgres:16 in Docker and applies the migrations.
func StartPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	url, terminate, err := StartEmptyPostgres(ctx)
	if err != nil {
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

// RunWithEmptyPostgres is a TestMain body for tests that need their own
// databases. It starts Postgres unless -short is set, stores the admin URL in
// *url (left empty under -short), runs the tests and exits.
func RunWithEmptyPostgres(m *testing.M, url *string) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	u, cleanup, err := StartEmptyPostgres(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	*url = u
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// FreshDB creates a uniquely named, empty database on the server behind
// adminURL, connects to it with db.Connect and drops it when the test ends.
// It skips the test when adminURL is empty (-short).
func FreshDB(t *testing.T, adminURL string) (string, *pgxpool.Pool) {
	t.Helper()
	if adminURL == "" {
		t.Skip("integration test: needs Docker; run without -short")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(b[:])
	adminExec(t, adminURL, `CREATE DATABASE `+pgx.Identifier{name}.Sanitize())

	u, err := neturl.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse admin URL: %v", err)
	}
	u.Path = "/" + name
	pool, err := db.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(func() {
		pool.Close()
		adminExec(t, adminURL, `DROP DATABASE `+pgx.Identifier{name}.Sanitize()+` WITH (FORCE)`)
	})
	return u.String(), pool
}

func adminExec(t *testing.T, adminURL, sql string) {
	t.Helper()
	ctx := context.Background()
	admin, err := db.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
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
