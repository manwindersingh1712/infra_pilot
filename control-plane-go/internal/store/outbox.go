package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

// ErrOutboxPermanent marks a Start failure that retrying cannot fix (a
// poison payload). The publisher sends such rows straight to dead.
var ErrOutboxPermanent = errors.New("outbox: permanent failure")

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

// RequeueOutbox returns claimed rows to pending untouched: no attempt is
// burned and nextAttemptAt (already due) stays, so the next tick takes them.
func (s *Store) RequeueOutbox(ctx context.Context, ids []string) error {
	_, err := s.db.Exec(ctx, `UPDATE "OutboxEvent" SET "status" = 'pending' WHERE "id" = ANY($1) AND "status" = 'publishing'`, ids)
	return err
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'published', "publishedAt" = now(), "lastError" = NULL WHERE "id" = $1`, id)
	return err
}

// MaxLastErrorRunes caps the stored "lastError" so a hostile or verbose
// error cannot bloat the row.
const MaxLastErrorRunes = 500

// cleanLastError makes an error string safe for a Postgres text column (NUL
// and invalid UTF-8 are rejected there, which would fail the UPDATE and loop
// the row every tick) and caps it at n runes.
func cleanLastError(s string, n int) string {
	s = strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (s *Store) MarkOutboxRetry(ctx context.Context, id string, attempts int, lastErr string, delaySeconds int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent"
		 SET "status" = 'pending', "attempts" = $2, "lastError" = $3,
		     "nextAttemptAt" = now() + $4::int * interval '1 second'
		 WHERE "id" = $1`, id, attempts, cleanLastError(lastErr, MaxLastErrorRunes), delaySeconds)
	return err
}

func (s *Store) MarkOutboxDead(ctx context.Context, id string, attempts int, lastErr string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'dead', "attempts" = $2, "lastError" = $3 WHERE "id" = $1`,
		id, attempts, cleanLastError(lastErr, MaxLastErrorRunes))
	return err
}

// CountOutboxByStatus returns how many outbox rows are in each status.
func (s *Store) CountOutboxByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT "status"::text, count(*)::int FROM "OutboxEvent" GROUP BY "status"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

// OldestUnpublishedOutboxAge returns the age of the oldest pending or
// publishing outbox row, or 0 when there is none. createdAt is UTC (Prisma convention).
func (s *Store) OldestUnpublishedOutboxAge(ctx context.Context) (time.Duration, error) {
	var secs float64
	err := s.db.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM (now() AT TIME ZONE 'UTC') - min("createdAt")), 0)::float8
		FROM "OutboxEvent" WHERE "status" IN ('pending', 'publishing')`).Scan(&secs)
	return time.Duration(secs * float64(time.Second)), err
}
