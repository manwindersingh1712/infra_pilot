package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
		page, err := st.ListEnvVarsAfter(ctx, after, 2, 1<<20, 1<<30)
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
	page, err := st.ListEnvVarsAfter(ctx, "", 10, 5, 1<<30)
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

// walkEnvVars pages the whole table with the keyset loop encrypt-env uses and
// returns the pages as lists of "key:size".
func walkEnvVars(t *testing.T, st *store.Store, limit, maxValue, maxBatch int) [][]string {
	t.Helper()
	var pages [][]string
	after := ""
	for {
		page, err := st.ListEnvVarsAfter(context.Background(), after, limit, maxValue, maxBatch)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return pages
		}
		var p []string
		for _, v := range page {
			if v.ID <= after {
				t.Fatalf("id %s not after %s (duplicate or out of order)", v.ID, after)
			}
			after = v.ID
			p = append(p, fmt.Sprintf("%s:%d", v.Key, len(v.Value)))
		}
		pages = append(pages, p)
	}
}

// A page stops filling once the summed value sizes would pass the byte budget;
// a row exactly at the budget still fits; the next page resumes at the next row
// (no row skipped or repeated).
func TestListEnvVarsAfterBoundsPagesByBytes(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	// Created in id order (cuid ids sort by creation time).
	sizes := []int{10, 10, 10, 30, 5, 5, 5, 5, 5}
	for i, n := range sizes {
		if _, err := st.UpsertEnvVar(ctx, f.ServiceID, fmt.Sprintf("K%d", i), strings.Repeat("x", n)); err != nil {
			t.Fatal(err)
		}
	}
	// Budget 20: [10 10] | [10] (next 30 would pass) | [30 alone, bigger than the budget] | [5 5 5 5] | [5]
	got := walkEnvVars(t, st, 50, 100, 20)
	want := [][]string{{"K0:10", "K1:10"}, {"K2:10"}, {"K3:30"}, {"K4:5", "K5:5", "K6:5", "K7:5"}, {"K8:5"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	// The row limit binds too, whichever comes first.
	got = walkEnvVars(t, st, 2, 100, 1<<30)
	if len(got) != 5 || len(got[0]) != 2 || len(got[4]) != 1 {
		t.Fatalf("row limit: %v", got)
	}
	// Every row exactly once, for several budgets.
	for _, budget := range []int{1, 9, 10, 11, 29, 30, 31, 1000} {
		n := 0
		for _, p := range walkEnvVars(t, st, 50, 100, budget) {
			n += len(p)
		}
		if n != len(sizes) {
			t.Fatalf("budget %d saw %d rows", budget, n)
		}
	}
}

// Values over the per-value cap are not read and weigh nothing, so they never
// stop a page from filling; and the first row of a page is always returned.
func TestListEnvVarsAfterByteBudgetIgnoresTooLarge(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	for i, n := range []int{8, 500, 8, 500, 8} {
		if _, err := st.UpsertEnvVar(ctx, f.ServiceID, fmt.Sprintf("K%d", i), strings.Repeat("x", n)); err != nil {
			t.Fatal(err)
		}
	}
	got := walkEnvVars(t, st, 50, 100, 16)
	want := [][]string{{"K0:8", "K1:0", "K2:8", "K3:0"}, {"K4:8"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
