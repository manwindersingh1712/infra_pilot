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

// runbookBlock is one fenced "sql <name>" block of the runbook.
type runbookBlock struct{ Name, Body string }

// runbookBlocks returns EVERY fenced sql block of the runbook, in document
// order. The tests run the document's own SQL, so the runbook cannot drift
// from the schema (a column typo fails the test).
func runbookBlocks(t *testing.T) []runbookBlock {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "docs", "superpowers", "runbook", "teardown-stuck.md"))
	if err != nil {
		t.Fatal(err)
	}
	var out []runbookBlock
	s := string(raw)
	for {
		i := strings.Index(s, "```sql")
		if i < 0 {
			return out
		}
		s = s[i+len("```sql"):]
		nl := strings.Index(s, "\n")
		name := strings.TrimSpace(s[:nl])
		s = s[nl+1:]
		j := strings.Index(s, "```")
		if j < 0 {
			t.Fatal("runbook block is not closed")
		}
		if name == "" {
			t.Fatal("runbook sql block has no name (use ```sql <name>)")
		}
		out = append(out, runbookBlock{Name: name, Body: s[:j]})
		s = s[j+3:]
	}
}

// stmts substitutes the psql variable :'sid' and splits the block into
// statements (comments stripped; the runbook SQL has no ';' inside literals).
func (b runbookBlock) stmts(sid string) []string {
	var lines []string
	for _, l := range strings.Split(b.Body, "\n") {
		if k := strings.Index(l, "--"); k >= 0 {
			l = l[:k]
		}
		lines = append(lines, l)
	}
	body := strings.ReplaceAll(strings.Join(lines, "\n"), ":'sid'", "'"+sid+"'")
	var out []string
	for _, st := range strings.Split(body, ";") {
		if strings.TrimSpace(st) != "" {
			out = append(out, st)
		}
	}
	return out
}

