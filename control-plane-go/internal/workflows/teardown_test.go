package workflows

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"infrapilot/internal/metrics"
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
	// purgeData=false on a managed DB keeps only the generated password row.
	if m, _ := e.st.EnvMap(ctx, f.ServiceID); len(m) != 1 || m["REDIS_PASSWORD"] != "enc:v1:x" {
		t.Fatalf("env vars after keep: %v", m)
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
	if m, _ := e.st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
		t.Fatalf("purge left env vars: %v", m)
	}
}

// Ruling 3: the activity passes keepGeneratedSecret = !PurgeData.
func TestTeardownServiceKeepsPasswordOnlyWithoutPurge(t *testing.T) {
	for _, purge := range []bool{false, true} {
		e := newEnv(t)
		ctx := context.Background()
		f := testutil.Seed(t, e.st, store.TypeMongoDB)
		e.st.UpsertEnvVar(ctx, f.ServiceID, "MONGO_INITDB_ROOT_PASSWORD", "enc:v1:pw")
		e.st.UpsertEnvVar(ctx, f.ServiceID, "EXTRA", "enc:v1:x")
		e.st.SoftDeleteService(ctx, f.ServiceID)
		if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID, PurgeData: purge}); err != nil {
			t.Fatal(err)
		}
		m, _ := e.st.EnvMap(ctx, f.ServiceID)
		if !purge && (len(m) != 1 || m["MONGO_INITDB_ROOT_PASSWORD"] != "enc:v1:pw") {
			t.Errorf("keep: %v", m)
		}
		if purge && len(m) != 0 {
			t.Errorf("purge: %v", m)
		}
	}
	// A docker-type service that happens to use REDIS_PASSWORD loses everything.
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeDocker)
	e.st.UpsertEnvVar(ctx, f.ServiceID, "REDIS_PASSWORD", "enc:v1:pw")
	e.st.SoftDeleteService(ctx, f.ServiceID)
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
		t.Fatalf("docker service kept rows: %v", m)
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

// Ruling 1: teardown retries without limit; only InvalidInput and NotDeleted are permanent.
func TestTeardownRetryPolicy(t *testing.T) {
	r := teardownRetry
	if r.MaximumAttempts != 0 || r.InitialInterval != 10*time.Second || r.BackoffCoefficient != 2 || r.MaximumInterval != 5*time.Minute {
		t.Fatalf("teardownRetry = %+v", r)
	}
	got := slices.Clone(r.NonRetryableErrorTypes)
	slices.Sort(got)
	if !slices.Equal(got, []string{"InvalidInput", "NotDeleted"}) {
		t.Fatalf("non-retryable types = %v", got)
	}
	for name, c := range map[string]struct {
		o     workflow.ActivityOptions
		start time.Duration
		hbeat time.Duration
	}{
		"cancel":   {cancelOptions(), 6 * time.Minute, time.Minute},
		"teardown": {teardownOptions(), 2 * time.Minute, 0},
	} {
		if c.o.RetryPolicy != teardownRetry {
			t.Errorf("%s options do not use teardownRetry: %+v", name, c.o.RetryPolicy)
		}
		if c.o.ScheduleToCloseTimeout != 0 || c.o.StartToCloseTimeout != c.start || c.o.HeartbeatTimeout != c.hbeat {
			t.Errorf("%s options = %+v", name, c.o)
		}
	}
	// Deploy workflows keep their bounded policy.
	if retry.MaximumAttempts != 6 {
		t.Fatalf("deploy retry changed: %+v", retry)
	}
}

// An activity that fails many times (more than the old limit of 6) and then
// succeeds completes the workflow; the test clock skips the backoff.
func TestTeardownWorkflowRetriesTransientFailuresUntilSuccess(t *testing.T) {
	for _, failing := range []string{"cancel", "teardown"} {
		var s testsuite.WorkflowTestSuite
		env := s.NewTestWorkflowEnvironment()
		env.RegisterActivity(&Activities{})
		var a *Activities
		in := TeardownInput{ServiceID: "s1"}
		const failures = 8
		calls := map[string]int{}
		step := func(name string) func(context.Context, TeardownInput) error {
			return func(context.Context, TeardownInput) error {
				calls[name]++
				if name == failing && calls[name] <= failures {
					return errors.New("docker daemon down")
				}
				return nil
			}
		}
		env.OnActivity(a.CancelServiceDeploys, mock.Anything, in).Return(step("cancel"))
		env.OnActivity(a.TeardownService, mock.Anything, in).Return(step("teardown"))
		env.ExecuteWorkflow(TeardownWorkflow, in)
		if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
			t.Fatalf("%s: %v", failing, env.GetWorkflowError())
		}
		if calls[failing] != failures+1 {
			t.Fatalf("%s: %d attempts, want %d", failing, calls[failing], failures+1)
		}
	}
}

