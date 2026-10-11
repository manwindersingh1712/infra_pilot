package api

import (
	"errors"
	"math"
	"net/http"
	"unicode"
	"unicode/utf8"

	"infrapilot/internal/store"
)

type canvasConnection struct {
	ID       string  `json:"id"`
	SourceID string  `json:"sourceId"`
	TargetID string  `json:"targetId"`
	Label    *string `json:"label"`
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

// maxCoord bounds a node coordinate. Real canvases use values in the
// thousands; the bound keeps NaN, Inf and absurd values out of the database
// (and out of the UI's layout maths).
const maxCoord = 1e6

func validCoord(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && math.Abs(v) <= maxCoord
}

// positionInput is a position as sent by a client; the pointers tell a missing
// coordinate from zero.
type positionInput struct {
	ID        string   `json:"id"`
	PositionX *float64 `json:"positionX"`
	PositionY *float64 `json:"positionY"`
}

func (p positionInput) valid() bool {
	return p.PositionX != nil && p.PositionY != nil && validCoord(*p.PositionX) && validCoord(*p.PositionY)
}

func (s *Server) updateCanvas(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Services []positionInput `json:"services"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.Services == nil {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	for _, p := range body.Services {
		if !p.valid() {
			writeError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	id, ok := s.ownedProjectID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	err := s.Store.InTx(ctx, func(tx *store.Store) error {
		for _, p := range body.Services {
			if !idShape.MatchString(p.ID) {
				continue // cannot be a stored id; like an unknown id it is ignored
			}
			if err := tx.UpdateServicePositionInProject(ctx, p.ID, id, *p.PositionX, *p.PositionY); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) updatePosition(w http.ResponseWriter, r *http.Request) {
	var body positionInput
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if !body.valid() {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Service not found")
		return
	}
	svc, err := s.Store.GetServiceForOwner(r.Context(), id, userID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Service not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// Scoped to the project the ownership check just read.
	if err := s.Store.UpdateServicePositionInProject(r.Context(), svc.ID, svc.ProjectID, *body.PositionX, *body.PositionY); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) autoLayout(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedProjectID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	services, err := s.Store.ListCanvasServices(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	edges, err := s.Store.ListSourceEdges(ctx, id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	ids := make([]string, len(services))
	for i, svc := range services {
		ids[i] = svc.ID
	}
	positions := layout(ids, edges)
	err = s.Store.InTx(ctx, func(tx *store.Store) error {
		for _, p := range positions {
			if err := tx.UpdateServicePositionInProject(ctx, p.ID, id, p.PositionX, p.PositionY); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": positions})
}

const maxLabelRunes = 100

// validLabel: at most 100 runes, valid UTF-8, no control characters (a NUL
// byte would make Postgres fail the insert with a 500).
func validLabel(l string) bool {
	if !utf8.ValidString(l) || utf8.RuneCountInString(l) > maxLabelRunes {
		return false
	}
	for _, r := range l {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// createConnection connects two services of one project the caller owns.
// Foreign, missing, deleted, malformed and other-project ends all answer the
// same 404, so the response never says which end failed.
func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SourceID string  `json:"sourceId"`
		TargetID string  `json:"targetId"`
		Label    *string `json:"label"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.Label != nil && !validLabel(*body.Label) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !idShape.MatchString(body.SourceID) || !idShape.MatchString(body.TargetID) {
		writeError(w, http.StatusNotFound, "Service not found")
		return
	}
	ctx := r.Context()
	if body.SourceID == body.TargetID {
		// Ownership first, so a foreign id cannot tell "self" from "not yours".
		_, err := s.Store.GetServiceForOwner(ctx, body.SourceID, userID(r))
		switch {
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "Service not found")
		case err != nil:
			s.internalError(w, r, err)
		default:
			writeError(w, http.StatusBadRequest, "Cannot connect service to itself")
		}
		return
	}
	c, err := s.Store.CreateConnectionForOwner(ctx, body.SourceID, body.TargetID, userID(r), body.Label)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "Service not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "Connection already exists")
	case err != nil:
		s.internalError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, c)
	}
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "Connection not found")
		return
	}
	// Both ends must be the caller's live services.
	c, err := s.Store.GetConnectionForOwner(r.Context(), id, userID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Connection not found")
		return
	}
	if err == nil {
		err = s.Store.DeleteConnection(r.Context(), c.ID)
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}
