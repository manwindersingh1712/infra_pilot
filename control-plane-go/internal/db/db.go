// Package db opens the Postgres pool and applies schema migrations.
package db

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool. Sessions run in UTC because the schema stores UTC
// values in TIMESTAMP(3) WITHOUT TIME ZONE columns.
func Connect(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	connURL, schema, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(connURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// normalizeURL removes Prisma's ?schema= parameter, which Postgres rejects as
// an unknown runtime parameter, and returns it separately.
func normalizeURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	q := u.Query()
	schema := q.Get("schema")
	q.Del("schema")
	u.RawQuery = q.Encode()
	return u.String(), schema, nil
}
