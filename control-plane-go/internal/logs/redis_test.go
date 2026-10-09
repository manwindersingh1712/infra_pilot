package logs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

func TestFormatTimeMatchesToISOString(t *testing.T) {
	got := logs.FormatTime(time.Date(2026, 3, 4, 5, 6, 7, 8_000_000, time.UTC))
	if got != "2026-03-04T05:06:07.008Z" {
		t.Fatalf("got %s", got)
	}
}

func TestEntryJSONShape(t *testing.T) {
	b, _ := json.Marshal(logs.Entry{DeploymentID: "d", Source: "stdout", Message: "m", Timestamp: "t"})
	if string(b) != `{"deploymentId":"d","source":"stdout","message":"m","timestamp":"t"}` {
		t.Fatalf("got %s", b)
	}
}

func TestNewRedisParseErrorHidesPassword(t *testing.T) {
	for _, u := range []string{
		"redis://u:s3cretPW@host:badport",
		"redis://:s3cret PW%zz@host",
	} {
		_, err := logs.NewRedis(u)
		if err == nil {
			t.Fatalf("NewRedis(%q): want error", u)
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Fatalf("error leaks password: %v", err)
		}
	}
}

func TestRedis(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	url, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := logs.NewRedis(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	live, stop, err := r.Subscribe(ctx, "dep1")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	e1 := logs.Entry{DeploymentID: "dep1", Source: "stdout", Message: "hello", Timestamp: "2026-01-01T00:00:00.000Z"}
	e2 := logs.Entry{DeploymentID: "dep1", Source: "stderr", Message: "oops", Timestamp: "2026-01-01T00:00:01.000Z"}
	for _, e := range []logs.Entry{e1, e2} {
		if err := r.Write(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.Read(ctx, "dep1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != e1 || got[1] != e2 {
		t.Fatalf("Read = %+v", got)
	}
	if one, _ := r.Read(ctx, "dep1", 1); len(one) != 1 || one[0] != e1 {
		t.Fatalf("Read(count=1) = %+v", one)
	}
	if empty, err := r.Read(ctx, "missing", 10); err != nil || len(empty) != 0 {
		t.Fatalf("missing stream: %v %v", empty, err)
	}

	select {
	case e := <-live:
		if e != e1 {
			t.Fatalf("live = %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("no live entry")
	}

	streams, err := r.ActiveStreams(ctx)
	if err != nil || len(streams) != 1 || streams[0] != "dep1" {
		t.Fatalf("ActiveStreams = %v %v", streams, err)
	}
}
