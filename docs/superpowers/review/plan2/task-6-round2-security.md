## Security, fix round 1 (bab3c79, 5c680c3, 55c66b1; diff against d531344)

Reviewed: control-plane-go/internal/store/refresh.go, migrations/00004_refresh_family.sql, internal/api/{auth,server,ratelimit}.go, internal/auth/refresh.go, spec §5 edits, against decisions.md "2026-10-11 Plan 2 Task 6 rulings" and my round-1 findings.

### Tools
- govulncheck (hard-limited, full path): exit 0, "Your code is affected by 0 vulnerabilities"; 1 in a required module not called (the already-accepted GO-2026-5932).
- gosec -quiet ./...: 2 issues, both G204 in internal/runtime (logstream.go:61, exec.go:26), unchanged, pre-existing, argument-list exec by design. Nothing in the changed packages. (The "export data version" import warnings are the known stale build cache noise.)
- gitleaks git --redact (repo root): 335 commits, no leaks.

### SEC-1 closure (refresh rotation)
Closed. Walk-through of RotateRefreshToken:
- Lock order is the same in both writers: plain read of familyId (immutable column, no lock) -> pg_advisory_xact_lock(family) -> row work. RevokeRefreshFamily takes the same advisory lock first, so there is no lock-order inversion and no deadlock (the FOR UPDATE row lock is only ever taken while holding the family lock). No other code in the repo takes advisory locks or writes RefreshToken (grep), so the key space has no foreign users.
- Step 3 re-reads the row FOR UPDATE in a fresh READ COMMITTED statement after the lock, so it sees the previous holder's commit. A loser of a two-way rotate sees revokedAt+rotatedAt set within grace -> ErrReplayInGrace, no revoke. A revoke that runs after a winner's commit sees and revokes the winner's child (it is committed before the lock is released); a revoke that runs first leaves the child revoked with rotatedAt NULL, so a later rotation of that child is ErrNotFound. No interleaving leaves a live token after a family revoke: every insert and every revoke for a family is inside the same lock.
- Lock key: hashtextextended(familyId, 0) is a 64-bit hash of a cuid. A collision only makes two unrelated families wait on each other briefly (over-serialisation), never a correctness or isolation problem; collision odds are negligible at this scale. Not a finding.
- Revoke commit on reuse: the callback returns revokeFamily's error (nil) and sets reused, so pgx.BeginFunc commits, then the function returns ErrTokenReused. If the revoke itself fails, err is non-nil and is returned (500), nothing committed. Correct.
- Child insert failure (e.g. unique violation on newHash): the callback returns the error, BeginFunc rolls back the old token's revokedAt/rotatedAt update, so the old token stays live and nothing is revoked. newHash is server generated (32 random bytes), the client cannot influence it.
- Handler: new token generated first, RotateRefreshToken, then Sign. Signer failure after commit would burn the token; HMAC signing does not fail in practice and the retry lands in the grace window (401, no revoke), as the ruling accepts.
- Ordering of the switch: a rotated token always has revokedAt set, so `live` cannot be true for it; an expired-and-never-rotated token falls to the default (ErrNotFound, no revoke); an expired rotated token replayed after grace still revokes the family (right: it is a replay).

### Grace window abuse
No abuse found. An attacker replaying a stolen just-rotated token inside 10 s gets the same `401 invalid_refresh_token` body as any bad token, receives no tokens, and nothing is revoked. Replay after the window is a family revoke plus the same 401 body. The status/body/headers are identical for unknown, expired, logged-out, in-grace and reuse cases, so there is no response oracle; the only differences are a server-side log line (Info vs Warn, requestId only, no token) and a few ms of DB work. The accepted residual: an attacker who holds an old token can, after the window, force one family re-login (DoS of that session only, and only with token possession); a thief who rotates first is killed when the victim next presents the old token after the window. Inherent to the design in the ruling.

### Logout / RevokeRefreshFamily
- Semantics match the ruling: looks up the family of the presented token even if rotated or revoked, revokes live tokens of that family under the advisory lock, unknown token returns {"ok":true} (pgx.ErrNoRows from the callback is swallowed after rollback). Other families of the user are untouched (WHERE familyId = $1).
- Enumeration: response is {"ok":true} for known and unknown tokens; no oracle beyond sub-ms timing, and a valid 256-bit token is required to affect anything. Possession of any token of the chain (even a rotated one) can end the chain; that is the stated intent.
- Logout is now behind the refresh limiter (closes round-1 SEC-6).

### Migration 00004
Safe. Add nullable columns, backfill familyId = id, then SET NOT NULL, then index; goose wraps the file in one transaction, so a failure leaves nothing half-applied. No default needed because the table is tiny and the only writer (CreateRefreshToken) always supplies familyId. Down drops the index and both columns and loses family info only, which is acceptable. Notes (informational, not findings): (a) the backfill makes every existing token its own family and old revoked rows keep rotatedAt NULL, so pre-migration chains are not linked and a replay of a pre-migration rotated token is a plain 401 rather than theft detection; the table was introduced one task earlier (00003) and nothing is deployed, so there is no exposure. (b) The grace comparison `now() - "rotatedAt"` mixes timestamptz and a TIMESTAMP(3) column (same pattern as expiresAt in 00003); it is only correct if the session time zone is constant, which holds for a pool with a fixed server/role TimeZone. Worth keeping the DB on UTC (the compose/Postgres default); no code change needed.

### New error codes, email/name caps
- password_too_short / password_too_long are returned only after the email is valid and before the 409 existence check, and depend on the request alone, so they leak nothing about accounts (an existing email with a short password gets 400, same as a new one). Login has no length check (parity), still equalises timing.
- Email 254-byte cap runs before mail.ParseAddress on register and login (validEmail is shared). Name cap 100 runes, null/absent OK, multi-byte counted once. Body size is still bounded by the 1 MiB reader.

### Rate limiters
Independent buckets, independent LRU maps: authLimit (register, login: every 6 s, burst 5) and refreshLimit (refresh, logout: every 1 s, burst 20), same key function (TCP peer; IPv6 grouped by /64, which is stricter than the ruling's "full address" and fine). Defaults apply for 0. The limiter runs before body parsing. Residual (already accepted in the rulings, pending Task 14/18): until TRUSTED_PROXIES exists, behind nginx all clients share one refresh bucket (60/min, burst 20), so one script could 429 everyone's refresh; Task 15 must not sign the user out on a 429 (it should retry after Retry-After). Make sure the TRUSTED_PROXIES item gates the cutover.

### Spec text
§5 JWT and brute-force rows match the ruling wording (single transaction, family, 10 s grace, plain 401 without revoke, logout revokes the presenter's family, per-class buckets, TRUSTED_PROXIES description). No inaccuracy found.

### Findings

[SEC-8] severity: low
where: control-plane-go/internal/api/auth.go register (name handling), store/users.go
problem: The name cap counts runes but does not reject control characters. A JSON name containing \u0000 decodes to a NUL byte, which Postgres text columns reject, so register returns 500 internal_error (logged server side) instead of 400. Reasoned from Postgres behaviour, not executed. Only affects the caller's own request; no data or security impact beyond log noise and an easy way to generate error-level logs.
fix: Reject names containing NUL (and ideally any control characters) with 400 invalid_request, with a boundary test. Backlog is fine.

No critical, high or medium findings. Earlier SEC-1, SEC-2, SEC-5, SEC-6 are closed; SEC-3 (409 enumeration) and SEC-4 (dev@local timing) remain accepted/deferred as ruled; SEC-7 stays a Task 14 checklist item.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 1 low)
