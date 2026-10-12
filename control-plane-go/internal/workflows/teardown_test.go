package workflows

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

// fakeTemporal stands in for the Temporal client's cancel and describe calls.
type fakeTemporal struct {
	mu        sync.Mutex
	cancelled []string
	running   map[string]int // workflow id -> Describe calls that still say RUNNING (-1 = forever)
	missing   map[string]bool
}

func (f *fakeTemporal) CancelWorkflow(_ context.Context, id, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	if f.missing[id] {
		return serviceerror.NewNotFound("no such workflow")
	}
	return nil
}

func (f *fakeTemporal) DescribeWorkflowExecution(_ context.Context, id, _ string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.missing[id] {
		return nil, serviceerror.NewNotFound("no such workflow")
	}
	status := enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED
	if n := f.running[id]; n != 0 {
		status = enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING
		if n > 0 {
			f.running[id]--
		}
	}
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Status: status},
	}, nil
}

func TestTeardownServiceActivity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeRedis)
	d1, _ := e.st.CreateDeployment(ctx, f.ServiceID, "latest")
	e.st.MarkDeployed(ctx, d1.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 6379})
	dq, _ := e.st.CreateDeployment(ctx, f.ServiceID, "latest") // still queued
	e.st.UpsertEnvVar(ctx, f.ServiceID, "REDIS_PASSWORD", "enc:v1:x")
	if err := e.st.SoftDeleteService(ctx, f.ServiceID); err != nil {
		t.Fatal(err)
	}

	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: false}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.docker.calls, []string{"remove cp-" + d1.ID, "rmservice " + f.ServiceID}) || !slices.Equal(e.router.removed, []string{f.ServiceID}) {
		t.Fatalf("calls %v removed %v", e.docker.calls, e.router.removed)
	}
	if d, _ := e.st.GetDeployment(ctx, d1.ID); d.StoppedAt == nil {
		t.Fatal("deployment not marked stopped")
	}
	if d, _ := e.st.GetDeployment(ctx, dq.ID); d.Status != store.StatusFailed {
		t.Fatalf("open deployment status %q, want failed", d.Status)
	}
	if m, _ := e.st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
		t.Fatalf("env vars left: %v", m)
	}
	if s, _ := e.st.GetServiceAnyState(ctx, f.ServiceID); s.RepoURL != nil {
		t.Fatal("repoUrl not scrubbed")
	}

	// A re-run (Temporal retry, or teardown of an already torn-down service) succeeds.
	e.docker.calls = nil
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.docker.calls, []string{"rmservice " + f.ServiceID, "rmvolume cp-vol-" + f.ServiceID}) {
		t.Fatalf("purge calls %v", e.docker.calls)
	}
}

func TestTeardownKeepsVolumeUnlessPurgeIsTrue(t *testing.T) {
	e := newEnv(t)
	f := testutil.Seed(t, e.st, store.TypeRedis)
	e.st.SoftDeleteService(context.Background(), f.ServiceID)
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.docker.calls {
		if c == "rmvolume cp-vol-"+f.ServiceID {
			t.Fatal("volume removed without purgeData")
		}
	}
}

func TestTeardownRefusesLiveOrBadServices(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	e.st.UpsertEnvVar(ctx, f.ServiceID, "K", "enc:v1:x")
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: true}); !isNonRetryable(err) {
		t.Fatalf("live service: err = %v", err)
	}
	if len(e.docker.calls) != 0 || len(e.router.removed) != 0 {
		t.Fatalf("live service touched: %v %v", e.docker.calls, e.router.removed)
	}
	if m, _ := e.st.EnvMap(ctx, f.ServiceID); len(m) != 1 {
		t.Fatal("live service lost env vars")
	}
	for _, bad := range []string{"", "../x", "S1", "svc-1", "a b"} {
		if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: bad, PurgeData: true}); !isNonRetryable(err) {
			t.Errorf("%q: err = %v", bad, err)
		}
		if _, err := e.run(t, e.acts.CancelServiceDeploys, TeardownInput{ServiceID: bad}); !isNonRetryable(err) {
			t.Errorf("cancel %q: err = %v", bad, err)
		}
	}
	if len(e.docker.calls) != 0 {
		t.Fatalf("docker called for bad ids: %v", e.docker.calls)
	}
	// An unknown (never existing) service is a no-op success.
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: "cm0a1b2c3d4e5f6g7h8i9j0k1", PurgeData: true}); err != nil {
		t.Fatal(err)
	}
	if len(e.docker.calls) != 0 {
		t.Fatalf("docker called for unknown service: %v", e.docker.calls)
	}
}

