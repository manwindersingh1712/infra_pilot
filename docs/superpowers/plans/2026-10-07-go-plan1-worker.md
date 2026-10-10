# Plan 1 — Foundation + Temporal Worker

> **For agentic workers:** REQUIRED: execute this plan with the team orchestration in spec §13 (`docs/superpowers/specs/2026-10-07-go-temporal-design.md`). The Engineer follows `docs/superpowers/agents/engineer.md`. After every task, Security, Staff and Verifier review in parallel, plus Product when the task changes something users see, and fixes loop until the task is clean. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Node worker and RabbitMQ with a Go worker that runs deployments as Temporal workflows. Along the way: fix container isolation, resource limits, managed-DB credentials, container leaks and health checks. The Node API keeps serving the UI until Plan 2.

**Architecture:** A new Go module in `control-plane-go/` (module `infrapilot`). The Node API keeps writing `Deployment` and `OutboxEvent` rows exactly as today. The Go worker:
- claims outbox rows and starts `DeployWorkflow` in Temporal, using workflow ID `deploy-<deploymentId>`;
- runs that workflow's activities, which build with an isolated BuildKit builder, run containers on a per-project Docker network with limits, health-check them, switch the nginx route and retire old containers;
- streams logs into Redis and flushes them to ClickHouse.

**Tech Stack:** Go 1.24+, `go.temporal.io/sdk`, `jackc/pgx/v5`, `pressly/goose/v3`, `redis/go-redis/v9`, `ClickHouse/clickhouse-go/v2`, `prometheus/client_golang`, `lucsky/cuid`, `joho/godotenv`, `testcontainers-go`. The infrastructure is the existing `control-plane/docker-compose.yml`, plus a Temporal dev server.

---

## Background for the implementer

Read spec sections §1–§9 first. The essentials:

**What the Node backend does today** (`control-plane/`, frozen reference, do not edit except where a task says so):
1. `POST /services/:id/deploy` inserts a `Deployment` row (`queued`) and an `OutboxEvent` in one transaction: `build.requested`, or `deploy.requested` for the managed types `mongodb` and `redis`.
2. The Node worker's outbox publisher sends those rows to RabbitMQ.
3. Its consumers clone/build/push the image, run `docker run` on network `cp_net` with a published host port, write an nginx route and stream `docker logs -f` into Redis.

**What this plan builds:**
- A Go worker that reads the **same** `OutboxEvent` rows (types `build.requested` and `deploy.requested`) and starts **one** Temporal workflow per deployment. The workflow decides whether to build.
- Nothing about the Node API changes in this plan. During the rollback window the Node worker can still be started again, so every schema change here is additive (spec §7).

**Database rules** (the schema was created by Prisma):
- Tables are PascalCase and columns camelCase. Double-quote every identifier: `"Deployment"."serviceId"`.
- Enum columns are Postgres enums. Cast parameters (`$1::"DeploymentStatus"`) and read them with `"status"::text`.
- `Deployment."updatedAt"` has no default. Set it on every insert and update.
- IDs are cuids (`github.com/lucsky/cuid`). Timestamps are UTC `TIMESTAMP(3)`; connections run with `timezone=UTC`.
- `DATABASE_URL` contains Prisma's `?schema=public`, which must be stripped (Task 3).

**Naming used everywhere:**
| Thing | Name |
|---|---|
| Container | `cp-<deploymentId>` |
| Network alias | `svc-<serviceId>` |
| Project network | `cp-proj-<projectId>` |
| Managed DB volume | `cp-vol-<serviceId>` |
| Workflow ID | `deploy-<deploymentId>` |
| Task queue | `deployments` |
| Labels | `cp.deploymentId`, `cp.serviceId`, `cp.projectId` |

**Conventions:**
- All `go` commands run in `control-plane-go/`.
- Integration tests use Docker (testcontainers) and are skipped by `go test -short ./...`.
- Always test with `-race`.
- Every task ends with a **Review gate** step. Stop there; the orchestrator runs the reviews.

## File structure

```
.github/workflows/ci.yml              # CI for Go, UI and secrets (repo root)
control-plane-go/
  .golangci.yml  .gitignore  go.mod  go.sum  .env (untracked)
  cmd/migrate/main.go                 # migrate up|status|baseline
  cmd/worker/main.go                  # Temporal worker, outbox starter, flush, metrics
  migrations/{embed.go,00001_baseline.sql,00002_deployment_stopped_at.sql}
  internal/
    config/       env vars and defaults
    db/           pgx pool, goose
    store/        all SQL: users, projects, services, envvars, deployments(+jobs), outbox, events
    secrets/      AES-256-GCM for env var values
    logs/         entry, redis (hot tier + flush cursor), clickhouse, aggregator
    logging/      slog setup
    metrics/      Prometheus registry for the worker
    runtime/      exec runner, docker (networks, volumes, limits, probes), ports, nginx, logstream (+resume)
    worker/build/ Dockerfile generation, buildx builder
    worker/outbox/ outbox → workflow starter loop
    workflows/    activities, DeployWorkflow, Temporal starter
    testutil/     postgres container, fixtures, FakeRunner
```

---

# Part 0 — Setup

### Task 1: Toolchain, Go module and CI

**Files:**
- Create: `control-plane-go/go.mod`, `control-plane-go/.gitignore`, `control-plane-go/.golangci.yml`, `.github/workflows/ci.yml`, `control-plane-go/.env` (untracked)

- [ ] **Step 1: Install the tools**

```bash
brew install go golangci-lint gosec gitleaks
go install golang.org/x/vuln/cmd/govulncheck@latest
export PATH="$PATH:$(go env GOPATH)/bin"
go version && golangci-lint version && gosec -version && gitleaks version && govulncheck -version
```
Expected: `go1.24` or newer, and a version line from each tool. Add the `export PATH` line to `~/.zshrc` so later shells find `govulncheck`.

- [ ] **Step 2: Start Docker**

```bash
open -a Docker
until docker info >/dev/null 2>&1; do sleep 2; done; docker info --format '{{.ServerVersion}}'
```
Expected: a version number such as `28.x`.

- [ ] **Step 3: Create the branch and module**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git checkout -b go-migration
mkdir -p control-plane-go && cd control-plane-go
go mod init infrapilot
```
Expected: `go: creating new go.mod: module infrapilot`

- [ ] **Step 4: Add `.gitignore`**

`control-plane-go/.gitignore`:
```
bin/
.env
```

- [ ] **Step 5: Create `.env`**

Start from the Node settings, then add the Go-only ones. The encryption key is generated locally and never committed.
```bash
cp ../control-plane/.env .env
cat >> .env <<EOF
APP_ENV=development
NGINX_ROUTES_DIR=../control-plane/infra/nginx/conf.d/routes
TEMPORAL_HOST_PORT=localhost:7233
ENV_ENCRYPTION_KEY=$(openssl rand -base64 32)
EOF
chmod 600 .env
grep -c ENV_ENCRYPTION_KEY .env
```
Expected: `1`. Back up this key somewhere safe: env vars encrypted with it cannot be read without it.

- [ ] **Step 6: Add the linter config**

`control-plane-go/.golangci.yml`:
```yaml
version: "2"
linters:
  default: standard
  exclusions:
    presets:
      - std-error-handling
    rules:
      # Tests write fixtures with os.WriteFile etc. and fail loudly anyway.
      - path: _test\.go
        linters: [errcheck]
```

- [ ] **Step 7: Add CI**

`.github/workflows/ci.yml` (at the repository root):
```yaml
name: CI

on:
  push:
    branches: [master, go-migration]
  pull_request:

permissions:
  contents: read

jobs:
  go:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: control-plane-go
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: control-plane-go/go.mod
          cache-dependency-path: control-plane-go/go.sum
      - run: go build ./...
      - run: go vet ./...
      - uses: golangci/golangci-lint-action@v8
        with:
          working-directory: control-plane-go
          version: latest
      - run: go test -race ./...
      - run: go install golang.org/x/vuln/cmd/govulncheck@latest && govulncheck ./...
      # G204: the runtime package runs git/docker with argument lists by design (no shell);
      #       the security reviewer checks every call site.
      # G304/G306: paths come from our own config and IDs, not from user input.
      - run: go install github.com/securego/gosec/v2/cmd/gosec@latest && gosec -quiet -exclude=G204,G304,G306 ./...

  ui:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: cp-ui
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: cp-ui/package-lock.json
      - run: npm ci
      - run: npm run lint
      - run: npm run build

  secrets:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: gitleaks/gitleaks-action@v2
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```
The frozen Node backend (`control-plane/`) is deliberately not built or tested.

> **Amended by the Task 1 fix round** (CTO decisions SEC-1 and lows triage, `review/decisions.md`, 2026-10-07): gosec runs with `-exclude=G204` only, a "Forbid shell invocation" step precedes it, every checkout has `persist-credentials: false`, setup-go uses `go-version: '1.27.x'` + `check-latest: true`, tool versions are pinned, and a root `.gitleaksignore` covers three documented false positives. The committed `ci.yml` is the source of truth.

- [ ] **Step 8: Check the workflow file parses**

Run: `cd .. && ruby -ryaml -e 'YAML.load_file(".github/workflows/ci.yml"); puts "ok"' && cd control-plane-go`
Expected: `ok`

- [ ] **Step 9: Commit**

```bash
git add go.mod .gitignore .golangci.yml ../.github/workflows/ci.yml
git commit -m "chore(go): start control-plane-go module with CI and lint config"
```

- [ ] **Step 10: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier. The Verifier checks that the tools run and the YAML parses. Do not push `go-migration` yet: the go job needs at least one package (Task 2) and the ui job needs Task 1a.

**First-push gate** (CTO, STAFF-3): push only after the Task 1 fix round, Task 1a and Task 2 have all passed review, and a local dry run is green: in `control-plane-go` `go build ./... && go vet ./... && golangci-lint run && go test -race ./... && govulncheck ./... && gosec -quiet -exclude=G204 ./...` plus the shell-guard grep; `cd cp-ui && npm ci && npm run lint && npm run build`; `gitleaks detect --no-banner` over full history. Ask the user before pushing. After the push run `gh run watch` and record the run URL in `review/plan1/task-2.md`.

---

### Task 1a: Green cp-ui lint and build baseline

Added by CTO decision STAFF-1 (`review/decisions.md`, 2026-10-07). The `ui` CI job must pass before the first push, and Plan 2 changes the UI, so it needs a green baseline.

**Files:** only `cp-ui/src/**`. Do not edit `eslint.config.js`, `tsconfig*.json`, `package.json` or the lockfile.

- [ ] **Step 1:** `cd cp-ui && npm ci && npm run lint; npm run build` and record the error list (expect about 20 lint errors, 5 warnings, 14 tsc errors).
- [ ] **Step 2:** Fix each error with no behaviour or visual change:
  - replace `any` with real types, or `unknown` plus narrowing;
  - fix the LandingPage motion `animate` typing (TS2769) with the correct overload, never a cast to `any`;
  - delete unreferenced locals such as `ProjectCanvasPage.runAutoLayout` (`ServiceCanvas` and `useCanvas` keep their own copy).
  A per-line `// eslint-disable-next-line <rule> -- <reason>` is allowed only where a real type is impossible; list each in the report.
- [ ] **Step 3:** `npm run lint && npm run build` exits 0 with 0 errors. Warnings are allowed but listed in the report.
- [ ] **Step 4: Commit** `chore(ui): fix existing lint and type errors so CI is green`.
- [ ] **Step 5: Review gate.** Staff, Verifier (lint, build, then `npm run dev` and load `/`, `/login`, `/projects`, `/projects/:id`: animations and canvas still work, no new console errors) and Product.

---

### Task 2: Configuration

> **CTO amendment** (three 2026-10-07 "Plan 1 Task 2" entries in `review/decisions.md`): unset `APP_ENV`/`NODE_ENV` means production, values are range-checked, and `Config.LogValue`/`String` redact secrets. The code blocks below are the committed result of the fix round (`e1801fe` + `0ab3bd8`); the committed code wins over any older copy. Step 2's expected failure (`undefined: Load`) still holds for a fresh start.

**Files:**
- Create: `control-plane-go/internal/config/config.go`
- Test: `control-plane-go/internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:
```go
package config

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var validKey = base64.StdEncoding.EncodeToString(make([]byte, 32))

// mustLoad fails the test if Load returns an error.
func mustLoad(t *testing.T, m map[string]string) Config {
	t.Helper()
	c, err := Load(env(m))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadAppliesDefaults(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "postgres://x"})
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, "production"},
		{"LogFormat", c.LogFormat, "text"},
		{"APIPort", c.APIPort, "8080"},
		{"APIMetricsAddr", c.APIMetricsAddr, "127.0.0.1:9090"},
		{"WorkerMetricsAddr", c.WorkerMetricsAddr, "127.0.0.1:9091"},
		{"TemporalHostPort", c.TemporalHostPort, "localhost:7233"},
		{"TemporalNamespace", c.TemporalNamespace, "default"},
		{"RedisURL", c.RedisURL, "redis://localhost:6379"},
		{"ClickHouseURL", c.ClickHouseURL, "http://localhost:8123"},
		{"ClickHouseUser", c.ClickHouseUser, "default"},
		{"RegistryHost", c.RegistryHost, "localhost:5000"},
		{"BuildWorkdir", c.BuildWorkdir, "/tmp/cp-builds"},
		{"BuilderName", c.BuilderName, "cp-builder"},
		{"BuilderMemory", c.BuilderMemory, "2g"},
		{"BuilderCPUs", c.BuilderCPUs, 2},
		{"ContainerMemory", c.ContainerMemory, "512m"},
		{"ContainerCPUs", c.ContainerCPUs, "0.5"},
		{"ContainerPids", c.ContainerPids, 256},
		{"DefaultContainerPort", c.DefaultContainerPort, 3080},
		{"ExposeManagedPorts", c.ExposeManagedPorts, false},
		{"PortRangeStart", c.PortRangeStart, 20000},
		{"PortRangeEnd", c.PortRangeEnd, 20100},
		{"LegacyVolumeRoot", c.LegacyVolumeRoot, "/tmp/cp-volumes"},
		{"ProbeImage", c.ProbeImage, "busybox:1.36"},
		{"HealthTimeout", c.HealthTimeout, 60 * time.Second},
		{"NginxPort", c.NginxPort, "80"},
		{"NginxRoutesDir", c.NginxRoutesDir, "infra/nginx/conf.d/routes"},
		{"NginxContainer", c.NginxContainer, "cp_nginx"},
		{"ServiceBaseDomain", c.ServiceBaseDomain, "localhost"},
		{"CORSOrigins", strings.Join(c.CORSOrigins, ","), "http://localhost:5173"},
		{"IsDevelopment", c.IsDevelopment(), false},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	c := mustLoad(t, map[string]string{
		"DATABASE_URL": " postgres://x ", "NODE_ENV": "production", "LOG_FORMAT": "json",
		"REGISTRY_HOST": "localhost:5001", "CORS_ORIGINS": "https://a.test, https://b.test",
		"HEALTH_TIMEOUT_SECONDS": " 90 ", "ENV_ENCRYPTION_KEY": validKey + "\n",
		"JWT_SECRET": "  s  ", "CLICKHOUSE_PASSWORD": " pw ", "CONTAINER_PIDS": "100",
	})
	checks := []struct {
		name      string
		got, want any
	}{
		{"AppEnv", c.AppEnv, "production"},
		{"LogFormat", c.LogFormat, "json"},
		{"RegistryHost", c.RegistryHost, "localhost:5001"},
		{"ExposeManagedPorts", c.ExposeManagedPorts, false},
		{"HealthTimeout", c.HealthTimeout, 90 * time.Second},
		{"EnvEncryptionKey length", len(c.EnvEncryptionKey), 32},
		{"CORSOrigins", strings.Join(c.CORSOrigins, ","), "https://a.test,https://b.test"},
		{"DatabaseURL (trimmed)", c.DatabaseURL, "postgres://x"},
		{"JWTSecret (trimmed)", c.JWTSecret, "s"},
		{"ClickHousePassword (not trimmed)", c.ClickHousePassword, " pw "},
		{"ContainerPids", c.ContainerPids, 100},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("%s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
}

func TestAppEnv(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		wantDev     bool
		wantExposed bool
	}{
		{"unset means production", map[string]string{}, false, false},
		{"APP_ENV=development", map[string]string{"APP_ENV": "development"}, true, true},
		{"NODE_ENV=development (Node .env)", map[string]string{"NODE_ENV": "development"}, true, true},
		{"APP_ENV wins over NODE_ENV", map[string]string{"APP_ENV": "development", "NODE_ENV": "production"}, true, true},
		{"APP_ENV=production wins", map[string]string{"APP_ENV": "production", "NODE_ENV": "development"}, false, false},
		{"dev can turn exposure off", map[string]string{"APP_ENV": "development", "EXPOSE_MANAGED_PORTS": "false"}, true, false},
	}
	for _, tc := range cases {
		tc.env["DATABASE_URL"] = "x"
		c := mustLoad(t, tc.env)
		if c.IsDevelopment() != tc.wantDev || c.ExposeManagedPorts != tc.wantExposed {
			t.Errorf("%s: IsDevelopment=%v ExposeManagedPorts=%v, want %v %v",
				tc.name, c.IsDevelopment(), c.ExposeManagedPorts, tc.wantDev, tc.wantExposed)
		}
	}
}

func TestCORSOriginFallback(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "x", "CORS_ORIGIN": "https://old.test"})
	if got := strings.Join(c.CORSOrigins, ","); got != "https://old.test" {
		t.Errorf("CORS_ORIGIN fallback: got %q", got)
	}
	c = mustLoad(t, map[string]string{"DATABASE_URL": "x", "CORS_ORIGIN": "https://old.test", "CORS_ORIGINS": "https://new.test"})
	if got := strings.Join(c.CORSOrigins, ","); got != "https://new.test" {
		t.Errorf("CORS_ORIGINS must win: got %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantMsg string
	}{
		{"missing DATABASE_URL", map[string]string{}, "missing required env: DATABASE_URL"},
		{"whitespace DATABASE_URL", map[string]string{"DATABASE_URL": "  "}, "missing required env: DATABASE_URL"},
		{"non-integer container port", map[string]string{"DEFAULT_CONTAINER_PORT": "abc"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"container port 0", map[string]string{"DEFAULT_CONTAINER_PORT": "0"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"container port 65536", map[string]string{"DEFAULT_CONTAINER_PORT": "65536"}, "DEFAULT_CONTAINER_PORT must be an integer"},
		{"range start 0", map[string]string{"DATA_PLANE_PORT_RANGE_START": "0"}, "DATA_PLANE_PORT_RANGE_START must be an integer"},
		{"range end 70000", map[string]string{"DATA_PLANE_PORT_RANGE_END": "70000"}, "DATA_PLANE_PORT_RANGE_END must be an integer"},
		{"range start > end", map[string]string{"DATA_PLANE_PORT_RANGE_START": "30000", "DATA_PLANE_PORT_RANGE_END": "20000"}, "must not be greater than DATA_PLANE_PORT_RANGE_END"},
		{"health timeout 0", map[string]string{"HEALTH_TIMEOUT_SECONDS": "0"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 3600"},
		{"health timeout -5", map[string]string{"HEALTH_TIMEOUT_SECONDS": "-5"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 3600"},
		{"health timeout too big", map[string]string{"HEALTH_TIMEOUT_SECONDS": "99999999999999"}, "HEALTH_TIMEOUT_SECONDS must be an integer from 1 to 3600"},
		{"pids 0 (unlimited)", map[string]string{"CONTAINER_PIDS": "0"}, "CONTAINER_PIDS must be an integer"},
		{"pids -1 (unlimited)", map[string]string{"CONTAINER_PIDS": "-1"}, "CONTAINER_PIDS must be an integer"},
		{"builder cpus 0", map[string]string{"BUILDER_CPUS": "0"}, "BUILDER_CPUS must be an integer"},
		{"container cpus 0 (unlimited)", map[string]string{"CONTAINER_CPUS": "0"}, "CONTAINER_CPUS must be a number of CPUs"},
		{"container cpus over 1024", map[string]string{"CONTAINER_CPUS": "2000"}, "CONTAINER_CPUS must be a number of CPUs greater than 0 and at most 1024"},
		{"container cpus text", map[string]string{"CONTAINER_CPUS": "half"}, "CONTAINER_CPUS must be a number of CPUs"},
		{"container memory 0 (unlimited)", map[string]string{"CONTAINER_MEMORY": "0"}, "CONTAINER_MEMORY must be a positive size"},
		{"container memory unit", map[string]string{"CONTAINER_MEMORY": "512mb"}, "CONTAINER_MEMORY must be a positive size"},
		{"builder memory", map[string]string{"BUILDER_MEMORY": "-1"}, "BUILDER_MEMORY must be a positive size"},
		{"api port text", map[string]string{"API_PORT": "http"}, "API_PORT must be a port"},
		{"nginx port 0", map[string]string{"NGINX_PORT": "0"}, "NGINX_PORT must be a port"},
		{"non-boolean expose", map[string]string{"EXPOSE_MANAGED_PORTS": "maybe"}, "EXPOSE_MANAGED_PORTS must be true or false"},
		{"expose in production", map[string]string{"APP_ENV": "production", "EXPOSE_MANAGED_PORTS": "true"}, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production"},
		{"expose with env unset", map[string]string{"EXPOSE_MANAGED_PORTS": "true"}, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production"},
		{"unknown APP_ENV", map[string]string{"APP_ENV": "prod"}, "APP_ENV (or NODE_ENV) must be one of development, production"},
		{"unknown NODE_ENV", map[string]string{"NODE_ENV": "test"}, "APP_ENV (or NODE_ENV) must be one of development, production"},
		{"unknown LOG_FORMAT", map[string]string{"LOG_FORMAT": "logfmt"}, "LOG_FORMAT must be one of text, json"},
		{"wildcard CORS", map[string]string{"CORS_ORIGINS": "https://a.test,*"}, "* is not allowed"},
		{"short encryption key", map[string]string{"ENV_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte("short"))}, "ENV_ENCRYPTION_KEY must decode to 32 bytes, got 5"},
		{"non-base64 encryption", map[string]string{"ENV_ENCRYPTION_KEY": "%%%"}, "ENV_ENCRYPTION_KEY must be base64"},
	}
	for _, tc := range cases {
		if _, ok := tc.env["DATABASE_URL"]; !ok && tc.name != "missing DATABASE_URL" {
			tc.env["DATABASE_URL"] = "x"
		}
		_, err := Load(env(tc.env))
		if err == nil {
			t.Errorf("%s: expected an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: error %q does not contain %q", tc.name, err, tc.wantMsg)
		}
	}
}

func TestRequireWorker(t *testing.T) {
	c := mustLoad(t, map[string]string{"DATABASE_URL": "x"})
	if err := c.RequireWorker(); err == nil || !strings.Contains(err.Error(), "ENV_ENCRYPTION_KEY is required") {
		t.Fatalf("missing key: got %v", err)
	}
	c = mustLoad(t, map[string]string{"DATABASE_URL": "x", "ENV_ENCRYPTION_KEY": validKey})
	if err := c.RequireWorker(); err != nil {
		t.Fatal(err)
	}
}

func TestRequireAPI(t *testing.T) {
	strong := strings.Repeat("k", 32)
	cases := []struct {
		name    string
		env     map[string]string
		wantMsg string // "" means no error
	}{
		{"missing key", map[string]string{"JWT_SECRET": strong}, "ENV_ENCRYPTION_KEY is required"},
		{"missing secret", map[string]string{"ENV_ENCRYPTION_KEY": validKey}, "JWT_SECRET is required"},
		{"whitespace secret", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "   "}, "JWT_SECRET is required"},
		{"production short", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "s"}, "at least 32 bytes"},
		{"production dev placeholder", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "dev-secret-change-me"}, "placeholder"},
		{"production example placeholder", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "generate-with-openssl-rand-base64-32"}, "placeholder"},
		{"production strong", map[string]string{"ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": strong}, ""},
		{"development placeholder ok", map[string]string{"APP_ENV": "development", "ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "dev-secret-change-me"}, ""},
		{"development short ok", map[string]string{"APP_ENV": "development", "ENV_ENCRYPTION_KEY": validKey, "JWT_SECRET": "s"}, ""},
	}
	for _, tc := range cases {
		tc.env["DATABASE_URL"] = "x"
		err := mustLoad(t, tc.env).RequireAPI()
		switch {
		case tc.wantMsg == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantMsg != "" && (err == nil || !strings.Contains(err.Error(), tc.wantMsg)):
			t.Errorf("%s: error %v does not contain %q", tc.name, err, tc.wantMsg)
		}
	}
}

func TestSecretsNeverPrinted(t *testing.T) {
	secrets := map[string]string{
		"jwt":        "jwt-secret-value-0123456789abcdefghij",
		"key":        validKey,
		"dbPassword": "dbpass123",
		"redis":      "redispass456",
		"clickhouse": "chpass789",
	}
	c := mustLoad(t, map[string]string{
		"DATABASE_URL":        "postgresql://cp:" + secrets["dbPassword"] + "@db:5432/cp",
		"REDIS_URL":           "redis://:" + secrets["redis"] + "@redis:6379",
		"JWT_SECRET":          secrets["jwt"],
		"ENV_ENCRYPTION_KEY":  secrets["key"],
		"CLICKHOUSE_PASSWORD": secrets["clickhouse"],
	})
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("config", "cfg", c)
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config", "cfg", c)
	fmt.Fprintf(&buf, "%v %+v %s", c, c, c)
	out := buf.String()
	for name, s := range secrets {
		if strings.Contains(out, s) {
			t.Errorf("%s secret leaked: %s", name, out)
		}
	}
	for _, want := range []string{"appEnv", "production", "jwtSecretSet", "db:5432"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/`
Expected: FAIL with `undefined: Load`.

- [ ] **Step 3: Implement**

`internal/config/config.go`:
```go
// Package config loads process configuration from environment variables.
// Names shared with the Node backend keep their meaning and defaults.
package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv    string // development | production (APP_ENV, falling back to NODE_ENV; unset means production)
	LogFormat string // text | json

	APIPort           string
	DatabaseURL       string
	JWTSecret         string
	EnvEncryptionKey  []byte // 32 bytes, from base64 ENV_ENCRYPTION_KEY
	CORSOrigins       []string
	APIMetricsAddr    string
	WorkerMetricsAddr string

	TemporalHostPort  string
	TemporalNamespace string

	RedisURL           string
	ClickHouseURL      string
	ClickHouseUser     string
	ClickHousePassword string

	RegistryHost  string
	BuildWorkdir  string
	BuilderName   string
	BuilderMemory string
	BuilderCPUs   int

	ContainerMemory      string
	ContainerCPUs        string
	ContainerPids        int
	DefaultContainerPort int
	ExposeManagedPorts   bool // publish managed DB ports on 127.0.0.1 (dev convenience)
	PortRangeStart       int
	PortRangeEnd         int
	LegacyVolumeRoot     string // where the Node worker kept managed DB data
	ProbeImage           string
	HealthTimeout        time.Duration

	NginxPort string
	// NginxRoutesDir is resolved against the process working directory when
	// relative. The dev .env sets it explicitly; the worker logs the absolute path.
	NginxRoutesDir    string
	NginxContainer    string
	ServiceBaseDomain string
}

const maxHealthTimeoutSeconds = 3600

// Docker size: a positive integer with an optional b/k/m/g unit, e.g. 512m.
var dockerSize = regexp.MustCompile(`^[1-9][0-9]*[bkmgBKMG]?$`)

// Placeholders that must never be used as a JWT secret outside development.
var placeholderSecrets = []string{"dev-secret-change-me", "generate-with-openssl-rand-base64-32"}

// Load reads configuration through getenv (os.Getenv in production).
// Every value except CLICKHOUSE_PASSWORD is trimmed of surrounding whitespace.
func Load(getenv func(string) string) (Config, error) {
	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	var invalid []string
	num := func(key string, def, lo, hi int) int {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			invalid = append(invalid, fmt.Sprintf("%s must be an integer from %d to %d, got %q", key, lo, hi, v))
		}
		return n
	}
	flag := func(key string, def bool) bool {
		v := strings.TrimSpace(getenv(key))
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			invalid = append(invalid, fmt.Sprintf("%s must be true or false, got %q", key, v))
		}
		return b
	}
	oneOf := func(key, v string, allowed ...string) string {
		if !slices.Contains(allowed, v) {
			invalid = append(invalid, fmt.Sprintf("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v))
		}
		return v
	}
	port := func(key, def string) string {
		v := str(key, def)
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 65535 {
			invalid = append(invalid, fmt.Sprintf("%s must be a port from 1 to 65535, got %q", key, v))
		}
		return v
	}
	size := func(key, def string) string {
		v := str(key, def)
		if !dockerSize.MatchString(v) {
			invalid = append(invalid, fmt.Sprintf("%s must be a positive size like 512m or 2g, got %q", key, v))
		}
		return v
	}
	cpus := func(key, def string) string {
		v := str(key, def)
		if f, err := strconv.ParseFloat(v, 64); err != nil || !(f > 0) || f > 1024 {
			invalid = append(invalid, fmt.Sprintf("%s must be a number of CPUs greater than 0 and at most 1024, got %q", key, v))
		}
		return v
	}

	appEnv := oneOf("APP_ENV (or NODE_ENV)", str("APP_ENV", str("NODE_ENV", "production")), "development", "production")
	c := Config{
		AppEnv:    appEnv,
		LogFormat: oneOf("LOG_FORMAT", str("LOG_FORMAT", "text"), "text", "json"),

		APIPort:           port("API_PORT", "8080"),
		DatabaseURL:       strings.TrimSpace(getenv("DATABASE_URL")),
		JWTSecret:         strings.TrimSpace(getenv("JWT_SECRET")),
		CORSOrigins:       splitList(str("CORS_ORIGINS", str("CORS_ORIGIN", "http://localhost:5173"))),
		APIMetricsAddr:    str("API_METRICS_ADDR", "127.0.0.1:9090"),
		WorkerMetricsAddr: str("WORKER_METRICS_ADDR", "127.0.0.1:9091"),

		TemporalHostPort:  str("TEMPORAL_HOST_PORT", "localhost:7233"),
		TemporalNamespace: str("TEMPORAL_NAMESPACE", "default"),

		RedisURL:           str("REDIS_URL", "redis://localhost:6379"),
		ClickHouseURL:      str("CLICKHOUSE_URL", "http://localhost:8123"),
		ClickHouseUser:     str("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getenv("CLICKHOUSE_PASSWORD"), // not trimmed: spaces may be part of it

		RegistryHost:  str("REGISTRY_HOST", "localhost:5000"),
		BuildWorkdir:  str("BUILD_WORKDIR", "/tmp/cp-builds"),
		BuilderName:   str("BUILDER_NAME", "cp-builder"),
		BuilderMemory: size("BUILDER_MEMORY", "2g"),
		BuilderCPUs:   num("BUILDER_CPUS", 2, 1, 1024),

		ContainerMemory:      size("CONTAINER_MEMORY", "512m"),
		ContainerCPUs:        cpus("CONTAINER_CPUS", "0.5"),
		ContainerPids:        num("CONTAINER_PIDS", 256, 1, 1<<22),
		DefaultContainerPort: num("DEFAULT_CONTAINER_PORT", 3080, 1, 65535),
		ExposeManagedPorts:   flag("EXPOSE_MANAGED_PORTS", appEnv == "development"),
		PortRangeStart:       num("DATA_PLANE_PORT_RANGE_START", 20000, 1, 65535),
		PortRangeEnd:         num("DATA_PLANE_PORT_RANGE_END", 20100, 1, 65535),
		LegacyVolumeRoot:     str("LEGACY_VOLUME_ROOT", "/tmp/cp-volumes"),
		ProbeImage:           str("PROBE_IMAGE", "busybox:1.36"),
		HealthTimeout:        time.Duration(num("HEALTH_TIMEOUT_SECONDS", 60, 1, maxHealthTimeoutSeconds)) * time.Second,

		NginxPort:         port("NGINX_PORT", "80"),
		NginxRoutesDir:    str("NGINX_ROUTES_DIR", "infra/nginx/conf.d/routes"),
		NginxContainer:    str("NGINX_CONTAINER", "cp_nginx"),
		ServiceBaseDomain: str("SERVICE_BASE_DOMAIN", "localhost"),
	}

	if c.PortRangeStart > c.PortRangeEnd {
		invalid = append(invalid, fmt.Sprintf("DATA_PLANE_PORT_RANGE_START (%d) must not be greater than DATA_PLANE_PORT_RANGE_END (%d)", c.PortRangeStart, c.PortRangeEnd))
	}
	if c.ExposeManagedPorts && appEnv == "production" {
		invalid = append(invalid, "EXPOSE_MANAGED_PORTS=true is not allowed when APP_ENV=production")
	}
	if slices.Contains(c.CORSOrigins, "*") {
		invalid = append(invalid, "CORS_ORIGINS must list exact origins; * is not allowed")
	}
	if raw := strings.TrimSpace(getenv("ENV_ENCRYPTION_KEY")); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		switch {
		case err != nil:
			invalid = append(invalid, "ENV_ENCRYPTION_KEY must be base64")
		case len(key) != 32:
			invalid = append(invalid, fmt.Sprintf("ENV_ENCRYPTION_KEY must decode to 32 bytes, got %d", len(key)))
		default:
			c.EnvEncryptionKey = key
		}
	}
	if c.DatabaseURL == "" {
		invalid = append(invalid, "missing required env: DATABASE_URL")
	}
	if len(invalid) > 0 {
		return Config{}, fmt.Errorf("config: %s", strings.Join(invalid, "; "))
	}
	return c, nil
}

func (c Config) IsDevelopment() bool { return c.AppEnv == "development" }

// RequireWorker checks the settings only the worker needs.
func (c Config) RequireWorker() error {
	if len(c.EnvEncryptionKey) != 32 {
		return fmt.Errorf("config: ENV_ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)")
	}
	return nil
}

// RequireAPI checks the settings the API needs.
func (c Config) RequireAPI() error {
	if err := c.RequireWorker(); err != nil {
		return err
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("config: JWT_SECRET is required")
	}
	if !c.IsDevelopment() {
		if slices.Contains(placeholderSecrets, c.JWTSecret) {
			return fmt.Errorf("config: JWT_SECRET is a placeholder; generate one with: openssl rand -base64 32")
		}
		if len(c.JWTSecret) < 32 {
			return fmt.Errorf("config: JWT_SECRET must be at least 32 bytes outside development, got %d", len(c.JWTSecret))
		}
	}
	return nil
}

// LogValue keeps secrets out of logs: it reports whether each secret is set,
// and URLs with any password replaced.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("appEnv", c.AppEnv),
		slog.String("logFormat", c.LogFormat),
		slog.String("apiPort", c.APIPort),
		slog.String("databaseURL", redactURL(c.DatabaseURL)),
		slog.Bool("jwtSecretSet", c.JWTSecret != ""),
		slog.Bool("envEncryptionKeySet", len(c.EnvEncryptionKey) > 0),
		slog.Any("corsOrigins", c.CORSOrigins),
		slog.String("temporalHostPort", c.TemporalHostPort),
		slog.String("temporalNamespace", c.TemporalNamespace),
		slog.String("redisURL", redactURL(c.RedisURL)),
		slog.String("clickHouseURL", redactURL(c.ClickHouseURL)),
		slog.String("clickHouseUser", c.ClickHouseUser),
		slog.Bool("clickHousePasswordSet", c.ClickHousePassword != ""),
		slog.String("registryHost", c.RegistryHost),
		slog.Bool("exposeManagedPorts", c.ExposeManagedPorts),
		slog.String("nginxRoutesDir", c.NginxRoutesDir),
		slog.String("serviceBaseDomain", c.ServiceBaseDomain),
	)
}

// String makes fmt print the same redacted view as LogValue.
func (c Config) String() string { return c.LogValue().String() }

func redactURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparseable]"
	}
	return u.Redacted()
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/config/ && go vet ./... && golangci-lint run`
Expected: `ok  infrapilot/internal/config`, and no lint findings.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(go): config with Temporal, isolation and encryption settings"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 3: Database connection, schema migrations and the `migrate` command

**Files:**
- Create: `control-plane-go/internal/db/db.go`, `control-plane-go/internal/db/migrate.go`, `control-plane-go/migrations/embed.go`, `control-plane-go/migrations/00001_baseline.sql`, `control-plane-go/cmd/migrate/main.go`
- Test: `control-plane-go/internal/db/db_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/jackc/pgx/v5 github.com/pressly/goose/v3 github.com/joho/godotenv
```

- [ ] **Step 2: Write the failing test**

`internal/db/db_test.go`:
```go
package db

import "testing"

func TestNormalizeURLStripsPrismaSchemaParam(t *testing.T) {
	got, schema, err := normalizeURL("postgresql://cp:cp@localhost:5432/control_plane?schema=public")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgresql://cp:cp@localhost:5432/control_plane" || schema != "public" {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLKeepsOtherParams(t *testing.T) {
	got, schema, err := normalizeURL("postgres://h/db?sslmode=disable&schema=app")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://h/db?sslmode=disable" || schema != "app" {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}

func TestNormalizeURLWithoutSchema(t *testing.T) {
	got, schema, err := normalizeURL("postgres://h/db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "postgres://h/db" || schema != "" {
		t.Fatalf("got %q, schema %q", got, schema)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/db/`
Expected: FAIL with `undefined: normalizeURL`.

- [ ] **Step 4: Implement the connection**

`internal/db/db.go`:
```go
// Package db opens the Postgres pool and applies schema migrations.
package db

import (
	"context"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect opens a pool. Sessions run in UTC because the schema stores UTC
// values in TIMESTAMP(3) WITHOUT TIME ZONE columns.
func Connect(ctx context.Context, rawURL string) (*pgxpool.Pool, error) {
	connURL, schema, err := normalizeURL(rawURL)
	if err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(connURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

// normalizeURL removes Prisma's ?schema= parameter, which Postgres rejects as
// an unknown runtime parameter, and returns it separately.
func normalizeURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	q := u.Query()
	schema := q.Get("schema")
	q.Del("schema")
	u.RawQuery = q.Encode()
	return u.String(), schema, nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/db/`
Expected: `ok  infrapilot/internal/db`

- [ ] **Step 6: Write the baseline migration**

This is the schema that the 10 Prisma migrations produce today. Columns are in the same order, so a `pg_dump` of both databases matches in Step 10.

`migrations/00001_baseline.sql`:
```sql
-- +goose Up
CREATE TYPE "DeploymentStatus" AS ENUM ('queued', 'building', 'deploying', 'deployed', 'failed');
CREATE TYPE "OutboxStatus" AS ENUM ('pending', 'publishing', 'published', 'dead');
CREATE TYPE "ServiceType" AS ENUM ('docker', 'nodejs', 'mongodb', 'redis', 'nextjs', 'react');

CREATE TABLE "User" (
    "id" TEXT NOT NULL,
    "email" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "name" TEXT,
    "password" TEXT NOT NULL,
    "updatedAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "User_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Project" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "ownerUserId" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "Project_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Service" (
    "id" TEXT NOT NULL,
    "projectId" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "repoUrl" TEXT,
    "branch" TEXT NOT NULL DEFAULT 'main',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "serviceType" "ServiceType" NOT NULL DEFAULT 'docker',
    "positionX" DOUBLE PRECISION NOT NULL DEFAULT 0,
    "positionY" DOUBLE PRECISION NOT NULL DEFAULT 0,
    CONSTRAINT "Service_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "EnvVar" (
    "id" TEXT NOT NULL,
    "serviceId" TEXT NOT NULL,
    "key" TEXT NOT NULL,
    "value" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "EnvVar_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Deployment" (
    "id" TEXT NOT NULL,
    "serviceId" TEXT NOT NULL,
    "commitSha" TEXT NOT NULL,
    "image" TEXT,
    "status" "DeploymentStatus" NOT NULL DEFAULT 'queued',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "containerId" TEXT,
    "containerPort" INTEGER,
    "hostPort" INTEGER,
    "runtimeUrl" TEXT,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "volumePath" TEXT,
    CONSTRAINT "Deployment_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "OutboxEvent" (
    "id" TEXT NOT NULL,
    "type" TEXT NOT NULL,
    "payload" JSONB NOT NULL,
    "status" "OutboxStatus" NOT NULL DEFAULT 'pending',
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "nextAttemptAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "lastError" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "publishedAt" TIMESTAMP(3),
    CONSTRAINT "OutboxEvent_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "ServiceConnection" (
    "id" TEXT NOT NULL,
    "sourceId" TEXT NOT NULL,
    "targetId" TEXT NOT NULL,
    "label" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "ServiceConnection_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX "User_email_key" ON "User"("email");
CREATE INDEX "Service_projectId_idx" ON "Service"("projectId");
CREATE UNIQUE INDEX "EnvVar_serviceId_key_key" ON "EnvVar"("serviceId", "key");
CREATE INDEX "Deployment_serviceId_createdAt_idx" ON "Deployment"("serviceId", "createdAt");
CREATE INDEX "OutboxEvent_status_nextAttemptAt_idx" ON "OutboxEvent"("status", "nextAttemptAt");
CREATE INDEX "OutboxEvent_createdAt_idx" ON "OutboxEvent"("createdAt");
CREATE INDEX "ServiceConnection_sourceId_idx" ON "ServiceConnection"("sourceId");
CREATE INDEX "ServiceConnection_targetId_idx" ON "ServiceConnection"("targetId");
CREATE UNIQUE INDEX "ServiceConnection_sourceId_targetId_key" ON "ServiceConnection"("sourceId", "targetId");

ALTER TABLE "Project" ADD CONSTRAINT "Project_ownerUserId_fkey" FOREIGN KEY ("ownerUserId") REFERENCES "User"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Service" ADD CONSTRAINT "Service_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES "Project"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "EnvVar" ADD CONSTRAINT "EnvVar_serviceId_fkey" FOREIGN KEY ("serviceId") REFERENCES "Service"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Deployment" ADD CONSTRAINT "Deployment_serviceId_fkey" FOREIGN KEY ("serviceId") REFERENCES "Service"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_sourceId_fkey" FOREIGN KEY ("sourceId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_targetId_fkey" FOREIGN KEY ("targetId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- +goose Down
DROP TABLE "ServiceConnection", "OutboxEvent", "Deployment", "EnvVar", "Service", "Project", "User";
DROP TYPE "ServiceType", "OutboxStatus", "DeploymentStatus";
```

`migrations/embed.go`:
```go
// Package migrations embeds the goose SQL migrations into the binaries.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

- [ ] **Step 7: Implement goose wiring**

`internal/db/migrate.go`:
```go
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"infrapilot/migrations"
)

func gooseDB(pool *pgxpool.Pool) (*sql.DB, error) {
	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return nil, err
	}
	return stdlib.OpenDBFromPool(pool), nil
}

// Migrate applies all pending migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return goose.UpContext(ctx, sqlDB, ".")
}

// Status prints applied and pending migrations.
func Status(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	return goose.StatusContext(ctx, sqlDB, ".")
}

// MarkBaseline records migration 1 as applied on a database whose schema was
// already created by Prisma, so `migrate up` does not try to create it again.
func MarkBaseline(ctx context.Context, pool *pgxpool.Pool) error {
	sqlDB, err := gooseDB(pool)
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	version, err := goose.EnsureDBVersionContext(ctx, sqlDB)
	if err != nil {
		return err
	}
	if version >= 1 {
		return nil
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('"Deployment"') IS NOT NULL`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errors.New("no existing schema found: run `migrate up` instead of `baseline`")
	}
	if _, err := sqlDB.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (1, true)`); err != nil {
		return fmt.Errorf("record baseline: %w", err)
	}
	return nil
}
```

`cmd/migrate/main.go`:
```go
// Command migrate manages the Postgres schema: `migrate up`, `migrate status` or `migrate baseline`.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"infrapilot/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	_ = godotenv.Load()
	if len(os.Args) != 2 {
		return errors.New("usage: migrate up|status|baseline")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "up":
		return db.Migrate(ctx, pool)
	case "status":
		return db.Status(ctx, pool)
	case "baseline":
		return db.MarkBaseline(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q (want up|status|baseline)", os.Args[1])
	}
}
```

- [ ] **Step 8: Build**

Run: `go build ./... && go vet ./...`
Expected: no output.

- [ ] **Step 9: Check the baseline against a fresh database**

The infrastructure must be running: `cd ../control-plane && docker compose up -d && cd ../control-plane-go`. Then:
```bash
docker exec cp_postgres createdb -U cp control_plane_go_check
DATABASE_URL="postgresql://cp:cp@localhost:5432/control_plane_go_check" go run ./cmd/migrate up
```
Expected: goose prints `OK   00001_baseline.sql` and `goose: successfully migrated database to version: 1`.

- [ ] **Step 10: Diff against the Prisma-created schema**

```bash
dump() { docker exec cp_postgres pg_dump -U cp --schema-only --no-owner \
  --exclude-table=_prisma_migrations --exclude-table='goose_db_version*' "$1" \
  | grep -vE '^(--|\\restrict|\\unrestrict)' ; }
diff <(dump control_plane) <(dump control_plane_go_check) && echo SCHEMAS_MATCH
```
Expected: `SCHEMAS_MATCH`. If the diff shows a column order or type difference, fix `00001_baseline.sql` to match the Prisma side, then run `docker exec cp_postgres dropdb -U cp control_plane_go_check` and repeat Steps 9–10.

> **Review note (STAFF-3, 2026-10-07):** a dev DB whose `public` schema was recreated by a Prisma reset also shows `COMMENT ON SCHEMA public IS '';` and `REVOKE USAGE ON SCHEMA public FROM PUBLIC;`. Those two lines are not from any migration and may be ignored. The stronger gate, used in review, is to build a fresh DB by applying `control-plane/prisma/migrations/*/migration.sql` in order (`psql -v ON_ERROR_STOP=1`) and diff it against `control_plane_go_check`: that must be identical.

- [ ] **Step 11: Baseline the existing dev database and clean up**

```bash
go run ./cmd/migrate baseline
go run ./cmd/migrate status
docker exec cp_postgres dropdb -U cp control_plane_go_check
```
Expected: `status` lists `00001_baseline.sql` as applied. The existing data is untouched; the command only creates `goose_db_version` (goose's version-0 row plus a row for version 1). `baseline` refuses unless all 10 Prisma migrations are recorded as finished in `_prisma_migrations` (fix round STAFF-1).

**From here on, never run `npm run prisma:migrate` (or `prisma migrate dev` / `prisma db push` / `prisma db pull`).** goose owns the schema. Prisma's drift detection now sees `goose_db_version` and `migrate dev` would offer to reset the database. `prisma migrate status`/`deploy` and Prisma Client at runtime are unaffected (STAFF-2).

- [ ] **Step 12: Commit**

```bash
git add go.mod go.sum internal/db migrations cmd/migrate
git commit -m "feat(go): pgx connection and goose baseline migration matching Prisma schema"
```

- [ ] **Step 13: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 4: Test helpers (Postgres container, fakes)

**Files:**
- Create: `control-plane-go/internal/testutil/postgres.go`, `control-plane-go/internal/testutil/fakes.go`
- Test: `control-plane-go/internal/testutil/postgres_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/testcontainers/testcontainers-go github.com/testcontainers/testcontainers-go/modules/postgres
```

- [ ] **Step 2: Write the failing test**

`internal/testutil/postgres_test.go`:
```go
package testutil_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

