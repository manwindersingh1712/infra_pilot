package testutil_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

func TestMigrationsCreateSchema(t *testing.T) {
	p := testutil.NeedDB(t, pool)
	var n int
	err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('User','Project','Service','EnvVar','Deployment','OutboxEvent','ServiceConnection')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("found %d of 7 tables", n)
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, _ = r.Run(context.Background(), "/tmp", "docker", "ps")
	if got := r.Commands(); len(got) != 1 || got[0] != "docker ps" {
		t.Fatalf("Commands() = %v", got)
	}
}
