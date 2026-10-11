# Plan 2 Task 8 round 2 - Security review (6d83d2f..77f8daf vs ce69f7f)

## Tools
- gitleaks git --redact (repo root): 356 commits, no leaks.
- govulncheck ./... (hard-limited, full path): exit 0, 0 reachable (1 module-level, not called: known item).
- gosec -quiet ./...: 2 x G204 (internal/runtime/exec.go:26, logs exec.CommandContext). Both in files untouched by this range, arg-list runners by design (pre-existing). No new findings; the two new `#nosec G101` on MongoPasswordEnv are justified inline (env var name).
- go test -p 2 -count=1 ./internal/{api,runtime,store,workflows}/...: all ok.

## Verified
1. Reserved key guard (store.ReservedEnvKey, services.go set/delete).
   - Exact byte comparison against the same literals the worker reads (`Managed[...].PasswordEnv` now references the same constants, so they cannot drift). The worker also looks the key up by exact match, so case variants (`redis_password`), whitespace-padded keys and unicode lookalikes are different variables that never reach the password slot; on set they also fail ValidEnvKey (ASCII charset only), on delete they hit a different row. No bypass.
   - Applied after ownership (foreign/missing ids still 404) and before the lenient delete lookup; both POST (set) and DELETE variants guarded; there is no PUT/PATCH route. Check uses the stored svc.ServiceType, and there is no endpoint that changes a service type, so no type-switch bypass. Non-managed services may still use the names (intended).
   - Node-era managed service with a plain-text password var: untouched by the guard (read-only until the next deploy). EnsureCredentials uses Secrets.Decrypt, which passes non-`enc:v1:` values through, keeps a non-empty plaintext value and only replaces an empty one; the DB keeps working. Plaintext at rest is handled by the existing encrypt-env migration, not by this change. `?reveal=true` still returns the password to the owner (intended).
   - Rejection text `reserved_env_var` only appears after the ownership check, so it is not an oracle for foreign services.
2. runtime.ValidEnvKey: `len(k) <= 256 && envKeyRE.MatchString(k)` is the exact negation of the old worker condition (`len(k) > 256 || !match`); regex literal and constant unchanged (diff shows only the added function and the call site). RE2 `$` without (?m) is end-of-text, so a trailing newline is still rejected. Worker acceptance set is identical; no new key can reach the container env file. The API now applies the same rule it had before (same regex), so nothing newly accepted there either. Table tests assert both ValidEnvKey and validateEnv on the same accept/reject lists.
   - internal/api -> internal/runtime import: runtime's first-party deps are only internal/logs; it shells out via exec (no docker SDK in deps, no `func init()` in the package; only error vars at package level). No side effects on import. Minor: the API binary now links the runtime package (larger surface, exec runner types), cosmetic.
3. Reveal audit line: `s.Log.Info("env revealed", "serviceId", svc.ID, "userId", userID(r), "requestId", requestID(r))` - ids only, no keys, values or counts; ids are server-generated/validated by pathID, so no CRLF injection. Logged only after decryption succeeds (a failed reveal logs via internalError, which carries no values). Fine.
4. `*body.Value == maskedValue` rejection: compares the caller's own input to a public constant; nothing secret is compared, so no timing or equality oracle, and the stored value is never consulted. The error is the generic `invalid_env_var`, identical to every other value/key rejection (no distinguishing text). Side effect: a user cannot store the literal value `********` (accepted tradeoff). The check runs before the ownership lookup but depends only on the request.
5. Transaction tests: the `test_fail_outbox` function and trigger are created inside failOutboxInserts only in those two tests, with t.Cleanup dropping both (DROP ... IF EXISTS); they live in a per-test database created and force-dropped by testutil, so they cannot reach the dev DB or other tests. Not present in migrations or non-test code.

## Findings

[SEC-R2-1] severity: low
where: internal/workflows/activities.go:373 (merge(m.Env, env)) / services.go setServiceEnv
problem: the guard covers only the password key. A managed service owner can still set other keys that override the worker's fixed managed env (e.g. `MONGO_INITDB_ROOT_USERNAME`, which is in `Managed[mongodb].Env` and is overridden by user env). On an existing volume this does nothing (init runs once), on a fresh volume it changes the admin name the connection string assumes. Self-inflicted, own tenant only, no security impact.
fix: optional: extend ReservedEnvKey to `MONGO_INITDB_ROOT_USERNAME` for mongodb, or document. Not a gate item.

[SEC-R2-2] severity: informational
where: services.go setServiceEnv ordering
problem: a managed service with a Node-era empty-string password var cannot be repaired via the API (writes are refused); it is repaired automatically by EnsureCredentials on the next deploy, which is the intended path. No action.

SEC-1, SEC-2 from round 1: resolved. SEC-3..SEC-6 remain as recorded (Plan 3 / Task 12 carry-forwards), not touched by this range.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 1 low, 1 informational)
