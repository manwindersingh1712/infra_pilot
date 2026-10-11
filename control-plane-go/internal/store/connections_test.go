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
