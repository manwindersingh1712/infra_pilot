# Review backlog

Low-severity and deferred findings. Each line: date, task, finding ID, severity, description, roadmap phase.

- 2026-10-07, P1 T1, SEC-2, low, pin GitHub Actions by commit SHA and add dependabot for actions and gomod (tool-version half promoted to the Task 1 fix round by CTO), Plan 3 (hardening)
- 2026-10-07, P1 T1, SEC-3, low, `actions/checkout` keeps `persist-credentials: true`; add `persist-credentials: false` to every checkout step, Plan 3 hardening — **promoted by CTO to the Task 1 fix round**
- 2026-10-07, P1 T1, SEC-4, low, local `.env` files are mode 0644 and hold `ENV_ENCRYPTION_KEY`; `chmod 600` them and add that to setup docs, Plan 3 docs — **done 2026-10-07** (`chmod 600 control-plane-go/.env`; Task 1 Step 5 amended; `control-plane/.env` left as is, frozen dir)
- 2026-10-07, P1 T1, STAFF-4, low, `go-version-file` pins CI to exactly go 1.27.1, so stdlib CVEs will fail govulncheck until go.mod is bumped; consider `go-version: '1.27.x'` + `check-latest: true`, Plan 3 hardening — **promoted by CTO to the Task 1 fix round**
- 2026-10-07, P1 T1, STAFF-5, low, golangci-lint/govulncheck/gosec versions unpinned in CI (`latest`); pin and bump deliberately (same work as SEC-2), Plan 3 hardening — **promoted by CTO to the Task 1 fix round**
- 2026-10-07, P1 T1, STAFF-6, low, setup-go warns "Restore cache failed" until `go.sum` exists; expected, clears after first dependency, none
- 2026-10-07, P1 T1 fix round 1, STAFF-7, low, gitleaks-action downloads the latest gitleaks; add `GITLEAKS_VERSION: 8.30.1` to the step env (one line; bundle with the next ci.yml edit or SEC-2 SHA pinning), Plan 3 (hardening)
- 2026-10-07, P1 T1 fix round 1, STAFF-8, low, shell guard misses absolute paths (`"/bin/sh"`) and shell names in variables; widen literal to `"(/usr)?(/bin/)?(ba)?sh"` (CTO-owned regex), Security still checks runtime call sites, Plan 3 (hardening)
- 2026-10-07, P1 T1 fix round 1, SEC-5, low, shell guard grep is bypassable (/bin/sh, zsh/dash, `context.Background()` first arg, `filepath.Join` dir, `RunWithEnv`, space before paren, multi-line calls, variables, syscall.Exec, exec.Cmd literal); candidate wider regex `'(exec\.Command(Context)?|\.Run[A-Za-z]*) *\([^"]*"(/(usr/)?bin/)?(ba|da|z|k)?sh"'` or a go/analysis/ruleguard rule; meanwhile Security and Staff check "no shell via the runner" by hand in Tasks 15-22 (supersedes STAFF-8), Plan 3 (hardening)
