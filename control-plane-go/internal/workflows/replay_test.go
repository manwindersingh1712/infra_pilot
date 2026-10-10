package workflows_test

import (
	"io"
	"log/slog"
	"testing"

	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/workflows"
)

// TestDeployWorkflowReplay replays a history recorded from a real Temporal
// server (testdata/deploy_history.json) against the current DeployWorkflow. It
// needs no server. A failure means the workflow code changed in a way that
// breaks running workflows (non-determinism): version the change with
// workflow.GetVersion, or, if no deployment can be in flight, re-record with
// scripts/e2e-temporal.sh --record.
func TestDeployWorkflowReplay(t *testing.T) {
	r := worker.NewWorkflowReplayer()
	r.RegisterWorkflow(workflows.DeployWorkflow)
	if err := r.ReplayWorkflowHistoryFromJSONFile(log.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))), "testdata/deploy_history.json"); err != nil {
		t.Fatalf("replay: %v", err)
	}
}
