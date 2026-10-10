package workflows

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"infrapilot/internal/store"
)

// fakeClient records ExecuteWorkflow calls. The embedded nil interface makes
// any other client method panic, so a test notices if the starter calls one.
type fakeClient struct {
	client.Client
	mu   sync.Mutex
	opts []client.StartWorkflowOptions
	args [][]any
	err  error
}

func (f *fakeClient) ExecuteWorkflow(_ context.Context, o client.StartWorkflowOptions, _ any, args ...any) (client.WorkflowRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opts = append(f.opts, o)
	f.args = append(f.args, args)
	return nil, f.err
}

func TestStarterStartsDeployWorkflowForBothEventTypes(t *testing.T) {
	for _, typ := range []string{store.EventBuildRequested, store.EventDeployRequested} {
		fc := &fakeClient{}
		s := Starter{Client: fc}
		if err := s.Start(context.Background(), typ, []byte(`{"deploymentId":"dep-1"}`)); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if len(fc.opts) != 1 {
			t.Fatalf("%s: %d starts", typ, len(fc.opts))
		}
		o := fc.opts[0]
		if o.ID != DeployWorkflowID("dep-1") || o.ID != "deploy-dep-1" || o.TaskQueue != TaskQueue ||
			o.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
			t.Fatalf("%s: options %+v", typ, o)
		}
		if len(fc.args[0]) != 1 || fc.args[0][0] != "dep-1" {
			t.Fatalf("%s: args %v", typ, fc.args[0])
		}
	}
}

func TestStarterTreatsAlreadyStartedAsSuccess(t *testing.T) {
	already := serviceerror.NewWorkflowExecutionAlreadyStarted("exists", "req", "run")
	s := Starter{Client: &fakeClient{err: already}}
	if err := s.Start(context.Background(), store.EventDeployRequested, []byte(`{"deploymentId":"dep-1"}`)); err != nil {
		t.Fatalf("already started must be success, got %v", err)
	}
	// Wrapped errors count too.
	s = Starter{Client: &fakeClient{err: errors.Join(errors.New("ctx"), already)}}
	if err := s.Start(context.Background(), store.EventBuildRequested, []byte(`{"deploymentId":"dep-1"}`)); err != nil {
		t.Fatalf("wrapped already started must be success, got %v", err)
	}
}

func TestStarterReturnsOtherErrorsWithDeploymentID(t *testing.T) {
	boom := errors.New("frontend unavailable")
	s := Starter{Client: &fakeClient{err: boom}}
	err := s.Start(context.Background(), store.EventBuildRequested, []byte(`{"deploymentId":"dep-9"}`))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "dep-9") {
		t.Fatalf("err = %v", err)
	}
}

func TestStarterRejectsPoisonEvents(t *testing.T) {
	fc := &fakeClient{}
	s := Starter{Client: fc}
	cases := map[string][2]string{
		"unknown type":  {"teardown.requested", `{"deploymentId":"dep-1"}`},
		"empty object":  {store.EventBuildRequested, `{}`},
		"not an object": {store.EventBuildRequested, `"dep-1"`},
		"array":         {store.EventBuildRequested, `[]`},
		"malformed":     {store.EventBuildRequested, `{"deploymentId":`},
		"empty id":      {store.EventDeployRequested, `{"deploymentId":""}`},
		"non-string id": {store.EventDeployRequested, `{"deploymentId":7}`},
	}
	for name, c := range cases {
		if err := s.Start(context.Background(), c[0], []byte(c[1])); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(fc.opts) != 0 {
		t.Fatalf("poison events started %d workflows", len(fc.opts))
	}
}
