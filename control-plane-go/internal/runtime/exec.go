// Package runtime runs external commands (git, docker) and manages
// containers, networks, volumes, nginx routes and container log streaming.
package runtime

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// waitDelay bounds how long Run waits for stdout/stderr to close after the
// process exits or is killed (a grandchild can keep the pipes open).
const waitDelay = 5 * time.Second

// Runner executes a command and returns its stdout. Tests use testutil.FakeRunner.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// ExecRunner runs commands without a shell, inheriting the process environment.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	// SEC-1: bounded buffers, so a flood of output (a tenant build) cannot OOM the worker.
	stdout := &headBuffer{max: maxStdout}
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Callers (Temporal activities) need errors.Is(err, context.Canceled).
			err = fmt.Errorf("%w (%w)", ctxErr, err)
		}
		return stdout.result(), fmt.Errorf("%s %s: %w: %s",
			name, firstArg(args), err, tail(strings.TrimSpace(stderr.String()), 2000))
	}
	return stdout.result(), nil
}

// firstArg names the subcommand in errors without echoing every argument
// (arguments can contain paths and other details we don't want in logs).
// Leading "-c k=v" pairs (git config) are skipped.
func firstArg(args []string) string {
	i := 0
	for i+1 < len(args) && args[i] == "-c" { // git -c k=v pairs
		i += 2
	}
	if i < len(args) {
		return args[i]
	}
	if len(args) == 1 {
		return args[0]
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

const (
	maxStdout = 1 << 20  // keep the first 1 MiB of stdout
	maxStderr = 64 << 10 // keep the last 64 KiB of stderr
)

// truncatedNote ends stdout that was cut at maxStdout.
const truncatedNote = "\n[output truncated]\n"

// headBuffer keeps the first max bytes written and drops the rest. Write
// never fails, so the command is not killed by a short write.
type headBuffer struct {
	buf       []byte
	max       int
	truncated bool
}

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
		if len(p) > room {
			h.truncated = true
		}
	} else if len(p) > 0 {
		h.truncated = true
	}
	return len(p), nil
}

func (h *headBuffer) String() string { return string(h.buf) }

// result returns the kept output; truncated output ends with truncatedNote
// and stays within max bytes.
func (h *headBuffer) result() string {
	if !h.truncated {
		return string(h.buf)
	}
	keep := max(h.max-len(truncatedNote), 0)
	return string(h.buf[:min(keep, len(h.buf))]) + truncatedNote
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	t.buf = append(t.buf, p...)
	return n, nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
