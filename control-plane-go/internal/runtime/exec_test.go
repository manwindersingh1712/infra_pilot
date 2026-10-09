package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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
