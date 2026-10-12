// Command migrate manages the Postgres schema: `migrate up`, `migrate status`, `migrate baseline` or
// `migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]`.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/pressly/goose/v3"

	"infrapilot/internal/db"
)

func main() { os.Exit(realMain(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

// realMain runs the command and returns the process exit code.
func realMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := run(ctx, args, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
	}
	return exitCode(err)
}

// loadDotenv loads ./.env if present and says which file it was (path only):
// a stray .env in the working directory can silently select the database.
func loadDotenv(w io.Writer) {
	if err := godotenv.Load(); err != nil {
		return
	}
	if wd, err := os.Getwd(); err == nil {
		fmt.Fprintf(w, "loaded env file: %s (values not shown)\n", filepath.Join(wd, ".env"))
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) >= 1 && args[0] == "encrypt-env" {
		loadDotenv(stdout)
		// Validates the key before it connects to the database.
		return runEncryptEnv(ctx, args[1:], os.Getenv, connectDB, stdout)
	}
	_ = godotenv.Load()
	if len(args) != 1 {
		return errors.New("usage: migrate up|status|baseline, or migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch args[0] {
	case "up":
		return db.Migrate(ctx, pool)
	case "status":
		statuses, err := db.Status(ctx, pool)
		if err != nil {
			return err
		}
		return printStatus(stdout, statuses)
	case "baseline":
		return db.MarkBaseline(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (want up|status|baseline|encrypt-env)", args[0])
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
