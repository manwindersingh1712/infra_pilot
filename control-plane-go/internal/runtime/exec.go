// Package runtime runs external commands (git, docker) and manages
// containers, networks, volumes, nginx routes and container log streaming.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a command and returns its stdout. Tests use testutil.FakeRunner.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// ExecRunner runs commands without a shell, inheriting the process environment.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s",
			name, firstArg(args), err, tail(strings.TrimSpace(stderr.String()), 2000))
	}
	return stdout.String(), nil
}

// firstArg names the subcommand in errors without echoing every argument
// (arguments can contain paths and other details we don't want in logs).
func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
