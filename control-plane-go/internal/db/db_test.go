package db

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNormalizeURLStripsPrismaSchemaParam(t *testing.T) {
	got, schema, err := normalizeURL("postgresql://cp:cp@localhost:5432/control_plane?schema=public")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgresql://cp:cp@localhost:5432/control_plane" || schema != `"public"` {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLKeepsOtherParams(t *testing.T) {
	got, schema, err := normalizeURL("postgres://h/db?sslmode=disable&schema=app")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://h/db?sslmode=disable" || schema != `"app"` {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLWithoutSchema(t *testing.T) {
	got, schema, err := normalizeURL("postgres://h/db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://h/db" || schema != "" {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLParseErrorsDoNotLeakPassword(t *testing.T) {
	cases := []struct{ raw, pw string }{
		{"postgresql://cp:FAKEPW123@localhost:bad/x", "FAKEPW123"},
		{"postgresql://cp:pa%ss@localhost/cp", "pa%ss"},
		{"postgresql://cp:pa%ss@localhost/cp", "%ss"},
		{"postgres://user:Zq9SECRET@[::1/db", "Zq9SECRET"},
		{"postgres://user:Ab%zzHIDDEN@host:5432/db", "HIDDEN"},
	}
	for _, tc := range cases {
		_, _, err := normalizeURL(tc.raw)
		if err == nil {
			t.Errorf("normalizeURL(%q): expected error", tc.raw)
			continue
		}
		if strings.Contains(err.Error(), tc.pw) {
			t.Errorf("normalizeURL(%q) error leaks %q: %v", tc.raw, tc.pw, err)
		}
	}
}

func TestNormalizeURLPassesKeywordValueDSNUnchanged(t *testing.T) {
	const dsn = "host=localhost port=5432 user=cp password=S3CRETPW dbname=x sslmode=disable"
	got, schema, err := normalizeURL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if got != dsn || schema != "" {
		t.Fatalf("got %q, schema %q; want input unchanged", got, schema)
	}
	cfg, err := pgxpool.ParseConfig(got)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	cc := cfg.ConnConfig
	if cc.Host != "localhost" || cc.Port != 5432 || cc.User != "cp" || cc.Database != "x" || cc.Password != "S3CRETPW" {
		t.Fatalf("parsed host=%q port=%d user=%q db=%q", cc.Host, cc.Port, cc.User, cc.Database)
	}
}

func TestNormalizeURLOnlyRewritesPostgresSchemes(t *testing.T) {
	for _, raw := range []string{
		"POSTGRES://h/db?schema=app&sslmode=disable",
		"PostgreSQL://h/db?schema=app",
	} {
		got, schema, err := normalizeURL(raw)
		if err != nil {
			t.Fatal(err)
		}
		if schema != `"app"` || strings.Contains(got, "schema=") {
			t.Errorf("normalizeURL(%q) = %q, schema %q", raw, got, schema)
		}
	}
}

func TestPoolConfigParseErrorsDoNotLeakPassword(t *testing.T) {
	// pgx 5.11 accepts `password = X`, `password=X\ Y` and `Password=X` as
	// written (libpq syntax, and an unknown runtime parameter), so those
	// inputs carry `sslmode=bogus` to force a ParseConfig error; pgx's own
	// error then echoes them because only a well-formed `password=` token is
	// redacted.
	cases := []struct{ dsn, secret string }{
		{`host=h password = PW1X sslmode=bogus`, "PW1X"},
		{`host=h password=PW2\ SECRET2X sslmode=bogus`, "SECRET2X"},
		{`host=h password='PW3X`, "PW3X"},
		{`host=h connect_timeout='2 password=PW4X`, "PW4X"},
		{`host=h Password=PW5X sslmode=bogus`, "PW5X"},
	}
	for _, tc := range cases {
		_, err := poolConfig(tc.dsn)
		if err == nil {
			t.Errorf("poolConfig(%q): expected error", tc.dsn)
			continue
		}
		msg := err.Error()
		if strings.Contains(msg, tc.secret) || strings.Contains(strings.ToLower(msg), "password") {
			t.Errorf("poolConfig(%q) error leaks: %v", tc.dsn, err)
		}
		if msg != "parse DATABASE_URL: invalid connection string" {
			t.Errorf("poolConfig(%q) error = %q, want the fixed message", tc.dsn, msg)
		}
	}
}

func TestPoolConfigDefaultsConnectTimeout(t *testing.T) {
	cfg, err := poolConfig("postgres://h/db")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.ConnectTimeout; got != 10*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 10s", got)
	}
	if got := cfg.ConnConfig.RuntimeParams["timezone"]; got != "UTC" {
		t.Fatalf("timezone = %q, want UTC", got)
	}
}

func TestPoolConfigKeepsExplicitConnectTimeout(t *testing.T) {
	cfg, err := poolConfig("postgres://h/db?connect_timeout=3")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.ConnectTimeout; got != 3*time.Second {
		t.Fatalf("ConnectTimeout = %v, want 3s", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := []struct {
		name, raw, want, schema, err string
	}{
		{name: "mixed-case schema is quoted", raw: "postgres://h/db?schema=MySchema",
			want: "postgres://h/db", schema: `"MySchema"`},
		{name: "quote in schema is escaped", raw: `postgres://h/db?schema=a"b`,
			want: "postgres://h/db", schema: `"a""b"`},
		{name: "two schema params", raw: "postgres://h/db?schema=a&schema=b",
			err: "DATABASE_URL has more than one schema parameter"},
		{name: "prisma-only params removed",
			raw:  "postgres://h/db?connection_limit=5&pool_timeout=10&socket_timeout=3&pgbouncer=true&sslmode=disable",
			want: "postgres://h/db?sslmode=disable"},
		{name: "prisma-only params removed with schema",
			raw:  "postgresql://u:p@h:5432/db?schema=public&connection_limit=1",
			want: "postgresql://u:p@h:5432/db", schema: `"public"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, schema, err := normalizeURL(tc.raw)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want || schema != tc.schema {
				t.Fatalf("got %q, schema %q; want %q, schema %q", got, schema, tc.want, tc.schema)
			}
		})
	}
}

func TestPoolConfigSetsQuotedSearchPath(t *testing.T) {
	cfg, err := poolConfig("postgres://h/db?schema=MySchema&connection_limit=3")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ConnConfig.RuntimeParams["search_path"]; got != `"MySchema"` {
		t.Fatalf("search_path = %q", got)
	}
	if _, ok := cfg.ConnConfig.RuntimeParams["connection_limit"]; ok {
		t.Fatal("connection_limit reached pgx as a runtime param")
	}
}
