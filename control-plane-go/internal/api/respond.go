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

// maxBodyBytes caps every JSON request body.
const maxBodyBytes = 1 << 20

var errTrailingData = errors.New("unexpected data after JSON body")

// decodeJSON reads an optional JSON body (max 1 MiB, one value, no trailing
// data). An empty body leaves dst unchanged. On failure call writeDecodeError.
//
//nolint:unused // used by the endpoint tasks that follow (Plan 2 Tasks 6+)
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return err
		}
		return errTrailingData
	}
	return nil
}

// writeDecodeError answers a decodeJSON failure: 413 when the body was over the
// limit, otherwise 400 with msg.
//
//nolint:unused // used by the endpoint tasks that follow (Plan 2 Tasks 6+)
func writeDecodeError(w http.ResponseWriter, err error, msg string) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
		return
	}
	writeError(w, http.StatusBadRequest, msg)
}
