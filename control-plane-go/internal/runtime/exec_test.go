package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecRunnerReturnsStdout(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), "", "sh", "-c", "echo hello")
	if err != nil || out != "hello\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestExecRunnerErrorIncludesStderr(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), "", "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecRunnerUsesDir(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	out, err := ExecRunner{}.Run(context.Background(), dir, "pwd")
	if err != nil || strings.TrimSpace(out) != dir {
		t.Fatalf("out=%q err=%v want %s", out, err, dir)
	}
}

// STAFF-2 / SEC-4 (T16): a cancelled command reports ctx.Err(), and a
// grandchild that keeps stdout open cannot hold Run past WaitDelay.
func TestExecRunnerCancelWrapsContextErrorAndDoesNotHang(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for WaitDelay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The background sleep inherits stdout and survives the kill of sh.
	_, err := ExecRunner{}.Run(ctx, "", "sh", "-c", "sleep 30 & exec sleep 30")
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed > waitDelay+5*time.Second {
		t.Fatalf("Run took %v; WaitDelay is not applied", elapsed)
	}
}
