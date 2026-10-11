package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeHot struct {
	data      map[string][]Entry
	cursors   map[string]string
	setCalls  []string // every cursor value passed to SetFlushCursor
	activeErr error    // returned by ActiveStreams when set
}

func newFakeHot(data map[string][]Entry) *fakeHot {
	return &fakeHot{data: data, cursors: map[string]string{}}
}

func (f *fakeHot) Read(_ context.Context, id string, count int64) ([]Entry, error) {
	e := f.data[id]
	if int64(len(e)) > count {
		e = e[:count]
	}
	return e, nil
}

func (f *fakeHot) ReadLatest(_ context.Context, id string, count int64) ([]Entry, error) {
	e := f.data[id]
	if int64(len(e)) > count {
		e = e[int64(len(e))-count:]
	}
	return e, nil
}

func (f *fakeHot) ActiveStreams(context.Context) ([]string, error) {
	if f.activeErr != nil {
		return nil, f.activeErr
	}
	ids := []string{}
	for id := range f.data {
		ids = append(ids, id)
	}
	return ids, nil
}

// Stream IDs are "1", "2", ... by position.
func (f *fakeHot) ReadAfter(_ context.Context, id, after string, count int64) ([]StreamEntry, error) {
	var out []StreamEntry
	for i, e := range f.data[id] {
		sid := fmt.Sprint(i + 1)
		if after != "" && i+1 <= mustAtoi(after) {
			continue
		}
		if int64(len(out)) == count {
			break
		}
		out = append(out, StreamEntry{ID: sid, Entry: e})
	}
	return out, nil
}

func (f *fakeHot) FlushCursor(_ context.Context, id string) (string, error) {
	return f.cursors[id], nil
}
func (f *fakeHot) SetFlushCursor(_ context.Context, id, c string) error {
	f.setCalls = append(f.setCalls, c)
	f.cursors[id] = c
	return nil
}

func mustAtoi(s string) int {
	var n int
	_, _ = fmt.Sscan(s, &n)
	return n
}

type fakeWarm struct {
	rows      []Entry
	inserted  []Entry
	sizes     []int // batch size of each Insert call
	insertErr error // returned by every Insert when set
	failOn    int   // 1-based Insert call that fails with insertErr or a default error
	lastQ     Query
}

func (f *fakeWarm) Insert(_ context.Context, e []Entry) error {
	f.sizes = append(f.sizes, len(e))
	if f.failOn > 0 {
		if len(f.sizes) == f.failOn {
			if f.insertErr != nil {
				return f.insertErr
			}
			return errors.New("insert failed")
		}
	} else if f.insertErr != nil {
		return f.insertErr
	}
	f.inserted = append(f.inserted, e...)
	return nil
}
func (f *fakeWarm) Query(_ context.Context, q Query) ([]Entry, error) {
	f.lastQ = q
	return f.rows, nil
}
func (f *fakeWarm) Count(context.Context, string) (int, error) { return len(f.rows) + 40, nil }

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func entry(msg string) Entry {
	return Entry{DeploymentID: "d", Source: "stdout", Message: msg, Timestamp: "2026-01-01T11:59:00.000Z"}
}

func TestQueryServesRecentLogsFromRedis(t *testing.T) {
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{"d": {entry("a"), entry("b")}}), Warm: &fakeWarm{}, Now: func() time.Time { return now }}
	res, err := a.Query(context.Background(), Query{DeploymentID: "d", Limit: 100})
	if err != nil || res.Source != "redis" || len(res.Logs) != 2 || res.TotalCount != nil {
		t.Fatalf("got %+v err %v", res, err)
	}
}

func TestQueryFallsBackToClickHouseAndMerges(t *testing.T) {
	warm := &fakeWarm{rows: []Entry{entry("old"), entry("dup")}}
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{}), Warm: warm, Now: func() time.Time { return now }}
	res, err := a.Query(context.Background(), Query{DeploymentID: "d"})
	if err != nil || res.Source != "clickhouse" || len(res.Logs) != 2 || res.TotalCount == nil || *res.TotalCount != 42 {
		t.Fatalf("got %+v err %v", res, err)
	}
	if warm.lastQ.Limit != 100 {
		t.Fatalf("limit default not applied: %+v", warm.lastQ)
	}
}

func TestQueryOldRangeSkipsRedis(t *testing.T) {
	old := now.Add(-time.Hour)
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{"d": {entry("hot")}}), Warm: &fakeWarm{rows: []Entry{entry("warm")}}, Now: func() time.Time { return now }}
	res, _ := a.Query(context.Background(), Query{DeploymentID: "d", Start: &old})
	if res.Source != "clickhouse" || len(res.Logs) != 1 || res.Logs[0].Message != "warm" || res.TotalCount != nil {
		t.Fatalf("got %+v", res)
	}
}

func TestMergeDropsDuplicatesAndKeepsNewestLimit(t *testing.T) {
	merged := mergeTiers([]Entry{entry("1"), entry("2")}, []Entry{entry("2"), entry("3")}, 2)
	if len(merged) != 2 || merged[0].Message != "2" || merged[1].Message != "3" {
		t.Fatalf("got %+v", merged)
	}
}

func TestFlushOnlyCopiesNewEntries(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2")}})
	warm := &fakeWarm{}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now}

	if processed, errs := a.Flush(context.Background()); processed != 2 || errs != 0 {
		t.Fatalf("first flush processed=%d errs=%d", processed, errs)
	}
	hot.data["d"] = append(hot.data["d"], entry("3"))
	if processed, _ := a.Flush(context.Background()); processed != 1 {
		t.Fatalf("second flush processed=%d, want 1", processed)
	}
	if processed, _ := a.Flush(context.Background()); processed != 0 {
		t.Fatalf("third flush processed=%d, want 0", processed)
	}
	if len(warm.inserted) != 3 || hot.cursors["d"] != "3" {
		t.Fatalf("inserted=%d cursor=%q", len(warm.inserted), hot.cursors["d"])
	}
}

