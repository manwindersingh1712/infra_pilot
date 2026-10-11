package api

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"infrapilot/internal/logs"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestStreamReleasesSlotOnDisconnect(t *testing.T) {
	sf := newStreamFixture(t)
	resp, cancel := sf.open(t, sf.ticket())
	(&sseReader{t: t, rd: bufio.NewReader(resp.Body)}).next()
	if sf.hs.srv.streams.total() != 1 {
		t.Fatalf("slots %d", sf.hs.srv.streams.total())
	}
	cancel()
	waitFor(t, "slot release", func() bool { return sf.hs.srv.streams.total() == 0 })
	waitFor(t, "subscription release", func() bool { return sf.hs.live.active.Load() == 0 })
}

func TestStreamSlotReleasedWhenStartFails(t *testing.T) {
	sf := newStreamFixture(t)
	sf.hs.live.err = io.ErrClosedPipe
	if code, _ := sf.get(t, sf.dep.ID, sf.ticket()); code != 500 {
		t.Fatalf("status %d", code)
	}
	if sf.hs.srv.streams.total() != 0 {
		t.Fatal("slot leaked after a failed start")
	}
}

func TestStreamServerShutdownEndsStreams(t *testing.T) {
	shutdown, stop := context.WithCancel(context.Background())
	defer stop()
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.Shutdown = shutdown })
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReader(resp.Body)}
	rd.next()
	stop()
	if fr := rd.next(); fr.event != "end" || !strings.Contains(fr.data, `"reason":"shutdown"`) {
		t.Fatalf("end frame %v", fr)
	}
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("stream did not close cleanly: %v", err)
	}
	waitFor(t, "release", func() bool { return sf.hs.live.active.Load() == 0 && sf.hs.srv.streams.total() == 0 })
	// A new stream after shutdown began is refused and keeps its ticket.
	tk := sf.ticket()
	if code, body := sf.get(t, sf.dep.ID, tk); code != 503 || body != `{"error":"unavailable"}`+"\n" {
		t.Fatalf("stream after shutdown: %d %q", code, body)
	}
	if _, ok := sf.hs.tickets.m[tk]; !ok {
		t.Fatal("a refused stream burned the ticket")
	}
}

func TestStreamMaxDuration(t *testing.T) {
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.MaxDuration = 150 * time.Millisecond })
	resp, _ := sf.open(t, sf.ticket())
	rd := &sseReader{t: t, rd: bufio.NewReader(resp.Body)}
	rd.next()
	if fr := rd.next(); fr.event != "end" || !strings.Contains(fr.data, `"reason":"max_duration"`) {
		t.Fatalf("end frame %v", fr)
	}
	waitFor(t, "release", func() bool { return sf.hs.live.active.Load() == 0 && sf.hs.srv.streams.total() == 0 })
}

func (sf *streamFixture) expect429(t *testing.T, depID string) {
	t.Helper()
	tk := decode[map[string]string](t, sf.hs.do("POST", "/logs/deployments/"+depID+"/stream-ticket", sf.tok, nil))["ticket"]
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) // a wrongly opened stream never ends
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", sf.streamURL(depID, tk), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != 429 || string(b) != `{"error":"too_many_streams"}`+"\n" || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("want 429, got %d %q", resp.StatusCode, b)
	}
}

func TestStreamPerDeploymentCap(t *testing.T) {
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.MaxPerUser = 10; d.Stream.MaxPerDeployment = 2 })
	second, _ := sf.hs.st.CreateDeployment(context.Background(), sf.f.ServiceID, "v2")
	var cancels []context.CancelFunc
	for range 2 {
		resp, cancel := sf.open(t, sf.ticket())
		(&sseReader{t: t, rd: bufio.NewReader(resp.Body)}).next()
		cancels = append(cancels, cancel)
	}
	sf.expect429(t, sf.dep.ID)
	// Another deployment of the same user is not affected.
	tk := decode[map[string]string](t, sf.hs.do("POST", "/logs/deployments/"+second.ID+"/stream-ticket", sf.tok, nil))["ticket"]
	if code, _ := sf.getOpen(t, second.ID, tk); code != 200 {
		t.Fatalf("other deployment: %d", code)
	}
	// Closing one frees a slot on the first.
	cancels[0]()
	waitFor(t, "slot", func() bool { return sf.hs.srv.streams.forDeployment(sf.dep.ID) == 1 })
	resp, _ := sf.open(t, sf.ticket())
	if resp.StatusCode != 200 {
		t.Fatalf("after release: %d", resp.StatusCode)
	}
}

func TestStreamPerUserCap(t *testing.T) {
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.MaxPerUser = 2; d.Stream.MaxPerDeployment = 10 })
	hs := sf.hs
	second, _ := hs.st.CreateDeployment(context.Background(), sf.f.ServiceID, "v2")
	third, _ := hs.st.CreateDeployment(context.Background(), sf.f.ServiceID, "v3")
	for _, id := range []string{sf.dep.ID, second.ID} {
		tk := decode[map[string]string](t, hs.do("POST", "/logs/deployments/"+id+"/stream-ticket", sf.tok, nil))["ticket"]
		if code, _ := sf.getOpen(t, id, tk); code != 200 {
			t.Fatalf("stream on %s: %d", id, code)
		}
	}
	sf.expect429(t, third.ID)
	// Another user is unaffected by this user's cap.
	other := testutil.Seed(t, hs.st, store.TypeNodejs)
	od, _ := hs.st.CreateDeployment(context.Background(), other.ServiceID, "main")
	otk := decode[map[string]string](t, hs.do("POST", "/logs/deployments/"+od.ID+"/stream-ticket", hs.token(other.UserID), nil))["ticket"]
	if code, _ := sf.getOpen(t, od.ID, otk); code != 200 {
		t.Fatalf("other user: %d", code)
	}
}

// getOpen opens a stream that stays open until the test ends and returns its
// status and first frame.
func (sf *streamFixture) getOpen(t *testing.T, depID, ticket string) (int, sseFrame) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", sf.streamURL(depID, ticket), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		return resp.StatusCode, sseFrame{}
	}
	return resp.StatusCode, (&sseReader{t: t, rd: bufio.NewReader(resp.Body)}).next()
}

// The global server WriteTimeout is not what stops a stalled SSE client: each
// write sets its own deadline, so a client that stops reading is dropped.
func TestStreamWriteDeadlineDropsStalledClient(t *testing.T) {
	sf := newStreamFixture(t, func(d *Deps) { d.Stream.WriteTimeout = 200 * time.Millisecond })
	tk := sf.ticket()
	u, _ := url.Parse(sf.srv.URL)
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096) // keep the client's receive window small
	}
	_, _ = conn.Write([]byte("GET /logs/deployments/" + sf.dep.ID + "/stream?ticket=" + tk + " HTTP/1.1\r\nHost: x\r\n\r\n"))
	waitFor(t, "stream start", func() bool { return sf.hs.live.active.Load() == 1 })

	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() { // flood; the client never reads
		big := strings.Repeat("y", maxLogLineBytes)
		for {
			select {
			case sf.hs.live.ch <- logs.Entry{DeploymentID: sf.dep.ID, Source: "stdout", Message: big, Timestamp: "t"}:
			case <-done:
				return
			}
		}
	}()
	waitFor(t, "stalled client to be dropped", func() bool {
		return sf.hs.live.active.Load() == 0 && sf.hs.srv.streams.total() == 0
	})
}
