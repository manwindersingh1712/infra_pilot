package store_test

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestConnections(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	db, err := st.CreateService(ctx, store.NewService{ProjectID: f.ProjectID, Name: "db", ServiceType: "redis", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}

	label := "uses"
	c, err := st.CreateConnection(ctx, f.ServiceID, db.ID, &label)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConnection(ctx, f.ServiceID, db.ID, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate err = %v", err)
	}

	list, _ := st.ListProjectConnections(ctx, f.ProjectID)
	if len(list) != 1 || list[0].ID != c.ID || *list[0].Label != "uses" {
		t.Fatalf("list = %+v", list)
	}
	edges, _ := st.ListSourceEdges(ctx, f.ProjectID)
	if len(edges) != 1 || edges[0] != [2]string{f.ServiceID, db.ID} {
		t.Fatalf("edges = %v", edges)
	}

	if _, err := st.GetConnectionForOwner(ctx, c.ID, "intruder"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if _, err := st.GetConnectionForOwner(ctx, c.ID, f.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConnection(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListProjectConnections(ctx, f.ProjectID)
	if len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
}

// connFixture: owner A has project P1 (svc, db) and project P2 (other);
// tenant B has one service.
type connFixture struct {
	f         testutil.Fixture
	db, other store.Service
	p2        store.Project
	foreign   testutil.Fixture
}

func newConnFixture(t *testing.T, st *store.Store) connFixture {
	t.Helper()
	ctx := context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	mk := func(project, name string) store.Service {
		s, err := st.CreateService(ctx, store.NewService{ProjectID: project, Name: name, ServiceType: "redis", Branch: "main"})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	p2, err := st.CreateProject(ctx, "p2", f.UserID)
	if err != nil {
		t.Fatal(err)
	}
	g := testutil.Seed(t, st, store.TypeNodejs)
	return connFixture{f: f, db: mk(f.ProjectID, "db"), other: mk(p2.ID, "other"), p2: p2, foreign: g}
}

func TestConnectionListsRespectProjectAndDirection(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	c := newConnFixture(t, st)
	mustConn := func(src, dst string) store.ServiceConnection {
		t.Helper()
		conn, err := st.CreateConnection(ctx, src, dst, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	in := mustConn(c.other.ID, c.f.ServiceID)     // from P2 into P1: target side of the OR
	out := mustConn(c.f.ServiceID, c.other.ID)    // from P1 out to P2
	inside := mustConn(c.f.ServiceID, c.db.ID)    // within P1
	elsewhere := mustConn(c.other.ID, c.other.ID) // P2 only (self, raw store allows it)
	_ = elsewhere

	p1, err := st.ListProjectConnections(ctx, c.f.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, x := range p1 {
		got[x.ID] = true
	}
	if len(p1) != 3 || !got[in.ID] || !got[out.ID] || !got[inside.ID] {
		t.Fatalf("P1 list = %+v", p1)
	}
	p2, err := st.ListProjectConnections(ctx, c.p2.ID)
	if err != nil || len(p2) != 3 { // in, out and the P2 self edge all touch P2
		t.Fatalf("P2 list = %+v err %v", p2, err)
	}
	// A project with no services lists nothing, and a connection between two
	// OTHER projects' services is not in it.
	empty, _ := st.CreateProject(ctx, "empty", c.f.UserID)
	if l, err := st.ListProjectConnections(ctx, empty.ID); err != nil || len(l) != 0 {
		t.Fatalf("empty = %+v %v", l, err)
	}
	foreignDB, _ := st.CreateService(ctx, store.NewService{ProjectID: c.foreign.ProjectID, Name: "d", ServiceType: "redis", Branch: "main"})
	mustConn(c.foreign.ServiceID, foreignDB.ID)
	if l, _ := st.ListProjectConnections(ctx, c.f.ProjectID); len(l) != 3 {
		t.Fatalf("another tenant's edge leaked: %+v", l)
	}

	// Source edges: only edges whose SOURCE is in the project (no inbound edge).
	edges, err := st.ListSourceEdges(ctx, c.f.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]string]bool{{c.f.ServiceID, c.other.ID}: true, {c.f.ServiceID, c.db.ID}: true}
	if len(edges) != 2 || !want[edges[0]] || !want[edges[1]] {
		t.Fatalf("edges = %v (inbound edge from P2 must be excluded)", edges)
	}

	// A soft-deleted end removes the connection from both lists.
	if err := st.SoftDeleteService(ctx, c.db.ID); err != nil {
		t.Fatal(err)
	}
	p1, _ = st.ListProjectConnections(ctx, c.f.ProjectID)
	edges, _ = st.ListSourceEdges(ctx, c.f.ProjectID)
	if len(p1) != 2 || len(edges) != 1 {
		t.Fatalf("after soft delete: %+v %v", p1, edges)
	}
}

func TestCreateConnectionForOwner(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	c := newConnFixture(t, st)
	label := "x"
	conn, err := st.CreateConnectionForOwner(ctx, c.f.ServiceID, c.db.ID, c.f.UserID, &label)
	if err != nil || conn.SourceID != c.f.ServiceID || conn.TargetID != c.db.ID || *conn.Label != "x" {
		t.Fatalf("%+v %v", conn, err)
	}
	if _, err := st.CreateConnectionForOwner(ctx, c.f.ServiceID, c.db.ID, c.f.UserID, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := st.CreateConnectionForOwner(ctx, c.db.ID, c.f.ServiceID, c.f.UserID, nil); err != nil {
		t.Fatalf("reverse direction is a different pair: %v", err)
	}
	if err := st.SoftDeleteService(ctx, c.other.ID); err != nil {
		t.Fatal(err)
	}
	notFound := map[string][3]string{
		"foreign target": {c.f.ServiceID, c.foreign.ServiceID, c.f.UserID},
		"foreign source": {c.foreign.ServiceID, c.f.ServiceID, c.f.UserID},
		"foreign caller": {c.f.ServiceID, c.db.ID, c.foreign.UserID},
		"self":           {c.f.ServiceID, c.f.ServiceID, c.f.UserID},
		"deleted target": {c.f.ServiceID, c.other.ID, c.f.UserID},
		"missing":        {c.f.ServiceID, "nope", c.f.UserID},
		"unknown user":   {c.f.ServiceID, c.db.ID, "nope"},
	}
	for name, a := range notFound {
		if _, err := st.CreateConnectionForOwner(ctx, a[0], a[1], a[2], nil); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	// Same owner, different projects: refused.
	p3, _ := st.CreateService(ctx, store.NewService{ProjectID: c.p2.ID, Name: "p3", ServiceType: "redis", Branch: "main"})
	if _, err := st.CreateConnectionForOwner(ctx, c.f.ServiceID, p3.ID, c.f.UserID, nil); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-project err = %v", err)
	}
}

func TestGetConnectionForOwnerChecksBothEnds(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	c := newConnFixture(t, st)
	// Raw rows the API would never create: owner's source -> foreign target.
	mixed, _ := st.CreateConnection(ctx, c.f.ServiceID, c.foreign.ServiceID, nil)
	if _, err := st.GetConnectionForOwner(ctx, mixed.ID, c.f.UserID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("source-only owner err = %v", err)
	}
	if _, err := st.GetConnectionForOwner(ctx, mixed.ID, c.foreign.UserID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("target-only owner err = %v", err)
	}
	own, _ := st.CreateConnection(ctx, c.f.ServiceID, c.db.ID, nil)
	if _, err := st.GetConnectionForOwner(ctx, own.ID, c.f.UserID); err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteService(ctx, c.db.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetConnectionForOwner(ctx, own.ID, c.f.UserID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted end err = %v", err)
	}
}
