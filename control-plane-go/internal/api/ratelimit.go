package api

import (
	"container/list"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	limiterIdleTTL    = 10 * time.Minute
	limiterMaxClients = 100_000
)

// ipLimiter allows `burst` requests per client IP, refilling one every 6s
// (10/min). It is in memory: fine for one API instance (spec §12).
//
// Clients live in an LRU list (front = most recently seen), so idle entries are
// dropped from the back in O(expired) and, when the map is full, the least
// recently seen client is evicted in O(1). No full scan runs under the lock.
type ipLimiter struct {
	mu      sync.Mutex
	burst   int
	max     int
	clients map[string]*list.Element // value: *client
	lru     *list.List
	now     func() time.Time // injectable for tests
}

type client struct {
	key  string
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(burst int) *ipLimiter {
	return &ipLimiter{
		burst: burst, max: limiterMaxClients,
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
	c := &client{key: key, lim: rate.NewLimiter(rate.Every(6*time.Second), l.burst), seen: now}
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

// wrap rejects requests over the limit with 429. The client is the TCP peer;
// X-Forwarded-For is not trusted.
func (l *ipLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientKey(r.RemoteAddr)) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
