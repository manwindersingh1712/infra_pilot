package api

import (
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"infrapilot/internal/auth"
	"infrapilot/internal/metrics"
)

func TestCORSAllowsListedOriginOnly(t *testing.T) {
	h := withCORS([]string{"http://localhost:5173"}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	pre := httptest.NewRequest(http.MethodOptions, "/projects", nil)
	pre.Header.Set("Origin", "http://localhost:5173")
	pre.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	hd := rec.Header()
	if rec.Code != http.StatusNoContent || hd.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		!strings.Contains(hd.Get("Access-Control-Allow-Methods"), "DELETE") ||
		hd.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type, X-Request-ID" {
		t.Fatalf("preflight code=%d headers=%v", rec.Code, hd)
	}

	evil := httptest.NewRequest(http.MethodGet, "/projects", nil)
	evil.Header.Set("Origin", "https://evil.test")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, evil)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin must get no CORS headers")
	}
}

func TestRequestIDIsGeneratedOrKept(t *testing.T) {
	var seen string
	h := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = requestID(r) }))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if len(seen) != 16 || rec.Header().Get("X-Request-ID") != seen {
		t.Fatalf("generated id %q header %q", seen, rec.Header().Get("X-Request-ID"))
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-ID", "abc-123")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != "abc-123" {
		t.Fatalf("kept id %q", seen)
	}

	bad := httptest.NewRequest("GET", "/", nil)
	bad.Header.Set("X-Request-ID", "evil\nInjected: 1")
	h.ServeHTTP(httptest.NewRecorder(), bad)
	if strings.Contains(seen, "\n") || len(seen) != 16 {
		t.Fatalf("unsafe id kept: %q", seen)
	}
}

func TestRequireAuth(t *testing.T) {
	signer, err := auth.NewSigner("k")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Deps: Deps{Signer: signer, Log: slog.New(slog.DiscardHandler)}}
	tok, err := s.Signer.Sign("u1")
	if err != nil {
		t.Fatal(err)
	}
	var got string
	next := func(w http.ResponseWriter, r *http.Request) { got = userID(r) }

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	s.private(next).ServeHTTP(rec, req)
	if rec.Code != 200 || got != "u1" {
		t.Fatalf("bearer: code=%d user=%q", rec.Code, got)
	}

	rec = httptest.NewRecorder()
	s.private(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?token="+tok, nil))
	expectStatus(t, rec, 401)
	expectBody(t, rec, `{"error":"unauthorized"}`)
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	s := &Server{Deps: Deps{Log: slog.New(slog.DiscardHandler)}}
	h := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	expectStatus(t, rec, 500)
	expectBody(t, rec, `{"error":"internal_error"}`)
}

