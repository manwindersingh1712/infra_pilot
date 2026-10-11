package logs_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

// OBS-1: the SSE history must be the newest lines, in order.
func TestReadLatest(t *testing.T) {
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

	for i := 1; i <= 250; i++ {
		if err := r.Write(ctx, logs.Entry{DeploymentID: "big", Source: "stdout", Message: fmt.Sprint(i), Timestamp: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		if err := r.Write(ctx, logs.Entry{DeploymentID: "small", Source: "stdout", Message: fmt.Sprint(i), Timestamp: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(dep string, n int64, first, last, count int) {
		t.Helper()
		got, err := r.ReadLatest(ctx, dep, n)
		if err != nil || len(got) != count {
			t.Fatalf("%s: %d entries, err %v, want %d", dep, len(got), err, count)
		}
		for i, e := range got {
			if e.Message != fmt.Sprint(first+i) {
				t.Fatalf("%s: entry %d is %q, want %d", dep, i, e.Message, first+i)
			}
		}
		if count > 0 && got[count-1].Message != fmt.Sprint(last) {
			t.Fatalf("%s: last is %q", dep, got[count-1].Message)
		}
	}
	check("big", 100, 151, 250, 100)
	check("small", 100, 1, 3, 3)
	got, err := r.ReadLatest(ctx, "missing", 100)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("missing stream: %#v %v (want empty non-nil slice)", got, err)
	}
}
