package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/worker/outbox"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

type started struct{ Type, Payload string }

type fakeStarter struct {
	mu    sync.Mutex
	got   []started
	fail  error
	delay time.Duration
	// failType makes Start fail for one event type only (a poison row).
	failType string
	// block, if set, makes Start wait for ctx cancellation and return its error.
	block bool
	calls int
}

func (f *fakeStarter) Start(ctx context.Context, eventType string, payload []byte) error {
	f.mu.Lock()
	f.calls++
	block := f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.fail != nil {
		return f.fail
	}
	if f.failType != "" && eventType == f.failType {
		return errors.New("outbox: unknown event type")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, started{eventType, string(payload)})
	return nil
}

func (f *fakeStarter) all() []started {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]started(nil), f.got...)
}

func zero(int) int { return 0 }

// create queues an event that is already due. "nextAttemptAt" is a
// timestamp(3), so a freshly inserted CURRENT_TIMESTAMP can round up to a
// moment after the next now() and make an immediate Tick miss the row.
func create(t *testing.T, st *store.Store, eventType string, payload any) string {
	t.Helper()
	id, err := st.CreateOutboxEvent(context.Background(), eventType, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE "OutboxEvent" SET "nextAttemptAt" = now() - interval '1 second' WHERE "id" = $1`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestBackoffSeconds(t *testing.T) {
	want := map[int]int{1: 2, 2: 4, 3: 8, 4: 16, 5: 32, 6: 60, 7: 60, 9: 60}
	for attempt, w := range want {
		if got := outbox.BackoffSeconds(attempt, zero); got != w {
			t.Errorf("BackoffSeconds(%d) = %d, want %d", attempt, got, w)
		}
	}
	if got := outbox.BackoffSeconds(1, func(n int) int { return n - 1 }); got != 6 {
		t.Errorf("jitter must add 0-4s, got %d", got)
	}
}

func status(t *testing.T, id string) (string, int) {
	t.Helper()
	var s string
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT "status"::text, "attempts" FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&s, &attempts); err != nil {
		t.Fatal(err)
	}
	return s, attempts
}

func setup(t *testing.T) (*store.Store, *fakeStarter, *outbox.Publisher) {
	st := store.New(testutil.NeedDB(t, pool))
	fs := &fakeStarter{}
	return st, fs, outbox.New(st, fs, slog.New(slog.DiscardHandler))
}

func TestTickStartsPendingEvents(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id := create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "d1"})

	n, err := p.Tick(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := fs.all()
	if len(got) != 1 || got[0].Type != store.EventBuildRequested || got[0].Payload != `{"deploymentId":"d1"}` {
		t.Fatalf("started %+v", got)
	}
	if s, _ := status(t, id); s != "published" {
		t.Fatalf("status = %s", s)
	}
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatalf("second tick processed %d", n)
	}
}

func TestTickRetriesThenDies(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id := create(t, st, store.EventDeployRequested, map[string]string{"deploymentId": "d1"})
	fs.fail = errors.New("temporal unavailable")

	p.Tick(ctx)
	if s, attempts := status(t, id); s != "pending" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
	var lastErr string
	if err := pool.QueryRow(ctx, `SELECT "lastError" FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&lastErr); err != nil || lastErr != "temporal unavailable" {
		t.Fatalf("lastError=%q err=%v", lastErr, err)
	}
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatal("event must wait for its backoff")
	}

	pool.Exec(ctx, `UPDATE "OutboxEvent" SET "attempts" = $2, "nextAttemptAt" = now() - interval '1 second' WHERE "id" = $1`, id, outbox.MaxAttempts-1)
	p.Tick(ctx)
	if s, attempts := status(t, id); s != "dead" || attempts != outbox.MaxAttempts {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatal("a dead event must never be claimed again")
	}
}

