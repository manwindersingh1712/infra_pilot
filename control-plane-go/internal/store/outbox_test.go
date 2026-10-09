package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

func TestCountOutboxByStatus(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, err := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateOutboxEvent(ctx, "x", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxDead(ctx, a, 10, "gone"); err != nil {
		t.Fatal(err)
	}

	counts, err := st.CountOutboxByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["pending"] != 1 || counts["dead"] != 1 || counts["published"] != 0 {
		t.Fatalf("counts = %v", counts)
	}
}

func TestOldestUnpublishedOutboxAge(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	age, err := st.OldestUnpublishedOutboxAge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age != 0 {
		t.Fatalf("empty outbox age = %v, want 0", age)
	}

	pending, err := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	published, err := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	dead, err := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxPublished(ctx, published); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxDead(ctx, dead, 10, "gone"); err != nil {
		t.Fatal(err)
	}
	set := func(id, ago string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`UPDATE "OutboxEvent" SET "createdAt" = (now() AT TIME ZONE 'UTC') - $2::interval WHERE "id" = $1`, id, ago); err != nil {
			t.Fatal(err)
		}
	}
	set(pending, "90 seconds")
	set(published, "1 hour")
	set(dead, "2 hours")

	age, err = st.OldestUnpublishedOutboxAge(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if age < 89*time.Second || age > 120*time.Second {
		t.Fatalf("age = %v, want 89s..120s (published and dead rows must be ignored)", age)
	}
}