func TestTeardownPurgeRemovesOnlyThisServicesLegacyDir(t *testing.T) {
	e := newEnv(t)
	f := testutil.Seed(t, e.st, store.TypeRedis)
	other := testutil.Seed(t, e.st, store.TypeRedis)
	root := e.acts.Cfg.LegacyVolumeRoot
	for _, id := range []string{f.ServiceID, other.ServiceID} {
		os.MkdirAll(filepath.Join(root, id), 0o755)
		os.WriteFile(filepath.Join(root, id, "dump.rdb"), []byte("x"), 0o600)
	}
	e.st.SoftDeleteService(context.Background(), f.ServiceID)

	// Without purge nothing on disk is touched.
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, f.ServiceID)); err != nil {
		t.Fatal("legacy dir removed without purge")
	}
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, f.ServiceID)); !os.IsNotExist(err) {
		t.Fatal("legacy dir survived purge")
	}
	if _, err := os.Stat(filepath.Join(root, other.ServiceID, "dump.rdb")); err != nil {
		t.Fatal("another service's legacy data was removed")
	}
}

func TestTeardownPurgeDoesNotFollowLegacySymlink(t *testing.T) {
	e := newEnv(t)
	f := testutil.Seed(t, e.st, store.TypeRedis)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "precious"), []byte("x"), 0o600)
	if err := os.Symlink(outside, filepath.Join(e.acts.Cfg.LegacyVolumeRoot, f.ServiceID)); err != nil {
		t.Fatal(err)
	}
	e.st.SoftDeleteService(context.Background(), f.ServiceID)
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Fatal("purge followed a symlink out of the legacy root")
	}
}

func TestCancelServiceDeploysCancelsOpenWorkflowsAndWaits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	d1, _ := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	d2, _ := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	d3, _ := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	e.st.SetDeploymentStatus(ctx, d2.ID, "deploying")
	e.st.SetDeploymentStatus(ctx, d3.ID, "deployed") // closed workflow: not cancelled
	other := testutil.Seed(t, e.st, store.TypeNodejs)
	od, _ := e.st.CreateDeployment(ctx, other.ServiceID, "main")
	e.st.SoftDeleteService(ctx, f.ServiceID)

	ft := &fakeTemporal{running: map[string]int{DeployWorkflowID(d2.ID): 3}, missing: map[string]bool{DeployWorkflowID(d1.ID): true}}
	e.acts.Temporal = ft
	e.acts.Cfg.CancelPollInterval = time.Millisecond
	e.acts.Cfg.CancelWaitTimeout = 5 * time.Second
	if _, err := e.run(t, e.acts.CancelServiceDeploys, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ft.cancelled)
	want := []string{DeployWorkflowID(d1.ID), DeployWorkflowID(d2.ID)}
	slices.Sort(want)
	if !slices.Equal(ft.cancelled, want) {
		t.Fatalf("cancelled %v, want %v (foreign %s, deployed %s)", ft.cancelled, want, od.ID, d3.ID)
	}
	if ft.running[DeployWorkflowID(d2.ID)] != 0 {
		t.Fatal("did not wait for the running workflow to close")
	}
}

func TestCancelServiceDeploysFailsRetryablyIfWorkflowWontClose(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	d, _ := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	e.st.SoftDeleteService(ctx, f.ServiceID)
	e.acts.Temporal = &fakeTemporal{running: map[string]int{DeployWorkflowID(d.ID): -1}}
	e.acts.Cfg.CancelPollInterval = time.Millisecond
	e.acts.Cfg.CancelWaitTimeout = 30 * time.Millisecond
	if _, err := e.run(t, e.acts.CancelServiceDeploys, TeardownInput{ServiceID: f.ServiceID}); err == nil || isNonRetryable(err) {
		t.Fatalf("err = %v, want a retryable error", err)
	}
}

// Task 2 STAFF-9: a deploy workflow that was already running (or starts late)
// for a deleted service must stop at its first store read.
func TestLoadJobAndBuildImageRefuseDeletedService(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	if err := e.st.SoftDeleteService(ctx, j.ServiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, e.acts.LoadJob, j.DeploymentID); !isNonRetryableType(err, "NotFound") {
		t.Fatalf("LoadJob err = %v", err)
	}
	if _, err := e.run(t, e.acts.BuildImage, BuildInput{DeploymentID: j.DeploymentID}); !isNonRetryableType(err, "NotFound") {
		t.Fatalf("BuildImage err = %v", err)
	}
	if len(e.builder.specs) != 0 {
		t.Fatal("builder ran for a deleted service")
	}
	// ClaimDeploy also refuses (the row is not claimable).
	if won, err := e.acts.ClaimDeploy(ctx, j.DeploymentID); err == nil && won {
		t.Fatal("ClaimDeploy won for a deleted service")
	}
}

func TestTeardownWorkflowRunsActivities(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	var a *Activities
	in := TeardownInput{ServiceID: "s1", PurgeData: true}
	var order []string
	env.OnActivity(a.CancelServiceDeploys, mock.Anything, in).Return(func(context.Context, TeardownInput) error {
		order = append(order, "cancel")
		return nil
	}).Once()
	env.OnActivity(a.TeardownService, mock.Anything, in).Return(func(context.Context, TeardownInput) error {
		order = append(order, "teardown")
		return nil
	}).Once()
	env.ExecuteWorkflow(TeardownWorkflow, in)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatal(env.GetWorkflowError())
	}
	if !slices.Equal(order, []string{"cancel", "teardown"}) {
		t.Fatalf("order %v", order)
	}
	env.AssertExpectations(t)
}
