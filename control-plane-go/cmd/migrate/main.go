// Command migrate manages the Postgres schema: `migrate up`, `migrate status`, `migrate baseline` or
// `migrate encrypt-env [--dry-run]`.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"

	"infrapilot/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	if len(os.Args) >= 2 && os.Args[1] == "encrypt-env" {
		// Validates the key before it connects to the database.
		return runEncryptEnv(context.Background(), os.Args[2:], os.Getenv, connectDB, os.Stdout)
	}
	if len(os.Args) != 2 {
		return errors.New("usage: migrate up|status|baseline|encrypt-env [--dry-run]")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "up":
		return db.Migrate(ctx, pool)
	case "status":
		statuses, err := db.Status(ctx, pool)
		if err != nil {
			return err
		}
		return printStatus(os.Stdout, statuses)
	case "baseline":
		return db.MarkBaseline(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (want up|status|baseline|encrypt-env)", os.Args[1])
	}
}

// connectDB opens the pool and returns the func that closes it.
func connectDB(ctx context.Context, url string) (*pgxpool.Pool, func(), error) {
	pool, err := db.Connect(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	return pool, pool.Close, nil
}

// printStatus writes one line per migration: "<version> <file> applied <time>"
// or "<version> <file> pending".
func printStatus(w io.Writer, statuses []*goose.MigrationStatus) error {
	for _, s := range statuses {
		line := fmt.Sprintf("%05d %s pending", s.Source.Version, path.Base(s.Source.Path))
		if s.State == goose.StateApplied {
			line = fmt.Sprintf("%05d %s applied %s", s.Source.Version, path.Base(s.Source.Path),
				s.AppliedAt.UTC().Format(time.RFC3339))
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
}
