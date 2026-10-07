# Decisions log

Appended by the CTO reviewer (format in docs/superpowers/agents/cto.md).

## 2026-10-07 Plan 1 Task 1: cp-ui lint/build must be green before the first push (STAFF-1)
Context: The `ui` CI job added in efacab3 fails on pre-existing cp-ui problems: `npm run lint` reports 25 problems (20 errors, 5 warnings, e.g. @typescript-eslint/no-explicit-any) and `npm run build` reports 14 tsc errors (e.g. LandingPage.tsx(89,15) TS2769 motion `animate` target typing; ProjectCanvasPage.tsx(241,18) TS6133 unused `runAutoLayout`). Spec §8 requires the UI job.
Options considered: (a) new Task 1a "green cp-ui lint/build baseline", no behaviour change, before the first push; (b) `continue-on-error: true` on the ui job with a TODO until Plan 2.
Decision: fix now — change plan: insert Task 1a after Task 1, before the first push.
Why: A permanently red or ignored job trains everyone to ignore CI, and Plan 2 (Tasks 15-17) rewrites UI files; it needs a green baseline so its reviewers and the Verifier can tell new breakage from old. The fix is mechanical (types and dead code) and cheap now. Option (b) hides failures exactly when the UI starts changing.
Follow-ups:
- Plan 1 gets "Task 1a: Green cp-ui lint and build baseline". Files: `cp-ui/src/**` only. Rules: no behaviour or visual change; replace `any` with real types (or `unknown` plus narrowing); fix the motion typing by using the correct `animate` overload/target, not a cast to `any`; remove unreferenced locals such as `ProjectCanvasPage.runAutoLayout` (ServiceCanvas/useCanvas keep their own working copy); do not change `eslint.config.js` or `tsconfig*.json` to turn rules off; a per-line `// eslint-disable-next-line <rule> -- <reason>` is allowed only where a real type is impossible, and each one is listed in the report. Done when `cd cp-ui && npm ci && npm run lint && npm run build` exits 0 with 0 errors (warnings allowed, but listed). Commit: `chore(ui): fix existing lint and type errors so CI is green`.
- Reviewers for Task 1a: Staff, Verifier (lint, build, then `npm run dev` and load `/`, `/login`, `/projects`, `/projects/:id`, confirm landing animations and canvas still work, no new console errors) and Product (UI touched). Security only if `package.json`/lockfile changes (it must not).
- Plan 2 UI tasks keep `npm run lint && npm run build` green; the Verifier already runs them when `cp-ui/` changes (spec §13).

