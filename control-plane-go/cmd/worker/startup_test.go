package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/metrics"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

func TestCheckGitVersion(t *testing.T) {
	cases := []struct {
		out     string
		wantErr bool
	}{
		{"git version 2.39.5 (Apple Git-154)\n", false},
		{"git version 2.43.0", false},
		{"git version 2.30.0", false},
		{"git version 3.0.1", false},
		{"git version 2.29.9", true},
		{"git version 1.9.5", true},
		{"", true},
		{"not git at all", true},
	}
	for _, c := range cases {
		v, err := checkGitVersion(c.out)
		if (err != nil) != c.wantErr {
			t.Errorf("checkGitVersion(%q) = %q, %v; wantErr %v", c.out, v, err, c.wantErr)
		}
	}
	if v, _ := checkGitVersion("git version 2.39.5 (Apple Git-154)"); v != "2.39.5" {
		t.Errorf("version = %q", v)
	}
}

func TestPrepareEnvDisablesGitPrompts(t *testing.T) {
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	if err := prepareEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GIT_TERMINAL_PROMPT"); got != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
}

func fullDeps(t *testing.T) activityDeps {
	t.Helper()
	box, err := secrets.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return activityDeps{
		Store: &store.Store{}, Secrets: box, Docker: nopDocker{}, Router: nopRouter{}, Builder: nopBuilder{},
		Logs: nopLogs{}, Metrics: metrics.NewWorker(), Log: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		Cfg: workflows.ActivityConfig{HealthTimeout: time.Minute},
	}
}

func TestNewActivitiesRejectsNilFields(t *testing.T) {
	acts, err := newActivities(fullDeps(t))
	if err != nil {
		t.Fatal(err)
	}
	if acts.Logs == nil || acts.Metrics == nil || acts.Log == nil {
		t.Fatal("Logs, Metrics and Log must be set")
	}
	if acts.Cfg.HealthInterval != healthInterval || acts.Cfg.HealthTimeout != time.Minute {
		t.Fatalf("health config = %+v", acts.Cfg)
	}
	for name, mut := range map[string]func(*activityDeps){
		"Store":   func(d *activityDeps) { d.Store = nil },
		"Secrets": func(d *activityDeps) { d.Secrets = nil },
		"Docker":  func(d *activityDeps) { d.Docker = nil },
		"Router":  func(d *activityDeps) { d.Router = nil },
		"Builder": func(d *activityDeps) { d.Builder = nil },
		"Logs":    func(d *activityDeps) { d.Logs = nil },
		"Metrics": func(d *activityDeps) { d.Metrics = nil },
		"Log":     func(d *activityDeps) { d.Log = nil },
	} {
		d := fullDeps(t)
		mut(&d)
		if _, err := newActivities(d); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("nil %s: err = %v", name, err)
		}
	}
	d := fullDeps(t)
	d.Cfg.HealthTimeout = 0
	if _, err := newActivities(d); err == nil {
		t.Error("zero HealthTimeout must be rejected")
	}
}

func TestSupervisorLogsExitAndCancels(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := newSupervisor(slog.New(slog.NewTextHandler(&buf, nil)), cancel)
	sup.Start(ctx, "failing loop", func() error { return errors.New("boom") })
	sup.Wait()
	if ctx.Err() == nil {
		t.Fatal("a failing loop must cancel the root context")
	}
	if out := buf.String(); !strings.Contains(out, "failing loop stopped") || !strings.Contains(out, "boom") {
		t.Fatalf("log = %s", out)
	}

	// A loop that returns nil while the process is still running is a bug:
	// log it and stop the process.
	buf.Reset()
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	sup2 := newSupervisor(slog.New(slog.NewTextHandler(&buf, nil)), cancel2)
	sup2.Start(ctx2, "early", func() error { return nil })
	sup2.Wait()
	if ctx2.Err() == nil || !strings.Contains(buf.String(), "early exited unexpectedly") {
		t.Fatalf("ctx err = %v, log = %s", ctx2.Err(), buf.String())
	}

	// A loop that ends because the context ended is a normal shutdown: logged, no error level.
	buf.Reset()
	ctx3, cancel3 := context.WithCancel(context.Background())
	sup3 := newSupervisor(slog.New(slog.NewTextHandler(&buf, nil)), cancel3)
	sup3.Start(ctx3, "outbox starter", func() error { <-ctx3.Done(); return nil })
	cancel3()
	sup3.Wait()
	if out := buf.String(); !strings.Contains(out, "outbox starter stopped") || strings.Contains(out, "level=ERROR") {
		t.Fatalf("log = %s", out)
	}
}

func TestComposeTemporalIsLoopbackOnly(t *testing.T) {
	b, err := os.ReadFile("../../../control-plane/docker-compose.yml")
	if err != nil {
		t.Skipf("compose file not found: %v", err)
	}
	inService, inPorts, seen := false, false, 0
	for _, l := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(l)
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case indent == 2 && strings.HasSuffix(trimmed, ":"):
			inService, inPorts = trimmed == "temporal:", false
		case inService && indent == 4:
			inPorts = trimmed == "ports:"
		case inService && inPorts && strings.HasPrefix(trimmed, "- "):
			seen++
			if !strings.HasPrefix(trimmed, `- "127.0.0.1:`) {
				t.Errorf("temporal port is not bound to loopback: %s", trimmed)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no temporal port mappings found")
	}
}

type nopDocker struct{ workflows.ContainerRuntime }
type nopRouter struct{ workflows.Router }
type nopBuilder struct{ workflows.ImageBuilder }
type nopLogs struct{}

func (nopLogs) Start(string, string) {}

func TestResumeLoopDoesNotBlockOthersOrCancel(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup := newSupervisor(slog.New(slog.NewTextHandler(&buf, nil)), cancel)

	release := make(chan struct{})
	sup.Start(ctx, "resume", resumeLoop(ctx, slog.New(slog.NewTextHandler(&buf, nil)), func() int {
		<-release // a hung Docker daemon
		return 3
	}))
	other := make(chan struct{})
	sup.Start(ctx, "other", func() error { close(other); <-ctx.Done(); return nil })
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a slow resume delayed the other loops")
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("a finished resume must not cancel the root context")
	}
	cancel()
	sup.Wait()
	out := buf.String()
	if !strings.Contains(out, "count=3") || strings.Contains(out, "exited unexpectedly") {
		t.Fatalf("log = %s", out)
	}
}
