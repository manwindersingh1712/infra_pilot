package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"infrapilot/internal/logs"
	"infrapilot/internal/tickets"
)

// StreamConfig tunes the log stream routes. Zero values mean the defaults.
type StreamConfig struct {
	// Burst is the per-IP burst of the limiter shared by the stream-ticket and
	// stream routes (one token per second). Default 20.
	Burst int
	// Heartbeat is the interval between ": ping" comment lines that keep
	// proxies from closing an idle stream. Default 15 s.
	Heartbeat time.Duration
}

const (
	defaultStreamBurst = 20
	streamEvery        = time.Second // 60 requests/min per client address
	defaultHeartbeat   = 15 * time.Second
	// historyEntries is the most lines the first "history" event carries.
	historyEntries = 100
)

type streamMeta struct {
	Source     string `json:"source"`
	TotalCount *int   `json:"totalCount,omitempty"`
}

// invalidTicket is the one answer for every ticket problem (missing, malformed,
// unknown, expired, reused, wrong deployment, wrong user, deployment no longer
// owned), so a caller learns nothing about which check failed.
func invalidTicket(w http.ResponseWriter) {
	writeError(w, http.StatusUnauthorized, "invalid_ticket")
}

// redactTicket removes the ticket from text that is about to be logged. The
// ticket travels in the query string, so it must never reach a log line.
func redactTicket(err error, ticket string) string {
	msg := err.Error()
	if ticket != "" {
		msg = strings.ReplaceAll(msg, ticket, "[redacted]")
	}
	return msg
}

// streamFailed logs a failure on the stream route without the ticket and
// answers status/code.
func (s *Server) streamFailed(w http.ResponseWriter, r *http.Request, ticket string, err error, status int, code string) {
	s.Log.Error("log stream failed", "method", r.Method, "path", r.URL.Path, "requestId", requestID(r), "err", redactTicket(err, ticket))
	writeError(w, status, code)
}

// streamLogs authenticates with a single-use ticket (EventSource cannot send
// headers), then sends one "history" event ({logs, meta}), a "log" event per
// new line, and a comment every Heartbeat so proxies keep the connection open.
//
// The ticket is in the URL, so nothing on this route logs or echoes the query.
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	value, err := s.Tickets.Redeem(r.Context(), ticket)
	if err != nil {
		if errors.Is(err, tickets.ErrInvalid) {
			invalidTicket(w)
			return
		}
		s.streamFailed(w, r, ticket, err, http.StatusServiceUnavailable, "unavailable")
		return
	}
	// The ticket must be exactly "<userID>:<deploymentID of this URL>".
	id, shaped := pathID(r)
	uid, _, _ := strings.Cut(value, ":")
	if !shaped || uid == "" || value != uid+":"+id {
		invalidTicket(w)
		return
	}
	// Ownership again at redeem time: the service may have been deleted or the
	// deployment is not this user's after all.
	owned, err := s.deploymentOwned(r.Context(), id, uid)
	if err != nil {
		s.streamFailed(w, r, ticket, err, http.StatusInternalServerError, "internal_error")
		return
	}
	if !owned {
		invalidTicket(w)
		return
	}

	ctx := r.Context()
	// Subscribe before reading history so lines written in between are not
	// lost; the UI drops duplicates (same timestamp and message).
	live, stop, err := s.Live.Subscribe(ctx, id)
	if err != nil {
		s.streamFailed(w, r, ticket, err, http.StatusInternalServerError, "internal_error")
		return
	}
	defer stop()
	hist, err := s.Logs.Query(ctx, logs.Query{DeploymentID: id, Limit: historyEntries})
	if err != nil {
		s.streamFailed(w, r, ticket, err, http.StatusInternalServerError, "internal_error")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	send := func(event string, v any) error {
		if err := writeEvent(w, event, v); err != nil {
			return err
		}
		return rc.Flush()
	}

	history := map[string]any{
		"logs": capEntries(hist.Logs[:min(len(hist.Logs), historyEntries)]),
		"meta": streamMeta{Source: hist.Source, TotalCount: hist.TotalCount},
	}
	if send("history", history) != nil {
		return
	}
	beat := s.Stream.Heartbeat
	if beat <= 0 {
		beat = defaultHeartbeat
	}
	ping := time.NewTicker(beat)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-live:
			if !ok || send("log", capEntry(e)) != nil {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}

// writeEvent writes one SSE event. json.Marshal escapes newlines and carriage
// returns, so the data is always a single line and log content cannot add
// fields or events; the event name is a constant chosen by this package.
func writeEvent(w io.Writer, event string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}
