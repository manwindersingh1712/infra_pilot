package workflows_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/store"
	"infrapilot/internal/workflows"
)

// These tests run against a REAL Temporal server. They are skipped unless
// TEMPORAL_E2E_ADDR is set (scripts/e2e-temporal.sh starts a throwaway server
// and sets it). Docker, nginx and Postgres are replaced by fakeWorld.

var stickyName = regexp.MustCompile(`"name": "[^"]+:[0-9a-f]{8}-[0-9a-f-]{27}"`)

const e2eWait = 4 * time.Minute

func e2eAddr(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}
	addr := os.Getenv("TEMPORAL_E2E_ADDR")
	if addr == "" {
		t.Skip("e2e: TEMPORAL_E2E_ADDR is not set")
	}
	return addr
}

func e2eClient(t *testing.T) client.Client {
	t.Helper()
	c, err := client.Dial(client.Options{HostPort: e2eAddr(t), Identity: "e2e"})
	if err != nil {
		t.Fatalf("dial temporal: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

// rid returns a cuid-shaped id (^[a-z0-9]{20,32}$).
func rid() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b)
	return "e2e" + hex.EncodeToString(b)
}

func startWorker(t *testing.T, c client.Client, tq string, w *fakeWorld) worker.Worker {
	t.Helper()
	wk := worker.New(c, tq, worker.Options{Identity: "e2e-worker"})
	wk.RegisterWorkflow(workflows.DeployWorkflow)
	wk.RegisterActivity(&fakeActs{w})
	if err := wk.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(wk.Stop)
	return wk
}

func startDeploy(t *testing.T, c client.Client, tq, id string) client.WorkflowRun {
	t.Helper()
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{
		ID: workflows.DeployWorkflowID(id), TaskQueue: tq,
		WorkflowExecutionTimeout: 10 * time.Minute,
	}, workflows.DeployWorkflow, id)
	if err != nil {
		t.Fatalf("start workflow: %v", err)
	}
	t.Cleanup(func() { _ = c.TerminateWorkflow(context.Background(), run.GetID(), run.GetRunID(), "e2e cleanup") })
	return run
}

func waitEntered(t *testing.T, h *hook) {
	t.Helper()
	select {
	case <-h.entered:
	case <-time.After(e2eWait):
		t.Fatal("activity never reached its hook")
	}
}

func getWithin(t *testing.T, run client.WorkflowRun) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
	defer cancel()
	return run.Get(ctx, nil)
}

func indexOf(t *testing.T, trace []string, step string) int {
	t.Helper()
	i := slices.Index(trace, step)
	if i < 0 {
		t.Fatalf("step %q missing from trace %v", step, trace)
	}
	return i
}

func TestE2EKillWorkerMidDeploy(t *testing.T) {
	c := e2eClient(t)
	tq, id := "e2e-kill-"+rid(), rid()
	w := newWorld()
	w.addRow(id, "svc"+id, store.StatusQueued)
	h := w.addHook("RunContainer:"+id, true) // only the first attempt blocks
	w1 := startWorker(t, c, tq, w)
	run := startDeploy(t, c, tq, id)

	waitEntered(t, h) // the container exists, RunContainer has not returned
	w1.Stop()         // the worker dies while the activity runs
	startWorker(t, c, tq, w)

	if err := getWithin(t, run); err != nil {
		t.Fatalf("workflow did not resume to success: %v", err)
	}
	if n := strings.Count(strings.Join(w.trace(), " "), "run:"+id); n < 2 {
		t.Fatalf("RunContainer ran %d time(s); the kill did not interrupt it", n)
	}
	if got := w.status(id); got != store.StatusDeployed {
		t.Fatalf("row status = %s, want deployed", got)
	}
	if got, want := w.liveContainers(), []string{workflows.ContainerName(id)}; !slices.Equal(got, want) {
		t.Fatalf("containers = %v, want exactly %v", got, want)
	}
	if got := w.routeOf("svc" + id); got != workflows.ContainerName(id) {
		t.Fatalf("route = %q", got)
	}
}

