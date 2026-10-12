// Package db opens the Postgres pool and applies schema migrations.
package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	errInvalidURL      = errors.New("parse DATABASE_URL: invalid URL")
	errDuplicateSchema = errors.New("DATABASE_URL has more than one schema parameter")
	// errInvalidConnString replaces every pgxpool.ParseConfig error, whose
	// text can echo a malformed DSN including its password.
	errInvalidConnString = errors.New("parse DATABASE_URL: invalid connection string")
)

// connectTimeout bounds the TCP/TLS dial (unless DATABASE_URL sets
// connect_timeout) and the initial Ping, so a bad host fails fast.
const connectTimeout = 10 * time.Second

// Connect opens a pool. Sessions run in UTC because the schema stores UTC
// values in TIMESTAMP(3) WITHOUT TIME ZONE columns.
func Connect(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	cfg, err := poolConfig(rawURL)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// poolConfig turns DATABASE_URL into a pool config: Prisma-only parameters
// removed, sessions in UTC, search_path from ?schema=, and a default connect
// timeout. Parse errors never include the input.
func poolConfig(rawURL string) (*pgxpool.Config, error) {
	connURL, schema, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(connURL)
	if err != nil {
		return nil, errInvalidConnString
	}
	if cfg.ConnConfig.ConnectTimeout == 0 {
		cfg.ConnConfig.ConnectTimeout = connectTimeout
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}
	return cfg, nil
}

var (
	targetHostRe   = regexp.MustCompile(`^[A-Za-z0-9._\-]+$`)
	targetSocketRe = regexp.MustCompile(`^/[A-Za-z0-9._\-/]*$`)
	targetDBRe     = regexp.MustCompile(`^[A-Za-z0-9_.\-]+$`)
)

// Target renders "host:port[,host:port...]/dbname" from what pgx will really
// connect to: the resolved config (URL query overrides, key=value DSNs, PG*
// environment defaults, unix socket hosts and every multi-host fallback), not
// the raw URL text. It returns ok=false when the config cannot be parsed or a
// part does not look like a plain host or database name. It never includes the
// user, the password or any query value.
func Target(rawURL string) (string, bool) {
	cfg, err := poolConfig(rawURL)
	if err != nil {
		return "", false
	}
	cc := cfg.ConnConfig
	type hp struct {
		host string
		port uint16
	}
	hosts := []hp{{cc.Host, cc.Port}}
	for _, fb := range cc.Fallbacks {
		hosts = append(hosts, hp{fb.Host, fb.Port})
	}
	if !targetDBRe.MatchString(cc.Database) {
		return "", false
	}
	var parts []string
	seen := map[string]bool{}
	for _, h := range hosts {
		var host string
		switch {
		case targetSocketRe.MatchString(h.host), targetHostRe.MatchString(h.host):
			host = h.host
		case strings.Contains(h.host, ":") && net.ParseIP(h.host) != nil:
			host = "[" + h.host + "]"
		default:
			return "", false
		}
		s := fmt.Sprintf("%s:%d", host, h.port)
		if !seen[s] {
			seen[s] = true
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ",") + "/" + cc.Database, true
}

// normalizeURL removes Prisma's ?schema= parameter and the other Prisma-only
// parameters, which Postgres rejects as unknown runtime parameters, and
// returns the schema separately, quoted for use as search_path. Only postgres:// and
// postgresql:// URLs are rewritten; anything else (such as a keyword/value DSN)
// is returned unchanged for pgx to parse, because round-tripping it through
// url.Parse mangles it. pgx only redacts a well-formed `password=` token in its
// parse errors (not `password = x`, `Password=x` or escaped values), which is
// why poolConfig drops the ParseConfig cause.
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
	if len(q["schema"]) > 1 {
		return "", "", errDuplicateSchema
	}
	schema := q.Get("schema")
	q.Del("schema")
	for _, p := range prismaOnlyParams {
		q.Del(p)
	}
	u.RawQuery = q.Encode()
	if schema != "" {
		// Prisma treats schema= as a case-sensitive identifier.
		schema = pgx.Identifier{schema}.Sanitize()
	}
	return u.String(), schema, nil
}

// prismaOnlyParams are Prisma connection-string options that Postgres would
// reject as unknown runtime parameters.
var prismaOnlyParams = []string{"connection_limit", "pool_timeout", "socket_timeout", "pgbouncer"}

func isPostgresURL(raw string) bool {
	scheme, _, ok := strings.Cut(raw, "://")
	return ok && (strings.EqualFold(scheme, "postgres") || strings.EqualFold(scheme, "postgresql"))
}
