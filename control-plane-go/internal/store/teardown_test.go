package store_test

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestGetServiceAnyStateSeesDeletedAndLive(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeRedis)
	s, err := st.GetServiceAnyState(ctx, f.ServiceID)
	if err != nil || s.ID != f.ServiceID || s.DeletedAt != nil {
		t.Fatalf("live: %+v err=%v", s, err)
	}
	if err := st.SoftDeleteService(ctx, f.ServiceID); err != nil {
		t.Fatal(err)
	}
	s, err = st.GetServiceAnyState(ctx, f.ServiceID)
	if err != nil || s.DeletedAt == nil {
		t.Fatalf("deleted: %+v err=%v", s, err)
	}
	// The ordinary getters must stay strict.
	if _, err := st.GetService(ctx, f.ServiceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetService err = %v", err)
	}
	if _, err := st.GetServiceAnyState(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestFailOpenDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	other := testutil.Seed(t, st, store.TypeNodejs)
	mk := func(svc, status string) string {
		d, err := st.CreateDeployment(ctx, svc, "main")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetDeploymentStatus(ctx, d.ID, status); err != nil {
			t.Fatal(err)
		}
		return d.ID
	}
	q, b, dg, dep, fl := mk(f.ServiceID, "queued"), mk(f.ServiceID, "building"), mk(f.ServiceID, "deploying"),
		mk(f.ServiceID, "deployed"), mk(f.ServiceID, "failed")
	foreign := mk(other.ServiceID, "queued")

	ids, err := st.FailOpenDeployments(ctx, f.ServiceID)
	if err != nil || len(ids) != 3 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	status := func(id string) string {
		d, _ := st.GetDeployment(ctx, id)
		return d.Status
	}
	for _, id := range []string{q, b, dg, fl} {
		if status(id) != "failed" {
			t.Fatalf("%s not failed", id)
		}
	}
	if status(dep) != "deployed" || status(foreign) != "queued" {
		t.Fatalf("deployed=%s foreign=%s", status(dep), status(foreign))
	}
	if ids, err := st.FailOpenDeployments(ctx, f.ServiceID); err != nil || len(ids) != 0 {
		t.Fatalf("second run ids=%v err=%v", ids, err)
	}
}

func TestScrubDeletedService(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	keep := testutil.Seed(t, st, store.TypeNodejs)
	peer, _ := st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "p", ServiceType: "redis", Branch: "main"})
	if _, err := pool.Exec(ctx, `UPDATE "Service" SET "repoUrl" = 'https://user:tok@example.com/x.git' WHERE "id" = $1`, f.ServiceID); err != nil {
		t.Fatal(err)
	}
	st.UpsertEnvVar(ctx, f.ServiceID, "A", "enc:v1:x")
	st.UpsertEnvVar(ctx, keep.ServiceID, "A", "enc:v1:y")
	st.CreateConnection(ctx, f.ServiceID, peer.ID, nil)
	st.CreateConnection(ctx, peer.ID, f.ServiceID, nil)
	st.CreateConnection(ctx, keep.ServiceID, peer.ID, nil)

	// A live service is never scrubbed.
	if err := st.ScrubDeletedService(ctx, f.ServiceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("live service scrub err = %v", err)
	}
	if m, _ := st.EnvMap(ctx, f.ServiceID); len(m) != 1 {
		t.Fatal("live service lost env vars")
	}

	st.SoftDeleteService(ctx, f.ServiceID)
	for range 2 { // idempotent
		if err := st.ScrubDeletedService(ctx, f.ServiceID); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := st.GetServiceAnyState(ctx, f.ServiceID)
	if s.RepoURL != nil {
		t.Fatalf("repoUrl not scrubbed: %q", *s.RepoURL)
	}
	if m, _ := st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
		t.Fatalf("env left: %v", m)
	}
	if m, _ := st.EnvMap(ctx, keep.ServiceID); len(m) != 1 {
		t.Fatal("another service's env vars were deleted")
	}
	conns, _ := st.ListProjectConnections(ctx, f.ProjectID)
	if len(conns) != 0 {
		t.Fatalf("connections left: %+v", conns)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "ServiceConnection"`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("remaining connections = %d err=%v", n, err)
	}
}
