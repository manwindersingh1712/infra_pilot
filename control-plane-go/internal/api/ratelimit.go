package api

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter allows `burst` requests per client IP, refilling one every 6s
// (10/min). It is in memory: fine for one API instance (spec §12).
type ipLimiter struct {
	mu      sync.Mutex
	burst   int
	clients map[string]*client
	pruned  time.Time
}

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

func newIPLimiter(burst int) *ipLimiter {
	return &ipLimiter{burst: burst, clients: map[string]*client{}, pruned: time.Now()}
}

func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.Sub(l.pruned) > time.Minute {
		for k, c := range l.clients {
			if now.Sub(c.seen) > 10*time.Minute {
				delete(l.clients, k)
			}
		}
		l.pruned = now
	}
	c, ok := l.clients[ip]
	if !ok {
		c = &client{lim: rate.NewLimiter(rate.Every(6*time.Second), l.burst)}
		l.clients[ip] = c
	}
	c.seen = now
	return c.lim.Allow()
}

// wrap rejects requests over the limit with 429. The client IP is the TCP
// peer; X-Forwarded-For is not trusted.
func (l *ipLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !l.allow(ip) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
