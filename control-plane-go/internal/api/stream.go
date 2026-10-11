package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
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
	// MaxPerUser and MaxPerDeployment cap concurrent streams (429
	// too_many_streams beyond them). Defaults 5 and 20. In memory, so they
	// hold for a single API instance (spec §12).
	MaxPerUser, MaxPerDeployment int
	// MaxDuration ends a stream after this long with an "end" event; the
	// client asks for a new ticket and reconnects. Default 1 h.
	MaxDuration time.Duration
	// WriteTimeout is the deadline for each write to a stream. A client that
	// stops reading is dropped after this long. Default 10 s.
	WriteTimeout time.Duration
	// Shutdown, when cancelled, ends every open stream (with an "end" event)
	// and makes new ones answer 503. cmd/api passes a context that is
	// cancelled when shutdown begins (Task 14). Nil means never.
	Shutdown context.Context
}

const (
	defaultStreamBurst = 20
	streamEvery        = time.Second // 60 requests/min per client address
	defaultHeartbeat   = 15 * time.Second
	// historyEntries is the most lines the first "history" event carries.
	historyEntries = 100

	defaultMaxPerUser       = 5
	defaultMaxPerDeployment = 20
	defaultMaxDuration      = time.Hour
	defaultWriteTimeout     = 10 * time.Second
)

// streamSlots counts open streams per user and per deployment.
type streamSlots struct {
	mu                           sync.Mutex
	perUser, perDep              map[string]int
	maxPerUser, maxPerDeployment int
}

func newStreamSlots(c StreamConfig) *streamSlots {
	u, d := c.MaxPerUser, c.MaxPerDeployment
	if u <= 0 {
		u = defaultMaxPerUser
	}
	if d <= 0 {
		d = defaultMaxPerDeployment
	}
	return &streamSlots{perUser: map[string]int{}, perDep: map[string]int{}, maxPerUser: u, maxPerDeployment: d}
}

// acquire takes a slot for (user, deployment) unless either cap is reached.
func (l *streamSlots) acquire(uid, dep string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.perUser[uid] >= l.maxPerUser || l.perDep[dep] >= l.maxPerDeployment {
		return false
	}
	l.perUser[uid]++
	l.perDep[dep]++
	return true
}

func (l *streamSlots) release(uid, dep string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for m, k := range map[*map[string]int]string{&l.perUser: uid, &l.perDep: dep} {
		if (*m)[k] <= 1 {
			delete(*m, k)
		} else {
			(*m)[k]--
		}
	}
}

func (l *streamSlots) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.perUser {
		n += c
	}
	return n
}

func (l *streamSlots) forDeployment(dep string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.perDep[dep]
}

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
	sd := s.Stream.Shutdown
	if sd != nil && sd.Err() != nil { // closing down: refuse before spending the ticket
		writeError(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
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

	if !s.streams.acquire(uid, id) {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusTooManyRequests, "too_many_streams")
		return
	}
	defer s.streams.release(uid, id)

	// The stream ends when the client goes away, after MaxDuration, or when the
	// server shuts down; the Redis subscription is tied to this context.
	maxDur := s.Stream.MaxDuration
	if maxDur <= 0 {
		maxDur = defaultMaxDuration
	}
	ctx, cancel := context.WithTimeout(r.Context(), maxDur)
	defer cancel()
	if sd != nil {
		defer context.AfterFunc(sd, cancel)()
	}
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
	// Per-request write deadline, set before every write. The http.Server's
	// WriteTimeout covers a whole response and would kill a long-lived stream
	// (Task 14: leave WriteTimeout set for the other routes; this overrides it
	// for this request). A client that stops reading fails the write after
	// WriteTimeout instead of pinning the goroutine and the subscription.
	wt := s.Stream.WriteTimeout
	if wt <= 0 {
		wt = defaultWriteTimeout
	}
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	arm := func() error {
		if err := rc.SetWriteDeadline(time.Now().Add(wt)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	send := func(event string, v any) error {
		if err := arm(); err != nil {
			return err
		}
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
			switch {
			case r.Context().Err() != nil: // client gone: nothing to tell it
			case sd != nil && sd.Err() != nil:
				_ = send("end", map[string]string{"reason": "shutdown"})
			default:
				_ = send("end", map[string]string{"reason": "max_duration"})
			}
			return
		case e, ok := <-live:
			if !ok || send("log", capEntry(e)) != nil {
				return
			}
		case <-ping.C:
			if arm() != nil {
				return
			}
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
