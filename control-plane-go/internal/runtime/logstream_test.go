package runtime

import (
	"context"
	"errors"
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
	s.pump(context.Background(), &lineWriter{s: s, deploymentID: "dep1"}, "stdout", strings.NewReader(in))

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
type hangingSink struct {
	memorySink
	onBlock func() // called once, when the first write starts blocking
}

func (h *hangingSink) Write(ctx context.Context, e logs.Entry) error {
	if e.Source == "stdout" {
		if h.onBlock != nil {
			h.onBlock()
			h.onBlock = nil
		}
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

	released := make(chan struct{}, 2)
	s.released = func(string) { released <- struct{}{} }

	s.Start("dep1", "ctr1")
	s.Stop("dep1")
	s.Start("dep1", "ctr1")
	select { // STAFF-7 T19: the first stream's cleanup has run
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("first stream was not released")
	}
	if !s.IsStreaming("dep1") || ended() != 1 {
		t.Fatalf("streaming=%v ended=%d entries=%+v", s.IsStreaming("dep1"), ended(), sink.all())
	}
}

// failingSink fails every write, like Redis being down; attempts are counted.
type failingSink struct{ lines, system atomic.Int32 }

func (f *failingSink) Write(_ context.Context, e logs.Entry) error {
	if e.Source == "system" {
		f.system.Add(1)
	} else {
		f.lines.Add(1)
	}
	return errors.New("redis down")
}

// warnCounter is a slog handler that counts Warn records.
type warnCounter struct{ n atomic.Int32 }

func (w *warnCounter) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnCounter) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		w.n.Add(1)
	}
	return nil
}
func (w *warnCounter) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnCounter) WithGroup(string) slog.Handler      { return w }

// STAFF-5 / SEC-6 T19: after the first failed write the stream warns once
// and drops the rest, but still drains the pipe and ends.
func TestStreamerStopsWritingAfterSinkFailure(t *testing.T) {
	sink := &failingSink{}
	warns := &warnCounter{}
	s := NewStreamer(context.Background(), sink, slog.New(warns))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "for i in $(seq 1 200); do echo line$i; echo err$i 1>&2; done")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
	if l, sys, w := sink.lines.Load(), sink.system.Load(), warns.n.Load(); l != 1 || sys > 1 || w != 1 {
		t.Fatalf("line writes=%d system writes=%d warns=%d, want 1, <=1, 1", l, sys, w)
	}
}

// SEC-6 T19: Stop cancels an in-flight line write instead of waiting out
// the sink timeout.
func TestStreamerStopCancelsHungWrite(t *testing.T) {
	sink := &hangingSink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo out1; exec sleep 30")
	}
	started := make(chan struct{})
	sink.onBlock = func() { close(started) }

	s.Start("dep1", "ctr1")
	<-started
	t0 := time.Now()
	s.Stop("dep1")
	waitFor(t, func() bool { // the goroutine wrote its final entry
		got := sink.all()
		return len(got) > 0 && got[len(got)-1].Message == "Log stream stopped"
	})
	if d := time.Since(t0); d >= sinkTimeout {
		t.Fatalf("stream took %v to end after Stop", d)
	}
}

// STAFF-8 T19: a line of exactly maxLogLine bytes ending in CRLF is whole,
// so it gets no truncation marker.
func TestStreamerPumpExactLineWithCRLF(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	exact := strings.Repeat("a", maxLogLine)
	s.pump(context.Background(), &lineWriter{s: s, deploymentID: "dep1"}, "stdout", strings.NewReader(exact+"\r\nnext\n"))

	got := sink.all()
	if len(got) != 2 || got[0].Message != exact || got[1].Message != "next" {
		t.Fatalf("entries = %d, first len=%d marker=%v", len(got), len(got[0].Message), strings.HasSuffix(got[0].Message, truncMarker))
	}
}

// STAFF-6 T20: Stop racing a start failure ends the stream with "stopped",
// not "context canceled".
func TestStreamerStopDuringStartFailureSaysStopped(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		s.Stop("dep1") // cancels ctx, so cmd.Start fails with context canceled
		return exec.CommandContext(ctx, "sh", "-c", "true")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return len(sink.all()) > 0 })
	got := sink.all()
	if len(got) != 1 || got[0].Message != "Log stream stopped" {
		t.Fatalf("entries = %+v", got)
	}
}

// STAFF-7 T20: an unterminated final line of exactly maxLogLine bytes plus a
// lone "\r" at EOF is whole, so it gets no truncation marker.
func TestStreamerPumpExactLineWithLoneCRAtEOF(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	exact := strings.Repeat("a", maxLogLine)
	s.pump(context.Background(), &lineWriter{s: s, deploymentID: "dep1"}, "stdout", strings.NewReader(exact+"\r"))

	got := sink.all()
	if len(got) != 1 || got[0].Message != exact {
		t.Fatalf("entries = %d, len=%d marker=%v", len(got), len(got[0].Message), strings.HasSuffix(got[0].Message, truncMarker))
	}
}
