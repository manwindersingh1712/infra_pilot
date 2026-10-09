package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

func NewRedis(url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Redis{c: redis.NewClient(opt)}, nil
}

func (r *Redis) Close() error                   { return r.c.Close() }
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// Write appends to the deployment's stream and publishes the entry to live viewers.
func (r *Redis) Write(ctx context.Context, e Entry) error {
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
		out = append(out, Entry{
			DeploymentID: deploymentID,
			Source:       fmt.Sprint(m.Values["source"]),
			Message:      fmt.Sprint(m.Values["message"]),
			Timestamp:    fmt.Sprint(m.Values["timestamp"]),
		})
	}
	return out, nil
}

// Subscribe streams live entries for a deployment. Call stop to unsubscribe;
// the channel closes after that or when ctx ends.
func (r *Redis) Subscribe(ctx context.Context, deploymentID string) (<-chan Entry, func(), error) {
	ps := r.c.Subscribe(ctx, channelPrefix+deploymentID)
	if _, err := ps.Receive(ctx); err != nil { // wait until the subscription is active
		_ = ps.Close()
		return nil, nil, err
	}
	out := make(chan Entry, 64)
	go func() {
		defer close(out)
		for msg := range ps.Channel() {
			var e Entry
			if json.Unmarshal([]byte(msg.Payload), &e) != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { _ = ps.Close() }, nil
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
