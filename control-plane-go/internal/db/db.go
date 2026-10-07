// Package db opens the Postgres pool and applies schema migrations.
package db

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

var errInvalidURL = errors.New("parse DATABASE_URL: invalid URL")

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
// an unknown runtime parameter, and returns it separately. Only postgres:// and
// postgresql:// URLs are rewritten; anything else (such as a keyword/value DSN)
// is returned unchanged for pgx to parse, because round-tripping it through
// url.Parse mangles it and pgx redacts passwords in its own errors.
func normalizeURL(raw string) (string, string, error) {
	if !isPostgresURL(raw) {
		return raw, "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Error embeds the raw URL (and EscapeError part of it), which
		// may contain the password, so the cause is deliberately dropped.
		return "", "", errInvalidURL
	}
	q := u.Query()
	schema := q.Get("schema")
	q.Del("schema")
	u.RawQuery = q.Encode()
	return u.String(), schema, nil
}

func isPostgresURL(raw string) bool {
	scheme, _, ok := strings.Cut(raw, "://")
	return ok && (strings.EqualFold(scheme, "postgres") || strings.EqualFold(scheme, "postgresql"))
}
