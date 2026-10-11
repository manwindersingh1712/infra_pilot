package store_test

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestSoftDeletedServicesDisappear(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	other, _ := st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	st.CreateConnection(ctx, f.ServiceID, other.ID, nil)

	if err := st.SoftDeleteService(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteService(ctx, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := st.GetService(ctx, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetService err = %v", err)
	}
	if _, err := st.GetServiceForOwner(ctx, other.ID, f.UserID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetServiceForOwner err = %v", err)
	}
	if err := st.UpdateServicePosition(ctx, other.ID, 1, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("UpdateServicePosition err = %v", err)
	}
	cs, _ := st.ListCanvasServices(ctx, f.ProjectID)
	conns, _ := st.ListProjectConnections(ctx, f.ProjectID)
	edges, _ := st.ListSourceEdges(ctx, f.ProjectID)
	mine, _ := st.ListServicesForOwner(ctx, f.UserID, "")
	if len(cs) != 1 || len(conns) != 0 || len(edges) != 0 || len(mine) != 1 {
		t.Fatalf("canvas=%d conns=%d edges=%d mine=%d", len(cs), len(conns), len(edges), len(mine))
	}
}

func TestOwnerScopedServicesAndDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a := testutil.Seed(t, st, store.TypeNodejs)
	b := testutil.Seed(t, st, store.TypeNodejs)
	da, _ := st.CreateDeployment(ctx, a.ServiceID, "main")
	db, _ := st.CreateDeployment(ctx, b.ServiceID, "main")

	if s, _ := st.ListServicesForOwner(ctx, a.UserID, ""); len(s) != 1 || s[0].ID != a.ServiceID {
		t.Fatalf("services %+v", s)
	}
	if s, _ := st.ListServicesForOwner(ctx, a.UserID, b.ProjectID); len(s) != 0 {
		t.Fatalf("foreign project leaked: %+v", s)
	}
	if _, err := st.GetDeploymentForOwner(ctx, db.ID, a.UserID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign deployment err = %v", err)
	}
	if d, err := st.GetDeploymentForOwner(ctx, da.ID, a.UserID); err != nil || d.ID != da.ID {
		t.Fatalf("own deployment %+v %v", d, err)
	}
	if l, _ := st.ListDeploymentsForOwner(ctx, a.UserID, ""); len(l) != 1 || l[0].ID != da.ID {
		t.Fatalf("list %+v", l)
	}
	if l, _ := st.ListDeploymentsForOwner(ctx, a.UserID, b.ServiceID); len(l) != 0 {
		t.Fatalf("foreign service leaked %+v", l)
	}
}

func TestUnencryptedEnvVars(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	st.UpsertEnvVar(ctx, f.ServiceID, "PLAIN", "x")
	st.UpsertEnvVar(ctx, f.ServiceID, "DONE", "enc:v1:abc")

	plain, err := st.ListUnencryptedEnvVars(ctx)
	if err != nil || len(plain) != 1 || plain[0].Key != "PLAIN" {
		t.Fatalf("plain %+v err %v", plain, err)
	}
	if err := st.SetEnvVarValue(ctx, plain[0].ID, "enc:v1:def"); err != nil {
		t.Fatal(err)
	}
	if plain, _ := st.ListUnencryptedEnvVars(ctx); len(plain) != 0 {
		t.Fatalf("still plain: %+v", plain)
	}
}
