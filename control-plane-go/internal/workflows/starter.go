package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	case store.EventTeardownRequested:
		in, err := parseTeardownPayload(payload)
		if err != nil {
			return fmt.Errorf("outbox %s: %w: %w", eventType, err, store.ErrOutboxPermanent)
		}
		_, err = s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                    TeardownWorkflowID(in.ServiceID),
			TaskQueue:             TaskQueue,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, TeardownWorkflow, in)
		if err = ignoreAlreadyStarted(err); err != nil {
			return fmt.Errorf("outbox %s: start workflow for service %s: %w", eventType, in.ServiceID, err)
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

// parseTeardownPayload reads {serviceId, purgeData} strictly: the payload is
// data. serviceId must be a cuid; purgeData is optional and, when present, a
// JSON boolean (anything else, including null, is refused rather than guessed,
// because a wrong guess either keeps data the user wanted gone or destroys data
// they wanted kept); unknown and duplicate keys and trailing data are refused.
func parseTeardownPayload(payload []byte) (TeardownInput, error) {
	var in TeardownInput
	dec := json.NewDecoder(bytes.NewReader(payload))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return in, errors.New("payload is not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		key, ok := keyTok.(string)
		if err != nil || !ok {
			return in, errors.New("malformed payload")
		}
		if seen[key] {
			return in, fmt.Errorf("duplicate key %q", clip(key, 40))
		}
		seen[key] = true
		switch key {
		case "serviceId":
			var id string
			if err := dec.Decode(&id); err != nil {
				return in, errors.New("serviceId is not a string")
			}
			if !serviceIDPattern.MatchString(id) {
				return in, fmt.Errorf("serviceId %q is not a cuid", clip(id, 40))
			}
			in.ServiceID = id
		case "purgeData":
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return in, errors.New("malformed purgeData")
			}
			switch string(raw) {
			case "true":
				in.PurgeData = true
			case "false":
			default:
				return in, errors.New("purgeData is not a boolean")
			}
		default:
			return in, fmt.Errorf("unknown key %q", clip(key, 40))
		}
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return in, errors.New("malformed payload")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return in, errors.New("trailing data after payload")
	}
	if in.ServiceID == "" {
		return in, errors.New("payload has no serviceId")
	}
	return in, nil
}
