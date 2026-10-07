// Command migrate manages the Postgres schema: `migrate up`, `migrate status` or `migrate baseline`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

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
	if len(os.Args) != 2 {
		return errors.New("usage: migrate up|status|baseline")
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
		return db.Status(ctx, pool)
	case "baseline":
		return db.MarkBaseline(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (want up|status|baseline)", os.Args[1])
	}
}