func TestMigrationsCreateSchema(t *testing.T) {
	p := testutil.NeedDB(t, pool)
	var n int
	err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('User','Project','Service','EnvVar','Deployment','OutboxEvent','ServiceConnection')`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("found %d of 7 tables", n)
	}
}

func TestFakeRunnerRecordsCalls(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, _ = r.Run(context.Background(), "/tmp", "docker", "ps")
	if got := r.Commands(); len(got) != 1 || got[0] != "docker ps" {
		t.Fatalf("Commands() = %v", got)
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/testutil/`
Expected: FAIL with `undefined: testutil.RunWithPostgres`.

- [ ] **Step 4: Implement the Postgres helpers**

`internal/testutil/postgres.go`:
```go
// Package testutil holds helpers shared by tests: throwaway containers, fixtures and fakes.
package testutil

import (
	"context"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"infrapilot/internal/db"
)

// StartPostgres runs postgres:16 in Docker and applies the migrations.
func StartPostgres(ctx context.Context) (*pgxpool.Pool, func(), error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("cp_test"),
		tcpostgres.WithUsername("cp"),
		tcpostgres.WithPassword("cp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("start postgres container: %w", err)
	}
	terminate := func() { _ = ctr.Terminate(context.Background()) }

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		terminate()
		return nil, nil, err
	}
	pool, err := db.Connect(ctx, url)
	if err != nil {
		terminate()
		return nil, nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		terminate()
		return nil, nil, fmt.Errorf("migrate test db: %w", err)
	}
	return pool, func() { pool.Close(); terminate() }, nil
}

// RunWithPostgres is a TestMain body. It starts Postgres unless -short is set,
// stores the pool in *pool, runs the tests and exits.
func RunWithPostgres(m *testing.M, pool **pgxpool.Pool) {
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}
	p, cleanup, err := StartPostgres(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	*pool = p
	code := m.Run()
	cleanup()
	os.Exit(code)
}

// NeedDB skips the test under -short and otherwise empties every table.
func NeedDB(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	if pool == nil {
		t.Skip("integration test: needs Docker; run without -short")
	}
	_, err := pool.Exec(context.Background(),
		`TRUNCATE "ServiceConnection", "OutboxEvent", "Deployment", "EnvVar", "Service", "Project", "User" CASCADE`)
	if err != nil {
		t.Fatalf("reset db: %v", err)
	}
	return pool
}
```

- [ ] **Step 5: Implement the fakes**

`internal/testutil/fakes.go`:
```go
package testutil

import (
	"context"
	"strings"
	"sync"
)

// Call is one command recorded by FakeRunner.
type Call struct {
	Dir  string
	Name string
	Args []string
}

func (c Call) String() string { return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " ")) }

// FakeRunner records commands instead of executing them.
type FakeRunner struct {
	mu    sync.Mutex
	calls []Call
	// Handle, if set, decides each call's stdout and error.
	Handle func(c Call) (string, error)
}

func (f *FakeRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	c := Call{Dir: dir, Name: name, Args: args}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.Handle != nil {
		return f.Handle(c)
	}
	return "", nil
}

// Commands returns every recorded call as "name arg1 arg2".
func (f *FakeRunner) Commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = c.String()
	}
	return out
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/testutil/` (Docker running)
Expected: `ok  infrapilot/internal/testutil`. The first run pulls `postgres:16`.

Also run: `go test -short ./internal/testutil/`
Expected: `ok`. The migration test is skipped.

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/testutil
git commit -m "test(go): postgres testcontainer helper and fakes"
```

- [ ] **Step 8: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 5: Additive migration — `Deployment.stoppedAt`

**Files:**
- Create: `control-plane-go/migrations/00002_deployment_stopped_at.sql`
- Modify: `control-plane-go/internal/testutil/postgres_test.go`

The new column records when a deployment's container was retired. The change is additive only: Prisma (the Node API, still running) reads explicit columns and ignores it.

- [ ] **Step 1: Write the failing test**

Append to `internal/testutil/postgres_test.go`:
```go
func TestDeploymentHasStoppedAtColumn(t *testing.T) {
	p := testutil.NeedDB(t, pool)
	var n int
	err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'Deployment' AND column_name = 'stoppedAt' AND is_nullable = 'YES'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal(`"Deployment"."stoppedAt" is missing`)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/testutil/ -run StoppedAt`
Expected: FAIL with `"Deployment"."stoppedAt" is missing`.

- [ ] **Step 3: Add the migration**

`migrations/00002_deployment_stopped_at.sql`:
```sql
-- +goose Up
ALTER TABLE "Deployment" ADD COLUMN "stoppedAt" TIMESTAMP(3);

-- +goose Down
ALTER TABLE "Deployment" DROP COLUMN "stoppedAt";
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/testutil/`
Expected: `ok  infrapilot/internal/testutil`

- [ ] **Step 5: Apply it to the dev database**

```bash
go run ./cmd/migrate up && go run ./cmd/migrate status
```
Expected: `00002_deployment_stopped_at.sql` is listed as applied. Confirm the Node API still works: `curl -s -X POST localhost:8080/dev/login` returns a token, provided the Node API is running.

From now on, do **not** run `npm run prisma:migrate` in `control-plane/`. Prisma would see the new column as drift and try to drop it. goose owns the schema.

- [ ] **Step 6: Commit**

```bash
git add migrations internal/testutil
git commit -m "feat(go): add Deployment.stoppedAt (additive, Node-compatible)"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 5a: db and test-helper hardening

Added by CTO decision (`review/decisions.md`, "2026-10-08 Plan 1 Task 4: db hardening round (Staff proposal) becomes Task 5a"). Closes backlog items SEC-3, STAFF-4, STAFF-5, STAFF-6 (T3); SEC-5, STAFF-7, STAFF-8, STAFF-9 (T3r1); STAFF-1..5, SEC-2 (T4).

**Files:**
- Modify: `control-plane-go/internal/db/{db.go,migrate.go,db_test.go,migrate_test.go}`, `control-plane-go/cmd/migrate/main.go`, `control-plane-go/internal/testutil/{postgres.go,fakes.go,postgres_test.go}`, `.github/workflows/ci.yml`
- Create: `control-plane-go/internal/db/{main_test.go,migrate_integration_test.go}`

Test first; one commit per numbered group; run the full CI command set plus `gosec -quiet -exclude=G204 ./...` before each commit; do not touch `control-plane/`.

1. **Connect (SEC-5, SEC-3)** — `internal/db/db.go`. Extract `func poolConfig(rawURL string) (*pgxpool.Config, error)` (normalizeURL + `pgxpool.ParseConfig` + runtime params) and have `Connect` use it. A `ParseConfig` error returns the fixed error `parse DATABASE_URL: invalid connection string` (no `%w`, no cause text). If `cfg.ConnConfig.ConnectTimeout == 0`, set it to 10s (an explicit `connect_timeout` in the URL/DSN wins). `Ping` runs under `context.WithTimeout(ctx, 10*time.Second)`. Fix the `normalizeURL` comment: pgx only redacts a well-formed `password=` token, which is why Connect drops the cause. Tests (unit, `package db`): `poolConfig` errors for `host=h password = PW1X`, `host=h password=PW2\ X`, `host=h password='PW3X`, `host=h connect_timeout='2 password=PW4X`, `host=h Password=PW5X` (adjust each to an input pgx actually rejects; if pgx accepts one, assert the parsed config does not carry the secret anywhere in the error instead and say so in the report) each return an error whose text contains neither the secret nor `password`; `poolConfig("postgres://h/db")` has ConnectTimeout 10s; `poolConfig("postgres://h/db?connect_timeout=3")` keeps 3s.
2. **normalizeURL (STAFF-5 T3)** — `search_path` is set to `pgx.Identifier{schema}.Sanitize()` (so `schema=MySchema` means the case-sensitive schema Prisma used); more than one `schema=` value is an error `DATABASE_URL has more than one schema parameter`; the Prisma-only params `connection_limit`, `pool_timeout`, `socket_timeout`, `pgbouncer` are removed before pgx sees them. Table test rows for each, plus the existing tests updated (`public` → `"public"`). An integration test (group 4) connects with `?schema=public` and runs a query, proving the quoted form works as a startup parameter.
3. **goose Provider and one lock (STAFF-4, STAFF-9, STAFF-7, STAFF-8)** — `internal/db/migrate.go`. Remove every goose global (`SetBaseFS`, `SetDialect`, `UpContext`, `StatusContext`, `EnsureDBVersionContext`). Define `const migrationLockID int64` (keep the current `0x1F2A_B0A5_E11E` value) and build one `lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID))`. `Migrate` = `goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS, goose.WithSessionLocker(locker))` then `Up`. `Status` uses `Provider.Status` and returns the statuses (`func Status(ctx, pool) ([]*goose.MigrationStatus, error)`); `cmd/migrate status` prints one line per migration (`<version> <file> applied <time>` or `pending`); note the output change in the report. `MarkBaseline`: take a dedicated `*sql.Conn` from `stdlib.OpenDBFromPool(pool)`, `locker.SessionLock` it (deferred `SessionUnlock`), then in one transaction on that conn: re-check `baselineApplied` (return nil if done), `checkPrismaSchema`, and if `goose_db_version` does not exist create it with `database.NewStore(database.DialectPostgres, "goose_db_version")` `.CreateVersionTable` and `.Insert(InsertRequest{Version: 0})`, then `.Insert(InsertRequest{Version: 1})`; commit. The read-only pre-check may stay or go; a refused baseline must still leave no `goose_db_version` table (DDL rolls back). `baselineLockID` and `pg_advisory_xact_lock` go away (the shared session lock replaces them). `checkPrismaSchema` refuses finished rows not in `prismaMigrations` as `incompleteErr("unknown", …)` with the message reworded to say the schema diverged from the frozen Node schema (STAFF-7). `TestPrismaMigrationsMatchFrozenNodeSchema` uses `t.Fatalf` (STAFF-8). If a goose API named here differs in v3.28.0, use the v3.28.0 equivalent and report it; never fall back to globals.
4. **Integration tests (STAFF-6, STAFF-5 T4)** — testutil: split `StartEmptyPostgres(ctx) (adminURL string, cleanup func(), err error)` (container only, no migrations) from `StartPostgres` (= empty + Connect + Migrate, unchanged signature); add `RunWithEmptyPostgres(m *testing.M, url *string)` (same -short behaviour as `RunWithPostgres`) and `FreshDB(t *testing.T, adminURL string) (url string, pool *pgxpool.Pool)` that creates a uniquely named database, connects with `db.Connect`, and drops it in `t.Cleanup`. In `internal/db`: `main_test.go` (`package db_test`, the only TestMain in the directory) calls `RunWithEmptyPostgres`; `migrate_integration_test.go` (`package db_test`, every test calls `FreshDB`, skipped under -short) with a helper that applies `control-plane/prisma/migrations/*/migration.sql` in order and inserts matching finished `_prisma_migrations` rows (`t.Fatalf` if the directory is missing). Cases: up on empty → latest version, second `Migrate` no-op; Prisma DB + one `User` row → `MarkBaseline` → `Migrate` → latest version, row still there; baseline twice → goose rows exactly (0, 1) before `up`; baseline on empty DB → "no existing schema" and no `goose_db_version`; one Prisma row deleted → "missing" and no goose table; one row with `finished_at` NULL → "unfinished"; one extra finished row → "unknown"; `Status` on a migrated DB → all applied; 4 goroutines `MarkBaseline` on one Prisma DB → all nil, rows (0, 1); 4 goroutines `Migrate` on one empty DB → all nil, latest version; schema equivalence (the CI drift guard): Prisma DB vs a goose DB migrated with `UpTo(1)` (an unexported-test-only helper or `MigrateTo(ctx, pool, v)`, your choice, keep it minimal) compare, excluding `_prisma_migrations` and `goose_db_version`, the sorted sets of columns (table, column, data_type, udt_name, is_nullable, column_default), `pg_indexes.indexdef`, `pg_get_constraintdef` per (table, constraint name), and `pg_enum` labels per type, and fail with the diff.
5. **Test-helper lows (STAFF-1..4 T4)** — `RunWithPostgres`/`RunWithEmptyPostgres` print the error plus `(start Docker, or run with -short to skip integration tests)` and still exit 1. Doc comments: cleanup relies on Ryuk if a test panics or hits -timeout, never disable Ryuk on GitHub-hosted CI; `NeedDB` must not be used with `t.Parallel`. `NeedDB` truncates every table in `current_schema()` except `goose_db_version` (read from `pg_tables`, quoted with `pgx.Identifier`), in one `TRUNCATE … CASCADE`, using `t.Context()`. `FakeRunner.Run` stores `append([]string(nil), args...)`; doc that `Handle` may be called concurrently and must be safe for that. Tests: `TestFakeRunnerCopiesArgs` (mutate the caller's slice after `Run`, `Commands()` unchanged); a `NeedDB` test that inserts a row in a table created by the test and sees it truncated.
6. **CI guard (SEC-2 T4)** — in the go job after `go build`: `if go list -deps ./cmd/... | grep -E 'testcontainers|github.com/moby/|infrapilot/internal/testutil'; then echo "::error::production binaries must not import test helpers"; exit 1; fi`.
Done when: CI command set green locally with and without `-short`; `go run ./cmd/migrate status` against the dev DB lists 00001 and 00002 applied and `migrate up` is a no-op; dev DB row counts unchanged.

**Fix round 1:** CTO decision 2026-10-08 (one lock held by us, `up` refuses un-baselined Prisma); see `review/decisions.md`.

**Review gate:** Security, Staff, Verifier (no Product: nothing user-visible). Verifier for 5a, beyond the full suite: Verifier check (replaces the 5a concurrent check). On a scratch DB restored from the Prisma-only schema: (i) start two `migrate baseline` and two `migrate up` at the same time, 5 runs on fresh scratch DBs. Both baselines exit 0. Each `up` exits 0 or exits 1 with exactly the "run `migrate baseline` first" message, never a Postgres error (23505/42P07/42710). One further `migrate up` then exits 0 and the rows are (0, 1, 2). (ii) Staged order (two baselines together, then two ups together): all exit 0, rows (0, 1, 2). (iii) Hold `pg_advisory_lock(0x1F2AB0A5E11E)` in psql and start `migrate up` on an empty DB: `goose_db_version` does not appear while the lock is held, and `up` finishes after unlock. (iv) `migrate up` on an un-baselined Prisma DB → the refusal, and no `goose_db_version`. (v) `migrate status` on an empty DB creates nothing. The other 5a checks are unchanged and only need rerunning if `db.go` changes (it should not). Also: `migrate status` against the dev DB; a malformed keyword DSN in `DATABASE_URL` makes `migrate status` fail without printing the password; `DATABASE_URL` pointing at an unroutable host fails within ~10 s; Node API `POST /dev/login` still works.

Record: `docs/superpowers/review/plan1/task-5a.md`.

---

# Part 1 — Data layer

### Task 6: Store core, models and users

**Files:**
- Create: `control-plane-go/internal/store/store.go`, `control-plane-go/internal/store/models.go`, `control-plane-go/internal/store/users.go`
- Test: `control-plane-go/internal/store/models_test.go`, `control-plane-go/internal/store/main_test.go`, `control-plane-go/internal/store/users_test.go`

- [ ] **Step 1: Add the cuid dependency**

Run: `go get github.com/lucsky/cuid`

- [ ] **Step 2: Write the failing tests**

`internal/store/models_test.go` (internal package, so it needs no database):
```go
package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTimestampMarshalsLikeJavaScriptToISOString(t *testing.T) {
	ts := Timestamp{time.Date(2026, 1, 2, 3, 4, 5, 120_000_000, time.UTC)}
	b, err := json.Marshal(ts)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"2026-01-02T03:04:05.120Z"` {
		t.Fatalf("got %s", b)
	}
}

func TestTimestampConvertsToUTC(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	ts := Timestamp{time.Date(2026, 1, 2, 9, 0, 0, 0, ist)}
	b, _ := json.Marshal(ts)
	if string(b) != `"2026-01-02T03:30:00.000Z"` {
		t.Fatalf("got %s", b)
	}
}

func TestServiceTypeHelpers(t *testing.T) {
	if !IsManaged("redis") || !IsManaged("mongodb") || IsManaged("nodejs") {
		t.Fatal("IsManaged wrong")
	}
	for _, typ := range []string{"docker", "nodejs", "nextjs", "react"} {
		if !NeedsRepo(typ) {
			t.Fatalf("NeedsRepo(%q) = false", typ)
		}
	}
	if NeedsRepo("redis") || !ValidServiceType("nextjs") || ValidServiceType("php") {
		t.Fatal("service type helpers wrong")
	}
}
```

`internal/store/main_test.go`:
```go
package store_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

func newStore(t *testing.T) *store.Store {
	t.Helper()
	return store.New(testutil.NeedDB(t, pool))
}
```

`internal/store/users_test.go`:
```go
package store_test

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/store"
)

func TestCreateAndGetUser(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	name := "Ada"
	u, err := st.CreateUser(ctx, "ada@example.com", "hash", &name)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.GetUserByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != u.ID || got.Password != "hash" || got.Name == nil || *got.Name != "Ada" {
		t.Fatalf("got %+v", got)
	}
}

func TestCreateUserDuplicateEmail(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	if _, err := st.CreateUser(ctx, "a@b.c", "h", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(ctx, "a@b.c", "h", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestGetUserByEmailNotFound(t *testing.T) {
	st := newStore(t)
	if _, err := st.GetUserByEmail(context.Background(), "nobody@x.y"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestUpsertDevUserIsIdempotent(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, err := st.UpsertDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.UpsertDevUser(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.Email != "dev@local" {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL with `undefined: Timestamp` (and other undefined names).

- [ ] **Step 4: Implement the store core**

`internal/store/store.go`:
```go
// Package store is the Postgres data layer. Table and column names come from
// the schema Prisma created, so every identifier is double-quoted.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	db   DBTX
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{db: pool, pool: pool} }

// InTx runs fn in a transaction. fn receives a Store bound to that transaction;
// returning an error rolls it back.
func (s *Store) InTx(ctx context.Context, fn func(tx *Store) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		return fn(&Store{db: tx, pool: s.pool})
	})
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func noRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// collect scans every row with scan. It never returns a nil slice, so an
// empty result encodes as [] in JSON.
func collect[T any](rows pgx.Rows, err error, scan func(pgx.Row) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
```

`internal/store/models.go`:
```go
package store

import "time"

// Timestamp marshals like JavaScript's Date.toISOString(), which is how Prisma
// returned dates: millisecond precision, UTC, "Z" suffix.
type Timestamp struct{ time.Time }

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return []byte(`"` + t.UTC().Format("2006-01-02T15:04:05.000Z07:00") + `"`), nil
}

const (
	StatusQueued    = "queued"
	StatusBuilding  = "building"
	StatusDeploying = "deploying"
	StatusDeployed  = "deployed"
	StatusFailed    = "failed"
)

const (
	TypeDocker  = "docker"
	TypeNodejs  = "nodejs"
	TypeNextjs  = "nextjs"
	TypeReact   = "react"
	TypeMongoDB = "mongodb"
	TypeRedis   = "redis"
)

// IsManaged reports whether the service type runs a stock image and skips the build.
func IsManaged(serviceType string) bool {
	return serviceType == TypeMongoDB || serviceType == TypeRedis
}

// NeedsRepo reports whether the service type is built from a git repository.
func NeedsRepo(serviceType string) bool {
	switch serviceType {
	case TypeDocker, TypeNodejs, TypeNextjs, TypeReact:
		return true
	}
	return false
}

func ValidServiceType(t string) bool { return NeedsRepo(t) || IsManaged(t) }

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Password  string    `json:"-"`
	Name      *string   `json:"name"`
	CreatedAt Timestamp `json:"createdAt"`
	UpdatedAt Timestamp `json:"updatedAt"`
}

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerUserID string    `json:"ownerUserId"`
	CreatedAt   Timestamp `json:"createdAt"`
}

type Service struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"projectId"`
	Name        string    `json:"name"`
	ServiceType string    `json:"serviceType"`
	RepoURL     *string   `json:"repoUrl"`
	Branch      string    `json:"branch"`
	CreatedAt   Timestamp `json:"createdAt"`
	PositionX   float64   `json:"positionX"`
	PositionY   float64   `json:"positionY"`
}

type EnvVar struct {
	ID        string    `json:"id"`
	ServiceID string    `json:"serviceId"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	CreatedAt Timestamp `json:"createdAt"`
}

type Deployment struct {
	ID            string    `json:"id"`
	ServiceID     string    `json:"serviceId"`
	CommitSha     string    `json:"commitSha"`
	Image         *string   `json:"image"`
	Status        string    `json:"status"`
	ContainerID   *string   `json:"containerId"`
	HostPort      *int      `json:"hostPort"`
	ContainerPort *int      `json:"containerPort"`
	RuntimeURL    *string   `json:"runtimeUrl"`
	VolumePath    *string   `json:"volumePath"`
	CreatedAt     Timestamp `json:"createdAt"`
	UpdatedAt     Timestamp `json:"updatedAt"`
}

type ServiceConnection struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"sourceId"`
	TargetID  string    `json:"targetId"`
	Label     *string   `json:"label"`
	CreatedAt Timestamp `json:"createdAt"`
}

// OutboxEvent is a claimed row waiting to be published.
type OutboxEvent struct {
	ID       string
	Type     string
	Payload  []byte
	Attempts int
}
```

`internal/store/users.go`:
```go
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const userCols = `"id", "email", "password", "name", "createdAt", "updatedAt"`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.Password, &u.Name, &u.CreatedAt.Time, &u.UpdatedAt.Time)
	return u, noRows(err)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(s.db.QueryRow(ctx, `SELECT `+userCols+` FROM "User" WHERE "email" = $1`, email))
}

// CreateUser returns ErrConflict when the email is already registered.
func (s *Store) CreateUser(ctx context.Context, email, passwordHash string, name *string) (User, error) {
	u, err := scanUser(s.db.QueryRow(ctx,
		`INSERT INTO "User" ("id", "email", "password", "name") VALUES ($1, $2, $3, $4) RETURNING `+userCols,
		cuid.New(), email, passwordHash, name))
	if isUniqueViolation(err) {
		return User{}, ErrConflict
	}
	return u, err
}

// UpsertDevUser finds or creates dev@local for POST /dev/login.
func (s *Store) UpsertDevUser(ctx context.Context) (User, error) {
	return scanUser(s.db.QueryRow(ctx,
		`INSERT INTO "User" ("id", "email", "password") VALUES ($1, 'dev@local', 'dev-password-not-used')
		 ON CONFLICT ("email") DO UPDATE SET "email" = EXCLUDED."email"
		 RETURNING `+userCols, cuid.New()))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/store/`
Expected: `ok  infrapilot/internal/store`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "feat(go): store core, JSON models and user queries"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 7: Projects, services, env vars and the `Seed` fixture

> **Note from Task 6 review (2026-10-08):** Task 6 added `//nolint:unused` on `store.collect` because its first caller is here. Remove that directive in this task's commit (nolintlint is not enabled, so nothing else will flag it). Backlog STAFF-1 (T6) suggests adding timestamp-UTC and no-password-in-JSON assertions alongside this task's scan tests (optional, low).

**Files:**
- Create: `control-plane-go/internal/store/projects.go`, `control-plane-go/internal/store/services.go`, `control-plane-go/internal/store/envvars.go`, `control-plane-go/internal/testutil/fixtures.go`, `control-plane-go/internal/store/deployments.go` (a one-function stub that Task 8 replaces)
- Test: `control-plane-go/internal/store/projects_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/store/projects_test.go`:
```go
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func TestListProjectsEmptyIsJSONArray(t *testing.T) {
	st := newStore(t)
	ps, err := st.ListProjects(context.Background(), "nobody")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(ps)
	if string(b) != "[]" {
		t.Fatalf("got %s", b)
	}
}

func TestProjectsAreScopedToOwner(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	other, _ := st.CreateUser(ctx, "other@x.y", "h", nil)

	if _, err := st.GetProjectForOwner(ctx, f.ProjectID, f.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetProjectForOwner(ctx, f.ProjectID, other.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	ps, _ := st.ListProjects(ctx, f.UserID)
	if len(ps) != 1 || ps[0].Name != "proj" {
		t.Fatalf("got %+v", ps)
	}
}

func TestServicesCRUD(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	svc, err := st.GetService(ctx, f.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if svc.ServiceType != "nodejs" || svc.Branch != "main" || svc.RepoURL == nil {
		t.Fatalf("got %+v", svc)
	}

	all, _ := st.ListServices(ctx, "")
	scoped, _ := st.ListServices(ctx, f.ProjectID)
	none, _ := st.ListServices(ctx, "missing")
	if len(all) != 1 || len(scoped) != 1 || len(none) != 0 {
		t.Fatalf("all=%d scoped=%d none=%d", len(all), len(scoped), len(none))
	}

	if err := st.UpdateServicePosition(ctx, f.ServiceID, 10.5, 20); err != nil {
		t.Fatal(err)
	}
	svc, _ = st.GetServiceForOwner(ctx, f.ServiceID, f.UserID)
	if svc.PositionX != 10.5 || svc.PositionY != 20 {
		t.Fatalf("position not saved: %+v", svc)
	}
	if _, err := st.GetServiceForOwner(ctx, f.ServiceID, "someone-else"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if err := st.UpdateServicePosition(ctx, "missing", 1, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestCanvasServicesCountDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	for range 2 {
		if _, err := st.CreateDeployment(ctx, f.ServiceID, "main"); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := st.ListCanvasServices(ctx, f.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].DeploymentCount != 2 || cs[0].Name != "svc" {
		t.Fatalf("got %+v", cs)
	}
}

func TestEnvVars(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	if _, err := st.UpsertEnvVar(ctx, f.ServiceID, "B", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertEnvVar(ctx, f.ServiceID, "A", "x"); err != nil {
		t.Fatal(err)
	}
	ev, err := st.UpsertEnvVar(ctx, f.ServiceID, "B", "2")
	if err != nil || ev.Value != "2" {
		t.Fatalf("upsert overwrite: %+v %v", ev, err)
	}

	list, _ := st.ListEnvVars(ctx, f.ServiceID)
	if len(list) != 2 || list[0].Key != "A" || list[1].Value != "2" {
		t.Fatalf("got %+v", list)
	}
	m, _ := st.EnvMap(ctx, f.ServiceID)
	if m["A"] != "x" || m["B"] != "2" {
		t.Fatalf("EnvMap = %v", m)
	}
	if err := st.DeleteEnvVar(ctx, f.ServiceID, "A"); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListEnvVars(ctx, f.ServiceID)
	if len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL with `undefined: testutil.Seed` and the store methods undefined.

- [ ] **Step 3: Implement projects**

`internal/store/projects.go`:
```go
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const projectCols = `"id", "name", "ownerUserId", "createdAt"`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.OwnerUserID, &p.CreatedAt.Time)
	return p, noRows(err)
}

func (s *Store) CreateProject(ctx context.Context, name, ownerUserID string) (Project, error) {
	return scanProject(s.db.QueryRow(ctx,
		`INSERT INTO "Project" ("id", "name", "ownerUserId") VALUES ($1, $2, $3) RETURNING `+projectCols,
		cuid.New(), name, ownerUserID))
}

func (s *Store) ListProjects(ctx context.Context, ownerUserID string) ([]Project, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+projectCols+` FROM "Project" WHERE "ownerUserId" = $1 ORDER BY "createdAt" DESC`, ownerUserID)
	return collect(rows, err, scanProject)
}

func (s *Store) GetProjectForOwner(ctx context.Context, id, ownerUserID string) (Project, error) {
	return scanProject(s.db.QueryRow(ctx,
		`SELECT `+projectCols+` FROM "Project" WHERE "id" = $1 AND "ownerUserId" = $2`, id, ownerUserID))
}
```

- [ ] **Step 4: Implement services**

`internal/store/services.go`:
```go
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const serviceCols = `"id", "projectId", "name", "serviceType"::text, "repoUrl", "branch", "createdAt", "positionX", "positionY"`

func scanService(row pgx.Row) (Service, error) {
	var v Service
	err := row.Scan(&v.ID, &v.ProjectID, &v.Name, &v.ServiceType, &v.RepoURL, &v.Branch,
		&v.CreatedAt.Time, &v.PositionX, &v.PositionY)
	return v, noRows(err)
}

type NewService struct {
	ProjectID   string
	Name        string
	ServiceType string
	RepoURL     *string
	Branch      string
}

func (s *Store) CreateService(ctx context.Context, n NewService) (Service, error) {
	return scanService(s.db.QueryRow(ctx,
		`INSERT INTO "Service" ("id", "projectId", "name", "serviceType", "repoUrl", "branch")
		 VALUES ($1, $2, $3, $4::"ServiceType", $5, $6) RETURNING `+serviceCols,
		cuid.New(), n.ProjectID, n.Name, n.ServiceType, n.RepoURL, n.Branch))
}

func (s *Store) GetService(ctx context.Context, id string) (Service, error) {
	return scanService(s.db.QueryRow(ctx, `SELECT `+serviceCols+` FROM "Service" WHERE "id" = $1`, id))
}

// ListServices returns all services, or one project's when projectID is not empty. Newest first.
func (s *Store) ListServices(ctx context.Context, projectID string) ([]Service, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+serviceCols+` FROM "Service" WHERE ($1 = '' OR "projectId" = $1) ORDER BY "createdAt" DESC`,
		projectID)
	return collect(rows, err, scanService)
}

func (s *Store) GetServiceForOwner(ctx context.Context, id, ownerUserID string) (Service, error) {
	return scanService(s.db.QueryRow(ctx,
		`SELECT `+serviceCols+` FROM "Service"
		 WHERE "id" = $1 AND "projectId" IN (SELECT "id" FROM "Project" WHERE "ownerUserId" = $2)`,
		id, ownerUserID))
}

func (s *Store) UpdateServicePosition(ctx context.Context, id string, x, y float64) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Service" SET "positionX" = $2, "positionY" = $3 WHERE "id" = $1`, id, x, y)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateServicePositionInProject ignores services outside projectID (no error), like Prisma's updateMany.
func (s *Store) UpdateServicePositionInProject(ctx context.Context, id, projectID string, x, y float64) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "Service" SET "positionX" = $3, "positionY" = $4 WHERE "id" = $1 AND "projectId" = $2`,
		id, projectID, x, y)
	return err
}

// CanvasService is the canvas view of a service.
type CanvasService struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	ServiceType     string  `json:"serviceType"`
	PositionX       float64 `json:"positionX"`
	PositionY       float64 `json:"positionY"`
	DeploymentCount int     `json:"deploymentCount"`
}

func (s *Store) ListCanvasServices(ctx context.Context, projectID string) ([]CanvasService, error) {
	rows, err := s.db.Query(ctx,
		`SELECT s."id", s."name", s."serviceType"::text, s."positionX", s."positionY",
		        (SELECT count(*) FROM "Deployment" d WHERE d."serviceId" = s."id")::int
		 FROM "Service" s WHERE s."projectId" = $1 ORDER BY s."createdAt" ASC`, projectID)
	return collect(rows, err, func(row pgx.Row) (CanvasService, error) {
		var c CanvasService
		err := row.Scan(&c.ID, &c.Name, &c.ServiceType, &c.PositionX, &c.PositionY, &c.DeploymentCount)
		return c, err
	})
}
```

- [ ] **Step 5: Implement env vars**

`internal/store/envvars.go`:
```go
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const envCols = `"id", "serviceId", "key", "value", "createdAt"`

func scanEnvVar(row pgx.Row) (EnvVar, error) {
	var e EnvVar
	err := row.Scan(&e.ID, &e.ServiceID, &e.Key, &e.Value, &e.CreatedAt.Time)
	return e, noRows(err)
}

func (s *Store) ListEnvVars(ctx context.Context, serviceID string) ([]EnvVar, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+envCols+` FROM "EnvVar" WHERE "serviceId" = $1 ORDER BY "key" ASC`, serviceID)
	return collect(rows, err, scanEnvVar)
}

func (s *Store) UpsertEnvVar(ctx context.Context, serviceID, key, value string) (EnvVar, error) {
	return scanEnvVar(s.db.QueryRow(ctx,
		`INSERT INTO "EnvVar" ("id", "serviceId", "key", "value") VALUES ($1, $2, $3, $4)
		 ON CONFLICT ("serviceId", "key") DO UPDATE SET "value" = EXCLUDED."value"
		 RETURNING `+envCols,
		cuid.New(), serviceID, key, value))
}

func (s *Store) DeleteEnvVar(ctx context.Context, serviceID, key string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM "EnvVar" WHERE "serviceId" = $1 AND "key" = $2`, serviceID, key)
	return err
}

// EnvMap returns a service's env vars as key → value for docker run.
func (s *Store) EnvMap(ctx context.Context, serviceID string) (map[string]string, error) {
	vars, err := s.ListEnvVars(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(vars))
	for _, v := range vars {
		m[v.Key] = v.Value
	}
	return m, nil
}
```

- [ ] **Step 6: Implement the fixture**

`internal/testutil/fixtures.go`:
```go
package testutil

import (
	"context"
	"testing"

	"github.com/lucsky/cuid"

	"infrapilot/internal/store"
)

// Fixture holds the IDs created by Seed.
type Fixture struct {
	UserID    string
	ProjectID string
	ServiceID string
}

// Seed creates a user, a project named "proj" and a service named "svc" of the given type.
func Seed(t *testing.T, st *store.Store, serviceType string) Fixture {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, cuid.New()+"@test.local", "hash", nil)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	p, err := st.CreateProject(ctx, "proj", u.ID)
	if err != nil {
		t.Fatalf("seed project: %v", err)
	}
	var repoURL *string
	if store.NeedsRepo(serviceType) {
		repo := "https://github.com/example/app.git"
		repoURL = &repo
	}
	svc, err := st.CreateService(ctx, store.NewService{
		ProjectID: p.ID, Name: "svc", ServiceType: serviceType, RepoURL: repoURL, Branch: "main",
	})
	if err != nil {
		t.Fatalf("seed service: %v", err)
	}
	return Fixture{UserID: u.ID, ProjectID: p.ID, ServiceID: svc.ID}
}
```

`TestCanvasServicesCountDeployments` calls `CreateDeployment` from Task 8, so this package will not compile until then. Add this minimal version of `internal/store/deployments.go` now; Task 8 replaces the whole file:
```go
package store

