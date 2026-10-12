package api

import (
	"container/list"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	limiterIdleTTL    = 10 * time.Minute
	limiterMaxClients = 100_000
)

const (
	strictEvery         = 6 * time.Second // register, login: 10 requests/min
	refreshEvery        = time.Second     // refresh, logout: 60 requests/min
	defaultStrictBurst  = 5
	defaultRefreshBurst = 20
)

// ipLimiter allows `burst` requests per client IP, refilling one token every
// `every`. It is in memory: fine for one API instance (spec §12).
//
// Clients live in an LRU list (front = most recently seen), so idle entries are
// dropped from the back in O(expired) and, when the map is full, the least
// recently seen client is evicted in O(1). No full scan runs under the lock.
type ipLimiter struct {
	mu      sync.Mutex
	burst   int
	every   time.Duration
	max     int
	clients map[string]*list.Element // value: *client
	lru     *list.List
	now     func() time.Time // injectable for tests
	trusted []netip.Prefix   // TRUSTED_PROXIES; empty means the key is always the TCP peer
}

type client struct {
	key  string
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(burst int, every time.Duration) *ipLimiter {
	return &ipLimiter{
		burst: burst, every: every, max: limiterMaxClients,
		clients: map[string]*list.Element{}, lru: list.New(), now: time.Now,
	}
}

func (l *ipLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

func (l *ipLimiter) remove(e *list.Element) {
	l.lru.Remove(e)
	delete(l.clients, e.Value.(*client).key)
}

func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for e := l.lru.Back(); e != nil && now.Sub(e.Value.(*client).seen) > limiterIdleTTL; e = l.lru.Back() {
		l.remove(e)
	}
	if e, ok := l.clients[key]; ok {
		c := e.Value.(*client)
		c.seen = now
		l.lru.MoveToFront(e)
		return c.lim.AllowN(now, 1)
	}
	for len(l.clients) >= l.max && l.lru.Len() > 0 {
		l.remove(l.lru.Back())
	}
	c := &client{key: key, lim: rate.NewLimiter(rate.Every(l.every), l.burst), seen: now}
	l.clients[key] = l.lru.PushFront(c)
	return c.lim.AllowN(now, 1)
}

// clientKey is the limiter key for a TCP peer address. IPv4-mapped IPv6 is
// unmapped, and IPv6 is reduced to its /64 (the usual per-subscriber
// allocation), so one subscriber cannot mint unlimited buckets.
func clientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		if p, err := addr.Prefix(64); err == nil {
			return p.String()
		}
	}
	return addr.String()
}

// requestClientKey is the limiter key for a request. The key is the TCP peer
// unless the peer is in trusted; then it is the right-most X-Forwarded-For
// entry that is not in trusted (the first address a trusted proxy did not
// vouch for). If the header is absent, an entry does not parse, or every entry
// is trusted, the peer is used. A peer outside trusted never has the header
// honoured, so clients cannot mint buckets by forging it.
func requestClientKey(r *http.Request, trusted []netip.Prefix) string {
	peer := clientKey(r.RemoteAddr)
	if len(trusted) == 0 || !inPrefixes(trusted, peerAddr(r.RemoteAddr)) {
		return peer
	}
	var entries []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		entries = append(entries, strings.Split(v, ",")...)
	}
	for i := len(entries) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(entries[i]))
		if err != nil {
			return peer
		}
		a = a.Unmap().WithZone("")
		if !inPrefixes(trusted, a) {
			return clientKey(a.String())
		}
	}
	return peer
}

func peerAddr(remoteAddr string) netip.Addr {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}

func inPrefixes(ps []netip.Prefix, a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// wrap rejects requests over the limit with 429. The client is the TCP peer,
// or, when the peer is a trusted proxy (TRUSTED_PROXIES), the right-most
// untrusted X-Forwarded-For entry; see requestClientKey.
func (l *ipLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(requestClientKey(r, l.trusted)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
