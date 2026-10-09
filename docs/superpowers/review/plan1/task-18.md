# Plan 1, Task 18: Nginx routes

## Implementation
- Engineer commit `d93ad11` feat(go): nginx route files and reload (`internal/runtime/{nginx.go,nginx_test.go}`). Plan-verbatim plus the CTO SEC-1 `#nosec G301/G306` annotations. Byte-identical to Node's `nginx.ts` output. Test failed first (`undefined: RenderRoute`); CI dry run green.

## Round 0 reviews (d93ad11)
- **Security: CLEAN (1 low).** No callers yet; planned callers pass cuids (`ServiceID`, `cp-<deploymentID>`) and operator config (`SERVICE_BASE_DOMAIN`). gosec/govulncheck/gitleaks clean. SEC-1 T18 (low): serviceID/containerName/baseDomain unvalidated (path traversal / directive injection if a future caller passes non-generated values); reuse `checkName` (docker.go) as Task 16 SEC-5 did.
- **Staff: CHANGES REQUESTED.**
  - STAFF-1 T18 (filed high): "`nginx -s reload` exits 0 on invalid config". **Orchestrator checked on real nginx:alpine:** a syntax error makes `nginx -s reload` print `[emerg]` and exit 1, so the error is not silent. The remaining valid part: the bad file stays on disk and makes every later reload (for every service) fail. **Downgraded to medium**; fix = roll the file back when the reload fails (no separate `nginx -t` needed).
  - STAFF-2 T18 (medium): non-atomic `WriteFile`; a concurrent reload or failed write leaves a truncated `svc-*.conf`. Fix: temp file not matching `*.conf` + rename.
  - STAFF-3 T18 (medium): no tests for error paths.
  - STAFF-4 T18 (low): errors lack context (service/file).
  - STAFF-5 T18 (low): duplicate of SEC-1 T18.
  - STAFF-6 T18 (forward, Plan 2 `RemoveRoute`): share one checked reload helper; confirm deploys are serialized per service.
- **Verifier: PASS.** Full suite + lint green. Real nginx:alpine on a throwaway network: `UpsertRoute` → curl with `Host: svc-1.localhost` reached the app; app recreated with a new IP and no reload → still 200 (Docker DNS at request time confirmed). 0o750/0o640 work on Docker Desktop (master reads as root; the share ignores mode bits), untested for rootless/userns-remap Linux.

## Orchestrator decisions
- Fix round 1 takes STAFF-1 (as medium), STAFF-2, STAFF-3, and also the two lows SEC-1 and STAFF-4: they touch the same ~10 lines, and the error wrapping is needed by the rollback anyway.
- Perms stay 0o755/0o644 with the `#nosec` reasons (the CTO amendment's first option): tightening is unverified for rootless/userns-remap hosts and `MkdirAll`/`WriteFile` would not change existing modes anyway.
- STAFF-6 → backlog (Plan 2 RemoveRoute).

## Fix round 1
- `fbaf974` SEC-1: validate serviceID (DNS label), container (`checkName`), base domain (hostname) before touching disk.
- `ed2009a` STAFF-2: `writeFileAtomic` (CreateTemp `.svc-*.tmp`, Sync, Chmod 0o644, Rename). Test proves replace-not-rewrite via a hard link.
- `fe58874` STAFF-1 + STAFF-4: on reload failure restore the old file (or remove the new one); `nginx route <id>: …` wrapping; `errors.Join` if rollback also fails. No second reload after rollback (running nginx kept its last good config).
- `235ef07` STAFF-3: MkdirAll failure test (reload-failure tests are in `fe58874`).
- New annotations: `#nosec G302` (Chmod), `#nosec G304` (read previous file). Engineer CI dry run green.
