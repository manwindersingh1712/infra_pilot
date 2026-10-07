package db

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNormalizeURLStripsPrismaSchemaParam(t *testing.T) {
	got, schema, err := normalizeURL("postgresql://cp:cp@localhost:5432/control_plane?schema=public")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgresql://cp:cp@localhost:5432/control_plane" || schema != "public" {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLKeepsOtherParams(t *testing.T) {
	got, schema, err := normalizeURL("postgres://h/db?sslmode=disable&schema=app")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://h/db?sslmode=disable" || schema != "app" {
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
		if schema != "app" || strings.Contains(got, "schema=") {
			t.Errorf("normalizeURL(%q) = %q, schema %q", raw, got, schema)
		}
	}
}
