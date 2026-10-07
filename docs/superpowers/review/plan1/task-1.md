# Plan 1, Task 1: Toolchain, Go module and CI

## Implementation
- Engineer commit `efacab3` chore(go): start control-plane-go module with CI and lint config.
- Deviations: branch pre-created by the orchestrator (`git checkout -b` skipped); Homebrew Go 1.27.1 so `go.mod` says `go 1.27.1`; Docker wait loop capped at ~180 s; PATH export appended to `~/.zshrc`.
- Tools: go 1.27.1, golangci-lint 2.14.0, gosec 2.29.0, gitleaks 8.30.1, govulncheck v1.8.0. Docker 29.8.2.

## Round 0 reviews (commit efacab3)
- **Verifier: PASS.** Tools run in a fresh login shell, YAML parses, actionlint clean, `.env` ignored, key decodes to 32 bytes. VER-1 (advisory): don't push before Task 2 (empty module makes vet/lint/test exit non-zero). VER-2 (note): go 1.27.1 pin.
- **Security: FINDINGS (0 critical, 0 high, 1 medium, 3 low).**
  - SEC-1 medium: module-wide gosec `-exclude=G204,G304,G306`; G306 reason unrelated, G304 reason false (clone files are attacker-controlled, symlinks), bypasses the per-line `#nosec` rule.
  - SEC-2 low: actions on mutable tags, tools `@latest`.
  - SEC-3 low: checkout persists credentials.
  - SEC-4 low: `.env` mode 0644 holds `ENV_ENCRYPTION_KEY`.
- **Staff: CHANGES REQUESTED (0 critical, 2 high, 1 medium, 3 low).**
  - STAFF-1 high: `ui` job fails on pre-existing cp-ui errors (25 lint problems, 14 tsc errors).
  - STAFF-2 high: full-history gitleaks finds 3 false positives, `secrets` job would fail.
  - STAFF-3 medium: `go` job red on an empty module.
  - STAFF-4 low: `go-version-file` pins exact 1.27.1. STAFF-5 low: tool versions unpinned. STAFF-6 low: no `go.sum` yet (cache warning).

## Triage
- SEC-1, STAFF-2: fix. STAFF-1, SEC-1 scope, STAFF-3: sent to CTO (plan changes).
- CTO rulings (see `../decisions.md`, 2026-10-07): new **Task 1a** (green cp-ui baseline); gosec `-exclude=G204` only plus a shell-invocation guard, per-line `#nosec` rule and `os.OpenRoot` rule added to the Engineer brief, spec §5/§8 and Tasks 18/21/22; STAFF-3 resolved by a first-push gate (no code); SEC-3, STAFF-4, STAFF-5 and the tool half of SEC-2 promoted into the fix round; SEC-4 done by the orchestrator (`chmod 600 control-plane-go/.env`); rest of SEC-2 and STAFF-6 in `../backlog.md`.
- Orchestrator applied doc amendments in `fefe1be`.

## Fix round 1 (range fefe1be..2fa5b46)
- `7818fb8` ci(go): exclude only G204 in gosec and forbid shell invocation (SEC-1)
- `d79dede` ci(secrets): add .gitleaksignore for 3 triaged false positives (STAFF-2)
- `c52426d` ci: do not persist checkout credentials (SEC-3)
- `5562fbd` ci(go): build with the latest Go 1.27.x patch release (STAFF-4)
- `2fa5b46` ci(go): pin lint and security tool versions (STAFF-5, SEC-2 tool half)
- Deviation: gosec comment says "the step above" (guard precedes gosec) instead of the CTO's "next step".
- Re-checks: Security, Staff, Verifier dispatched.

## Fix round 1 re-checks (HEAD 2fa5b46)
- **Verifier: PASS.** Tools match pins; actionlint clean; shell guard passes on the module and fails on `sh`/`bash` fixtures; full-history gitleaks clean with the ignore file, exactly the 3 ignored fingerprints without it; `.env` mode 600 and ignored. Suite exits non-zero only for "no packages" (expected until Task 2, CTO STAFF-3).
- **Staff: APPROVE.** New lows STAFF-7 (pin gitleaks binary via `GITLEAKS_VERSION`) and STAFF-8 (guard misses `/bin/sh`) → backlog. Confirmed `persist-credentials: false` does not affect gitleaks-action.
- **Security: FINDINGS (0/0/0/1).** SEC-1, SEC-3, SEC-4 and the SEC-2 tool half fixed per ruling; independently confirmed the 3 gitleaks ignores are false positives (nodeToken HMAC recomputed with "test-secret"). New low SEC-5 (guard bypasses) → backlog.

## Result
**Done 2026-10-07.** Verifier PASS, no open critical/high/medium. Next: Task 1a. Do not push until the first-push gate (Task 1 fixes, Task 1a, Task 2) is met.
