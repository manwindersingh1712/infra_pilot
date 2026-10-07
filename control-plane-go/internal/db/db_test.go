package db

import (
	"strings"
	"testing"
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
