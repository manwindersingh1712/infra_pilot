# Plan 2 Task 3 review (commit f088c71)

## Security

Reviewer: Security. Commit f088c71 (Plan 2 Task 3, internal/tickets). Spec §5 / D5.

Tool output:
- govulncheck: 0 vulnerabilities affect the code; 1 in a required module that is not called (same as Task 2).
- gosec -quiet ./internal/tickets/...: no issues.
- gitleaks (f088c71^!): no leaks found.
- cp-ui unchanged, so no npm audit.

Checked:
- Entropy: 32 bytes from crypto/rand, base64 RawURL (43 chars, 256 bits). rand.Read error is returned, not ignored.
- Single use: Redeem uses GETDEL, one atomic Redis command (Redis >= 6.2; test uses redis:7). No check-then-delete race. Missing, expired and reused tickets all map to ErrInvalid.
- TTL: SET with expiry in the same command, so no window where the key exists without a TTL.
- Namespace: "tickets:" prefix; ticket is appended to the key verbatim, but the key space is separate from logs/cursors.
- Logging: package has no logging; go-redis errors do not include key names. Nothing logs a ticket.
- Binding: value is an opaque string ("user:dep" in the test). The store does not enforce binding; the handler must (see SEC-1).

[SEC-1] severity: low
where: control-plane-go/internal/tickets/tickets.go:57 (Redeem)
problem: Binding to user and deployment is delegated to the caller via a free-form "user:dep" string. If the SSE handler only checks that a ticket is valid, or splits on ":" and does not compare both parts to the requested deployment and re-check ownership, a ticket issued for deployment A could open the stream of deployment B.
fix: In the Task 7/8 SSE handler, compare the redeemed value to the exact expected "<userID>:<deploymentID>" for the URL's deployment, and re-check ownership with GetDeploymentForOwner. Add a test for a ticket redeemed against the wrong deployment. Consider a small typed helper (Issue(user, dep) / Redeem returning both) to avoid string splitting.

[SEC-2] severity: low
where: control-plane-go/internal/tickets/tickets.go:23 (New)
problem: A ttl of 0 or less makes go-redis SET store the key with no expiry (ttl 0) or with KEEPTTL (-1), so a misconfigured value would yield never-expiring tickets. Only single use would remain.
fix: In New, return an error if ttl <= 0 (and optionally cap it, e.g. <= 5 minutes). Add a test.

[SEC-3] severity: low
where: control-plane-go/internal/tickets/tickets.go:57 (Redeem)
problem: Redeem accepts a ticket of any length from the query string and puts it into a Redis key. Not exploitable, but an oversized value costs memory and bandwidth per request. A value of unexpected length can never match a 43-char ticket.
fix: Reject tickets whose length is not 43 before calling Redis, or rely on the API body/URL size limits; the SSE route should also be rate limited.

Note: the Redis connection uses REDIS_URL as given (no TLS enforcement); fine for the loopback dev stack, document for production.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 3 low)

## Staff

Reviewer: Staff. Commit f088c71 (Plan 2 Task 3, `internal/tickets`).

Tool output (from `control-plane-go/`): `go build ./...` clean; `go vet ./internal/tickets/` clean; `golangci-lint run ./internal/tickets/...` 0 issues. `go test -race -count=1 ./internal/tickets/` passes (4.9s, Docker available).

Spec match: the code and test are identical to plan Task 3 (lines 694-847). No deviations and no extra scope. `Ping` is in the plan, though nothing uses it yet.

Mutation check (throwaway copy under the scratchpad, repo untouched; each mutation run against the committed test):
- `GetDel` replaced by `Get` (single-use broken): FAIL at tickets_test.go:44 "second redeem err = <nil>". Killed.
- TTL set to 0 (never expires): FAIL at :55 "expired ticket err = <nil>". Killed.
- 4 random bytes instead of 32: FAIL at :34 (len check). Killed.
- Constant ticket: FAIL at :38 "tickets must be unique". Killed.
So the single-use and expiry assertions do detect their breakage; the build-failure-only red step is acceptable here.

