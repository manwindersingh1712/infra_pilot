package api

import (
	"errors"
	"net/http"
	"unicode"
	"unicode/utf8"

	"infrapilot/internal/store"
)

const maxProjectNameRunes = 100

// validProjectName requires 2 to 100 runes and no control characters (a NUL
// byte would pass the length check and make Postgres fail with a 500).
func validProjectName(name string) bool {
	n := utf8.RuneCountInString(name)
	if n < 2 || n > maxProjectNameRunes || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if !validProjectName(body.Name) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	p, err := s.Store.CreateProject(r.Context(), body.Name, userID(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListProjects(r.Context(), userID(r))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

// getProject answers 404 for a missing project and for one owned by someone
// else, so ids cannot be probed.
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProjectForOwner(r.Context(), r.PathValue("id"), userID(r))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Project not found")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}