func TestBackoffGrowsAcrossAttempts(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id := create(t, st, store.EventDeployRequested, map[string]string{"deploymentId": "d1"})
	fs.fail = errors.New("down")
	// attempt 1 -> 2s, attempt 2 -> 4s (jitter adds up to 4s more).
	p.Tick(ctx)
	var d1 float64
	pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM ("nextAttemptAt" - (now() AT TIME ZONE 'UTC')))::float8 FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&d1)
	if d1 < 0 || d1 > 7 {
		t.Fatalf("first retry delay %.1fs, want about 2-6s", d1)
	}
	pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() - interval '1 second' WHERE "id" = $1`, id)
	p.Tick(ctx)
	if s, attempts := status(t, id); s != "pending" || attempts != 2 {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
}

func TestPoisonRowDoesNotWedgeTheLoop(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	fs.failType = "teardown.requested"
	bad := create(t, st, "teardown.requested", map[string]string{"deploymentId": "d1"})
	good := create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "d2"})

	n, err := p.Tick(ctx)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if s, _ := status(t, good); s != "published" {
		t.Fatalf("good event behind a poison row: %s", s)
	}
	if s, attempts := status(t, bad); s != "pending" || attempts != 1 {
		t.Fatalf("poison: status=%s attempts=%d", s, attempts)
	}
	// The poison row keeps failing and ends up dead without ever blocking others.
	for range outbox.MaxAttempts {
		pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() - interval '1 second' WHERE "id" = $1`, bad)
		if _, err := p.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if s, _ := status(t, bad); s != "dead" {
		t.Fatalf("poison row status = %s", s)
	}
}

func TestStartTimeoutDoesNotHoldTheBatch(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	fs.block = true
	p.StartTimeout = 50 * time.Millisecond
	id := create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "d1"})

	done := make(chan error, 1)
	go func() { _, err := p.Tick(ctx); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a hung Start held the batch past its timeout")
	}
	if s, attempts := status(t, id); s != "pending" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
}

func TestHungStartStopsTheBatchAndRequeuesTheRest(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	fs.block = true
	p.StartTimeout = 200 * time.Millisecond
	var ids []string
	for i := range outbox.BatchSize {
		ids = append(ids, create(t, st, store.EventBuildRequested, map[string]int{"n": i}))
	}

	begin := time.Now()
	n, err := p.Tick(ctx)
	elapsed := time.Since(begin)
	if err != nil || n != outbox.BatchSize {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("tick took %v with a hung Start; want about one StartTimeout", elapsed)
	}
	fs.mu.Lock()
	calls := fs.calls
	fs.mu.Unlock()
	if calls != 1 {
		t.Fatalf("Start called %d times, want 1", calls)
	}
	burned := 0
	for _, id := range ids {
		s, attempts := status(t, id)
		if s != "pending" {
			t.Fatalf("status = %s, want pending (never committed as publishing)", s)
		}
		burned += attempts
	}
	if burned != 1 {
		t.Fatalf("%d attempts burned, want exactly 1", burned)
	}

	// Temporal recovers: the requeued rows are due on the next tick.
	fs.mu.Lock()
	fs.block = false
	fs.mu.Unlock()
	n, err = p.Tick(ctx)
	if err != nil || n != outbox.BatchSize-1 || len(fs.all()) != outbox.BatchSize-1 {
		t.Fatalf("recovery tick: n=%d err=%v started=%d", n, err, len(fs.all()))
	}
}

func TestCancelMidBatchLeavesRowsClaimable(t *testing.T) {
	st, fs, p := setup(t)
	bg := context.Background()
	fs.block = true
	var ids []string
	for i := range 3 {
		id := create(t, st, store.EventBuildRequested, map[string]int{"n": i})
		ids = append(ids, id)
	}
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { _, err := p.Tick(ctx); done <- err }()
	time.Sleep(300 * time.Millisecond) // the batch is claimed and Start is blocked
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled tick must report the cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tick did not return after cancellation")
	}
	for _, id := range ids {
		if s, attempts := status(t, id); s != "pending" || attempts != 0 {
			t.Fatalf("after shutdown: status=%s attempts=%d (shutdown must not burn attempts)", s, attempts)
		}
	}
	// A fresh publisher claims them all.
	fs2 := &fakeStarter{}
	n, err := outbox.New(st, fs2, slog.New(slog.DiscardHandler)).Tick(bg)
	if err != nil || n != 3 || len(fs2.all()) != 3 {
		t.Fatalf("n=%d err=%v started=%d", n, err, len(fs2.all()))
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	_, _, p := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestRunStartsEventsUntilCancelled(t *testing.T) {
	st, fs, p := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	id := create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "d1"})
	go p.Run(ctx)
	deadline := time.Now().Add(15 * time.Second)
	for len(fs.all()) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if s, _ := status(t, id); s != "published" {
		t.Fatalf("status = %s", s)
	}
}

