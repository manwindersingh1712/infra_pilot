package logs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	streamPrefix  = "logs:stream:"
	channelPrefix = "logs:pubsub:"
	streamMaxLen  = 10000
	streamTTL     = 10 * time.Minute
)

type Redis struct{ c *redis.Client }

func NewRedis(rawURL string) (*Redis, error) {
	opt, err := redis.ParseURL(rawURL)
	if err != nil {
		// A *url.Error embeds the raw URL (password included), and its inner
		// error can echo part of the userinfo (e.g. a bad %-escape), so neither
		// may reach logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, errors.New("parse REDIS_URL: invalid URL")
		}
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Redis{c: redis.NewClient(opt)}, nil
}

func (r *Redis) Close() error                   { return r.c.Close() }
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// Write appends to the deployment's stream and publishes the entry to live viewers.
// An empty Timestamp defaults to now here, so every reader sees one value.
func (r *Redis) Write(ctx context.Context, e Entry) error {
	if e.Timestamp == "" {
		e.Timestamp = FormatTime(time.Now())
	}
	key := streamPrefix + e.DeploymentID
	err := r.c.XAdd(ctx, &redis.XAddArgs{
		Stream: key, MaxLen: streamMaxLen, Approx: true,
		Values: []any{"source", e.Source, "message", e.Message, "timestamp", e.Timestamp},
	}).Err()
	if err != nil {
		return err
	}
	if err := r.c.PExpire(ctx, key, streamTTL).Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.c.Publish(ctx, channelPrefix+e.DeploymentID, payload).Err()
}

// Read returns up to count entries from the start of the stream (oldest first).
func (r *Redis) Read(ctx context.Context, deploymentID string, count int64) ([]Entry, error) {
	msgs, err := r.c.XRangeN(ctx, streamPrefix+deploymentID, "-", "+", count).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, toEntry(deploymentID, m))
	}
	return out, nil
}

// toEntry converts a stream message to an Entry. Missing fields read as ""
// (not "<nil>"), and a missing timestamp defaults to now, as in Node's readLogs.
func toEntry(deploymentID string, m redis.XMessage) Entry {
	ts := field(m.Values, "timestamp")
	if ts == "" {
		ts = FormatTime(time.Now())
	}
	return Entry{
		DeploymentID: deploymentID,
		Source:       field(m.Values, "source"),
		Message:      field(m.Values, "message"),
		Timestamp:    ts,
	}
}

func field(v map[string]any, k string) string { s, _ := v[k].(string); return s }

// Subscribe streams live entries for a deployment. The channel closes after
// stop is called or ctx ends; either way the pub/sub connection is released.
// stop is idempotent and safe to call after ctx has ended.
func (r *Redis) Subscribe(ctx context.Context, deploymentID string) (<-chan Entry, func(), error) {
	ps := r.c.Subscribe(ctx, channelPrefix+deploymentID)
	if _, err := ps.Receive(ctx); err != nil { // wait until the subscription is active
		_ = ps.Close()
		return nil, nil, err
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			_ = ps.Close()
		})
	}
	msgs := ps.Channel()
	out := make(chan Entry, 64)
	go func() {
		defer close(out)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case msg, ok := <-msgs:
				if !ok {
					return
				}
				var e Entry
				if json.Unmarshal([]byte(msg.Payload), &e) != nil {
					continue
				}
				select {
				case out <- e:
				case <-ctx.Done():
					return
				case <-done:
					return
				}
			}
		}
	}()
	return out, stop, nil
}

// ActiveStreams lists deployment IDs that still have a hot log stream.
func (r *Redis) ActiveStreams(ctx context.Context) ([]string, error) {
	var ids []string
	iter := r.c.Scan(ctx, 0, streamPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		ids = append(ids, strings.TrimPrefix(iter.Val(), streamPrefix))
	}
	return ids, iter.Err()
}

const (
	cursorPrefix = "logs:flushed:"
	cursorTTL    = 15 * time.Minute // longer than streamTTL; Flush refreshes it on every pass while the stream exists
)

// StreamEntry is a log entry with its Redis stream ID.
type StreamEntry struct {
	ID string
	Entry
}

// ReadAfter returns up to count entries with IDs greater than afterID
// (all entries when afterID is empty), oldest first.
func (r *Redis) ReadAfter(ctx context.Context, deploymentID, afterID string, count int64) ([]StreamEntry, error) {
	start := "-"
	if afterID != "" {
		start = "(" + afterID // exclusive range, Redis 6.2+
	}
	msgs, err := r.c.XRangeN(ctx, streamPrefix+deploymentID, start, "+", count).Result()
	if err != nil {
		return nil, err
	}
	out := make([]StreamEntry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, StreamEntry{ID: m.ID, Entry: toEntry(deploymentID, m)})
	}
	return out, nil
}

// FlushCursor returns the last stream ID copied to ClickHouse, or "".
func (r *Redis) FlushCursor(ctx context.Context, deploymentID string) (string, error) {
	v, err := r.c.Get(ctx, cursorPrefix+deploymentID).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return v, err
}

func (r *Redis) SetFlushCursor(ctx context.Context, deploymentID, id string) error {
	return r.c.Set(ctx, cursorPrefix+deploymentID, id, cursorTTL).Err()
}
