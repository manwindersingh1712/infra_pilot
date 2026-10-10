package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
	"time"
)

func TestResumeStartsOnlyRunningContainers(t *testing.T) {
	var mu sync.Mutex
	var started []string
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, containerID string) *exec.Cmd {
		mu.Lock()
		started = append(started, containerID)
		mu.Unlock()
		return exec.CommandContext(ctx, "sleep", "30")
	}
	defer s.Stop("d1")

	running := map[string]bool{"c1": true, "c2": false}
	isRunning := func(_ context.Context, id string) (bool, error) {
		if id == "c3" {
			return false, errors.New("docker down")
		}
		return running[id], nil
	}
	n := s.Resume(context.Background(), []Running{
		{DeploymentID: "d1", ContainerID: "c1"},
		{DeploymentID: "d2", ContainerID: "c2"},
		{DeploymentID: "d3", ContainerID: "c3"},
	}, isRunning)

	if n != 1 || !s.IsStreaming("d1") || s.IsStreaming("d2") || s.IsStreaming("d3") {
		t.Fatalf("n=%d d1=%v d2=%v d3=%v", n, s.IsStreaming("d1"), s.IsStreaming("d2"), s.IsStreaming("d3"))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 1 || started[0] != "c1" {
		t.Fatalf("started = %v", started)
	}
}

// STAFF-1 T20: a deployment listed twice starts one stream and counts once.
func TestResumeCountsDuplicateOnce(t *testing.T) {
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "30")
	}
	defer s.Stop("d1")

	n := s.Resume(context.Background(), []Running{
		{DeploymentID: "d1", ContainerID: "c1"},
		{DeploymentID: "d1", ContainerID: "c1"},
	}, func(context.Context, string) (bool, error) { return true, nil })
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
}

// STAFF-2 T20: a cancelled context stops Resume before the next inspect.
func TestResumeStopsWhenContextCancelled(t *testing.T) {
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	n := s.Resume(ctx, []Running{
		{DeploymentID: "d1", ContainerID: "c1"},
		{DeploymentID: "d2", ContainerID: "c2"},
	}, func(context.Context, string) (bool, error) {
		calls++
		cancel()
		return false, nil
	})
	if n != 0 || calls != 1 {
		t.Fatalf("n=%d calls=%d, want 0 and 1", n, calls)
	}
}

// SEC-2 T20: each inspect gets its own deadline, so a hung docker cannot
// hold Resume forever.
func TestResumeBoundsEachInspect(t *testing.T) {
	defer func(d time.Duration) { inspectTimeout = d }(inspectTimeout)
	inspectTimeout = 50 * time.Millisecond
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	calls := 0
	done := make(chan int, 1)
	go func() {
		done <- s.Resume(context.Background(), []Running{
			{DeploymentID: "d1", ContainerID: "c1"},
			{DeploymentID: "d2", ContainerID: "c2"},
		}, func(ctx context.Context, _ string) (bool, error) {
			calls++
			<-ctx.Done()
			return false, ctx.Err()
		})
	}()
	select {
	case n := <-done:
		if n != 0 || calls != 2 {
			t.Fatalf("n=%d calls=%d, want 0 and 2", n, calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resume did not return: inspect is unbounded")
	}
}