func TestTeardownWorkflowStopsOnPermanentErrors(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	var a *Activities
	in := TeardownInput{ServiceID: "s1"}
	calls := 0
	env.OnActivity(a.CancelServiceDeploys, mock.Anything, in).Return(func(context.Context, TeardownInput) error {
		return nil
	})
	env.OnActivity(a.TeardownService, mock.Anything, in).Return(func(context.Context, TeardownInput) error {
		calls++
		return temporal.NewNonRetryableApplicationError("service is not deleted", "NotDeleted", nil)
	})
	env.ExecuteWorkflow(TeardownWorkflow, in)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() == nil || calls != 1 {
		t.Fatalf("err %v, calls %d", env.GetWorkflowError(), calls)
	}
}

func TestTeardownFailureMetricAndLogs(t *testing.T) {
	e := newEnv(t)
	// A live service: TeardownService fails (NotDeleted), counted under step=teardown.
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID}); err == nil {
		t.Fatal("want an error")
	}
	// A bad id fails CancelServiceDeploys, counted under step=cancel.
	if _, err := e.run(t, e.acts.CancelServiceDeploys, TeardownInput{ServiceID: "../x"}); err == nil {
		t.Fatal("want an error")
	}
	body := scrape(t, e.metrics)
	for _, want := range []string{
		`cp_teardown_activity_failures_total{step="teardown"} 1`,
		`cp_teardown_activity_failures_total{step="cancel"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in\n%s", want, body)
		}
	}
	// Success does not count.
	e.st.SoftDeleteService(context.Background(), f.ServiceID)
	if _, err := e.run(t, e.acts.TeardownService, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	if body := scrape(t, e.metrics); !strings.Contains(body, `cp_teardown_activity_failures_total{step="teardown"} 1`) {
		t.Fatalf("success changed the counter:\n%s", body)
	}
}

func TestTeardownFailureLogLevelEscalatesFromAttempt3(t *testing.T) {
	var buf bytes.Buffer
	a := &Activities{Log: slog.New(slog.NewTextHandler(&buf, nil)), Metrics: metrics.NewWorker()}
	boom := errors.New("nginx reload failed")
	for _, c := range []struct {
		attempt int32
		level   string
	}{{1, "WARN"}, {2, "WARN"}, {3, "ERROR"}, {7, "ERROR"}} {
		buf.Reset()
		a.recordTeardownFailure("teardown", svcID, c.attempt, boom)
		out := buf.String()
		if !strings.Contains(out, "level="+c.level) || !strings.Contains(out, "serviceId="+svcID) ||
			!strings.Contains(out, "step=teardown") || !strings.Contains(out, "attempt="+strconv.Itoa(int(c.attempt))) {
			t.Errorf("attempt %d: %q", c.attempt, out)
		}
	}
	// A nil metrics registry and a nil error are fine.
	(&Activities{Log: slog.New(slog.NewTextHandler(&buf, nil))}).recordTeardownFailure("cancel", svcID, 1, boom)
}

// Ruling 6 (STAFF-4a): CancelServiceDeploys heartbeats while it waits, or its
// 1-minute heartbeat timeout would kill a legitimate 3-minute wait.
func TestCancelServiceDeploysHeartbeatsWhileWaiting(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	d, _ := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	e.st.SoftDeleteService(ctx, f.ServiceID)
	e.acts.Temporal = &fakeTemporal{running: map[string]int{DeployWorkflowID(d.ID): 5}}
	e.acts.Cfg.CancelPollInterval = time.Millisecond
	e.acts.Cfg.CancelWaitTimeout = 5 * time.Second

	var s testsuite.WorkflowTestSuite
	ae := s.NewTestActivityEnvironment()
	ae.RegisterActivity(e.acts)
	var beats int
	ae.SetOnActivityHeartbeatListener(func(*activity.Info, converter.EncodedValues) { beats++ })
	if _, err := ae.ExecuteActivity(e.acts.CancelServiceDeploys, TeardownInput{ServiceID: f.ServiceID}); err != nil {
		t.Fatal(err)
	}
	if beats < 1 {
		t.Fatalf("%d heartbeats while waiting, want at least 1", beats)
	}
}

// Ruling 6 (STAFF-4b): when the cancel step fails permanently, nothing is torn down.
func TestTeardownWorkflowSkipsTeardownWhenCancelFails(t *testing.T) {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	var a *Activities
	in := TeardownInput{ServiceID: "s1", PurgeData: true}
	env.OnActivity(a.CancelServiceDeploys, mock.Anything, in).Return(
		temporal.NewNonRetryableApplicationError("invalid service id", "InvalidInput", nil))
	teardownCalls := 0
	env.OnActivity(a.TeardownService, mock.Anything, in).Return(func(context.Context, TeardownInput) error {
		teardownCalls++
		return nil
	})
	env.ExecuteWorkflow(TeardownWorkflow, in)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() == nil {
		t.Fatalf("workflow error = %v, want a failure", env.GetWorkflowError())
	}
	if teardownCalls != 0 {
		t.Fatalf("TeardownService ran %d times after the cancel step failed", teardownCalls)
	}
}