func TestE2ECancelDuringRunContainer(t *testing.T) {
	c := e2eClient(t)
	tq, old, id := "e2e-cancel-run-"+rid(), rid(), rid()
	svc := "svc" + old
	w := newWorld()
	w.addRow(old, svc, store.StatusDeployed)
	w.serve(old)
	w.addRow(id, svc, store.StatusQueued)
	h := w.addHook("RunContainer:"+id, false)
	startWorker(t, c, tq, w)
	run := startDeploy(t, c, tq, id)

	waitEntered(t, h)
	if err := c.CancelWorkflow(context.Background(), run.GetID(), run.GetRunID()); err != nil {
		t.Fatal(err)
	}
	if err := getWithin(t, run); err == nil {
		t.Fatal("cancelled workflow returned success")
	}
	tr := w.trace()
	if indexOf(t, tr, "failed:"+id) > indexOf(t, tr, "remove:"+workflows.ContainerName(id)) {
		// default path: MarkFailed first, then RemoveContainer
		t.Fatalf("unexpected cleanup order: %v", tr)
	}
	if slices.Contains(tr, "remove:"+workflows.ContainerName(old)) || slices.Contains(tr, "retire:"+old) {
		t.Fatalf("the serving container was touched: %v", tr)
	}
	if got, want := w.liveContainers(), []string{workflows.ContainerName(old)}; !slices.Equal(got, want) {
		t.Fatalf("containers = %v, want only the serving %v", got, want)
	}
	if w.status(id) != store.StatusFailed || w.status(old) != store.StatusDeployed {
		t.Fatalf("statuses: new=%s old=%s", w.status(id), w.status(old))
	}
	if got := w.routeOf(svc); got != workflows.ContainerName(old) {
		t.Fatalf("route = %q, want the serving container", got)
	}
}

func TestE2ECancelDuringSwitchRoute(t *testing.T) {
	c := e2eClient(t)
	tq, old, id := "e2e-cancel-route-"+rid(), rid(), rid()
	svc := "svc" + old
	w := newWorld()
	w.addRow(old, svc, store.StatusDeployed)
	w.serve(old)
	w.addRow(id, svc, store.StatusQueued)
	h := w.addHook("SwitchRoute:"+workflows.ContainerName(id), false)
	startWorker(t, c, tq, w)
	run := startDeploy(t, c, tq, id)

	waitEntered(t, h)
	if err := c.CancelWorkflow(context.Background(), run.GetID(), run.GetRunID()); err != nil {
		t.Fatal(err)
	}
	if err := getWithin(t, run); err == nil {
		t.Fatal("cancelled workflow returned success")
	}
	tr := w.trace()
	rt, rv, rm, mf := indexOf(t, tr, "route:"+workflows.ContainerName(id)), indexOf(t, tr, "revert:"+id),
		indexOf(t, tr, "remove:"+workflows.ContainerName(id)), indexOf(t, tr, "failed:"+id)
	if rt >= rv || rv >= rm || rm >= mf || mf != len(tr)-1 {
		t.Fatalf("want route < revert < remove < failed(last), got %v", tr)
	}
	if slices.Contains(tr, "remove:"+workflows.ContainerName(old)) || slices.Contains(tr, "retire:"+old) {
		t.Fatalf("the serving container was removed: %v", tr)
	}
	if got, want := w.liveContainers(), []string{workflows.ContainerName(old)}; !slices.Equal(got, want) {
		t.Fatalf("containers = %v, want only %v", got, want)
	}
	if got := w.routeOf(svc); got != workflows.ContainerName(old) {
		t.Fatalf("route = %q, want reverted to %q", got, workflows.ContainerName(old))
	}
}

