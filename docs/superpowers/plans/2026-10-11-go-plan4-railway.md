# Plan 4: Railway-style services (builds, settings, databases, variables, networking)

> **For agentic workers:** REQUIRED: execute this plan with the team orchestration in spec §13 (`docs/superpowers/specs/2026-10-07-go-temporal-design.md`). The Engineer follows `docs/superpowers/agents/engineer.md`, test first, then code, then commit. After every task, Security, Staff and Verifier review in parallel; **Product** also reviews every task that changes API responses or the UI. Records go in `docs/superpowers/review/plan4/task-<n>.md`. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move Infra Pilot from "pick a JS service type" to Railway's model. Give it a repo in any language and it deploys: Dockerfile if present, otherwise Railpack. It gets Railway's settings (root dir, build/start/pre-deploy commands, healthcheck, restart policy, target port), config as code, `PORT` injection, live build logs, managed Postgres/MySQL/Redis/Mongo with connection variables, reference and shared variables, private networking, and public-image services. Where cheap, it goes further than Railway: a detect preview before the first deploy, real listening-port discovery, plain-English build hints, `railway.json`/`railway.toml` compatibility, and canvas connections that offer reference variables.

**Architecture:** The worker's build step (`internal/worker/build`) gains `Detect` (builder choice and Railpack `prepare`) and builds either the repo Dockerfile or the Railpack plan in the existing isolated buildx builder (`cp-builder`). Service settings live in new `Service` columns, merged with an optional repo config file (repo wins, as on Railway). The deploy workflow gains a pre-deploy step, HTTP health checks and port discovery behind `workflow.GetVersion`. Variables are resolved inside the activities (`${{svc.VAR}}`, `${{shared.VAR}}`, `INFRAPILOT_*`), so plaintext never enters workflow history. The API and UI from Plan 2 get the new create flow, a Settings tab and the detect preview.

**Tech Stack:** Go, Temporal Go SDK, Railpack CLI **v0.40.1** (MIT; BuildKit frontend `ghcr.io/railwayapp/railpack-frontend:v0.40.1`), docker buildx, `github.com/BurntSushi/toml`, Postgres 16 (goose), React 19 + Vite UI.

**Spec:** this document is the design record (no separate spec). Background: the 2026-10-11 discussion. Railway uses Railpack (successor of Nixpacks) when there is no Dockerfile, injects `PORT`, and supports config as code and reference variables. Sources: https://blog.railway.com/p/introducing-railpack, https://railpack.com/platforms/running-railpack-in-production, https://railpack.com/config/environment-variables, https://github.com/railwayapp/railpack.

---

## Prerequisites (do not start before all are true)

- Plans 1, 2 and 3 are executed and their gates passed (START-HERE §5 tracker).
- The Plan 1 rollback window is closed, the Go API serves the UI, and Node is frozen (`control-plane/FROZEN.md`).
- A fresh DB backup exists (`pg_dump`), plus the `ENV_ENCRYPTION_KEY` backup.
- Railpack installed locally at the pinned version (Task 3 adds the script).

## Task 0: Refresh this plan against the code as it is at pickup time

This plan was written on 2026-10-11 while Plan 2 was at Task 3. Plans 2-3 will change files it touches.

- [ ] Re-read: `control-plane-go/internal/api/services.go`, `internal/api/canvas.go` (or wherever Plan 2 put the canvas handlers), `internal/workflows/{activities.go,deploy.go,names.go}`, `internal/worker/build/builder.go`, `internal/runtime/{docker.go,exec.go}`, `internal/store/{models.go,services.go,deployments.go,envvars.go}`, `cp-ui/src/lib/api.ts`, `cp-ui/src/components/{CreateServiceModal.tsx,ServiceDetailsPanel.tsx}`, the worker Dockerfile and compose file Plan 3 created, and the latest migration number in `control-plane-go/migrations/`.
- [ ] Check the latest Railpack release (`gh api repos/railwayapp/railpack/releases/latest --jq .tag_name`). If it's newer than v0.40.1, pin the newer one everywhere this plan says v0.40.1 and record why in `review/plan4/task-0.md`.
- [ ] Update the file paths, migration numbers and function names in this plan to match, and record each deviation in `review/plan4/task-0.md`. A change of design goes to the CTO (`review/decisions.md`).
- [ ] Commit the refreshed plan by explicit path.

## Global Constraints

- Never edit or delete `control-plane/` (frozen Node reference).
- Goose owns the schema. Never run `prisma migrate`.
- Every new `docker run` keeps `--cap-drop ALL`, `--security-opt no-new-privileges`, the `Limits` (memory, CPUs, pids) and a project network, just like `runtime.Docker.runArgs` today.
- Plaintext secrets (env values, generated passwords, build secrets, repo tokens) never appear in workflow inputs/results/heartbeats, command arguments, logs or error messages. The existing rule: errors name keys, never values.
- Untrusted repo content is only read by `railpack prepare` (static analysis, no repo code runs) and the config-file parser, and only built inside `cp-builder`. Repo files are opened through `os.Root` (no symlink escape). Config files are capped at 64 KiB.
- The Railpack CLI version and frontend image tag are the same pinned version.
- API contract as in Plan 2: camelCase JSON, `[]` for empty lists, `{"error": "..."}`, foreign or missing ID → 404.
- UI: inline styles only, the `motion` library for animation, all calls through `cp-ui/src/lib/api.ts`.
- Tests run with `-race`. Docker, Railpack and DB tests run in the background with a log file (subagent watchdog rule).
- Stage by explicit path. Never stage `ruvector.db`, `agentdb.rvf*`, `README_NEW.md` or `.env`. Ask the user before pushing, before the Part 1 conversion, and before anything destructive.

## Review Focus (inputs no single task's happy path covers)

1. **App ignores `PORT`** (hard-coded 3000, or Vite preview on 4173): it must still route, through port discovery (Task 9), with a visible warning. It must not time out silently.
2. **Monorepo with a root directory**, including `rootDirectory` = `../x`, an absolute path, or a symlink out of the repo: reject with 400 / non-retryable `invalid_root_directory` (Tasks 2 and 4).
3. **Repo has both a Dockerfile and a `railway.json` with `builder: RAILPACK`**: the repo config wins, and the UI shows why (Tasks 2, 4 and 12).
4. **A secret value equal to a common word** ("production", "true"): scrubbing must not mangle build logs. Only values of 8+ characters are scrubbed, and the test pins this (Task 7).
5. **Reference to a renamed or deleted service** (`${{db.DATABASE_URL}}` after `db` is renamed): the deploy fails before the pull with `unknown_reference: DATABASE_URL -> db`. It never deploys with an empty value (Part 2 Task 6).

---

## Railway parity (target)

| Area | Railway | Infra Pilot after Plan 4 |
|---|---|---|
| Builder | Dockerfile if present, else Railpack | Same: explicit setting → Dockerfile at `dockerfilePath`/root → Railpack v0.40.1 |
| Languages | Node, Bun, Deno, Python, Go, PHP, Ruby, Java, .NET, Rust, Elixir, static/SPA, shell | Same set (Railpack) |
| Service kinds | Repo, Docker image, Database, Empty | `app`, `image`, `postgres`, `mysql`, `redis`, `mongodb` |
| Overrides | Root dir, build/start cmd, Dockerfile path, pre-deploy, healthcheck, restart policy, target port | Same, Settings tab |
| Config as code | `railway.json` / `railway.toml` | `infrapilot.json`, **plus `railway.json` / `railway.toml`** |
| Port | Injects `PORT`; manual target port | `PORT=8080` injected; **listening-port discovery** with a warning |
| Build logs | Live | Live (`source: "build"` in the Redis hot tier) |
| Detection | Visible in build logs | **Detect preview before the first deploy** |
| Failures | Raw logs | Raw logs **plus one-line hints** |
| Variables | Service, shared, reference, `RAILWAY_*`, available at build | Same, `INFRAPILOT_*`, build secrets |
| Canvas | No action on connect | **Connect app → DB offers the reference variable** |
| Private network | `<svc>.railway.internal` | `<slug>.internal` |
| Databases | Postgres, MySQL, Redis, Mongo | Same, generated passwords, published URLs |

