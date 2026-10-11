// Package api is the HTTP API used by cp-ui. Every route except health, auth
// and the ticket-authenticated log stream requires a JWT and checks ownership.
package api

import (
	"context"
	"log/slog"
	"net/http"

	"infrapilot/internal/auth"
	"infrapilot/internal/logs"
	"infrapilot/internal/metrics"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

// LiveLogs is implemented by *logs.Redis.
type LiveLogs interface {
	Subscribe(ctx context.Context, deploymentID string) (<-chan logs.Entry, func(), error)
}

// Tickets is implemented by *tickets.Store.
type Tickets interface {
	Issue(ctx context.Context, value string) (string, error)
	Redeem(ctx context.Context, ticket string) (string, error)
}

type Deps struct {
	Store       *store.Store
	Redis       Pinger
	Signer      *auth.Signer
	Secrets     *secrets.Box
	Logs        *logs.Aggregator
	Live        LiveLogs
	Tickets     Tickets
	Metrics     *metrics.API
	Log         *slog.Logger
	CORSOrigins []string
	DevLogin    bool // register POST /dev/login (development only; unset APP_ENV means production)
	AuthBurst   int  // requests per IP before /auth/* is rate limited; 0 means 5
}

type Server struct {
	Deps
	authLimit *ipLimiter
}

func NewHandler(d Deps) http.Handler {
	burst := d.AuthBurst
	if burst <= 0 {
		burst = 5
	}
	s := &Server{Deps: d, authLimit: newIPLimiter(burst)}
	mux := http.NewServeMux()
	s.routes(mux)
	return withSecurityHeaders(withRequestID(withCORS(d.CORSOrigins, s.withRecover(jsonErrors(mux)))))
}

// routes registers every endpoint. s.private requires a JWT; s.authLimit
// rate-limits credential endpoints per client IP.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
}

// withSecurityHeaders marks every response (including preflights and errors)
// as non-sniffable and uncacheable by shared caches.
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// statusCapture records what a handler would write, discarding the body.
type statusCapture struct {
	h      http.Header
	status int
}

func (c *statusCapture) Header() http.Header         { return c.h }
func (c *statusCapture) WriteHeader(code int)        { c.status = code }
func (c *statusCapture) Write(b []byte) (int, error) { return len(b), nil }

// jsonErrors answers the mux's own 404 and 405 with the API's JSON error shape
// (keeping the Allow header). Matched routes go through mux.ServeHTTP so
// r.Pattern is set for metrics.
func jsonErrors(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			c := &statusCapture{h: http.Header{}, status: http.StatusOK}
			h.ServeHTTP(c, r)
			switch c.status {
			case http.StatusNotFound:
				writeError(w, http.StatusNotFound, "not_found")
				return
			case http.StatusMethodNotAllowed:
				if allow := c.h.Get("Allow"); allow != "" {
					w.Header().Set("Allow", allow)
				}
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
