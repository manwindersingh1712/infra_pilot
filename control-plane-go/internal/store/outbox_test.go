package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"infrapilot/internal/store"
)

type outboxRow struct {
	Status   string
	Attempts int
	LastErr  *string
	Due      bool
	HasPubAt bool
}

func readOutbox(t *testing.T, id string) outboxRow {
	t.Helper()
	var r outboxRow
	err := pool.QueryRow(context.Background(),
		`SELECT "status"::text, "attempts", "lastError", "nextAttemptAt" <= now(), "publishedAt" IS NOT NULL
		 FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&r.Status, &r.Attempts, &r.LastErr, &r.Due, &r.HasPubAt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaimOutboxBatchReturnsOnlyDueEvents(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	due, _ := st.CreateOutboxEvent(ctx, "build.requested", map[string]string{"deploymentId": "d1"})
	later, _ := st.CreateOutboxEvent(ctx, "build.requested", map[string]string{"deploymentId": "d2"})
	if _, err := pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() + interval '1 hour' WHERE "id" = $1`, later); err != nil {
		t.Fatal(err)
	}

	var events []store.OutboxEvent
	err := st.InTx(ctx, func(tx *store.Store) error {
		var err error
		events, err = tx.ClaimOutboxBatch(ctx, 25)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != due || events[0].Type != "build.requested" {
		t.Fatalf("got %+v", events)
	}
	var payload map[string]string
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload["deploymentId"] != "d1" {
		t.Fatalf("payload %s err %v", events[0].Payload, err)
	}
	if r := readOutbox(t, due); r.Status != "publishing" {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestOutboxStatusTransitions(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	b, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	c, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})

	if err := st.MarkOutboxPublished(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxRetry(ctx, b, 3, "boom", 30); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxDead(ctx, c, 10, "gone"); err != nil {
		t.Fatal(err)
	}

	if r := readOutbox(t, a); r.Status != "published" || !r.HasPubAt || r.LastErr != nil {
		t.Fatalf("a = %+v", r)
	}
	if r := readOutbox(t, b); r.Status != "pending" || r.Attempts != 3 || r.Due || *r.LastErr != "boom" {
		t.Fatalf("b = %+v", r)
	}
	if r := readOutbox(t, c); r.Status != "dead" || r.Attempts != 10 || *r.LastErr != "gone" {
		t.Fatalf("c = %+v", r)
	}
}