[STAFF-1] severity: low
where: control-plane-go/internal/tickets/tickets_test.go:44
problem: Single-use is only tested sequentially. A non-atomic GET-then-DEL implementation passes the test, though it would let two concurrent redeems both win. Atomicity is the actual security property.
evidence: mutations above only prove GET-without-DEL fails; no concurrent redeem test exists.
fix: Add N goroutines redeeming the same ticket and assert exactly one succeeds (cheap, same container).

[STAFF-2] severity: low
where: control-plane-go/internal/tickets/tickets_test.go:53
problem: Expiry test sleeps 150ms for a 50ms TTL. Spec-prescribed, and the failure direction is safe (a slow run still passes), but it is a fixed sleep where a poll would do.
evidence: `time.Sleep(150 * time.Millisecond)`.
fix: Optional: poll Redeem-by-TTL via `s.c.TTL` or loop until ErrInvalid with a deadline. Can stay as is.

[STAFF-3] severity: low
where: control-plane-go/internal/tickets/tickets.go:51
problem: Redeem passes any non-Nil Redis error straight through unwrapped (callers cannot tell a Redis outage from ErrInvalid without errors.Is, which is correct, but there is no context in the message). Also Redeem of "" is not rejected explicitly (it GETDELs key "tickets:", harmless). Unused `Ping` and the redeemed-error path are untested. Issue ignores ttl<=0 (New accepts 0, which makes tickets never expire).
evidence: `return v, err`; `New` has no ttl validation.
fix: Wrap errors (`fmt.Errorf("redeem ticket: %w", err)`); have New reject ttl <= 0. The handler task must map non-ErrInvalid errors to 5xx, not 401.

Operability: the package logs nothing, which is appropriate (the ticket is a secret and must not be logged; handlers should log userId/deploymentId only).

RESULT: APPROVE (0 critical, 0 high, 0 medium, 3 low)

## Verifier (commit f088c71)

RESULT: PASS (one flaky, unrelated failure on the first full run; see VER-1)

- `go build ./...` -> exit 0
- `go vet ./...` -> exit 0
- `golangci-lint run --allow-parallel-runners` (run alone) -> `0 issues.`
- `go test -race -count=5 ./internal/tickets/` -> `ok  	infrapilot/internal/tickets	13.833s`
- `go test -race -count=1 ./...`, run 1 (lint and the tickets x5 run were executing at the same time): FAIL
  `--- FAIL: TestRunStartsEventsUntilCancelled (1.25s)` / `publisher_test.go:469: status = pending` / `FAIL infrapilot/internal/worker/outbox 33.266s`. Every other package ok.
- `go test -race -count=1 ./internal/worker/outbox/` -> `ok 5.564s`; `-count=3 -run TestRunStartsEventsUntilCancelled` -> `ok 6.819s`
- `go test -race -count=1 ./...`, run 2 (machine otherwise idle): all 16 packages `ok`, including `internal/tickets 9.406s` and `internal/worker/outbox 16.871s`.

Failures:
- [VER-1] (low, pre-existing, not caused by f088c71, which only touches internal/tickets) `TestRunStartsEventsUntilCancelled` in `internal/worker/outbox/publisher_test.go:469` asserted `status = pending` under CPU load. Likely a timing-dependent test (event not published within its wait window while other jobs compete for CPU). Passes in isolation and on an idle full run. Worth a backlog item to make it poll longer.

No feature run against live infra: Task 3 is library-only (internal/tickets), covered by its tests with the redis testcontainer.

## Fix round 1 (orchestrator check)
Commits 1bca18d (SEC-2/STAFF-3), f512922 (SEC-3), 2a1a2ca (STAFF-1). Engineer ran build, vet, golangci-lint (0 issues), gosec (clean), `go test -race -count=3 ./internal/tickets/` and the full `go test -race -count=1 ./...` (all ok). The orchestrator read the final tickets.go: TTL guard first in `New`, `Redeem` rejects malformed tickets before Redis, `ErrInvalid` kept apart from wrapped Redis errors. The concurrent-redeem test was mutation-checked (Get+Del fails it). The TTL test was not seen failing on its own; the guard is a single early return and the test covers 0, -1 and -1s. Result: task clean; remaining lows (SEC-1 handler binding, outbox flake) are in backlog.md.