func runbookBlockNamed(t *testing.T, name string) runbookBlock {
	t.Helper()
	for _, b := range runbookBlocks(t) {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("runbook has no 'sql %s' block", name)
	return runbookBlock{}
}

// restartSQL returns the statement of the block "sql restart-teardown".
func restartSQL(t *testing.T, sid string) string {
	t.Helper()
	b := runbookBlockNamed(t, "restart-teardown")
	if !strings.Contains(b.Body, ":'sid'") {
		t.Fatal("runbook SQL does not use :'sid'")
	}
	return b.stmts(sid)[0]
}

// seedService creates a service of the type, soft-deleted when deleted is set.
func seedService(t *testing.T, st *store.Store, typ string, deleted bool) string {
	t.Helper()
	f := testutil.Seed(t, st, typ)
	if deleted {
		if err := st.SoftDeleteService(context.Background(), f.ServiceID); err != nil {
			t.Fatal(err)
		}
	}
	return f.ServiceID
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

// The runbook statement copies the service's latest teardown event as a fresh
// pending row (new id, attempts 0), keeps purgeData, and touches nothing else.
func TestRunbookRestartSQLCopiesLatestTeardownEvent(t *testing.T) {
	st := store.New(testutil.NeedDB(t, pool))
	ctx := context.Background()
	runbookSvc := seedService(t, st, store.TypeNodejs, true)
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

	// SEC-8: a LIVE service (mistyped id) with a teardown event gets no copy.
	live := seedService(t, st, store.TypeNodejs, false)
	create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": live, "purgeData": true})
	tag, err = pool.Exec(ctx, restartSQL(t, live))
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("live service: %v rows %d", err, tag.RowsAffected())
	}
	if n := len(teardownRows(t, live)); n != 1 {
		t.Fatalf("live service got a copied event: %d", n)
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
	st := store.New(testutil.NeedDB(t, pool))
	sid := seedService(t, st, store.TypeNodejs, true)
	acts := &recordingTeardownActs{}
	wk := worker.New(c, tq, worker.Options{Identity: "e2e-runbook-worker"})
	wk.RegisterWorkflow(workflows.TeardownWorkflow)
	wk.RegisterActivity(acts)
	if err := wk.Start(); err != nil {
		t.Fatal(err)
	}
	defer wk.Stop()

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

// mutating reports whether a statement changes data.
func mutating(stmt string) bool {
	f := strings.ToUpper(strings.TrimSpace(stmt))
	return strings.HasPrefix(f, "DELETE") || strings.HasPrefix(f, "UPDATE") || strings.HasPrefix(f, "INSERT")
}

type counts struct{ env, conns, outbox int }

func countsFor(t *testing.T, sid string) (c counts, repo *string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "EnvVar" WHERE "serviceId" = $1`, sid).Scan(&c.env); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "ServiceConnection" WHERE "sourceId" = $1 OR "targetId" = $1`, sid).Scan(&c.conns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "OutboxEvent" WHERE "payload"->>'serviceId' = $1`, sid).Scan(&c.outbox); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT "repoUrl" FROM "Service" WHERE "id" = $1`, sid).Scan(&repo); err != nil {
		t.Fatal(err)
	}
	return c, repo
}

func seedRows(t *testing.T, st *store.Store, sid, peer string, envKeys ...string) {
	t.Helper()
	ctx := context.Background()
	for _, k := range envKeys {
		if _, err := st.UpsertEnvVar(ctx, sid, k, "enc:v1:x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateConnection(ctx, sid, peer, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateConnection(ctx, peer, sid, nil); err != nil {
		t.Fatal(err)
	}
	create(t, st, store.EventTeardownRequested, map[string]any{"serviceId": sid, "purgeData": false})
}

// SEC-8 / STAFF-R2-2: EVERY sql block of the runbook runs verbatim against the
// real schema, and no destructive or restart statement can hit a LIVE service
// (a mistyped id): 0 rows affected, everything still there.
func TestRunbookEverySQLBlockRunsAndSparesLiveServices(t *testing.T) {
	st := store.New(testutil.NeedDB(t, pool))
	ctx := context.Background()
	blocks := runbookBlocks(t)
	if len(blocks) < 6 {
		t.Fatalf("only %d sql blocks found", len(blocks))
	}
	for _, typ := range []string{store.TypeNodejs, store.TypeRedis, store.TypeMongoDB} {
		live := seedService(t, st, typ, false)
		peer := seedService(t, st, store.TypeNodejs, false)
		seedRows(t, st, live, peer, "FOO", store.RedisPasswordEnv, store.MongoPasswordEnv)
		if _, err := pool.Exec(ctx, `UPDATE "Service" SET "repoUrl" = 'https://x@example.com/r.git' WHERE "id" = $1`, live); err != nil {
			t.Fatal(err)
		}
		before, repoBefore := countsFor(t, live)
		for _, b := range blocks {
			for _, stmt := range b.stmts(live) {
				tag, err := pool.Exec(ctx, stmt)
				if err != nil {
					t.Fatalf("%s: block %q failed on real Postgres: %v\n%s", typ, b.Name, err, stmt)
				}
				if mutating(stmt) && tag.RowsAffected() != 0 {
					t.Errorf("%s: block %q changed %d rows of a LIVE service:\n%s", typ, b.Name, tag.RowsAffected(), stmt)
				}
			}
		}
		after, repoAfter := countsFor(t, live)
		if after != before || repoBefore == nil || repoAfter == nil || *repoAfter != *repoBefore {
			t.Errorf("%s: live service changed: %+v -> %+v repo %v -> %v", typ, before, after, repoBefore, repoAfter)
		}
	}
}

// Against a soft-deleted service the scrub removes its rows; against a managed
// one the first (step 4) scrub keeps the password rows and only the section 5
// statement removes exactly those; another service is never touched.
func TestRunbookScrubAndPasswordStatementsOnDeletedServices(t *testing.T) {
	st := store.New(testutil.NeedDB(t, pool))
	ctx := context.Background()
	scrub, purge := runbookBlockNamed(t, "scrub-rows"), runbookBlockNamed(t, "purge-password")
	run := func(b runbookBlock, sid string) {
		t.Helper()
		for _, stmt := range b.stmts(sid) {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v\n%s", b.Name, err, stmt)
			}
		}
	}
	envKeys := func(sid string) []string {
		rows, err := pool.Query(ctx, `SELECT "key" FROM "EnvVar" WHERE "serviceId" = $1 ORDER BY "key"`, sid)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var ks []string
		for rows.Next() {
			var k string
			rows.Scan(&k)
			ks = append(ks, k)
		}
		return ks
	}
	bystander := seedService(t, st, store.TypeRedis, true)
	peer := seedService(t, st, store.TypeNodejs, false)
	seedRows(t, st, bystander, peer, "FOO", store.RedisPasswordEnv)

	// A deleted node service: everything goes (even a user key named like a password).
	node := seedService(t, st, store.TypeNodejs, true)
	seedRows(t, st, node, peer, "FOO", store.RedisPasswordEnv)
	if _, err := pool.Exec(ctx, `UPDATE "Service" SET "repoUrl" = 'https://tok@example.com/r.git' WHERE "id" = $1`, node); err != nil {
		t.Fatal(err)
	}
	run(scrub, node)
	c, repo := countsFor(t, node)
	if c.env != 0 || c.conns != 0 || repo != nil {
		t.Fatalf("node service not scrubbed: %+v repo %v", c, repo)
	}
	run(purge, node) // not a managed service: nothing to do, no error

	// A deleted redis service: scrub keeps the password, purge-password removes only it.
	redis := seedService(t, st, store.TypeRedis, true)
	seedRows(t, st, redis, peer, "FOO", "BAR", store.RedisPasswordEnv)
	run(scrub, redis)
	if got := envKeys(redis); len(got) != 1 || got[0] != store.RedisPasswordEnv {
		t.Fatalf("after scrub: %v", got)
	}
	if c, _ := countsFor(t, redis); c.conns != 0 {
		t.Fatalf("connections left: %+v", c)
	}
	run(purge, redis)
	if got := envKeys(redis); len(got) != 0 {
		t.Fatalf("after purge-password: %v", got)
	}

	// purge-password removes only the two password keys.
	mongo := seedService(t, st, store.TypeMongoDB, true)
	seedRows(t, st, mongo, peer, "FOO", store.MongoPasswordEnv)
	run(purge, mongo)
	if got := envKeys(mongo); len(got) != 1 || got[0] != "FOO" {
		t.Fatalf("purge-password left %v, want only FOO", got)
	}

	if got := envKeys(bystander); len(got) != 2 {
		t.Fatalf("bystander env changed: %v", got)
	}
	if c, _ := countsFor(t, bystander); c.conns != 2 {
		t.Fatalf("bystander connections changed: %+v", c)
	}

	// The read-only blocks return rows for the right services.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+strings.TrimSuffix(strings.TrimSpace(runbookBlockNamed(t, "orphan-volumes").stmts("x")[0]), ";")+`) q`).Scan(&n); err != nil || n < 1 {
		t.Fatalf("orphan-volumes: %d %v", n, err)
	}
}
