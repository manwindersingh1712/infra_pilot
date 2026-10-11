package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/auth"
	"infrapilot/internal/logs"
	"infrapilot/internal/metrics"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/tickets"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

type fakePinger struct{ err error }

func (f *fakePinger) Ping(context.Context) error { return f.err }

type fakeHot struct{ entries map[string][]logs.Entry }

func (f *fakeHot) Read(_ context.Context, id string, count int64) ([]logs.Entry, error) {
	e := f.entries[id]
	if int64(len(e)) > count {
		e = e[:count]
	}
	return e, nil
}
func (f *fakeHot) ReadLatest(_ context.Context, id string, count int64) ([]logs.Entry, error) {
	e := f.entries[id]
	if int64(len(e)) > count {
		e = e[int64(len(e))-count:]
	}
	return e, nil
}
func (f *fakeHot) ActiveStreams(context.Context) ([]string, error) {
	ids := []string{}
	for id := range f.entries {
		ids = append(ids, id)
	}
	return ids, nil
}
func (f *fakeHot) ReadAfter(context.Context, string, string, int64) ([]logs.StreamEntry, error) {
	return nil, nil
}
func (f *fakeHot) FlushCursor(context.Context, string) (string, error)  { return "", nil }
func (f *fakeHot) SetFlushCursor(context.Context, string, string) error { return nil }

type emptyWarm struct{}

func (emptyWarm) Insert(context.Context, []logs.Entry) error              { return nil }
func (emptyWarm) Query(context.Context, logs.Query) ([]logs.Entry, error) { return []logs.Entry{}, nil }
func (emptyWarm) Count(context.Context, string) (int, error)              { return 0, nil }

// fakeLive hands every subscriber the same channel and counts open
// subscriptions, so tests can prove the stream handler releases them.
type fakeLive struct {
	ch     chan logs.Entry
	active atomic.Int32 // subscriptions not yet stopped
	err    error        // Subscribe fails with it when set
}

func (f *fakeLive) Subscribe(context.Context, string) (<-chan logs.Entry, func(), error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	f.active.Add(1)
	var once sync.Once
	return f.ch, func() { once.Do(func() { f.active.Add(-1) }) }, nil
}

// fakeTickets is an in-memory single-use ticket store.
type fakeTickets struct {
	mu        sync.Mutex
	m         map[string]string
	n         int
	redeemErr error // Redeem fails with it (not ErrInvalid) when set
}

func (f *fakeTickets) Issue(_ context.Context, v string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	t := "ticket-" + string(rune('a'+f.n))
	f.m[t] = v
	return t, nil
}
func (f *fakeTickets) Redeem(_ context.Context, t string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redeemErr != nil {
		return "", f.redeemErr
	}
	v, ok := f.m[t]
	if !ok {
		return "", tickets.ErrInvalid
	}
	delete(f.m, t)
	return v, nil
}

type harness struct {
	t       *testing.T
	h       http.Handler
	srv     *Server
	st      *store.Store
	signer  *auth.Signer
	box     *secrets.Box
	hot     *fakeHot
	live    *fakeLive
	redis   *fakePinger
	tickets *fakeTickets
	metrics *metrics.API
}

func newHarness(t *testing.T) *harness { return newHarnessWith(t) }

// newHarnessWith builds a harness and lets each option adjust the Deps before
// the handler is constructed (for example a small AuthBurst for limiter tests).
func newHarnessWith(t *testing.T, opts ...func(*Deps)) *harness {
	t.Helper()
	st := store.New(testutil.NeedDB(t, pool))
	box, err := secrets.New(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewSigner("test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hs := &harness{
		t: t, st: st, signer: signer, box: box,
		hot: &fakeHot{entries: map[string][]logs.Entry{}}, live: &fakeLive{ch: make(chan logs.Entry, 8)},
		redis: &fakePinger{}, tickets: &fakeTickets{m: map[string]string{}}, metrics: metrics.NewAPI(),
	}
	deps := Deps{
		Store: st, Redis: hs.redis, Signer: hs.signer, Secrets: box,
		Logs: &logs.Aggregator{Hot: hs.hot, Warm: emptyWarm{}, Now: time.Now},
		Live: hs.live, Tickets: hs.tickets, Metrics: hs.metrics, Log: slog.New(slog.DiscardHandler),
		CORSOrigins: []string{"http://localhost:5173"}, DevLogin: true, AuthBurst: 1000, RefreshBurst: 1000,
	}
	for _, opt := range opts {
		opt(&deps)
	}
	hs.srv, hs.h = buildHandler(deps)
	return hs
}

func (hs *harness) token(userID string) string {
	tok, err := hs.signer.Sign(userID)
	if err != nil {
		hs.t.Fatal(err)
	}
	return tok
}

func (hs *harness) do(method, path, token string, body any) *httptest.ResponseRecorder {
	hs.t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			hs.t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	return hs.doRaw(method, path, token, rdr, nil)
}

// doRaw sends an arbitrary body (malformed JSON, oversize, trailing data) with
// extra request headers (Origin, X-Request-ID, Authorization, ...). A header in
// hdr overrides the defaults, so hdr["Authorization"] beats token.
func (hs *harness) doRaw(method, path, token string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	hs.t.Helper()
	return hs.doFrom("192.0.2.1:1234", method, path, token, body, hdr)
}

// doFrom is doRaw with a chosen RemoteAddr (host:port), for per-IP limiter tests.
func (hs *harness) doFrom(remoteAddr, method, path, token string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	hs.t.Helper()
	if body == nil {
		body = http.NoBody
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	hs.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status %d, want %d; body %s", rec.Code, want, rec.Body.String())
	}
}

func expectBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Body.String() != want+"\n" {
		t.Fatalf("body %q, want %q", rec.Body.String(), want)
	}
}
