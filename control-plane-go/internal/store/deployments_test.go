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
