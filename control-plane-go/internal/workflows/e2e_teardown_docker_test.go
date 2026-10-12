package workflows_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/runtime"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/workflows"
)

// This test (adapted from the Plan 2 Task 12 Verifier's scratch test) drives
// the REAL TeardownWorkflow against real Temporal, real Postgres (a testcontainer),
// real Docker (busybox containers and volumes, a throwaway nginx for the reload)
// and the real DeployWorkflow blocked in RunContainer. Every Docker object it
// creates is named after fresh cuids or "e2etd-", carries the label cp.e2e=teardown
// and is removed by exact name. It skips when the two small images are not local.

type teardownWrap struct {
	*workflows.Activities
	w *fakeWorld
}

func (x teardownWrap) step(s string) {
	x.w.mu.Lock()
	x.w.steps = append(x.w.steps, s)
	x.w.mu.Unlock()
}

func (x teardownWrap) CancelServiceDeploys(ctx context.Context, in workflows.TeardownInput) error {
	x.step("ACT cancel-start")
	err := x.Activities.CancelServiceDeploys(ctx, in)
	x.step("ACT cancel-end")
	return err
}

func (x teardownWrap) TeardownService(ctx context.Context, in workflows.TeardownInput) error {
	x.step("ACT teardown-start")
	return x.Activities.TeardownService(ctx, in)
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput() // #nosec G204 -- test helper, fixed docker subcommands
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func dockerQuiet(args ...string) string {
	out, _ := exec.Command("docker", args...).CombinedOutput() // #nosec G204 -- test helper
	return strings.TrimSpace(string(out))
}

func hasImage(img string) bool { return exec.Command("docker", "image", "inspect", img).Run() == nil } // #nosec G204

func labelled(svc string) []string {
	return strings.Fields(dockerQuiet("ps", "-a", "--format", "{{.Names}}", "--filter", "label=cp.serviceId="+svc))
}

func volumeExists(name string) bool {
	return exec.Command("docker", "volume", "inspect", name).Run() == nil
} // #nosec G204

type tdSvc struct {
	id, routeFile, legacy string
	names                 []string
}

func TestE2ETeardownRealDocker(t *testing.T) {
	c := e2eClient(t)
	if !hasImage("busybox:1.36") || !hasImage("nginx:alpine") {
		t.Skip("e2e: busybox:1.36 and nginx:alpine must be local")
	}
	ctx := context.Background()
	pool, cleanup, err := testutil.StartPostgres(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	st := store.New(pool)
	routes, legacy := t.TempDir(), t.TempDir()

	nginx := "e2etd-nginx-" + rid()
	docker(t, "run", "-d", "--name", nginx, "--label", "cp.e2e=teardown", "nginx:alpine")
	defer dockerQuiet("rm", "-f", nginx)

	r := runtime.ExecRunner{}
	acts := &workflows.Activities{
		Store: st, Docker: runtime.Docker{R: r, Nginx: nginx, ProbeImage: "busybox:1.36"},
		Router:   runtime.Nginx{R: r, RoutesDir: routes, Container: nginx, BaseDomain: "localhost", Port: "80"},
		Temporal: c, Log: slog.New(slog.DiscardHandler),
		Cfg: workflows.ActivityConfig{LegacyVolumeRoot: legacy, CancelPollInterval: 300 * time.Millisecond, CancelWaitTimeout: 60 * time.Second},
	}
	w := newWorld()
	tq := "e2e-td-" + rid()
	tw := worker.New(c, tq, worker.Options{Identity: "e2e-td"})
	tw.RegisterWorkflow(workflows.TeardownWorkflow)
	tw.RegisterActivity(&teardownWrap{acts, w})
	if err := tw.Start(); err != nil {
		t.Fatal(err)
	}
	defer tw.Stop()
	dtq := "e2e-td-deploy-" + rid()
	startWorker(t, c, dtq, w)

	mk := func(typ string, inflight bool) tdSvc {
		f := testutil.Seed(t, st, typ)
		s := tdSvc{id: f.ServiceID}
		st.UpsertEnvVar(ctx, s.id, "SECRET", "enc:v1:x")
		if typ == store.TypeRedis {
			st.UpsertEnvVar(ctx, s.id, store.RedisPasswordEnv, "enc:v1:pw")
		}
		peer := testutil.Seed(t, st, store.TypeNodejs)
		if _, err := st.CreateConnection(ctx, s.id, peer.ServiceID, nil); err != nil {
			t.Fatal(err)
		}
		d1, _ := st.CreateDeployment(ctx, s.id, "main")
		st.MarkDeployed(ctx, d1.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 8080})
		cn := workflows.ContainerName(d1.ID)
		docker(t, "run", "-d", "--name", cn, "--label", "cp.serviceId="+s.id, "--label", "cp.e2e=teardown", "busybox:1.36", "sleep", "3600")
		orphan := "e2etd-orphan-" + s.id
		docker(t, "run", "-d", "--name", orphan, "--label", "cp.serviceId="+s.id, "--label", "cp.e2e=teardown", "busybox:1.36", "sleep", "3600")
		s.names = []string{cn, orphan}
		docker(t, "volume", "create", "--label", "cp.e2e=teardown", workflows.VolumeName(s.id))
		s.routeFile = filepath.Join(routes, "svc-"+s.id+".conf")
		if err := os.WriteFile(s.routeFile, []byte(runtime.RenderRoute(s.id, "localhost", cn, 8080)), 0o600); err != nil {
			t.Fatal(err)
		}
		s.legacy = filepath.Join(legacy, s.id)
		if err := os.MkdirAll(s.legacy, 0o750); err != nil {
			t.Fatal(err)
		}
		if inflight {
			dq, _ := st.CreateDeployment(ctx, s.id, "main") // queued, never started
			_ = dq
			db, _ := st.CreateDeployment(ctx, s.id, "main")
			st.SetDeploymentStatus(ctx, db.ID, "building")
			w.addRow(db.ID, s.id, store.StatusQueued)
			h := w.addHook("RunContainer:"+db.ID, false)
			startDeploy(t, c, dtq, db.ID)
			waitEntered(t, h)
		}
		return s
	}
	keep := mk(store.TypeRedis, true)   // purgeData=false, with a running deploy
	purge := mk(store.TypeRedis, false) // purgeData=true
	live := mk(store.TypeNodejs, false) // never deleted
	t.Cleanup(func() {                  // exact names only
		for _, s := range []tdSvc{keep, purge, live} {
			for _, n := range append(labelled(s.id), s.names...) {
				dockerQuiet("rm", "-f", n)
			}
			dockerQuiet("volume", "rm", "-f", workflows.VolumeName(s.id))
		}
	})
	for _, s := range []tdSvc{keep, purge} {
		if err := st.SoftDeleteService(ctx, s.id); err != nil {
			t.Fatal(err)
		}
	}

	starter := workflows.Starter{Client: c, TaskQueue: tq}
	teardown := func(id string, purgeData bool) error {
		if err := starter.Start(ctx, store.EventTeardownRequested, []byte(fmt.Sprintf(`{"serviceId":%q,"purgeData":%v}`, id, purgeData))); err != nil {
			return err
		}
		return c.GetWorkflow(ctx, workflows.TeardownWorkflowID(id), "").Get(ctx, nil)
	}
	if err := teardown(keep.id, false); err != nil {
		t.Fatalf("teardown keep: %v", err)
	}
	if err := teardown(purge.id, true); err != nil {
		t.Fatalf("teardown purge: %v", err)
	}

	for _, s := range []tdSvc{keep, purge} {
		if got := labelled(s.id); len(got) != 0 {
			t.Errorf("%s: containers remain: %v", s.id, got)
		}
		if _, err := os.Stat(s.routeFile); !os.IsNotExist(err) {
			t.Errorf("%s: route file remains", s.id)
		}
		var open int
		pool.QueryRow(ctx, `SELECT count(*) FROM "Deployment" WHERE "serviceId"=$1 AND status IN ('queued','building','deploying')`, s.id).Scan(&open)
		var conns int
		pool.QueryRow(ctx, `SELECT count(*) FROM "ServiceConnection" WHERE "sourceId"=$1 OR "targetId"=$1`, s.id).Scan(&conns)
		any, _ := st.GetServiceAnyState(ctx, s.id)
		if open != 0 || conns != 0 || any.RepoURL != nil {
			t.Errorf("%s: open=%d conns=%d repoUrl=%v", s.id, open, conns, any.RepoURL)
		}
	}
	if !volumeExists(workflows.VolumeName(keep.id)) || volumeExists(workflows.VolumeName(purge.id)) {
		t.Error("volume: keep must stay, purge must go")
	}
	if _, err := os.Stat(keep.legacy); err != nil {
		t.Error("keep: legacy dir removed")
	}
	if _, err := os.Stat(purge.legacy); !os.IsNotExist(err) {
		t.Error("purge: legacy dir remains")
	}
	// Ruling 3: the kept database keeps only its generated password row.
	if m, _ := st.EnvMap(ctx, keep.id); len(m) != 1 || m[store.RedisPasswordEnv] != "enc:v1:pw" {
		t.Errorf("keep: env = %v", m)
	}
	if m, _ := st.EnvMap(ctx, purge.id); len(m) != 0 {
		t.Errorf("purge: env = %v", m)
	}
	var failed int
	pool.QueryRow(ctx, `SELECT count(*) FROM "Deployment" WHERE "serviceId"=$1 AND status='failed'`, keep.id).Scan(&failed)
	if failed != 2 {
		t.Errorf("keep: %d failed deployments, want 2 (queued + building)", failed)
	}

	// The deploy workflow cleaned up before the cancel activity ended, and the
	// teardown activity ran only after that.
	tr := w.trace()
	var depFailed = -1
	for i, s := range tr {
		if strings.HasPrefix(s, "failed:") {
			depFailed = i
		}
	}
	ce, ts := slices.Index(tr, "ACT cancel-end"), slices.Index(tr, "ACT teardown-start")
	if depFailed < 0 || depFailed >= ce || ce >= ts {
		t.Errorf("ordering wrong: %v", tr)
	}
	d, err := c.DescribeWorkflowExecution(ctx, workflows.TeardownWorkflowID(keep.id), "")
	if err != nil || d.GetWorkflowExecutionInfo().GetStatus().String() != "Completed" {
		t.Errorf("teardown workflow: %v %v", d.GetWorkflowExecutionInfo().GetStatus(), err)
	}

	// A live service is refused (permanent) and nothing of it is touched.
	err = teardown(live.id, true)
	var ae *temporal.ApplicationError
	if err == nil || !errors.As(err, &ae) || !ae.NonRetryable() {
		t.Errorf("live service: err = %v", err)
	}
	if len(labelled(live.id)) != 2 || !volumeExists(workflows.VolumeName(live.id)) {
		t.Error("live service containers or volume touched")
	}
	if _, err := os.Stat(live.routeFile); err != nil {
		t.Error("live route removed")
	}
	if m, _ := st.EnvMap(ctx, live.id); len(m) != 1 {
		t.Error("live env rows touched")
	}
}
