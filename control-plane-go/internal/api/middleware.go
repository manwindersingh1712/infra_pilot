package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

type userKey struct{}
type requestIDKey struct{}

// userID returns the authenticated user set by private.
func userID(r *http.Request) string {
	id, _ := r.Context().Value(userKey{}).(string)
	return id
}

func requestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey{}).(string)
	return id
}

// private requires a valid Bearer access token.
func (s *Server) private(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		uid, err := s.Signer.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey{}, uid)))
	})
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// withRequestID keeps a well-formed incoming X-Request-ID or generates one,
// echoes it in the response and stores it in the context for logs.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !safeRequestID.MatchString(id) {
			buf := make([]byte, 8)
			_, _ = rand.Read(buf)
			id = hex.EncodeToString(buf)
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// withCORS allows only the configured origins.
// withCORS must stay an exact-match allow-list (config rejects "*"). Adding
// Access-Control-Allow-Credentials needs a security re-review (CTO, 2026-10-07).
func withCORS(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		allowed := origin != "" && slices.Contains(origins, origin)
		h := w.Header()
		if allowed {
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Expose-Headers", "X-Request-ID")
		}
		h.Add("Vary", "Origin")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if allowed {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				h.Set("Access-Control-Max-Age", "600")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach Flush on the real writer (SSE).
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// withRecover logs each request (path only, never the query), records
// metrics by route pattern, and turns panics into 500s.
func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Error("panic", "method", r.Method, "path", r.URL.Path, "requestId", requestID(r), "panic", rec)
				writeError(sw, http.StatusInternalServerError, "internal_error")
			}
			route := r.Pattern // set by ServeMux on this request
			if route == "" {
				route = "unmatched"
			}
			s.Metrics.Observe(route, r.Method, sw.status, time.Since(start))
			s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
				"ms", time.Since(start).Milliseconds(), "requestId", requestID(r))
		}()
		next.ServeHTTP(sw, r)
	})
}
