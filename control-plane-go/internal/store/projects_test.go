package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestListProjectsEmptyIsJSONArray(t *testing.T) {
	st := newStore(t)
	ps, err := st.ListProjects(context.Background(), "nobody")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ps)
	if string(b) != "[]" {
		t.Fatalf("got %s", b)
	}
}

func TestProjectsAreScopedToOwner(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	other, _ := st.CreateUser(ctx, "other@x.y", "h", nil)

	if _, err := st.GetProjectForOwner(ctx, f.ProjectID, f.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProjectForOwner(ctx, f.ProjectID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	ps, _ := st.ListProjects(ctx, f.UserID)
	if len(ps) != 1 || ps[0].Name != "proj" {
		t.Fatalf("got %+v", ps)
	}
}

// STAFF-1 (T6): timestamps read from the DB are non-zero UTC, and the User
// JSON never carries the password hash.
func TestScannedTimestampsAreUTCAndUserJSONHasNoPassword(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	p, err := st.GetProjectForOwner(ctx, f.ProjectID, f.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if p.CreatedAt.IsZero() || p.CreatedAt.Location() != time.UTC {
		t.Fatalf("createdAt = %v (loc %v), want non-zero UTC", p.CreatedAt.Time, p.CreatedAt.Location())
	}

	u, err := st.CreateUser(ctx, "json@x.y", "secret-hash", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["password"]; ok || strings.Contains(string(b), "secret-hash") {
		t.Fatalf("user JSON leaks the password: %s", b)
	}
	if c, _ := m["createdAt"].(string); !strings.HasSuffix(c, "Z") {
		t.Fatalf("createdAt = %q, want a Z suffix", c)
	}
}

func TestServicesCRUD(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	svc, err := st.GetService(ctx, f.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if svc.ServiceType != "nodejs" || svc.Branch != "main" || svc.RepoURL == nil {
		t.Fatalf("got %+v", svc)
	}

	all, _ := st.ListServices(ctx, "")
	scoped, _ := st.ListServices(ctx, f.ProjectID)
	none, _ := st.ListServices(ctx, "missing")
	if len(all) != 1 || len(scoped) != 1 || len(none) != 0 {
		t.Fatalf("all=%d scoped=%d none=%d", len(all), len(scoped), len(none))
	}

	if err := st.UpdateServicePosition(ctx, f.ServiceID, 10.5, 20); err != nil {
		t.Fatal(err)
	}
	svc, _ = st.GetServiceForOwner(ctx, f.ServiceID, f.UserID)
	if svc.PositionX != 10.5 || svc.PositionY != 20 {
		t.Fatalf("position not saved: %+v", svc)
	}
	if _, err := st.GetServiceForOwner(ctx, f.ServiceID, "someone-else"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := st.UpdateServicePosition(ctx, "missing", 1, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanvasServicesCountDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	for range 2 {
		if _, err := st.CreateDeployment(ctx, f.ServiceID, "main"); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := st.ListCanvasServices(ctx, f.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].DeploymentCount != 2 || cs[0].Name != "svc" {
		t.Fatalf("got %+v", cs)
	}
}

func TestEnvVars(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	if _, err := st.UpsertEnvVar(ctx, f.ServiceID, "B", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertEnvVar(ctx, f.ServiceID, "A", "x"); err != nil {
		t.Fatal(err)
	}
	ev, err := st.UpsertEnvVar(ctx, f.ServiceID, "B", "2")
	if err != nil || ev.Value != "2" {
		t.Fatalf("upsert overwrite: %+v %v", ev, err)
	}

	list, _ := st.ListEnvVars(ctx, f.ServiceID)
	if len(list) != 2 || list[0].Key != "A" || list[1].Value != "2" {
		t.Fatalf("got %+v", list)
	}
	m, _ := st.EnvMap(ctx, f.ServiceID)
	if m["A"] != "x" || m["B"] != "2" {
		t.Fatalf("EnvMap = %v", m)
	}
	if err := st.DeleteEnvVar(ctx, f.ServiceID, "A"); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListEnvVars(ctx, f.ServiceID)
	if len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}
}

func TestInsertEnvVarIfAbsentNeverReplaces(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	ok, err := st.InsertEnvVarIfAbsent(ctx, f.ServiceID, "K", "first")
	if err != nil || !ok {
		t.Fatalf("first insert: ok=%v err=%v", ok, err)
	}
	ok, err = st.InsertEnvVarIfAbsent(ctx, f.ServiceID, "K", "second")
	if err != nil || ok {
		t.Fatalf("second insert: ok=%v err=%v", ok, err)
	}
	m, _ := st.EnvMap(ctx, f.ServiceID)
	if m["K"] != "first" {
		t.Fatalf("EnvMap = %v", m)
	}
}
