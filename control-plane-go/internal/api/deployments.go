package api

import (
	"net/http"

	"infrapilot/internal/store"
)

// listDeployments returns the caller's 50 newest deployments, optionally for
// one service. A serviceId that cannot be a stored id answers like a service
// with no deployments (never reaches Postgres: NUL bytes and invalid UTF-8
// would be a 500).
func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	serviceID := r.URL.Query().Get("serviceId")
	if serviceID != "" && !idShape.MatchString(serviceID) {
		writeJSON(w, http.StatusOK, []store.Deployment{})
		return
	}
	deps, err := s.Store.ListDeploymentsForOwner(r.Context(), userID(r), serviceID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deps)
}
