package runtime

import (
	"context"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

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
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		started.Add(1)
		return exec.CommandContext(ctx, "sh", "-c", "echo ready; exec sleep 30")
	}

	s.Start("dep1", "ctr1")
	s.Start("dep1", "ctr1")
	if started.Load() != 1 || !s.IsStreaming("dep1") {
		t.Fatalf("started=%d streaming=%v", started.Load(), s.IsStreaming("dep1"))
	}
	waitFor(t, func() bool { return len(sink.all()) > 0 }) // the process is running
	s.Stop("dep1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
	// STAFF-5 T19: the process was killed and the goroutine wrote its last
	// entry; VER-4 T19: a Stop reads "stopped", not "exit code: -1".
	waitFor(t, func() bool {
		got := sink.all()
		last := got[len(got)-1]
		return last.Source == "system" && last.Message == "Log stream stopped"
	})
}

// STAFF-4(a) T19: on worker shutdown the container keeps running, so the
// stream must not tell the UI it ended.
func TestStreamerShutdownWritesNoEndEntry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sink := &memorySink{}
	s := NewStreamer(ctx, sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo ready; exec sleep 30")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return len(sink.all()) > 0 })
	cancel()
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
	for _, e := range sink.all() {
		if e.Source == "system" {
			t.Fatalf("unexpected system entry %+v", e)
		}
	}
}

// SEC-1 / SEC-2 T19: an over-long line is cut to maxLogLine with a marker and
// the pipe keeps draining, so later stdout and stderr lines still arrive.
func TestStreamerTruncatesLongLineAndKeepsDraining(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c",
			"echo before; head -c 2097152 /dev/zero | tr '\\0' a; echo; echo after; echo err1 1>&2")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })

	got := sink.all()
	var msgs []string
	long := 0
	for _, e := range got {
		if e.Source == "stdout" && strings.HasPrefix(e.Message, "aaaa") {
			long++
			if len(e.Message) > maxLogLine+len(truncMarker) || !strings.HasSuffix(e.Message, truncMarker) {
				t.Fatalf("long entry len=%d suffix=%q", len(e.Message), e.Message[len(e.Message)-20:])
			}
			continue
		}
		msgs = append(msgs, e.Source+":"+e.Message)
	}
	want := []string{"stdout:before", "stdout:after", "stderr:err1", "system:Log stream ended (exit code: 0)"}
	for _, w := range want {
		if !slices.Contains(msgs, w) {
			t.Fatalf("missing %q in %v", w, msgs)
		}
	}
	if long != 1 {
		t.Fatalf("long entries = %d, want 1", long)
	}
	if last := msgs[len(msgs)-1]; last != want[3] {
		t.Fatalf("last = %q", last)
	}
}

// SEC-1 T19: the cut never splits a UTF-8 rune; short lines lose a trailing \r.
func TestStreamerPumpCutsOnRuneBoundary(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	in := strings.Repeat("a", maxLogLine-1) + "é" + strings.Repeat("b", 10) + "\nshort\r\n\nlast"
	s.pump(context.Background(), "dep1", "stdout", strings.NewReader(in))

	got := sink.all()
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3", len(got))
	}
	if want := strings.Repeat("a", maxLogLine-1) + truncMarker; got[0].Message != want {
		t.Fatalf("cut len=%d valid=%v", len(got[0].Message), utf8.ValidString(got[0].Message))
	}
	if got[1].Message != "short" || got[2].Message != "last" {
		t.Fatalf("got %q, %q", got[1].Message, got[2].Message)
	}
}

// SEC-3 T19: a container ID docker could read as a flag, or an empty
// deployment ID, starts nothing.
func TestStreamerRejectsBadIDs(t *testing.T) {
	var started atomic.Int32
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		started.Add(1)
		return exec.CommandContext(ctx, "sleep", "30")
	}

	s.Start("dep1", "-x")
	s.Start("", "ctr1")
	if started.Load() != 0 || s.IsStreaming("dep1") || s.IsStreaming("") {
		t.Fatalf("started=%d", started.Load())
	}
}

// hangingSink blocks the first stdout write until its context ends, like a
// hung Redis; every write is recorded.
type hangingSink struct{ memorySink }

func (h *hangingSink) Write(ctx context.Context, e logs.Entry) error {
	if e.Source == "stdout" {
		<-ctx.Done()
	}
	return h.memorySink.Write(ctx, e)
}

// SEC-4 T19: a sink write that hangs is bounded, so the stream still ends.
func TestStreamerBoundsSinkWrites(t *testing.T) {
	sink := &hangingSink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo out1")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
	got := sink.all()
	if last := got[len(got)-1]; last.Message != "Log stream ended (exit code: 0)" {
		t.Fatalf("last = %+v", last)
	}
}

// STAFF-2 T19: the cleanup of a stopped stream must not remove or cancel a
// newer stream started for the same deployment.
func TestStreamerStopThenStartKeepsNewStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &memorySink{}
	s := NewStreamer(ctx, sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	ended := func() int {
		n := 0
		for _, e := range sink.all() {
			if e.Source == "system" {
				n++
			}
		}
		return n
	}

	s.Start("dep1", "ctr1")
	s.Stop("dep1")
	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return ended() >= 1 }) // first stream is ending
	time.Sleep(300 * time.Millisecond)              // let its deferred cleanup run
	if !s.IsStreaming("dep1") || ended() != 1 {
		t.Fatalf("streaming=%v ended=%d entries=%+v", s.IsStreaming("dep1"), ended(), sink.all())
	}
}
