package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"infrapilot/internal/logs"
	"infrapilot/internal/store"
)

const (
	maxLogLimit  = 1000 // spec §5: at most 1000 lines per request
	maxSearchLen = 256
	// maxLogLineBytes caps one log message in every response and event. Log
	// content is tenant data of unbounded size; it is cut, never interpreted.
	maxLogLineBytes = 8 << 10
	truncatedSuffix = "...[truncated]"
)

var validSources = map[string]bool{"stdout": true, "stderr": true, "system": true}

type logsMeta struct {
	Source     string `json:"source"`
	Count      int    `json:"count"`
	TotalCount *int   `json:"totalCount,omitempty"`
	HasMore    bool   `json:"hasMore"`
}

// capEntry cuts an over-long message at a rune boundary. Nothing else about
// the content is changed or interpreted.
func capEntry(e logs.Entry) logs.Entry {
	if len(e.Message) <= maxLogLineBytes {
		return e
	}
	cut := maxLogLineBytes
	for cut > 0 && !utf8.RuneStart(e.Message[cut]) {
		cut--
	}
	e.Message = e.Message[:cut] + truncatedSuffix
	return e
}

func capEntries(in []logs.Entry) []logs.Entry {
	out := make([]logs.Entry, len(in))
	for i, e := range in {
		out[i] = capEntry(e)
	}
	return out
}

// escapeLike makes s literal inside an ILIKE pattern (ClickHouse escapes with a
// backslash), so searching for "%" or "_" does not match every row.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func parseLogQuery(deploymentID string, v url.Values) (logs.Query, error) {
	q := logs.Query{DeploymentID: deploymentID, Limit: 100}
	if l, err := strconv.Atoi(v.Get("limit")); err == nil && l > 0 {
		q.Limit = min(l, maxLogLimit)
	}
	if search := v.Get("search"); search != "" {
		if len(search) > maxSearchLen {
			return q, fmt.Errorf("search is longer than %d bytes", maxSearchLen)
		}
		q.Search = escapeLike(search)
	}
	for name, dst := range map[string]**time.Time{"after": &q.Start, "before": &q.End} {
		raw := v.Get(name)
		if raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, fmt.Errorf("invalid %s: expected an ISO-8601 timestamp", name)
		}
		*dst = &t
	}
	if src := v.Get("sources"); src != "" {
		q.Sources = strings.Split(src, ",")
		if len(q.Sources) > len(validSources) {
			return q, errors.New("invalid sources")
		}
		for _, s := range q.Sources {
			if !validSources[s] {
				return q, errors.New("invalid sources: use stdout, stderr or system")
			}
		}
	}
	return q, nil
}

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

// getLogs returns history from the right tier (Redis for the last 10 minutes,
// ClickHouse otherwise; the aggregator decides). Same JSON as the Node API.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	id, ok := s.ownedDeployment(w, r)
	if !ok {
		return
	}
	q, err := parseLogQuery(id, r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.Logs.Query(r.Context(), q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	entries := capEntries(res.Logs)
	meta := logsMeta{Source: res.Source, Count: len(entries), TotalCount: res.TotalCount}
	if res.TotalCount != nil && *res.TotalCount > 0 {
		meta.HasMore = len(entries) < *res.TotalCount
	}
	writeJSON(w, http.StatusOK, struct {
		Logs []logs.Entry `json:"logs"`
		Meta logsMeta     `json:"meta"`
	}{entries, meta})
}

func (s *Server) logsHealth(w http.ResponseWriter, r *http.Request) {
	n, err := s.Logs.ActiveStreamCount(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "healthy", "activeLogStreams": n})
}
