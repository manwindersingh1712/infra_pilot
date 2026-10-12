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
	if err := st.ScrubDeletedService(ctx, f.ServiceID, false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("live service scrub err = %v", err)
	}
	if m, _ := st.EnvMap(ctx, f.ServiceID); len(m) != 1 {
		t.Fatal("live service lost env vars")
	}

	st.SoftDeleteService(ctx, f.ServiceID)
	for range 2 { // idempotent
		if err := st.ScrubDeletedService(ctx, f.ServiceID, false); err != nil {
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

func TestListOpenDeploymentIDs(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d1, _ := st.CreateDeployment(ctx, f.ServiceID, "main")
	d2, _ := st.CreateDeployment(ctx, f.ServiceID, "main")
	st.SetDeploymentStatus(ctx, d2.ID, "deployed")
	ids, err := st.ListOpenDeploymentIDs(ctx, f.ServiceID)
	if err != nil || len(ids) != 1 || ids[0] != d1.ID {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}

// Ruling 3: with keepGeneratedSecret a managed database keeps ONLY its generated
// password row; everything else goes.
func TestScrubDeletedServiceKeepsOnlyTheGeneratedPassword(t *testing.T) {
	for _, c := range []struct{ typ, secret string }{
		{store.TypeRedis, store.RedisPasswordEnv}, {store.TypeMongoDB, store.MongoPasswordEnv},
	} {
		for _, keep := range []bool{true, false} {
			st, ctx := newStore(t), context.Background()
			f := testutil.Seed(t, st, c.typ)
			peer := testutil.Seed(t, st, store.TypeNodejs)
			st.UpsertEnvVar(ctx, f.ServiceID, c.secret, "enc:v1:secret")
			st.UpsertEnvVar(ctx, f.ServiceID, "OTHER", "enc:v1:o")
			st.UpsertEnvVar(ctx, f.ServiceID, "API_TOKEN", "enc:v1:t")
			st.UpsertEnvVar(ctx, peer.ServiceID, c.secret, "enc:v1:peer")
			st.CreateConnection(ctx, f.ServiceID, peer.ServiceID, nil)
			pool.Exec(ctx, `UPDATE "Service" SET "repoUrl" = 'https://u:t@example.com/x.git' WHERE "id" = $1`, f.ServiceID)
			st.SoftDeleteService(ctx, f.ServiceID)
			for range 2 { // idempotent
				if err := st.ScrubDeletedService(ctx, f.ServiceID, keep); err != nil {
					t.Fatal(err)
				}
			}
			m, _ := st.EnvMap(ctx, f.ServiceID)
			if keep && (len(m) != 1 || m[c.secret] != "enc:v1:secret") {
				t.Errorf("%s keep: env = %v, want only %s with its ciphertext", c.typ, m, c.secret)
			}
			if !keep && len(m) != 0 {
				t.Errorf("%s purge: env left %v", c.typ, m)
			}
			if pm, _ := st.EnvMap(ctx, peer.ServiceID); len(pm) != 1 {
				t.Errorf("%s: another service's env rows changed: %v", c.typ, pm)
			}
			if conns, _ := st.ListProjectConnections(ctx, f.ProjectID); len(conns) != 0 {
				t.Errorf("%s: connections left", c.typ)
			}
			if s, _ := st.GetServiceAnyState(ctx, f.ServiceID); s.RepoURL != nil {
				t.Errorf("%s: repoUrl left", c.typ)
			}
		}
	}
}

// Non-managed services ignore the flag, even when they use a reserved name.
func TestScrubDeletedServiceIgnoresKeepForOtherTypes(t *testing.T) {
	for _, typ := range []string{store.TypeNodejs, store.TypeDocker, store.TypeNextjs, store.TypeReact} {
		st, ctx := newStore(t), context.Background()
		f := testutil.Seed(t, st, typ)
		st.UpsertEnvVar(ctx, f.ServiceID, store.RedisPasswordEnv, "enc:v1:a")
		st.UpsertEnvVar(ctx, f.ServiceID, store.MongoPasswordEnv, "enc:v1:b")
		st.SoftDeleteService(ctx, f.ServiceID)
		if err := st.ScrubDeletedService(ctx, f.ServiceID, true); err != nil {
			t.Fatal(err)
		}
		if m, _ := st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
			t.Errorf("%s kept rows: %v", typ, m)
		}
	}
	// A redis service does not keep the Mongo name, nor a mongodb service the Redis name.
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeRedis)
	st.UpsertEnvVar(ctx, f.ServiceID, store.MongoPasswordEnv, "enc:v1:b")
	st.SoftDeleteService(ctx, f.ServiceID)
	st.ScrubDeletedService(ctx, f.ServiceID, true)
	if m, _ := st.EnvMap(ctx, f.ServiceID); len(m) != 0 {
		t.Fatalf("redis kept the mongo key: %v", m)
	}
}
