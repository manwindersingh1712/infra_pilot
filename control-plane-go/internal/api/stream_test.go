package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/logs"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestWriteEvent(t *testing.T) {
	var buf bytes.Buffer
	if err := writeEvent(&buf, "log", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "event: log\ndata: {\"a\":\"b\"}\n\n" {
		t.Fatalf("got %q", buf.String())
	}
}

type sseFrame struct {
	event string
	data  string
	lines []string // raw lines of the frame, without the final blank
}

type sseReader struct {
	t  *testing.T
	rd *bufio.Reader
}

// next returns the next frame; a comment-only frame has an empty event.
func (s *sseReader) next() sseFrame {
	s.t.Helper()
	var f sseFrame
	for {
		line, err := s.rd.ReadString('\n')
		if err != nil {
			s.t.Fatalf("read frame (%v) after %q", err, f.lines)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			if len(f.lines) == 0 {
				continue
			}
			return f
		}
		f.lines = append(f.lines, line)
		switch {
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

type streamFixture struct {
	hs   *harness
	srv  *httptest.Server
	f    testutil.Fixture
	dep  store.Deployment
	user string
	tok  string
}

func newStreamFixture(t *testing.T, opts ...func(*Deps)) *streamFixture {
	t.Helper()
	hs := newHarnessWith(t, opts...)
	f := testutil.Seed(t, hs.st, store.TypeNodejs)
	dep, err := hs.st.CreateDeployment(context.Background(), f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(hs.h)
	t.Cleanup(srv.Close)
	return &streamFixture{hs: hs, srv: srv, f: f, dep: dep, user: f.UserID, tok: hs.token(f.UserID)}
}

func (sf *streamFixture) ticket() string {
	sf.hs.t.Helper()
	return decode[map[string]string](sf.hs.t, sf.hs.do("POST", "/logs/deployments/"+sf.dep.ID+"/stream-ticket", sf.tok, nil))["ticket"]
}

func (sf *streamFixture) streamURL(depID, ticket string) string {
	return sf.srv.URL + "/logs/deployments/" + depID + "/stream?ticket=" + ticket
}

// open starts a stream; the returned cancel closes the client side.
func (sf *streamFixture) open(t *testing.T, ticket string) (*http.Response, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", sf.streamURL(sf.dep.ID, ticket), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	return resp, cancel
}

// get requests a stream URL and returns status and body (for rejections).
func (sf *streamFixture) get(t *testing.T, depID, ticket string) (int, string) {
	t.Helper()
	// A request that wrongly opens a stream never ends; the short timeout turns
	// that into a failing status instead of a hung test.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", sf.streamURL(depID, ticket), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(b)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamWithTicket(t *testing.T) {
	sf := newStreamFixture(t)
	hs, dep := sf.hs, sf.dep
	other, _ := hs.st.CreateDeployment(context.Background(), sf.f.ServiceID, "v2")
	hs.hot.entries[dep.ID] = []logs.Entry{{DeploymentID: dep.ID, Source: "stdout", Message: "hello", Timestamp: "t0"}}

	expectStatus(t, hs.do("POST", "/logs/deployments/"+dep.ID+"/stream-ticket", hs.token("intruder"), nil), 404)
	ticket := sf.ticket()
	// A ticket only works for the deployment it was issued for (and is then used up).
	if code, _ := sf.get(t, other.ID, ticket); code != 401 {
		t.Fatalf("wrong deployment: %d", code)
	}
	ticket = sf.ticket()

	resp, _ := sf.open(t, ticket)
	h := resp.Header
	if resp.StatusCode != 200 || h.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d content-type %q", resp.StatusCode, h.Get("Content-Type"))
	}
	if h.Get("Cache-Control") != "no-cache, no-store" || h.Get("X-Accel-Buffering") != "no" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers %v", h)
	}
	rd := &sseReader{t: t, rd: bufio.NewReader(resp.Body)}
	fr := rd.next()
	if fr.event != "history" || !strings.Contains(fr.data, `"message":"hello"`) || !strings.Contains(fr.data, `"meta":{"source":"redis"}`) {
		t.Fatalf("history: %v", fr)
	}
	// Each live line arrives as its own flushed event.
	for _, msg := range []string{"live!", "again"} {
		hs.live.ch <- logs.Entry{DeploymentID: dep.ID, Source: "stderr", Message: msg, Timestamp: "t1"}
		fr = rd.next()
		if fr.event != "log" || fr.data != `{"deploymentId":"`+dep.ID+`","source":"stderr","message":"`+msg+`","timestamp":"t1"}` {
			t.Fatalf("live: %v", fr)
		}
	}

	if code, _ := sf.get(t, dep.ID, ticket); code != 401 {
		t.Fatalf("reused ticket: %d", code)
	}
	resp2, err := http.Get(sf.srv.URL + "/logs/deployments/" + dep.ID + "/stream?token=" + sf.tok)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 401 {
		t.Fatalf("JWT in the URL must not work: %d", resp2.StatusCode)
	}
	resp3, err := http.Get(sf.srv.URL + "/logs/deployments/" + dep.ID + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 401 {
		t.Fatalf("no ticket: %d", resp3.StatusCode)
	}
}

// Every way a ticket can be wrong gives the same status and body, with no hint
// of why (no oracle).
func TestStreamRejectsBadTicketsUniformly(t *testing.T) {
	sf := newStreamFixture(t)
	hs := sf.hs
	otherDep, _ := hs.st.CreateDeployment(context.Background(), sf.f.ServiceID, "v2")
	stranger := testutil.Seed(t, hs.st, store.TypeNodejs)
	deleted := testutil.Seed(t, hs.st, store.TypeNodejs)
	depDeleted, _ := hs.st.CreateDeployment(context.Background(), deleted.ServiceID, "main")

	put := func(name, value string) string { hs.tickets.m[name] = value; return name }
	cases := map[string]struct {
		path   string // id in the stream URL
		ticket func() string
	}{
		"missing":             {sf.dep.ID, func() string { return "" }},
		"malformed":           {sf.dep.ID, func() string { return "not-a-ticket" }},
		"unknown well-formed": {sf.dep.ID, func() string { return strings.Repeat("A", 43) }},
		"wrong deployment (ticket for A used on B)": {otherDep.ID, sf.ticket},
		"wrong user (ticket value names another user)": {sf.dep.ID, func() string {
			return put("w-user", stranger.UserID+":"+sf.dep.ID)
		}},
		"ticket value without a user":       {sf.dep.ID, func() string { return put("no-user", ":"+sf.dep.ID) }},
		"ticket value with extra parts":     {sf.dep.ID, func() string { return put("extra", sf.user+":"+sf.dep.ID+":x") }},
		"ticket value is only a deployment": {sf.dep.ID, func() string { return put("bare", sf.dep.ID) }},
		"user does not own the deployment":  {sf.dep.ID, func() string { return put("not-owner", stranger.UserID+":"+sf.dep.ID) }},
		"service soft-deleted after issue": {depDeleted.ID, func() string {
			tk := put("deleted", deleted.UserID+":"+depDeleted.ID)
			if err := hs.st.SoftDeleteService(context.Background(), deleted.ServiceID); err != nil {
				t.Fatal(err)
			}
			return tk
		}},
		"malformed id in the URL": {"a%00b", sf.ticket},
		"reused": {sf.dep.ID, func() string {
			tk := sf.ticket()
			_, _ = hs.tickets.Redeem(context.Background(), tk)
			return tk
		}},
	}
	const want = `{"error":"invalid_ticket"}` + "\n"
	for name, c := range cases {
		code, body := sf.get(t, c.path, c.ticket())
		if code != 401 || body != want {
			t.Errorf("%s: %d %q", name, code, body)
		}
	}
	if hs.live.active.Load() != 0 {
		t.Fatalf("a rejected request left %d subscriptions", hs.live.active.Load())
	}
}

// A Redis failure is not "your ticket is bad": 503 with a fixed body, no cause.
func TestStreamTicketStoreFailureIs503(t *testing.T) {
	sf := newStreamFixture(t)
	sf.hs.tickets.redeemErr = errors.New("redeem ticket: dial tcp 10.0.0.9:6379: connect: refused")
	code, body := sf.get(t, sf.dep.ID, strings.Repeat("B", 43))
	if code != 503 || body != `{"error":"unavailable"}`+"\n" {
		t.Fatalf("%d %q", code, body)
	}
}

// A database failure while re-checking ownership is a 500, not a 401 and not a
// stream.
func TestStreamOwnershipStoreErrorIs500(t *testing.T) {
	sf := newStreamFixture(t)
	ticket := sf.ticket()
	closed, err := pgxpool.NewWithConfig(context.Background(), pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	sf.hs.srv.Store = store.New(closed)
	code, body := sf.get(t, sf.dep.ID, ticket)
	if code != 500 || body != `{"error":"internal_error"}`+"\n" {
		t.Fatalf("%d %q", code, body)
	}
}

// A subscribe failure after a good ticket is a 500 and releases everything.
func TestStreamSubscribeFailureIs500(t *testing.T) {
	sf := newStreamFixture(t)
	sf.hs.live.err = errors.New("redis down")
	code, body := sf.get(t, sf.dep.ID, sf.ticket())
	if code != 500 || body != `{"error":"internal_error"}`+"\n" {
		t.Fatalf("%d %q", code, body)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

// The ticket must never reach the logs or any response body, on any path.
func TestStreamTicketNeverLogged(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	sf := newStreamFixture(t, func(d *Deps) {
		d.Log = slog.New(slog.NewTextHandler(lockedWriter{&mu, &buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	hs := sf.hs
	var bodies []string
	try := func(tk string) {
		_, body := sf.get(t, sf.dep.ID, tk)
		bodies = append(bodies, body)
	}
	// success path
	good := sf.ticket()
	resp, cancel := sf.open(t, good)
	(&sseReader{t: t, rd: bufio.NewReader(resp.Body)}).next()
	cancel()
	waitFor(t, "stream to end", func() bool { return hs.live.active.Load() == 0 })
	// reused, unknown and malformed
	try(good)
	try(strings.Repeat("Z", 43))
	try("short")
	// Redis failure whose error text contains the ticket
	leaky := strings.Repeat("Q", 43)
	hs.tickets.redeemErr = errors.New("GETDEL tickets:" + leaky + ": connection refused")
	try(leaky)
	hs.tickets.redeemErr = nil
	// subscribe failure whose error text contains the ticket
	t2 := sf.ticket()
	hs.live.err = errors.New("subscribe failed for " + t2)
	try(t2)
	hs.live.err = nil

	mu.Lock()
	logged := buf.String()
	mu.Unlock()
	if !strings.Contains(logged, "/stream") {
		t.Fatalf("expected the access log to name the path:\n%s", logged)
	}
	for _, tk := range []string{good, leaky, t2, "ticket="} {
		if strings.Contains(logged, tk) {
			t.Errorf("logs contain %q:\n%s", tk, logged)
		}
	}
	for _, b := range bodies {
		for _, tk := range []string{good, leaky, t2} {
			if strings.Contains(b, tk) {
				t.Errorf("response body %q echoes a ticket", b)
			}
		}
	}
}

// Log lines are attacker-controlled: newlines, CRs and fake SSE fields inside a
// message must stay inside the JSON string and never start a new SSE line.
func TestStreamSSEInjectionSafety(t *testing.T) {
	sf := newStreamFixture(t)
	evil := []string{
		"line1\nevent: evil\ndata: {\"x\":1}\n\n",
		"data: fake\r\nevent: log\r\n\r\n",
		"\r\r\n\n: comment\nid: 9\nretry: 1\n",
		"a\u2028b\u2029c\x00d",
	}
	var hist []logs.Entry
	for _, m := range evil {
		hist = append(hist, logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout\nevent: x", Message: m, Timestamp: "t\ndata: y"})
	}
	sf.hs.hot.entries[sf.dep.ID] = hist
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReader(resp.Body)}

	check := func(fr sseFrame, wantEvent string, want []logs.Entry) {
		t.Helper()
		if fr.event != wantEvent || len(fr.lines) != 2 ||
			!strings.HasPrefix(fr.lines[0], "event: ") || !strings.HasPrefix(fr.lines[1], "data: ") {
			t.Fatalf("frame framing broken: %q", fr.lines)
		}
		var got []logs.Entry
		if wantEvent == "history" {
			var h struct{ Logs []logs.Entry }
			if err := json.Unmarshal([]byte(fr.data), &h); err != nil {
				t.Fatal(err)
			}
			got = h.Logs
		} else {
			var e logs.Entry
			if err := json.Unmarshal([]byte(fr.data), &e); err != nil {
				t.Fatal(err)
			}
			got = []logs.Entry{e}
		}
		for i := range want {
			if got[i].Message != want[i].Message {
				t.Fatalf("message %d changed: %q != %q", i, got[i].Message, want[i].Message)
			}
		}
	}
	check(rd.next(), "history", hist)
	for _, m := range evil {
		e := logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout", Message: m, Timestamp: "t"}
		sf.hs.live.ch <- e
		check(rd.next(), "log", []logs.Entry{e})
	}
}

// The history event is bounded: at most historyEntries lines of at most
// maxLogLineBytes each; live lines are cut the same way.
func TestStreamHistoryAndLiveLinesAreBounded(t *testing.T) {
	sf := newStreamFixture(t)
	var hist []logs.Entry
	for range 3 * historyEntries {
		hist = append(hist, logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout", Message: strings.Repeat("x", 3*maxLogLineBytes), Timestamp: "t"})
	}
	sf.hs.hot.entries[sf.dep.ID] = hist
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReaderSize(resp.Body, 1<<20)}
	fr := rd.next()
	var h struct{ Logs []logs.Entry }
	if err := json.Unmarshal([]byte(fr.data), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Logs) != historyEntries {
		t.Fatalf("history has %d entries, want %d", len(h.Logs), historyEntries)
	}
	for _, e := range h.Logs {
		if len(e.Message) > maxLogLineBytes+len(truncatedSuffix) {
			t.Fatalf("history line of %d bytes", len(e.Message))
		}
	}
	sf.hs.live.ch <- logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout", Message: strings.Repeat("z", 5*maxLogLineBytes), Timestamp: "t"}
	var e logs.Entry
	if err := json.Unmarshal([]byte(rd.next().data), &e); err != nil || len(e.Message) > maxLogLineBytes+len(truncatedSuffix) {
		t.Fatalf("live line of %d bytes (%v)", len(e.Message), err)
	}
}

// OBS-1: with more lines than the history holds, the history is the newest
// ones (in order), so live lines continue right after it.
func TestStreamHistoryIsTheNewestLines(t *testing.T) {
	sf := newStreamFixture(t)
	var hist []logs.Entry
	for i := 1; i <= 250; i++ {
		hist = append(hist, logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout", Message: fmt.Sprint(i), Timestamp: "t"})
	}
	sf.hs.hot.entries[sf.dep.ID] = hist
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReaderSize(resp.Body, 1<<20)}
	var h struct{ Logs []logs.Entry }
	if err := json.Unmarshal([]byte(rd.next().data), &h); err != nil {
		t.Fatal(err)
	}
	if len(h.Logs) != historyEntries || h.Logs[0].Message != "151" || h.Logs[historyEntries-1].Message != "250" {
		t.Fatalf("history is %d lines, first %q", len(h.Logs), h.Logs[0].Message)
	}
}

func TestStreamHeartbeat(t *testing.T) {
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.Heartbeat = 20 * time.Millisecond })
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReader(resp.Body)}
	if fr := rd.next(); fr.event != "history" {
		t.Fatal(fr)
	}
	fr := rd.next()
	if len(fr.lines) != 1 || fr.lines[0] != ": ping" {
		t.Fatalf("heartbeat %q", fr.lines)
	}
}

// The handler must stop and release its Redis subscription when the client
// goes away.
func TestStreamDisconnectReleasesSubscription(t *testing.T) {
	sf := newStreamFixture(t)
	resp, cancel := sf.open(t, sf.ticket())
	(&sseReader{t: t, rd: bufio.NewReader(resp.Body)}).next()
	if sf.hs.live.active.Load() != 1 {
		t.Fatalf("active %d", sf.hs.live.active.Load())
	}
	cancel()
	waitFor(t, "subscription release", func() bool { return sf.hs.live.active.Load() == 0 })
}
