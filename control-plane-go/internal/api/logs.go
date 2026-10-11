package api

import (
	"context"
	"errors"
	"net/http"

	"infrapilot/internal/store"
)

// deploymentOwned reports whether uid owns the deployment through a live
// service. Missing, foreign and soft-deleted deployments are all false.
func (s *Server) deploymentOwned(ctx context.Context, id, uid string) (bool, error) {
	_, err := s.Store.GetDeploymentForOwner(ctx, id, uid)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ownedDeployment returns the path's deployment id after the ownership check.
// It writes the same 404 for missing, malformed, foreign and soft-deleted ids.
func (s *Server) ownedDeployment(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, shaped := pathID(r)
	if !shaped {
		writeError(w, http.StatusNotFound, "deployment not found")
		return "", false
	}
	ok, err := s.deploymentOwned(r.Context(), id, userID(r))
	if err != nil {
		s.internalError(w, r, err)
		return "", false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "deployment not found")
		return "", false
	}
	return id, true
}

// streamTicket issues a short-lived (the ticket store's TTL, 60 s in cmd/api),
// single-use ticket bound to this user and this deployment. EventSource cannot
// send an Authorization header, so the stream route accepts the ticket in the
// query string instead of the JWT.
func (s *Server) streamTicket(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedDeployment(w, r)
	if !ok {
		return
	}
	ticket, err := s.Tickets.Issue(r.Context(), userID(r)+":"+id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}
