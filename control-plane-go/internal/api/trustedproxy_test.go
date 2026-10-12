package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"infrapilot/internal/auth"
)

func prefixes(t *testing.T, s ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, v := range s {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestRequestClientKey(t *testing.T) {
	trusted := prefixes(t, "172.18.0.0/16", "10.0.0.5/32")
	cases := []struct {
		name    string
		trusted []netip.Prefix
		peer    string
		xff     []string
		want    string
	}{
		{"untrusted peer: spoofed XFF ignored", trusted, "203.0.113.9:1234", []string{"1.2.3.4"}, "203.0.113.9"},
		{"empty list keeps the peer", nil, "172.18.0.2:1234", []string{"1.2.3.4"}, "172.18.0.2"},
		{"trusted peer, single entry", trusted, "172.18.0.2:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		{"trusted peer, forged leftmost entry", trusted, "172.18.0.2:1234", []string{"6.6.6.6, 198.51.100.7"}, "198.51.100.7"},
		{"right-most untrusted skips trusted hops", trusted, "172.18.0.2:1234", []string{"6.6.6.6, 198.51.100.7, 10.0.0.5, 172.18.0.9"}, "198.51.100.7"},
		{"several header lines are one list", trusted, "172.18.0.2:1234", []string{"6.6.6.6", "198.51.100.7"}, "198.51.100.7"},
		{"all entries trusted falls back to the peer", trusted, "172.18.0.2:1234", []string{"10.0.0.5, 172.18.0.9"}, "172.18.0.2"},
		{"header absent falls back to the peer", trusted, "172.18.0.2:1234", nil, "172.18.0.2"},
		{"unparsable right-most entry falls back to the peer", trusted, "172.18.0.2:1234", []string{"198.51.100.7, garbage"}, "172.18.0.2"},
		{"unparsable entry after a trusted hop falls back to the peer", trusted, "172.18.0.2:1234", []string{"garbage, 10.0.0.5"}, "172.18.0.2"},
		{"empty entry falls back to the peer", trusted, "172.18.0.2:1234", []string{"198.51.100.7,"}, "172.18.0.2"},
		{"IPv6 client keeps the /64 reduction", trusted, "172.18.0.2:1234", []string{"2001:db8:1:2:aaaa:bbbb:cccc:dddd"}, "2001:db8:1:2::/64"},
		{"IPv4-mapped trusted peer", trusted, "[::ffff:172.18.0.2]:1234", []string{"198.51.100.7"}, "198.51.100.7"},
		{"IPv4-mapped XFF entry is unmapped", trusted, "172.18.0.2:1234", []string{"::ffff:198.51.100.7"}, "198.51.100.7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = c.peer
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := requestClientKey(r, c.trusted); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// The limiter itself must use the helper: two clients behind one trusted proxy
// get separate buckets, and a forged XFF from a direct peer does not mint new ones.
func TestWrapUsesTrustedProxyClientKey(t *testing.T) {
	l := newIPLimiter(1, strictEvery)
	l.trusted = prefixes(t, "172.18.0.0/16")
	h := l.wrap(okHandler())
	call := func(peer, xff string) int {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = peer
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	if call("172.18.0.2:1", "198.51.100.1") != 200 || call("172.18.0.2:1", "198.51.100.2") != 200 {
		t.Fatal("distinct clients behind the proxy must have their own bucket")
	}
	if call("172.18.0.2:1", "198.51.100.1") != http.StatusTooManyRequests {
		t.Fatal("same client behind the proxy must be limited")
	}
	if call("203.0.113.9:1", "1.1.1.1") != 200 || call("203.0.113.9:1", "2.2.2.2") != http.StatusTooManyRequests {
		t.Fatal("an untrusted peer must be keyed by its address whatever XFF says")
	}
}

// STAFF-2: Deps.TrustedProxies must reach every limiter that NewHandler builds.
// Each route group below is limited by its own ipLimiter (auth, refresh,
// stream); dropping the wiring from any one of them fails its case.
func TestNewHandlerWiresTrustedProxiesIntoEveryLimiter(t *testing.T) {
	signer, err := auth.NewSigner("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	newH := func() http.Handler { // fresh limiters per route: register shares login's bucket
		return NewHandler(Deps{
			Signer: signer, Log: slog.New(slog.DiscardHandler),
			AuthBurst: 1, RefreshBurst: 1, Stream: StreamConfig{Burst: 1},
			TrustedProxies: prefixes(t, "172.18.0.0/16"),
		})
	}
	routes := []struct{ name, method, path string }{
		{"login (auth limiter)", "POST", "/auth/login"},
		{"register (auth limiter)", "POST", "/auth/register"},
		{"refresh (refresh limiter)", "POST", "/auth/refresh"},
		{"logout (refresh limiter)", "POST", "/auth/logout"},
		{"stream-ticket (stream limiter)", "POST", "/logs/deployments/d1/stream-ticket"},
		{"stream (stream limiter)", "GET", "/logs/deployments/d1/stream"},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			h := newH()
			call := func(peer, xff string) int {
				r := httptest.NewRequest(rt.method, rt.path, nil)
				r.RemoteAddr = peer
				r.Header.Set("X-Forwarded-For", xff)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				return rec.Code
			}
			const proxy = "172.18.0.2:1"
			if call(proxy, "198.51.100.1") == http.StatusTooManyRequests ||
				call(proxy, "198.51.100.2") == http.StatusTooManyRequests {
				t.Fatal("two clients behind the trusted proxy must not share a bucket")
			}
			if call(proxy, "198.51.100.1") != http.StatusTooManyRequests {
				t.Fatal("the same client behind the proxy must be limited")
			}
			// A direct, untrusted peer cannot mint buckets with a forged header.
			const direct = "203.0.113.9:1"
			if call(direct, "9.9.9.1") == http.StatusTooManyRequests {
				t.Fatal("first request from a direct peer was limited")
			}
			if call(direct, "9.9.9.2") != http.StatusTooManyRequests {
				t.Fatal("a forged X-Forwarded-For from an untrusted peer must not get a fresh bucket")
			}
		})
	}
}

// Metrics are served only on API_METRICS_ADDR; the API mux must not expose them.
func TestMetricsNotOnAPIMux(t *testing.T) {
	signer, err := auth.NewSigner("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Deps{Signer: signer, Log: slog.New(slog.DiscardHandler)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /metrics = %d, want 404", rec.Code)
	}
}