## 2026-10-07 Plan 1 Task 1: gosec exclusions, per-site #nosec and symlinks in cloned repos (SEC-1)
Context: CI runs `gosec -quiet -exclude=G204,G304,G306 ./...`. The G304/G306 reason ("paths come from our own config and IDs") is false: Task 21 reads `package.json` and `vite.config.*` and Task 22 writes `Dockerfile` inside an attacker-controlled clone. Worse than a read leak: Task 22's `BuildAndPush` does `os.Stat(repo/Dockerfile)` and, on ErrNotExist, `os.WriteFile`. A repo that commits a dangling symlink `Dockerfile -> /some/host/path` makes Stat return ErrNotExist (it follows the link) and WriteFile then follows the link and writes outside the clone on the worker host. Module-wide exclusions also bypass the Security brief's rule that `#nosec` needs a written reason.
Options considered: keep global exclusions; drop all three; drop G304/G306 and keep G204 excluded with a CI guard against shell invocation; make symlink handling a spec rule vs. a one-off fix in the build code.
Decision: fix now — change plan (Task 1 fix round, Engineer brief, Tasks 21 and 22) and change spec (§5 and §8 text below).
Why: The symlink write is a host-escape from user-controlled input, which is a security hole by our rules; fixing it in the plan text now costs nothing, fixing it after code exists costs a fix round per task. G204 cannot be per-site annotated usefully (the runtime package runs git/docker everywhere by design, always as argument lists), so a global exclusion plus a mechanical guard is the simpler equivalent. G304/G306 have few sites and each deserves its own reason.
Follow-ups:
- Task 1 fix round (Engineer), in `.github/workflows/ci.yml` go job: change the gosec step to `gosec -quiet -exclude=G204 ./...` with the comment `# G204 excluded: the runtime runs git/docker with argument lists by design. The next step forbids shell invocation. Every other finding is fixed or annotated "// #nosec Gxxx -- <reason>" at the site.` Add, before gosec, this step:
  ```yaml
      - name: Forbid shell invocation (G204 is excluded)
        run: |
          if grep -rnE --include='*.go' --exclude='*_test.go' \
            '(exec\.Command(Context)?\(([A-Za-z_][A-Za-z0-9_.]*, *)?|\.Run\([^,]+, *[^,]+, *)"(ba)?sh"' .; then
            echo "::error::Run programs directly with an argument list, never through sh/bash."; exit 1
          fi
  ```
  (Checked against the plan's code: it matches `Run(ctx, dir, "sh", ...)`, `exec.Command("sh", ...)` and `exec.CommandContext(ctx, "bash", ...)`; it does not match `docker run ... alpine sh -c <fixed literal>` in Task 16 `CopyIntoVolume`, container `Cmd` slices, or `_test.go` files, which are legitimate.)
- Engineer brief (`agents/engineer.md`), add rule 9: "Run `gosec -quiet -exclude=G204 ./...` before committing. Fix every finding, or put `// #nosec Gxxx -- <reason>` on that line. A bare `#nosec` or a new module-wide exclusion is not allowed; adding an exclusion is a CTO decision." Add rule 10: "Files inside a cloned repo are attacker-controlled. Open them only through `os.OpenRoot(<repo dir>)` (`root.ReadFile`, `root.Stat`, `root.Lstat`, `root.OpenFile`), never with `os.ReadFile`/`os.Stat`/`os.WriteFile` on a joined path."
- Plan 1 Task 21 (Dockerfile generation): `ReadPackageJSON` and `ReactOutputDir` open the repo with `os.OpenRoot(dir)` and use `root.ReadFile("package.json")` / `root.Stat(name)`; any error (missing, malformed, or a symlink escaping the root) behaves like "file missing". Add a test: a `package.json` symlink pointing to a file outside the temp repo yields `PackageJSON{}`.
- Plan 1 Task 22 (builder), in `BuildAndPush`: replace the Stat+WriteFile with `root, err := os.OpenRoot(repo)`; `if _, err := root.Lstat("Dockerfile"); errors.Is(err, fs.ErrNotExist) { f, err := root.OpenFile("Dockerfile", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) // #nosec G306 -- BuildKit must read it; contents are generated, not secret ... }`. Any existing entry, including a dangling symlink, means "repo has its own Dockerfile" and nothing is written; O_EXCL closes the race. Add a test: the clone contains a dangling `Dockerfile` symlink to a path in a second temp dir; after `BuildAndPush` that path does not exist.
- Plan 1 Task 18 (nginx routes): annotate `os.MkdirAll(..., 0o755)` and `os.WriteFile(..., 0o644)` with `// #nosec G301/G306 -- nginx (separate container, bind mount) must read route files; contents are generated from our IDs`, or tighten to 0o750/0o640 if the Verifier confirms nginx still reads them.
- Spec §5, add a table row: "| Build reads and writes files inside an untrusted cloned repo (symlinks can point anywhere on the worker host) | All access to the clone goes through `os.OpenRoot`; a generated Dockerfile is created with `O_EXCL` and never written through an existing entry. |"
- Spec §8, change "`govulncheck ./...`, `gosec ./...`" to "`govulncheck ./...`, `gosec ./...` (only G204 excluded; a CI step forbids `sh`/`bash` invocation in non-test code; any other suppression is a per-line `#nosec` with a reason)".
- Security brief needs no change; it already runs gosec with no exclusions and requires reasons.

## 2026-10-07 Plan 1 Task 1: go job is red on an empty module; first-push gate (STAFF-3)
Context: With no Go package yet, `go vet`, golangci-lint, `go test`, govulncheck and gosec exit non-zero on "no packages"/"no Go files", so the go job fails until Task 2 adds `internal/config`. Nothing is pushed yet.
Options considered: add a placeholder package or `if` guards in CI; or push nothing until Task 2 is committed.
Decision: fix now — change plan (process only, no code change). Confirmed as proposed.
Why: Placeholder code or conditional CI steps would outlive their purpose; Task 2 is the next task anyway. The branch is local, so holding the push costs nothing.
Follow-ups:
- Reword Plan 1 Task 1 Step 10: "Review gate. Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier. The Verifier checks that the tools run and the YAML parses. Do not push `go-migration` yet: the go job needs at least one package (Task 2) and the ui job needs Task 1a."
- First-push gate (orchestrator, before `git push -u origin go-migration`): Task 1 fix round committed (SEC-1, STAFF-2 `.gitleaksignore`, promoted lows below); Task 1a committed and passed; Task 2 committed and passed; and a local dry run is green: in `control-plane-go`: `go build ./... && go vet ./... && golangci-lint run && go test -race ./... && govulncheck ./... && gosec -quiet -exclude=G204 ./...` plus the shell-guard grep; `cd cp-ui && npm ci && npm run lint && npm run build`; `gitleaks detect --no-banner` at the repo root (full history). After the push, the orchestrator checks the Actions run with `gh run watch` and records the run URL in `review/plan1/task-2.md`.
- STAFF-2 (`.gitleaksignore` with the 3 fingerprints) is endorsed as triaged: each entry gets a comment line naming the file, commit and why it is a false positive (truncated example JWT; test fixture; curl prose).

## 2026-10-07 Plan 1 Task 1: lows triage adjustments
Context: Lows from the Task 1 review went to the backlog: SEC-2 unpinned actions/tools + dependabot, SEC-3 `persist-credentials: false`, SEC-4 `chmod 600 .env`, STAFF-4 exact Go pin via go-version-file, STAFF-5 unpinned tool versions, STAFF-6 missing go.sum warning.
Options considered: leave all in the backlog; promote the ones that are one-line edits to a file already being changed before the first push.
Decision: fix now for SEC-3, SEC-4, STAFF-4, STAFF-5 and the tool-version half of SEC-2 (in the Task 1 fix round); defer (Plan 3, hardening) SHA-pinning of actions and dependabot; STAFF-6 stays in the backlog and closes itself when Task 2/3 add dependencies.
Why: "Cheap now, expensive later": these are a few lines in `ci.yml` that is being edited anyway, and `@latest` tools make CI fail at random when a new gosec/golangci-lint release adds checks, which costs a fix round at an unrelated task. SHA-pinning plus dependabot is real supply-chain hardening but needs upkeep and is not in the spec §1 done criteria.
Follow-ups (Task 1 fix round, same commit series as SEC-1):
- Every `actions/checkout@v4` gets `with: persist-credentials: false` (the secrets job keeps `fetch-depth: 0`).
- `actions/setup-go`: replace `go-version-file` with `go-version: '1.27.x'` and `check-latest: true` (go.mod keeps its `go` line as the minimum).
- Pin tools to the exact versions installed locally in Step 1 (record them in the commit message): `golangci-lint-action` `version: v<x.y.z>`, `go install golang.org/x/vuln/cmd/govulncheck@v<x.y.z>`, `go install github.com/securego/gosec/v2/cmd/gosec@v<x.y.z>`.
- SEC-4: orchestrator runs `chmod 600 control-plane-go/.env` locally (untracked file, no commit); add `chmod 600 .env` to Plan 1 Task 1 Step 5.
- Backlog line: "2026-10-07, plan1 task-1, SEC-2, low, pin GitHub Actions by commit SHA and add dependabot for actions and gomod, Plan 3 (hardening)".