import (
	"context"

	"github.com/lucsky/cuid"
)

func (s *Store) CreateDeployment(ctx context.Context, serviceID, commitSha string) (string, error) {
	id := cuid.New()
	_, err := s.db.Exec(ctx,
		`INSERT INTO "Deployment" ("id", "serviceId", "commitSha", "status", "updatedAt")
		 VALUES ($1, $2, $3, 'queued', now())`, id, serviceID, commitSha)
	return id, err
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `go test ./internal/store/`
Expected: `ok  infrapilot/internal/store`

- [ ] **Step 8: Commit**

```bash
git add internal/store internal/testutil/fixtures.go
git commit -m "feat(go): project, service and env var queries"
```

- [ ] **Step 9: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 8: Deployments, jobs and container lifecycle

**Files:**
- Modify: `control-plane-go/internal/store/models.go` (add `StoppedAt`)
- Modify: `control-plane-go/internal/store/deployments.go` (replace the whole file)
- Test: `control-plane-go/internal/store/deployments_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/store/deployments_test.go`:
```go
package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

func TestCreateAndListDeployments(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)

	first, err := st.CreateDeployment(ctx, f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != store.StatusQueued || first.CommitSha != "main" || first.Image != nil || first.StoppedAt != nil {
		t.Fatalf("got %+v", first)
	}
	if _, err := pool.Exec(ctx, `UPDATE "Deployment" SET "createdAt" = now() - interval '1 minute' WHERE "id" = $1`, first.ID); err != nil {
		t.Fatal(err)
	}
	second, _ := st.CreateDeployment(ctx, f.ServiceID, "abc1234")

	list, err := st.ListDeployments(ctx, f.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("want newest first, got %+v", list)
	}
	all, _ := st.ListDeployments(ctx, "")
	other, _ := st.ListDeployments(ctx, "missing")
	if len(all) != 2 || len(other) != 0 {
		t.Fatalf("all=%d other=%d", len(all), len(other))
	}
	ok, _ := st.DeploymentExists(ctx, first.ID)
	missing, _ := st.DeploymentExists(ctx, "nope")
	if !ok || missing {
		t.Fatal("DeploymentExists wrong")
	}
	b, _ := json.Marshal(first)
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, has := m["stoppedAt"]; !has || len(m) != 13 {
		t.Fatalf("JSON keys: %v", m)
	}
}

func TestClaimDeploymentHasExactlyOneWinner(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "main")

	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := st.ClaimDeployment(ctx, d.ID, store.StatusQueued, store.StatusDeploying)
			if err != nil {
				t.Error(err)
			}
			if won {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d winners, want 1", wins.Load())
	}
	got, _ := st.GetDeployment(ctx, d.ID)
	if got.Status != store.StatusDeploying {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestGetJobJoinsServiceAndProject(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "abc1234")

	j, err := st.GetJob(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if j.DeploymentID != d.ID || j.ServiceID != f.ServiceID || j.ProjectID != f.ProjectID ||
		j.ServiceType != "nodejs" || j.Branch != "main" || j.CommitSha != "abc1234" ||
		j.RepoURL == nil || j.Image != nil || j.Status != store.StatusQueued {
		t.Fatalf("got %+v", j)
	}
	if _, err := st.GetJob(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildAndDeployLifecycle(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeNodejs)
	old, _ := st.CreateDeployment(ctx, f.ServiceID, "v1")
	cur, _ := st.CreateDeployment(ctx, f.ServiceID, "v2")

	if err := st.MarkBuilt(ctx, cur.ID, "reg/svc:v2"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []store.Deployment{old, cur} {
		err := st.MarkDeployed(ctx, d.ID, store.DeployedInfo{
			ContainerID: "ctr-" + d.ID, ContainerPort: 3000, RuntimeURL: ptr("http://x.localhost"),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.GetDeployment(ctx, cur.ID)
	if got.Status != store.StatusDeployed || *got.Image != "reg/svc:v2" || got.HostPort != nil ||
		*got.ContainerPort != 3000 || *got.RuntimeURL != "http://x.localhost" {
		t.Fatalf("got %+v", got)
	}

	live, _ := st.ListLiveDeployments(ctx, f.ServiceID, cur.ID)
	if len(live) != 1 || live[0].ID != old.ID {
		t.Fatalf("live = %+v", live)
	}
	running, _ := st.ListRunningDeployments(ctx)
	if len(running) != 2 {
		t.Fatalf("running = %+v", running)
	}

	if err := st.MarkStopped(ctx, []string{old.ID}); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkStopped(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stopped, _ := st.GetDeployment(ctx, old.ID)
	if stopped.StoppedAt == nil {
		t.Fatal("stoppedAt not set")
	}
	live, _ = st.ListLiveDeployments(ctx, f.ServiceID, cur.ID)
	running, _ = st.ListRunningDeployments(ctx)
	if len(live) != 0 || len(running) != 1 || running[0].ID != cur.ID {
		t.Fatalf("after stop: live=%d running=%+v", len(live), running)
	}
}

func TestManagedDeploymentKeepsHostPortAndVolume(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	f := testutil.Seed(t, st, store.TypeRedis)
	d, _ := st.CreateDeployment(ctx, f.ServiceID, "latest")
	err := st.MarkDeployed(ctx, d.ID, store.DeployedInfo{
		ContainerID: "c", HostPort: ptr(20001), ContainerPort: 6379, VolumePath: ptr("cp-vol-x"),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetDeployment(ctx, d.ID)
	if *got.HostPort != 20001 || *got.VolumePath != "cp-vol-x" || got.RuntimeURL != nil {
		t.Fatalf("got %+v", got)
	}
	ports, _ := st.UsedHostPorts(ctx)
	if !slices.Equal(ports, []int{20001}) {
		t.Fatalf("ports = %v", ports)
	}
	if err := st.SetDeploymentStatus(ctx, "missing", store.StatusFailed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run 'Deployment|Claim|Job|Lifecycle'`
Expected: FAIL (compile errors): `first.Status undefined` because the stub returns `string`, plus `undefined: GetJob` and `undefined: StoppedAt`.

- [ ] **Step 3: Add `StoppedAt` to the model**

In `internal/store/models.go`, inside `type Deployment struct`, replace:
```go
	UpdatedAt     Timestamp `json:"updatedAt"`
}
```
with:
```go
	UpdatedAt     Timestamp  `json:"updatedAt"`
	StoppedAt     *Timestamp `json:"stoppedAt"`
}
```

- [ ] **Step 4: Implement**

Replace `internal/store/deployments.go` with:
```go
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

const deploymentCols = `"id", "serviceId", "commitSha", "image", "status"::text, "containerId", "hostPort", "containerPort", "runtimeUrl", "volumePath", "createdAt", "updatedAt", "stoppedAt"`

func scanDeployment(row pgx.Row) (Deployment, error) {
	var d Deployment
	var stopped *time.Time
	err := row.Scan(&d.ID, &d.ServiceID, &d.CommitSha, &d.Image, &d.Status, &d.ContainerID,
		&d.HostPort, &d.ContainerPort, &d.RuntimeURL, &d.VolumePath, &d.CreatedAt.Time, &d.UpdatedAt.Time, &stopped)
	if stopped != nil {
		d.StoppedAt = &Timestamp{*stopped}
	}
	return d, noRows(err)
}

// CreateDeployment inserts a queued deployment. "updatedAt" has no DB default
// (Prisma set it in app code), so it is set explicitly.
func (s *Store) CreateDeployment(ctx context.Context, serviceID, commitSha string) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(ctx,
		`INSERT INTO "Deployment" ("id", "serviceId", "commitSha", "status", "updatedAt")
		 VALUES ($1, $2, $3, 'queued', now()) RETURNING `+deploymentCols,
		cuid.New(), serviceID, commitSha))
}

func (s *Store) GetDeployment(ctx context.Context, id string) (Deployment, error) {
	return scanDeployment(s.db.QueryRow(ctx, `SELECT `+deploymentCols+` FROM "Deployment" WHERE "id" = $1`, id))
}

func (s *Store) DeploymentExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "Deployment" WHERE "id" = $1)`, id).Scan(&ok)
	return ok, err
}

// ListDeployments returns the 50 newest deployments, optionally for one service.
func (s *Store) ListDeployments(ctx context.Context, serviceID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment" WHERE ($1 = '' OR "serviceId" = $1)
		 ORDER BY "createdAt" DESC LIMIT 50`, serviceID)
	return collect(rows, err, scanDeployment)
}

// ClaimDeployment moves a deployment from one status to another. Only one
// concurrent caller gets true.
func (s *Store) ClaimDeployment(ctx context.Context, id, from, to string) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "status" = $3::"DeploymentStatus", "updatedAt" = now()
		 WHERE "id" = $1 AND "status" = $2::"DeploymentStatus"`, id, from, to)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) SetDeploymentStatus(ctx context.Context, id, status string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "status" = $2::"DeploymentStatus", "updatedAt" = now() WHERE "id" = $1`,
		id, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkBuilt stores the pushed image and returns the deployment to queued for
// the deploy step (the same transition the Node worker used).
func (s *Store) MarkBuilt(ctx context.Context, id, image string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "image" = $2, "status" = 'queued', "updatedAt" = now() WHERE "id" = $1`,
		id, image)
	return err
}

type DeployedInfo struct {
	ContainerID   string
	HostPort      *int    // only for managed DBs exposed on 127.0.0.1 in dev
	ContainerPort int
	RuntimeURL    *string // nil for managed DBs (no HTTP route)
	VolumePath    *string // managed DB volume name; nil keeps the stored value
}

func (s *Store) MarkDeployed(ctx context.Context, id string, info DeployedInfo) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "Deployment"
		 SET "containerId" = $2, "hostPort" = $3, "containerPort" = $4, "runtimeUrl" = $5,
		     "volumePath" = COALESCE($6, "volumePath"), "status" = 'deployed', "updatedAt" = now()
		 WHERE "id" = $1`,
		id, info.ContainerID, info.HostPort, info.ContainerPort, info.RuntimeURL, info.VolumePath)
	return err
}

// UsedHostPorts lists host ports held by deployments whose container still runs.
func (s *Store) UsedHostPorts(ctx context.Context) ([]int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "hostPort" FROM "Deployment" WHERE "hostPort" IS NOT NULL AND "stoppedAt" IS NULL ORDER BY "hostPort"`)
	return collect(rows, err, func(row pgx.Row) (int, error) {
		var p int
		err := row.Scan(&p)
		return p, err
	})
}

// MarkStopped records that these deployments' containers were removed.
func (s *Store) MarkStopped(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx,
		`UPDATE "Deployment" SET "stoppedAt" = now(), "updatedAt" = now()
		 WHERE "id" = ANY($1) AND "stoppedAt" IS NULL`, ids)
	return err
}

// ListLiveDeployments returns a service's deployments that still own a
// container, except exceptID.
func (s *Store) ListLiveDeployments(ctx context.Context, serviceID, exceptID string) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment"
		 WHERE "serviceId" = $1 AND "id" <> $2 AND "containerId" IS NOT NULL AND "stoppedAt" IS NULL
		 ORDER BY "createdAt"`, serviceID, exceptID)
	return collect(rows, err, scanDeployment)
}

// ListRunningDeployments returns deployed, not-stopped deployments with a
// container: the ones whose logs should be streamed.
func (s *Store) ListRunningDeployments(ctx context.Context) ([]Deployment, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+deploymentCols+` FROM "Deployment"
		 WHERE "status" = 'deployed' AND "containerId" IS NOT NULL AND "stoppedAt" IS NULL
		 ORDER BY "createdAt"`)
	return collect(rows, err, scanDeployment)
}

// Job is everything the deploy workflow needs about one deployment.
type Job struct {
	DeploymentID string
	Status       string
	CommitSha    string
	Image        *string
	VolumePath   *string
	ServiceID    string
	ServiceType  string
	RepoURL      *string
	Branch       string
	ProjectID    string
}

func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	var j Job
	err := s.db.QueryRow(ctx,
		`SELECT d."id", d."status"::text, d."commitSha", d."image", d."volumePath",
		        s."id", s."serviceType"::text, s."repoUrl", s."branch", s."projectId"
		 FROM "Deployment" d JOIN "Service" s ON s."id" = d."serviceId" WHERE d."id" = $1`, id).
		Scan(&j.DeploymentID, &j.Status, &j.CommitSha, &j.Image, &j.VolumePath,
			&j.ServiceID, &j.ServiceType, &j.RepoURL, &j.Branch, &j.ProjectID)
	return j, noRows(err)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/store/`
Expected: `ok  infrapilot/internal/store`

- [ ] **Step 6: Commit**

```bash
git add internal/store
git commit -m "feat(go): deployment queries, workflow jobs and container lifecycle"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 9: Outbox queries

**Files:**
- Create: `control-plane-go/internal/store/outbox.go`
- Test: `control-plane-go/internal/store/outbox_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/store/outbox_test.go`:
```go
package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"infrapilot/internal/store"
)

type outboxRow struct {
	Status   string
	Attempts int
	LastErr  *string
	Due      bool
	HasPubAt bool
}

func readOutbox(t *testing.T, id string) outboxRow {
	t.Helper()
	var r outboxRow
	err := pool.QueryRow(context.Background(),
		`SELECT "status"::text, "attempts", "lastError", "nextAttemptAt" <= now(), "publishedAt" IS NOT NULL
		 FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&r.Status, &r.Attempts, &r.LastErr, &r.Due, &r.HasPubAt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaimOutboxBatchReturnsOnlyDueEvents(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	due, _ := st.CreateOutboxEvent(ctx, "build.requested", map[string]string{"deploymentId": "d1"})
	later, _ := st.CreateOutboxEvent(ctx, "build.requested", map[string]string{"deploymentId": "d2"})
	if _, err := pool.Exec(ctx, `UPDATE "OutboxEvent" SET "nextAttemptAt" = now() + interval '1 hour' WHERE "id" = $1`, later); err != nil {
		t.Fatal(err)
	}

	var events []store.OutboxEvent
	err := st.InTx(ctx, func(tx *store.Store) error {
		var err error
		events, err = tx.ClaimOutboxBatch(ctx, 25)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != due || events[0].Type != "build.requested" {
		t.Fatalf("got %+v", events)
	}
	var payload map[string]string
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil || payload["deploymentId"] != "d1" {
		t.Fatalf("payload %s err %v", events[0].Payload, err)
	}
	if r := readOutbox(t, due); r.Status != "publishing" {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestOutboxStatusTransitions(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	b, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	c, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})

	if err := st.MarkOutboxPublished(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxRetry(ctx, b, 3, "boom", 30); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkOutboxDead(ctx, c, 10, "gone"); err != nil {
		t.Fatal(err)
	}

	if r := readOutbox(t, a); r.Status != "published" || !r.HasPubAt || r.LastErr != nil {
		t.Fatalf("a = %+v", r)
	}
	if r := readOutbox(t, b); r.Status != "pending" || r.Attempts != 3 || r.Due || *r.LastErr != "boom" {
		t.Fatalf("b = %+v", r)
	}
	if r := readOutbox(t, c); r.Status != "dead" || r.Attempts != 10 || *r.LastErr != "gone" {
		t.Fatalf("c = %+v", r)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/store/ -run Outbox`
Expected: FAIL with `undefined` errors for the outbox methods.

- [ ] **Step 3: Implement**

`internal/store/outbox.go`:
```go
package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/lucsky/cuid"
)

// CreateOutboxEvent queues an event for the outbox publisher. Call it inside
// InTx together with the state change it announces.
func (s *Store) CreateOutboxEvent(ctx context.Context, eventType string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	id := cuid.New()
	_, err = s.db.Exec(ctx,
		`INSERT INTO "OutboxEvent" ("id", "type", "payload") VALUES ($1, $2, $3::jsonb)`,
		id, eventType, string(body))
	return id, err
}

// ClaimOutboxBatch locks up to limit due events and marks them publishing.
// It must run inside InTx so the row locks last until the batch is settled;
// SKIP LOCKED lets several publishers run without double-publishing.
func (s *Store) ClaimOutboxBatch(ctx context.Context, limit int) ([]OutboxEvent, error) {
	rows, err := s.db.Query(ctx,
		`SELECT "id", "type", "payload"::text, "attempts" FROM "OutboxEvent"
		 WHERE "status" = 'pending' AND "nextAttemptAt" <= now()
		 ORDER BY "createdAt" ASC
		 LIMIT $1
		 FOR UPDATE SKIP LOCKED`, limit)
	events, err := collect(rows, err, func(row pgx.Row) (OutboxEvent, error) {
		var e OutboxEvent
		var payload string
		err := row.Scan(&e.ID, &e.Type, &payload, &e.Attempts)
		e.Payload = []byte(payload)
		return e, err
	})
	if err != nil || len(events) == 0 {
		return events, err
	}
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	_, err = s.db.Exec(ctx, `UPDATE "OutboxEvent" SET "status" = 'publishing' WHERE "id" = ANY($1)`, ids)
	return events, err
}

func (s *Store) MarkOutboxPublished(ctx context.Context, id string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'published', "publishedAt" = now(), "lastError" = NULL WHERE "id" = $1`, id)
	return err
}

func (s *Store) MarkOutboxRetry(ctx context.Context, id string, attempts int, lastErr string, delaySeconds int) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent"
		 SET "status" = 'pending', "attempts" = $2, "lastError" = $3,
		     "nextAttemptAt" = now() + $4::int * interval '1 second'
		 WHERE "id" = $1`, id, attempts, lastErr, delaySeconds)
	return err
}

func (s *Store) MarkOutboxDead(ctx context.Context, id string, attempts int, lastErr string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE "OutboxEvent" SET "status" = 'dead', "attempts" = $2, "lastError" = $3 WHERE "id" = $1`,
		id, attempts, lastErr)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/store/`
Expected: `ok  infrapilot/internal/store`

- [ ] **Step 4b: Add the outbox event type constants**

`internal/store/events.go`:
```go
package store

// Outbox event types. The Node API writes the first two; the Go worker starts
// DeployWorkflow for both (the workflow decides whether to build).
const (
	EventBuildRequested  = "build.requested"
	EventDeployRequested = "deploy.requested"
)
```

Run: `go build ./internal/store/`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "feat(go): outbox claim and status queries"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 10: Encrypted secrets (`internal/secrets`)

**Files:**
- Create: `control-plane-go/internal/secrets/secrets.go`
- Test: `control-plane-go/internal/secrets/secrets_test.go`

Env var values (generated DB passwords now, and every user env var once Plan 2 lands) are stored as `enc:v1:<base64(nonce||ciphertext)>`, using AES-256-GCM. Values without the prefix are legacy plain text and are read as-is.

- [ ] **Step 1: Write the failing tests**

`internal/secrets/secrets_test.go`:
```go
package secrets

import (
	"bytes"
	"strings"
	"testing"
)

func box(t *testing.T) *Box {
	t.Helper()
	b, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	b := box(t)
	enc, err := b.Encrypt("s3cret value")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "enc:v1:") || strings.Contains(enc, "s3cret") || !IsEncrypted(enc) {
		t.Fatalf("enc = %q", enc)
	}
	if got, err := b.Decrypt(enc); err != nil || got != "s3cret value" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	b := box(t)
	x, _ := b.Encrypt("same")
	y, _ := b.Encrypt("same")
	if x == y {
		t.Fatal("two encryptions of the same value must differ")
	}
}

func TestPlainTextPassesThrough(t *testing.T) {
	if got, err := box(t).Decrypt("legacy-plain"); err != nil || got != "legacy-plain" {
		t.Fatalf("got %q err %v", got, err)
	}
	if IsEncrypted("legacy-plain") {
		t.Fatal("plain text reported as encrypted")
	}
}

func TestTamperedOrWrongKeyFails(t *testing.T) {
	b := box(t)
	enc, _ := b.Encrypt("v")
	tampered := enc[:len(enc)-2] + "AA"
	if _, err := b.Decrypt(tampered); err == nil {
		t.Fatal("tampered value must not decrypt")
	}
	other, _ := New(bytes.Repeat([]byte{8}, 32))
	if _, err := other.Decrypt(enc); err == nil {
		t.Fatal("wrong key must not decrypt")
	}
	if _, err := b.Decrypt("enc:v1:%%%"); err == nil {
		t.Fatal("bad base64 must fail")
	}
}

func TestNewRejectsWrongKeySize(t *testing.T) {
	if _, err := New(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected (AES-256 only)")
	}
}

