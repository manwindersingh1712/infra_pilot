package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestCreateAndListDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	first, err := st.CreateDeployment(ctx, f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != store.StatusQueued || first.CommitSha != "main" || first.Image != nil || first.StoppedAt != nil {
		t.Fatalf("got %+v", first)
	}
	if _, err := pool.Exec(ctx, `UPDATE "Deployment" SET "createdAt" = now() - interval '1 minute' WHERE "id" = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, _ := st.CreateDeployment(ctx, f.ServiceID, "abc1234")

	list, err := st.ListDeployments(ctx, f.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("want newest first, got %+v", list)
	}
	all, _ := st.ListDeployments(ctx, "")
	other, _ := st.ListDeployments(ctx, "missing")
	if len(all) != 2 || len(other) != 0 {
		t.Fatalf("all=%d other=%d", len(all), len(other))
	}
	ok, _ := st.DeploymentExists(ctx, first.ID)
	missing, _ := st.DeploymentExists(ctx, "nope")
	if !ok || missing {
		t.Fatal("DeploymentExists wrong")
	}
	b, _ := json.Marshal(first)
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, has := m["stoppedAt"]; !has || len(m) != 13 {
		t.Fatalf("JSON keys: %v", m)
	}
}

func TestClaimDeploymentHasExactlyOneWinner(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "main")

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := st.ClaimDeployment(ctx, d.ID, store.StatusQueued, store.StatusDeploying)
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d winners, want 1", wins.Load())
	}
	got, _ := st.GetDeployment(ctx, d.ID)
	if got.Status != store.StatusDeploying {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestGetJobJoinsServiceAndProject(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "abc1234")

	j, err := st.GetJob(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if j.DeploymentID != d.ID || j.ServiceID != f.ServiceID || j.ProjectID != f.ProjectID ||
		j.ServiceType != "nodejs" || j.Branch != "main" || j.CommitSha != "abc1234" ||
		j.RepoURL == nil || j.Image != nil || j.Status != store.StatusQueued {
		t.Fatalf("got %+v", j)
	}
	if _, err := st.GetJob(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildAndDeployLifecycle(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	old, _ := st.CreateDeployment(ctx, f.ServiceID, "v1")
	cur, _ := st.CreateDeployment(ctx, f.ServiceID, "v2")

	if err := st.MarkBuilt(ctx, cur.ID, "reg/svc:v2"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []store.Deployment{old, cur} {
		err := st.MarkDeployed(ctx, d.ID, store.DeployedInfo{
			ContainerID: "ctr-" + d.ID, ContainerPort: 3000, RuntimeURL: ptr("http://x.localhost"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetDeployment(ctx, cur.ID)
	if got.Status != store.StatusDeployed || *got.Image != "reg/svc:v2" || got.HostPort != nil ||
		*got.ContainerPort != 3000 || *got.RuntimeURL != "http://x.localhost" {
		t.Fatalf("got %+v", got)
	}

	live, _ := st.ListLiveDeployments(ctx, f.ServiceID, cur.ID)
	if len(live) != 1 || live[0].ID != old.ID {
		t.Fatalf("live = %+v", live)
	}
	running, _ := st.ListRunningDeployments(ctx)
	if len(running) != 2 {
		t.Fatalf("running = %+v", running)
	}

	if err := st.MarkStopped(ctx, []string{old.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStopped(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stopped, _ := st.GetDeployment(ctx, old.ID)
	if stopped.StoppedAt == nil {
		t.Fatal("stoppedAt not set")
	}
	live, _ = st.ListLiveDeployments(ctx, f.ServiceID, cur.ID)
	running, _ = st.ListRunningDeployments(ctx)
	if len(live) != 0 || len(running) != 1 || running[0].ID != cur.ID {
		t.Fatalf("after stop: live=%d running=%+v", len(live), running)
	}
}

func TestManagedDeploymentKeepsHostPortAndVolume(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeRedis)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "latest")
	err := st.MarkDeployed(ctx, d.ID, store.DeployedInfo{
		ContainerID: "c", HostPort: ptr(20001), ContainerPort: 6379, VolumePath: ptr("cp-vol-x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetDeployment(ctx, d.ID)
	if *got.HostPort != 20001 || *got.VolumePath != "cp-vol-x" || got.RuntimeURL != nil {
		t.Fatalf("got %+v", got)
	}
	ports, _ := st.UsedHostPorts(ctx)
	if !slices.Equal(ports, []int{20001}) {
		t.Fatalf("ports = %v", ports)
	}
	if err := st.SetDeploymentStatus(ctx, "missing", store.StatusFailed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestMarkFailedNeverOverwritesDeployed(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	live, _ := st.CreateDeployment(ctx, f.ServiceID, "v1")
	building, _ := st.CreateDeployment(ctx, f.ServiceID, "v2")
	if err := st.MarkDeployed(ctx, live.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.ClaimDeployment(ctx, building.ID, store.StatusQueued, store.StatusDeploying); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}

	changed, err := st.MarkFailed(ctx, live.ID)
	if err != nil || changed {
		t.Fatalf("deployed row: changed=%v err=%v", changed, err)
	}
	if d, _ := st.GetDeployment(ctx, live.ID); d.Status != store.StatusDeployed {
		t.Fatalf("deployed row became %s", d.Status)
	}
	changed, err = st.MarkFailed(ctx, building.ID)
	if err != nil || !changed {
		t.Fatalf("deploying row: changed=%v err=%v", changed, err)
	}
	if d, _ := st.GetDeployment(ctx, building.ID); d.Status != store.StatusFailed {
		t.Fatalf("deploying row is %s", d.Status)
	}
	// SEC-3 (T23r1): failing an already-failed row changes nothing.
	if changed, err := st.MarkFailed(ctx, building.ID); err != nil || changed {
		t.Fatalf("already-failed row: changed=%v err=%v", changed, err)
	}
	if changed, err := st.MarkFailed(ctx, "unknown"); err != nil || changed {
		t.Fatalf("unknown id: changed=%v err=%v", changed, err)
	}
}

func TestMarkBuiltAndMarkDeployedReportMissingRows(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if err := st.MarkBuilt(ctx, "missing", "img"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MarkBuilt err = %v", err)
	}
	err := st.MarkDeployed(ctx, "missing", store.DeployedInfo{ContainerID: "c", ContainerPort: 1})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("MarkDeployed err = %v", err)
	}
}

// STAFF-1 (T8): branches that had no assertion.
func TestDeploymentQueryFiltersAndNotFound(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	if _, err := st.GetDeployment(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetDeployment err = %v", err)
	}
	a, _ := st.CreateDeployment(ctx, f.ServiceID, "v1")
	if err := st.SetDeploymentStatus(ctx, a.ID, store.StatusBuilding); err != nil {
		t.Fatal(err)
	}
	if d, _ := st.GetDeployment(ctx, a.ID); d.Status != store.StatusBuilding {
		t.Fatalf("status = %s", d.Status)
	}

	// no containerId: not live, not running
	if live, _ := st.ListLiveDeployments(ctx, f.ServiceID, "none"); len(live) != 0 {
		t.Fatalf("live = %+v", live)
	}
	// a container that is not deployed is live but not running
	if _, err := pool.Exec(ctx, `UPDATE "Deployment" SET "containerId" = 'c', "hostPort" = 20005 WHERE "id" = $1`, a.ID); err != nil {
		t.Fatal(err)
	}
	live, _ := st.ListLiveDeployments(ctx, f.ServiceID, "none")
	running, _ := st.ListRunningDeployments(ctx)
	if len(live) != 1 || len(running) != 0 {
		t.Fatalf("live=%d running=%d", len(live), len(running))
	}

	// MarkStopped keeps the first stoppedAt, and a stopped row frees its host port
	if err := st.MarkStopped(ctx, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	first, _ := st.GetDeployment(ctx, a.ID)
	if err := st.MarkStopped(ctx, []string{a.ID}); err != nil {
		t.Fatal(err)
	}
	second, _ := st.GetDeployment(ctx, a.ID)
	if first.StoppedAt == nil || !first.StoppedAt.Equal(second.StoppedAt.Time) {
		t.Fatalf("stoppedAt changed: %v -> %v", first.StoppedAt, second.StoppedAt)
	}
	if ports, _ := st.UsedHostPorts(ctx); len(ports) != 0 {
		t.Fatalf("ports = %v", ports)
	}
}

func TestClaimDeployOneWinnerPerServiceThenBusyUntilItLeavesDeploying(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	var ids []string
	for range 6 {
		d, _ := st.CreateDeployment(ctx, f.ServiceID, "main")
		ids = append(ids, d.ID)
	}
	var mu sync.Mutex
	results := map[store.ClaimResult]int{}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := st.ClaimDeploy(ctx, id)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			results[r]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if results[store.ClaimWon] != 1 || results[store.ClaimBusy] != 5 {
		t.Fatalf("results = %v, want 1 won and 5 busy", results)
	}
	var winner string
	if err := pool.QueryRow(ctx, `SELECT "id" FROM "Deployment" WHERE "serviceId" = $1 AND "status" = 'deploying'`, f.ServiceID).Scan(&winner); err != nil {
		t.Fatal(err)
	}
	// Busy rows stay queued and win once the first leaves deploying.
	if _, err := st.MarkFailed(ctx, winner); err != nil {
		t.Fatal(err)
	}
	won := 0
	for _, id := range ids {
		if id == winner {
			continue
		}
		r, err := st.ClaimDeploy(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if r == store.ClaimWon {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d winners after the first left deploying, want 1", won)
	}
}

func TestClaimDeployDifferentServicesDoNotBlockEachOther(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a := testutil.Seed(t, st, store.TypeNodejs)
	b := testutil.Seed(t, st, store.TypeNodejs)
	da, _ := st.CreateDeployment(ctx, a.ServiceID, "main")
	db, _ := st.CreateDeployment(ctx, b.ServiceID, "main")
	for _, id := range []string{da.ID, db.ID} {
		if r, err := st.ClaimDeploy(ctx, id); err != nil || r != store.ClaimWon {
			t.Fatalf("claim %s = %v, %v", id, r, err)
		}
	}
}

func TestClaimDeployStaleDeployingRowDoesNotBlock(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	stale, _ := st.CreateDeployment(ctx, f.ServiceID, "v1")
	next, _ := st.CreateDeployment(ctx, f.ServiceID, "v2")
	if r, _ := st.ClaimDeploy(ctx, stale.ID); r != store.ClaimWon {
		t.Fatal("first claim should win")
	}
	if r, _ := st.ClaimDeploy(ctx, next.ID); r != store.ClaimBusy {
		t.Fatalf("fresh deploying row must block, got %v", r)
	}
	if _, err := pool.Exec(ctx, `UPDATE "Deployment" SET "updatedAt" = now() - interval '61 minutes' WHERE "id" = $1`, stale.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := st.ClaimDeploy(ctx, next.ID); err != nil || r != store.ClaimWon {
		t.Fatalf("a row older than 1 h must not block: %v, %v", r, err)
	}
}

func TestClaimDeployOwnDeployingRowWinsAgain(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "main")
	for i := range 2 {
		if r, err := st.ClaimDeploy(ctx, d.ID); err != nil || r != store.ClaimWon {
			t.Fatalf("claim %d = %v, %v", i, r, err)
		}
	}
}

func TestClaimDeployNonQueuedRowIsLost(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "main")
	if _, err := st.MarkFailed(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if r, err := st.ClaimDeploy(ctx, d.ID); err != nil || r != store.ClaimLost {
		t.Fatalf("failed row = %v, %v", r, err)
	}
	if r, err := st.ClaimDeploy(ctx, "missing"); err != nil || r != store.ClaimLost {
		t.Fatalf("missing row = %v, %v", r, err)
	}
}