func TestFlushDrainsInBatches(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2"), entry("3"), entry("4"), entry("5")}})
	warm := &fakeWarm{}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now, BatchSize: 2}

	processed, errs := a.Flush(context.Background())
	if processed != 5 || errs != 0 {
		t.Fatalf("processed=%d errs=%d", processed, errs)
	}
	if fmt.Sprint(warm.sizes) != "[2 2 1]" || hot.cursors["d"] != "5" {
		t.Fatalf("insert sizes=%v cursor=%q", warm.sizes, hot.cursors["d"])
	}
	if processed, errs := a.Flush(context.Background()); processed != 0 || errs != 0 || len(warm.sizes) != 3 {
		t.Fatalf("second flush processed=%d errs=%d sizes=%v", processed, errs, warm.sizes)
	}
}

func TestFlushStopsAfterMaxBatches(t *testing.T) {
	var many []Entry
	for i := range maxFlushBatches + 5 {
		many = append(many, entry(fmt.Sprint(i)))
	}
	hot := newFakeHot(map[string][]Entry{"d": many})
	warm := &fakeWarm{}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now, BatchSize: 1}

	if processed, errs := a.Flush(context.Background()); processed != maxFlushBatches || errs != 0 {
		t.Fatalf("processed=%d errs=%d, want %d", processed, errs, maxFlushBatches)
	}
	if processed, _ := a.Flush(context.Background()); processed != 5 {
		t.Fatalf("leftover processed=%d, want 5", processed)
	}
}

func TestFlushRefreshesCursorOnInsertFailure(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2"), entry("3")}})
	hot.cursors["d"] = "1"
	warm := &fakeWarm{insertErr: errors.New("clickhouse down")}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now}

	processed, errs := a.Flush(context.Background())
	if processed != 0 || errs != 1 {
		t.Fatalf("processed=%d errs=%d", processed, errs)
	}
	if fmt.Sprint(hot.setCalls) != "[1]" || hot.cursors["d"] != "1" || len(warm.sizes) != 1 {
		t.Fatalf("setCalls=%v cursor=%q inserts=%v", hot.setCalls, hot.cursors["d"], warm.sizes)
	}
}

func TestFlushRefreshesCursorWhenIdle(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2")}})
	hot.cursors["d"] = "2"
	warm := &fakeWarm{}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now}

	processed, errs := a.Flush(context.Background())
	if processed != 0 || errs != 0 {
		t.Fatalf("processed=%d errs=%d", processed, errs)
	}
	if fmt.Sprint(hot.setCalls) != "[2]" || len(warm.sizes) != 0 {
		t.Fatalf("setCalls=%v inserts=%v", hot.setCalls, warm.sizes)
	}
}

func TestFlushLogsFailedInsertWithDeploymentID(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2"), entry("3"), entry("4"), entry("5")}})
	warm := &fakeWarm{failOn: 2, insertErr: errors.New("clickhouse down")}
	var buf bytes.Buffer
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now, BatchSize: 2, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	processed, errs := a.Flush(context.Background())
	if processed != 2 || errs != 1 || hot.cursors["d"] != "2" {
		t.Fatalf("processed=%d errs=%d cursor=%q", processed, errs, hot.cursors["d"])
	}
	out := buf.String()
	for _, want := range []string{`"msg":"log flush failed"`, `"level":"WARN"`, `"deploymentId":"d"`, `"step":"insert"`, `clickhouse down`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %s: %s", want, out)
		}
	}
}

func TestFlushLogsActiveStreamsError(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1")}})
	hot.activeErr = errors.New("redis down")
	var buf bytes.Buffer
	a := &Aggregator{Hot: hot, Warm: &fakeWarm{}, Now: time.Now, Logger: slog.New(slog.NewJSONHandler(&buf, nil))}

	if processed, errs := a.Flush(context.Background()); processed != 0 || errs != 1 {
		t.Fatalf("processed=%d errs=%d", processed, errs)
	}
	out := buf.String()
	for _, want := range []string{`"msg":"log flush failed"`, `"step":"activeStreams"`, `redis down`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %s: %s", want, out)
		}
	}
	if strings.Contains(out, "deploymentId") {
		t.Fatalf("activeStreams log has deploymentId: %s", out)
	}
}

// OBS-1: Newest asks the hot tier for the newest lines (SSE history); the
// default stays oldest-first for the HTTP pagination.
func TestQueryNewestReadsLatestHotLines(t *testing.T) {
	var data []Entry
	for i := 1; i <= 5; i++ {
		data = append(data, Entry{DeploymentID: "d", Message: string(rune('a' + i - 1)), Timestamp: "t"})
	}
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{"d": data}), Now: time.Now}
	res, err := a.Query(context.Background(), Query{DeploymentID: "d", Limit: 2, Newest: true})
	if err != nil || len(res.Logs) != 2 || res.Logs[0].Message != "d" || res.Logs[1].Message != "e" || res.Source != "redis" {
		t.Fatalf("newest: %+v %v", res, err)
	}
	res, err = a.Query(context.Background(), Query{DeploymentID: "d", Limit: 2})
	if err != nil || len(res.Logs) != 2 || res.Logs[0].Message != "a" {
		t.Fatalf("default: %+v %v", res, err)
	}
}
