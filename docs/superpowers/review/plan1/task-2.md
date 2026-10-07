# Plan 1, Task 2: Configuration

## Implementation
- Engineer commit `e1801fe` feat(go): config with Temporal, isolation and encryption settings (`internal/config/{config.go,config_test.go}`).
- Deviation: gofmt whitespace in the test's `cases` map. `config.go` is the plan code verbatim.
- First commit where the whole CI `go` job passes locally (build, vet, golangci-lint, test -race, govulncheck, shell guard, gosec).

## Round 0 reviews (commit e1801fe)
- **Verifier: PASS.** Real `.env` loads (RequireWorker/RequireAPI nil, RegistryHost localhost:5001, key 32 bytes); bad key + bad port list both errors without echoing the key; `NODE_ENV=production` → ExposeManagedPorts false; missing DATABASE_URL → error. Regression: ci.yml unchanged, full-history gitleaks clean, cp-ui lint/build green.
- **Security: FINDINGS (0/0/1/2).**
  - SEC-1 medium: unset APP_ENV and NODE_ENV → development → Plan 2 registers `/dev/login` and exposes managed ports (fails open).
  - SEC-2 low: AppEnv not validated; EXPOSE_MANAGED_PORTS=true allowed in production.
  - SEC-3 low: JWT_SECRET only non-empty; CONTAINER_PIDS 0/-1 (unlimited) accepted; port range unchecked.
  - Note: Config needs redaction (LogValue) before anything logs it.
- **Staff: CHANGES REQUESTED (0/0/2/4).**
  - STAFF-1 medium: same as SEC-1 (Node treated unset NODE_ENV as not development).
  - STAFF-2 medium: no range validation (health timeout 0 fails every deploy; inverted port range; pids 0).
  - STAFF-3 low: tests discard Load errors, TestLoadErrors doesn't check messages.
  - STAFF-4 low: inconsistent whitespace trimming.
  - STAFF-5 low: relative NGINX_ROUTES_DIR default depends on cwd.
  - STAFF-6 low: Node's `CORS_ORIGIN` ignored; unknown LOG_FORMAT silently text.

## Triage
- SEC-1/STAFF-1: reviewers disagreed on the fix → CTO. STAFF-2 changes plan code → CTO. Lows sent with them.
- **CTO rulings** (`../decisions.md`, three 2026-10-07 Task 2 entries): fix everything now, nothing to backlog. Unset env means production; AppEnv validated; EXPOSE_MANAGED_PORTS refused in production; range checks; JWT strength outside dev; trimming; CORS_ORIGIN fallback and `*` refused; LOG_FORMAT validated; `Config.LogValue`/`String` redaction now. The CTO wrote reference `config.go`/`config_test.go` in the session scratchpad; the Engineer owns and verifies the commit.
- Orchestrator doc amendments: spec §4 step 6 and §5 `/dev/login` row; Plan 1 Task 15 note, Task 26 `worker up` log line (+ `path/filepath` import, expected log text); Plan 2 `DevLogin` comment, `withCORS` exact-match note, api startup log adds `"config", cfg`. Task 2 code blocks in the plan are replaced with the committed files after the fix round.

## Fix round 1
- Engineer dispatched.
- `0ab3bd8` fix(go): fail-closed APP_ENV, validate config ranges, redact secrets in logs (SEC-1..3, STAFF-1..6, redaction note). Deviations: clearer CONTAINER_CPUS message; extra TestLoadErrors row (CPUS=2000). Tests failed against the old code first (25 TestLoadErrors rows, all 5 secrets leaked), then passed.

## Fix round 1 re-checks (HEAD 0ab3bd8)
- **Verifier: PASS.** Full CI go job green; real `.env` loads as development with secrets absent from slog JSON and `fmt` output (DB password masked `xxxxx`); bare env is production; every bad-input case rejected with the expected message; placeholder JWT refused in production, random 32+ byte secret accepted. Regression green.
- **Security: FINDINGS (0/0/0/2).** Original SEC-1..3 fixed per ruling. New lows SEC-1 (T2r1) (`%#v`, `json.Marshal`, holder structs bypass redaction) and SEC-2 (T2r1) (`redactURL` misses query/keyword/scheme-less forms) → backlog, with a reviewer rule for Tasks 15/26 and Plan 2: never store Config in another struct or `%#v`/`json.Marshal` it.
- **Staff: APPROVE.** STAFF-1..6 closed. STAFF-7 medium (plan text): Plan 2 cutover item 10 would fail on the placeholder JWT → orchestrator fixed the plan text. STAFF-11 low (plan text): `filepath.Abs` error dropped, expected log text wrong, Task 2 plan code stale → orchestrator fixed all three (Task 2 plan code blocks now equal the committed files, with an amendment note). STAFF-8/10 = Security lows; STAFF-9 → backlog.

## Result
**Done 2026-10-07.** Verifier PASS, no open critical/high/medium. First-push gate (Task 1 fixes, Task 1a, Task 2) is met; push needs user approval.

## First push (user-approved 2026-10-07)
- `git push -u origin go-migration` at `77eeb6a` (14 commits ahead of master). Pre-push full-history gitleaks: no leaks.
- GitHub Actions run: https://github.com/manwindersingh1712/infra_pilot/actions/runs/37620641256 → **success** (go: success, ui: success, secrets: success).
