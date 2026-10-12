package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	// TaskQueue overrides the default queue (tests only).
	TaskQueue string
	// Log receives the serviceId of teardown events rejected as permanent; nil
	// uses slog.Default().
	Log *slog.Logger
}

func (s Starter) taskQueue() string {
	if s.TaskQueue != "" {
		return s.TaskQueue
	}
	return TaskQueue
}

func (s Starter) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
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
			TaskQueue:             s.taskQueue(),
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, DeployWorkflow, msg.DeploymentID)
		if err = ignoreAlreadyStarted(err); err != nil {
			return fmt.Errorf("outbox %s: start workflow for deployment %s: %w", eventType, msg.DeploymentID, err)
		}
		return nil
	case store.EventTeardownRequested:
		in, err := s.parseTeardown(payload)
		if err != nil {
			return fmt.Errorf("outbox %s: %w: %w", eventType, err, store.ErrOutboxPermanent)
		}
		// A completed teardown stays unique and a running one is never doubled
		// ("already started" is success). A failed, terminated, cancelled or
		// timed-out one is restarted by a new outbox event (the runbook).
		_, err = s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                    TeardownWorkflowID(in.ServiceID),
			TaskQueue:             s.taskQueue(),
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
		}, TeardownWorkflow, in)
		if err = ignoreAlreadyStarted(err); err != nil {
			return fmt.Errorf("outbox %s: start workflow for service %s: %w", eventType, in.ServiceID, err)
		}
		return nil
	}
	return fmt.Errorf("outbox: unknown event type %q", eventType)
}

// parseTeardown parses the payload and, when it is refused, logs the serviceId
// it carried (clipped; best effort) so an operator can find the service.
func (s Starter) parseTeardown(payload []byte) (TeardownInput, error) {
	in, odd, err := parseTeardownPayload(payload)
	if err == nil && odd != "" {
		s.log().Warn("teardown event has a purgeData that is not true or false; not purging", "serviceId", in.ServiceID, "purgeData", odd)
	}
	if err != nil {
		var probe struct {
			ServiceID any `json:"serviceId"`
		}
		_ = json.Unmarshal(payload, &probe)
		id := ""
		if probe.ServiceID != nil {
			id = clip(fmt.Sprint(probe.ServiceID), 40)
		}
		s.log().Error("teardown event rejected as permanent", "serviceId", id, "err", err)
	}
	return in, err
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
// data. serviceId must be a cuid; unknown and duplicate keys, trailing data and
// non-objects are refused. purgeData purges only for the exact JSON literal
// true; absent, false and any other value mean no purge (never fall back to
// true: the volume is the one thing that cannot be undone), and any other value
// is returned in odd (clipped) so the caller can warn.
func parseTeardownPayload(payload []byte) (in TeardownInput, odd string, err error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return in, "", errors.New("payload is not a JSON object")
	}
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		key, ok := keyTok.(string)
		if err != nil || !ok {
			return in, "", errors.New("malformed payload")
		}
		if seen[key] {
			return in, "", fmt.Errorf("duplicate key %q", clip(key, 40))
		}
		seen[key] = true
		switch key {
		case "serviceId":
			var id string
			if err := dec.Decode(&id); err != nil {
				return in, "", errors.New("serviceId is not a string")
			}
			if !serviceIDPattern.MatchString(id) {
				return in, "", fmt.Errorf("serviceId %q is not a cuid", clip(id, 40))
			}
			in.ServiceID = id
		case "purgeData":
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return in, "", errors.New("malformed purgeData")
			}
			switch string(raw) {
			case "true":
				in.PurgeData = true
			case "false":
			default:
				odd = clip(string(raw), 40) // never true: a wrong guess would destroy data
			}
		default:
			return in, "", fmt.Errorf("unknown key %q", clip(key, 40))
		}
	}
	if _, err := dec.Token(); err != nil { // closing brace
		return in, "", errors.New("malformed payload")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return in, "", errors.New("trailing data after payload")
	}
	if in.ServiceID == "" {
		return in, "", errors.New("payload has no serviceId")
	}
	return in, odd, nil
}