func TestConcurrentPublishersStartEachEventOnce(t *testing.T) {
	st, fs, _ := setup(t)
	fs.delay = 2 * time.Millisecond // keep the claims overlapping
	ctx := context.Background()
	for i := range 60 {
		create(t, st, store.EventBuildRequested, map[string]int{"n": i})
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := outbox.New(st, fs, slog.New(slog.DiscardHandler))
			for range 5 {
				if _, err := p.Tick(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, s := range fs.all() {
		if seen[s.Payload] {
			t.Fatalf("event %s started twice", s.Payload)
		}
		seen[s.Payload] = true
	}
	if len(seen) != 60 {
		t.Fatalf("started %d distinct events, want 60", len(seen))
	}
	counts, err := st.CountOutboxByStatus(ctx)
	if err != nil || counts["published"] != 60 || counts["pending"] != 0 || counts["publishing"] != 0 {
		t.Fatalf("counts=%v err=%v", counts, err)
	}
}

func TestOutboxMetricsFollowTicks(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id := create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "d1"})
	pool.Exec(ctx, `UPDATE "OutboxEvent" SET "createdAt" = (now() AT TIME ZONE 'UTC') - interval '90 seconds' WHERE "id" = $1`, id)

	fs.fail = errors.New("down")
	p.Tick(ctx)
	counts, _ := st.CountOutboxByStatus(ctx)
	age, err := st.OldestUnpublishedOutboxAge(ctx)
	if err != nil || counts["pending"] != 1 || age < 85*time.Second {
		t.Fatalf("while failing: counts=%v age=%v err=%v", counts, age, err)
	}

	fs.fail = nil
	pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() - interval '1 second' WHERE "id" = $1`, id)
	p.Tick(ctx)
	counts, _ = st.CountOutboxByStatus(ctx)
	age, _ = st.OldestUnpublishedOutboxAge(ctx)
	if counts["published"] != 1 || counts["pending"] != 0 || age != 0 {
		t.Fatalf("after publish: counts=%v age=%v", counts, age)
	}
}

func TestLogsNeverContainPayloadSecrets(t *testing.T) {
	st := store.New(testutil.NeedDB(t, pool))
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx := context.Background()
	create(t, st, store.EventBuildRequested, map[string]string{"deploymentId": "ok1", "token": "hunter2-secret"})
	create(t, st, store.EventDeployRequested, map[string]string{"deploymentId": "bad1", "token": "hunter2-secret"})

	fs := &fakeStarter{}
	p := outbox.New(st, starterFunc(func(ctx context.Context, typ string, payload []byte) error {
		if strings.Contains(string(payload), "bad1") {
			return errors.New("frontend unavailable")
		}
		return fs.Start(ctx, typ, payload)
	}), log)
	if n, err := p.Tick(ctx); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if out := buf.String(); strings.Contains(out, "hunter2-secret") || strings.Contains(out, `"token"`) {
		t.Fatalf("payload leaked into logs:\n%s", out)
	}
	if !strings.Contains(buf.String(), "eventId") {
		t.Fatal("expected the logs to carry the event id")
	}
}

type starterFunc func(ctx context.Context, eventType string, payload []byte) error

func (f starterFunc) Start(ctx context.Context, eventType string, payload []byte) error {
	return f(ctx, eventType, payload)
}
