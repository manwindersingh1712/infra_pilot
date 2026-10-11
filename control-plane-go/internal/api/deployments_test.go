package api

import (
	"context"
	"net/url"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestListDeploymentsIsOwnerScoped(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	other := testutil.Seed(t, hs.st, store.TypeNodejs)
	tok := hs.token(f.UserID)
	expectBody(t, hs.do("GET", "/deployments", tok, nil), `[]`)

	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	hs.st.CreateDeployment(context.Background(), other.ServiceID, "main")

	list := decode[[]map[string]any](t, hs.do("GET", "/deployments?serviceId="+f.ServiceID, tok, nil))
	if len(list) != 1 || list[0]["id"] != dep.ID || list[0]["status"] != "queued" || list[0]["stoppedAt"] != nil || len(list[0]) != 13 {
		t.Fatalf("list %v", list)
	}
	if all := decode[[]map[string]any](t, hs.do("GET", "/deployments", tok, nil)); len(all) != 1 {
		t.Fatalf("other users' deployments leaked: %v", all)
	}
	if foreign := decode[[]map[string]any](t, hs.do("GET", "/deployments?serviceId="+other.ServiceID, tok, nil)); len(foreign) != 0 {
		t.Fatalf("foreign %v", foreign)
	}
}

// A malformed serviceId (NUL, invalid UTF-8, too long) must answer exactly like
// a missing or foreign one, never a Postgres 500.
func TestListDeploymentsMalformedServiceIDAnswersLikeMissing(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	tok := hs.token(f.UserID)
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	for _, bad := range []string{"a\x00b", "\xff\xfe", "a b", "../x", string(long), "nosuchservice"} {
		rec := hs.do("GET", "/deployments?serviceId="+url.QueryEscape(bad), tok, nil)
		expectStatus(t, rec, 200)
		expectBody(t, rec, `[]`)
	}
}

func TestListDeploymentsHidesSoftDeletedServices(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	tok := hs.token(f.UserID)
	expectStatus(t, hs.do("DELETE", "/services/"+f.ServiceID, tok, nil), 202)
	expectBody(t, hs.do("GET", "/deployments", tok, nil), `[]`)
	expectBody(t, hs.do("GET", "/deployments?serviceId="+f.ServiceID, tok, nil), `[]`)
}

func TestListDeploymentsRequiresAuth(t *testing.T) {
	hs := newHarness(t)
	expectStatus(t, hs.do("GET", "/deployments", "", nil), 401)
}

// The UI asks for one service's deployments per panel: a user with two
// services must get only the requested service's rows back (STAFF-1).
func TestListDeploymentsServiceFilterWithinOneOwner(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	second, err := hs.st.CreateService(ctx, store.NewService{
		ProjectID: f.ProjectID, Name: "second", ServiceType: "redis", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	d1, err := hs.st.CreateDeployment(ctx, f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := hs.st.CreateDeployment(ctx, second.ID, "latest")
	if err != nil {
		t.Fatal(err)
	}
	tok := hs.token(f.UserID)

	all := decode[[]map[string]any](t, hs.do("GET", "/deployments", tok, nil))
	if len(all) != 2 {
		t.Fatalf("unfiltered list = %d rows, want 2", len(all))
	}
	for _, c := range []struct{ serviceID, depID string }{{f.ServiceID, d1.ID}, {second.ID, d2.ID}} {
		got := decode[[]map[string]any](t, hs.do("GET", "/deployments?serviceId="+c.serviceID, tok, nil))
		if len(got) != 1 || got[0]["id"] != c.depID || got[0]["serviceId"] != c.serviceID {
			t.Fatalf("filter %s returned %v, want only %s", c.serviceID, got, c.depID)
		}
	}
}

// At most the 50 newest deployments come back, newest first (STAFF-2).
func TestListDeploymentsNewestFirstAndCappedAt50(t *testing.T) {
	hs := newHarness(t)
	ctx := context.Background()
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	var ids []string
	for i := 0; i < 52; i++ {
		d, err := hs.st.CreateDeployment(ctx, f.ServiceID, "main")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	got := decode[[]map[string]any](t, hs.do("GET", "/deployments?serviceId="+f.ServiceID, hs.token(f.UserID), nil))
	if len(got) != 50 {
		t.Fatalf("got %d rows, want the 50 newest", len(got))
	}
	for i, row := range got {
		want := ids[len(ids)-1-i]
		if row["id"] != want {
			t.Fatalf("row %d is %v, want %s (newest first); the two oldest must be dropped", i, row["id"], want)
		}
	}
}
