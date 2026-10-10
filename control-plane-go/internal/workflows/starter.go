package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"infrapilot/internal/store"
)

// Starter starts workflows for outbox events. Both Node-era event types
// start the same DeployWorkflow; the workflow decides whether to build.
//
// Authorization: DeployWorkflow takes only a deployment id and does no
// ownership check. The starter is therefore the only way a workflow begins,
// and it must be fed only from OutboxEvent rows written by the
// ownership-checked API in the same transaction as the Deployment row. It
// never starts a workflow from request input. The Temporal frontend address
// is equally privileged: tenants must never be able to reach it (loopback or
// the compose network now, mTLS in production).
//
// Serialisation of deployments of one service lives in the workflow
// (ClaimDeploy), not here: the starter deliberately has no service-level check.
type Starter struct {
	Client client.Client
}

// deploymentIDPattern matches the cuids the API generates. The id goes into a
// workflow id and a container name, so nothing else may through.
var deploymentIDPattern = regexp.MustCompile(`^[a-z0-9]{20,32}$`)

// clip shortens s to at most n runes for error text.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func (s Starter) Start(ctx context.Context, eventType string, payload []byte) error {
	switch eventType {
	case store.EventBuildRequested, store.EventDeployRequested:
		var msg struct {
			DeploymentID string `json:"deploymentId"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil || msg.DeploymentID == "" {
			return fmt.Errorf("outbox %s: payload has no deploymentId", eventType)
		}
		if !deploymentIDPattern.MatchString(msg.DeploymentID) {
			return fmt.Errorf("outbox %s: deploymentId %q is not a cuid: %w", eventType, clip(msg.DeploymentID, 40), store.ErrOutboxPermanent)
		}
		_, err := s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                    DeployWorkflowID(msg.DeploymentID),
			TaskQueue:             TaskQueue,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, DeployWorkflow, msg.DeploymentID)
		if err = ignoreAlreadyStarted(err); err != nil {
			return fmt.Errorf("outbox %s: start workflow for deployment %s: %w", eventType, msg.DeploymentID, err)
		}
		return nil
	}
	return fmt.Errorf("outbox: unknown event type %q", eventType)
}

// ignoreAlreadyStarted treats "this deployment's workflow already exists" as success.
func ignoreAlreadyStarted(err error) error {
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &already) {
		return nil
	}
	return err
}
