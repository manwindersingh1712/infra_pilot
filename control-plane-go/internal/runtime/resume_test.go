package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
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