func TestRandomPassword(t *testing.T) {
	a, err := RandomPassword()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RandomPassword()
	if len(a) != 32 || a == b || strings.ContainsAny(a, "+/=\"'$ ") {
		t.Fatalf("a=%q b=%q", a, b)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/secrets/`
Expected: FAIL with `undefined: New`.

- [ ] **Step 3: Implement**

`internal/secrets/secrets.go`:
```go
// Package secrets encrypts env var values at rest with AES-256-GCM.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const prefix = "enc:v1:"

// Box encrypts and decrypts values with one key.
type Box struct{ aead cipher.AEAD }

// New takes the 32-byte key from ENV_ENCRYPTION_KEY.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secrets: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Encrypt returns "enc:v1:" + base64(nonce || ciphertext).
func (b *Box) Encrypt(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Values without the prefix are legacy plain text
// and are returned unchanged.
func (b *Box) Decrypt(value string) (string, error) {
	if !IsEncrypted(value) {
		return value, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", errors.New("secrets: malformed encrypted value")
	}
	n := b.aead.NonceSize()
	if len(raw) < n {
		return "", errors.New("secrets: malformed encrypted value")
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return "", errors.New("secrets: cannot decrypt value (wrong key or tampered)")
	}
	return string(plain), nil
}

func IsEncrypted(value string) bool { return strings.HasPrefix(value, prefix) }

// RandomPassword returns 32 URL-safe characters (24 random bytes), safe to put
// in connection strings and shell-quoted commands.
func RandomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/secrets/`
Expected: `ok  infrapilot/internal/secrets`

- [ ] **Step 5: Commit**

```bash
git add internal/secrets
git commit -m "feat(go): AES-256-GCM encryption for env var values"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

# Part 2 — Logs and observability

### Task 11: Log entry and the Redis hot tier

**Files:**
- Create: `control-plane-go/internal/logs/entry.go`, `control-plane-go/internal/logs/redis.go`
- Test: `control-plane-go/internal/logs/redis_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/redis/go-redis/v9 github.com/testcontainers/testcontainers-go/modules/redis
```

- [ ] **Step 2: Write the failing tests**

`internal/logs/redis_test.go`:
```go
package logs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

func TestFormatTimeMatchesToISOString(t *testing.T) {
	got := logs.FormatTime(time.Date(2026, 3, 4, 5, 6, 7, 8_000_000, time.UTC))
	if got != "2026-03-04T05:06:07.008Z" {
		t.Fatalf("got %s", got)
	}
}

func TestEntryJSONShape(t *testing.T) {
	b, _ := json.Marshal(logs.Entry{DeploymentID: "d", Source: "stdout", Message: "m", Timestamp: "t"})
	if string(b) != `{"deploymentId":"d","source":"stdout","message":"m","timestamp":"t"}` {
		t.Fatalf("got %s", b)
	}
}

func TestRedis(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	url, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := logs.NewRedis(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	live, stop, err := r.Subscribe(ctx, "dep1")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	e1 := logs.Entry{DeploymentID: "dep1", Source: "stdout", Message: "hello", Timestamp: "2026-01-01T00:00:00.000Z"}
	e2 := logs.Entry{DeploymentID: "dep1", Source: "stderr", Message: "oops", Timestamp: "2026-01-01T00:00:01.000Z"}
	for _, e := range []logs.Entry{e1, e2} {
		if err := r.Write(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := r.Read(ctx, "dep1", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != e1 || got[1] != e2 {
		t.Fatalf("Read = %+v", got)
	}
	if one, _ := r.Read(ctx, "dep1", 1); len(one) != 1 || one[0] != e1 {
		t.Fatalf("Read(count=1) = %+v", one)
	}
	if empty, err := r.Read(ctx, "missing", 10); err != nil || len(empty) != 0 {
		t.Fatalf("missing stream: %v %v", empty, err)
	}

	select {
	case e := <-live:
		if e != e1 {
			t.Fatalf("live = %+v", e)
		}
	case <-ctx.Done():
		t.Fatal("no live entry")
	}

	streams, err := r.ActiveStreams(ctx)
	if err != nil || len(streams) != 1 || streams[0] != "dep1" {
		t.Fatalf("ActiveStreams = %v %v", streams, err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/logs/`
Expected: FAIL with `undefined: logs.FormatTime`.

- [ ] **Step 4: Implement**

`internal/logs/entry.go`:
```go
// Package logs stores container logs in two tiers: Redis (last 10 minutes,
// plus pub/sub for live viewers) and ClickHouse (queryable history, 30 days).
package logs

import "time"

// Entry is one log line. The JSON shape is what the UI and the Redis pub/sub
// channel have always used.
type Entry struct {
	DeploymentID string `json:"deploymentId"`
	Source       string `json:"source"` // stdout | stderr | system
	Message      string `json:"message"`
	Timestamp    string `json:"timestamp"`
}

// FormatTime renders t like JavaScript's Date.toISOString().
func FormatTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// ParseTime reads a FormatTime value, falling back to now for malformed input.
func ParseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Now().UTC()
	}
	return t
}
```

`internal/logs/redis.go`:
```go
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	streamPrefix  = "logs:stream:"
	channelPrefix = "logs:pubsub:"
	streamMaxLen  = 10000
	streamTTL     = 10 * time.Minute
)

type Redis struct{ c *redis.Client }

func NewRedis(url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	return &Redis{c: redis.NewClient(opt)}, nil
}

func (r *Redis) Close() error                   { return r.c.Close() }
func (r *Redis) Ping(ctx context.Context) error { return r.c.Ping(ctx).Err() }

// Write appends to the deployment's stream and publishes the entry to live viewers.
func (r *Redis) Write(ctx context.Context, e Entry) error {
	key := streamPrefix + e.DeploymentID
	err := r.c.XAdd(ctx, &redis.XAddArgs{
		Stream: key, MaxLen: streamMaxLen, Approx: true,
		Values: []any{"source", e.Source, "message", e.Message, "timestamp", e.Timestamp},
	}).Err()
	if err != nil {
		return err
	}
	if err := r.c.PExpire(ctx, key, streamTTL).Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return r.c.Publish(ctx, channelPrefix+e.DeploymentID, payload).Err()
}

// Read returns up to count entries from the start of the stream (oldest first).
func (r *Redis) Read(ctx context.Context, deploymentID string, count int64) ([]Entry, error) {
	msgs, err := r.c.XRangeN(ctx, streamPrefix+deploymentID, "-", "+", count).Result()
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, Entry{
			DeploymentID: deploymentID,
			Source:       fmt.Sprint(m.Values["source"]),
			Message:      fmt.Sprint(m.Values["message"]),
			Timestamp:    fmt.Sprint(m.Values["timestamp"]),
		})
	}
	return out, nil
}

// Subscribe streams live entries for a deployment. Call stop to unsubscribe;
// the channel closes after that or when ctx ends.
func (r *Redis) Subscribe(ctx context.Context, deploymentID string) (<-chan Entry, func(), error) {
	ps := r.c.Subscribe(ctx, channelPrefix+deploymentID)
	if _, err := ps.Receive(ctx); err != nil { // wait until the subscription is active
		_ = ps.Close()
		return nil, nil, err
	}
	out := make(chan Entry, 64)
	go func() {
		defer close(out)
		for msg := range ps.Channel() {
			var e Entry
			if json.Unmarshal([]byte(msg.Payload), &e) != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, func() { _ = ps.Close() }, nil
}

// ActiveStreams lists deployment IDs that still have a hot log stream.
func (r *Redis) ActiveStreams(ctx context.Context) ([]string, error) {
	var ids []string
	iter := r.c.Scan(ctx, 0, streamPrefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		ids = append(ids, strings.TrimPrefix(iter.Val(), streamPrefix))
	}
	return ids, iter.Err()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/logs/`
Expected: `ok  infrapilot/internal/logs`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/logs
git commit -m "feat(go): redis hot log tier with pub/sub"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 12: Stream IDs and the flush cursor

**Files:**
- Modify: `control-plane-go/internal/logs/redis.go` (append)
- Test: `control-plane-go/internal/logs/redis_cursor_test.go`

The Node flush job re-read each whole stream every 5 minutes and inserted the same lines into ClickHouse again. The fix: remember the last stream ID flushed for each deployment (`logs:flushed:<deploymentId>`) and only read entries after it.

- [ ] **Step 1: Write the failing test**

`internal/logs/redis_cursor_test.go`:
```go
package logs_test

import (
	"context"
	"testing"
	"time"

	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"infrapilot/internal/logs"
)

func TestReadAfterAndFlushCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctr, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	url, _ := ctr.ConnectionString(ctx)
	r, err := logs.NewRedis(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for _, msg := range []string{"a", "b", "c"} {
		if err := r.Write(ctx, logs.Entry{DeploymentID: "d1", Source: "stdout", Message: msg, Timestamp: "t"}); err != nil {
			t.Fatal(err)
		}
	}

	cursor, err := r.FlushCursor(ctx, "d1")
	if err != nil || cursor != "" {
		t.Fatalf("fresh cursor = %q err %v", cursor, err)
	}
	all, err := r.ReadAfter(ctx, "d1", "", 100)
	if err != nil || len(all) != 3 || all[0].Message != "a" || all[0].ID == "" {
		t.Fatalf("all = %+v err %v", all, err)
	}
	if err := r.SetFlushCursor(ctx, "d1", all[1].ID); err != nil {
		t.Fatal(err)
	}
	cursor, _ = r.FlushCursor(ctx, "d1")
	rest, _ := r.ReadAfter(ctx, "d1", cursor, 100)
	if len(rest) != 1 || rest[0].Message != "c" || rest[0].DeploymentID != "d1" {
		t.Fatalf("rest = %+v", rest)
	}
	if two, _ := r.ReadAfter(ctx, "d1", "", 2); len(two) != 2 {
		t.Fatalf("count limit ignored: %d", len(two))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/logs/ -run Cursor`
Expected: FAIL with `undefined: FlushCursor` (and `ReadAfter`).

- [ ] **Step 3: Implement**

Append to `internal/logs/redis.go`:
```go
const (
	cursorPrefix = "logs:flushed:"
	cursorTTL    = 15 * time.Minute // longer than streamTTL, refreshed on every flush
)

// StreamEntry is a log entry with its Redis stream ID.
type StreamEntry struct {
	ID string
	Entry
}

// ReadAfter returns up to count entries with IDs greater than afterID
// (all entries when afterID is empty), oldest first.
func (r *Redis) ReadAfter(ctx context.Context, deploymentID, afterID string, count int64) ([]StreamEntry, error) {
	start := "-"
	if afterID != "" {
		start = "(" + afterID // exclusive range, Redis 6.2+
	}
	msgs, err := r.c.XRangeN(ctx, streamPrefix+deploymentID, start, "+", count).Result()
	if err != nil {
		return nil, err
	}
	out := make([]StreamEntry, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, StreamEntry{ID: m.ID, Entry: Entry{
			DeploymentID: deploymentID,
			Source:       fmt.Sprint(m.Values["source"]),
			Message:      fmt.Sprint(m.Values["message"]),
			Timestamp:    fmt.Sprint(m.Values["timestamp"]),
		}})
	}
	return out, nil
}

// FlushCursor returns the last stream ID copied to ClickHouse, or "".
func (r *Redis) FlushCursor(ctx context.Context, deploymentID string) (string, error) {
	v, err := r.c.Get(ctx, cursorPrefix+deploymentID).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return v, err
}

func (r *Redis) SetFlushCursor(ctx context.Context, deploymentID, id string) error {
	return r.c.Set(ctx, cursorPrefix+deploymentID, id, cursorTTL).Err()
}
```
Add `"errors"` to the import block of `redis.go`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/logs/`
Expected: `ok  infrapilot/internal/logs`

- [ ] **Step 5: Commit**

```bash
git add internal/logs
git commit -m "feat(go): redis stream IDs and per-deployment flush cursor"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 13: ClickHouse warm tier

**Files:**
- Create: `control-plane-go/internal/logs/clickhouse.go`
- Test: `control-plane-go/internal/logs/clickhouse_test.go`

- [ ] **Step 1: Add dependencies**

```bash
go get github.com/ClickHouse/clickhouse-go/v2 github.com/testcontainers/testcontainers-go/modules/clickhouse
```

- [ ] **Step 2: Write the failing tests**

`internal/logs/clickhouse_test.go`:
```go
package logs

import (
	"context"
	"reflect"
	"testing"
	"time"

	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
)

func TestBuildQueryUsesBoundParameters(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sql, args := buildQuery(Query{
		DeploymentID: "d'1", Start: &start, Sources: []string{"stdout"}, Search: "err'or", Limit: 50, Offset: 10,
	})
	wantSQL := "SELECT deployment_id, source, message, timestamp FROM container_logs" +
		" WHERE deployment_id = ? AND timestamp >= ? AND has(?, source) AND message ILIKE ?" +
		" ORDER BY timestamp DESC LIMIT 50 OFFSET 10"
	if sql != wantSQL {
		t.Fatalf("sql:\n%s\nwant:\n%s", sql, wantSQL)
	}
	wantArgs := []any{"d'1", start, []string{"stdout"}, "%err'or%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v", args)
	}
}

func TestBuildQueryDefaultsLimit(t *testing.T) {
	sql, _ := buildQuery(Query{DeploymentID: "d"})
	if want := "SELECT deployment_id, source, message, timestamp FROM container_logs WHERE deployment_id = ? ORDER BY timestamp DESC LIMIT 100 OFFSET 0"; sql != want {
		t.Fatalf("sql = %s", sql)
	}
}

func TestClickHouseRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: needs Docker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := tcclickhouse.Run(ctx, "clickhouse/clickhouse-server:24.8-alpine",
		tcclickhouse.WithUsername("test"), tcclickhouse.WithPassword("test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })
	host, _ := ctr.Host(ctx)
	port, err := ctr.MappedPort(ctx, "8123/tcp")
	if err != nil {
		t.Fatal(err)
	}

	ch, err := NewClickHouse("http://"+host+":"+port.Port(), "test", "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Init(ctx); err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{DeploymentID: "d1", Source: "stdout", Message: "first", Timestamp: "2026-01-01T00:00:00.001Z"},
		{DeploymentID: "d1", Source: "stderr", Message: "it's an error", Timestamp: "2026-01-01T00:00:00.002Z"},
		{DeploymentID: "d2", Source: "stdout", Message: "other", Timestamp: "2026-01-01T00:00:00.003Z"},
	}
	if err := ch.Insert(ctx, entries); err != nil {
		t.Fatal(err)
	}

	got, err := ch.Query(ctx, Query{DeploymentID: "d1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != entries[0] || got[1] != entries[1] {
		t.Fatalf("Query = %+v", got)
	}
	errs, _ := ch.Query(ctx, Query{DeploymentID: "d1", Search: "it's", Sources: []string{"stderr"}})
	if len(errs) != 1 || errs[0].Message != "it's an error" {
		t.Fatalf("filtered = %+v", errs)
	}
	if n, err := ch.Count(ctx, "d1"); err != nil || n != 2 {
		t.Fatalf("Count = %d %v", n, err)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/logs/ -run 'BuildQuery|ClickHouse'`
Expected: FAIL with `undefined: buildQuery`.

- [ ] **Step 4: Implement**

`internal/logs/clickhouse.go`:
```go
package logs

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS container_logs (
  deployment_id String,
  source LowCardinality(String),
  message String,
  timestamp DateTime64(3),
  ingestion_time DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
ORDER BY (deployment_id, timestamp)
PARTITION BY toYYYYMMDD(timestamp)
TTL timestamp + INTERVAL 30 DAY
SETTINGS index_granularity = 8192`

// Query filters log lookups. Start/End are inclusive; Limit defaults to 100.
type Query struct {
	DeploymentID string
	Start, End   *time.Time
	Sources      []string
	Search       string
	Limit        int
	Offset       int
}

type ClickHouse struct{ conn driver.Conn }

// NewClickHouse connects over HTTP, e.g. rawURL "http://localhost:8123".
func NewClickHouse(rawURL, user, password string) (*ClickHouse, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse CLICKHOUSE_URL: %w", err)
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Protocol: clickhouse.HTTP,
		Addr:     []string{u.Host},
		Auth:     clickhouse.Auth{Database: "default", Username: user, Password: password},
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	return &ClickHouse{conn: conn}, nil
}

func (c *ClickHouse) Close() error { return c.conn.Close() }

// Init creates the logs table if it does not exist.
func (c *ClickHouse) Init(ctx context.Context) error { return c.conn.Exec(ctx, createTableSQL) }

func (c *ClickHouse) Insert(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, "INSERT INTO container_logs (deployment_id, source, message, timestamp)")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := batch.Append(e.DeploymentID, e.Source, e.Message, ParseTime(e.Timestamp)); err != nil {
			return err
		}
	}
	return batch.Send()
}

// Query returns matching entries oldest-first (the newest `Limit` of them).
func (c *ClickHouse) Query(ctx context.Context, q Query) ([]Entry, error) {
	sql, args := buildQuery(q)
	rows, err := c.conn.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts time.Time
		if err := rows.Scan(&e.DeploymentID, &e.Source, &e.Message, &ts); err != nil {
			return nil, err
		}
		e.Timestamp = FormatTime(ts)
		out = append(out, e)
	}
	slices.Reverse(out)
	return out, rows.Err()
}

func (c *ClickHouse) Count(ctx context.Context, deploymentID string) (int, error) {
	var n uint64
	err := c.conn.QueryRow(ctx, "SELECT count() FROM container_logs WHERE deployment_id = ?", deploymentID).Scan(&n)
	return int(n), err
}

func buildQuery(q Query) (string, []any) {
	where := []string{"deployment_id = ?"}
	args := []any{q.DeploymentID}
	if q.Start != nil {
		where = append(where, "timestamp >= ?")
		args = append(args, *q.Start)
	}
	if q.End != nil {
		where = append(where, "timestamp <= ?")
		args = append(args, *q.End)
	}
	if len(q.Sources) > 0 {
		where = append(where, "has(?, source)")
		args = append(args, q.Sources)
	}
	if q.Search != "" {
		where = append(where, "message ILIKE ?")
		args = append(args, "%"+q.Search+"%")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 100
	}
	sql := "SELECT deployment_id, source, message, timestamp FROM container_logs" +
		" WHERE " + strings.Join(where, " AND ") +
		" ORDER BY timestamp DESC LIMIT " + strconv.Itoa(limit) + " OFFSET " + strconv.Itoa(q.Offset)
	return sql, args
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/logs/`
Expected: `ok  infrapilot/internal/logs`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/logs
git commit -m "feat(go): clickhouse warm log tier with bound parameters"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 14: Tiered query and cursor-based flush (`Aggregator`)

**Files:**
- Create: `control-plane-go/internal/logs/aggregator.go`
- Test: `control-plane-go/internal/logs/aggregator_test.go`

Two log tiers: Redis (last 10 minutes) and ClickHouse (30 days). The S3 archive is cut (spec D8).

> **CTO amendment (T12 STAFF-1, STAFF-2/SEC-1, STAFF-3, Staff note; `review/decisions.md` 2026-10-09 "Task 14 flush durability"):**
> (a) **Cursor refresh.** In `Flush`, after `FlushCursor` returns `cursor != ""`, call `a.Hot.SetFlushCursor(ctx, id, cursor)` before reading. This refreshes the TTL even when the batch is empty or the insert fails. On error: `errs++; continue`. In `redis.go`, change the `cursorTTL` comment to "longer than streamTTL; Flush refreshes it on every pass while the stream exists".
> (b) **Drain loop.** Add the field `BatchSize int64` to `Aggregator`; 0 means `flushBatchSize` (10000). Per stream, loop: `ReadAfter(cursor, size)`. If the batch is empty, stop. Otherwise `Insert`, then `SetFlushCursor(lastID)`, then `cursor = lastID` and `processed += len`. Stop when `len(batch) < size`, on any error (`errs++`), on `ctx.Err() != nil`, or after `maxFlushBatches = 20` iterations for that stream. The leftover is picked up on the next run.
> (c) **Single flusher.** Doc comment on `Flush`: "Not safe for concurrent calls: the cursor does not coordinate runs. Exactly one caller, the worker's flush ticker (spec §6.7)."
> (d) **Write-time timestamp.** In `Redis.Write`, put `if e.Timestamp == "" { e.Timestamp = FormatTime(time.Now()) }` before XADD and PUBLISH.
> Tests: `TestFlushDrainsInBatches`: fakeHot has 5 entries and `BatchSize: 2`. Expect 3 `Insert` calls of sizes 2, 2, 1, `processed == 5`, `errs == 0`, and the cursor at the 5th ID. A second `Flush` inserts nothing. `TestFlushRefreshesCursorOnInsertFailure`: an existing cursor and a failing fake Warm. Expect `SetFlushCursor` to be called with the unchanged cursor and `errs == 1`. `TestFlushRefreshesCursorWhenIdle`: an existing cursor and no new entries. Expect the cursor to be re-set and no `Insert` call. Redis integration tests (also STAFF-3 T12): after `SetFlushCursor`, the key's `TTL` is >0 and ≤15m; `ReadAfter` on an expired or missing stream with a non-empty cursor returns `(empty, nil)`; `Write` with `Timestamp: ""` followed by two `Read`s returns the same non-empty timestamp both times, and a `Subscribe`r receives that same value.

- [ ] **Step 1: Write the failing tests**

`internal/logs/aggregator_test.go`:
```go
package logs

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type fakeHot struct {
	data    map[string][]Entry
	cursors map[string]string
}

func newFakeHot(data map[string][]Entry) *fakeHot {
	return &fakeHot{data: data, cursors: map[string]string{}}
}

func (f *fakeHot) Read(_ context.Context, id string, count int64) ([]Entry, error) {
	e := f.data[id]
	if int64(len(e)) > count {
		e = e[:count]
	}
	return e, nil
}

func (f *fakeHot) ActiveStreams(context.Context) ([]string, error) {
	ids := []string{}
	for id := range f.data {
		ids = append(ids, id)
	}
	return ids, nil
}

// Stream IDs are "1", "2", ... by position.
func (f *fakeHot) ReadAfter(_ context.Context, id, after string, count int64) ([]StreamEntry, error) {
	var out []StreamEntry
	for i, e := range f.data[id] {
		sid := fmt.Sprint(i + 1)
		if after != "" && i+1 <= mustAtoi(after) {
			continue
		}
		if int64(len(out)) == count {
			break
		}
		out = append(out, StreamEntry{ID: sid, Entry: e})
	}
	return out, nil
}

func (f *fakeHot) FlushCursor(_ context.Context, id string) (string, error) { return f.cursors[id], nil }
func (f *fakeHot) SetFlushCursor(_ context.Context, id, c string) error  { f.cursors[id] = c; return nil }

func mustAtoi(s string) int {
	var n int
	fmt.Sscan(s, &n)
	return n
}

type fakeWarm struct {
	rows     []Entry
	inserted []Entry
	lastQ    Query
}

func (f *fakeWarm) Insert(_ context.Context, e []Entry) error {
	f.inserted = append(f.inserted, e...)
	return nil
}
func (f *fakeWarm) Query(_ context.Context, q Query) ([]Entry, error) { f.lastQ = q; return f.rows, nil }
func (f *fakeWarm) Count(context.Context, string) (int, error)        { return len(f.rows) + 40, nil }

var now = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func entry(msg string) Entry {
	return Entry{DeploymentID: "d", Source: "stdout", Message: msg, Timestamp: "2026-01-01T11:59:00.000Z"}
}

func TestQueryServesRecentLogsFromRedis(t *testing.T) {
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{"d": {entry("a"), entry("b")}}), Warm: &fakeWarm{}, Now: func() time.Time { return now }}
	res, err := a.Query(context.Background(), Query{DeploymentID: "d", Limit: 100})
	if err != nil || res.Source != "redis" || len(res.Logs) != 2 || res.TotalCount != nil {
		t.Fatalf("got %+v err %v", res, err)
	}
}

func TestQueryFallsBackToClickHouseAndMerges(t *testing.T) {
	warm := &fakeWarm{rows: []Entry{entry("old"), entry("dup")}}
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{}), Warm: warm, Now: func() time.Time { return now }}
	res, err := a.Query(context.Background(), Query{DeploymentID: "d"})
	if err != nil || res.Source != "clickhouse" || len(res.Logs) != 2 || res.TotalCount == nil || *res.TotalCount != 42 {
		t.Fatalf("got %+v err %v", res, err)
	}
	if warm.lastQ.Limit != 100 {
		t.Fatalf("limit default not applied: %+v", warm.lastQ)
	}
}

func TestQueryOldRangeSkipsRedis(t *testing.T) {
	old := now.Add(-time.Hour)
	a := &Aggregator{Hot: newFakeHot(map[string][]Entry{"d": {entry("hot")}}), Warm: &fakeWarm{rows: []Entry{entry("warm")}}, Now: func() time.Time { return now }}
	res, _ := a.Query(context.Background(), Query{DeploymentID: "d", Start: &old})
	if res.Source != "clickhouse" || len(res.Logs) != 1 || res.Logs[0].Message != "warm" || res.TotalCount != nil {
		t.Fatalf("got %+v", res)
	}
}

func TestMergeDropsDuplicatesAndKeepsNewestLimit(t *testing.T) {
	merged := mergeTiers([]Entry{entry("1"), entry("2")}, []Entry{entry("2"), entry("3")}, 2)
	if len(merged) != 2 || merged[0].Message != "2" || merged[1].Message != "3" {
		t.Fatalf("got %+v", merged)
	}
}

func TestFlushOnlyCopiesNewEntries(t *testing.T) {
	hot := newFakeHot(map[string][]Entry{"d": {entry("1"), entry("2")}})
	warm := &fakeWarm{}
	a := &Aggregator{Hot: hot, Warm: warm, Now: time.Now}

	if processed, errs := a.Flush(context.Background()); processed != 2 || errs != 0 {
		t.Fatalf("first flush processed=%d errs=%d", processed, errs)
	}
	hot.data["d"] = append(hot.data["d"], entry("3"))
	if processed, _ := a.Flush(context.Background()); processed != 1 {
		t.Fatalf("second flush processed=%d, want 1", processed)
	}
	if processed, _ := a.Flush(context.Background()); processed != 0 {
		t.Fatalf("third flush processed=%d, want 0", processed)
	}
	if len(warm.inserted) != 3 || hot.cursors["d"] != "3" {
		t.Fatalf("inserted=%d cursor=%q", len(warm.inserted), hot.cursors["d"])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test -short ./internal/logs/ -run 'Query|Merge|Flush'`
Expected: FAIL with `undefined: Aggregator`.

- [ ] **Step 3: Implement**

`internal/logs/aggregator.go`:
```go
package logs

import (
	"context"
	"time"
)

type HotStore interface {
	Read(ctx context.Context, deploymentID string, count int64) ([]Entry, error)
	ActiveStreams(ctx context.Context) ([]string, error)
	ReadAfter(ctx context.Context, deploymentID, afterID string, count int64) ([]StreamEntry, error)
	FlushCursor(ctx context.Context, deploymentID string) (string, error)
	SetFlushCursor(ctx context.Context, deploymentID, id string) error
}

type WarmStore interface {
	Insert(ctx context.Context, entries []Entry) error
	Query(ctx context.Context, q Query) ([]Entry, error)
	Count(ctx context.Context, deploymentID string) (int, error)
}

// Aggregator reads from the right tier and moves logs from Redis to ClickHouse.
type Aggregator struct {
	Hot  HotStore
	Warm WarmStore
	Now  func() time.Time
}

type Result struct {
	Logs       []Entry
	Source     string // "redis" | "clickhouse"
	TotalCount *int
}

const (
	hotWindow      = 10 * time.Minute
	flushBatchSize = 10000
)

// Query reads from Redis when the range is recent and Redis has data,
// otherwise from ClickHouse, merging in Redis when the range reaches now.
func (a *Aggregator) Query(ctx context.Context, q Query) (Result, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	cutoff := a.Now().Add(-hotWindow)

	if q.Start == nil || q.Start.After(cutoff) {
		hot, err := a.Hot.Read(ctx, q.DeploymentID, int64(q.Limit))
		if err != nil {
			return Result{}, err
		}
		if len(hot) > 0 {
			return Result{Logs: hot, Source: "redis"}, nil
		}
	}

	warm, err := a.Warm.Query(ctx, q)
	if err != nil {
		return Result{}, err
	}
	if q.Start == nil || (q.End != nil && q.End.After(cutoff)) {
		hot, err := a.Hot.Read(ctx, q.DeploymentID, int64(q.Limit))
		if err != nil {
			return Result{}, err
		}
		total, err := a.Warm.Count(ctx, q.DeploymentID)
		if err != nil {
			return Result{}, err
		}
		return Result{Logs: mergeTiers(warm, hot, q.Limit), Source: "clickhouse", TotalCount: &total}, nil
	}
	return Result{Logs: warm, Source: "clickhouse"}, nil
}

// mergeTiers appends hot after warm, drops warm entries already in hot (same
// timestamp and message) and keeps the newest `limit`.
func mergeTiers(warm, hot []Entry, limit int) []Entry {
	seen := make(map[string]bool, len(hot))
	for _, h := range hot {
		seen[h.Timestamp+"-"+h.Message] = true
	}
	out := make([]Entry, 0, len(warm)+len(hot))
	for _, w := range warm {
		if !seen[w.Timestamp+"-"+w.Message] {
			out = append(out, w)
		}
	}
	out = append(out, hot...)
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func (a *Aggregator) ActiveStreamCount(ctx context.Context) (int, error) {
	ids, err := a.Hot.ActiveStreams(ctx)
	return len(ids), err
}

// Flush copies entries added since the last flush of each stream into
// ClickHouse, then advances that stream's cursor. It returns the number of
// entries copied and the number of streams that failed.
func (a *Aggregator) Flush(ctx context.Context) (processed, errs int) {
	ids, err := a.Hot.ActiveStreams(ctx)
	if err != nil {
		return 0, 1
	}
	for _, id := range ids {
		cursor, err := a.Hot.FlushCursor(ctx, id)
		if err != nil {
			errs++
			continue
		}
		batch, err := a.Hot.ReadAfter(ctx, id, cursor, flushBatchSize)
		if err != nil {
			errs++
			continue
		}
		if len(batch) == 0 {
			continue
		}
		entries := make([]Entry, len(batch))
		for i, se := range batch {
			entries[i] = se.Entry
		}
		if err := a.Warm.Insert(ctx, entries); err != nil {
			errs++
			continue
		}
		if err := a.Hot.SetFlushCursor(ctx, id, batch[len(batch)-1].ID); err != nil {
			errs++
			continue
		}
		processed += len(entries)
	}
	return processed, errs
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/logs/`
Expected: `ok  infrapilot/internal/logs`

- [ ] **Step 5: Commit**

```bash
git add internal/logs
git commit -m "feat(go): tiered log queries and duplicate-free flush"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 15: Logging, worker metrics and outbox counts

> **CTO amendment (Task 2 redaction note, `review/decisions.md` 2026-10-07):** Config is logged only as `"config", cfg` (`Config.LogValue` redacts); never log secret fields individually.

> **CTO amendment (STAFF-1 T15, `review/decisions.md` 2026-10-09):**
> - **Store.** Append to `internal/store/outbox.go`:
>   ```go
>   // OldestUnpublishedOutboxAge returns the age of the oldest pending or
>   // publishing outbox row, or 0 when there is none. createdAt is UTC (Prisma convention).
>   func (s *Store) OldestUnpublishedOutboxAge(ctx context.Context) (time.Duration, error) {
>   	var secs float64
>   	err := s.db.QueryRow(ctx, `SELECT COALESCE(EXTRACT(EPOCH FROM (now() AT TIME ZONE 'UTC') - min("createdAt")), 0)::float8
>   		FROM "OutboxEvent" WHERE "status" IN ('pending', 'publishing')`).Scan(&secs)
>   	return time.Duration(secs * float64(time.Second)), err
>   }
>   ```
> - **Metrics.** In `internal/metrics/metrics.go`, add `outboxLag prometheus.Gauge` named `cp_outbox_oldest_pending_age_seconds` (Help: "Age of the oldest pending or publishing outbox row; 0 when none."), register it, and add `func (m *Worker) SetOutboxLag(d time.Duration)` (nil-safe).
> - **Tests.** `TestOldestUnpublishedOutboxAge` (testcontainers): with no rows it returns 0. Insert a `pending` row with `createdAt = (now() AT TIME ZONE 'UTC') - interval '90 seconds'`, plus a `published` row and a `dead` row that are both older. The result must be between 89 s and 120 s, which shows `published` and `dead` rows are ignored. The metrics test calls `SetOutboxLag(42*time.Second)` and asserts that `cp_outbox_oldest_pending_age_seconds 42` is in the handler output; a nil `*Worker` must not panic.

**Files:**
- Create: `control-plane-go/internal/logging/logging.go`, `control-plane-go/internal/metrics/metrics.go`
- Modify: `control-plane-go/internal/store/outbox.go` (append `CountOutboxByStatus`)
- Test: `control-plane-go/internal/logging/logging_test.go`, `control-plane-go/internal/metrics/metrics_test.go`, `control-plane-go/internal/store/outbox_test.go` (append)

- [ ] **Step 1: Add the dependency**

Run: `go get github.com/prometheus/client_golang/prometheus github.com/prometheus/client_golang/prometheus/promhttp`

- [ ] **Step 2: Write the failing tests**

`internal/logging/logging_test.go`:
```go
package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	New("json", &buf).Info("hello", "deploymentId", "d1")
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil || m["msg"] != "hello" || m["deploymentId"] != "d1" {
		t.Fatalf("got %q err %v", buf.String(), err)
	}
}

func TestTextFormatIsDefault(t *testing.T) {
	var buf bytes.Buffer
	New("anything-else", &buf).Info("hello")
	if !strings.Contains(buf.String(), "msg=hello") {
		t.Fatalf("got %q", buf.String())
	}
}
```

`internal/metrics/metrics_test.go`:
```go
package metrics

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkerMetricsAreExposed(t *testing.T) {
	m := NewWorker()
	m.ObserveBuild(2 * time.Second)
	m.DeploymentFinished("deployed", 30*time.Second)
	m.SetOutbox(map[string]int{"pending": 3, "dead": 1})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`cp_deployments_total{outcome="deployed"} 1`,
		`cp_build_duration_seconds_count 1`,
		`cp_deploy_duration_seconds_count 1`,
		`cp_outbox_events{status="pending"} 3`,
		`cp_outbox_events{status="dead"} 1`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestNilWorkerIsSafe(t *testing.T) {
	var m *Worker
	m.ObserveBuild(time.Second)
	m.DeploymentFinished("failed", time.Second)
	m.SetOutbox(nil)
}
```

Append to `internal/store/outbox_test.go`:
```go
func TestCountOutboxByStatus(t *testing.T) {
	st, ctx := newStore(t), context.Background()
	a, _ := st.CreateOutboxEvent(ctx, "x", map[string]string{})
	st.CreateOutboxEvent(ctx, "x", map[string]string{})
	st.MarkOutboxDead(ctx, a, 10, "gone")

	counts, err := st.CountOutboxByStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if counts["pending"] != 1 || counts["dead"] != 1 || counts["published"] != 0 {
		t.Fatalf("counts = %v", counts)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/logging/ ./internal/metrics/ ./internal/store/ -run 'Format|Metrics|Nil|CountOutbox'`
Expected: FAIL with `undefined: New`, `undefined: NewWorker` and `undefined: CountOutboxByStatus`.

- [ ] **Step 4: Implement**

`internal/logging/logging.go`:
```go
// Package logging builds the process logger: JSON in production, text in dev.
package logging

import (
	"io"
	"log/slog"
)

func New(format string, w io.Writer) *slog.Logger {
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, nil))
	}
	return slog.New(slog.NewTextHandler(w, nil))
}
```

`internal/metrics/metrics.go`:
```go
// Package metrics exposes Prometheus metrics on a private address.
package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Worker holds the worker's metrics. A nil *Worker is valid and records nothing.
type Worker struct {
	reg         *prometheus.Registry
	deployments *prometheus.CounterVec
	build       prometheus.Histogram
	deploy      prometheus.Histogram
	outbox      *prometheus.GaugeVec
}

func NewWorker() *Worker {
	reg := prometheus.NewRegistry()
	m := &Worker{
		reg: reg,
		deployments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cp_deployments_total", Help: "Finished deployments by outcome (deployed|failed).",
		}, []string{"outcome"}),
		build: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "cp_build_duration_seconds", Help: "Clone + build + push time.",
			Buckets: prometheus.ExponentialBuckets(5, 2, 9), // 5s .. ~21m
		}),
		deploy: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "cp_deploy_duration_seconds", Help: "Time from deployment creation to running.",
			Buckets: prometheus.ExponentialBuckets(5, 2, 10),
		}),
		outbox: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cp_outbox_events", Help: "Outbox rows by status.",
		}, []string{"status"}),
	}
	reg.MustRegister(m.deployments, m.build, m.deploy, m.outbox,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Worker) ObserveBuild(d time.Duration) {
	if m != nil {
		m.build.Observe(d.Seconds())
	}
}

// DeploymentFinished counts an outcome; d is the total time since the
// deployment was created and is recorded only for successful deployments.
func (m *Worker) DeploymentFinished(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.deployments.WithLabelValues(outcome).Inc()
	if outcome == "deployed" {
		m.deploy.Observe(d.Seconds())
	}
}

func (m *Worker) SetOutbox(counts map[string]int) {
	if m == nil {
		return
	}
	for _, status := range []string{"pending", "publishing", "published", "dead"} {
		m.outbox.WithLabelValues(status).Set(float64(counts[status]))
	}
}

func (m *Worker) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Serve exposes /metrics on addr until ctx ends. addr should be a private
// address such as 127.0.0.1:9091.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("metrics listening", "addr", addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

Append to `internal/store/outbox.go`:
```go
// CountOutboxByStatus returns how many outbox rows are in each status.
func (s *Store) CountOutboxByStatus(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT "status"::text, count(*)::int FROM "OutboxEvent" GROUP BY "status"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/logging/ ./internal/metrics/ ./internal/store/`
Expected: three `ok` lines.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/logging internal/metrics internal/store
git commit -m "feat(go): structured logging, worker metrics and outbox counts"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

# Part 3 — Container runtime

### Task 16: Command runner and the Docker runtime

**Files:**
- Create: `control-plane-go/internal/runtime/exec.go`, `control-plane-go/internal/runtime/docker.go`
- Test: `control-plane-go/internal/runtime/exec_test.go`, `control-plane-go/internal/runtime/docker_test.go`

Every container gets:
- its project network, plus a network alias;
- labels;
- `--memory`, `--cpus`, `--pids-limit` and `--restart unless-stopped`.

App containers publish no ports; nginx reaches them over the project network. Env vars go through a `0600` temp file (`--env-file`), so secrets never show up in the host's process list.

- [ ] **Step 1: Write the failing tests**

`internal/runtime/exec_test.go`:
```go
package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecRunnerReturnsStdout(t *testing.T) {
	out, err := ExecRunner{}.Run(context.Background(), "", "sh", "-c", "echo hello")
	if err != nil || out != "hello\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

func TestExecRunnerErrorIncludesStderr(t *testing.T) {
	_, err := ExecRunner{}.Run(context.Background(), "", "sh", "-c", "echo boom >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecRunnerUsesDir(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	out, err := ExecRunner{}.Run(context.Background(), dir, "pwd")
	if err != nil || strings.TrimSpace(out) != dir {
		t.Fatalf("out=%q err=%v want %s", out, err, dir)
	}
}
```

`internal/runtime/docker_test.go`:
```go
package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

func newDocker(r Runner) Docker {
	return Docker{R: r, Limits: Limits{Memory: "512m", CPUs: "0.5", Pids: 256}, Nginx: "cp_nginx", ProbeImage: "busybox:1.36"}
}

func TestParseExposedPortsPicksLowest(t *testing.T) {
	p, err := parseExposedPorts(`{"8080/tcp":{},"3000/tcp":{},"9229/udp":{}}` + "\n")
	if err != nil || p != 3000 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	for _, raw := range []string{"", "null\n", "{}"} {
		if _, err := parseExposedPorts(raw); err == nil {
			t.Errorf("parseExposedPorts(%q) should fail", raw)
		}
	}
}

func TestRunBuildsIsolatedLimitedContainer(t *testing.T) {
	var envFile string
	var envContent string
	var envMode os.FileMode
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "rm" {
			return "", errors.New("Error: No such container: cp-d1")
		}
		for i, a := range c.Args {
			if a == "--env-file" {
				envFile = c.Args[i+1]
				b, _ := os.ReadFile(envFile)
				envContent = string(b)
				info, _ := os.Stat(envFile)
				envMode = info.Mode().Perm()
			}
		}
		return "abc123\n", nil
	}}
	id, err := newDocker(r).Run(context.Background(), RunSpec{
		Image: "redis:7-alpine", Name: "cp-d1", Network: "cp-proj-p1", Aliases: []string{"svc-s1"},
		Labels: map[string]string{"cp.serviceId": "s1", "cp.deploymentId": "d1"},
		Env:    map[string]string{"B": "2", "A": "1"},
		Cmd:    []string{"sh", "-c", "exec redis-server"},
		ContainerPort: 6379, PublishPort: 20000, Volume: "cp-vol-s1",
	})
	if err != nil || id != "abc123" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	cmds := r.Commands()
	want := "docker run -d --name cp-d1 --network cp-proj-p1 --network-alias svc-s1 " +
		"--memory 512m --cpus 0.5 --pids-limit 256 --restart unless-stopped " +
		"--label cp.deploymentId=d1 --label cp.serviceId=s1 " +
		"-p 127.0.0.1:20000:6379 -v cp-vol-s1:/data --env-file " + envFile + " " +
		"redis:7-alpine sh -c exec redis-server"
	if cmds[0] != "docker rm -f cp-d1" || cmds[1] != want {
		t.Fatalf("cmds:\n%s\nwant:\n%s", strings.Join(cmds, "\n"), want)
	}
	if envContent != "A=1\nB=2\n" || envMode != 0o600 {
		t.Fatalf("env file %q mode %v", envContent, envMode)
	}
	if _, err := os.Stat(envFile); !os.IsNotExist(err) {
		t.Fatal("env file must be deleted after docker run")
	}
}

func TestRunWithoutPortOrVolumeOrEnv(t *testing.T) {
	r := &testutil.FakeRunner{}
	_, _ = newDocker(r).Run(context.Background(), RunSpec{Image: "img", Name: "cp-d2", Network: "n", ContainerPort: 3000})
	got := r.Commands()[1]
	if strings.Contains(got, " -p ") || strings.Contains(got, " -v ") || strings.Contains(got, "--env-file") {
		t.Fatalf("unexpected flags: %s", got)
	}
}

func TestRunRejectsMultilineEnv(t *testing.T) {
	_, err := newDocker(&testutil.FakeRunner{}).Run(context.Background(), RunSpec{
		Image: "img", Name: "n", Network: "n", Env: map[string]string{"X": "a\nb"},
	})
	if err == nil {
		t.Fatal("newline in env value must be rejected")
	}
}

func TestRemoveIgnoresMissingContainer(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) {
		return "", errors.New("Error response from daemon: No such container: cp-x")
	}}
	if err := newDocker(r).Remove(context.Background(), "cp-x"); err != nil {
		t.Fatal(err)
	}
	failing := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("daemon down") }}
	if err := newDocker(failing).Remove(context.Background(), "cp-x"); err == nil {
		t.Fatal("other errors must be returned")
	}
}

func TestEnsureNetworkCreatesAndAttachesNginx(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch c.Args[1] {
		case "inspect":
			return "", errors.New("Error: No such network: cp-proj-p1")
		case "connect":
			return "", errors.New("endpoint with name cp_nginx already exists in network cp-proj-p1")
		}
		return "", nil
	}}
	if err := newDocker(r).EnsureNetwork(context.Background(), "cp-proj-p1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"docker network inspect cp-proj-p1",
		"docker network create --driver bridge --label cp.managed=true cp-proj-p1",
		"docker network connect cp-proj-p1 cp_nginx",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestVolumesProbeAndInspect(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Args[0] == "inspect" {
			return "true\n", nil
		}
		if c.Args[0] == "volume" && c.Args[1] == "inspect" {
			return "", errors.New("no such volume")
		}
		return "", nil
	}}
	d := newDocker(r)
	ctx := context.Background()
	if d.VolumeExists(ctx, "cp-vol-s1") {
		t.Fatal("volume should not exist")
	}
	_ = d.CreateVolume(ctx, "cp-vol-s1")
	_ = d.CopyIntoVolume(ctx, "/tmp/cp-volumes/s1", "cp-vol-s1")
	_ = d.Probe(ctx, "cp-proj-p1", "cp-d1", 3000)
	running, err := d.IsRunning(ctx, "abc")
	if err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
	want := []string{
		"docker volume inspect cp-vol-s1",
		"docker volume create --label cp.managed=true cp-vol-s1",
		"docker run --rm -v /tmp/cp-volumes/s1:/from:ro -v cp-vol-s1:/to busybox:1.36 sh -c [ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)",
		"docker run --rm --network cp-proj-p1 busybox:1.36 nc -z -w 2 cp-d1 3000",
		"docker inspect -f {{.State.Running}} abc",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestDetectContainerPortAndPull(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return `{"8080/tcp":{}}`, nil }}
	d := newDocker(r)
	if p, err := d.DetectContainerPort(context.Background(), "img"); err != nil || p != 8080 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	_ = d.Pull(context.Background(), "img")
	cmds := r.Commands()
	if cmds[0] != "docker image inspect img --format {{json .Config.ExposedPorts}}" || cmds[1] != "docker pull img" {
		t.Fatalf("cmds = %v", cmds)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/`
Expected: FAIL with `undefined: ExecRunner` and `undefined: Docker`.

- [ ] **Step 3: Implement the runner**

`internal/runtime/exec.go`:
```go
// Package runtime runs external commands (git, docker) and manages
// containers, networks, volumes, nginx routes and container log streaming.
package runtime

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a command and returns its stdout. Tests use testutil.FakeRunner.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// ExecRunner runs commands without a shell, inheriting the process environment.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s: %w: %s",
			name, firstArg(args), err, tail(strings.TrimSpace(stderr.String()), 2000))
	}
	return stdout.String(), nil
}

