package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/lucsky/cuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"infrapilot/internal/store"
)

// Fixture ids shaped like real cuids (lowercase alphanumeric, 25 chars).
const (
	depID = "cm0a1b2c3d4e5f6g7h8i9j0k1"
	dep9  = "cm9a1b2c3d4e5f6g7h8i9j0k9"
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
		if err := s.Start(context.Background(), typ, []byte(`{"deploymentId":"`+depID+`"}`)); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if len(fc.opts) != 1 {
			t.Fatalf("%s: %d starts", typ, len(fc.opts))
		}
		o := fc.opts[0]
		if o.ID != DeployWorkflowID(depID) || o.ID != "deploy-"+depID || o.TaskQueue != TaskQueue ||
			o.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
			t.Fatalf("%s: options %+v", typ, o)
		}
		if len(fc.args[0]) != 1 || fc.args[0][0] != depID {
			t.Fatalf("%s: args %v", typ, fc.args[0])
		}
	}
}

func TestStarterTreatsAlreadyStartedAsSuccess(t *testing.T) {
	already := serviceerror.NewWorkflowExecutionAlreadyStarted("exists", "req", "run")
	s := Starter{Client: &fakeClient{err: already}}
	if err := s.Start(context.Background(), store.EventDeployRequested, []byte(`{"deploymentId":"`+depID+`"}`)); err != nil {
		t.Fatalf("already started must be success, got %v", err)
	}
	// Wrapped errors count too.
	s = Starter{Client: &fakeClient{err: errors.Join(errors.New("ctx"), already)}}
	if err := s.Start(context.Background(), store.EventBuildRequested, []byte(`{"deploymentId":"`+depID+`"}`)); err != nil {
		t.Fatalf("wrapped already started must be success, got %v", err)
	}
}

func TestStarterReturnsOtherErrorsWithDeploymentID(t *testing.T) {
	boom := errors.New("frontend unavailable")
	s := Starter{Client: &fakeClient{err: boom}}
	err := s.Start(context.Background(), store.EventBuildRequested, []byte(`{"deploymentId":"`+dep9+`"}`))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), dep9) {
		t.Fatalf("err = %v", err)
	}
}

func TestStarterRejectsPoisonEvents(t *testing.T) {
	fc := &fakeClient{}
	s := Starter{Client: fc}
	cases := map[string][2]string{
		"unknown type":  {"unknown.event", `{"deploymentId":"` + depID + `"}`},
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

func TestStarterRejectsMalformedDeploymentIDsPermanently(t *testing.T) {
	long := strings.Repeat("a", 5000)
	for name, id := range map[string]string{
		"short legacy":  "dep-1",
		"uppercase":     strings.ToUpper(depID),
		"path":          "../../etc/passwd" + depID,
		"control chars": "abc\u001b[31m\ndef" + depID,
		"too long":      long,
		"space":         depID + " x",
		"unicode":       "cm0a1b2c3d4e5f6g7h8i9jé1",
	} {
		fc := &fakeClient{}
		payload, _ := json.Marshal(map[string]string{"deploymentId": id})
		err := Starter{Client: fc}.Start(context.Background(), store.EventBuildRequested, payload)
		if !errors.Is(err, store.ErrOutboxPermanent) {
			t.Errorf("%s: err = %v, want ErrOutboxPermanent", name, err)
			continue
		}
		if len(fc.opts) != 0 {
			t.Errorf("%s: Start reached Temporal", name)
		}
		msg := err.Error()
		if len(msg) > 200 {
			t.Errorf("%s: error text is %d bytes, must cap the echoed id", name, len(msg))
		}
		for _, r := range msg {
			if r < 0x20 || r == 0x7f {
				t.Errorf("%s: error text has control char %q", name, r)
			}
		}
	}
}

func TestRealCuidsPassTheIDCheck(t *testing.T) {
	for range 50 {
		fc := &fakeClient{}
		id := cuid.New()
		if err := (Starter{Client: fc}).Start(context.Background(), store.EventBuildRequested, []byte(`{"deploymentId":"`+id+`"}`)); err != nil {
			t.Fatalf("cuid %q rejected: %v", id, err)
		}
	}
}

const svcID = "cm1a1b2c3d4e5f6g7h8i9j0k1"

func TestStarterStartsTeardownOnce(t *testing.T) {
	for payload, purge := range map[string]bool{
		`{"serviceId":"` + svcID + `","purgeData":true}`:  true,
		`{"serviceId":"` + svcID + `","purgeData":false}`: false,
		`{"serviceId":"` + svcID + `"}`:                   false,
	} {
		fc := &fakeClient{}
		if err := (Starter{Client: fc}).Start(context.Background(), store.EventTeardownRequested, []byte(payload)); err != nil {
			t.Fatalf("%s: %v", payload, err)
		}
		o := fc.opts[0]
		if o.ID != "teardown-"+svcID || o.TaskQueue != TaskQueue ||
			o.WorkflowIDReusePolicy != enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE {
			t.Fatalf("options %+v", o)
		}
		if in, ok := fc.args[0][0].(TeardownInput); !ok || in.ServiceID != svcID || in.PurgeData != purge {
			t.Fatalf("%s: args %+v", payload, fc.args[0])
		}
	}
	// A second event for the same service maps to the same workflow ID, and the
	// server's "already started" is success.
	already := serviceerror.NewWorkflowExecutionAlreadyStarted("exists", "req", "run")
	if err := (Starter{Client: &fakeClient{err: already}}).Start(context.Background(), store.EventTeardownRequested, []byte(`{"serviceId":"`+svcID+`"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestStarterRejectsBadTeardownPayloadsPermanently(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":            `{}`,
		"not object":       `"x"`,
		"malformed":        `{"serviceId":`,
		"bad id":           `{"serviceId":"../etc"}`,
		"uppercase id":     `{"serviceId":"` + "CM1A1B2C3D4E5F6G7H8I9J0K1" + `"}`,
		"purge string":     `{"serviceId":"` + svcID + `","purgeData":"true"}`,
		"purge number":     `{"serviceId":"` + svcID + `","purgeData":1}`,
		"purge null":       `{"serviceId":"` + svcID + `","purgeData":null}`,
		"purge object":     `{"serviceId":"` + svcID + `","purgeData":{}}`,
		"unknown field":    `{"serviceId":"` + svcID + `","volume":"cp-vol-other"}`,
		"trailing data":    `{"serviceId":"` + svcID + `"} {}`,
		"duplicate purge":  `{"serviceId":"` + svcID + `","purgeData":false,"purgeData":true}`,
		"deployment shape": `{"deploymentId":"` + depID + `"}`,
	} {
		fc := &fakeClient{}
		err := (Starter{Client: fc}).Start(context.Background(), store.EventTeardownRequested, []byte(payload))
		if !errors.Is(err, store.ErrOutboxPermanent) {
			t.Errorf("%s: err = %v, want ErrOutboxPermanent", name, err)
		}
		if len(fc.opts) != 0 {
			t.Errorf("%s: started a workflow", name)
		}
	}
}
