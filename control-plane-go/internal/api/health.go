package api

import "net/http"

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// readyz reports 503 when Postgres or Redis is unreachable.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Store.Ping(r.Context()) != nil || s.Redis.Ping(r.Context()) != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"ok": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