// firstArg names the subcommand in errors without echoing every argument
// (arguments can contain paths and other details we don't want in logs).
func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
```

- [ ] **Step 4: Implement the Docker runtime**

`internal/runtime/docker.go`:
```go
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
)

var errNoExposedPorts = errors.New("no_exposed_ports_found")

// Limits caps every container we start.
type Limits struct {
	Memory string // e.g. "512m"
	CPUs   string // e.g. "0.5"
	Pids   int
}

// Docker implements the container runtime with the docker CLI.
type Docker struct {
	R          Runner
	Limits     Limits
	Nginx      string // nginx container joined to every project network
	ProbeImage string // small image with sh, nc and cp (busybox)
}

type RunSpec struct {
	Image         string
	Name          string
	Network       string
	Aliases       []string
	Labels        map[string]string
	Env           map[string]string
	Cmd           []string // appended after the image
	ContainerPort int
	PublishPort   int    // 0: not published; otherwise bound to 127.0.0.1 only
	Volume        string // named volume mounted at /data
}

func (d Docker) Pull(ctx context.Context, image string) error {
	_, err := d.R.Run(ctx, "", "docker", "pull", image)
	return err
}

// DetectContainerPort returns the lowest port the image EXPOSEs.
func (d Docker) DetectContainerPort(ctx context.Context, image string) (int, error) {
	out, err := d.R.Run(ctx, "", "docker", "image", "inspect", image, "--format", "{{json .Config.ExposedPorts}}")
	if err != nil {
		return 0, err
	}
	return parseExposedPorts(out)
}

func parseExposedPorts(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return 0, errNoExposedPorts
	}
	var exposed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &exposed); err != nil {
		return 0, fmt.Errorf("parse exposed ports: %w", err)
	}
	var ports []int
	for k := range exposed {
		if p, err := strconv.Atoi(strings.SplitN(k, "/", 2)[0]); err == nil {
			ports = append(ports, p)
		}
	}
	if len(ports) == 0 {
		return 0, errNoExposedPorts
	}
	return slices.Min(ports), nil
}

// Run replaces any container with the same name and starts a new one
// detached. It returns the container ID.
func (d Docker) Run(ctx context.Context, s RunSpec) (string, error) {
	envFile, err := writeEnvFile(s.Env)
	if err != nil {
		return "", err
	}
	if envFile != "" {
		defer os.Remove(envFile)
	}
	if err := d.Remove(ctx, s.Name); err != nil {
		return "", err
	}
	out, err := d.R.Run(ctx, "", "docker", d.runArgs(s, envFile)...)
	return strings.TrimSpace(out), err
}

func (d Docker) runArgs(s RunSpec, envFile string) []string {
	args := []string{"run", "-d", "--name", s.Name, "--network", s.Network}
	for _, a := range s.Aliases {
		args = append(args, "--network-alias", a)
	}
	args = append(args,
		"--memory", d.Limits.Memory, "--cpus", d.Limits.CPUs,
		"--pids-limit", strconv.Itoa(d.Limits.Pids), "--restart", "unless-stopped")
	for _, k := range sortedKeys(s.Labels) {
		args = append(args, "--label", k+"="+s.Labels[k])
	}
	if s.PublishPort > 0 {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:%d", s.PublishPort, s.ContainerPort))
	}
	if s.Volume != "" {
		args = append(args, "-v", s.Volume+":/data")
	}
	if envFile != "" {
		args = append(args, "--env-file", envFile)
	}
	args = append(args, s.Image)
	return append(args, s.Cmd...)
}

