package api

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// recordingRouter registers on a real mux and remembers every pattern.
type recordingRouter struct {
	mux      *http.ServeMux
	patterns []string
}

func (r *recordingRouter) Handle(p string, h http.Handler) {
	r.patterns = append(r.patterns, p)
	r.mux.Handle(p, h)
}

func (r *recordingRouter) HandleFunc(p string, h func(http.ResponseWriter, *http.Request)) {
	r.Handle(p, http.HandlerFunc(h))
}

// publicRoutes is the explicit list of routes that may answer without a Bearer
// token. Adding a route here is a security decision: a new route that is not
// wrapped in s.private and not on this list fails TestEveryRouteIsPrivateOrListedPublic.
var publicRoutes = []string{
	"GET /healthz",
	"GET /readyz",
	"POST /auth/register",
	"POST /auth/login",
	"POST /auth/refresh",
	"POST /auth/logout",
	"POST /dev/login",                   // only registered with DevLogin
	"GET /logs/deployments/{id}/stream", // single-use ticket instead of a Bearer token
}

func TestEveryRouteIsPrivateOrListedPublic(t *testing.T) {
	hs := newHarness(t) // DevLogin is on, so /dev/login is registered
	rr := &recordingRouter{mux: http.NewServeMux()}
	hs.srv.routes(rr)
	if len(rr.patterns) < 20 {
		t.Fatalf("only %d patterns recorded: %v", len(rr.patterns), rr.patterns)
	}
	for _, p := range publicRoutes {
		if !slices.Contains(rr.patterns, p) {
			t.Errorf("public route %q is listed but not registered: update publicRoutes", p)
		}
	}
	for _, pattern := range rr.patterns {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("pattern %q has no method", pattern)
		}
		path = strings.NewReplacer("{id}", "someid", "{key}", "SOME_KEY").Replace(path)
		if strings.Contains(path, "{") {
			t.Fatalf("pattern %q has a wildcard this test does not fill in", pattern)
		}
		// No Authorization header at all. Host is loopback so the dev-login
		// Host check, if any, does not fire first.
		req := httptest.NewRequest(method, path, http.NoBody)
		req.Host = "localhost"
		req.RemoteAddr = "192.0.2.77:1"
		rec := httptest.NewRecorder()
		hs.h.ServeHTTP(rec, req)
		unauthorized := rec.Code == http.StatusUnauthorized && strings.Contains(rec.Body.String(), `"unauthorized"`)
		if slices.Contains(publicRoutes, pattern) {
			if unauthorized {
				t.Errorf("%s is listed public but answers unauthorized", pattern)
			}
			continue
		}
		if !unauthorized {
			t.Errorf("%s is not on the public list and answered %d %q without a token: wrap it in s.private or list it", pattern, rec.Code, rec.Body.String())
		}
	}
}
