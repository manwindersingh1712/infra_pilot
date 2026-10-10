# Plan 1, Task 21: Dockerfile generation

## Implementation
- Engineer commit `780d4a4` feat(go): Dockerfile generation for node, next and react apps (`internal/worker/build/{dockerfile.go,dockerfile_test.go}`). Plan code plus: CTO SEC-1 `os.OpenRoot` access (escaping symlink = missing file), and an injection fix not in the plan: `cmdString()` JSON-encodes `scripts.start` for the nodemon `CMD ["sh","-c","…"]` path (plan code let `"]\nRUN …` add Docker instructions). Extra tests: symlink escapes (package.json, vite config), in-repo symlink allowed, start-script breakout, baseImage injection. Failed first for the right reasons; CI dry run green.

## Round 0 reviews (780d4a4)
- **Security: CHANGES REQUESTED (1 medium, 1 low note).** No Dockerfile injection found (baseImage strips to `[0-9a-zA-Z.-]` under a fixed `node:` prefix; cmdString safe incl. U+2028/0085/NUL; OpenRoot is the only repo access). SEC-1 T21 medium: `root.ReadFile("package.json")` + `Unmarshal` has no size cap; a multi-GB package.json can OOM the shared worker. Fix: open, Stat (regular file, ≤ ~1 MiB), decode through `io.LimitReader`; over cap → empty `PackageJSON`; test just over the cap. SEC-2 low (accepted): nodemon path puts the raw script in `sh -c` (tenant code in a tenant container). Root-user generated images accepted for T21 (byte-identical to Node); track as a requirement on Task 22 / spec §5: tenant containers `--cap-drop=ALL`, `no-new-privileges`, read-only rootfs where possible, memory/CPU/pids limits, and BuildKit builder sandbox limits (`RUN npm ci`/`npm run build` run tenant code).
- **Staff: CHANGES REQUESTED (1 medium, 3 low).** Parity with Node confirmed (same text, priority, nodemon single replace; trailing-newline behaviour same; Node had no package-manager/lockfile detection; Go widens vite config check to `.ts/.mjs/.mts`: improvement, recorded). cmdString is byte-identical for normal values; for values with `"` Node emitted invalid JSON (Docker fell back to shell form) so this is a fix. STAFF-1 T21 medium: no committed test for `GenerateDockerfile` (the empty-string return for docker/mongodb/unknown is what makes Task 22 skip writing a Dockerfile). STAFF-2 low: `Unmarshal` into a typed struct drops the whole package.json on any type mismatch (`"engines":"node >=14"`); Node's JSON.parse kept other fields. STAFF-3 low: version `^18 || ^20` → `18.020-alpine`, `>=` → `node:-alpine`; fall back to `20-alpine` if empty/not starting with a digit. STAFF-4 low: weak start-command tests (slice-tail panic risk; no table for special-character scripts that must pass unchanged).
- **Verifier: PASS.** Suite green. Real Docker 29: nodejs image built and served; react (vite.config.js → `dist`, nginx) served index.html and SPA route; nextjs not built (npm install next too slow; generator read only). Injection payloads (start and nodemon variants) never became a RUN layer (`ls /pwned` fails, `docker history` clean). Symlinked package.json treated as missing. VER-1 info: `npm ci --only=production` needs a lockfile and the flag is deprecated (same as Node).

## Fix round 1 plan
SEC-1 (size cap), STAFF-1 (GenerateDockerfile table test), STAFF-2 (tolerant field parsing), STAFF-3 (version fallback), STAFF-4 (tests). Backlog: SEC-2 note, root-user/isolation requirements for Task 22 + spec §5, VER-1.

## Fix round 1 (b7ee458..d3ae1b6)
- `b7ee458` SEC-1 (1 MiB cap, regular file, Open+f.Stat+LimitReader), `15c2abd` STAFF-1 (`TestGenerateDockerfile`, 10 rows), `c708157` STAFF-2 (per-field RawMessage parsing), `0a967a8` STAFF-3 (first digit-run rule, `defaultNodeTag = "20-alpine"` fallback), `d3ae1b6` STAFF-4 (named tables). Engineer CI dry run green; orchestrator ran `go test ./... -race` green.

## Round 1 re-check
- **Staff + Security (combined): CHANGES REQUESTED (1 medium, 1 low).** Original five resolved. **SEC-3 T21 (medium):** `root.Open` is a blocking O_RDONLY open, so a FIFO named `package.json` hangs the worker (reproduced: HANG after 10 s; the `IsRegular` check never runs). Needs a pre-build step or in-root symlink to a FIFO (git cannot store FIFOs), so narrow, but one tenant could wedge a worker goroutine forever. Fix: `root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)` then the `f.Stat` regular-file check (or `root.Lstat` first; symlinks need Stat); test with `syscall.Mkfifo` + timeout. **STAFF-5 T21 (low):** `baseImage` tag not length-capped (a 1 MiB digit run exceeds Docker's 128-char tag limit); fall back to the default if longer than ~64.

## Fix round 2 plan
SEC-3, STAFF-5. Check the same blocking-open pattern anywhere else a clone file is opened (Task 22 BuildAndPush uses `Lstat`+`OpenFile(O_EXCL)` for writing: fine).

## Fix round 2
- `bfabd03` SEC-3: `root.OpenFile(..., O_RDONLY|O_NONBLOCK)` + f.Stat regular-file check; tests (FIFO, in-repo symlink to FIFO, FIFO vite config) run under a 3 s timeout; red run showed both package.json tests hanging 3.00 s on the old code. `396e9c5` STAFF-5: tags longer than 64 chars fall back to `20-alpine` (rows at 63/64/65/5000).
- **Orchestrator check (diff is ~12 lines):** read the diff; `go vet`, gofmt, `go test -race -count=5` of the package green; Engineer's CI dry run green (govulncheck exit 0, gosec clean).

## Result
**PASSED 2026-10-10** after two fix rounds. Forward requirement for Task 22 (backlog): every read of a clone file must be non-blocking and regular-file-only (FIFO hang class).
