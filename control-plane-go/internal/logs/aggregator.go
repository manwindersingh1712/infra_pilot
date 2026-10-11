package logs

import (
	"context"
	"log/slog"
	"time"
)

type HotStore interface {
	Read(ctx context.Context, deploymentID string, count int64) ([]Entry, error)
	ReadLatest(ctx context.Context, deploymentID string, count int64) ([]Entry, error)
	ActiveStreams(ctx context.Context) ([]string, error)
	ReadAfter(ctx context.Context, deploymentID, afterID string, count int64) ([]StreamEntry, error)
	FlushCursor(ctx context.Context, deploymentID string) (string, error)
	SetFlushCursor(ctx context.Context, deploymentID, id string) error
}

type WarmStore interface {
	Insert(ctx context.Context, entries []Entry) error
	Query(ctx context.Context, q Query) ([]Entry, error)
	Count(ctx context.Context, deploymentID string) (int, error)
}

// Aggregator reads from the right tier and moves logs from Redis to ClickHouse.
type Aggregator struct {
	Hot  HotStore
	Warm WarmStore
	Now  func() time.Time
	// BatchSize is the number of entries per ClickHouse insert during Flush;
	// 0 means flushBatchSize.
	BatchSize int64
	// Logger receives one Warn line per failed Flush step; nil means
	// slog.Default().
	Logger *slog.Logger
}

type Result struct {
	Logs       []Entry
	Source     string // "redis" | "clickhouse"
	TotalCount *int
}

const (
	hotWindow      = 10 * time.Minute
	flushBatchSize = 10000
	// maxFlushBatches caps the batches drained per stream in one Flush; the
	// rest is picked up on the next run.
	maxFlushBatches = 20
)

// Query reads from Redis when the range is recent and Redis has data,
// otherwise from ClickHouse, merging in Redis when the range reaches now.
func (a *Aggregator) Query(ctx context.Context, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	cutoff := a.Now().Add(-hotWindow)
	readHot := a.Hot.Read
	if q.Newest {
		readHot = a.Hot.ReadLatest
	}

	if q.Start == nil || q.Start.After(cutoff) {
		hot, err := readHot(ctx, q.DeploymentID, int64(q.Limit))
		if err != nil {
			return Result{}, err
		}
		if len(hot) > 0 {
			return Result{Logs: hot, Source: "redis"}, nil
		}
	}

	warm, err := a.Warm.Query(ctx, q)
	if err != nil {
		return Result{}, err
	}
	if q.Start == nil || (q.End != nil && q.End.After(cutoff)) {
		hot, err := readHot(ctx, q.DeploymentID, int64(q.Limit))
		if err != nil {
			return Result{}, err
		}
		total, err := a.Warm.Count(ctx, q.DeploymentID)
		if err != nil {
			return Result{}, err
		}
		return Result{Logs: mergeTiers(warm, hot, q.Limit), Source: "clickhouse", TotalCount: &total}, nil
	}
	return Result{Logs: warm, Source: "clickhouse"}, nil
}

// mergeTiers appends hot after warm, drops warm entries already in hot (same
// timestamp and message) and keeps the newest `limit`.
func mergeTiers(warm, hot []Entry, limit int) []Entry {
	seen := make(map[string]bool, len(hot))
	for _, h := range hot {
		seen[h.Timestamp+"-"+h.Message] = true
	}
	out := make([]Entry, 0, len(warm)+len(hot))
	for _, w := range warm {
		if !seen[w.Timestamp+"-"+w.Message] {
			out = append(out, w)
		}
	}
	out = append(out, hot...)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (a *Aggregator) ActiveStreamCount(ctx context.Context) (int, error) {
	ids, err := a.Hot.ActiveStreams(ctx)
	return len(ids), err
}

// Flush copies entries added since the last flush of each stream into
// ClickHouse, then advances that stream's cursor. Each stream is drained in
// batches of BatchSize, up to maxFlushBatches per run. An existing cursor is
// re-set before reading so its TTL outlives the stream even when nothing new
// arrives or the insert fails. It returns the number of entries copied and the
// number of streams that failed; each failure is logged with its step.
//
// processed counts entries both inserted and cursored: a SetFlushCursor
// failure after a successful Insert leaves that batch uncounted, and it is
// re-sent next run (the accepted one-batch duplicate).
//
// Not safe for concurrent calls: the cursor does not coordinate runs. Exactly
// one caller, the worker's flush ticker (spec §6.7).
func (a *Aggregator) Flush(ctx context.Context) (processed, errs int) {
	log := a.Logger
	if log == nil {
		log = slog.Default()
	}
	ids, err := a.Hot.ActiveStreams(ctx)
	if err != nil {
		log.WarnContext(ctx, "log flush failed", "step", "activeStreams", "err", err)
		return 0, 1
	}
	size := a.BatchSize
	if size <= 0 {
		size = flushBatchSize
	}
	fail := func(id, step string, err error) {
		errs++
		log.WarnContext(ctx, "log flush failed", "deploymentId", id, "step", step, "err", err)
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			// shutdown: remaining streams are flushed next run.
			return processed, errs
		}
		cursor, err := a.Hot.FlushCursor(ctx, id)
		if err != nil {
			fail(id, "cursor", err)
			continue
		}
		if cursor != "" {
			if err := a.Hot.SetFlushCursor(ctx, id, cursor); err != nil {
				fail(id, "refresh", err)
				continue
			}
		}
		n, step, err := a.drain(ctx, id, cursor, size)
		processed += n
		if err != nil {
			fail(id, step, err)
		}
	}
	return processed, errs
}

// drain copies up to maxFlushBatches batches of one stream, starting after
// cursor. It returns the entries copied before stopping and, on error, the
// step that failed.
func (a *Aggregator) drain(ctx context.Context, id, cursor string, size int64) (int, string, error) {
	processed := 0
	for range maxFlushBatches {
		if err := ctx.Err(); err != nil {
			return processed, "read", err
		}
		batch, err := a.Hot.ReadAfter(ctx, id, cursor, size)
		if err != nil {
			return processed, "read", err
		}
		if len(batch) == 0 {
			return processed, "", nil
		}
		entries := make([]Entry, len(batch))
		for i, se := range batch {
			entries[i] = se.Entry
		}
		if err := a.Warm.Insert(ctx, entries); err != nil {
			return processed, "insert", err
		}
		last := batch[len(batch)-1].ID
		if err := a.Hot.SetFlushCursor(ctx, id, last); err != nil {
			return processed, "setCursor", err
		}
		cursor = last
		processed += len(entries)
		if int64(len(batch)) < size {
			return processed, "", nil
		}
	}
	return processed, "", nil
}
