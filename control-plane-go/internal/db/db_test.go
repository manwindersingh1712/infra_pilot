package db

import "testing"

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