// writeEnvFile writes KEY=VALUE lines to a 0600 temp file, so values never
// appear in the docker command line. It returns "" when env is empty.
func writeEnvFile(env map[string]string) (string, error) {
	if len(env) == 0 {
		return "", nil
	}
	var b strings.Builder
	for _, k := range sortedKeys(env) {
		v := env[k]
		if strings.ContainsAny(k, "=\n") || strings.Contains(v, "\n") {
			return "", fmt.Errorf("env var %q: names cannot contain '=' or newlines, values cannot contain newlines", k)
		}
		b.WriteString(k + "=" + v + "\n")
	}
	f, err := os.CreateTemp("", "cp-env-*")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Remove force-removes a container; a missing container is not an error.
func (d Docker) Remove(ctx context.Context, name string) error {
	_, err := d.R.Run(ctx, "", "docker", "rm", "-f", name)
	if err != nil && !strings.Contains(err.Error(), "No such container") {
		return err
	}
	return nil
}

// EnsureNetwork creates the network if needed and attaches nginx to it.
func (d Docker) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := d.R.Run(ctx, "", "docker", "network", "inspect", name); err != nil {
		_, err := d.R.Run(ctx, "", "docker", "network", "create", "--driver", "bridge", "--label", "cp.managed=true", name)
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	if d.Nginx == "" {
		return nil
	}
	_, err := d.R.Run(ctx, "", "docker", "network", "connect", name, d.Nginx)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

func (d Docker) VolumeExists(ctx context.Context, name string) bool {
	_, err := d.R.Run(ctx, "", "docker", "volume", "inspect", name)
	return err == nil
}

func (d Docker) CreateVolume(ctx context.Context, name string) error {
	_, err := d.R.Run(ctx, "", "docker", "volume", "create", "--label", "cp.managed=true", name)
	return err
}

// CopyIntoVolume copies hostDir into the volume once. A marker file makes
// repeated calls (activity retries, later deploys) no-ops.
func (d Docker) CopyIntoVolume(ctx context.Context, hostDir, volume string) error {
	_, err := d.R.Run(ctx, "", "docker", "run", "--rm",
		"-v", hostDir+":/from:ro", "-v", volume+":/to", d.ProbeImage,
		"sh", "-c", "[ -f /to/.cp-migrated ] || (cp -a /from/. /to/ && touch /to/.cp-migrated)")
	return err
}

// Probe checks that host:port accepts TCP connections from inside network.
func (d Docker) Probe(ctx context.Context, network, host string, port int) error {
	_, err := d.R.Run(ctx, "", "docker", "run", "--rm", "--network", network, d.ProbeImage,
		"nc", "-z", "-w", "2", host, strconv.Itoa(port))
	return err
}

// IsRunning reports whether the container exists and is running.
func (d Docker) IsRunning(ctx context.Context, id string) (bool, error) {
	out, err := d.R.Run(ctx, "", "docker", "inspect", "-f", "{{.State.Running}}", id)
	if err != nil {
		if strings.Contains(err.Error(), "No such object") {
			return false, nil
		}
		return false, err
	}
	return strings.TrimSpace(out) == "true", nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/runtime/`
Expected: `ok  infrapilot/internal/runtime`

- [ ] **Step 6: Try it against real Docker**

```bash
docker network create --label cp.managed=true cp-proj-plantest
docker run -d --name cp-plantest --network cp-proj-plantest --network-alias svc-plantest \
  --memory 512m --cpus 0.5 --pids-limit 256 nginx:alpine
docker run --rm --network cp-proj-plantest busybox:1.36 nc -z -w 2 cp-plantest 80 && echo PROBE_OK
docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}}' cp-plantest
docker rm -f cp-plantest && docker network rm cp-proj-plantest
```
Expected: `PROBE_OK`, then `536870912 500000000 256`. This confirms that the flags the runtime emits work on this Docker version.

- [ ] **Step 7: Commit**

```bash
git add internal/runtime
git commit -m "feat(go): docker runtime with project networks, limits, volumes and probes"
```

- [ ] **Step 8: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 17: Host port allocation

**Files:**
- Create: `control-plane-go/internal/runtime/ports.go`
- Test: `control-plane-go/internal/runtime/ports_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/runtime/ports_test.go`:
```go
package runtime

import (
	"context"
	"errors"
	"testing"

	"infrapilot/internal/testutil"
)

func TestParseDockerPorts(t *testing.T) {
	out := "0.0.0.0:20000->27017/tcp, :::20000->27017/tcp\n\n0.0.0.0:20003->3000/tcp\n"
	got := parseDockerPorts(out)
	if len(got) != 2 || !got[20000] || !got[20003] {
		t.Fatalf("got %v", got)
	}
}

func TestPickFreePort(t *testing.T) {
	p, err := PickFreePort(20000, 20002, map[int]bool{20000: true, 20001: true})
	if err != nil || p != 20002 {
		t.Fatalf("p=%d err=%v", p, err)
	}
	if _, err := PickFreePort(1, 1, map[int]bool{1: true}); err == nil || err.Error() != "no_free_ports_in_range" {
		t.Fatalf("err = %v", err)
	}
}

func TestUsedPortsIgnoresDockerErrors(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(testutil.Call) (string, error) { return "", errors.New("docker down") }}
	if got := (Docker{R: r}).UsedPorts(context.Background()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if cmds := r.Commands(); cmds[0] != "docker ps --format {{.Ports}}" {
		t.Fatalf("cmds = %v", cmds)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run 'Port'`
Expected: FAIL with `undefined: parseDockerPorts`.

- [ ] **Step 3: Implement**

`internal/runtime/ports.go`:
```go
package runtime

import (
	"context"
	"errors"
	"regexp"
	"strconv"
)

var hostPortRe = regexp.MustCompile(`:(\d+)->`)

// UsedPorts returns host ports published by running containers. Errors are
// treated as "none in use", as in the Node worker.
func (d Docker) UsedPorts(ctx context.Context) map[int]bool {
	out, err := d.R.Run(ctx, "", "docker", "ps", "--format", "{{.Ports}}")
	if err != nil {
		return map[int]bool{}
	}
	return parseDockerPorts(out)
}

func parseDockerPorts(out string) map[int]bool {
	used := map[int]bool{}
	for _, m := range hostPortRe.FindAllStringSubmatch(out, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil {
			used[p] = true
		}
	}
	return used
}

// PickFreePort returns the first port in [start, end] not in used.
func PickFreePort(start, end int, used map[int]bool) (int, error) {
	for p := start; p <= end; p++ {
		if !used[p] {
			return p, nil
		}
	}
	return 0, errors.New("no_free_ports_in_range")
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/runtime/`
Expected: `ok  infrapilot/internal/runtime`

- [ ] **Step 5: Commit**

```bash
git add internal/runtime
git commit -m "feat(go): host port allocation"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 18: Nginx routes

> **CTO amendment (SEC-1, `review/decisions.md` 2026-10-07):** add `// #nosec G301 -- <reason>` / `// #nosec G306 -- <reason>` to the `MkdirAll` 0o755 and `WriteFile` 0o644 calls, or tighten them to 0o750/0o640 if the Verifier confirms nginx can still read the files.

**Files:**
- Create: `control-plane-go/internal/runtime/nginx.go`
- Test: `control-plane-go/internal/runtime/nginx_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/runtime/nginx_test.go`:
```go
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"infrapilot/internal/testutil"
)

const wantRoute = `# auto-generated – service svc-1
server {
    listen 80;
    server_name svc-1.localhost;

    resolver 127.0.0.11 valid=10s;

    location / {
        set $upstream_svc_1 http://cp-dep1:3000;
        proxy_pass $upstream_svc_1;

        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
`

func TestRenderRouteMatchesNodeOutput(t *testing.T) {
	if got := RenderRoute("svc-1", "localhost", "cp-dep1", 3000); got != wantRoute {
		t.Fatalf("got:\n%s", got)
	}
}

func TestUpsertRouteWritesFileAndReloads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "routes")
	r := &testutil.FakeRunner{}
	n := Nginx{R: r, RoutesDir: dir, Container: "cp_nginx", BaseDomain: "localhost", Port: "80"}

	if err := n.UpsertRoute(context.Background(), "svc-1", "cp-dep1", 3000); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "svc-svc-1.conf"))
	if err != nil || string(b) != wantRoute {
		t.Fatalf("file = %q err=%v", b, err)
	}
	if cmds := r.Commands(); len(cmds) != 1 || cmds[0] != "docker exec cp_nginx nginx -s reload" {
		t.Fatalf("cmds = %v", cmds)
	}
}

func TestRuntimeURL(t *testing.T) {
	if got := (Nginx{BaseDomain: "localhost", Port: "80"}).RuntimeURL("abc"); got != "http://abc.localhost" {
		t.Fatalf("got %s", got)
	}
	if got := (Nginx{BaseDomain: "example.com", Port: "8088"}).RuntimeURL("abc"); got != "http://abc.example.com:8088" {
		t.Fatalf("got %s", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run 'Route|RuntimeURL'`
Expected: FAIL with `undefined: RenderRoute`.

- [ ] **Step 3: Implement**

`internal/runtime/nginx.go`:
```go
package runtime

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Nginx writes one server block per service into RoutesDir (mounted into the
// nginx container) and reloads nginx.
type Nginx struct {
	R          Runner
	RoutesDir  string
	Container  string
	BaseDomain string
	Port       string // public nginx port; "80" adds no :port suffix to URLs
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// RenderRoute returns the server block for <serviceID>.<baseDomain>. The
// upstream is held in a variable so nginx resolves the container name through
// Docker DNS at request time, not only at reload time.
func RenderRoute(serviceID, baseDomain, containerName string, containerPort int) string {
	v := "$upstream_" + nonAlnum.ReplaceAllString(serviceID, "_")
	return strings.Join([]string{
		"# auto-generated – service " + serviceID,
		"server {",
		"    listen 80;",
		"    server_name " + serviceID + "." + baseDomain + ";",
		"",
		"    resolver 127.0.0.11 valid=10s;",
		"",
		"    location / {",
		"        set " + v + " http://" + containerName + ":" + strconv.Itoa(containerPort) + ";",
		"        proxy_pass " + v + ";",
		"",
		"        proxy_http_version 1.1;",
		"        proxy_set_header Host $host;",
		"        proxy_set_header X-Real-IP $remote_addr;",
		"        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;",
		"        proxy_set_header X-Forwarded-Proto $scheme;",
		"        proxy_set_header Upgrade $http_upgrade;",
		`        proxy_set_header Connection "upgrade";`,
		"    }",
		"}",
		"",
	}, "\n")
}

func (n Nginx) UpsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error {
	if err := os.MkdirAll(n.RoutesDir, 0o755); err != nil {
		return err
	}
	conf := RenderRoute(serviceID, n.BaseDomain, containerName, containerPort)
	if err := os.WriteFile(filepath.Join(n.RoutesDir, "svc-"+serviceID+".conf"), []byte(conf), 0o644); err != nil {
		return err
	}
	_, err := n.R.Run(ctx, "", "docker", "exec", n.Container, "nginx", "-s", "reload")
	return err
}

func (n Nginx) RuntimeURL(serviceID string) string {
	suffix := ""
	if n.Port != "80" {
		suffix = ":" + n.Port
	}
	return "http://" + serviceID + "." + n.BaseDomain + suffix
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/runtime/`
Expected: `ok  infrapilot/internal/runtime`

- [ ] **Step 5: Commit**

```bash
git add internal/runtime
git commit -m "feat(go): nginx route files and reload"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 19: Container log streaming

**Files:**
- Create: `control-plane-go/internal/runtime/logstream.go`
- Test: `control-plane-go/internal/runtime/logstream_test.go`

- [ ] **Step 1: Write the failing tests**

`internal/runtime/logstream_test.go`:
```go
package runtime

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"infrapilot/internal/logs"
)

type memorySink struct {
	mu      sync.Mutex
	entries []logs.Entry
}

func (m *memorySink) Write(_ context.Context, e logs.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func (m *memorySink) all() []logs.Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]logs.Entry(nil), m.entries...)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStreamerForwardsStdoutStderrAndExit(t *testing.T) {
	sink := &memorySink{}
	s := NewStreamer(context.Background(), sink, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo out1; echo err1 1>&2")
	}

	s.Start("dep1", "ctr1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })

	got := sink.all()
	has := func(source, msg string) bool {
		for _, e := range got {
			if e.DeploymentID == "dep1" && e.Source == source && e.Message == msg && e.Timestamp != "" {
				return true
			}
		}
		return false
	}
	if !has("stdout", "out1") || !has("stderr", "err1") {
		t.Fatalf("entries = %+v", got)
	}
	if last := got[len(got)-1]; last.Source != "system" || last.Message != "Log stream ended (exit code: 0)" {
		t.Fatalf("last = %+v", last)
	}
}

func TestStreamerStartsOncePerDeploymentAndStops(t *testing.T) {
	var started atomic.Int32
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, _ string) *exec.Cmd {
		started.Add(1)
		return exec.CommandContext(ctx, "sleep", "30")
	}

	s.Start("dep1", "ctr1")
	s.Start("dep1", "ctr1")
	if started.Load() != 1 || !s.IsStreaming("dep1") {
		t.Fatalf("started=%d streaming=%v", started.Load(), s.IsStreaming("dep1"))
	}
	s.Stop("dep1")
	waitFor(t, func() bool { return !s.IsStreaming("dep1") })
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/runtime/ -run Streamer`
Expected: FAIL with `undefined: NewStreamer`.

- [ ] **Step 3: Implement**

`internal/runtime/logstream.go`:
```go
package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"infrapilot/internal/logs"
)

// LogSink receives log lines; *logs.Redis implements it.
type LogSink interface {
	Write(ctx context.Context, e logs.Entry) error
}

// Streamer follows `docker logs -f` for deployed containers and forwards each
// line to the sink. Streams live until the container exits, Stop is called or
// the root context ends.
type Streamer struct {
	ctx    context.Context
	sink   LogSink
	log    *slog.Logger
	now    func() time.Time
	newCmd func(ctx context.Context, containerID string) *exec.Cmd

	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewStreamer(ctx context.Context, sink LogSink, log *slog.Logger) *Streamer {
	return &Streamer{
		ctx: ctx, sink: sink, log: log, now: time.Now,
		newCmd: func(ctx context.Context, containerID string) *exec.Cmd {
			return exec.CommandContext(ctx, "docker", "logs", "-f", "--tail=100", containerID)
		},
		active: map[string]context.CancelFunc{},
	}
}

// Start begins streaming unless this deployment is already streaming.
func (s *Streamer) Start(deploymentID, containerID string) {
	s.mu.Lock()
	if _, ok := s.active[deploymentID]; ok {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.active[deploymentID] = cancel
	s.mu.Unlock()

	s.log.Info("log stream starting", "deploymentId", deploymentID, "containerId", containerID)
	go s.stream(ctx, deploymentID, s.newCmd(ctx, containerID))
}

func (s *Streamer) Stop(deploymentID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.active[deploymentID]; ok {
		cancel()
		delete(s.active, deploymentID)
	}
}

func (s *Streamer) IsStreaming(deploymentID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[deploymentID]
	return ok
}

func (s *Streamer) stream(ctx context.Context, deploymentID string, cmd *exec.Cmd) {
	defer s.Stop(deploymentID)
	emitCtx := context.WithoutCancel(ctx)

	stdout, err := cmd.StdoutPipe()
	if err == nil {
		var stderr io.ReadCloser
		if stderr, err = cmd.StderrPipe(); err == nil {
			if err = cmd.Start(); err == nil {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); s.pump(emitCtx, deploymentID, "stdout", stdout) }()
				go func() { defer wg.Done(); s.pump(emitCtx, deploymentID, "stderr", stderr) }()
				wg.Wait()
				_ = cmd.Wait()
				code := cmd.ProcessState.ExitCode()
				s.emit(emitCtx, deploymentID, "system", fmt.Sprintf("Log stream ended (exit code: %d)", code))
				return
			}
		}
	}
	s.emit(emitCtx, deploymentID, "system", "Log stream error: "+err.Error())
}

func (s *Streamer) pump(ctx context.Context, deploymentID, source string, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Text(); line != "" {
			s.emit(ctx, deploymentID, source, line)
		}
	}
}

func (s *Streamer) emit(ctx context.Context, deploymentID, source, msg string) {
	e := logs.Entry{DeploymentID: deploymentID, Source: source, Message: msg, Timestamp: logs.FormatTime(s.now())}
	if err := s.sink.Write(ctx, e); err != nil {
		s.log.Warn("log write failed", "deploymentId", deploymentID, "err", err)
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/runtime/`
Expected: `ok  infrapilot/internal/runtime`

- [ ] **Step 5: Commit**

```bash
git add internal/runtime
git commit -m "feat(go): stream container logs into redis"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 20: Resume log streams after a worker restart

**Files:**
- Create: `control-plane-go/internal/runtime/resume.go`
- Test: `control-plane-go/internal/runtime/resume_test.go`

- [ ] **Step 1: Write the failing test**

`internal/runtime/resume_test.go`:
```go
package runtime

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
)

func TestResumeStartsOnlyRunningContainers(t *testing.T) {
	var mu sync.Mutex
	var started []string
	s := NewStreamer(context.Background(), &memorySink{}, slog.New(slog.DiscardHandler))
	s.newCmd = func(ctx context.Context, containerID string) *exec.Cmd {
		mu.Lock()
		started = append(started, containerID)
		mu.Unlock()
		return exec.CommandContext(ctx, "sleep", "30")
	}
	defer s.Stop("d1")

	running := map[string]bool{"c1": true, "c2": false}
	isRunning := func(_ context.Context, id string) (bool, error) {
		if id == "c3" {
			return false, errors.New("docker down")
		}
		return running[id], nil
	}
	n := s.Resume(context.Background(), []Running{
		{DeploymentID: "d1", ContainerID: "c1"},
		{DeploymentID: "d2", ContainerID: "c2"},
		{DeploymentID: "d3", ContainerID: "c3"},
	}, isRunning)

	if n != 1 || !s.IsStreaming("d1") || s.IsStreaming("d2") || s.IsStreaming("d3") {
		t.Fatalf("n=%d d1=%v d2=%v d3=%v", n, s.IsStreaming("d1"), s.IsStreaming("d2"), s.IsStreaming("d3"))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(started) != 1 || started[0] != "c1" {
		t.Fatalf("started = %v", started)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runtime/ -run Resume`
Expected: FAIL with `undefined: Running`.

- [ ] **Step 3: Implement**

`internal/runtime/resume.go`:
```go
package runtime

import "context"

// Running identifies a deployment whose container logs should be streamed.
type Running struct {
	DeploymentID string
	ContainerID  string
}

// Resume re-attaches log streams for deployments whose containers are still
// running (streams live in worker memory and are lost on restart). It
// returns how many streams were started. Check errors are logged and skipped.
func (s *Streamer) Resume(ctx context.Context, deps []Running, isRunning func(context.Context, string) (bool, error)) int {
	n := 0
	for _, d := range deps {
		ok, err := isRunning(ctx, d.ContainerID)
		if err != nil {
			s.log.Warn("resume: cannot inspect container", "deploymentId", d.DeploymentID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		s.Start(d.DeploymentID, d.ContainerID)
		n++
	}
	return n
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/runtime/`
Expected: `ok  infrapilot/internal/runtime`

- [ ] **Step 5: Commit**

```bash
git add internal/runtime
git commit -m "feat(go): resume log streams for running containers on worker start"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

# Part 4 — Build, workflows and the worker

### Task 21: Dockerfile generation

> **CTO amendment (SEC-1, `review/decisions.md` 2026-10-07):** `ReadPackageJSON` and `ReactOutputDir` open files only through `os.OpenRoot(repo)` and treat any error, including a symlink escape, as "file missing". Add a test: a `package.json` symlink pointing outside the repo yields `PackageJSON{}`.

**Files:**
- Create: `control-plane-go/internal/worker/build/dockerfile.go`
- Test: `control-plane-go/internal/worker/build/dockerfile_test.go`

- [ ] **Step 1: Write the failing tests**

The expected strings are byte-for-byte what `control-plane/apps/worker/src/build/dockerfile-gen.ts` produces.

`internal/worker/build/dockerfile_test.go`:
```go
package build

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNodejsDockerfileDefaults(t *testing.T) {
	want := `FROM node:20-alpine

WORKDIR /app

# Install dependencies first (better caching)
COPY package*.json ./

RUN npm ci --only=production

# Copy source code
COPY . .

# Expose port
EXPOSE 3000

# Start application
CMD ["sh", "-c", "node index.js"]`
	if got := NodejsDockerfile(PackageJSON{}); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestNodejsDockerfileWithBuildAndNodemon(t *testing.T) {
	pkg := PackageJSON{Scripts: map[string]string{"start": "nodemon server.js", "build": "tsc"}}
	pkg.Engines.Node = ">=18"
	want := `FROM node:18-alpine

WORKDIR /app

# Install dependencies first (better caching)
COPY package*.json ./

RUN npm ci --only=production

# Copy source code
COPY . .

# Build application
RUN npm run build

# Expose port
EXPOSE 3000

# Start application
CMD ["sh", "-c", "node server.js"]`
	if got := NodejsDockerfile(pkg); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestStartCommandPriority(t *testing.T) {
	cases := []struct {
		scripts map[string]string
		want    string
	}{
		{map[string]string{"start": "node a.js", "serve": "x"}, `CMD ["sh", "-c", "npm start"]`},
		{map[string]string{"start:prod": "x", "serve": "y"}, `CMD ["sh", "-c", "npm run start:prod"]`},
		{map[string]string{"serve": "y"}, `CMD ["sh", "-c", "npm run serve"]`},
	}
	for _, c := range cases {
		got := NodejsDockerfile(PackageJSON{Scripts: c.scripts})
		if last := got[len(got)-len(c.want):]; last != c.want {
			t.Errorf("scripts %v: last line %q, want %q", c.scripts, last, c.want)
		}
	}
}

func TestBaseImage(t *testing.T) {
	cases := map[string]string{"": "node:20-alpine", "20-alpine": "node:20-alpine", "^20.11.0": "node:20.11.0-alpine", ">=18": "node:18-alpine"}
	for in, want := range cases {
		pkg := PackageJSON{}
		pkg.Engines.Node = in
		if got := baseImage(pkg); got != want {
			t.Errorf("baseImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNextjsDockerfile(t *testing.T) {
	want := `FROM node:20-alpine

WORKDIR /app

# Install dependencies
COPY package*.json ./
RUN npm ci

# Copy source code
COPY . .

# Build Next.js app
RUN npm run build

# Expose port (Next.js default is 3000)
EXPOSE 3000

# Start Next.js in production mode
CMD ["npm", "start"]
`
	if got := NextjsDockerfile(PackageJSON{}); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestReactDockerfile(t *testing.T) {
	want := `# Build stage
FROM node:20-alpine AS builder

WORKDIR /app

# Install dependencies
COPY package*.json ./
RUN npm ci

# Copy source and build
COPY . .
RUN npm run build

# Production stage with nginx
FROM nginx:alpine

# Copy built files from builder stage
COPY --from=builder /app/dist /usr/share/nginx/html

# Create nginx config for SPA routing
RUN echo 'server { listen 80; location / { root /usr/share/nginx/html; try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]
`
	if got := ReactDockerfile(PackageJSON{}, "dist"); got != want {
		t.Fatalf("got:\n%s", got)
	}
}

func TestReactOutputDir(t *testing.T) {
	dir := t.TempDir()
	if got := ReactOutputDir(dir); got != "build" {
		t.Fatalf("no vite config: %s", got)
	}
	os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(""), 0o644)
	if got := ReactOutputDir(dir); got != "dist" {
		t.Fatalf("vite.config.ts: %s", got)
	}
}

func TestReadPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if pkg := ReadPackageJSON(dir); pkg.Scripts != nil {
		t.Fatal("missing file should give empty package")
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"start":"node x"},"engines":{"node":"22"}}`), 0o644)
	pkg := ReadPackageJSON(dir)
	if pkg.Scripts["start"] != "node x" || pkg.Engines.Node != "22" {
		t.Fatalf("got %+v", pkg)
	}
	os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{broken`), 0o644)
	if pkg := ReadPackageJSON(dir); pkg.Scripts != nil {
		t.Fatal("malformed file should give empty package")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/worker/build/`
Expected: FAIL with `undefined: NodejsDockerfile`.

- [ ] **Step 3: Implement**

`internal/worker/build/dockerfile.go`:
```go
// Package build clones repositories, builds and pushes images, and consumes
// build.requested messages.
package build

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type PackageJSON struct {
	Scripts map[string]string `json:"scripts"`
	Engines struct {
		Node string `json:"node"`
	} `json:"engines"`
}

// ReadPackageJSON returns an empty PackageJSON when the file is missing or malformed.
func ReadPackageJSON(dir string) PackageJSON {
	var pkg PackageJSON
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return PackageJSON{}
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return PackageJSON{}
	}
	return pkg
}

var nonVersionChars = regexp.MustCompile(`[^0-9a-zA-Z.-]`)

func baseImage(pkg PackageJSON) string {
	v := pkg.Engines.Node
	if v == "" {
		v = "20-alpine"
	}
	clean := nonVersionChars.ReplaceAllString(v, "")
	if strings.Contains(clean, "alpine") {
		return "node:" + clean
	}
	return "node:" + clean + "-alpine"
}

func NodejsDockerfile(pkg PackageJSON) string {
	scripts := pkg.Scripts
	start := "node index.js"
	switch {
	case scripts["start"] != "":
		start = "npm start"
	case scripts["start:prod"] != "":
		start = "npm run start:prod"
	case scripts["serve"] != "":
		start = "npm run serve"
	}
	// nodemon is a devDependency and is not installed in the production image.
	if strings.Contains(scripts["start"], "nodemon") {
		start = strings.Replace(scripts["start"], "nodemon", "node", 1)
	}

	lines := []string{
		"FROM " + baseImage(pkg), "",
		"WORKDIR /app", "",
		"# Install dependencies first (better caching)",
		"COPY package*.json ./", "",
		"RUN npm ci --only=production", "",
		"# Copy source code",
		"COPY . .",
	}
	if scripts["build"] != "" {
		lines = append(lines, "", "# Build application", "RUN npm run build")
	}
	lines = append(lines, "",
		"# Expose port", "EXPOSE 3000", "",
		"# Start application", `CMD ["sh", "-c", "`+start+`"]`)
	return strings.Join(lines, "\n")
}

func NextjsDockerfile(pkg PackageJSON) string {
	return strings.Join([]string{
		"FROM " + baseImage(pkg), "",
		"WORKDIR /app", "",
		"# Install dependencies",
		"COPY package*.json ./",
		"RUN npm ci", "",
		"# Copy source code",
		"COPY . .", "",
		"# Build Next.js app",
		"RUN npm run build", "",
		"# Expose port (Next.js default is 3000)",
		"EXPOSE 3000", "",
		"# Start Next.js in production mode",
		`CMD ["npm", "start"]`,
		"",
	}, "\n")
}

// ReactDockerfile builds static files, then serves outDir with nginx.
func ReactDockerfile(pkg PackageJSON, outDir string) string {
	return strings.Join([]string{
		"# Build stage",
		"FROM " + baseImage(pkg) + " AS builder", "",
		"WORKDIR /app", "",
		"# Install dependencies",
		"COPY package*.json ./",
		"RUN npm ci", "",
		"# Copy source and build",
		"COPY . .",
		"RUN npm run build", "",
		"# Production stage with nginx",
		"FROM nginx:alpine", "",
		"# Copy built files from builder stage",
		"COPY --from=builder /app/" + outDir + " /usr/share/nginx/html", "",
		"# Create nginx config for SPA routing",
		"RUN echo 'server { listen 80; location / { root /usr/share/nginx/html; try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf", "",
		"EXPOSE 80", "",
		`CMD ["nginx", "-g", "daemon off;"]`,
		"",
	}, "\n")
}

// ReactOutputDir is "dist" for Vite projects and "build" for Create React App.
func ReactOutputDir(dir string) string {
	for _, name := range []string{"vite.config.js", "vite.config.ts", "vite.config.mjs", "vite.config.mts"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return "dist"
		}
	}
	return "build"
}

// GenerateDockerfile returns a Dockerfile for nodejs, nextjs and react
// services, or "" for types that must ship their own.
func GenerateDockerfile(serviceType, dir string) string {
	pkg := ReadPackageJSON(dir)
	switch serviceType {
	case "nodejs":
		return NodejsDockerfile(pkg)
	case "nextjs":
		return NextjsDockerfile(pkg)
	case "react":
		return ReactDockerfile(pkg, ReactOutputDir(dir))
	}
	return ""
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/worker/build/`
Expected: `ok  infrapilot/internal/worker/build`

- [ ] **Step 5: Commit**

```bash
git add internal/worker/build
git commit -m "feat(go): Dockerfile generation for node, next and react apps"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 22: Build in an isolated BuildKit builder

> **CTO amendment (fix round 1, `review/decisions.md` 2026-10-10 "Task 22 fix round 1"; supersedes the `git checkout --end-of-options` and "both still best effort" text of the FWD-A1 amendment below):**
> Git steps in `BuildAndPush`, each under its own `context.WithTimeout(ctx, gitStepTimeout)` (const 5 min; a deadline hit returns an error that names the step): `clone --depth 1 --single-branch --no-tags --branch <branch> -- <url> <repo>`; then, only if `s.CommitSha` is neither `s.Branch` nor `HEAD`: `fetch --depth 50 --no-tags --end-of-options origin <sha>` (failure is logged at warn and ignored, the object may already be present), then `git switch --detach --end-of-options <sha>` (FAILURE FAILS THE BUILD, error scrubbed), then if the ref matches `^[0-9a-fA-F]{7,40}$` run `git rev-parse HEAD` and fail unless the output, lower-cased, has the lower-cased ref as prefix. A non-hex, non-branch ref (tag) just has to switch. Clone, fetch, switch and rev-parse ALL use `gitArgs(...)`, so every step carries `gitSafe`; add to `gitSafe`: `-c core.hooksPath=/dev/null -c submodule.recurse=false`. Do NOT add `core.symlinks=false` (rejected: it rewrites symlinks in tenant build contexts; SEC-1 OpenRoot already handles symlinks). Docker push gets a 10 min timeout; `buildx build` keeps the caller's ctx (Task 26's 30 min).
> Every error returned from `BuildAndPush` (clone, fetch, switch, rev-parse, buildx, push) goes through one helper `b.fail(op, err, repoURL)` = `fmt.Errorf("%s: %s", op, gitsrc.Scrub(err.Error(), repoURL))`, ctx error first.
> `gitsrc` changes (the API reuses them): `ValidRepoURL` rejects any rune outside 0x21..0x7e; `RedactURL` also replaces a non-empty query with `****` and drops the fragment; `Scrub` also replaces the full `rawURL` text and its raw query value with `****`; new `ValidImageTag(tag, ref string) error`: `len <= 255`, matches `^[a-z0-9][A-Za-z0-9._:/-]*$`, ends in `":"+ref`. `BuildAndPush` calls it right after `Validate` (error `ErrInvalid`, field `image tag`). Task 23 keeps building `registry/serviceId:commitSha` and needs no change beyond a test that an over-long or `-`-prefixed registry value gives `InvalidSource`.
> `internal/runtime/exec.go` `firstArg`: skip leading `-c <k=v>` pairs so errors read `git clone: exit status 128` (VER-2); extend `TestFirstArg`.
> Tests (STAFF-4): `RedactURL`/`Scrub` with a password-only userinfo escaped (`https://:p%40ss@h/x`), special characters in token, query token (`?token=ghp_Q`), fragment; `Scrub` applied to a fake fetch, switch and buildx error; `TestBuildAndPushSwitchFailureFails` (switch fake returns an error, build errors, no `docker` call); `TestBuildAndPushVerifiesSha` (rev-parse returns another sha -> error; matching prefix -> ok; ref `main`/`HEAD` -> no fetch, switch or rev-parse); non-ASCII URL rows (U+2028, U+00A0, fullwidth, bidi) in the reject table; `ValidImageTag` table; step timeout test with a fake runner that blocks until ctx is done (tiny `gitStepTimeout` via a Builder field). Step 5 real-git probe: with the real git, `git switch --detach --end-of-options <sha>` on a throwaway repo, and record `git --version`.
> `#nosec G302 G304 G306`: leave as is for this round; the first CI gosec run decides (backlog line).


> **CTO amendment (SEC-1, `review/decisions.md` 2026-10-07):** in `BuildAndPush`, use `root.Lstat("Dockerfile")` on `os.OpenRoot(repo)`. Only on not-found, create it with `root.OpenFile("Dockerfile", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)` and `// #nosec G306 -- BuildKit must read it; generated, not secret`. Any existing entry, including a dangling symlink, means the repo has its own Dockerfile: write nothing. Add a test: a dangling `Dockerfile` symlink pointing into a second temp dir; the target must not exist after the build.

> **CTO amendment (FWD-A1, FWD-A2, `review/decisions.md` 2026-10-08):**
> Files: also create `control-plane-go/internal/gitsrc/gitsrc.go` and `gitsrc_test.go` (the API reuses them in Plan 2 Task 8).
> `package gitsrc`: `var ErrInvalid = errors.New("invalid build source")`. `ValidRepoURL(s) error`: `len(s) <= 2048`, no ASCII control chars or whitespace, `url.Parse` succeeds, `Scheme == "https"`, `Host != ""`, `Opaque == ""`, and `Hostname()` does not start with `-`. `ValidBranch(s) error`: matches `^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`, has no `..`, `//` or `@{`, and does not end in `/`, `.` or `.lock`. `ValidRef(s) error` (commitSha, also used as the image tag): matches `^[A-Za-z0-9][A-Za-z0-9._-]{3,99}$` and has no `..`. `Validate(repoURL, branch, ref) error` runs all three. Every error wraps `ErrInvalid` and names the field, never the value. `RedactURL(s) string`: on a parse error return `[unparseable]`; if `u.User != nil`, set `u.User = url.User("****")`; return `u.String()` (this covers a token-as-username, which `url.Redacted` misses). `Scrub(msg, rawURL) string`: if `rawURL` has userinfo, replace in `msg`, in this order, `u.User.String()`, the unescaped password (if set) and the unescaped username with `****`.
> `gitsrc_test.go`, table tests. Accepted: `https://github.com/x/y.git`, `https://tok@github.com/x/y`, branch `main`, `feature/a-b`, ref `HEAD`, `main`, `abcd`, 40-hex. Rejected: `file:///etc`, `ext::sh -c id`, `ssh://git@h/x`, `git@github.com:x/y.git`, `http://h/x`, `https://-oProxyCommand=x/y`, `https://h/x y`, branch `--upload-pack=x`, `-b`, `a..b`, `a/`, `x.lock`, ref `--upload-pack=touch /tmp/pwn`, `-x12`, `ab`, `a..bc`, `feature/x`. Each rejection satisfies `errors.Is(err, ErrInvalid)`, and its message does not contain the input. `RedactURL("https://user:pw1@h/x")` and `RedactURL("https://ghp_T0K@h/x")` both contain neither secret. A `Scrub` test where git-style text echoes the URL (`fatal: unable to access 'https://user:pw%21@h/x/'`, with both escaped and unescaped forms) removes the secret.
> `builder.go`: first line of `BuildAndPush`: `if err := gitsrc.Validate(s.RepoURL, s.Branch, s.CommitSha); err != nil { return err }`, before any filesystem or runner call. `var gitSafe = []string{"-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "credential.helper="}` and `func gitArgs(a ...string) []string { return append(slices.Clone(gitSafe), a...) }`. The commands become `git <gitSafe> clone --depth 1 --branch <branch> -- <repoURL> <repo>`, `git <gitSafe> fetch --depth 50 --end-of-options origin <sha>` and `git checkout --end-of-options <sha>` (both still best effort). Log `"repo", gitsrc.RedactURL(s.RepoURL)`. A clone error returns `fmt.Errorf("git clone: %s", gitsrc.Scrub(err.Error(), s.RepoURL))` (no `%w`), and if `ctx.Err() != nil` it returns `ctx.Err()` first. Requires git >= 2.24 for `--end-of-options`: Step 5 also runs `git --version` and `git -C <tmp repo> checkout --end-of-options main`, and records both.
> Tests: update `fakeDocker` to find the git subcommand after the `-c k=v` pairs (helper `gitSub(args)`). Update the `want` list to the exact new argv. Valid values replace `RepoURL: "u"`, `CommitSha: "c"` and `Branch: "b"` in the other tests. New `TestBuildAndPushRejectsUnsafeSource`: the same reject rows through `BuildAndPush` → `errors.Is(err, gitsrc.ErrInvalid)`, and `r.Commands()` is empty. New `TestBuildAndPushHidesRepoCredentials`: `RepoURL` is `https://user:ghp_SECRET1@example.com/r.git` (and a second case `https://ghp_TOKEN2@example.com/r.git`). The fake clone returns `fatal: unable to access '<that URL>/': 403`. The builder logs to a JSON slog handler on a buffer. Neither the returned error nor the buffer contains `ghp_`. In a success run the buffer contains `****@example.com`.

**Files:**
- Create: `control-plane-go/internal/worker/build/builder.go`
- Test: `control-plane-go/internal/worker/build/builder_test.go`

Dockerfile steps from user repos run inside the `cp-builder` BuildKit container, which has CPU and memory limits, instead of on the host's Docker daemon. `--load` hands the image back to the host daemon, and a normal `docker push` sends it to the registry: BuildKit's container cannot reach `localhost:5001` itself.

- [ ] **Step 1: Write the failing tests**

`internal/worker/build/builder_test.go`:
```go
package build

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"infrapilot/internal/testutil"
)

// fakeDocker simulates git clone (creates the repo dir with files), an
// existing or missing buildx builder, and captures the Dockerfile at build time.
func fakeDocker(files map[string]string, builderExists bool, dockerfile *string) *testutil.FakeRunner {
	return &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		switch {
		case c.Name == "git" && c.Args[0] == "clone":
			dir := c.Args[len(c.Args)-1]
			os.MkdirAll(dir, 0o755)
			for name, body := range files {
				os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
			}
		case c.Name == "git" && c.Args[0] == "fetch":
			return "", errors.New("shallow fetch failed") // ignored, like Node
		case c.Name == "docker" && c.Args[0] == "buildx" && c.Args[1] == "inspect" && !builderExists:
			return "", errors.New("no builder \"cp-builder\" found")
		case c.Name == "docker" && c.Args[0] == "buildx" && c.Args[1] == "build":
			b, _ := os.ReadFile(filepath.Join(c.Dir, "Dockerfile"))
			*dockerfile = string(b)
		}
		return "", nil
	}}
}

func newBuilder(r *testutil.FakeRunner) Builder {
	return Builder{R: r, Name: "cp-builder", Memory: "2g", CPUs: 2, Log: slog.New(slog.DiscardHandler)}
}

func TestBuildAndPushCreatesBuilderAndGeneratesDockerfile(t *testing.T) {
	var dockerfile string
	r := fakeDocker(map[string]string{"package.json": `{"scripts":{"start":"node app.js"}}`}, false, &dockerfile)
	wd := filepath.Join(t.TempDir(), "dep1")

	err := newBuilder(r).BuildAndPush(context.Background(), Spec{
		RepoURL: "https://example.com/r.git", Branch: "main", CommitSha: "abc1234",
		ImageTag: "localhost:5001/svc:abc1234", WorkDir: wd, ServiceType: "nodejs",
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(wd, "repo")
	want := []string{
		"git clone --depth 1 --branch main https://example.com/r.git " + repo,
		"git fetch --depth 50 origin abc1234",
		"git checkout abc1234",
		"docker buildx inspect cp-builder",
		"docker buildx create --name cp-builder --driver docker-container --driver-opt memory=2g --driver-opt cpu-period=100000 --driver-opt cpu-quota=200000 --bootstrap",
		"docker buildx build --builder cp-builder --load -t localhost:5001/svc:abc1234 .",
		"docker push localhost:5001/svc:abc1234",
	}
	if got := r.Commands(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	if !strings.Contains(dockerfile, `CMD ["sh", "-c", "npm start"]`) {
		t.Fatalf("generated Dockerfile:\n%s", dockerfile)
	}
	if _, err := os.Stat(wd); !os.IsNotExist(err) {
		t.Fatal("work dir should be removed")
	}
}

func TestBuildAndPushReusesBuilderAndKeepsDockerfile(t *testing.T) {
	var dockerfile string
	r := fakeDocker(map[string]string{"Dockerfile": "FROM custom"}, true, &dockerfile)
	err := newBuilder(r).BuildAndPush(context.Background(), Spec{
		RepoURL: "u", Branch: "main", CommitSha: "main", ImageTag: "img", WorkDir: t.TempDir() + "/w", ServiceType: "nodejs",
	})
	if err != nil || dockerfile != "FROM custom" {
		t.Fatalf("err=%v dockerfile=%q", err, dockerfile)
	}
	for _, c := range r.Commands() {
		if strings.HasPrefix(c, "docker buildx create") {
			t.Fatal("existing builder must be reused")
		}
	}
}

func TestBuildAndPushStopsOnBuildFailure(t *testing.T) {
	r := &testutil.FakeRunner{Handle: func(c testutil.Call) (string, error) {
		if c.Name == "docker" && len(c.Args) > 1 && c.Args[1] == "build" {
			return "", errors.New("build broke")
		}
		return "", nil
	}}
	err := newBuilder(r).BuildAndPush(context.Background(), Spec{RepoURL: "u", Branch: "b", CommitSha: "c", ImageTag: "i", WorkDir: t.TempDir() + "/w", ServiceType: "docker"})
	if err == nil || !strings.Contains(err.Error(), "build broke") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.Commands() {
		if strings.HasPrefix(c, "docker push") {
			t.Fatal("must not push after failed build")
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/worker/build/ -run BuildAndPush`
Expected: FAIL with `undefined: Builder`.

- [ ] **Step 3: Implement**

`internal/worker/build/builder.go`:
```go
package build

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"

	"infrapilot/internal/runtime"
)

type Spec struct {
	RepoURL     string
	Branch      string
	CommitSha   string
	ImageTag    string // <registry>/<serviceId>:<commitSha>
	WorkDir     string // <BUILD_WORKDIR>/<deploymentId>, removed afterwards
	ServiceType string
}

// Builder builds images in an isolated buildx builder (BuildKit in its own
// container with CPU/memory limits) instead of on the host daemon.
type Builder struct {
	R      runtime.Runner
	Name   string // buildx builder name, e.g. "cp-builder"
	Memory string // BuildKit container memory limit, e.g. "2g"
	CPUs   int    // BuildKit container CPU limit
	Log    *slog.Logger
}

// EnsureBuilder creates the builder on first use.
func (b Builder) EnsureBuilder(ctx context.Context) error {
	if _, err := b.R.Run(ctx, "", "docker", "buildx", "inspect", b.Name); err == nil {
		return nil
	}
	b.Log.Info("creating buildx builder", "name", b.Name)
	_, err := b.R.Run(ctx, "", "docker", "buildx", "create", "--name", b.Name, "--driver", "docker-container",
		"--driver-opt", "memory="+b.Memory,
		"--driver-opt", "cpu-period=100000",
		"--driver-opt", "cpu-quota="+strconv.Itoa(b.CPUs*100000),
		"--bootstrap")
	return err
}

// BuildAndPush clones the repo, writes a Dockerfile if the type needs one and
// the repo has none, builds in the isolated builder and pushes the image.
func (b Builder) BuildAndPush(ctx context.Context, s Spec) error {
	if err := os.RemoveAll(s.WorkDir); err != nil {
		return err
	}
	if err := os.MkdirAll(s.WorkDir, 0o750); err != nil {
		return err
	}
	defer os.RemoveAll(s.WorkDir)
	repo := filepath.Join(s.WorkDir, "repo")

	b.Log.Info("cloning", "repo", s.RepoURL, "branch", s.Branch)
	if _, err := b.R.Run(ctx, "", "git", "clone", "--depth", "1", "--branch", s.Branch, s.RepoURL, repo); err != nil {
		return err
	}
	// Best effort: check out the exact commit when it is not the branch tip.
	_, _ = b.R.Run(ctx, repo, "git", "fetch", "--depth", "50", "origin", s.CommitSha)
	_, _ = b.R.Run(ctx, repo, "git", "checkout", s.CommitSha)

	if content := GenerateDockerfile(s.ServiceType, repo); content != "" {
		path := filepath.Join(repo, "Dockerfile")
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			b.Log.Info("generating Dockerfile", "serviceType", s.ServiceType)
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return fmt.Errorf("write Dockerfile: %w", err)
			}
		}
	}

	if err := b.EnsureBuilder(ctx); err != nil {
		return err
	}
	b.Log.Info("building image", "image", s.ImageTag)
	if _, err := b.R.Run(ctx, repo, "docker", "buildx", "build", "--builder", b.Name, "--load", "-t", s.ImageTag, "."); err != nil {
		return err
	}
	b.Log.Info("pushing image", "image", s.ImageTag)
	_, err := b.R.Run(ctx, "", "docker", "push", s.ImageTag)
	return err
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/worker/build/`
Expected: `ok  infrapilot/internal/worker/build`

- [ ] **Step 5: Check the builder flags against real Docker**

```bash
docker buildx create --name cp-builder-check --driver docker-container \
  --driver-opt memory=2g --driver-opt cpu-period=100000 --driver-opt cpu-quota=200000 --bootstrap
docker inspect -f '{{.HostConfig.Memory}} {{.HostConfig.CpuQuota}}' buildx_buildkit_cp-builder-check0
docker buildx rm cp-builder-check
```
Expected: `2147483648 200000`.

If `buildx create` rejects `memory` or `cpu-quota`, the installed buildx is too old: update Docker Desktop. Do not drop the limits; report it in the task record.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/build
git commit -m "feat(go): build images in a resource-limited buildx builder"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 23: Deploy activities

> **CTO amendment (fix round 1, `review/decisions.md` 2026-10-10 "Task 23 fix round 1"):** (1) Mongo `CapAdd` = CHOWN, SETUID, SETGID, DAC_READ_SEARCH (VER-1); `RunSpec.validate` allows only {CHOWN, SETUID, SETGID, DAC_OVERRIDE, DAC_READ_SEARCH, NET_BIND_SERVICE}, errors wrap `ErrInvalidSpec` (SEC-1). (2) `var StaticSiteCaps = []string{"CHOWN","SETUID","SETGID"}`, set in `RunContainer` for `job.ServiceType == store.TypeReact` only; the port stays `DetectContainerPort` (EXPOSE 80), Task 21 unchanged (VER-2). (3) Remove `--appendonly yes` from Redis (STAFF-1). (4) Real-Docker tests in `docker_integration_test.go` (skip under `-short`/no daemon, Task 16 precedent): Mongo redeploy on an existing volume stays up 15 s; Redis seeded `dump.rdb` (uid 999, 0600) through `CopyIntoVolume` gives `DBSIZE` > 0; `nginx:alpine` with `StaticSiteCaps` accepts a `Probe` on 80, fails without (VER-3). (5) `InsertEnvVarIfAbsent` (`ON CONFLICT DO NOTHING`) used by `EnsureCredentials`, plus a 20-way concurrency test (STAFF-2). (6) Idempotency tests: `RunContainer` twice; `RetirePrevious` partial failure then retry; no discarded setup errors (STAFF-3). (7) `legacyDir` applies `filepath.EvalSymlinks` to root and candidate then re-checks `Rel`; `Probe` gets `--cap-drop ALL --security-opt no-new-privileges`; `CopyIntoVolume` gets `--cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add DAC_READ_SEARCH --cap-add FOWNER --security-opt no-new-privileges` (SEC-2). (8) `MarkFailed` counts `status NOT IN ('deployed','failed')` (SEC-3). (9) `Claim` returns true when `ClaimDeployment` is false but the row's status already equals `To` (workflow ID is unique, so it is our own lost result) (FWD-1). (10) `RunContainer` calls `keepAlive(ctx, 20*time.Second, "running")` (FWD-2).

> **CTO 2026-10-09 (managed volume path, `review/decisions.md`).**
> - `ManagedImage` gains `DataDir string` and `Tmpfs []string`. Redis: `DataDir: "/data"`. Mongo: `DataDir: "/data/db"`, `Tmpfs: []string{"/data/configdb"}`.
> - In `RunContainer`, the managed branch sets `spec.VolumeTarget = m.DataDir` and `spec.Tmpfs = m.Tmpfs`.
> - Delete `legacyMongoPassword` and `hasExistingData`. `EnsureCredentials` always generates a random password when `env[m.PasswordEnv]` is empty, for both Mongo and Redis.
> - `EnsureVolume` copies the legacy directory only for Redis (`job.ServiceType == store.TypeRedis`). Node never persisted Mongo data there, so Mongo always gets a fresh volume; its Node-era data moves through the Task 27 runbook.
> - Tests: replace `TestEnsureCredentialsKeepsLegacyMongoPassword` with `TestEnsureCredentialsMongoIgnoresLegacyDir` (legacy dir present → password is not `admin123` and has 20+ chars); add `TestEnsureVolumeSkipsLegacyCopyForMongo` (only `create cp-vol-<id>`); the managed `RunContainer` test asserts Mongo `VolumeTarget "/data/db"` + `Tmpfs ["/data/configdb"]` and Redis `VolumeTarget "/data"`; `TestEnsureVolumeMigratesLegacyDataIdempotently` stays (Redis).

> **CTO amendment (FWD-A1, FWD-A2, FWD-A3, `review/decisions.md` 2026-10-08):**
> `LoadJob` sets `job.RepoURL = nil` before returning, with the comment "may embed a token; must never enter workflow history. BuildImage reads it from the store." `BuildImage(ctx, in BuildInput) (string, error)` with `type BuildInput struct{ DeploymentID string }`: it calls `a.Store.GetJob`, maps `ErrNotFound` to non-retryable `NotFound`, keeps the nil/empty `RepoURL` → `MissingRepo` check, then builds as now. If the error satisfies `errors.Is(err, gitsrc.ErrInvalid)`, it returns `temporal.NewNonRetryableApplicationError("invalid_build_source", "InvalidSource", nil)` (no cause). Other builder errors pass through, since they are already scrubbed. `legacyDir`: use `*job.VolumePath` only if it is absolute and `rel, err := filepath.Rel(filepath.Clean(a.Cfg.LegacyVolumeRoot), filepath.Clean(p))` gives `err == nil`, `rel != "."`, `rel` not `..` and not prefixed by `../`. Otherwise use `filepath.Join(LegacyVolumeRoot, job.ServiceID)` and log a warning with the deployment ID only.
> Tests: `TestLoadJobOmitsRepoURL`: seed a service with `https://ghp_SECRET9@example.com/x.git`. `LoadJob`'s result has `RepoURL == nil`, and `converter.GetDefaultDataConverter().ToPayload(result)` data does not contain `ghp_`. `TestBuildImage` uses `BuildInput{DeploymentID}`, the fake builder receives the stored URL, and a nil URL still gives `MissingRepo`. New case: a fake builder returning `fmt.Errorf("x: %w", gitsrc.ErrInvalid)` → non-retryable `InvalidSource`. `TestLegacyDir`: `VolumePath` `/etc` → default dir; `<root>/../x` → default; `<root>/s1` → used; `<root>` itself → default; `cp-vol-x` (non-absolute) → default.
>
> **CTO amendment (STAFF-4 T8, `review/decisions.md` 2026-10-08):** add `func (s *Store) MarkFailed(ctx, id string) error` running `UPDATE "Deployment" SET "status" = 'failed', "updatedAt" = now() WHERE "id" = $1 AND "status" <> 'deployed'` (0 rows → nil). The `MarkFailed` activity uses it. Store test: a deployed row stays deployed and a deploying row becomes failed. Activity test: `MarkFailed` on a deployed row leaves it deployed. Add the Task 8 STAFF-1 assertions while editing `deployments_test.go`.
>
> **CTO amendment (STAFF-5 T15, `review/decisions.md` 2026-10-09):** the store method becomes `func (s *Store) MarkFailed(ctx, id string) (bool, error)`. It returns `true` only when the UPDATE changed a row; a missing row and a `deployed` row both give `(false, nil)`. The `MarkFailed` activity calls `a.Metrics.DeploymentFinished("failed", 0)` only when `changed` is true, and no longer branches on `ErrNotFound`. Tests: the store test asserts `changed == true` for a `deploying` row and `false` for a `deployed` row and for an unknown id. The activity test uses a real `*metrics.Worker` and asserts that `cp_deployments_total{outcome="failed"}` stays 0 after `MarkFailed` on a deployed row. On the `MarkDeployed` activity, add a comment that the counter is best-effort: if the worker dies after the increment and before Temporal records completion, the retry counts again. This is accepted for counters.

**Files:**
- Create: `control-plane-go/internal/workflows/names.go`, `control-plane-go/internal/workflows/activities.go`
- Test: `control-plane-go/internal/workflows/activities_test.go`

Activities are the side-effecting steps of `DeployWorkflow` (Task 24). They depend on small interfaces, so tests use fakes. Activities that heartbeat need a Temporal activity context, so tests run them through `testsuite.TestActivityEnvironment`.

- [ ] **Step 1: Add the Temporal SDK**

Run: `go get go.temporal.io/sdk`

- [ ] **Step 2: Write the failing tests**

`internal/workflows/activities_test.go`:
```go
package workflows

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/worker/build"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

type fakeDocker struct {
	mu        sync.Mutex
	calls     []string
	specs     []runtime.RunSpec
	volumes   map[string]bool
	probeFail int // failing probes before success; -1 = always fail
	port      int
	portErr   error
	used      map[int]bool
}

func (f *fakeDocker) rec(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeDocker) Pull(_ context.Context, image string) error { f.rec("pull " + image); return nil }
func (f *fakeDocker) DetectContainerPort(context.Context, string) (int, error) {
	return f.port, f.portErr
}
func (f *fakeDocker) Run(_ context.Context, s runtime.RunSpec) (string, error) {
	f.mu.Lock()
	f.specs = append(f.specs, s)
	f.mu.Unlock()
	f.rec("run " + s.Name)
	return "ctr-" + s.Name, nil
}
func (f *fakeDocker) Remove(_ context.Context, name string) error { f.rec("remove " + name); return nil }
func (f *fakeDocker) EnsureNetwork(_ context.Context, name string) error {
	f.rec("network " + name)
	return nil
}
func (f *fakeDocker) VolumeExists(_ context.Context, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumes[name]
}
func (f *fakeDocker) CreateVolume(_ context.Context, name string) error {
	f.mu.Lock()
	f.volumes[name] = true
	f.mu.Unlock()
	f.rec("create " + name)
	return nil
}
func (f *fakeDocker) CopyIntoVolume(_ context.Context, dir, volume string) error {
	f.rec("copy " + volume)
	return nil
}
func (f *fakeDocker) Probe(_ context.Context, _, host string, _ int) error {
	f.rec("probe " + host)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.probeFail == -1 {
		return errors.New("connection refused")
	}
	if f.probeFail > 0 {
		f.probeFail--
		return errors.New("connection refused")
	}
	return nil
}
func (f *fakeDocker) UsedPorts(context.Context) map[int]bool {
	out := map[int]bool{}
	for p := range f.used {
		out[p] = true
	}
	return out
}

type fakeRouter struct{ routes []string }

func (f *fakeRouter) UpsertRoute(_ context.Context, serviceID, name string, port int) error {
	f.routes = append(f.routes, serviceID+"->"+name)
	return nil
}
func (f *fakeRouter) RuntimeURL(serviceID string) string { return "http://" + serviceID + ".localhost" }

type fakeBuilder struct {
	specs []build.Spec
	err   error
}

func (f *fakeBuilder) BuildAndPush(_ context.Context, s build.Spec) error {
	f.specs = append(f.specs, s)
	return f.err
}

type fakeLogs struct{ started []string }

func (f *fakeLogs) Start(deploymentID, containerID string) {
	f.started = append(f.started, deploymentID+"/"+containerID)
}

type env struct {
	st      *store.Store
	acts    *Activities
	docker  *fakeDocker
	router  *fakeRouter
	builder *fakeBuilder
	logs    *fakeLogs
}

func newEnv(t *testing.T) env {
	st := store.New(testutil.NeedDB(t, pool))
	box, err := secrets.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := env{st: st, docker: &fakeDocker{port: 8080, volumes: map[string]bool{}, used: map[int]bool{}},
		router: &fakeRouter{}, builder: &fakeBuilder{}, logs: &fakeLogs{}}
	e.acts = &Activities{
		Store: st, Secrets: box, Docker: e.docker, Router: e.router, Builder: e.builder, Logs: e.logs,
		Log: slog.New(slog.DiscardHandler),
		Cfg: ActivityConfig{
			RegistryHost: "reg:5001", BuildWorkdir: "/tmp/cp-builds", DefaultContainerPort: 3080,
			PortStart: 20000, PortEnd: 20010, LegacyVolumeRoot: t.TempDir(),
			HealthTimeout: 50 * time.Millisecond, HealthInterval: time.Millisecond,
		},
	}
	return e
}

func (e env) run(t *testing.T, fn any, args ...any) (converter.EncodedValue, error) {
	t.Helper()
	var s testsuite.WorkflowTestSuite
	ae := s.NewTestActivityEnvironment()
	ae.RegisterActivity(e.acts)
	return ae.ExecuteActivity(fn, args...)
}

func (e env) job(t *testing.T, serviceType string) store.Job {
	t.Helper()
	ctx := context.Background()
	f := testutil.Seed(t, e.st, serviceType)
	d, err := e.st.CreateDeployment(ctx, f.ServiceID, "main")
	if err != nil {
		t.Fatal(err)
	}
	j, err := e.st.GetJob(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func isNonRetryable(err error) bool {
	var appErr *temporal.ApplicationError
	return errors.As(err, &appErr) && appErr.NonRetryable()
}

func TestLoadJob(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	val, err := e.run(t, e.acts.LoadJob, j.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	var got store.Job
	val.Get(&got)
	if got.ProjectID != j.ProjectID || got.ServiceType != "nodejs" {
		t.Fatalf("got %+v", got)
	}
	if _, err := e.run(t, e.acts.LoadJob, "missing"); !isNonRetryable(err) {
		t.Fatalf("missing deployment must be non-retryable, got %v", err)
	}
}

func TestBuildImage(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeNodejs)
	val, err := e.run(t, e.acts.BuildImage, j)
	if err != nil {
		t.Fatal(err)
	}
	var image string
	val.Get(&image)
	if image != "reg:5001/"+j.ServiceID+":main" {
		t.Fatalf("image = %s", image)
	}
	s := e.builder.specs[0]
	if s.WorkDir != filepath.Join("/tmp/cp-builds", j.DeploymentID) || s.ImageTag != image || s.ServiceType != "nodejs" {
		t.Fatalf("spec %+v", s)
	}

	j.RepoURL = nil
	if _, err := e.run(t, e.acts.BuildImage, j); !isNonRetryable(err) {
		t.Fatalf("missing repo must be non-retryable, got %v", err)
	}
}

func decryptedEnv(t *testing.T, e env, serviceID string) map[string]string {
	t.Helper()
	raw, _ := e.st.EnvMap(context.Background(), serviceID)
	out := map[string]string{}
	for k, v := range raw {
		if !secrets.IsEncrypted(v) {
			t.Fatalf("%s stored unencrypted", k)
		}
		out[k], _ = e.acts.Secrets.Decrypt(v)
	}
	return out
}

func TestEnsureCredentialsGeneratesEncryptedPasswordOnce(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeRedis)
	if _, err := e.run(t, e.acts.EnsureCredentials, j); err != nil {
		t.Fatal(err)
	}
	first := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]
	if len(first) != 32 {
		t.Fatalf("password %q", first)
	}
	e.run(t, e.acts.EnsureCredentials, j)
	if again := decryptedEnv(t, e, j.ServiceID)["REDIS_PASSWORD"]; again != first {
		t.Fatal("existing password must not be replaced")
	}
}

func TestEnsureCredentialsKeepsLegacyMongoPassword(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeMongoDB)
	os.MkdirAll(filepath.Join(e.acts.Cfg.LegacyVolumeRoot, j.ServiceID), 0o755)
	if _, err := e.run(t, e.acts.EnsureCredentials, j); err != nil {
		t.Fatal(err)
	}
	if pw := decryptedEnv(t, e, j.ServiceID)["MONGO_INITDB_ROOT_PASSWORD"]; pw != "admin123" {
		t.Fatalf("legacy Mongo data must keep admin123, got %q", pw)
	}
}

func TestEnsureVolumeMigratesLegacyDataIdempotently(t *testing.T) {
	e := newEnv(t)
	j := e.job(t, store.TypeRedis)
	os.MkdirAll(filepath.Join(e.acts.Cfg.LegacyVolumeRoot, j.ServiceID), 0o755)

	val, err := e.run(t, e.acts.EnsureVolume, j)
	if err != nil {
		t.Fatal(err)
	}
	var vol string
	val.Get(&vol)
	e.run(t, e.acts.EnsureVolume, j)
	vname := "cp-vol-" + j.ServiceID
	want := []string{"create " + vname, "copy " + vname, "copy " + vname}
	if vol != vname || !slices.Equal(e.docker.calls, want) {
		t.Fatalf("vol=%s calls=%v", vol, e.docker.calls)
	}
}

func TestRunContainerForBuiltApp(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)
	image := "reg:5001/" + j.ServiceID + ":main"
	j.Image = &image
	enc, _ := e.acts.Secrets.Encrypt("bar")
	e.st.UpsertEnvVar(ctx, j.ServiceID, "FOO", enc)
	e.st.UpsertEnvVar(ctx, j.ServiceID, "PLAIN", "legacy")

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "cp-proj-" + j.ProjectID})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	s := e.docker.specs[0]
	if s.Name != "cp-"+j.DeploymentID || s.Network != "cp-proj-"+j.ProjectID || s.Aliases[0] != "svc-"+j.ServiceID ||
		s.Labels["cp.projectId"] != j.ProjectID || s.Env["NODE_ENV"] != "production" || s.Env["FOO"] != "bar" ||
		s.Env["PLAIN"] != "legacy" || s.ContainerPort != 8080 || s.PublishPort != 0 || s.Volume != "" {
		t.Fatalf("spec %+v", s)
	}
	if res.HostPort != nil || res.ContainerName != s.Name || res.ContainerPort != 8080 {
		t.Fatalf("result %+v", res)
	}
}

func TestRunContainerForManagedRedis(t *testing.T) {
	e := newEnv(t)
	e.acts.Cfg.ExposeManagedPorts = true
	e.docker.used = map[int]bool{20000: true}
	j := e.job(t, store.TypeRedis)
	e.run(t, e.acts.EnsureCredentials, j)

	val, err := e.run(t, e.acts.RunContainer, RunInput{Job: j, Network: "n", Volume: "cp-vol-" + j.ServiceID})
	if err != nil {
		t.Fatal(err)
	}
	var res RunResult
	val.Get(&res)
	s := e.docker.specs[0]
	if s.Image != "redis:7-alpine" || s.ContainerPort != 6379 || s.Volume != "cp-vol-"+j.ServiceID ||
		!strings.Contains(strings.Join(s.Cmd, " "), `--requirepass "$REDIS_PASSWORD"`) || len(s.Env["REDIS_PASSWORD"]) != 32 {
		t.Fatalf("spec %+v", s)
	}
	if res.HostPort == nil || *res.HostPort != 20001 || s.PublishPort != 20001 {
		t.Fatalf("result %+v", res)
	}
}

func TestWaitHealthy(t *testing.T) {
	e := newEnv(t)
	e.docker.probeFail = 2
	if _, err := e.run(t, e.acts.WaitHealthy, WaitInput{Network: "n", Host: "cp-d1", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	if len(e.docker.calls) != 3 {
		t.Fatalf("probes = %v", e.docker.calls)
	}

	e.docker.probeFail = -1
	_, err := e.run(t, e.acts.WaitHealthy, WaitInput{Network: "n", Host: "cp-d1", Port: 3000})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestRetirePreviousRemovesOlderContainers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	f := testutil.Seed(t, e.st, store.TypeNodejs)
	old, _ := e.st.CreateDeployment(ctx, f.ServiceID, "v1")
	cur, _ := e.st.CreateDeployment(ctx, f.ServiceID, "v2")
	for _, d := range []store.Deployment{old, cur} {
		e.st.MarkDeployed(ctx, d.ID, store.DeployedInfo{ContainerID: "c", ContainerPort: 1})
	}

	if _, err := e.run(t, e.acts.RetirePrevious, RetireInput{ServiceID: f.ServiceID, KeepDeploymentID: cur.ID}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.docker.calls, []string{"remove cp-" + old.ID}) {
		t.Fatalf("calls %v", e.docker.calls)
	}
	o, _ := e.st.GetDeployment(ctx, old.ID)
	c, _ := e.st.GetDeployment(ctx, cur.ID)
	if o.StoppedAt == nil || c.StoppedAt != nil {
		t.Fatalf("old stopped=%v cur stopped=%v", o.StoppedAt, c.StoppedAt)
	}
}

func TestStatusActivities(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	j := e.job(t, store.TypeNodejs)

	val, _ := e.run(t, e.acts.Claim, ClaimInput{DeploymentID: j.DeploymentID, From: store.StatusQueued, To: store.StatusBuilding})
	var won bool
	val.Get(&won)
	if !won {
		t.Fatal("claim should win")
	}
	e.run(t, e.acts.MarkBuilt, MarkBuiltInput{DeploymentID: j.DeploymentID, Image: "img"})
	url := "http://x.localhost"
	if _, err := e.run(t, e.acts.MarkDeployed, DeployedInput{DeploymentID: j.DeploymentID, ContainerID: "c1", ContainerPort: 3000, RuntimeURL: &url}); err != nil {
		t.Fatal(err)
	}
	d, _ := e.st.GetDeployment(ctx, j.DeploymentID)
	if d.Status != store.StatusDeployed || *d.Image != "img" || *d.RuntimeURL != url {
		t.Fatalf("deployed %+v", d)
	}
	e.run(t, e.acts.MarkFailed, j.DeploymentID)
	if d, _ := e.st.GetDeployment(ctx, j.DeploymentID); d.Status != store.StatusFailed {
		t.Fatalf("status %s", d.Status)
	}

	val, _ = e.run(t, e.acts.SwitchRoute, RouteInput{ServiceID: "s1", ContainerName: "cp-d1", Port: 3000})
	var got string
	val.Get(&got)
	if got != "http://s1.localhost" || e.router.routes[0] != "s1->cp-d1" {
		t.Fatalf("url=%s routes=%v", got, e.router.routes)
	}
	e.run(t, e.acts.StartLogStream, StreamInput{DeploymentID: "d1", ContainerID: "c1"})
	if e.logs.started[0] != "d1/c1" {
		t.Fatalf("logs %v", e.logs.started)
	}
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/workflows/`
Expected: FAIL with `undefined: Activities`.

- [ ] **Step 4: Implement the names**

`internal/workflows/names.go`:
```go
// Package workflows holds the Temporal workflows that build and run
// deployments, their activities, and the starter used by the outbox.
package workflows

import "infrapilot/internal/store"

const TaskQueue = "deployments"

func DeployWorkflowID(deploymentID string) string { return "deploy-" + deploymentID }
func ContainerName(deploymentID string) string    { return "cp-" + deploymentID }
func ServiceAlias(serviceID string) string        { return "svc-" + serviceID }
func NetworkName(projectID string) string         { return "cp-proj-" + projectID }
func VolumeName(serviceID string) string          { return "cp-vol-" + serviceID }

// ManagedImage describes a managed database service.
type ManagedImage struct {
	Image       string
	Port        int
	PasswordEnv string            // env var holding the generated password
	Env         map[string]string // defaults; user env vars override them
	Cmd         []string
}

var Managed = map[string]ManagedImage{
	store.TypeRedis: {
		Image: "redis:7-alpine", Port: 6379, PasswordEnv: "REDIS_PASSWORD",
		// The password is expanded inside the container, so it never appears
		// in `docker inspect` output or the host's process list.
		Cmd: []string{"sh", "-c", `exec redis-server --appendonly yes --dir /data --requirepass "$REDIS_PASSWORD"`},
	},
	store.TypeMongoDB: {
		Image: "mongo:7", Port: 27017, PasswordEnv: "MONGO_INITDB_ROOT_PASSWORD",
		Env: map[string]string{"MONGO_INITDB_ROOT_USERNAME": "admin"},
	},
}

// legacyMongoPassword is what Node-era Mongo volumes were initialised with;
// Mongo ignores MONGO_INITDB_* once data exists, so those volumes keep it.
const legacyMongoPassword = "admin123"
```

- [ ] **Step 5: Implement the activities**

`internal/workflows/activities.go`:
```go
package workflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/worker/build"
)

// ContainerRuntime is implemented by runtime.Docker.
type ContainerRuntime interface {
	Pull(ctx context.Context, image string) error
	DetectContainerPort(ctx context.Context, image string) (int, error)
	Run(ctx context.Context, spec runtime.RunSpec) (string, error)
	Remove(ctx context.Context, name string) error
	EnsureNetwork(ctx context.Context, name string) error
	VolumeExists(ctx context.Context, name string) bool
	CreateVolume(ctx context.Context, name string) error
	CopyIntoVolume(ctx context.Context, hostDir, volume string) error
	Probe(ctx context.Context, network, host string, port int) error
	UsedPorts(ctx context.Context) map[int]bool
}

// Router is implemented by runtime.Nginx.
type Router interface {
	UpsertRoute(ctx context.Context, serviceID, containerName string, containerPort int) error
	RuntimeURL(serviceID string) string
}

// ImageBuilder is implemented by build.Builder.
type ImageBuilder interface {
	BuildAndPush(ctx context.Context, s build.Spec) error
}

// LogStarter is implemented by *runtime.Streamer.
type LogStarter interface {
	Start(deploymentID, containerID string)
}

type ActivityConfig struct {
	RegistryHost         string
	BuildWorkdir         string
	DefaultContainerPort int
	PortStart, PortEnd   int
	ExposeManagedPorts   bool
	LegacyVolumeRoot     string
	HealthTimeout        time.Duration
	HealthInterval       time.Duration // default 2s
}

type Activities struct {
	Store   *store.Store
	Secrets *secrets.Box
	Docker  ContainerRuntime
	Router  Router
	Builder ImageBuilder
	Logs    LogStarter
	Metrics *metrics.Worker // may be nil
	Log     *slog.Logger
	Cfg     ActivityConfig
}

type ClaimInput struct{ DeploymentID, From, To string }
type MarkBuiltInput struct{ DeploymentID, Image string }
type RunInput struct {
	Job     store.Job
	Network string
	Volume  string // managed DBs only
}
type RunResult struct {
	ContainerID   string
	ContainerName string
	ContainerPort int
	HostPort      *int
}
type WaitInput struct {
	Network string
	Host    string
	Port    int
}
type RouteInput struct {
	ServiceID     string
	ContainerName string
	Port          int
}
type RetireInput struct{ ServiceID, KeepDeploymentID string }
type DeployedInput struct {
	DeploymentID  string
	ContainerID   string
	HostPort      *int
	ContainerPort int
	RuntimeURL    *string
	VolumePath    *string
}
type StreamInput struct{ DeploymentID, ContainerID string }

func (a *Activities) LoadJob(ctx context.Context, deploymentID string) (store.Job, error) {
	job, err := a.Store.GetJob(ctx, deploymentID)
	if errors.Is(err, store.ErrNotFound) {
		return store.Job{}, temporal.NewNonRetryableApplicationError("deployment not found", "NotFound", err)
	}
	return job, err
}

func (a *Activities) Claim(ctx context.Context, in ClaimInput) (bool, error) {
	return a.Store.ClaimDeployment(ctx, in.DeploymentID, in.From, in.To)
}

func (a *Activities) BuildImage(ctx context.Context, job store.Job) (string, error) {
	if job.RepoURL == nil || *job.RepoURL == "" {
		return "", temporal.NewNonRetryableApplicationError("repoUrl_required_for_build", "MissingRepo", nil)
	}
	stop := keepAlive(ctx, 20*time.Second, "building")
	defer stop()

	start := time.Now()
	image := a.Cfg.RegistryHost + "/" + job.ServiceID + ":" + job.CommitSha
	err := a.Builder.BuildAndPush(ctx, build.Spec{
		RepoURL: *job.RepoURL, Branch: job.Branch, CommitSha: job.CommitSha, ImageTag: image,
		WorkDir: filepath.Join(a.Cfg.BuildWorkdir, job.DeploymentID), ServiceType: job.ServiceType,
	})
	if err != nil {
		return "", err
	}
	a.Metrics.ObserveBuild(time.Since(start))
	return image, nil
}

func (a *Activities) MarkBuilt(ctx context.Context, in MarkBuiltInput) error {
	return a.Store.MarkBuilt(ctx, in.DeploymentID, in.Image)
}

func (a *Activities) EnsureNetwork(ctx context.Context, projectID string) (string, error) {
	name := NetworkName(projectID)
	return name, a.Docker.EnsureNetwork(ctx, name)
}

// EnsureCredentials stores an encrypted password for a managed DB on first
// deploy. Mongo data created by the Node worker keeps its old password,
// because Mongo only applies MONGO_INITDB_* to an empty data directory.
func (a *Activities) EnsureCredentials(ctx context.Context, job store.Job) error {
	m, ok := Managed[job.ServiceType]
	if !ok {
		return nil
	}
	env, err := a.Store.EnvMap(ctx, job.ServiceID)
	if err != nil {
		return err
	}
	if env[m.PasswordEnv] != "" {
		return nil
	}
	password, err := secrets.RandomPassword()
	if err != nil {
		return err
	}
	if job.ServiceType == store.TypeMongoDB && a.hasExistingData(ctx, job) {
		password = legacyMongoPassword
	}
	enc, err := a.Secrets.Encrypt(password)
	if err != nil {
		return err
	}
	_, err = a.Store.UpsertEnvVar(ctx, job.ServiceID, m.PasswordEnv, enc)
	return err
}

// EnsureVolume returns the managed DB's named volume, creating it on first
// use and copying any Node-era data from the legacy host directory into it.
// The copy is idempotent (marker file), so retries and later deploys are safe.
func (a *Activities) EnsureVolume(ctx context.Context, job store.Job) (string, error) {
	name := VolumeName(job.ServiceID)
	if !a.Docker.VolumeExists(ctx, name) {
		if err := a.Docker.CreateVolume(ctx, name); err != nil {
			return "", err
		}
	}
	if dir := a.legacyDir(job); dirExists(dir) {
		if err := a.Docker.CopyIntoVolume(ctx, dir, name); err != nil {
			return "", err
		}
	}
	return name, nil
}

func (a *Activities) hasExistingData(ctx context.Context, job store.Job) bool {
	return a.Docker.VolumeExists(ctx, VolumeName(job.ServiceID)) || dirExists(a.legacyDir(job))
}

func (a *Activities) legacyDir(job store.Job) string {
	if job.VolumePath != nil && filepath.IsAbs(*job.VolumePath) {
		return *job.VolumePath
	}
	return filepath.Join(a.Cfg.LegacyVolumeRoot, job.ServiceID)
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// RetirePrevious removes containers of the service's other deployments and
// records them as stopped.
func (a *Activities) RetirePrevious(ctx context.Context, in RetireInput) error {
	old, err := a.Store.ListLiveDeployments(ctx, in.ServiceID, in.KeepDeploymentID)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(old))
	for _, d := range old {
		if err := a.Docker.Remove(ctx, ContainerName(d.ID)); err != nil {
			return err
		}
		ids = append(ids, d.ID)
	}
	return a.Store.MarkStopped(ctx, ids)
}

func (a *Activities) RunContainer(ctx context.Context, in RunInput) (RunResult, error) {
	job := in.Job
	env, err := a.decryptedEnv(ctx, job.ServiceID)
	if err != nil {
		return RunResult{}, err
	}
	spec := runtime.RunSpec{
		Name:    ContainerName(job.DeploymentID),
		Network: in.Network,
		Aliases: []string{ServiceAlias(job.ServiceID)},
		Labels: map[string]string{
			"cp.deploymentId": job.DeploymentID, "cp.serviceId": job.ServiceID, "cp.projectId": job.ProjectID,
		},
	}
	if m, ok := Managed[job.ServiceType]; ok {
		spec.Image, spec.ContainerPort, spec.Volume, spec.Cmd = m.Image, m.Port, in.Volume, m.Cmd
		spec.Env = merge(m.Env, env)
		if a.Cfg.ExposeManagedPorts {
			if spec.PublishPort, err = a.allocatePort(ctx); err != nil {
				return RunResult{}, err
			}
		}
	} else {
		if job.Image == nil {
			return RunResult{}, temporal.NewNonRetryableApplicationError("image_not_built_yet", "ImageMissing", nil)
		}
		spec.Image = *job.Image
		if err := a.Docker.Pull(ctx, spec.Image); err != nil {
			return RunResult{}, err
		}
		port, err := a.Docker.DetectContainerPort(ctx, spec.Image)
		if err != nil {
			port = a.Cfg.DefaultContainerPort
		}
		spec.ContainerPort = port
		spec.Env = merge(map[string]string{"NODE_ENV": "production"}, env)
	}

	id, err := a.Docker.Run(ctx, spec)
	if err != nil {
		return RunResult{}, err
	}
	res := RunResult{ContainerID: id, ContainerName: spec.Name, ContainerPort: spec.ContainerPort}
	if spec.PublishPort > 0 {
		p := spec.PublishPort
		res.HostPort = &p
	}
	return res, nil
}

// WaitHealthy polls a TCP probe until the container accepts connections or
// HealthTimeout passes.
func (a *Activities) WaitHealthy(ctx context.Context, in WaitInput) error {
	interval := a.Cfg.HealthInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(a.Cfg.HealthTimeout)
	for {
		activity.RecordHeartbeat(ctx, in.Host)
		err := a.Docker.Probe(ctx, in.Network, in.Host, in.Port)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s:%d not reachable after %s: %w", in.Host, in.Port, a.Cfg.HealthTimeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// SwitchRoute points <serviceId>.<domain> at the new container and returns the URL.
func (a *Activities) SwitchRoute(ctx context.Context, in RouteInput) (string, error) {
	if err := a.Router.UpsertRoute(ctx, in.ServiceID, in.ContainerName, in.Port); err != nil {
		return "", err
	}
	return a.Router.RuntimeURL(in.ServiceID), nil
}

func (a *Activities) MarkDeployed(ctx context.Context, in DeployedInput) error {
	err := a.Store.MarkDeployed(ctx, in.DeploymentID, store.DeployedInfo{
		ContainerID: in.ContainerID, HostPort: in.HostPort, ContainerPort: in.ContainerPort,
		RuntimeURL: in.RuntimeURL, VolumePath: in.VolumePath,
	})
	if err != nil {
		return err
	}
	if d, err := a.Store.GetDeployment(ctx, in.DeploymentID); err == nil {
		a.Metrics.DeploymentFinished("deployed", time.Since(d.CreatedAt.Time))
	}
	return nil
}

func (a *Activities) StartLogStream(_ context.Context, in StreamInput) error {
	a.Logs.Start(in.DeploymentID, in.ContainerID)
	return nil
}

func (a *Activities) MarkFailed(ctx context.Context, deploymentID string) error {
	if err := a.Store.SetDeploymentStatus(ctx, deploymentID, store.StatusFailed); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	a.Metrics.DeploymentFinished("failed", 0)
	return nil
}

func (a *Activities) RemoveContainer(ctx context.Context, name string) error {
	return a.Docker.Remove(ctx, name)
}

func (a *Activities) decryptedEnv(ctx context.Context, serviceID string) (map[string]string, error) {
	raw, err := a.Store.EnvMap(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		plain, err := a.Secrets.Decrypt(v)
		if err != nil {
			return nil, fmt.Errorf("env var %s: %w", k, err)
		}
		out[k] = plain
	}
	return out, nil
}

// allocatePort picks a host port used neither by a running container nor by
// a live deployment row. Only managed DBs in dev publish ports.
func (a *Activities) allocatePort(ctx context.Context) (int, error) {
	used := a.Docker.UsedPorts(ctx)
	dbPorts, err := a.Store.UsedHostPorts(ctx)
	if err != nil {
		return 0, err
	}
	for _, p := range dbPorts {
		used[p] = true
	}
	return runtime.PickFreePort(a.Cfg.PortStart, a.Cfg.PortEnd, used)
}

// keepAlive heartbeats every interval until the returned stop func is called,
// so long builds don't hit the heartbeat timeout.
func keepAlive(ctx context.Context, every time.Duration, details string) func() {
	activity.RecordHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				activity.RecordHeartbeat(ctx, details)
			}
		}
	}()
	return func() { close(done) }
}

// merge returns base overlaid with overrides, without modifying either.
func merge(base, overrides map[string]string) map[string]string {
	out := maps.Clone(base)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, overrides)
	return out
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race ./internal/workflows/`
Expected: `ok  infrapilot/internal/workflows`

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum internal/workflows
git commit -m "feat(go): deploy activities with isolation, credentials, health checks and retirement"
```

- [ ] **Step 8: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier. Security pays particular attention to the credential handling and the env file.

---

### Task 24: `DeployWorkflow`

> **CTO amendment, fix round 1 (`review/decisions.md` 2026-10-10 "Task 24 fix round 1"):** (1) `RunContainer` and `SwitchRoute` options get `WaitForCancellation: true`; `stage` gains `routeAttempted`, set immediately before `SwitchRoute`; if `routeAttempted && !routeSwitched`, cleanup runs new activity `RevertRoute{ServiceID, DeploymentID}` (repoint the route to the newest other deployed, not stopped deployment; none: remove the route) and removes the container only if it returned nil. (2) `MarkDeployed` runs in a disconnected context with `commitOptions` (StartToClose 1 min, retry 5 s x2 max 1 min, ScheduleToClose 30 min). (3) The deploying claim is the service-scoped `ClaimDeploy` (store transaction: lock the Service row `FOR UPDATE`; own `deploying` row wins; not queued loses; another `deploying` row of the service newer than 1 h is busy; busy is retryable `ServiceBusy` under `claimDeployOptions`: StartToClose 30 s, fixed 5 s retry, no attempt cap, ScheduleToClose 30 min). The building claim stays `Claim`. (4) Remove `SetHealthTimeout`; `const healthStartToClose = 11 * time.Minute`; `internal/config` `maxHealthTimeoutSeconds = 600`. (5) Build `ScheduleToCloseTimeout` 90 min. (6) Rename `TestDeployIsDeterministicAcrossRuns` to `TestDeployTraceIsStableAcrossRuns`; add a pinned `workflowcheck` step to `ci.yml`. (7) Cleanup logs carry deploymentId, serviceId and err; `TestRunContainerFailureMarksFailed` asserts the tail `["failed","remove:cp-d1"]`. Tests and the numbered list are in the decision entry.

> **CTO amendment (FWD-2, FWD-3, `review/decisions.md` 2026-10-10 "Task 23 fix round 1"):** `RunContainer` must not use `shortOptions`: give it its own options, `StartToClose` 10 min, `HeartbeatTimeout` 1 min, 3 attempts (it pulls an image and starts a container, and heartbeats via `keepAlive`). `healthOptions` keeps one attempt; its `StartToClose` is at least `HealthTimeout` + 60 s so a first busybox pull cannot time it out. `Claim` now returns true on a retry whose row is already in the target status; the workflow still treats `false` as "not ours, end silently".

> **CTO amendment (STAFF-4 T8, `review/decisions.md` 2026-10-08):** `deploy` returns `(stage, error)`, where `type stage struct{ routeSwitched, deployed bool }`. It sets `routeSwitched` after `SwitchRoute` succeeds (managed services never switch a route, so it stays false for them) and `deployed` after `MarkDeployed` succeeds. Post-deploy errors are wrapped as `fmt.Errorf("post-deploy %s: %w", step, err)`. `DeployWorkflow` on error: if `st.deployed`, log a warning (`workflow.GetLogger`) and return the error with no cleanup activities. Otherwise run `MarkFailed`, and run `RemoveContainer` only if `!st.routeSwitched`. Build gate: `if !managed && job.Image == nil { claim building, BuildImage, MarkBuilt }`; with an image already set, go straight to `claim(queued, deploying)` and run with `*job.Image`.
> Tests (trace-based, like the existing ones): `TestRetireFailureAfterDeployKeepsDeployment` (non-retryable `RetirePrevious` error → trace has `deployed`, has no `failed` and no `remove:cp-d1`, workflow error non-nil); `TestLogStreamFailureKeepsDeployment` (same for `StartLogStream`); `TestMarkDeployedFailureAfterSwitchKeepsContainer` (→ `failed` and no `remove:`); `TestHealthFailureRemovesContainer` (existing or kept: `failed` and `remove:cp-d1`, no `route`); `TestPrebuiltImageSkipsBuild` (nodejs job with `Image` set → no `BuildImage`, no building claim, `RunContainer` gets that image); managed DB failure after `RunContainer` and before `MarkDeployed` → `failed` and `remove:`.
> **CTO amendment, fix round 2 (`review/decisions.md` 2026-10-10 "Task 24 fix round 2"):** (1) On failure path 2 the cleanup order is `RevertRoute`, `RemoveContainer` (only if the revert succeeded), `MarkFailed` last, so the service lock is held until the route is settled; update the trace tests (`["revert","remove:cp-d1","failed"]`, failed revert `["revert","failed"]`). (2) After `MarkDeployed`, `RetirePrevious` and `StartLogStream` run in a disconnected context with `shortOptions()`; `TestMarkDeployedSurvivesCancellation` asserts `retire` and `logs`. (3) `ClaimDeploy` locks the Service row `FOR NO KEY UPDATE`.

> Review: the CTO checks these four paths in the Task 24 review (spec §13 already lists the CTO for this task).
>
> **CTO amendment (FWD-A2):** `BuildImage` is called with `BuildInput{DeploymentID: job.DeploymentID}`. Mocks change to `func(context.Context, BuildInput) (string, error)`, and the test `newJob` keeps `RepoURL` nil (as `LoadJob` now returns it). The other Task 24 changes are in the next entry.

**Files:**
- Create: `control-plane-go/internal/workflows/deploy.go`
- Test: `control-plane-go/internal/workflows/deploy_test.go`

Workflow code must be deterministic: no I/O, `time.Now`, randomness or goroutines. Everything with side effects goes through activities.

- [ ] **Step 1: Write the failing tests**

`internal/workflows/deploy_test.go`:
```go
package workflows

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/store"
)

type trace struct {
	mu    sync.Mutex
	steps []string
}

func (tr *trace) add(s string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.steps = append(tr.steps, s)
}

func (tr *trace) get() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Clone(tr.steps)
}

func strp(s string) *string { return &s }

func appJob() store.Job {
	return store.Job{DeploymentID: "d1", Status: store.StatusQueued, CommitSha: "main", ServiceID: "s1",
		ServiceType: store.TypeNodejs, RepoURL: strp("https://x/y.git"), Branch: "main", ProjectID: "p1"}
}

// mockAll registers every activity, recording its name in tr. Individual tests
// override single activities by registering their own mock first.
func mockAll(env *testsuite.TestWorkflowEnvironment, tr *trace, job store.Job) {
	var a *Activities
	env.OnActivity(a.LoadJob, mock.Anything, mock.Anything).Return(job, nil).Maybe()
	env.OnActivity(a.Claim, mock.Anything, mock.Anything).Return(func(_ context.Context, in ClaimInput) (bool, error) {
		tr.add("claim:" + in.To)
		return true, nil
	}).Maybe()
	env.OnActivity(a.BuildImage, mock.Anything, mock.Anything).Return(func(context.Context, store.Job) (string, error) {
		tr.add("build")
		return "reg/s1:main", nil
	}).Maybe()
	env.OnActivity(a.MarkBuilt, mock.Anything, mock.Anything).Return(func(context.Context, MarkBuiltInput) error { tr.add("built"); return nil }).Maybe()
	env.OnActivity(a.EnsureNetwork, mock.Anything, mock.Anything).Return(func(_ context.Context, p string) (string, error) {
		tr.add("network")
		return "cp-proj-" + p, nil
	}).Maybe()
	env.OnActivity(a.EnsureCredentials, mock.Anything, mock.Anything).Return(func(context.Context, store.Job) error { tr.add("credentials"); return nil }).Maybe()
	env.OnActivity(a.EnsureVolume, mock.Anything, mock.Anything).Return(func(context.Context, store.Job) (string, error) {
		tr.add("volume")
		return "cp-vol-s1", nil
	}).Maybe()
	env.OnActivity(a.RetirePrevious, mock.Anything, mock.Anything).Return(func(context.Context, RetireInput) error { tr.add("retire"); return nil }).Maybe()
	env.OnActivity(a.RunContainer, mock.Anything, mock.Anything).Return(func(_ context.Context, in RunInput) (RunResult, error) {
		tr.add("run")
		return RunResult{ContainerID: "c1", ContainerName: "cp-" + in.Job.DeploymentID, ContainerPort: 3000}, nil
	}).Maybe()
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(func(context.Context, WaitInput) error { tr.add("healthy"); return nil }).Maybe()
	env.OnActivity(a.SwitchRoute, mock.Anything, mock.Anything).Return(func(context.Context, RouteInput) (string, error) {
		tr.add("route")
		return "http://s1.localhost", nil
	}).Maybe()
	env.OnActivity(a.MarkDeployed, mock.Anything, mock.Anything).Return(func(context.Context, DeployedInput) error { tr.add("deployed"); return nil }).Maybe()
	env.OnActivity(a.StartLogStream, mock.Anything, mock.Anything).Return(func(context.Context, StreamInput) error { tr.add("logs"); return nil }).Maybe()
	env.OnActivity(a.MarkFailed, mock.Anything, mock.Anything).Return(func(context.Context, string) error { tr.add("failed"); return nil }).Maybe()
	env.OnActivity(a.RemoveContainer, mock.Anything, mock.Anything).Return(func(_ context.Context, n string) error { tr.add("remove:" + n); return nil }).Maybe()
}

func newWorkflowEnv() *testsuite.TestWorkflowEnvironment {
	var s testsuite.WorkflowTestSuite
	env := s.NewTestWorkflowEnvironment()
	env.RegisterActivity(&Activities{})
	return env
}

func TestDeployBuiltService(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("err = %v", env.GetWorkflowError())
	}
	want := []string{"claim:building", "build", "built", "claim:deploying", "network", "run", "healthy", "route", "deployed", "retire", "logs"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n got %v\nwant %v", got, want)
	}
}

func TestDeployManagedStopsPreviousBeforeStart(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	job := appJob()
	job.ServiceType, job.RepoURL = store.TypeRedis, nil
	mockAll(env, tr, job)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil {
		t.Fatal(env.GetWorkflowError())
	}
	want := []string{"claim:deploying", "network", "credentials", "volume", "retire", "run", "healthy", "deployed", "logs"}
	if got := tr.get(); !slices.Equal(got, want) {
		t.Fatalf("steps:\n got %v\nwant %v", got, want)
	}
}

func TestDeployHealthFailureMarksFailedAndRemovesNewContainer(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.WaitHealthy, mock.Anything, mock.Anything).Return(errors.New("not reachable")).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if err := env.GetWorkflowError(); err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
	got := tr.get()
	if slices.Contains(got, "route") || slices.Contains(got, "retire") || slices.Contains(got, "deployed") {
		t.Fatalf("traffic must not switch after a failed health check: %v", got)
	}
	if !slices.Equal(got[len(got)-2:], []string{"failed", "remove:cp-d1"}) {
		t.Fatalf("cleanup steps: %v", got)
	}
	env.AssertExpectations(t)
}

func TestDeployMissingRepoIsNotRetried(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.BuildImage, mock.Anything, mock.Anything).
		Return("", temporal.NewNonRetryableApplicationError("repoUrl_required_for_build", "MissingRepo", nil)).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() == nil || !slices.Contains(tr.get(), "failed") {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
	env.AssertExpectations(t)
}

func TestDeploySkipsWhenNotQueued(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	job := appJob()
	job.Status = store.StatusDeployed
	mockAll(env, tr, job)
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil || len(tr.get()) != 0 {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}

func TestDeployLostClaimIsANoop(t *testing.T) {
	env, tr := newWorkflowEnv(), &trace{}
	var a *Activities
	env.OnActivity(a.Claim, mock.Anything, mock.Anything).Return(false, nil).Once()
	mockAll(env, tr, appJob())
	env.ExecuteWorkflow(DeployWorkflow, "d1")

	if env.GetWorkflowError() != nil || len(tr.get()) != 0 {
		t.Fatalf("err=%v steps=%v", env.GetWorkflowError(), tr.get())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go get github.com/stretchr/testify && go test ./internal/workflows/ -run Deploy`
Expected: FAIL with `undefined: DeployWorkflow`.

- [ ] **Step 3: Implement**

`internal/workflows/deploy.go`:
```go
package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"infrapilot/internal/store"
)

// retry matches the Node worker: 1 attempt + 5 retries with backoff.
var retry = &temporal.RetryPolicy{
	InitialInterval:    10 * time.Second,
	BackoffCoefficient: 2,
	MaximumInterval:    5 * time.Minute,
	MaximumAttempts:    6,
}

func shortOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: 2 * time.Minute, RetryPolicy: retry}
}

func buildOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{StartToCloseTimeout: 30 * time.Minute, HeartbeatTimeout: time.Minute, RetryPolicy: retry}
}

// healthOptions allows one attempt: an unhealthy container fails the deploy
// and the previous deployment keeps serving. HEALTH_TIMEOUT_SECONDS must stay
// below the 3-minute limit.
func healthOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute, HeartbeatTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1},
	}
}

