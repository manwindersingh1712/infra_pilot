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

func TestDeploymentHasStoppedAtColumn(t *testing.T) {
	p := testutil.NeedDB(t, pool)
	var n int
	err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'Deployment' AND column_name = 'stoppedAt' AND is_nullable = 'YES'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal(`"Deployment"."stoppedAt" is missing`)
	}
}

func TestFakeRunnerCopiesArgs(t *testing.T) {
	r := &testutil.FakeRunner{}
	args := []string{"run", "app"}
	_, _ = r.Run(context.Background(), "/tmp", "docker", args...)
	args[1] = "MUTATED"
	if got := r.Commands(); len(got) != 1 || got[0] != "docker run app" {
		t.Fatalf("Commands() = %v, want [docker run app]", got)
	}
}

func TestNeedDBTruncatesEveryTable(t *testing.T) {
	p := testutil.NeedDB(t, pool)
	ctx := context.Background()
	if _, err := p.Exec(ctx, `CREATE TABLE "NeedDBScratch" (id int)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = p.Exec(context.Background(), `DROP TABLE IF EXISTS "NeedDBScratch"`) })
	if _, err := p.Exec(ctx, `INSERT INTO "NeedDBScratch" VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	testutil.NeedDB(t, pool)
	var n int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM "NeedDBScratch"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("NeedDBScratch has %d rows after NeedDB, want 0", n)
	}
	var versions int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions == 0 {
		t.Fatal("NeedDB truncated goose_db_version")
	}
}