**Out of scope (later plans):** GitHub OAuth, deploy on push and watch paths (roadmap Phase 3); custom domains, TCP proxy and HTTPS (Phase 1); replicas, cron services, app volumes, usage limits and billing (Phase 5); multi-VM (Phase 6). Nothing here blocks them: settings are per service, and builds still go through `ImageBuilder`.

## Existing code to reuse (do not rewrite)

- `internal/worker/build/builder.go`: `BuildAndPush`, `checkoutRef`, `git`, `gitSafe`, `EnsureBuilder`, `fail` (scrubbing), `writeDockerfile`'s `os.Root` pattern.
- `internal/workflows/activities.go`: `BuildImage`, `RunContainer`, `WaitHealthy`, `SwitchRoute`, `RevertRoute`, `EnsureCredentials`, `EnsureVolume`, `decryptedEnv`, `merge`, `keepAlive`.
- `internal/workflows/names.go`: `Managed`, `ManagedImage`, `ServiceAlias`, `NetworkName`, `VolumeName`, `TenantCaps`.
- `internal/runtime/docker.go`: `RunSpec`, `runArgs`, `Probe` (busybox `ProbeImage`), `DetectContainerPort` (kept as a hint), `checkImage`, `checkName`. `internal/runtime/exec.go`: `Runner`, `ExecRunner`, bounded `headBuffer`/`tailBuffer`.
- `internal/store`: `Type*` consts, `IsManaged`, `NeedsRepo`, `ValidServiceType`, `Job`/`GetJob`, `MarkBuilt`, `EnvMap`, `InsertEnvVarIfAbsent`.
- `internal/secrets`: `Box.Encrypt`, `Box.Decrypt`, `RandomPassword`. `internal/gitsrc`: `Validate`, `ValidImageTag`, `Scrub`, `RedactURL`.
- `internal/logs/redis.go`: `Redis.Write(ctx, logs.Entry{DeploymentID, Source, Message, Timestamp})`.
- `testutil.FakeRunner`, `testutil.Seed`, the Plan 2 API harness (`newHarness`, `hs.do`, `hs.token`, `expectStatus`).

## File structure (new or changed)

```
control-plane-go/
  migrations/00004_railway.sql               # Part 1 (renumber in Task 0 if needed)
  migrations/00005_railway_data.sql          # Part 2
  internal/svcconfig/{settings.go, repoconfig.go, merge.go}      (+ _test.go)
  internal/worker/build/{detect.go, railpack.go, hints.go, builder.go (changed), dockerfile.go (deleted in Task 13)}
  internal/worker/build/testdata/repos/{express,nextjs,vite-spa,fastapi,gohttp,dockerfile,monorepo,ignores-port}/
  internal/worker/build/testdata/railpack-info-*.json            # golden files captured from the real CLI
  internal/runtime/{exec.go (RunEnv, RunStream), docker.go (ListeningPorts, HTTPProbe, RunOnce, restart)}
  internal/runtime/netstat.go                                     # /proc/net/tcp parser
  internal/workflows/{activities.go, deploy.go, detect.go, names.go, vars.go}
  internal/store/{models.go, services.go, deployments.go, projectvars.go}
  internal/api/{services.go, settings.go, detect.go, variables.go}
  cmd/migrate/convert.go                                          # convert-services
  scripts/install-railpack.sh
cp-ui/src/
  components/NewServiceMenu.tsx, DetectCard.tsx, ServiceSettingsTab.tsx, VariablesTab.tsx
  components/{CreateServiceModal.tsx, ServiceDetailsPanel.tsx} (changed)
  lib/api.ts (new calls)
```

---

# Part 1: Railway-style builds

### Task 1: Migration and store for service settings

**Files:**
- Create: `control-plane-go/migrations/00004_railway.sql`
- Modify: `internal/store/models.go`, `internal/store/services.go`, `internal/store/deployments.go`
- Test: `internal/store/services_test.go`, `internal/store/deployments_test.go`, `internal/db` migration test (expected goose rows come from the embedded migrations, so nothing to edit there)

**Interfaces:**
- Produces: `store.TypeApp = "app"`, `store.TypeImage = "image"`, `store.TypePostgres = "postgres"`, `store.TypeMySQL = "mysql"`; `store.IsLegacyBuildType(t) bool` (nodejs/nextjs/react/docker); `store.ServiceSettings{RootDirectory, Builder, DockerfilePath, BuildCommand, StartCommand, PreDeployCommand, HealthcheckPath *string; HealthcheckTimeout, RestartMaxRetries, TargetPort *int; RestartPolicy string; Image *string}`; `Service.Settings ServiceSettings` (JSON `settings`); `(*Store).UpdateServiceSettings(ctx, id string, s ServiceSettings) error`; `Job.Settings ServiceSettings`, `Job.Image` for image services as `Job.SourceImage *string`; `Deployment.Builder *string`, `Deployment.BuildInfo json.RawMessage`, `Deployment.Warnings []string`; `(*Store).MarkBuilt(ctx, id, image string, builder string, info json.RawMessage) error` (signature change); `(*Store).AddDeploymentWarning(ctx, id, msg string) error`.

