package db

import (
	"os"
	"slices"
	"testing"
)

func TestPrismaMigrationsMatchFrozenNodeSchema(t *testing.T) {
	entries, err := os.ReadDir("../../../control-plane/prisma/migrations")
	if err != nil {
		t.Skipf("Node migrations directory not available: %v", err)
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
