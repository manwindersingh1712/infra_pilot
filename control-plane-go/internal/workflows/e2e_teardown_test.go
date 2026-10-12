package workflows_test

import (
	"context"
	"sync"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// fakeTeardownActs stands in for CancelServiceDeploys and TeardownService:
// the activity names match the real ones, which the workflow resolves by method name.
type fakeTeardownActs struct {
	mu            sync.Mutex
	teardownCalls int
	failFirst     bool          // the first TeardownService call fails permanently
	block         chan struct{} // when set, CancelServiceDeploys waits for it
	entered       chan struct{}
	enterOnce     sync.Once
}

func (f *fakeTeardownActs) CancelServiceDeploys(ctx context.Context, _ workflows.TeardownInput) error {
	if f.block != nil {
		f.enterOnce.Do(func() { close(f.entered) })
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeTeardownActs) TeardownService(_ context.Context, _ workflows.TeardownInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teardownCalls++
	if f.failFirst && f.teardownCalls == 1 {
		return temporal.NewNonRetryableApplicationError("boom", "NotDeleted", nil)
	}
	return nil
}

func (f *fakeTeardownActs) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.teardownCalls
}

func startTeardownWorker(t *testing.T, c client.Client, tq string, f *fakeTeardownActs) {
	t.Helper()
	wk := worker.New(c, tq, worker.Options{Identity: "e2e-teardown-worker"})
	wk.RegisterWorkflow(workflows.TeardownWorkflow)
	wk.RegisterActivity(f)
	if err := wk.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(wk.Stop)
}

// latest describes the latest run of the teardown workflow.
func latest(t *testing.T, c client.Client, svc string) (runID string, status enumspb.WorkflowExecutionStatus) {
	t.Helper()
	d, err := c.DescribeWorkflowExecution(context.Background(), workflows.TeardownWorkflowID(svc), "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	return d.GetWorkflowExecutionInfo().GetExecution().GetRunId(), d.GetWorkflowExecutionInfo().GetStatus()
}

func waitStatus(t *testing.T, c client.Client, svc string, want enumspb.WorkflowExecutionStatus) string {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for {
		run, st := latest(t, c, svc)
		if st == want {
			return run
		}
		if time.Now().After(deadline) {
			t.Fatalf("teardown workflow stuck at %s, want %s", st, want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Ruling 1 (real Temporal): a failed teardown is restarted by a new start with
// the same id; a completed one is not; a running one is not doubled.
func TestE2ETeardownRestartsAfterFailureOnly(t *testing.T) {
	c := e2eClient(t)
	tq, svc := "e2e-teardown-"+rid(), rid()
	f := &fakeTeardownActs{failFirst: true}
	startTeardownWorker(t, c, tq, f)
	st := workflows.Starter{Client: c, TaskQueue: tq}
	payload := []byte(`{"serviceId":"` + svc + `","purgeData":true}`)
	start := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := st.Start(ctx, store.EventTeardownRequested, payload); err != nil {
			t.Fatalf("start: %v", err)
		}
	}

	start()
	run1 := waitStatus(t, c, svc, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED)

	start() // restart after the failed run
	run2 := waitStatus(t, c, svc, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	if run2 == run1 {
		t.Fatal("a failed run was not replaced by a new run")
	}
	if f.calls() != 2 {
		t.Fatalf("TeardownService called %d times, want 2", f.calls())
	}

	start() // completed: swallowed, nothing starts
	start()
	if run, status := latest(t, c, svc); run != run2 || status != enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED || f.calls() != 2 {
		t.Fatalf("a completed teardown was restarted: run %s status %s calls %d", run, status, f.calls())
	}
}

func TestE2ETeardownRunningStartIsSwallowed(t *testing.T) {
	c := e2eClient(t)
	tq, svc := "e2e-teardown-run-"+rid(), rid()
	f := &fakeTeardownActs{block: make(chan struct{}), entered: make(chan struct{})}
	startTeardownWorker(t, c, tq, f)
	st := workflows.Starter{Client: c, TaskQueue: tq}
	payload := []byte(`{"serviceId":"` + svc + `"}`)
	if err := st.Start(context.Background(), store.EventTeardownRequested, payload); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("cancel activity never started")
	}
	run1, status := latest(t, c, svc)
	if status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatalf("status %s", status)
	}
	if err := st.Start(context.Background(), store.EventTeardownRequested, payload); err != nil {
		t.Fatalf("start while running: %v", err)
	}
	if run, _ := latest(t, c, svc); run != run1 {
		t.Fatal("a running teardown was doubled")
	}
	close(f.block)
	if run := waitStatus(t, c, svc, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); run != run1 {
		t.Fatalf("run changed to %s", run)
	}
}
