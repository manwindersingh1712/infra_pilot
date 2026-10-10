// Package outbox turns OutboxEvent rows into Temporal workflow starts
// (at least once; workflow IDs make repeats harmless).
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"go.temporal.io/api/serviceerror"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"infrapilot/internal/store"
)

const (
	BatchSize   = 25
	MaxAttempts = 10
	// DefaultStartTimeout bounds one Start call so a hung Temporal frontend
	// cannot hold the batch's row locks indefinitely.
	DefaultStartTimeout = 10 * time.Second
)

// Starter starts the work an event describes. workflows.Starter implements it.
type Starter interface {
	Start(ctx context.Context, eventType string, payload []byte) error
}

// BackoffSeconds is 2^attempt capped at 60, plus 0–4s of jitter. A negative
// attempt (corrupt row) counts as 0.
func BackoffSeconds(attempt int, jitter func(n int) int) int {
	return min(60, 1<<min(max(attempt, 0), 6)) + jitter(5)
}

type Publisher struct {
	// StartTimeout bounds each Start call (DefaultStartTimeout from New).
	StartTimeout time.Duration

	store   *store.Store
	starter Starter
	log     *slog.Logger
	jitter  func(int) int
}

func New(st *store.Store, starter Starter, log *slog.Logger) *Publisher {
	return &Publisher{StartTimeout: DefaultStartTimeout, store: st, starter: starter, log: log, jitter: rand.IntN}
}

// Run calls Tick every second until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	p.log.Info("outbox starter running")
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := p.Tick(ctx); err != nil && ctx.Err() == nil {
				p.log.Error("outbox tick failed", "err", err)
			}
		}
	}
}

// Tick claims one batch, starts each event and records the result, all in
// one transaction. It returns how many events were claimed. If ctx is
// cancelled mid-batch, Tick returns ctx's error and the transaction rolls
// back, so the rows stay pending and no attempt is consumed; events already
// started are started again later, which the workflow ID makes harmless.
func (p *Publisher) Tick(ctx context.Context) (int, error) {
	claimed := 0
	err := p.store.InTx(ctx, func(tx *store.Store) error {
		events, err := tx.ClaimOutboxBatch(ctx, BatchSize)
		if err != nil {
			return err
		}
		claimed = len(events)
		for i, e := range events {
			startErr := p.start(ctx, e)
			if startErr == nil {
				p.log.Info("outbox event started", "type", e.Type, "eventId", e.ID)
				if err := tx.MarkOutboxPublished(ctx, e.ID); err != nil {
					return err
				}
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err() // shutdown, not a failed attempt
			}
			attempts := e.Attempts + 1
			// The error text names the deployment but never the payload.
			p.log.Warn("outbox start failed", "type", e.Type, "eventId", e.ID, "attempts", attempts, "err", startErr)
			if attempts >= MaxAttempts || errors.Is(startErr, store.ErrOutboxPermanent) {
				err = tx.MarkOutboxDead(ctx, e.ID, attempts, startErr.Error())
			} else {
				err = tx.MarkOutboxRetry(ctx, e.ID, attempts, startErr.Error(), BackoffSeconds(attempts, p.jitter))
			}
			if err != nil {
				return err
			}
			if transient(startErr) {
				// Temporal is hung or unreachable: every further Start would
				// burn a full StartTimeout while this transaction holds the
				// row locks. Give the rest back untouched; the next tick
				// tries them.
				rest := events[i+1:]
				if len(rest) == 0 {
					return nil
				}
				ids := make([]string, len(rest))
				for j, r := range rest {
					ids[j] = r.ID
				}
				p.log.Warn("outbox start unavailable; deferring the rest of the batch", "deferred", len(ids))
				return tx.RequeueOutbox(ctx, ids)
			}
		}
		return nil
	})
	return claimed, err
}

// transient reports a timeout or connectivity failure: the signal that
// Temporal itself, not this one event, is the problem.
func transient(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// The Temporal client's interceptor turns gRPC errors into serviceerror
	// types, which have neither GRPCStatus nor Unwrap.
	var unavailable *serviceerror.Unavailable
	var deadline *serviceerror.DeadlineExceeded
	if errors.As(err, &unavailable) || errors.As(err, &deadline) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return false
}

func (p *Publisher) start(ctx context.Context, e store.OutboxEvent) error {
	if p.StartTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.StartTimeout)
		defer cancel()
	}
	return p.starter.Start(ctx, e.Type, compactJSON(e.Payload))
}

// compactJSON turns Postgres' jsonb text ({"a": 1}) into compact JSON ({"a":1}).
func compactJSON(raw []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