// DeployWorkflow builds (if needed) and runs one deployment. Its ID is
// DeployWorkflowID(deploymentID), so starting it twice is harmless.
func DeployWorkflow(ctx workflow.Context, deploymentID string) error {
	var a *Activities
	short := workflow.WithActivityOptions(ctx, shortOptions())

	var job store.Job
	if err := workflow.ExecuteActivity(short, a.LoadJob, deploymentID).Get(ctx, &job); err != nil {
		return err
	}
	if job.Status != store.StatusQueued {
		return nil // already handled by an earlier run or by the Node worker
	}

	err := deploy(ctx, job)
	if err != nil {
		// Record the failure and remove the half-started container even if
		// the workflow is being cancelled.
		cleanup, _ := workflow.NewDisconnectedContext(ctx)
		cleanup = workflow.WithActivityOptions(cleanup, shortOptions())
		_ = workflow.ExecuteActivity(cleanup, a.MarkFailed, deploymentID).Get(cleanup, nil)
		_ = workflow.ExecuteActivity(cleanup, a.RemoveContainer, ContainerName(deploymentID)).Get(cleanup, nil)
	}
	return err
}

func deploy(ctx workflow.Context, job store.Job) error {
	var a *Activities
	short := workflow.WithActivityOptions(ctx, shortOptions())
	managed := store.IsManaged(job.ServiceType)

	claim := func(from, to string) (bool, error) {
		var won bool
		err := workflow.ExecuteActivity(short, a.Claim, ClaimInput{DeploymentID: job.DeploymentID, From: from, To: to}).Get(ctx, &won)
		return won, err
	}

	if !managed {
		if won, err := claim(store.StatusQueued, store.StatusBuilding); err != nil || !won {
			return err
		}
		var image string
		if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, buildOptions()), a.BuildImage, job).Get(ctx, &image); err != nil {
			return err
		}
		if err := workflow.ExecuteActivity(short, a.MarkBuilt, MarkBuiltInput{DeploymentID: job.DeploymentID, Image: image}).Get(ctx, nil); err != nil {
			return err
		}
		job.Image = &image
	}
	if won, err := claim(store.StatusQueued, store.StatusDeploying); err != nil || !won {
		return err
	}

	var network string
	if err := workflow.ExecuteActivity(short, a.EnsureNetwork, job.ProjectID).Get(ctx, &network); err != nil {
		return err
	}
	var volume string
	if managed {
		if err := workflow.ExecuteActivity(short, a.EnsureCredentials, job).Get(ctx, nil); err != nil {
			return err
		}
		if err := workflow.ExecuteActivity(short, a.EnsureVolume, job).Get(ctx, &volume); err != nil {
			return err
		}
		// Two database processes must never share a data directory.
		retire := RetireInput{ServiceID: job.ServiceID, KeepDeploymentID: job.DeploymentID}
		if err := workflow.ExecuteActivity(short, a.RetirePrevious, retire).Get(ctx, nil); err != nil {
			return err
		}
	}

	var run RunResult
	if err := workflow.ExecuteActivity(short, a.RunContainer, RunInput{Job: job, Network: network, Volume: volume}).Get(ctx, &run); err != nil {
		return err
	}
	wait := WaitInput{Network: network, Host: run.ContainerName, Port: run.ContainerPort}
	if err := workflow.ExecuteActivity(workflow.WithActivityOptions(ctx, healthOptions()), a.WaitHealthy, wait).Get(ctx, nil); err != nil {
		return err
	}

	var runtimeURL *string
	if !managed {
		var url string
		route := RouteInput{ServiceID: job.ServiceID, ContainerName: run.ContainerName, Port: run.ContainerPort}
		if err := workflow.ExecuteActivity(short, a.SwitchRoute, route).Get(ctx, &url); err != nil {
			return err
		}
		runtimeURL = &url
	}
	var volumePath *string
	if volume != "" {
		volumePath = &volume
	}
	deployed := DeployedInput{
		DeploymentID: job.DeploymentID, ContainerID: run.ContainerID, HostPort: run.HostPort,
		ContainerPort: run.ContainerPort, RuntimeURL: runtimeURL, VolumePath: volumePath,
	}
	if err := workflow.ExecuteActivity(short, a.MarkDeployed, deployed).Get(ctx, nil); err != nil {
		return err
	}
	if !managed {
		retire := RetireInput{ServiceID: job.ServiceID, KeepDeploymentID: job.DeploymentID}
		if err := workflow.ExecuteActivity(short, a.RetirePrevious, retire).Get(ctx, nil); err != nil {
			return err
		}
	}
	stream := StreamInput{DeploymentID: job.DeploymentID, ContainerID: run.ContainerID}
	return workflow.ExecuteActivity(short, a.StartLogStream, stream).Get(ctx, nil)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test -race ./internal/workflows/`
Expected: `ok  infrapilot/internal/workflows`.

If the testsuite rejects the function-valued `Return(...)` mocks for your SDK version, switch the affected mocks to plain values plus `.Run(...)` and keep the same assertions. Record the change in the task record.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/workflows
git commit -m "feat(go): DeployWorkflow with health-gated route switch and cleanup on failure"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier. The CTO also reviews this task, because it defines the workflow architecture (spec §13: architecture changes).

---

### Task 25: Outbox → Temporal starter

> **CTO amendment (T24 fix round 1, `review/decisions.md` 2026-10-10):** (1) Treat `WorkflowExecutionAlreadyStarted` as success (the event is marked published). The per-service serialisation lives in the workflow (`ClaimDeploy`), not in the starter; do not add a service-level check here. (2) Authorization: the workflow does no ownership check, so the starter starts workflows only from outbox rows written by the ownership-checked API and never from request input; document it in `starter.go`. (3) The Temporal address is the only way to start workflows: tenants must never reach it.

**Files:**
- Create: `control-plane-go/internal/worker/outbox/publisher.go`, `control-plane-go/internal/workflows/starter.go`
- Test: `control-plane-go/internal/worker/outbox/publisher_test.go`, `control-plane-go/internal/workflows/starter_test.go`

The outbox keeps its guarantees: claim with `FOR UPDATE SKIP LOCKED`, backoff, and `dead` after 10 attempts. Each event now starts a workflow instead of publishing to RabbitMQ. The workflow ID is derived from the deployment, so re-sending an event is harmless.

- [ ] **Step 1: Write the failing tests**

`internal/worker/outbox/publisher_test.go`:
```go
package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"infrapilot/internal/store"
	"infrapilot/internal/testutil"
	"infrapilot/internal/worker/outbox"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) { testutil.RunWithPostgres(m, &pool) }

