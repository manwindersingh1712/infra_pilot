package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

//nolint:unused // used by the endpoint tasks that follow (Plan 2 Tasks 6+)
func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "requestId", requestID(r), "err", err)
	writeError(w, http.StatusInternalServerError, "internal_error")
}

// decodeJSON reads an optional JSON body (max 1 MiB). An empty body leaves dst unchanged.
//
//nolint:unused // used by the endpoint tasks that follow (Plan 2 Tasks 6+)
func decodeJSON(r *http.Request, dst any) error {
	err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