func TestRateLimitPerIP(t *testing.T) {
	l := newIPLimiter(5)
	h := l.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	call := func(ip string) int {
		req := httptest.NewRequest("POST", "/auth/login", nil)
		req.RemoteAddr = ip + ":5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := range 5 {
		if c := call("10.0.0.1"); c != 200 {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if c := call("10.0.0.1"); c != http.StatusTooManyRequests {
		t.Fatalf("6th request: %d", c)
	}
	if c := call("10.0.0.2"); c != 200 {
		t.Fatal("other IPs must not be limited")
	}
}

func TestHealthReadyAndMetrics(t *testing.T) {
	hs := newHarness(t)
	rec := hs.do("GET", "/healthz", "", nil)
	expectStatus(t, rec, 200)
	expectBody(t, rec, `{"ok":true}`)
	expectStatus(t, hs.do("GET", "/readyz", "", nil), 200)

	hs.redis.err = errors.New("down")
	rec = hs.do("GET", "/readyz", "", nil)
	expectStatus(t, rec, 503)
	expectBody(t, rec, `{"ok":false}`)

	mrec := httptest.NewRecorder()
	hs.metrics.Handler().ServeHTTP(mrec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), `cp_http_requests_total{method="GET",route="GET /healthz",status="200"} 1`) {
		t.Fatalf("metrics:\n%s", mrec.Body.String())
	}
}

func TestRequireAuthFixedBodyForEveryFailure(t *testing.T) {
	signer, err := auth.NewSigner("k")
	if err != nil {
		t.Fatal(err)
	}
	other, err := auth.NewSigner("different")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := other.Sign("u1")
	if err != nil {
		t.Fatal(err)
	}
	valid, err := signer.Sign("u1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Deps: Deps{Signer: signer, Log: slog.New(slog.DiscardHandler)}}
	called := false
	next := func(http.ResponseWriter, *http.Request) { called = true }
	for name, header := range map[string]string{
		"missing":        "",
		"not bearer":     "Basic abc",
		"garbage":        "Bearer not-a-jwt",
		"wrong key":      "Bearer " + foreign,
		"alg none":       "Bearer eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.e30.",
		"empty bearer":   "Bearer ",
		"lowercase type": "bearer " + valid,
		"bare token":     valid,
		"Token scheme":   "Token " + valid,
		"double space":   "Bearer  " + valid,
	} {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		s.private(next).ServeHTTP(rec, req)
		if rec.Code != 401 || rec.Body.String() != `{"error":"unauthorized"}`+"\n" {
			t.Errorf("%s: code=%d body=%q", name, rec.Code, rec.Body.String())
		}
	}
	if called {
		t.Fatal("handler ran for an unauthenticated request")
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
}

func TestCORSRejectsNearMissOrigins(t *testing.T) {
	h := withCORS([]string{"http://localhost:5173"}, okHandler())
	for _, origin := range []string{
		"http://localhost:5173.evil.test", "http://localhost:51730", "http://localhost", "https://localhost:5173",
		"http://localhost:5173/", "HTTP://LOCALHOST:5173", "null", "http://evil.test/http://localhost:5173",
	} {
		for _, preflight := range []bool{false, true} {
			method := http.MethodGet
			if preflight {
				method = http.MethodOptions
			}
			req := httptest.NewRequest(method, "/projects", nil)
			req.Header.Set("Origin", origin)
			if preflight {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			for k := range rec.Header() {
				if strings.HasPrefix(k, "Access-Control-") {
					t.Errorf("origin %q preflight=%v got %s=%q", origin, preflight, k, rec.Header().Get(k))
				}
			}
			if preflight && rec.Code != http.StatusNoContent {
				t.Errorf("origin %q preflight code %d", origin, rec.Code)
			}
		}
	}
}

func TestCORSHeadersOnListedOrigin(t *testing.T) {
	h := withCORS([]string{"http://localhost:5173"}, okHandler())
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	hd := rec.Header()
	if hd.Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("ACAO %q", hd.Get("Access-Control-Allow-Origin"))
	}
	if hd.Get("Access-Control-Expose-Headers") != "X-Request-ID" {
		t.Fatalf("Expose-Headers %q", hd.Get("Access-Control-Expose-Headers"))
	}
	if hd.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials must not be allowed")
	}
	if !strings.Contains(strings.Join(hd.Values("Vary"), ","), "Origin") {
		t.Fatalf("Vary %v", hd.Values("Vary"))
	}
	// Vary: Origin is also set for unlisted and absent origins (cache safety).
	for _, origin := range []string{"", "https://evil.test"} {
		req := httptest.NewRequest(http.MethodGet, "/projects", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "Origin") {
			t.Errorf("origin %q: Vary %v", origin, rec.Header().Values("Vary"))
		}
	}
}

func TestUnmatchedRoutesUseFixedMetricsLabel(t *testing.T) {
	m := metrics.NewAPI()
	h := NewHandler(Deps{Metrics: m, Log: slog.New(slog.DiscardHandler), CORSOrigins: []string{"http://localhost:5173"}})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/nope/123"}, {"GET", "/nope/456"}, {"POST", "/healthz"}, {"PROPFIND", "/healthz"},
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(tc.method, tc.path, nil))
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `cp_http_requests_total{method="GET",route="unmatched",status="404"} 2`) {
		t.Errorf("404s not aggregated under unmatched:\n%s", body)
	}
	if !strings.Contains(body, `route="unmatched",status="405"`) {
		t.Errorf("405 not under unmatched:\n%s", body)
	}
	for _, leaked := range []string{"/nope", "PROPFIND"} {
		if strings.Contains(body, leaked) {
			t.Errorf("raw %q leaked into metrics", leaked)
		}
	}
}

func TestRateLimitIgnoresForwardedFor(t *testing.T) {
	h := newIPLimiter(2).wrap(okHandler())
	call := func(peer, xff string) int {
		req := httptest.NewRequest("POST", "/auth/login", nil)
		req.RemoteAddr = peer + ":5555"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
			req.Header.Set("X-Real-IP", xff)
			req.Header.Set("Forwarded", "for="+xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	call("10.0.0.1", "")
	call("10.0.0.1", "")
	// Same peer with a fresh spoofed address in every header must still be limited.
	if c := call("10.0.0.1", "203.0.113.9"); c != http.StatusTooManyRequests {
		t.Fatalf("spoofed X-Forwarded-For got %d, want 429", c)
	}
	// A different peer claiming the limited address is not limited by it.
	if c := call("10.0.0.2", "10.0.0.1"); c != 200 {
		t.Fatalf("other peer claiming the limited IP got %d, want 200", c)
	}
}

func TestRateLimitedResponseBodyAndRetryAfter(t *testing.T) {
	h := newIPLimiter(1).wrap(okHandler())
	for range 2 {
		req := httptest.NewRequest("POST", "/auth/login", nil)
		req.RemoteAddr = "10.0.0.1:1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == 200 {
			continue
		}
		expectStatus(t, rec, http.StatusTooManyRequests)
		expectBody(t, rec, `{"error":"too_many_requests"}`)
		if rec.Header().Get("Retry-After") != "60" {
			t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
		}
		if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("Content-Type %q", rec.Header().Get("Content-Type"))
		}
		return
	}
	t.Fatal("never rate limited")
}

func TestRecoverRepanicsErrAbortHandler(t *testing.T) {
	s := &Server{Deps: Deps{Log: slog.New(slog.DiscardHandler)}}
	h := s.withRecover(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }))
	rec := httptest.NewRecorder()
	var got any
	func() {
		defer func() { got = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	if got != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("no body may be written for an aborted handler, got %q", rec.Body.String())
	}
}
