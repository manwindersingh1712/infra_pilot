package logs

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
)

func TestBuildQueryUsesBoundParameters(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sql, args := buildQuery(Query{
		DeploymentID: "d'1", Start: &start, Sources: []string{"stdout"}, Search: "err'or", Limit: 50, Offset: 10,
	})
	wantSQL := "SELECT deployment_id, source, message, timestamp FROM container_logs" +
		" WHERE deployment_id = ? AND timestamp >= ? AND has(?, source) AND message ILIKE ?" +
		" ORDER BY timestamp DESC LIMIT 50 OFFSET 10"
	if sql != wantSQL {
		t.Fatalf("sql:\n%s\nwant:\n%s", sql, wantSQL)
	}
	wantArgs := []any{"d'1", start, []string{"stdout"}, "%err'or%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildQueryDefaultsLimit(t *testing.T) {
	sql, _ := buildQuery(Query{DeploymentID: "d"})
	if want := "SELECT deployment_id, source, message, timestamp FROM container_logs WHERE deployment_id = ? ORDER BY timestamp DESC LIMIT 100 OFFSET 0"; sql != want {
		t.Fatalf("sql = %s", sql)
	}
}

func TestNewClickHouseParseErrorHidesCredentials(t *testing.T) {
	for _, raw := range []string{
		"http://u:s3cretpw@ho st:8123",
		"http://user:s3cretpw@host:99999/%zz",
	} {
		_, err := NewClickHouse(raw, "u", "p")
		if err == nil {
			t.Fatalf("NewClickHouse(%q): want error", raw)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("error leaks credentials: %v", err)
		}
	}
}

func TestClickHouseRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24.8-alpine",
		tcclickhouse.WithUsername("test"), tcclickhouse.WithPassword("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	host, _ := ctr.Host(ctx)
	port, err := ctr.MappedPort(ctx, "8123/tcp")
	if err != nil {
		t.Fatal(err)
	}

	ch, err := NewClickHouse("http://"+host+":"+port.Port(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Init(ctx); err != nil {
		t.Fatal(err)
	}
	// Timestamps must be recent: the table's 30-day TTL drops older rows.
	base := time.Now().UTC().Truncate(time.Second)
	entries := []Entry{
		{DeploymentID: "d1", Source: "stdout", Message: "first", Timestamp: FormatTime(base.Add(1 * time.Millisecond))},
		{DeploymentID: "d1", Source: "stderr", Message: "it's an error", Timestamp: FormatTime(base.Add(2 * time.Millisecond))},
		{DeploymentID: "d2", Source: "stdout", Message: "other", Timestamp: FormatTime(base.Add(3 * time.Millisecond))},
	}
	if err := ch.Insert(ctx, entries); err != nil {
		t.Fatal(err)
	}

	got, err := ch.Query(ctx, Query{DeploymentID: "d1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != entries[0] || got[1] != entries[1] {
		t.Fatalf("Query = %+v", got)
	}
	errs, _ := ch.Query(ctx, Query{DeploymentID: "d1", Search: "it's", Sources: []string{"stderr"}})
	if len(errs) != 1 || errs[0].Message != "it's an error" {
		t.Fatalf("filtered = %+v", errs)
	}
	if n, err := ch.Count(ctx, "d1"); err != nil || n != 2 {
		t.Fatalf("Count = %d %v", n, err)
	}
}
