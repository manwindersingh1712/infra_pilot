package logs_test

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

func TestReadAfterAndFlushCursor(t *testing.T) {
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
	url, _ := ctr.ConnectionString(ctx)
	r, err := logs.NewRedis(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for _, msg := range []string{"a", "b", "c"} {
		if err := r.Write(ctx, logs.Entry{DeploymentID: "d1", Source: "stdout", Message: msg, Timestamp: "t"}); err != nil {
			t.Fatal(err)
		}
	}

	cursor, err := r.FlushCursor(ctx, "d1")
	if err != nil || cursor != "" {
		t.Fatalf("fresh cursor = %q err %v", cursor, err)
	}
	all, err := r.ReadAfter(ctx, "d1", "", 100)
	if err != nil || len(all) != 3 || all[0].Message != "a" || all[0].ID == "" {
		t.Fatalf("all = %+v err %v", all, err)
	}
	if err := r.SetFlushCursor(ctx, "d1", all[1].ID); err != nil {
		t.Fatal(err)
	}
	cursor, _ = r.FlushCursor(ctx, "d1")
	if cursor != all[1].ID {
		t.Fatalf("cursor = %q, want %q", cursor, all[1].ID)
	}
	rest, _ := r.ReadAfter(ctx, "d1", cursor, 100)
	if len(rest) != 1 || rest[0].Message != "c" || rest[0].DeploymentID != "d1" {
		t.Fatalf("rest = %+v", rest)
	}
	if two, _ := r.ReadAfter(ctx, "d1", "", 2); len(two) != 2 {
		t.Fatalf("count limit ignored: %d", len(two))
	}

	// A stream entry missing fields (e.g. written by another producer) must
	// read back as "" rather than "<nil>", with the timestamp defaulted to now
	// like Node's readLogs.
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	raw := redis.NewClient(opt)
	t.Cleanup(func() { _ = raw.Close() })

	// The cursor outlives the 10m stream but still expires (15m).
	if ttl, err := raw.PTTL(ctx, "logs:flushed:d1").Result(); err != nil || ttl <= 0 || ttl > 15*time.Minute {
		t.Fatalf("cursor PTTL = %v err %v", ttl, err)
	}
	// A stream that expired (or never existed) under a live cursor reads as empty.
	if gone, err := r.ReadAfter(ctx, "gone", cursor, 100); err != nil || len(gone) != 0 {
		t.Fatalf("ReadAfter missing stream = %+v err %v", gone, err)
	}

	if err := raw.XAdd(ctx, &redis.XAddArgs{Stream: "logs:stream:sparse", Values: []any{"message", "only"}}).Err(); err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Second)
	check := func(name string, e logs.Entry) {
		t.Helper()
		if e.Message != "only" || e.Source != "" || e.DeploymentID != "sparse" {
			t.Fatalf("%s = %+v", name, e)
		}
		if ts := logs.ParseTime(e.Timestamp); e.Timestamp == "" || ts.Before(before) || ts.After(time.Now().Add(time.Second)) {
			t.Fatalf("%s timestamp = %q, want ~now", name, e.Timestamp)
		}
	}
	sparse, err := r.ReadAfter(ctx, "sparse", "", 10)
	if err != nil || len(sparse) != 1 {
		t.Fatalf("ReadAfter sparse = %+v err %v", sparse, err)
	}
	check("ReadAfter", sparse[0].Entry)
	plain, err := r.Read(ctx, "sparse", 10)
	if err != nil || len(plain) != 1 {
		t.Fatalf("Read sparse = %+v err %v", plain, err)
	}
	check("Read", plain[0])
}
