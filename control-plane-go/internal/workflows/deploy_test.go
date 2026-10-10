package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/store"
)

type trace struct {
	mu     sync.Mutex
	steps  []string
	inputs []string // JSON of every activity input, as it would enter history
}

// rec records the step and the serialised input.
func (tr *trace) rec(step string, in any) {
	b, _ := json.Marshal(in)
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.steps = append(tr.steps, step)
	tr.inputs = append(tr.inputs, step+" "+string(b))
}

func (tr *trace) get() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.steps)
}

func (tr *trace) in() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.inputs)
}

func (tr *trace) has(s string) bool { return slices.Contains(tr.get(), s) }

func (tr *trace) hasPrefix(p string) bool {
	return slices.ContainsFunc(tr.get(), func(s string) bool { return strings.HasPrefix(s, p) })
}

func strp(s string) *string { return &s }

// appJob keeps RepoURL nil, as LoadJob returns it.
func appJob() store.Job {
	return store.Job{DeploymentID: "d1", Status: store.StatusQueued, CommitSha: "main", ServiceID: "s1",
		ServiceType: store.TypeNodejs, Branch: "main", ProjectID: "p1"}
}

func managedJob() store.Job {
	j := appJob()
	j.ServiceType = store.TypeRedis
	return j
}

// mockAll registers every activity, recording its name in tr. Individual tests
// override single activities by registering their own mock first.
func mockAll(env *testsuite.TestWorkflowEnvironment, tr *trace, job store.Job) {
	var a *Activities
	env.OnActivity(a.LoadJob, mock.Anything, mock.Anything).Return(job, nil).Maybe()
	env.OnActivity(a.Claim, mock.Anything, mock.Anything).Return(func(_ context.Context, in ClaimInput) (bool, error) {
		tr.rec("claim:"+in.To, in)
		return true, nil
	}).Maybe()
	env.OnActivity(a.ClaimDeploy, mock.Anything, mock.Anything).Return(func(_ context.Context, id string) (bool, error) {
		tr.rec("claim:deploying", id)
		return true, nil
	}).Maybe()
	env.OnActivity(a.BuildImage, mock.Anything, mock.Anything).Return(func(_ context.Context, in BuildInput) (string, error) {
		tr.rec("build", in)
		return "reg/s1:main", nil
	}).Maybe()
	env.OnActivity(a.MarkBuilt, mock.Anything, mock.Anything).Return(func(_ context.Context, in MarkBuiltInput) error {
		tr.rec("built", in)
		return nil
	}).Maybe()
	env.OnActivity(a.EnsureNetwork, mock.Anything, mock.Anything).Return(func(_ context.Context, p string) (string, error) {
		tr.rec("network", p)
		return "cp-proj-" + p, nil
	}).Maybe()
	env.OnActivity(a.EnsureCredentials, mock.Anything, mock.Anything).Return(func(_ context.Context, j store.Job) error {
		tr.rec("credentials", j)
		return nil
	}).Maybe()
	env.OnActivity(a.EnsureVolume, mock.Anything, mock.Anything).Return(func(_ context.Context, j store.Job) (string, error) {
		tr.rec("volume", j)
		return "cp-vol-s1", nil
	}).Maybe()
	env.OnActivity(a.RetirePrevious, mock.Anything, mock.Anything).Return(func(_ context.Context, in RetireInput) error {
		tr.rec("retire", in)
		return nil
	}).Maybe()
	env.OnActivity(a.RunContainer, mock.Anything, mock.Anything).Return(func(_ context.Context, in RunInput) (RunResult, error) {
		tr.rec("run", in)
		return RunResult{ContainerID: "c1", ContainerName: "cp-" + in.Job.DeploymentID, ContainerPort: 3000}, nil
	}).Maybe()
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(func(_ context.Context, in WaitInput) error {
		tr.rec("healthy", in)
		return nil
	}).Maybe()
	env.OnActivity(a.SwitchRoute, mock.Anything, mock.Anything).Return(func(_ context.Context, in RouteInput) (string, error) {
		tr.rec("route", in)
		return "http://s1.localhost", nil
	}).Maybe()
	env.OnActivity(a.RevertRoute, mock.Anything, mock.Anything).Return(func(_ context.Context, in RevertInput) error {
		tr.rec("revert", in)
		return nil
	}).Maybe()
	env.OnActivity(a.MarkDeployed, mock.Anything, mock.Anything).Return(func(_ context.Context, in DeployedInput) error {
		tr.rec("deployed", in)
		return nil
	}).Maybe()
	env.OnActivity(a.StartLogStream, mock.Anything, mock.Anything).Return(func(_ context.Context, in StreamInput) error {
		tr.rec("logs", in)
		return nil
	}).Maybe()
	env.OnActivity(a.MarkFailed, mock.Anything, mock.Anything).Return(func(_ context.Context, id string) error {
		tr.rec("failed", id)
		return nil
	}).Maybe()
	env.OnActivity(a.RemoveContainer, mock.Anything, mock.Anything).Return(func(_ context.Context, n string) error {
		tr.rec("remove:"+n, n)
		return nil
	}).Maybe()
}

