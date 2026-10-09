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

// SEC-1 (T16): a command that floods stdout/stderr cannot grow the worker's
// memory without bound, and the cap itself is not an error.
func TestExecRunnerCapsStdout(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), "", "head", "-c", "8388608", "/dev/zero")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(out) > maxStdout {
		t.Fatalf("stdout = %d bytes, cap %d", len(out), maxStdout)
	}
	if !strings.HasSuffix(out, truncatedNote) {
		t.Fatal("truncated stdout must end with the truncation note")
	}
}

func TestExecRunnerKeepsStderrTail(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), "", "sh", "-c", "head -c 8388608 /dev/zero >&2; echo LAST >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "LAST") || len(err.Error()) > 2300 {
		t.Fatalf("err len %d: want the stderr tail containing LAST", len(err.Error()))
	}
}

func TestCappedWriters(t *testing.T) {
	h := &headBuffer{max: 10}
	for _, p := range []string{"abcdef", "ghijkl", "mn"} {
		if n, err := h.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("head write n=%d err=%v", n, err)
		}
	}
	if h.String() != "abcdefghij" || !h.truncated {
		t.Fatalf("head = %q truncated=%v", h.String(), h.truncated)
	}
	tb := &tailBuffer{max: 5}
	for _, p := range []string{"abc", "defghij", "kl"} {
		if n, err := tb.Write([]byte(p)); n != len(p) || err != nil {
			t.Fatalf("tail write n=%d err=%v", n, err)
		}
	}
	if tb.String() != "hijkl" {
		t.Fatalf("tail = %q", tb.String())
	}
}