Migration (`-- +goose NO TRANSACTION`, because `ALTER TYPE ... ADD VALUE` can't share a transaction with statements that use the new values):
```sql
-- +goose NO TRANSACTION
-- +goose Up
ALTER TYPE "ServiceType" ADD VALUE IF NOT EXISTS 'app';
ALTER TYPE "ServiceType" ADD VALUE IF NOT EXISTS 'image';
ALTER TYPE "ServiceType" ADD VALUE IF NOT EXISTS 'postgres';
ALTER TYPE "ServiceType" ADD VALUE IF NOT EXISTS 'mysql';
ALTER TABLE "Service"
  ADD COLUMN "rootDirectory" TEXT,
  ADD COLUMN "builder" TEXT NOT NULL DEFAULT 'auto' CHECK ("builder" IN ('auto','dockerfile','railpack')),
  ADD COLUMN "dockerfilePath" TEXT,
  ADD COLUMN "buildCommand" TEXT,
  ADD COLUMN "startCommand" TEXT,
  ADD COLUMN "preDeployCommand" TEXT,
  ADD COLUMN "healthcheckPath" TEXT,
  ADD COLUMN "healthcheckTimeout" INTEGER CHECK ("healthcheckTimeout" BETWEEN 1 AND 600),
  ADD COLUMN "restartPolicy" TEXT NOT NULL DEFAULT 'on_failure' CHECK ("restartPolicy" IN ('on_failure','always','never')),
  ADD COLUMN "restartMaxRetries" INTEGER NOT NULL DEFAULT 10 CHECK ("restartMaxRetries" BETWEEN 0 AND 100),
  ADD COLUMN "targetPort" INTEGER CHECK ("targetPort" BETWEEN 1 AND 65535),
  ADD COLUMN "image" TEXT;
ALTER TABLE "Deployment"
  ADD COLUMN "builder" TEXT,
  ADD COLUMN "buildInfo" JSONB,
  ADD COLUMN "warnings" JSONB NOT NULL DEFAULT '[]';
-- +goose Down
ALTER TABLE "Deployment" DROP COLUMN "warnings", DROP COLUMN "buildInfo", DROP COLUMN "builder";
ALTER TABLE "Service" DROP COLUMN "image", DROP COLUMN "targetPort", DROP COLUMN "restartMaxRetries",
  DROP COLUMN "restartPolicy", DROP COLUMN "healthcheckTimeout", DROP COLUMN "healthcheckPath",
  DROP COLUMN "preDeployCommand", DROP COLUMN "startCommand", DROP COLUMN "buildCommand",
  DROP COLUMN "dockerfilePath", DROP COLUMN "builder", DROP COLUMN "rootDirectory";
-- enum values cannot be dropped; they stay unused after Down.
```
Today's `restart unless-stopped` corresponds to `always`. The `on_failure` default is Railway's. Note it as a behaviour change for Product, since existing services keep `unless-stopped` until Task 10 reads the column.

- [ ] **Step 1: Failing tests.** `TestServiceSettingsRoundTrip`: create an `app` service, `UpdateServiceSettings` with every field set, and `GetService` returns them; JSON has `settings.rootDirectory` etc. and `null` for unset. `TestServiceSettingsCheckConstraints`: `targetPort` 0 / 70000, `healthcheckTimeout` 601, `builder` "nixpacks" → error. `TestMarkBuiltStoresBuildInfo`: `buildInfo` round-trips as JSON, `builder` set. `TestAddDeploymentWarning`: two warnings → `["a","b"]`. `TestEnumValuesAfterMigrate`: `INSERT ... 'app'`, `'image'`, `'postgres'`, `'mysql'` succeed. `TestMigrateDownUp00004`: goose down one + up one leaves the schema identical (compare `information_schema.columns`).
- [ ] **Step 2:** Run `go test -race ./internal/store/ ./internal/db/` in the background with a log. Expected: FAIL (unknown columns).
- [ ] **Step 3:** Write the migration, the struct fields, `serviceCols`/`scanService`, `GetJob` (join the settings and `image`), `UpdateServiceSettings`, the new `MarkBuilt`, and `AddDeploymentWarning` (`jsonb` append, `"warnings" || to_jsonb($2::text)`). Update `IsManaged` to include postgres/mysql, `NeedsRepo` to include `app` (plus legacy types), and `ValidServiceType`.
- [ ] **Step 4:** Tests pass. Fix every `MarkBuilt` caller (`activities.go`): pass `""` and `nil` until Task 5.
- [ ] **Step 5:** Commit `feat(go): service settings, new service types and build info columns (00004)`.
- [ ] **Review gate.**

### Task 2: `internal/svcconfig`: settings validation, repo config files, merge

**Files:**
- Create: `internal/svcconfig/settings.go`, `repoconfig.go`, `merge.go` + tests
- Modify: `go.mod` (`github.com/BurntSushi/toml`)

**Interfaces:**
- Consumes: `store.ServiceSettings`.
- Produces: `svcconfig.Validate(s store.ServiceSettings) error` (errors wrap `svcconfig.ErrInvalid` and name the field); `svcconfig.RepoConfig` (same fields as pointers, plus `Source string`, the file name); `svcconfig.LoadRepoConfig(root *os.Root) (RepoConfig, error)`; `svcconfig.Effective{Settings store.ServiceSettings; Origin map[string]string}` (field name → `"dashboard"` | `"infrapilot.json"` | `"railway.json"` | `"railway.toml"` | `"default"`); `svcconfig.Merge(dash store.ServiceSettings, repo RepoConfig) Effective`.

Rules:
- `rootDirectory`: relative, `filepath.Clean`, no `..` element, no NUL, ≤ 255 bytes, `""` and `"."` mean the repo root. It's checked again against the real tree with `os.Root` in Task 4.
- Commands: ≤ 4096 bytes, no NUL, no newline in `startCommand`.
- `healthcheckPath`: starts with `/`, ≤ 512 bytes, printable ASCII.
- `dockerfilePath`: same rule as `rootDirectory`, relative to the root directory.
- Ports and timeouts are in the DB CHECK ranges. `restartPolicy` is one of the three values.
- File lookup order: `infrapilot.json`, then `railway.json`, then `railway.toml`. The first one present wins, and only one is read. Size cap 64 KiB (`ErrInvalid`: "config file too large"). Opened with `root.Open` (no symlink escape); a symlinked config file is rejected.
- Railway mapping: `build.builder` (`DOCKERFILE`→dockerfile, `RAILPACK`→railpack, `NIXPACKS`→railpack, with a warning "NIXPACKS is treated as RAILPACK"), `build.buildCommand`, `build.dockerfilePath`, `deploy.startCommand`, `deploy.preDeployCommand` (string, or an array joined with ` && `, as Railway allows), `deploy.healthcheckPath`, `deploy.healthcheckTimeout`, `deploy.restartPolicyType` (`ON_FAILURE`/`ALWAYS`/`NEVER`), `deploy.restartPolicyMaxRetries`. Unknown keys are ignored but collected into `RepoConfig.Ignored []string`, so the UI can say "ignored: deploy.numReplicas". `infrapilot.json` uses the same shape (`build`, `deploy`), plus `rootDirectory` is *not* allowed in the file (Railway sets it outside the repo too).
- Merge: a repo value overrides the dashboard (Railway behaviour). Every field records its origin.

- [ ] **Step 1: Failing tests (table-driven).** Validate: each bad input above → `ErrInvalid` naming the field; good inputs pass. LoadRepoConfig on `t.TempDir()` trees: none → zero `RepoConfig`, `Source == ""`; `railway.json` with all fields → mapped; `railway.toml` same content → identical result; `infrapilot.json` and `railway.json` both present → infrapilot wins; a 65 KiB file → error; invalid JSON → `ErrInvalid` "railway.json: invalid JSON" (no file content echoed); `railway.json` as a symlink to `/etc/passwd` → error; `preDeployCommand` as an array → joined; `NIXPACKS` → railpack plus warning; unknown keys → `Ignored`. Merge: dashboard `startCommand=a` + repo `b` → `b`, origin `railway.json`; dashboard only → origin `dashboard`; neither → origin `default`.
- [ ] **Step 2:** Run → FAIL. **Step 3:** Implement. **Step 4:** Pass (`go test -race ./internal/svcconfig/`). **Step 5:** Commit `feat(go): service settings validation and railway.json/railway.toml/infrapilot.json config`.
- [ ] **Review gate.**

### Task 3: Railpack toolchain (pinned CLI + frontend)

**Files:**
- Create: `control-plane-go/scripts/install-railpack.sh`
- Modify: `internal/config/config.go` (+ test), `cmd/worker/main.go`, the worker Dockerfile from Plan 3, `.claude/CLAUDE.md` env-var list

**Interfaces:**
- Produces: `config.Config.RailpackBin` (`RAILPACK_BIN` [railpack]), `config.Config.RailpackVersion` (`RAILPACK_VERSION` [0.40.1], `^\d+\.\d+\.\d+$`), `config.Config.RailpackFrontend()` → `"ghcr.io/railwayapp/railpack-frontend:v" + RailpackVersion`; `config.Config.RailpackPrepareTimeout` (`RAILPACK_PREPARE_TIMEOUT_SECONDS` [120, max 600]); `build.CheckRailpack(ctx, r runtime.Runner, bin, version string) error`.

- `install-railpack.sh <version> <dest-dir>` downloads the release asset for the host OS/arch from `https://github.com/railwayapp/railpack/releases/download/v<version>/`, verifies its sha256 against the release's checksum file (aborts on mismatch or a missing checksum), and installs to `<dest-dir>/railpack`. No `curl | sh`. It uses `set -euo pipefail` and works on macOS (no `timeout`; use `curl --max-time`).
- The worker Dockerfile runs the same script at the pinned version and fails the image build if the checksum fails.
- `CheckRailpack` runs `railpack --version` and requires the version string to contain the pinned version. The worker exits at startup with a clear message otherwise ("railpack 0.39.0 found, 0.40.1 required: run scripts/install-railpack.sh 0.40.1 ~/bin").

- [ ] **Step 1: Failing tests.** Config defaults and bounds; `RAILPACK_VERSION=latest` → config error; `RailpackFrontend()` value. `CheckRailpack` with `FakeRunner`: matching output → nil; other version → error naming both; binary missing → error naming `RAILPACK_BIN`.
- [ ] **Step 2-4:** FAIL → implement → pass. Run the script locally (`scripts/install-railpack.sh 0.40.1 "$HOME/bin"`), then `railpack --version`.
- [ ] **Step 5:** Commit `feat(go): pinned railpack CLI and frontend`.
- [ ] **Review gate** (Security: the script's checksum handling).

### Task 4: `build.Detect`: builder choice and Railpack prepare

**Files:**
- Create: `internal/worker/build/detect.go`, `railpack.go`, `hints.go` + tests; `testdata/repos/*` fixtures; `testdata/railpack-info-*.json` golden files
- Test: `detect_test.go` (unit, FakeRunner), `detect_integration_test.go` (`//go:build integration`, real CLI, skipped when `railpack` is not on PATH)

**Interfaces:**
- Consumes: `svcconfig.LoadRepoConfig`, `svcconfig.Merge`, `svcconfig.Validate`, `store.ServiceSettings`.
- Produces:
```go
type DetectInput struct {
    RepoDir   string               // clone root
    WorkDir   string               // where plan.json / info.json go (outside RepoDir)
    Dashboard store.ServiceSettings
    BuildEnv  map[string]string    // decrypted service vars; passed to prepare via env only
}
type DetectResult struct {
    Builder     string            `json:"builder"`     // "dockerfile" | "railpack"
    ContextDir  string            `json:"-"`           // absolute build context (root dir applied)
    Dockerfile  string            `json:"dockerfile,omitempty"` // relative to ContextDir
    PlanPath    string            `json:"-"`
    Providers   []string          `json:"providers"`   // e.g. ["node"]
    Versions    map[string]string `json:"versions"`    // e.g. {"node":"22.11.0"}
    InstallCmd  string            `json:"installCommand,omitempty"`
    BuildCmd    string            `json:"buildCommand,omitempty"`
    StartCmd    string            `json:"startCommand,omitempty"`
    Hints       []string          `json:"hints"`
    Effective   svcconfig.Effective `json:"settings"`
    ConfigFile  string            `json:"configFile,omitempty"`
}
func (b Builder) Detect(ctx context.Context, in DetectInput) (DetectResult, error)
```
- Errors: `ErrInvalidRoot` (wraps `gitsrc.ErrInvalid`, so `BuildImage` maps it to non-retryable `invalid_build_source`) and `ErrDetectFailed{Hints []string}` (non-retryable `detect_failed`, with hints as the message).

Logic:
1. `os.OpenRoot(RepoDir)`. Resolve `rootDirectory` with `root.OpenRoot(dir)`. A failure (missing, outside, symlink escape) → `ErrInvalidRoot`.
2. `LoadRepoConfig` from the *root directory* (as Railway does), then `Merge` and `Validate`.
3. Builder: `Effective.Builder == "dockerfile"` → requires the Dockerfile (`dockerfilePath` or `Dockerfile`) to exist as a regular file (`Lstat`), else `ErrDetectFailed{"builder is dockerfile but <path> was not found"}`. `"railpack"` → Railpack. `"auto"` → Dockerfile if one is present, else Railpack.
4. Railpack: run `railpack prepare <ContextDir> --plan-out <WorkDir>/railpack-plan.json --info-out <WorkDir>/railpack-info.json` with `RAILPACK_BUILD_CMD` / `RAILPACK_START_CMD` from `Effective` passed as `--env K=V` (commands aren't secret). User build vars are passed through `RunEnv` as process env, never args (Task 6 adds `RunEnv`; until then, pass none). Timeout `RailpackPrepareTimeout`.
5. Parse `railpack-info.json` (cap 4 MiB): `success`, `detectedProviders`, `resolvedPackages` (name → resolved version), `metadata`, `logs` (level + msg), and the plan's deploy `startCommand`. `success == false` or an empty start command → `ErrDetectFailed` with hints.
6. `hints.go`: maps Railpack log messages and conditions to one-liners. Golden-test each rule:
   - no start command → "No start command found. Add a `start` script to package.json, a Procfile, or set Start Command in Settings."
   - no lockfile for node → "No lockfile found; installs are slower and not reproducible. Commit package-lock.json, pnpm-lock.yaml or yarn.lock."
   - unsupported or unknown version → "Requested <pkg> <ver> is not available; set a supported version."
   - nothing detected → "Could not detect a language. Add a Dockerfile, or see the supported languages: https://railpack.com."
   Unknown messages pass through unchanged (truncated to 300 chars).

- [ ] **Step 1: Capture golden files.** Run the real CLI over each fixture (`railpack prepare testdata/repos/<x> --plan-out /tmp/p.json --info-out testdata/railpack-info-<x>.json`) and commit the info files. Fixtures: `express` (package.json with a start script + lockfile), `nextjs`, `vite-spa`, `fastapi` (requirements.txt + main.py), `gohttp` (go.mod + main.go), `dockerfile` (Dockerfile only), `monorepo` (`apps/api/package.json`, `apps/web/...`), `ignores-port` (listens on 3000 regardless of PORT), `no-start` (package.json without a start script), `empty` (README only).
- [ ] **Step 2: Failing unit tests (FakeRunner returns the golden files).** Builder choice table: auto+Dockerfile → dockerfile; auto+none → railpack; dockerfile+missing → `ErrDetectFailed`; railpack+Dockerfile present → railpack; repo `railway.json` `builder: DOCKERFILE` overrides dashboard `railpack`. Root dir: `apps/api` → `ContextDir` ends in `apps/api`; `../x`, `/etc`, and a symlink `apps/evil -> /` → `ErrInvalidRoot`. Parse: express → providers `["node"]`, a node version, a start cmd; `no-start` → `ErrDetectFailed` with the start hint; `empty` → the could-not-detect hint. Args: prepare is called with `--env RAILPACK_START_CMD=...` only when set, and no user var values appear in args (sentinel check). Info file > 4 MiB → error.
- [ ] **Step 3: Integration test** (`go test -tags integration -race ./internal/worker/build/ -run TestDetectReal`, background + log): every fixture gives the expected builder and provider with the real CLI.
- [ ] **Step 4:** Implement. **Step 5:** Unit and integration pass. **Step 6:** Commit `feat(go): build.Detect picks Dockerfile or Railpack and explains failures`.
- [ ] **Review gate** (Security: os.Root use, args, size caps).

### Task 5: Build with Railpack in the isolated builder; store build info

**Files:**
- Modify: `internal/worker/build/builder.go` (+ tests), `internal/workflows/activities.go` (`BuildImage`, `ImageBuilder` interface) (+ tests)

**Interfaces:**
- Consumes: `Detect`, `store.MarkBuilt(ctx, id, image, builder, info)`.
- Produces: `build.Spec` gains `Settings store.ServiceSettings`, `ServiceID string`, `BuildEnv map[string]string`; `func (b Builder) BuildAndPush(ctx context.Context, s Spec) (BuildResult, error)`; `type BuildResult struct { Detect DetectResult }`; `ImageBuilder.BuildAndPush` returns `(build.BuildResult, error)`; `BuildImage` returns `BuildOutput{Image string; Builder string; Info json.RawMessage}`, and the workflow passes it to `MarkBuilt` (the info holds only the public `DetectResult` JSON: no env, no paths).

Build commands (always `--builder <b.Name> --load -t <image>`):
- dockerfile: `docker buildx build --builder cp-builder -f <ContextDir>/<Dockerfile> --load -t <image> <ContextDir>`
- railpack: `docker buildx build --builder cp-builder --build-arg BUILDKIT_SYNTAX=<RailpackFrontend> -f <PlanPath> --build-arg cache-key=<serviceId> --load -t <image> <ContextDir>`

Legacy types (`nodejs`/`nextjs`/`react`) keep today's `GenerateDockerfile` path unchanged until Task 13 converts them. `docker` behaves like `app` with `builder=dockerfile`.

Workflow change: `MarkBuilt` now takes the builder and info. This changes activity input shapes, so use `workflow.GetVersion(ctx, "build-info", workflow.DefaultVersion, 1)` and add a replay history (`testdata/`) recorded with the new code. The existing replay test must still pass.

- [ ] **Step 1: Failing tests.** FakeRunner sees the exact argv for both paths (frontend tag from config, `cache-key=<serviceId>`, context = root dir). Legacy `react` still writes the generated Dockerfile. `BuildImage` stores the builder and info. The info JSON contains no `BuildEnv` sentinel and no absolute work-dir path. Replay test with the old and new histories.
- [ ] **Step 2-4:** FAIL → implement → pass (`go test -race ./internal/worker/build/ ./internal/workflows/`, background).
- [ ] **Step 5:** Real run: `scripts/e2e-temporal.sh` plus a manual deploy of the `fastapi` fixture pushed to a throwaway public repo, or a local bare repo served over https by the e2e helper if Plan 1's e2e already has one (check in Task 0). Expect `deployed`.
- [ ] **Step 6:** Commit `feat(go): build any language with Railpack in the isolated builder`.
- [ ] **Review gate.**

### Task 6: Service variables at build time (BuildKit secrets)

**Files:**
- Modify: `internal/runtime/exec.go` (+ test), `internal/worker/build/{builder.go,detect.go}`, `internal/workflows/activities.go` (`BuildImage` loads decrypted env through `decryptedEnv`), `testutil.FakeRunner` (record env)

**Interfaces:**
- Produces: `runtime.EnvRunner` interface `RunEnv(ctx, dir string, env map[string]string, name string, args ...string) (string, error)`; `ExecRunner.RunEnv` sets `cmd.Env = append(os.Environ(), K=V...)`.
- Build: for each var `K` (keys validated by the existing env-key rule), add `--secret id=K,env=K` and pass the value in env only. Add `--build-arg secrets-hash=<hex sha256 over the sorted K=V pairs>` (Railpack's documented cache invalidation). Railpack `prepare` gets the same env.
- Dockerfile builds also get the secrets (Railway exposes variables to Dockerfile builds too). Document that a Dockerfile must use `RUN --mount=type=secret,id=K` to read one, and say so in the UI help text.

- [ ] **Step 1: Failing tests.** The sentinel value `s3ntinel-VALUE-9f` appears in `FakeRunner`'s env record only: not in any arg, error, log line or the stored build info. `secrets-hash` changes when a value changes and is stable across map orders. A key that is invalid for env → skipped with a warning naming the key.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** Commit `feat(go): pass service variables to builds as BuildKit secrets`.
- [ ] **Review gate** (Security focus).

### Task 7: Live build logs

**Files:**
- Modify: `internal/runtime/exec.go` (`RunStream`), `internal/worker/build/builder.go`, `internal/workflows/activities.go` (pass a log sink), `cmd/worker/main.go`
- Create: `internal/worker/build/logsink.go` + test

**Interfaces:**
- Produces: `runtime.StreamRunner` interface `RunStream(ctx, dir string, env map[string]string, out io.Writer, name string, args ...string) error`. The bounded stderr tail is still used for the error message. `build.LogSink` interface `Line(source, msg string)`; `build.NewRedisSink(r *logs.Redis, deploymentID string, scrub []string) *RedisSink` writes `logs.Entry{Source: "build"}`. It caps at 10,000 lines (then one "build log truncated" line), truncates each line at 8 KiB, and replaces every scrub value of 8+ bytes with `****`, including the repo URL credentials via `gitsrc.Scrub`. Writes are async through a buffered channel (1,024 slots); when it's full, lines are dropped and counted, and a "N lines dropped" line follows. A Redis failure never fails the build.
- Clone, prepare and buildx output all go to the sink with prefixes `[clone]`, `[detect]`, `[build]`, `[push]`. Detect hints are written as `[hint] ...` lines.
- The UI already shows deployment logs by source. It needs no change beyond Task 12 (the build/deploy toggle).

- [ ] **Step 1: Failing tests.** 12,000 lines → 10,000 kept plus a truncation line. A secret of 8+ bytes is masked; the value `true` (4 bytes) is not masked and the word "true" survives (Review Focus 4). A repo token in a git error line is masked. A full channel drops lines and reports the count, and `Line` never blocks (test with a stalled fake Redis). A Redis error doesn't change the build result.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** Real run: deploy a fixture and `GET /logs/deployments/<id>?source=build` (or the Plan 2 route) shows the build lines live over SSE.
- [ ] **Step 6:** Commit `feat(go): stream build output to the live logs`.
- [ ] **Review gate.**

### Task 8: `PORT` injection and target port

**Files:**
- Modify: `internal/workflows/activities.go` (`RunContainer`) (+ tests)

**Interfaces:**
- Produces: `workflows.DefaultAppPort = 8080`. In `RunContainer`, non-managed services: `port := targetPort ?? (user env PORT parsed 1-65535) ?? 8080`; env `PORT` is set to `port` unless the user set `PORT`; `spec.ContainerPort = port`. `DetectContainerPort` is still called, and when the image `EXPOSE`s a different single port and no target port is set, `AddDeploymentWarning` records "Image exposes 80 but the app was told PORT=8080." `NODE_ENV=production` is added only when the build info providers include `node`, or for legacy JS types.
- Legacy types keep today's behaviour (EXPOSE, then the default) until conversion, so existing services don't move.

- [ ] **Step 1: Failing tests (table).** app, no settings → `PORT=8080`, container port 8080. `targetPort` 3000 → `PORT=3000`, port 3000. User env `PORT=5000` → port 5000, env unchanged. User `PORT=abc` → non-retryable `invalid_port_variable`. python app → no `NODE_ENV`. Legacy react → EXPOSE 80 used, no `PORT` injected.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** Commit `feat(go): inject PORT and honour the target port`.
- [ ] **Review gate** (Product: behaviour for new services).

### Task 9: Listening-port discovery (better than Railway)

**Files:**
- Create: `internal/runtime/netstat.go` + test
- Modify: `internal/runtime/docker.go` (`ListeningPorts`) (+ test, + integration test), `internal/workflows/activities.go` (`WaitHealthy`), `internal/workflows/deploy.go` (use the returned port for `SwitchRoute` and `MarkDeployed`; `GetVersion` "port-discovery")

**Interfaces:**
- Produces: `runtime.ParseProcNetTCP(content string) []int` (LISTEN state `0A`; v4 and v6; dedup; sorted); `(Docker) ListeningPorts(ctx, containerName string) ([]int, error)` = `docker run --rm --network container:<name> <limits> --cap-drop ALL --security-opt no-new-privileges <ProbeImage> cat /proc/net/tcp /proc/net/tcp6` (`tcp6` may be missing: run `sh -c 'cat /proc/net/tcp; cat /proc/net/tcp6 2>/dev/null; true'`); `ContainerRuntime` interface gains `ListeningPorts`; `WaitHealthy` returns `WaitResult{Port int; Warning string}`.
- Algorithm per poll: probe the expected port and return it on success. Otherwise, after 10 s of failures, call `ListeningPorts`. Exactly one listening port `p != expected` that also passes `Probe` → return `p` with the warning "App listens on <p>, not $PORT (<expected>); routed to <p>. Set a target port in Settings to silence this." No ports → keep waiting. At the timeout, the error includes "listening ports: [..]" (or "none").
- Only for `app`/`image` services without an explicit `targetPort`. An explicit target port means no discovery (the user asked for that port).

- [ ] **Step 1: Failing tests.** Parser golden cases: a real `/proc/net/tcp` sample with LISTEN on `0BB8` (3000) and ESTABLISHED sockets → `[3000]`; tcp6 `:::8080` → 8080; garbage lines are ignored. `WaitHealthy` with a fake runtime: expected closed, one other open → port and warning; two others → timeout error listing both; explicit target port → no `ListeningPorts` call. Integration (`-tags integration`, real Docker, background): run `busybox nc -lk -p 3000` as the "app" and `ListeningPorts` → `[3000]`.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** Real run: deploy the `ignores-port` fixture; it reaches `deployed`, the URL works, and the warning shows in the deployment.
- [ ] **Step 6:** Commit `feat(go): route to the port the app really listens on`.
- [ ] **Review gate.**

### Task 10: HTTP healthcheck, restart policy, pre-deploy command

**Files:**
- Modify: `internal/runtime/docker.go` (`HTTPProbe`, `RunOnce`, restart flag) (+ tests), `internal/workflows/{activities.go,deploy.go}` (+ tests, replay history)

**Interfaces:**
- Produces: `(Docker) HTTPProbe(ctx, network, host string, port int, path string) error` = probe image `wget -q -T 2 --spider http://host:port/path`; `RunSpec.Restart string` (`"unless-stopped"`, `"on-failure:N"`, `"no"`; validated); `(Docker) RunOnce(ctx, spec RunSpec, cmd []string, timeout time.Duration) (exitCode int, err error)` (`--rm`, no `-d`, same limits/caps/network/env file; output goes to the log sink with source `predeploy`); a new activity `PreDeploy(ctx, PreDeployInput{Job store.Job; Network string}) error`; `WaitInput` gains `Path string`, `Timeout time.Duration`.
- Restart mapping: `always` → `unless-stopped` (today's), `on_failure` → `on-failure:<restartMaxRetries>`, `never` → `no`. Managed DBs keep `unless-stopped`.
- Health timeout: the service's `healthcheckTimeout` if set, else the global `HEALTH_TIMEOUT_SECONDS`, never above 600.
- Pre-deploy: runs after `RunContainer`'s pull and before starting the new container. It uses the new image and the same env (decrypted inside the activity), `sh -c <cmd>`, a 10-minute cap, and the same project network (so it can reach the DB). A non-zero exit is non-retryable `predeploy_failed: exit <n>`, logged. The old container keeps serving because nothing was switched yet. Workflow order: `Build → MarkBuilt → EnsureNetwork → PreDeploy (if set) → RunContainer → WaitHealthy → SwitchRoute → …`, guarded by `GetVersion("predeploy", …)`.

- [ ] **Step 1: Failing tests.** `runArgs` restart flag per policy. HTTP probe argv; healthcheck path used when set, TCP otherwise. A service timeout of 30 s is honoured; 900 is clamped to 600. PreDeploy: exit 0 → continue; exit 3 → deploy failed, `RevertRoute` not needed, old deployment untouched (deploy_test with the Temporal test env). Secrets are not in argv (sentinel). Replay test with the old histories plus a new one.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** Real run: the express fixture with `healthcheckPath=/health` and `preDeployCommand="node migrate.js"`, where the fixture's migrate script exits 1 when `FAIL_MIGRATE` is set: verify both paths.
- [ ] **Step 6:** Commit `feat(go): http health checks, restart policies and pre-deploy commands`.
- [ ] **Review gate.**

### Task 11: API: create by repo, settings, detect preview, build info

**Files:**
- Create: `internal/api/settings.go`, `internal/api/detect.go`, `internal/workflows/detect.go` (+ tests)
- Modify: `internal/api/services.go`, `internal/api/server.go` (routes), deployment list/get handlers, `cmd/worker/main.go` (register `DetectWorkflow`)

**Interfaces:**
- `POST /services` bodies:
  - `{projectId, repoUrl, branch?, name?}` → `app`; `name` defaults to the repo's last path segment without `.git`, slugified.
  - `{projectId, image, name?}` → `image` (Part 2 Task 7 deploys it; Part 1 accepts and stores it).
  - `{projectId, database: "postgres"|"mysql"|"redis"|"mongodb", name?}` → that type.
  - Legacy `{serviceType: "nodejs"|…}` → 400 `service_type_retired` (new services only).
- `PATCH /services/:id/settings` body = a partial `ServiceSettings` (JSON null clears a field) → `svcconfig.Validate` → 200 with the service. Errors: 400 `invalid_settings` + `field`.
- `POST /services/:id/detect` → starts `DetectWorkflow(serviceID, branch)` (ID `detect-<serviceId>-<unix>`, task queue `deployments`, run timeout 3 min). The workflow runs one activity `Detect` (clone like `BuildAndPush` into `<BUILD_WORKDIR>/detect-<id>`, then `Builder.Detect`, remove the work dir). The API waits up to 120 s → 200 `DetectResult` (with `settings` origins and `configFile`), 422 `detect_failed` + `hints`, or 504 `detect_timeout`. Rate limit: 6 per minute per user (Plan 2 limiter). The workflow input holds the service ID only; the repo URL is read from the store.
- Deployment JSON gains `builder`, `buildInfo` (`providers`, `versions`, `startCommand`, `hints`) and `warnings`.

- [ ] **Step 1: Failing tests (harness).** Create by repo → `serviceType: "app"`, name `y` for `https://github.com/x/y.git`. Create by database → its type. Legacy type → 400 `service_type_retired`. Intruder project → 404. PATCH validates each bad field (table from Task 2) and clears with null. Detect: fake Temporal client returns a result, an error with hints → 422, a slow one → 504. Ownership → 404. Deployment responses include the new fields, and `buildInfo` has no absolute paths.
- [ ] **Step 2-4:** FAIL → implement → pass. **Step 5:** `scripts/compare-api.sh` (Plan 2) updated for the new fields. Real detect call against the `fastapi` fixture repo.
- [ ] **Step 6:** Commit `feat(go): create services from a repo, edit settings and preview detection`.
- [ ] **Review gate** (Product).

### Task 12: UI: New menu, detect card, Settings tab, build logs

**Files:**
- Create: `cp-ui/src/components/NewServiceMenu.tsx`, `DetectCard.tsx`, `ServiceSettingsTab.tsx`
- Modify: `cp-ui/src/components/CreateServiceModal.tsx` (replaced by the menu flow, or reduced to the repo form), `ServiceDetailsPanel.tsx` (tabs: Deployments, Variables, Settings, plus Connect for databases), `cp-ui/src/lib/api.ts` (`createService`, `updateSettings`, `detectService`), the deployment view (builder badge, warnings, hints, Build/Deploy log toggle)

Behaviour:
- **New** menu (Railway-like): *GitHub repo* (URL + branch), *Docker image* (Part 2), *Database* (Postgres, MySQL, Redis, MongoDB), *Empty service* (an app without a repo, deployable after a repo is set).
- Repo flow: after the URL is entered, call Detect and show the `DetectCard`: "Detected: Python 3.12 · FastAPI", install/build/start commands, the config file used, hints in amber, and a **Deploy** button. Failure: show the hints and a link to Settings.
- Settings tab sections:
  - *Source*: repo, branch, root directory.
  - *Build*: builder (Auto / Dockerfile / Railpack), build command, Dockerfile path.
  - *Deploy*: start command, pre-deploy command, healthcheck path + timeout, restart policy + retries, target port.
  - *Networking*: public URL, private domain (Part 2).
  - A field whose origin is a repo file is read-only, with a badge "set in railway.json". Ignored keys are listed. Save → `PATCH`, with the server's `field` errors shown inline.
- Deployment view: builder badge (Dockerfile / Railpack · node 22), warnings (port discovery, EXPOSE mismatch), live build logs by default while `building`, deploy logs after.
- Legacy services show a banner "Built with the old Node/Next/React builder. Switch to auto-detect" that calls `PATCH settings` + a type switch endpoint added in Task 13 (until then, hidden).

- [ ] **Step 1:** `npm run lint` and `npm run build` pass before the change (baseline).
- [ ] **Step 2:** Implement. **Step 3:** `npm run lint && npm run build` pass.
- [ ] **Step 4: Product walk-through** (Verifier drives it in a browser): create from the `vite-spa` fixture, see the detect card, deploy, watch build logs, open the URL; change the start command, redeploy; add `railway.json` to a fixture and see the badge.
- [ ] **Step 5:** Commit `feat(ui): railway-style new-service flow, detect preview and settings`.
- [ ] **Review gate** (Product).

### Task 13: Convert existing services, remove the old generators, Part 1 gate

**Files:**
- Create: `cmd/migrate/convert.go` (+ test)
- Modify: `cmd/migrate/main.go`, `internal/api/services.go` (`POST /services/:id/convert`), `internal/worker/build/builder.go`
- Delete (after a successful conversion only): the JS generators in `internal/worker/build/dockerfile.go` and their tests, plus the legacy branches added in Tasks 5 and 8

**Interfaces:**
- `go run ./cmd/migrate convert-services --dry-run` (default) runs `DetectWorkflow` for every live service of type `nodejs`/`nextjs`/`react`/`docker` (sequentially, 3 min each) and prints a table: service id, name, old type, detected builder, providers, start command, proposed settings, problems. The proposed settings keep today's behaviour where Railpack differs:
  - `nodejs` / `nextjs` / `react` → `app` with `builder=auto`. Railpack serves SPAs itself and gets `PORT`, so `targetPort` stays unset. If detect fails, the service is **not converted** and is listed as a problem (generated Dockerfiles are never committed to user repos). The user fixes the repo or settings and runs the dry run again.
  - `docker` → `app` + `builder=dockerfile`.
- `--apply --ids a,b,c` (or `--all-clean`) converts only services whose dry run had no problems: sets `serviceType=app` plus the proposed settings, in one transaction per service. It prints what changed. No redeploy happens automatically.
- `POST /services/:id/convert` does the same for one owned service (the UI banner from Task 12).

Runbook (needs the user's approval before `--apply`):
- [ ] **Step 1:** Unit tests for the proposal logic (table: react/vite → app; docker → app + dockerfile; detect failure → problem, not converted). Implement. Commit.
- [ ] **Step 2:** `pg_dump` backup to `~/cp-backups/pre-plan4-convert-<date>.dump`.
- [ ] **Step 3:** Dry run and paste the table into `review/plan4/task-13.md`. **Ask the user to approve the list.**
- [ ] **Step 4:** Apply the approved IDs. Redeploy each converted service through the API, and check its URL returns the same status and content type as before (record before/after with `curl -sI`).
- [ ] **Step 5:** Once every live service is converted (or the user accepts the leftovers), delete the generators and legacy branches. `go test -race ./...` passes. Commit `refactor(go): remove the old JS Dockerfile generators`.
- [ ] **Part 1 gate:** Security and Staff review the whole Part 1 diff. The Verifier deploys every fixture (express, nextjs, vite-spa, fastapi, gohttp, dockerfile, monorepo with `rootDirectory=apps/api`, ignores-port). Each reaches `deployed` with a working URL, live build logs and the right detect card, and ignores-port shows the warning. Then `go test -race ./...`, `scripts/e2e-temporal.sh` (with the new replay histories) and cp-ui lint/build. Product walks the checklist, and the CTO gives a go/no-go in `review/decisions.md`.

---

# Part 2: Railway-style data and networking

### Task 1 (P2-1): Shared variables and unique service slugs (migration `00005`)

**Files:** `migrations/00005_railway_data.sql`, `internal/store/projectvars.go` (+ test), `internal/store/services.go` (slug)

- `ProjectVariable("id", "projectId" FK cascade, "key", "value" encrypted, "createdAt", UNIQUE(projectId,key))`.
- `Service."slug" TEXT`: backfilled from `name` (lowercase, `[a-z0-9-]`, ≤ 40, trimmed `-`, empty → `svc`). Duplicates in a project get `-2`, `-3`, and so on, ordered by `createdAt`. Then `NOT NULL` and a partial unique index `(projectId, slug) WHERE "deletedAt" IS NULL`. Renaming a service recomputes the slug, and a clash returns 409 `name_taken`.
- Store: `SetProjectVar`, `DeleteProjectVar`, `ProjectVarMap` (encrypted values), `ListProjectVarsForOwner` (masked).
- Tests: backfill with duplicates and unicode names; the unique index ignores soft-deleted services; encrypted round trip. Commit. **Review gate.**

### Task 2 (P2-2): System variables

**Files:** `internal/workflows/vars.go` (+ test), `activities.go`, `internal/api/services.go` (env key validation)

- `SystemVars(job, publicURL, privateDomain) map[string]string`: `INFRAPILOT_SERVICE_ID`, `INFRAPILOT_SERVICE_NAME`, `INFRAPILOT_PROJECT_ID`, `INFRAPILOT_DEPLOYMENT_ID`, `INFRAPILOT_GIT_COMMIT_SHA`, `INFRAPILOT_GIT_BRANCH`, `INFRAPILOT_PUBLIC_DOMAIN`, `INFRAPILOT_PRIVATE_DOMAIN`. They're set at run time and build time and override user values. The API rejects user keys with the `INFRAPILOT_` prefix (400 `reserved_variable`).
- Tests: every var present; a user `INFRAPILOT_X` is rejected by the API; a Node-era row with the prefix is overridden at run time. Commit. **Review gate.**

### Task 3 (P2-3): Private networking

**Files:** `internal/workflows/{names.go,activities.go}` (+ tests), `internal/runtime/docker.go` (alias validation)

- `PrivateDomain(slug) = slug + ".internal"`, added to `RunSpec.Aliases` next to `svc-<id>` for every service, managed DBs included. Validated as DNS labels (`checkName`-style). It takes effect on the next deploy after a rename (documented in the UI).
- Integration test (real Docker, background): two containers on one project network; `wget http://api.internal:8080` from the other succeeds; a different project's network can't resolve it. Commit. **Review gate.**

### Task 4 (P2-4): Managed Postgres and MySQL

**Files:** `internal/workflows/names.go` (`Managed`), `activities.go` (health by command), `internal/runtime/docker_managed_integration_test.go`

- `postgres`: image `postgres:16`, port 5432, `PasswordEnv: "POSTGRES_PASSWORD"`, env `POSTGRES_USER=postgres`, `POSTGRES_DB=app`. `DataDir: "/var/lib/postgresql/data"`, `PGDATA=/var/lib/postgresql/data/pgdata` (so the volume root's lost+found doesn't break initdb), and caps verified against the real image (start from `CHOWN, SETUID, SETGID, DAC_READ_SEARCH, FOWNER` and remove what the test proves unneeded).
- `mysql`: image `mysql:8.4`, port 3306, `PasswordEnv: "MYSQL_ROOT_PASSWORD"`, env `MYSQL_DATABASE=app`, `DataDir: "/var/lib/mysql"`, caps verified the same way.
- Health: the TCP probe as today, plus `ManagedImage.ReadyCmd` (`pg_isready -U postgres`, `mysqladmin ping -h 127.0.0.1 -p"$MYSQL_ROOT_PASSWORD"` via `sh -c` so the password stays in-container) run with `docker exec` until it succeeds.
- Integration tests (the method Plan 1 Task 23 used for Mongo): start, write a row, restart the container, the row survives; no anonymous volumes (`docker volume ls` diff); the password is required (an unauthenticated connection is refused). Commit. **Review gate.**

### Task 5 (P2-5): Published database variables and the Connect tab

**Files:** `internal/workflows/vars.go`, `internal/api/services.go` (`GET /services/:id/connect`), `cp-ui/src/components/ServiceDetailsPanel.tsx`

- `PublishedVars(svc, password) map[string]string`, computed (never stored), host = the private domain:
  - Postgres: `DATABASE_URL=postgresql://postgres:<pw>@<slug>.internal:5432/app`, `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, `PGDATABASE`.
  - MySQL: `MYSQL_URL=mysql://root:<pw>@<slug>.internal:3306/app`, `MYSQLHOST`, `MYSQLPORT`, `MYSQLUSER`, `MYSQLPASSWORD`, `MYSQLDATABASE`.
  - Redis: `REDIS_URL=redis://default:<pw>@<slug>.internal:6379`, `REDISHOST`, `REDISPORT`, `REDISPASSWORD`.
  - Mongo: `MONGO_URL=mongodb://admin:<pw>@<slug>.internal:27017`, `MONGOHOST`, `MONGOPORT`, `MONGOUSER`, `MONGOPASSWORD`.
  - Passwords are URL-escaped in URLs.
- `GET /services/:id/connect` (owner only) returns them masked; `?reveal=true` returns plaintext (Plan 2's env reveal rule). When `EXPOSE_MANAGED_PORTS` is on, it also returns a localhost URL with the published port.
- UI Connect tab: the variables with copy buttons, plus a `psql`/`mysql`/`redis-cli`/`mongosh` command.
- Tests: URL escaping of a password with `@:/?#`; masked vs revealed; 404 for intruders. Commit. **Review gate** (Product).

### Task 6 (P2-6): Reference variables and canvas suggestions

**Files:** `internal/workflows/vars.go` (resolver) (+ test), `activities.go` (`RunContainer`, `BuildImage`, `PreDeploy` use the resolved env), `internal/api/canvas.go` (suggestions), `cp-ui/src/components/VariablesTab.tsx`, the canvas connect handler in `cp-ui`

- Syntax: `${{<slug>.<VAR>}}` and `${{shared.<VAR>}}`, with optional spaces inside the braces. Multiple references per value and literal text around them are allowed (`postgres://${{db.PGUSER}}@...`).
- Resolution happens inside the activity: own vars → system vars → for each reference, the target service in the **same project** (by slug, live), whose value comes from its user vars, published vars or system vars, plus shared vars. One level only: a referenced value that itself contains `${{` is used literally, with a warning. An unknown service or variable → non-retryable `unknown_reference: <KEY> -> <slug>.<VAR>` before the image pull (Review Focus 5). Values never leave the activity.
- Canvas: `POST /canvas/connections` from an app to a database responds with `suggestions: [{key: "DATABASE_URL", value: "${{db.DATABASE_URL}}"}]`. The UI shows "Add DATABASE_URL to api?" and one click sets it.
- Variables tab: a table plus a raw `.env` editor (Railway has one), `${{` autocomplete over slugs and their variable names, and a shared-variables screen at the project level.
- Tests: resolution table (simple, multiple, spaces, shared, unknown service, unknown var, cross-project slug → unknown, nested reference → literal + warning, a deleted service → unknown); the sentinel is never in errors; suggestions per DB type. Commit. **Review gate** (Product, Security).

### Task 7 (P2-7): Image services and the Part 2 gate

**Files:** `internal/workflows/{deploy.go,activities.go}`, `internal/runtime/docker.go`, UI New menu → Docker image

- `image` services skip the build. `RunContainer` uses `Service.image` (validated with `checkImage`, registry and tag allowed, digest allowed), with the same PORT, discovery, health, restart and pre-deploy handling. `GetVersion("image-service", …)`. A private registry is out of scope (later plan).
- Tests: the workflow skips the build activities for `image`; an invalid image → 400 at create and non-retryable at deploy; real run of `nginx:alpine` with `targetPort=80`, and `traefik/whoami` without a target port (discovery finds its port).
- **Part 2 gate:** an app with `DATABASE_URL=${{db.DATABASE_URL}}` connects to managed Postgres and reads a row; the same against MySQL; an image service; two apps where one calls the other at `http://<slug>.internal:<port>`; shared variables visible in both. Then `go test -race ./...`, `scripts/e2e-temporal.sh` with the new replay histories, and cp-ui lint/build. Security and Staff review the whole Part 2 diff, Product walks the checklist, and the CTO gives a go/no-go. User approval before the dev stack runs it.

---

## Self-review notes (for the executor)

- Type names used across tasks: `store.ServiceSettings`, `svcconfig.Effective`, `svcconfig.RepoConfig`, `build.DetectInput`, `build.DetectResult`, `build.BuildResult`, `build.LogSink`, `runtime.EnvRunner`, `runtime.StreamRunner`, `Docker.ListeningPorts`, `Docker.HTTPProbe`, `Docker.RunOnce`, `workflows.WaitResult`, `workflows.SystemVars`, `workflows.PublishedVars`. Keep them; rename only through the CTO.
- Every workflow-shape change uses `workflow.GetVersion` and adds a replay history. Plan 1's replay test must keep passing.
- One open choice is left to the integration tests: the final capability sets for Postgres and MySQL (Part 2 Task 4). The Engineer records the result in the task file.
