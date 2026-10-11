package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestStreamTicketEndpoint(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	gone := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	depGone, _ := hs.st.CreateDeployment(context.Background(), gone.ServiceID, "main")
	if err := hs.st.SoftDeleteService(context.Background(), gone.ServiceID); err != nil {
		t.Fatal(err)
	}
	tok := hs.token(f.UserID)

	rec := hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", tok, nil)
	expectStatus(t, rec, 200)
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control %q", cc)
	}
	body := decode[map[string]string](t, rec)
	if len(body) != 1 || body["ticket"] == "" {
		t.Fatalf("body %v", body)
	}
	if got := hs.tickets.m[body["ticket"]]; got != f.UserID+":"+dep.ID {
		t.Fatalf("ticket bound to %q, want %q", got, f.UserID+":"+dep.ID)
	}

	// The Bearer token is required, and a JWT in the query string does not count.
	expectStatus(t, hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", "", nil), 401)
	expectStatus(t, hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket?token="+tok, "", nil), 401)

	// Foreign, missing, malformed and soft-deleted deployments: identical 404.
	for name, c := range map[string]struct{ id, tok string }{
		"foreign":      {dep.ID, hs.token("intruder")},
		"missing":      {"nosuchdeployment", tok},
		"malformed":    {"a%00b", tok},
		"invalid utf8": {"%ff%fe", tok},
		"too long":     {strings.Repeat("a", 65), tok},
		"soft-deleted": {depGone.ID, hs.token(gone.UserID)},
	} {
		r := hs.do("POST", "/logs/deployments/"+c.id+"/stream-ticket", c.tok, nil)
		if r.Code != 404 || r.Body.String() != `{"error":"deployment not found"}`+"\n" {
			t.Errorf("%s: %d %q", name, r.Code, r.Body.String())
		}
	}
	if len(hs.tickets.m) != 1 {
		t.Fatalf("a rejected request issued a ticket: %v", hs.tickets.m)
	}
}

func TestStreamTicketIssueFailureIs500(t *testing.T) {
	hs := newHarness(t)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	hs.srv.Tickets = failingIssuer{hs.tickets}
	rec := hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", hs.token(f.UserID), nil)
	expectStatus(t, rec, 500)
	expectBody(t, rec, `{"error":"internal_error"}`)
}

type failingIssuer struct{ *fakeTickets }

func (failingIssuer) Issue(context.Context, string) (string, error) {
	return "", errors.New("redis down")
}

// The ticket route has its own per-IP bucket (stream class), separate from the
// auth buckets, so a log viewer cannot lock a user out of login or refresh.
func TestStreamTicketRouteIsRateLimited(t *testing.T) {
	hs := newHarnessWith(t, func(d *Deps) { d.Stream.Burst = 2 })
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, _ := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	tok := hs.token(f.UserID)
	for range 2 {
		expectStatus(t, hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", tok, nil), 200)
	}
	rec := hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", tok, nil)
	expectStatus(t, rec, 429)
	expectBody(t, rec, `{"error":"too_many_requests"}`)
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	// The auth buckets are untouched (400 = reached the handler).
	expectStatus(t, hs.do("POST", "/auth/refresh", "", nil), 400)
}
