package db

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPrismaMigrationsMatchFrozenNodeSchema(t *testing.T) {
	entries, err := os.ReadDir("../../../control-plane/prisma/migrations")
	if err != nil {
		t.Fatalf("Node migrations directory not available: %v", err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	if !slices.Equal(dirs, prismaMigrations) {
		t.Fatalf("prismaMigrations = %v, want %v", prismaMigrations, dirs)
	}
}

func TestMissingPrismaMigrations(t *testing.T) {
	all := map[string]bool{}
	for _, m := range prismaMigrations {
		all[m] = true
	}
	if got := missingPrismaMigrations(all); len(got) != 0 {
		t.Fatalf("complete history reported missing %v", got)
	}

	last := prismaMigrations[len(prismaMigrations)-1]
	delete(all, last)
	if got := missingPrismaMigrations(all); !slices.Equal(got, []string{last}) {
		t.Fatalf("got %v, want [%s]", got, last)
	}

	if got := missingPrismaMigrations(nil); !slices.Equal(got, prismaMigrations) {
		t.Fatalf("empty history: got %v, want all", got)
	}
}

func TestUnknownPrismaMigrations(t *testing.T) {
	finished := map[string]bool{}
	for _, m := range prismaMigrations {
		finished[m] = true
	}
	if got := unknownPrismaMigrations(finished); len(got) != 0 {
		t.Fatalf("frozen history reported unknown %v", got)
	}
	finished["20990101000000_zeta"] = true
	finished["20990101000000_alpha"] = true
	want := []string{"20990101000000_alpha", "20990101000000_zeta"}
	if got := unknownPrismaMigrations(finished); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestIncompleteErrUnknownSaysDiverged(t *testing.T) {
	msg := incompleteErr("unknown", []string{"20990101000000_x"}).Error()
	for _, want := range []string{"unknown: 20990101000000_x", "diverged from the frozen Node schema"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "migrate deploy") {
		t.Fatalf("unknown migrations cannot be fixed by migrate deploy: %q", msg)
	}
}

func TestMigrateNeedsTwoConnections(t *testing.T) {
	// Nothing listens on port 1: the pool is lazy, so any attempt to touch the
	// database would fail with a connection error instead of the one we want.
	pool, err := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none?pool_max_conns=1")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for name, fn := range map[string]func() error{
		"Migrate":   func() error { return Migrate(t.Context(), pool) },
		"migrateTo": func() error { return migrateTo(t.Context(), pool, 1) },
	} {
		if err := fn(); err == nil || err.Error() != "migrations need a pool with at least 2 connections" {
			t.Errorf("%s: err = %v, want the 2-connection error", name, err)
		}
	}
}
