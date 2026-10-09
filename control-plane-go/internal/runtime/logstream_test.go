package runtime

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"infrapilot/internal/logs"
)

type memorySink struct {
	mu      sync.Mutex
	entries []logs.Entry
}

func (m *memorySink) Write(_ context.Context, e logs.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *memorySink) all() []logs.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]logs.Entry(nil), m.entries...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamerForwardsStdoutStderrAndExit(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo out1; echo err1 1>&2")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })

	got := sink.all()
	has := func(source, msg string) bool {
		for _, e := range got {
			if e.DeploymentID == "dep1" && e.Source == source && e.Message == msg && e.Timestamp != "" {
				return true
			}
		}
		return false
	}
	if !has("stdout", "out1") || !has("stderr", "err1") {
		t.Fatalf("entries = %+v", got)
	}
	if last := got[len(got)-1]; last.Source != "system" || last.Message != "Log stream ended (exit code: 0)" {
		t.Fatalf("last = %+v", last)
	}
}

func TestStreamerStartsOncePerDeploymentAndStops(t *testing.T) {
	var started atomic.Int32
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		started.Add(1)
		return exec.CommandContext(ctx, "sleep", "30")
	}

	s.Start("dep1", "ctr1")
	s.Start("dep1", "ctr1")
	if started.Load() != 1 || !s.IsStreaming("dep1") {
		t.Fatalf("started=%d streaming=%v", started.Load(), s.IsStreaming("dep1"))
	}
	s.Stop("dep1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
}