func TestE2ETwoDeploymentsOfOneServiceQueued(t *testing.T) {
	c := e2eClient(t)
	tq, a, b := "e2e-two-"+rid(), rid(), rid()
	svc := "svc" + a
	w := newWorld()
	w.addRow(a, svc, store.StatusQueued)
	w.addRow(b, svc, store.StatusQueued)
	ha, hb := w.addHook("RunContainer:"+a, false), w.addHook("RunContainer:"+b, false)
	startWorker(t, c, tq, w)
	ra, rb := startDeploy(t, c, tq, a), startDeploy(t, c, tq, b)

	select { // whoever wins ClaimDeploy reaches RunContainer first
	case <-ha.entered:
	case <-hb.entered:
	case <-time.After(e2eWait):
		t.Fatal("no deployment reached RunContainer")
	}
	deadline := time.Now().Add(e2eWait)
	for w.busyCount() < 1 { // the other one is waiting on ServiceBusy
		if time.Now().After(deadline) {
			t.Fatal("the second deployment never saw ServiceBusy")
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(ha.release)
	close(hb.release)

	if err := getWithin(t, ra); err != nil {
		t.Fatalf("deployment a: %v", err)
	}
	if err := getWithin(t, rb); err != nil {
		t.Fatalf("deployment b: %v", err)
	}
	if w.status(a) != store.StatusDeployed || w.status(b) != store.StatusDeployed {
		t.Fatalf("statuses: a=%s b=%s", w.status(a), w.status(b))
	}
	if n := w.busyCount(); n != 1 {
		t.Fatalf("ServiceBusy returned %d times, want exactly 1", n)
	}
	tr := w.trace()
	first, second := a, b
	if indexOf(t, tr, "deployed:"+b) < indexOf(t, tr, "deployed:"+a) {
		first, second = b, a
	}
	if indexOf(t, tr, "claim-deploy:"+second) < indexOf(t, tr, "deployed:"+first) {
		t.Fatalf("the second deployment claimed before the first finished: %v", tr)
	}
	want := workflows.ContainerName(second)
	if got := w.liveContainers(); !slices.Equal(got, []string{want}) {
		t.Fatalf("containers = %v, want exactly [%s]", got, want)
	}
	if got := w.routeOf(svc); got != want {
		t.Fatalf("route = %q, want %q", got, want)
	}
}

func starterDescribe(t *testing.T, c client.Client, id string) (string, enumspb.WorkflowExecutionStatus) {
	t.Helper()
	d, err := c.DescribeWorkflowExecution(context.Background(), workflows.DeployWorkflowID(id), "")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	return d.WorkflowExecutionInfo.Execution.RunId, d.WorkflowExecutionInfo.Status
}

// countExecutions counts executions (any run, any status) of the workflow ID.
func countExecutions(t *testing.T, c client.Client, id string) int {
	t.Helper()
	q := `WorkflowId = "` + workflows.DeployWorkflowID(id) + `"`
	var n int
	for range 50 { // visibility is eventually consistent
		resp, err := c.CountWorkflow(context.Background(), &countReq{Query: q})
		if err == nil {
			if n = int(resp.Count); n >= 1 {
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return n
}

// TestE2EStarterRejectDuplicate uses the production Starter on its fixed task
// queue. No worker polls it, so the workflow stays open until the test ends it.
func TestE2EStarterRejectDuplicate(t *testing.T) {
	c := e2eClient(t)
	st := workflows.Starter{Client: c}
	id := rid()
	payload := []byte(`{"deploymentId":"` + id + `"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		_ = c.TerminateWorkflow(context.Background(), workflows.DeployWorkflowID(id), "", "e2e cleanup")
	})

	if err := st.Start(ctx, store.EventDeployRequested, payload); err != nil {
		t.Fatalf("first start: %v", err)
	}
	run1, status := starterDescribe(t, c, id)
	if status != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatalf("status = %v, want running", status)
	}
	// Same ID while it is running: a clean success, no new run.
	if err := st.Start(ctx, store.EventBuildRequested, payload); err != nil {
		t.Fatalf("duplicate start of a running workflow: %v", err)
	}
	if run2, _ := starterDescribe(t, c, id); run2 != run1 {
		t.Fatalf("a second run %s started next to %s", run2, run1)
	}

	// Same ID after it closed: still a clean success (REJECT_DUPLICATE).
	if err := c.TerminateWorkflow(ctx, workflows.DeployWorkflowID(id), run1, "e2e: close it"); err != nil {
		t.Fatal(err)
	}
	if err := st.Start(ctx, store.EventDeployRequested, payload); err != nil {
		t.Fatalf("duplicate start of a closed workflow: %v", err)
	}
	run3, status := starterDescribe(t, c, id)
	if run3 != run1 || status != enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED {
		t.Fatalf("after closed duplicate: run %s (want %s), status %v", run3, run1, status)
	}
	if n := countExecutions(t, c, id); n != 1 {
		t.Fatalf("%d executions of the workflow ID exist, want 1", n)
	}
}

// TestE2ERecordDeployHistory records a real DeployWorkflow history for
// TestDeployWorkflowReplay. It runs only with RECORD_DEPLOY_HISTORY=1
// (scripts/e2e-temporal.sh --record does that). Run it again whenever
// DeployWorkflow changes in a way the replay test reports as nondeterministic.
func TestE2ERecordDeployHistory(t *testing.T) {
	_ = e2eAddr(t)
	if os.Getenv("RECORD_DEPLOY_HISTORY") != "1" {
		t.Skip("set RECORD_DEPLOY_HISTORY=1 to rewrite testdata/deploy_history.json")
	}
	c := e2eClient(t)
	tq, id := "e2e-record-"+rid(), "recordeddeploy000001"
	w := newWorld()
	w.addRow(id, "recordedservice0001", store.StatusQueued)
	startWorker(t, c, tq, w)
	run := startDeploy(t, c, tq, id)
	if err := getWithin(t, run); err != nil {
		t.Fatalf("recorded deploy failed: %v", err)
	}
	iter := c.GetWorkflowHistory(context.Background(), run.GetID(), run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	b, err := temporalproto.CustomJSONMarshalOptions{Indent: "  "}.Marshal(historyFrom(t, iter))
	if err != nil {
		t.Fatal(err)
	}
	// The sticky task queue name carries this machine's host name.
	b = stickyName.ReplaceAll(b, []byte(`"name": "sticky-queue"`))
	if err := os.WriteFile("testdata/deploy_history.json", append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
