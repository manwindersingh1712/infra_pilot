package api

import (
	"net/http"

	"infrapilot/internal/store"
)

type canvasConnection struct {
	ID       string  `json:"id"`
	SourceID string  `json:"sourceId"`
	TargetID string  `json:"targetId"`
	Label    *string `json:"label"`
}

// publicCanvasService runs the canvas view's repo URL through publicService,
// the only way a service's URL may leave the API.
func publicCanvasService(c store.CanvasService) store.CanvasService {
	c.RepoURL = publicService(store.Service{RepoURL: c.RepoURL}).RepoURL
	return c
}

// ownedProjectID returns the path's project id after the ownership check; it
// writes 404 "Project not found" (same for malformed, missing and foreign ids).
func (s *Server) ownedProjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, _ := pathID(r) // a malformed id fails the shape check inside ownsProject too
	return id, s.ownsProject(w, r, id)
}

func (s *Server) getCanvas(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedProjectID(w, r)
	if !ok {
		return
	}
	services, err := s.Store.ListCanvasServices(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	for i := range services {
		services[i] = publicCanvasService(services[i])
	}
	conns, err := s.Store.ListProjectConnections(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]canvasConnection, 0, len(conns))
	for _, c := range conns {
		out = append(out, canvasConnection{ID: c.ID, SourceID: c.SourceID, TargetID: c.TargetID, Label: c.Label})
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": services, "connections": out})
}