func newWorkflowEnv() *testsuite.TestWorkflowEnvironment {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	return env
}

func nonRetryable(msg, typ string) error {
	return temporal.NewNonRetryableApplicationError(msg, typ, nil)
}

func TestDeployBuiltService(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("err = %v", env.GetWorkflowError())
	}
	want := []string{"claim:building", "build", "built", "claim:deploying", "network", "run", "healthy", "route", "deployed", "retire", "logs"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n got %v\nwant %v", got, want)
	}
	// FWD-A1: BuildImage gets only the deployment ID; the built image reaches RunContainer.
	for _, in := range tr.in() {
		if strings.HasPrefix(in, "build ") && in != `build {"DeploymentID":"d1"}` {
			t.Fatalf("build input = %s", in)
		}
		if strings.HasPrefix(in, "run ") && !strings.Contains(in, `"Image":"reg/s1:main"`) {
			t.Fatalf("run input lacks the built image: %s", in)
		}
	}
}

func TestDeployManagedStopsPreviousBeforeStart(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	mockAll(env, tr, managedJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil {
		t.Fatal(env.GetWorkflowError())
	}
	want := []string{"claim:deploying", "network", "credentials", "volume", "retire", "run", "healthy", "deployed", "logs"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n got %v\nwant %v", got, want)
	}
}

// Failure path 1: before SwitchRoute the new container is removed and the row marked failed.
func TestHealthFailureRemovesContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(errors.New("not reachable")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
	got := tr.get()
	if slices.Contains(got, "route") || slices.Contains(got, "retire") || slices.Contains(got, "deployed") {
		t.Fatalf("traffic must not switch after a failed health check: %v", got)
	}
	if !slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("cleanup steps: %v", got)
	}
	env.AssertExpectations(t)
}

// Failure path 2: SwitchRoute errored, so the route state is unknown. The row
// is marked failed, the route is reverted, and the container is removed only
// after the revert succeeded.
func TestSwitchRouteErrorRevertsRouteThenRemoves(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.SwitchRoute, mock.Anything, mock.Anything).Return("", nonRetryable("nginx reload failed", "Route")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil {
		t.Fatal("want error")
	}
	got := tr.get()
	if want := []string{"run", "healthy", "failed", "revert", "remove:cp-d1"}; !slices.Equal(got[len(got)-len(want):], want) || tr.has("deployed") {
		t.Fatalf("steps: %v", got)
	}
	if in := tr.in(); !slices.Contains(in, `revert {"ServiceID":"s1","DeploymentID":"d1"}`) {
		t.Fatalf("inputs: %v", in)
	}
}

// If the revert fails the container is kept: it may be serving.
func TestSwitchRouteErrorRevertFailsKeepsContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.SwitchRoute, mock.Anything, mock.Anything).Return("", nonRetryable("nginx reload failed", "Route")).Once()
	env.OnActivity(a.RevertRoute, mock.Anything, mock.Anything).Return(nonRetryable("nginx still down", "Route")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil {
		t.Fatal("want error")
	}
	if !tr.has("failed") || tr.hasPrefix("remove:") || tr.has("deployed") || tr.has("retire") {
		t.Fatalf("steps: %v", tr.get())
	}
	env.AssertExpectations(t)
}

// A cancel during SwitchRoute is waited for (WaitForCancellation), then the
// route state is treated as unknown.
func TestCancelDuringSwitchRouteReverts(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.SwitchRoute, mock.Anything, mock.Anything).Return(func(ctx context.Context, _ RouteInput) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "http://s1.localhost", nil
		}
	}).Once()
	mockAll(env, tr, appJob())
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	got := tr.get()
	if env.GetWorkflowError() == nil || tr.has("deployed") ||
		!slices.Equal(got[len(got)-3:], []string{"failed", "revert", "remove:cp-d1"}) {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), got)
	}
}

func TestRunContainerFailureMarksFailed(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.RunContainer, mock.Anything, mock.Anything).Return(RunResult{}, nonRetryable("bad spec", "InvalidSpec")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	got := tr.get()
	if env.GetWorkflowError() == nil || tr.has("route") || tr.has("revert") ||
		!slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), got)
	}
}

// Failure path 1 for managed services: DB failure after RunContainer, before MarkDeployed.
func TestManagedHealthFailureAfterRunRemovesContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(errors.New("not reachable")).Once()
	mockAll(env, tr, managedJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	got := tr.get()
	if env.GetWorkflowError() == nil || tr.has("route") || tr.has("deployed") ||
		!slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), got)
	}
}

func TestManagedMarkDeployedFailureRemovesContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.MarkDeployed, mock.Anything, mock.Anything).Return(nonRetryable("db down", "DB")).Once()
	mockAll(env, tr, managedJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	got := tr.get()
	if env.GetWorkflowError() == nil || !slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), got)
	}
}

// Failure path 2: after SwitchRoute, before MarkDeployed, the serving container and route stay.
func TestMarkDeployedFailureAfterSwitchKeepsContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.MarkDeployed, mock.Anything, mock.Anything).Return(nonRetryable("db down", "DB")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	got := tr.get()
	if env.GetWorkflowError() == nil {
		t.Fatal("want error")
	}
	if !slices.Contains(got, "route") || !slices.Contains(got, "failed") {
		t.Fatalf("steps: %v", got)
	}
	if tr.hasPrefix("remove:") || tr.has("retire") || tr.has("logs") {
		t.Fatalf("the serving container must be kept and nothing retired: %v", got)
	}
	env.AssertExpectations(t)
}

// Failure path 3: after MarkDeployed the row stays deployed, no cleanup runs, the run fails visibly.
func TestRetireFailureAfterDeployKeepsDeployment(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.RetirePrevious, mock.Anything, mock.Anything).Return(nonRetryable("docker rm failed", "Docker")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "post-deploy retire") {
		t.Fatalf("err = %v", err)
	}
	if !tr.has("deployed") || tr.has("failed") || tr.hasPrefix("remove:") || tr.has("logs") {
		t.Fatalf("steps: %v", tr.get())
	}
}

func TestLogStreamFailureKeepsDeployment(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.StartLogStream, mock.Anything, mock.Anything).Return(nonRetryable("stream", "Logs")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "post-deploy logs") {
		t.Fatalf("err = %v", err)
	}
	if !tr.has("deployed") || tr.has("failed") || tr.hasPrefix("remove:") {
		t.Fatalf("steps: %v", tr.get())
	}
}

