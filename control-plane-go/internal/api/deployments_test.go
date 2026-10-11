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
