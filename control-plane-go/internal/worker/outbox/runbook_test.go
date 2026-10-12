package outbox_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/worker/outbox"
	"infrapilot/internal/workflows"
)

// restartSQL returns the statement of the fenced block "sql restart-teardown"
// in the runbook, with the psql variable :'sid' replaced by a quoted id. The
// tests run the document's own SQL, so the runbook cannot drift from the schema.
func restartSQL(t *testing.T, sid string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "superpowers", "runbook", "teardown-stuck.md"))
	if err != nil {
		t.Fatal(err)
	}
	const fence = "```sql restart-teardown\n"
	s := string(raw)
	i := strings.Index(s, fence)
	if i < 0 {
		t.Fatal("runbook has no 'sql restart-teardown' block")
	}
	s = s[i+len(fence):]
	j := strings.Index(s, "```")
	if j < 0 {
		t.Fatal("runbook block is not closed")
	}
	stmt := s[:j]
	if strings.Count(stmt, ":'sid'") == 0 {
		t.Fatal("runbook SQL does not use :'sid'")
	}
	return strings.ReplaceAll(stmt, ":'sid'", "'"+sid+"'")
}

type outboxRow struct {
	ID, Type, Payload, Status string
	Attempts                  int
}

func teardownRows(t *testing.T, sid string) []outboxRow {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT "id", "type", "payload"::text, "status"::text, "attempts" FROM "OutboxEvent"
		 WHERE "type" = 'teardown.requested' AND "payload"->>'serviceId' = $1 ORDER BY "createdAt", "id"`, sid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.ID, &r.Type, &r.Payload, &r.Status, &r.Attempts); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

const runbookSvc = "cm7a1b2c3d4e5f6g7h8i9j0k1"

// The runbook statement copies the service's latest teardown event as a fresh
// pending row (new id, attempts 0), keeps purgeData, and touches nothing else.
func TestRunbookRestartSQLCopiesLatestTeardownEvent(t *testing.T) {
	st := store.New(testutil.NeedDB(t, pool))
	ctx := context.Background()
	other := "cm8a1b2c3d4e5f6g7h8i9j0k1"
	old := create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": runbookSvc, "purgeData": false})
	time.Sleep(20 * time.Millisecond)
	latest := create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": runbookSvc, "purgeData": true})
	foreign := create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": other, "purgeData": false})
	deploy := create(t, st, store.EventBuildRequested, map[string]any{"deploymentId": runbookSvc})
	if err := st.MarkOutboxDead(ctx, latest, 10, "boom"); err != nil {
		t.Fatal(err)
	}

	tag, err := pool.Exec(ctx, restartSQL(t, runbookSvc))
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("rows affected %d", tag.RowsAffected())
	}
	rows := teardownRows(t, runbookSvc)
	if len(rows) != 3 {
		t.Fatalf("rows: %+v", rows)
	}
	var fresh outboxRow
	for _, r := range rows {
		if r.ID != old && r.ID != latest {
			fresh = r
		}
	}
	if fresh.ID == "" || fresh.Status != "pending" || fresh.Attempts != 0 || fresh.Type != store.EventTeardownRequested ||
		!strings.Contains(fresh.Payload, `"purgeData": true`) || !strings.Contains(fresh.Payload, runbookSvc) {
		t.Fatalf("fresh row %+v", fresh)
	}
	if n := len(teardownRows(t, other)); n != 1 {
		t.Fatalf("another service's events changed: %d", n)
	}
	_ = foreign
	_ = deploy

	// A service that never had an event: INSERT 0 0.
	tag, err = pool.Exec(ctx, restartSQL(t, "cm9a1b2c3d4e5f6g7h8i9j0k1"))
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("unknown service: %v rows %d", err, tag.RowsAffected())
	}
}

// recordingTeardownActs fails TeardownService permanently on the first call and
// records the input of every call.
type recordingTeardownActs struct {
	mu     sync.Mutex
	inputs []workflows.TeardownInput
}

func (r *recordingTeardownActs) CancelServiceDeploys(context.Context, workflows.TeardownInput) error {
	return nil
}

func (r *recordingTeardownActs) TeardownService(_ context.Context, in workflows.TeardownInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, in)
	if len(r.inputs) == 1 {
		return temporal.NewNonRetryableApplicationError("docker broke", "Boom", nil)
	}
	return nil
}

func (r *recordingTeardownActs) seen() []workflows.TeardownInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]workflows.TeardownInput(nil), r.inputs...)
}

// Real Temporal + real Postgres + real Starter and publisher: after a Failed
// run, the runbook row makes the publisher start a new run, with purgeData kept.
func TestE2ETeardownRunbookRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: skipped under -short")
	}
	addr := os.Getenv("TEMPORAL_E2E_ADDR")
	if addr == "" {
		t.Skip("e2e: TEMPORAL_E2E_ADDR is not set")
	}
	c, err := client.Dial(client.Options{HostPort: addr, Identity: "e2e"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tq := "e2e-runbook-" + time.Now().Format("150405000000")
	sid := "e2erestart" + time.Now().Format("150405000000")
	acts := &recordingTeardownActs{}
	wk := worker.New(c, tq, worker.Options{Identity: "e2e-runbook-worker"})
	wk.RegisterWorkflow(workflows.TeardownWorkflow)
	wk.RegisterActivity(acts)
	if err := wk.Start(); err != nil {
		t.Fatal(err)
	}
	defer wk.Stop()

	st := store.New(testutil.NeedDB(t, pool))
	ctx := context.Background()
	p := outbox.New(st, workflows.Starter{Client: c, TaskQueue: tq}, slog.New(slog.DiscardHandler))
	create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": sid, "purgeData": true})
	if n, err := p.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("tick: %d %v", n, err)
	}
	waitRun := func(want enumspb.WorkflowExecutionStatus) string {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for {
			d, err := c.DescribeWorkflowExecution(ctx, workflows.TeardownWorkflowID(sid), "")
			if err != nil {
				t.Fatal(err)
			}
			if d.GetWorkflowExecutionInfo().GetStatus() == want {
				return d.GetWorkflowExecutionInfo().GetExecution().GetRunId()
			}
			if time.Now().After(deadline) {
				t.Fatalf("status %s, want %s", d.GetWorkflowExecutionInfo().GetStatus(), want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	run1 := waitRun(enumspb.WORKFLOW_EXECUTION_STATUS_FAILED)

	// Without the runbook row nothing restarts.
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatalf("tick claimed %d", n)
	}

	if _, err := pool.Exec(ctx, restartSQL(t, sid)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() - interval '1 second' WHERE "status" = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if n, err := p.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("restart tick: %d %v", n, err)
	}
	run2 := waitRun(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED)
	if run1 == run2 {
		t.Fatal("no new run started")
	}
	got := acts.seen()
	if len(got) != 2 || !got[0].PurgeData || !got[1].PurgeData || got[1].ServiceID != sid {
		t.Fatalf("activity inputs %+v (purgeData must survive the restart)", got)
	}
}