type started struct{ Type, Payload string }

type fakeStarter struct {
	mu   sync.Mutex
	got  []started
	fail error
}

func (f *fakeStarter) Start(_ context.Context, eventType string, payload []byte) error {
	if f.fail != nil {
		return f.fail
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, started{eventType, string(payload)})
	return nil
}

func (f *fakeStarter) all() []started {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]started(nil), f.got...)
}

func zero(int) int { return 0 }

func TestBackoffSeconds(t *testing.T) {
	want := map[int]int{1: 2, 2: 4, 3: 8, 4: 16, 5: 32, 6: 60, 7: 60, 9: 60}
	for attempt, w := range want {
		if got := outbox.BackoffSeconds(attempt, zero); got != w {
			t.Errorf("BackoffSeconds(%d) = %d, want %d", attempt, got, w)
		}
	}
}

func status(t *testing.T, id string) (string, int) {
	t.Helper()
	var s string
	var attempts int
	if err := pool.QueryRow(context.Background(),
		`SELECT "status"::text, "attempts" FROM "OutboxEvent" WHERE "id" = $1`, id).Scan(&s, &attempts); err != nil {
		t.Fatal(err)
	}
	return s, attempts
}

func setup(t *testing.T) (*store.Store, *fakeStarter, *outbox.Publisher) {
	st := store.New(testutil.NeedDB(t, pool))
	fs := &fakeStarter{}
	return st, fs, outbox.New(st, fs, slog.New(slog.DiscardHandler))
}

func TestTickStartsPendingEvents(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id, _ := st.CreateOutboxEvent(ctx, store.EventBuildRequested, map[string]string{"deploymentId": "d1"})

	n, err := p.Tick(ctx)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := fs.all()
	if len(got) != 1 || got[0].Type != store.EventBuildRequested || got[0].Payload != `{"deploymentId":"d1"}` {
		t.Fatalf("started %+v", got)
	}
	if s, _ := status(t, id); s != "published" {
		t.Fatalf("status = %s", s)
	}
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatalf("second tick processed %d", n)
	}
}

func TestTickRetriesThenDies(t *testing.T) {
	st, fs, p := setup(t)
	ctx := context.Background()
	id, _ := st.CreateOutboxEvent(ctx, store.EventDeployRequested, map[string]string{"deploymentId": "d1"})
	fs.fail = errors.New("temporal unavailable")

	p.Tick(ctx)
	if s, attempts := status(t, id); s != "pending" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
	if n, _ := p.Tick(ctx); n != 0 {
		t.Fatal("event must wait for its backoff")
	}

	pool.Exec(ctx, `UPDATE "OutboxEvent" SET "attempts" = $2, "nextAttemptAt" = now() WHERE "id" = $1`, id, outbox.MaxAttempts-1)
	p.Tick(ctx)
	if s, attempts := status(t, id); s != "dead" || attempts != outbox.MaxAttempts {
		t.Fatalf("status=%s attempts=%d", s, attempts)
	}
}

func TestConcurrentPublishersStartEachEventOnce(t *testing.T) {
	st, fs, _ := setup(t)
	ctx := context.Background()
	for i := range 60 {
		st.CreateOutboxEvent(ctx, store.EventBuildRequested, map[string]int{"n": i})
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := outbox.New(st, fs, slog.New(slog.DiscardHandler))
			for range 5 {
				if _, err := p.Tick(ctx); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, s := range fs.all() {
		if seen[s.Payload] {
			t.Fatalf("event %s started twice", s.Payload)
		}
		seen[s.Payload] = true
	}
	if len(seen) != 60 {
		t.Fatalf("started %d distinct events, want 60", len(seen))
	}
}
```

`internal/workflows/starter_test.go`:
```go
package workflows

import (
	"context"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/testsuite"

	"infrapilot/internal/store"
)

func TestStarterIsIdempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: downloads and runs a Temporal dev server")
	}
	ctx := context.Background()
	srv, err := testsuite.StartDevServer(ctx, testsuite.DevServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	c := srv.Client()
	s := Starter{Client: c}

	payload := []byte(`{"deploymentId":"dep-1"}`)
	if err := s.Start(ctx, store.EventBuildRequested, payload); err != nil {
		t.Fatal(err)
	}
	// A second event for the same deployment must not fail or start a second run.
	if err := s.Start(ctx, store.EventDeployRequested, payload); err != nil {
		t.Fatal(err)
	}
	desc, err := c.DescribeWorkflowExecution(ctx, DeployWorkflowID("dep-1"), "")
	if err != nil {
		t.Fatal(err)
	}
	info := desc.GetWorkflowExecutionInfo()
	if info.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING || info.GetTaskQueue() != TaskQueue {
		t.Fatalf("status=%v queue=%s", info.GetStatus(), info.GetTaskQueue())
	}

	if err := s.Start(ctx, "teardown.requested", payload); err == nil {
		t.Fatal("unknown event types must fail (the outbox retries them, then marks them dead)")
	}
	if err := s.Start(ctx, store.EventBuildRequested, []byte(`{}`)); err == nil {
		t.Fatal("payload without deploymentId must fail")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/worker/outbox/ ./internal/workflows/ -run 'Backoff|Tick|Concurrent|Starter'`
Expected: FAIL with `undefined: outbox.New` and `undefined: Starter`.

- [ ] **Step 3: Implement the publisher**

`internal/worker/outbox/publisher.go`:
```go
// Package outbox turns OutboxEvent rows into Temporal workflow starts
// (at least once; workflow IDs make repeats harmless).
package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math/rand/v2"
	"time"

	"infrapilot/internal/store"
)

const (
	BatchSize   = 25
	MaxAttempts = 10
)

// Starter starts the work an event describes. workflows.Starter implements it.
type Starter interface {
	Start(ctx context.Context, eventType string, payload []byte) error
}

// BackoffSeconds is 2^attempt capped at 60, plus 0–4s of jitter.
func BackoffSeconds(attempt int, jitter func(n int) int) int {
	return min(60, 1<<min(attempt, 6)) + jitter(5)
}

type Publisher struct {
	store   *store.Store
	starter Starter
	log     *slog.Logger
	jitter  func(int) int
}

func New(st *store.Store, starter Starter, log *slog.Logger) *Publisher {
	return &Publisher{store: st, starter: starter, log: log, jitter: rand.IntN}
}

// Run calls Tick every second until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	p.log.Info("outbox starter running")
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := p.Tick(ctx); err != nil {
				p.log.Error("outbox tick failed", "err", err)
			}
		}
	}
}

// Tick claims one batch, starts each event and records the result, all in
// one transaction. It returns how many events were claimed.
func (p *Publisher) Tick(ctx context.Context) (int, error) {
	claimed := 0
	err := p.store.InTx(ctx, func(tx *store.Store) error {
		events, err := tx.ClaimOutboxBatch(ctx, BatchSize)
		if err != nil {
			return err
		}
		claimed = len(events)
		for _, e := range events {
			startErr := p.starter.Start(ctx, e.Type, compactJSON(e.Payload))
			if startErr == nil {
				p.log.Info("outbox event started", "type", e.Type, "eventId", e.ID)
				if err := tx.MarkOutboxPublished(ctx, e.ID); err != nil {
					return err
				}
				continue
			}
			attempts := e.Attempts + 1
			p.log.Warn("outbox start failed", "eventId", e.ID, "attempts", attempts, "err", startErr)
			if attempts >= MaxAttempts {
				err = tx.MarkOutboxDead(ctx, e.ID, attempts, startErr.Error())
			} else {
				err = tx.MarkOutboxRetry(ctx, e.ID, attempts, startErr.Error(), BackoffSeconds(attempts, p.jitter))
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
	return claimed, err
}

// compactJSON turns Postgres' jsonb text ({"a": 1}) into compact JSON ({"a":1}).
func compactJSON(raw []byte) []byte {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return raw
	}
	return buf.Bytes()
}
```

- [ ] **Step 4: Implement the Temporal starter**

`internal/workflows/starter.go`:
```go
package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"infrapilot/internal/store"
)

// Starter starts workflows for outbox events. Both Node-era event types
// start the same DeployWorkflow; the workflow decides whether to build.
type Starter struct {
	Client client.Client
}

func (s Starter) Start(ctx context.Context, eventType string, payload []byte) error {
	switch eventType {
	case store.EventBuildRequested, store.EventDeployRequested:
		var msg struct {
			DeploymentID string `json:"deploymentId"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil || msg.DeploymentID == "" {
			return fmt.Errorf("outbox %s: payload has no deploymentId", eventType)
		}
		_, err := s.Client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
			ID:                    DeployWorkflowID(msg.DeploymentID),
			TaskQueue:             TaskQueue,
			WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
		}, DeployWorkflow, msg.DeploymentID)
		return ignoreAlreadyStarted(err)
	}
	return fmt.Errorf("outbox: unknown event type %q", eventType)
}

// ignoreAlreadyStarted treats "this deployment's workflow already exists" as success.
func ignoreAlreadyStarted(err error) error {
	var already *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &already) {
		return nil
	}
	return err
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go mod tidy && go test -race ./internal/worker/outbox/ ./internal/workflows/`
Expected: two `ok` lines. The first run downloads the Temporal CLI for the dev server.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/worker/outbox internal/workflows
git commit -m "feat(go): outbox starts Temporal workflows idempotently"
```

- [ ] **Step 7: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 26: Temporal in docker-compose and the worker binary

> **CTO amendment (T24 fix round 1, `review/decisions.md` 2026-10-10):** do NOT call `workflows.SetHealthTimeout` (removed; the health activity timeout is the constant 11 min and config rejects `HEALTH_TIMEOUT_SECONDS` > 600). Publish the Temporal port (7233) on 127.0.0.1 only in compose. Smoke test against a real Temporal server (dev server or the compose service): start the worker, run one managed Redis deployment end to end, then two deployments of one service back to back and confirm they run one after the other.

> **CTO amendment (FWD-3, FWD-4, `review/decisions.md` 2026-10-10 "Task 23 fix round 1"):** at startup pull the probe image (`docker pull busybox:1.36`, log a warning on failure, do not exit). Build `Activities` with every field non-nil: `Logs`, `Metrics` and `Log` get real values (a nil-field test fails construction), and `HealthInterval`/`HealthTimeout` come from config (defaults 2 s / 60 s).

> **CTO amendment (T22 fix round 1, `review/decisions.md` 2026-10-10):** at startup run `git --version`, log it, and refuse to start (`log.Fatal`) when the parsed version is below 2.30 (spec §14); a test for the parser with `git version 2.39.5 (Apple Git-154)`, `2.43.0` and `2.29.9`.

> **CTO amendment (STAFF-5 of Task 2, `review/decisions.md` 2026-10-07):** the startup log line includes the absolute nginx routes dir and the redacted config, see the `worker up` line below.

> **CTO amendment (FWD-A2, `review/decisions.md` 2026-10-08):** before the worker starts, `os.Setenv("GIT_TERMINAL_PROMPT", "0")`, so a private repo without credentials fails fast instead of hanging until the 30-minute build timeout. One line plus a startup-test assertion.

> **CTO amendment (STAFF-1/STAFF-4 T15, `review/decisions.md` 2026-10-09):** replace the "outbox metrics" loop body with:
> ```go
> every(ctx, outboxMetricInterval, func() {
> 	if counts, err := st.CountOutboxByStatus(ctx); err != nil {
> 		logger.Warn("outbox metrics: count failed", "err", err)
> 	} else {
> 		m.SetOutbox(counts)
> 	}
> 	if age, err := st.OldestUnpublishedOutboxAge(ctx); err != nil {
> 		logger.Warn("outbox metrics: lag failed", "err", err)
> 	} else {
> 		m.SetOutboxLag(age)
> 	}
> })
> ```
> When a query fails, the gauge keeps its last value; this is acceptable because the warning is logged every 15 s. There is no Temporal `MetricsHandler` in Plan 1, so `client.Options` stays as written. Smoke step: `curl -s 127.0.0.1:9091/metrics | grep cp_outbox_oldest_pending_age_seconds` returns one line.

**Files:**
- Modify: `control-plane/docker-compose.yml` (adds the `temporal` service; this is infrastructure, not Node code)
- Create: `control-plane-go/cmd/worker/main.go`

- [ ] **Step 1: Add the Temporal dev server to compose**

In `control-plane/docker-compose.yml`, replace:
```yaml
      - clickhouse_data:/var/lib/clickhouse

networks:
```
with:
```yaml
      - clickhouse_data:/var/lib/clickhouse

  # Temporal dev server (SQLite). Production uses Temporal on Postgres (Plan 3).
  temporal:
    image: temporalio/temporal:latest
    container_name: cp_temporal
    user: "0:0" # the named volume is root-owned; the dev server writes SQLite there
    command: ["server", "start-dev", "--ip", "0.0.0.0", "--db-filename", "/data/temporal.db"]
    ports:
      - "127.0.0.1:7233:7233" # gRPC
      - "127.0.0.1:8233:8233" # Web UI
    volumes:
      - temporal_data:/data

networks:
```
and replace the last volume line:
```yaml
  clickhouse_data:
```
with:
```yaml
  clickhouse_data:
  temporal_data:
```

Then:
```bash
cd ../control-plane && docker compose up -d temporal && cd ../control-plane-go
until curl -sf http://localhost:8233 >/dev/null; do sleep 2; done; echo TEMPORAL_UP
```
Expected: `TEMPORAL_UP`, and the Temporal UI opens at http://localhost:8233.

Record in `docs/superpowers/review/backlog.md`: "pin `temporalio/temporal` to a digest once Plan 1 passes" (low).

- [ ] **Step 2: Implement the worker**

`cmd/worker/main.go`:
```go
// Command worker runs the Temporal deploy worker, the outbox starter, the log
// flush loop and the metrics endpoint.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"infrapilot/internal/config"
	"infrapilot/internal/db"
	"infrapilot/internal/logging"
	"infrapilot/internal/logs"
	"infrapilot/internal/metrics"
	"infrapilot/internal/runtime"
	"infrapilot/internal/secrets"
	"infrapilot/internal/store"
	"infrapilot/internal/worker/build"
	"infrapilot/internal/worker/outbox"
	"infrapilot/internal/workflows"
)

const (
	flushInterval        = 5 * time.Minute
	outboxMetricInterval = 15 * time.Second
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.LogFormat, os.Stdout)
	if err := run(cfg, logger); err != nil {
		logger.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, logger *slog.Logger) error {
	if err := cfg.RequireWorker(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)

	box, err := secrets.New(cfg.EnvEncryptionKey)
	if err != nil {
		return err
	}

	rdb, err := logs.NewRedis(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer rdb.Close()
	ch, err := logs.NewClickHouse(cfg.ClickHouseURL, cfg.ClickHouseUser, cfg.ClickHousePassword)
	if err != nil {
		return err
	}
	defer ch.Close()
	if err := ch.Init(ctx); err != nil {
		return err
	}
	agg := &logs.Aggregator{Hot: rdb, Warm: ch, Now: time.Now, Logger: logger} // Logger: Task 14 STAFF-1 fix round (per-stream flush errors)

	tc, err := client.Dial(client.Options{
		HostPort: cfg.TemporalHostPort, Namespace: cfg.TemporalNamespace,
		Logger: tlog.NewStructuredLogger(logger),
	})
	if err != nil {
		return err
	}
	defer tc.Close()

	m := metrics.NewWorker()
	runner := runtime.ExecRunner{}
	docker := runtime.Docker{
		R:          runner,
		Limits:     runtime.Limits{Memory: cfg.ContainerMemory, CPUs: cfg.ContainerCPUs, Pids: cfg.ContainerPids},
		Nginx:      cfg.NginxContainer,
		ProbeImage: cfg.ProbeImage,
	}
	streamer := runtime.NewStreamer(ctx, rdb, logger)

	acts := &workflows.Activities{
		Store:   st,
		Secrets: box,
		Docker:  docker,
		Router: runtime.Nginx{R: runner, RoutesDir: cfg.NginxRoutesDir, Container: cfg.NginxContainer,
			BaseDomain: cfg.ServiceBaseDomain, Port: cfg.NginxPort},
		Builder: build.Builder{R: runner, Name: cfg.BuilderName, Memory: cfg.BuilderMemory, CPUs: cfg.BuilderCPUs, Log: logger},
		Logs:    streamer,
		Metrics: m,
		Log:     logger,
		Cfg: workflows.ActivityConfig{
			RegistryHost: cfg.RegistryHost, BuildWorkdir: cfg.BuildWorkdir,
			DefaultContainerPort: cfg.DefaultContainerPort,
			PortStart:            cfg.PortRangeStart, PortEnd: cfg.PortRangeEnd,
			ExposeManagedPorts: cfg.ExposeManagedPorts, LegacyVolumeRoot: cfg.LegacyVolumeRoot,
			HealthTimeout: cfg.HealthTimeout,
		},
	}

	w := worker.New(tc, workflows.TaskQueue, worker.Options{})
	w.RegisterWorkflow(workflows.DeployWorkflow)
	w.RegisterActivity(acts)
	if err := w.Start(); err != nil {
		return err
	}
	defer w.Stop()

	if running, err := st.ListRunningDeployments(ctx); err != nil {
		logger.Warn("resume: cannot list running deployments", "err", err)
	} else {
		deps := make([]runtime.Running, 0, len(running))
		for _, d := range running {
			deps = append(deps, runtime.Running{DeploymentID: d.ID, ContainerID: *d.ContainerID})
		}
		logger.Info("resumed log streams", "count", streamer.Resume(ctx, deps, docker.IsRunning))
	}

	publisher := outbox.New(st, workflows.Starter{Client: tc}, logger)
	var wg sync.WaitGroup
	start := func(name string, fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				logger.Error(name+" stopped", "err", err)
				stop() // one broken loop stops the process so a supervisor restarts it
			}
		}()
	}
	start("outbox starter", func() error { publisher.Run(ctx); return nil })
	start("log flush", func() error { every(ctx, flushInterval, func() { flush(ctx, agg, logger) }); return nil })
	start("outbox metrics", func() error {
		every(ctx, outboxMetricInterval, func() {
			if counts, err := st.CountOutboxByStatus(ctx); err == nil {
				m.SetOutbox(counts)
			}
		})
		return nil
	})
	start("metrics server", func() error { return metrics.Serve(ctx, cfg.WorkerMetricsAddr, m.Handler(), logger) })

	routesAbs, err := filepath.Abs(cfg.NginxRoutesDir)
	if err != nil { // only fails if the working directory is unreadable
		routesAbs = cfg.NginxRoutesDir
	}
	logger.Info("worker up", "taskQueue", workflows.TaskQueue, "temporal", cfg.TemporalHostPort, "routesDir", routesAbs, "config", cfg)
	<-ctx.Done()
	logger.Info("worker shutting down")
	wg.Wait()
	return nil
}

func flush(ctx context.Context, agg *logs.Aggregator, logger *slog.Logger) {
	processed, errs := agg.Flush(ctx)
	logger.Info("flushed logs", "processed", processed, "errors", errs)
}

// every runs fn on each tick until ctx ends.
func every(ctx context.Context, d time.Duration, fn func()) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn()
		}
	}
}
```

- [ ] **Step 3: Build, vet and test everything**

Run: `go mod tidy && go build ./... && go vet ./... && golangci-lint run && go test -race ./...`
Expected: no build, vet or lint output, and every package `ok`.

- [ ] **Step 4: Smoke-run the worker**

With compose up (including `temporal`) and the Node **worker stopped**, run:
`go run ./cmd/worker`
Expected log lines: `resumed log streams count=…`, `outbox starter running`, `metrics listening addr=127.0.0.1:9091`, `worker up taskQueue=deployments routesDir=<absolute path> config.appEnv=… config.jwtSecretSet=true config.envEncryptionKeySet=true config.databaseURL=postgresql://cp:xxxxx@…` (never secret values). In the Temporal UI, under Workers, the `deployments` task queue shows one poller. Then:
```bash
curl -s 127.0.0.1:9091/metrics | grep -E '^cp_outbox_events'
```
Expected: four `cp_outbox_events{status=…}` lines. Stop the worker with Ctrl-C.

- [ ] **Step 5: Commit**

```bash
cd .. && git add control-plane/docker-compose.yml control-plane-go/cmd control-plane-go/go.mod control-plane-go/go.sum && cd control-plane-go
git commit -m "feat(go): worker binary and Temporal dev server in compose"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier. The Verifier runs one full real deploy here (see Task 27, checklist items 3–4).

---

### Task 27: Plan 1 gate and worker cutover

> **CTO amendment (T24 fix round 1, `review/decisions.md` 2026-10-10):** the e2e runs on a real Temporal server and covers what the Verifier could not run in Task 24: kill the worker mid-deploy and resume (no duplicate container, row ends `deployed`); cancel during RunContainer and during SwitchRoute (no leaked container, serving container never removed); two deployments of one service queued together (one `ServiceBusy` wait, both end `deployed` in turn, exactly one container left running, route points at it); record a real DeployWorkflow history to `internal/workflows/testdata/deploy_history.json` and replay it with `ReplayWorkflowHistoryFromJSONFile` as a test. Cutover checklist: the Node worker is stopped, and no Node row is left in `deploying`.

> **CTO amendment, Step 1 (FWD-A1, `review/decisions.md` 2026-10-08)** (Product and Security inputs; read-only): `SELECT "id", "serviceType", split_part("repoUrl", ':', 1) AS scheme FROM "Service" WHERE "repoUrl" IS NOT NULL AND "repoUrl" !~ '^https://';` and `SELECT count(*) FROM "Deployment" WHERE "commitSha" !~ '^[A-Za-z0-9][A-Za-z0-9._-]{3,99}$' AND "status" = 'queued';`. Never print full URLs. Builds for those services will fail with `invalid_build_source` after cutover, and Product names this in the release note.

**Files:**
- Modify: `docs/superpowers/review/decisions.md` (the CTO's go/no-go)

This task changes the running system. The cutover steps (Step 3 onward) start only after the **user** approves.

- [ ] **Step 1: Plan-end reviews (spec §13)**

The orchestrator dispatches, on the full range `master..go-migration`:
- **Security:** whole-diff review, plus `govulncheck`, `gosec`, and `gitleaks` over the range;
- **Staff:** whole-diff review for consistency across tasks;
- **Product:** this plan changes no UI, so Product checks the user-visible effects in the running UI. Managed DB connection info now shows ciphertext for the generated password until Plan 2, and the dev-only ports are now bound to `127.0.0.1`. Product drafts the release note;
- **CTO:** go/no-go, appended to `docs/superpowers/review/decisions.md`.

Open findings follow the usual fix loop.

- [ ] **Step 2: Ask the user to approve the cutover**

Report the gate results, the checklist below and the rollback steps. Wait for explicit approval.

- [ ] **Step 3: Cut over**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane && docker compose up -d && cd ../control-plane-go
# CTO 2026-10-08: if this database was not created by goose and was not baselined in Task 3
# (any Node-era DB other than the dev DB), first take `pg_dump -Fc` and run
# `go run ./cmd/migrate baseline`; `migrate status` must list 00001 as applied before `up`.
# `migrate up` refuses an un-baselined Node-era schema with 'run `migrate baseline` first'.
go run ./cmd/migrate up
# 1. Drain RabbitMQ: both queues must show 0 messages
curl -s -u cp:cp http://localhost:15672/api/queues | jq -r '.[] | "\(.name) \(.messages)"'
# 2. Stop the Node worker (Ctrl-C in its terminal). This also stops its outbox publisher.
# 3. Start the Go worker:
go run ./cmd/worker
```
The Node API (`cd control-plane && npm run dev:api`) and the UI (`cd cp-ui && npm run dev`) keep running.

- [ ] **Step 4: Cutover checklist** (the Verifier runs it and records each result)

1. **Workflow visible.** Deploy an existing `nodejs` service from the UI. The deployment goes `queued → building → deploying → deployed`, and the Temporal UI shows `deploy-<deploymentId>` completed.
2. **App reachable.** `http://<serviceId>.localhost/` serves the app.
3. **Isolation and limits.**
   - `docker inspect cp-<deploymentId> -f '{{json .HostConfig.PortBindings}} {{.HostConfig.Memory}} {{.HostConfig.NanoCpus}} {{.HostConfig.PidsLimit}}'` shows `{} 536870912 500000000 256`.
   - `docker network inspect cp-proj-<projectId> -f '{{range .Containers}}{{.Name}} {{end}}'` lists the app container and `cp_nginx`.
4. **Old containers retired.** Redeploy the same service. The previous `cp-<oldId>` container is gone (`docker ps -a --filter label=cp.serviceId=<serviceId>`), and its row has `stoppedAt` set.
5. **Redis credentials.** Deploy a `redis` service (new or existing).
   - `docker exec cp-<id> sh -c 'redis-cli -a "$REDIS_PASSWORD" --no-auth-warning ping'` prints `PONG`, and `docker exec cp-<id> redis-cli ping` prints `NOAUTH …`.
   - The published port is bound to `127.0.0.1` only.
   - Redeploying it removes the old container **before** the new one starts (Temporal history order).
6. **Mongo migration** (only if a Node-era Mongo service exists; CTO 2026-10-09). Do this **before** redeploying it with Go:
   - Dump: `docker exec <node-mongo-container> mongodump --archive --gzip -u admin -p admin123 --authenticationDatabase admin > ~/cp-mongo-<serviceId>.archive.gz`.
   - Redeploy the service with Go; read the new password with `docker exec cp-<newDeploymentId> printenv MONGO_INITDB_ROOT_PASSWORD`.
   - Restore: `docker exec -i cp-<newDeploymentId> sh -c 'mongorestore --archive --gzip -u admin -p "$MONGO_INITDB_ROOT_PASSWORD" --authenticationDatabase admin' < ~/cp-mongo-<serviceId>.archive.gz`.
   - Check: `mongosh` with the new password lists the old databases; redeploy once more and the data is still there; `docker inspect cp-<id>` shows no anonymous volume; `admin123` no longer works.
   - The old container's anonymous volume is left as a fallback; delete it by hand after verification.
7. **Broken repo.** Deploy a `docker` service with repo `https://github.com/octocat/does-not-exist.git`. It ends `failed`, and Temporal shows a failed workflow after the retries (several minutes because of backoff).
8. **Logs survive restarts.** Stop and restart the Go worker. Logs for a running app keep streaming in the UI (Node API Socket.io reads the same Redis channel).
9. **No duplicate flushes.** After two flush cycles (10+ minutes):
   `curl -s 'http://localhost:8123/' --data-binary "SELECT count() = uniqExact(timestamp, message) FROM container_logs WHERE deployment_id='<id>'"` prints `1`.
10. **Metrics.** `curl -s 127.0.0.1:9091/metrics | grep cp_deployments_total` shows the deployed and failed counts.

- [ ] **Step 5: Record the rollback window**

Append to `docs/superpowers/review/decisions.md`: the cutover date, and "rollback window open until <date + 14 days>".

Rollback procedure:
1. Wait for running workflows to finish (Temporal UI: no running `deploy-*`).
2. Stop the Go worker.
3. Run `cd control-plane && npm run dev:worker`.

RabbitMQ stays up for the whole window.

- [ ] **Step 6: Commit the records**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add docs/superpowers/review
git commit -m "docs: Plan 1 gate results and worker cutover record"
```
