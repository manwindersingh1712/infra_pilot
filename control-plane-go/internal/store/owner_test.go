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

func TestListEnvVarsAfterPagesByID(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	for _, k := range []string{"A", "B", "C", "D", "E"} {
		if _, err := st.UpsertEnvVar(ctx, f.ServiceID, k, "v"); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	after := ""
	for {
		page, err := st.ListEnvVarsAfter(ctx, after, 2, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, v := range page {
			if v.ID <= after {
				t.Fatalf("id %s not after %s", v.ID, after)
			}
			seen = append(seen, v.Key)
			after = v.ID
		}
	}
	if len(seen) != 5 {
		t.Fatalf("saw %v", seen)
	}
}

// A value above maxValueBytes comes back flagged with an empty Value, so the
// caller never holds it in memory; one exactly at the limit comes back whole.
func TestListEnvVarsAfterCapsLargeValues(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	for k, v := range map[string]string{"AT": "12345", "OVER": "123456", "UTF": "ää", "UTF3": "äää"} { // UTF is 4 bytes, UTF3 is 6
		if _, err := st.UpsertEnvVar(ctx, f.ServiceID, k, v); err != nil {
			t.Fatal(err)
		}
	}
	page, err := st.ListEnvVarsAfter(ctx, "", 10, 5)
	if err != nil || len(page) != 4 {
		t.Fatalf("%v %v", page, err)
	}
	for _, v := range page {
		switch v.Key {
		case "AT":
			if v.TooLarge || v.Value != "12345" {
				t.Fatalf("AT: %+v", v)
			}
		case "UTF":
			if v.TooLarge || v.Value != "ää" {
				t.Fatalf("UTF: %+v", v)
			}
		case "UTF3": // 3 characters but 6 bytes: over a 5-byte cap
			if !v.TooLarge || v.Value != "" {
				t.Fatalf("UTF3: %+v", v)
			}
		case "OVER":
			if !v.TooLarge || v.Value != "" {
				t.Fatalf("OVER: %+v", v)
			}
		}
	}
}
