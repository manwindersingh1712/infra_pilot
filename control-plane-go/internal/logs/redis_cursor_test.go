package logs_test

import (
	"context"
	"testing"
	"time"

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
	rest, _ := r.ReadAfter(ctx, "d1", cursor, 100)
	if len(rest) != 1 || rest[0].Message != "c" || rest[0].DeploymentID != "d1" {
		t.Fatalf("rest = %+v", rest)
	}
	if two, _ := r.ReadAfter(ctx, "d1", "", 2); len(two) != 2 {
		t.Fatalf("count limit ignored: %d", len(two))
	}
}
