package outbox

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// closedAddr returns a loopback address nothing listens on.
func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// TestE2ERealUnavailableErrorIsTransient guards the Task 25 defect: the error the
// real Temporal client returns for an unreachable server, after it went
// through the real Starter, must classify as transient so the outbox backs off
// instead of burning the event's attempts. It needs no server (the port is closed).
func TestE2ERealUnavailableErrorIsTransient(t *testing.T) {
	c, err := client.NewLazyClient(client.Options{HostPort: closedAddr(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = workflows.Starter{Client: c}.Start(ctx, store.EventDeployRequested, []byte(`{"deploymentId":"abcdefghij0123456789"}`))
	if err == nil {
		t.Fatal("start against a closed port succeeded")
	}
	t.Logf("real error: %T: %v", err, err)
	if !transient(err) {
		t.Fatalf("transient(%T: %v) = false, want true", err, err)
	}
}

// TestE2ERealAlreadyStartedIsNotAnError runs the Starter twice against a real
// server: the second start must be a clean success (nothing to classify).
// The raw client error for a duplicate is not transient either.
func TestE2ERealAlreadyStartedIsNotAnError(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}
	addr := os.Getenv("TEMPORAL_E2E_ADDR")
	if addr == "" {
		t.Skip("e2e: TEMPORAL_E2E_ADDR is not set")
	}
	c, err := client.Dial(client.Options{HostPort: addr, Identity: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	id := "e2eoutbox" + time.Now().Format("150405000000")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = c.TerminateWorkflow(context.Background(), workflows.DeployWorkflowID(id), "", "e2e cleanup")
	})

	st := workflows.Starter{Client: c}
	payload := []byte(`{"deploymentId":"` + id + `"}`)
	for i := range 2 {
		if err := st.Start(ctx, store.EventDeployRequested, payload); err != nil {
			t.Fatalf("start #%d: %v", i+1, err)
		}
	}
	// The raw duplicate error from the real server is a permanent, non-transient shape.
	_, err = c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: workflows.DeployWorkflowID(id), TaskQueue: workflows.TaskQueue,
		WorkflowIDReusePolicy:                    enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true}, workflows.DeployWorkflow, id)
	if err == nil {
		t.Fatal("a raw duplicate start unexpectedly succeeded")
	}
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if !errors.As(err, &already) {
		t.Fatalf("raw duplicate error is %T (%v), want WorkflowExecutionAlreadyStarted", err, err)
	}
	if transient(err) {
		t.Fatalf("already-started error classified transient: %T %v", err, err)
	}
}
