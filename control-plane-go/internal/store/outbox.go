package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

// CreateOutboxEvent queues an event for the outbox publisher. Call it inside
// InTx together with the state change it announces.
func (s *Store) CreateOutboxEvent(ctx context.Context, eventType string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := cuid.New()
	_, err = s.db.Exec(ctx,
		`INSERT INTO "OutboxEvent" ("id", "type", "payload") VALUES ($1, $2, $3::jsonb)`,
		id, eventType, string(body))
	return id, err
}

// ClaimOutboxBatch locks up to limit due events and marks them publishing.
// It must run inside InTx so the row locks last until the batch is settled;
// SKIP LOCKED lets several publishers run without double-publishing.
func (s *Store) ClaimOutboxBatch(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "id", "type", "payload"::text, "attempts" FROM "OutboxEvent"
		 WHERE "status" = 'pending' AND "nextAttemptAt" <= now()
		 ORDER BY "createdAt" ASC
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit)
	events, err := collect(rows, err, func(row pgx.Row) (OutboxEvent, error) {
		var e OutboxEvent
		var payload string
		err := row.Scan(&e.ID, &e.Type, &payload, &e.Attempts)
		e.Payload = []byte(payload)
		return e, err
	})
	if err != nil || len(events) == 0 {
		return events, err
	}
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	_, err = s.db.Exec(ctx, `UPDATE "OutboxEvent" SET "status" = 'publishing' WHERE "id" = ANY($1)`, ids)
	return events, err
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'published', "publishedAt" = now(), "lastError" = NULL WHERE "id" = $1`, id)
	return err
}

func (s *Store) MarkOutboxRetry(ctx context.Context, id string, attempts int, lastErr string, delaySeconds int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent"
		 SET "status" = 'pending', "attempts" = $2, "lastError" = $3,
		     "nextAttemptAt" = now() + $4::int * interval '1 second'
		 WHERE "id" = $1`, id, attempts, lastErr, delaySeconds)
	return err
}

func (s *Store) MarkOutboxDead(ctx context.Context, id string, attempts int, lastErr string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'dead', "attempts" = $2, "lastError" = $3 WHERE "id" = $1`,
		id, attempts, lastErr)
	return err
}
