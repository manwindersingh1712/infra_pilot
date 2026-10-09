package logs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

func TestFormatTimeMatchesToISOString(t *testing.T) {
	got := logs.FormatTime(time.Date(2026, 3, 4, 5, 6, 7, 8_000_000, time.UTC))
	if got != "2026-03-04T05:06:07.008Z" {
		t.Fatalf("got %s", got)
	}
}

func TestParseTime(t *testing.T) {
	now := time.Now()
	if got, want := logs.ParseTime(logs.FormatTime(now)), now.Truncate(time.Millisecond); !got.Equal(want) {
		t.Fatalf("round trip = %v, want %v", got, want)
	}
	if d := time.Since(logs.ParseTime("garbage")); d < -time.Second || d > time.Second {
		t.Fatalf("ParseTime(garbage) is %v from now", d)
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

	// The stream must carry the hot-tier TTL (10m) after Write.
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	raw := redis.NewClient(opt)
	t.Cleanup(func() { _ = raw.Close() })
	if ttl, err := raw.PTTL(ctx, "logs:stream:dep1").Result(); err != nil || ttl <= 0 || ttl > 10*time.Minute {
		t.Fatalf("stream PTTL = %v err %v", ttl, err)
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

	for _, want := range []logs.Entry{e1, e2} {
		select {
		case e := <-live:
			if e != want {
				t.Fatalf("live = %+v, want %+v", e, want)
			}
		case <-ctx.Done():
			t.Fatalf("no live entry %+v", want)
		}
	}

	streams, err := r.ActiveStreams(ctx)
	if err != nil || len(streams) != 1 || streams[0] != "dep1" {
		t.Fatalf("ActiveStreams = %v %v", streams, err)
	}

	// An empty timestamp is defaulted once at write time: every Read and every
	// live viewer sees the same value.
	if err := r.Write(ctx, logs.Entry{DeploymentID: "dep1", Source: "stdout", Message: "no-ts"}); err != nil {
		t.Fatal(err)
	}
	first, err := r.Read(ctx, "dep1", 100)
	if err != nil || len(first) != 3 {
		t.Fatalf("Read after no-ts write = %+v err %v", first, err)
	}
	time.Sleep(5 * time.Millisecond) // a read-time default would now differ
	second, err := r.Read(ctx, "dep1", 100)
	if err != nil || len(second) != 3 {
		t.Fatalf("second Read = %+v err %v", second, err)
	}
	ts := first[2].Timestamp
	if ts == "" || second[2].Timestamp != ts {
		t.Fatalf("timestamp not stable: %q then %q", ts, second[2].Timestamp)
	}
	select {
	case e := <-live:
		if e.Message != "no-ts" || e.Timestamp != ts {
			t.Fatalf("live = %+v, want timestamp %q", e, ts)
		}
	case <-ctx.Done():
		t.Fatal("no live entry for no-ts write")
	}

	// A quiet subscription must close when its ctx is cancelled, and stop
	// must stay safe to call (twice) afterwards.
	subCtx, subCancel := context.WithCancel(ctx)
	quiet, quietStop, err := r.Subscribe(subCtx, "quiet")
	if err != nil {
		t.Fatal(err)
	}
	subCancel()
	select {
	case _, ok := <-quiet:
		if ok {
			t.Fatal("unexpected entry on quiet channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after ctx cancel")
	}
	quietStop()
	quietStop()
}
