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
	Store        *store.Store
	Redis        Pinger
	Signer       *auth.Signer
	Secrets      *secrets.Box
	Logs         *logs.Aggregator
	Live         LiveLogs
	Tickets      Tickets
	Metrics      *metrics.API
	Log          *slog.Logger
	CORSOrigins  []string
	DevLogin     bool // register POST /dev/login (development only; unset APP_ENV means production)
	AuthBurst    int  // burst of the register/login limiter (10/min); 0 means 5
	RefreshBurst int  // burst of the refresh/logout limiter (60/min); 0 means 20
}

type Server struct {
	Deps
	authLimit    *ipLimiter // register, login
	refreshLimit *ipLimiter // refresh, logout
}

// newServer builds the Server with its two rate limiters (defaults applied).
func newServer(d Deps) *Server {
	strict, refresh := d.AuthBurst, d.RefreshBurst
	if strict <= 0 {
		strict = defaultStrictBurst
	}
	if refresh <= 0 {
		refresh = defaultRefreshBurst
	}
	return &Server{Deps: d,
		authLimit:    newIPLimiter(strict, strictEvery),
		refreshLimit: newIPLimiter(refresh, refreshEvery)}
}

func NewHandler(d Deps) http.Handler {
	s := newServer(d)
	mux := http.NewServeMux()
	s.routes(mux)
	return withSecurityHeaders(withRequestID(withCORS(d.CORSOrigins, s.withRecover(jsonErrors(mux)))))
}

// routes registers every endpoint. s.private requires a JWT. Each endpoint
// class has its own per-IP bucket: authLimit for register and login,
// refreshLimit for refresh and logout, so a login burst cannot block token
// refresh behind one shared address.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)

	strict := func(h http.HandlerFunc) http.Handler { return s.authLimit.wrap(h) }
	refresh := func(h http.HandlerFunc) http.Handler { return s.refreshLimit.wrap(h) }
	mux.Handle("POST /auth/register", strict(s.register))
	mux.Handle("POST /auth/login", strict(s.login))
	mux.Handle("POST /auth/refresh", refresh(s.refresh))
	mux.Handle("POST /auth/logout", refresh(s.logout))
	if s.DevLogin {
		mux.HandleFunc("POST /dev/login", s.devLogin)
	}

	mux.Handle("POST /projects", s.private(s.createProject))
	mux.Handle("GET /projects", s.private(s.listProjects))
	mux.Handle("GET /projects/{id}", s.private(s.getProject))

	mux.Handle("POST /services", s.private(s.createService))
	mux.Handle("GET /services", s.private(s.listServices))
	mux.Handle("POST /services/{id}/deploy", s.private(s.deployService))
	mux.Handle("GET /services/{id}/env", s.private(s.getServiceEnv))
	mux.Handle("POST /services/{id}/env", s.private(s.setServiceEnv))
	mux.Handle("DELETE /services/{id}/env/{key}", s.private(s.deleteServiceEnv))
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