func TestManagedLogStreamFailureKeepsDeployment(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.StartLogStream, mock.Anything, mock.Anything).Return(nonRetryable("stream", "Logs")).Once()
	mockAll(env, tr, managedJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil || !tr.has("deployed") || tr.has("failed") || tr.hasPrefix("remove:") {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

// Failure path 4: the build is skipped when the image exists.
func TestPrebuiltImageSkipsBuild(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	job := appJob()
	job.Image = strp("reg/s1:prebuilt")
	mockAll(env, tr, job)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil {
		t.Fatal(env.GetWorkflowError())
	}
	want := []string{"claim:deploying", "network", "run", "healthy", "route", "deployed", "retire", "logs"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n got %v\nwant %v", got, want)
	}
	found := false
	for _, in := range tr.in() {
		if strings.HasPrefix(in, "run ") {
			found = strings.Contains(in, `"Image":"reg/s1:prebuilt"`)
		}
	}
	if !found {
		t.Fatalf("RunContainer must get the existing image: %v", tr.in())
	}
}

// Failure path 4: a lost claim is a silent no-op, before and after the build.
func TestDeployLostClaimIsANoop(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.Claim, mock.Anything, mock.Anything).Return(false, nil).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil || len(tr.get()) != 0 {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

func TestDeployLostDeployClaimEndsWithoutCleanup(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.Claim, mock.Anything, ClaimInput{DeploymentID: "d1", From: store.StatusQueued, To: store.StatusBuilding}).Return(true, nil).Once()
	env.OnActivity(a.ClaimDeploy, mock.Anything, "d1").Return(false, nil).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil || tr.has("failed") || tr.hasPrefix("remove:") || tr.has("run") {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

func busyErr() error { return temporal.NewApplicationError("service busy", "ServiceBusy") }

// STAFF-2: a deploy of the same service is running. The claim retries every 5 s
// (the row stays queued) and the deploy goes ahead once the claim wins.
func TestServiceBusyRetriesThenDeploys(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	calls := 0
	env.OnActivity(a.ClaimDeploy, mock.Anything, mock.Anything).Return(func(_ context.Context, id string) (bool, error) {
		calls++
		if calls <= 3 {
			return false, busyErr()
		}
		tr.rec("claim:deploying", id)
		return true, nil
	})
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || !tr.has("deployed") || tr.has("failed") {
		t.Fatalf("calls=%d steps=%v", calls, tr.get())
	}
}

// STAFF-2: after ScheduleToClose (30 min of busy) the run takes failure path 1.
func TestServiceBusyExpiredMarksFailed(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.ClaimDeploy, mock.Anything, mock.Anything).Return(false, busyErr())
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil {
		t.Fatal("want error")
	}
	got := tr.get()
	if tr.has("run") || tr.has("route") || !slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("steps = %v", got)
	}
}

func TestDeployMissingRepoIsNotRetried(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.BuildImage, mock.Anything, mock.Anything).
		Return("", nonRetryable("repoUrl_required_for_build", "MissingRepo")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil || !tr.has("failed") {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
	env.AssertExpectations(t)
}

func TestDeploySkipsWhenNotQueued(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	job := appJob()
	job.Status = store.StatusDeployed
	mockAll(env, tr, job)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil || len(tr.get()) != 0 {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

func TestDeployLoadJobFailureEndsWithoutCleanup(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.LoadJob, mock.Anything, mock.Anything).Return(store.Job{}, nonRetryable("deployment not found", "NotFound")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil || len(tr.get()) != 0 {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

// Cleanup must run even when the workflow is cancelled mid-deploy.
func TestCancellationStillRunsCleanup(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(func(ctx context.Context, _ WaitInput) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	}).Once()
	mockAll(env, tr, appJob())
	env.RegisterDelayedCallback(env.CancelWorkflow, time.Second)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil {
		t.Fatal("a cancelled deploy must report an error")
	}
	got := tr.get()
	if tr.has("route") || tr.has("deployed") || len(got) < 2 || !slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("cleanup after cancel: %v", got)
	}
}

// Nothing secret may enter activity inputs, even if a job carried a repo URL.
func TestNoRepoURLInActivityInputsOrErrors(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	job := appJob()
	job.RepoURL = strp("https://ghp_SECRET9@example.com/x.git")
	var a *Activities
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(errors.New("not reachable")).Once()
	mockAll(env, tr, job)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil {
		t.Fatal("want error")
	}
	if len(tr.in()) == 0 {
		t.Fatal("no inputs recorded")
	}
	for _, in := range tr.in() {
		if strings.Contains(in, "ghp_") || strings.Contains(in, "example.com") {
			t.Fatalf("secret in activity input: %s", in)
		}
	}
	if strings.Contains(env.GetWorkflowError().Error(), "ghp_") {
		t.Fatalf("secret in error: %v", env.GetWorkflowError())
	}
}

func TestDeployIsDeterministicAcrossRuns(t *testing.T) {
	var first []string
	for i := range 3 {
		env, tr := newWorkflowEnv(), &trace{}
		mockAll(env, tr, appJob())
		env.ExecuteWorkflow(DeployWorkflow, "d1")
		if env.GetWorkflowError() != nil {
			t.Fatal(env.GetWorkflowError())
		}
		if i == 0 {
			first = tr.get()
		} else if !slices.Equal(first, tr.get()) {
			t.Fatalf("run %d differs: %v vs %v", i, tr.get(), first)
		}
	}
}

func TestActivityOptions(t *testing.T) {
	s := shortOptions()
	if s.StartToCloseTimeout != 2*time.Minute || s.RetryPolicy.MaximumAttempts != 6 {
		t.Fatalf("short = %+v", s)
	}
	if b := buildOptions(); b.StartToCloseTimeout != 30*time.Minute || b.HeartbeatTimeout != time.Minute || b.RetryPolicy.MaximumAttempts != 6 {
		t.Fatalf("build = %+v", b)
	}
	r := runOptions()
	if !r.WaitForCancellation {
		t.Fatal("RunContainer must wait for cancellation: cleanup may only run after docker run ended")
	}
	if !routeOptions().WaitForCancellation {
		t.Fatal("SwitchRoute must wait for cancellation: its outcome decides the cleanup")
	}
	if r.StartToCloseTimeout != 10*time.Minute || r.HeartbeatTimeout != time.Minute || r.RetryPolicy.MaximumAttempts != 3 {
		t.Fatalf("run = %+v", r)
	}
	if r.RetryPolicy == shortOptions().RetryPolicy {
		t.Fatal("RunContainer must not share the short retry policy")
	}
	c := claimDeployOptions()
	if c.StartToCloseTimeout != 30*time.Second || c.ScheduleToCloseTimeout != 30*time.Minute ||
		c.RetryPolicy.InitialInterval != 5*time.Second || c.RetryPolicy.BackoffCoefficient != 1 || c.RetryPolicy.MaximumAttempts != 0 {
		t.Fatalf("claimDeploy = %+v", c)
	}
	if b := buildOptions(); b.ScheduleToCloseTimeout != 90*time.Minute {
		t.Fatalf("build ScheduleToClose = %s", b.ScheduleToCloseTimeout)
	}
	if healthStartToClose != 11*time.Minute {
		t.Fatalf("healthStartToClose = %s", healthStartToClose)
	}
	h := healthOptions()
	if h.RetryPolicy.MaximumAttempts != 1 || h.StartToCloseTimeout != 11*time.Minute || h.HeartbeatTimeout != 30*time.Second {
		t.Fatalf("health = %+v", h)
	}
}
