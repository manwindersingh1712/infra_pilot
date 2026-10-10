#!/usr/bin/env bash
# smoke-worker.sh - end-to-end smoke test of the Go worker (Plan 1 Task 26/27).
#
# Usage: control-plane-go/scripts/smoke-worker.sh
#
# Creates a THROWAWAY docker compose project (Postgres, Redis, ClickHouse, nginx,
# Temporal) from images that must already exist locally (it never pulls), runs
# the migrations against that Postgres only, builds and starts the worker with a
# scratch .env written to a mktemp dir (control-plane*/.env files are never read
# or copied; the worker runs under `env -i`), seeds a managed Redis service with
# two back-to-back deployments, waits for both to reach `deployed`, checks the
# running container count, the isolation flags and the metrics endpoint, then
# tears everything down (trap: containers, network, volumes, worker, temp dir).
#
# Environment (all optional):
#   SMOKE_PROJECT   compose project name (default t26smoke; never control-plane)
#   SMOKE_PG_PORT 25432  SMOKE_REDIS_PORT 26379  SMOKE_TEMPORAL_PORT 27233
#   SMOKE_CH_PORT 28123  SMOKE_NGINX_PORT 28080  SMOKE_METRICS_PORT 29091
#   SMOKE_APP_PORT_START 21000  SMOKE_APP_PORT_END 21010
#   SMOKE_DEADLINE_SECONDS 300   how long to wait for both deployments
#   SMOKE_KEEP=1                 keep the stack and temp dir on exit (debugging)
set -euo pipefail

P="${SMOKE_PROJECT:-t26smoke}"
PG_PORT="${SMOKE_PG_PORT:-25432}"
REDIS_PORT="${SMOKE_REDIS_PORT:-26379}"
TEMPORAL_PORT="${SMOKE_TEMPORAL_PORT:-27233}"
CH_PORT="${SMOKE_CH_PORT:-28123}"
NGINX_PORT="${SMOKE_NGINX_PORT:-28080}"
METRICS_PORT="${SMOKE_METRICS_PORT:-29091}"
APP_START="${SMOKE_APP_PORT_START:-21000}"
APP_END="${SMOKE_APP_PORT_END:-21010}"
DEADLINE="${SMOKE_DEADLINE_SECONDS:-300}"

die() { echo "smoke: $*" >&2; exit 1; }
step() { echo "smoke: $*"; }

# --- guards -------------------------------------------------------------
[[ "$P" =~ ^[a-z][a-z0-9]{2,30}$ ]] || die "SMOKE_PROJECT must match ^[a-z][a-z0-9]{2,30}\$ (got '$P')"
case "$P" in control-plane|controlplane|cp) die "refusing to use the dev project name '$P'";; esac
for port in "$PG_PORT" "$REDIS_PORT" "$TEMPORAL_PORT" "$CH_PORT" "$NGINX_PORT" "$METRICS_PORT" "$APP_START" "$APP_END"; do
  [[ "$port" =~ ^[0-9]+$ ]] || die "port '$port' is not a number"
  case "$port" in 5432|6379|7233|8123|80) die "refusing dev port $port";; esac
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then die "port $port is already in use"; fi
done
(( APP_START <= APP_END )) || die "app port range is reversed"
(( APP_END - APP_START <= 100 )) || die "app port range is too large"
command -v docker >/dev/null || die "docker is required"
command -v go >/dev/null || die "go is required"
command -v openssl >/dev/null || die "openssl is required"
command -v curl >/dev/null || die "curl is required"
for img in postgres:16 redis:7-alpine clickhouse/clickhouse-server:latest nginx:alpine temporalio/temporal:latest busybox:1.36; do
  docker image inspect "$img" >/dev/null 2>&1 || die "image $img is not present locally (this script never pulls)"
done
if [[ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$P")" ]]; then
  die "compose project '$P' already has containers; pick another SMOKE_PROJECT or remove it"
fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_DIR="$(cd "$HERE/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/${P}.XXXXXX")"
mkdir -p "$TMP/run" "$TMP/bin" "$TMP/routes"
chmod 755 "$TMP" "$TMP/routes"

rid() { printf 't26%s' "$(openssl rand -hex 9)"; }   # 21 chars of [a-z0-9]
USER_ID="$(rid)"; PROJECT_ID="$(rid)"; SERVICE_ID="$(rid)"; DEP1="$(rid)"; DEP2="$(rid)"; EV1="$(rid)"; EV2="$(rid)"
for id in "$USER_ID" "$PROJECT_ID" "$SERVICE_ID" "$DEP1" "$DEP2" "$EV1" "$EV2"; do
  [[ "$id" =~ ^[a-z0-9]{20,32}$ ]] || die "generated id '$id' does not match the cuid pattern"
done
PG_PASS="$(openssl rand -hex 16)"
ENC_KEY="$(openssl rand -base64 32)"
PG="${P}_postgres"; NGINX="${P}_nginx"
WORKER_PID=""
APP_CONTAINERS=("cp-$DEP1" "cp-$DEP2")
APP_NETWORK="cp-proj-$PROJECT_ID"
APP_VOLUME="cp-vol-$SERVICE_ID"

cleanup() {
  local rc=$?
  set +e
  if [[ "${SMOKE_KEEP:-}" == "1" ]]; then echo "smoke: SMOKE_KEEP=1, leaving $TMP and project $P"; exit "$rc"; fi
  step "tearing down"
  [[ -n "$WORKER_PID" ]] && kill "$WORKER_PID" 2>/dev/null && wait "$WORKER_PID" 2>/dev/null
  for c in "${APP_CONTAINERS[@]}"; do docker rm -f "$c" >/dev/null 2>&1; done
  docker compose -p "$P" -f "$TMP/compose.yml" down -v --remove-orphans >/dev/null 2>&1
  for c in "${P}_postgres" "${P}_redis" "${P}_clickhouse" "${P}_nginx" "${P}_temporal"; do docker rm -f -v "$c" >/dev/null 2>&1; done
  docker network rm "$APP_NETWORK" >/dev/null 2>&1
  docker network rm "${P}_default" >/dev/null 2>&1
  docker volume rm "$APP_VOLUME" >/dev/null 2>&1
  rm -rf "$TMP"
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# --- throwaway compose project ------------------------------------------
cat >"$TMP/nginx.conf" <<NGX
events {}
http {
  include /etc/nginx/conf.d/routes/*.conf;
}
NGX
cat >"$TMP/compose.yml" <<YML
name: $P
services:
  postgres:
    image: postgres:16
    container_name: $PG
    environment: { POSTGRES_USER: smoke, POSTGRES_PASSWORD: "$PG_PASS", POSTGRES_DB: smoke }
    ports: ["127.0.0.1:$PG_PORT:5432"]
    tmpfs: [/var/lib/postgresql/data]
  redis:
    image: redis:7-alpine
    container_name: ${P}_redis
    ports: ["127.0.0.1:$REDIS_PORT:6379"]
  clickhouse:
    image: clickhouse/clickhouse-server:latest
    container_name: ${P}_clickhouse
    environment: { CLICKHOUSE_DB: default, CLICKHOUSE_USER: default, CLICKHOUSE_PASSWORD: "", CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT: 1 }
    ports: ["127.0.0.1:$CH_PORT:8123"]
    tmpfs: [/var/lib/clickhouse]
  nginx:
    image: nginx:alpine
    container_name: $NGINX
    ports: ["127.0.0.1:$NGINX_PORT:80"]
    volumes:
      - $TMP/nginx.conf:/etc/nginx/nginx.conf:ro
      - $TMP/routes:/etc/nginx/conf.d/routes
  temporal:
    image: temporalio/temporal:latest
    container_name: ${P}_temporal
    user: "0:0"
    command: ["server", "start-dev", "--ip", "0.0.0.0", "--db-filename", "/data/temporal.db"]
    ports: ["127.0.0.1:$TEMPORAL_PORT:7233"]
    tmpfs: [/data]
YML

step "starting throwaway project $P"
docker compose -p "$P" -f "$TMP/compose.yml" up -d --pull never >/dev/null

wait_for() { # description, command...
  local what="$1"; shift
  for _ in $(seq 1 90); do "$@" >/dev/null 2>&1 && return 0; sleep 1; done
  die "timed out waiting for $what"
}
wait_for postgres docker exec "$PG" pg_isready -U smoke -d smoke
wait_for redis docker exec "${P}_redis" redis-cli ping
wait_for clickhouse curl -fsS "http://127.0.0.1:$CH_PORT/ping"
wait_for temporal bash -c "exec 3<>/dev/tcp/127.0.0.1/$TEMPORAL_PORT"

# --- scratch env, binaries -----------------------------------------------
cat >"$TMP/run/.env" <<ENV
DATABASE_URL="postgresql://smoke:$PG_PASS@127.0.0.1:$PG_PORT/smoke?sslmode=disable"
ENV_ENCRYPTION_KEY="$ENC_KEY"
APP_ENV=development
TEMPORAL_HOST_PORT=127.0.0.1:$TEMPORAL_PORT
REDIS_URL=redis://127.0.0.1:$REDIS_PORT
CLICKHOUSE_URL=http://127.0.0.1:$CH_PORT
WORKER_METRICS_ADDR=127.0.0.1:$METRICS_PORT
NGINX_CONTAINER=$NGINX
NGINX_PORT=$NGINX_PORT
NGINX_ROUTES_DIR=$TMP/routes
BUILD_WORKDIR=$TMP/builds
LEGACY_VOLUME_ROOT=$TMP/volumes
DATA_PLANE_PORT_RANGE_START=$APP_START
DATA_PLANE_PORT_RANGE_END=$APP_END
EXPOSE_MANAGED_PORTS=true
ENV
chmod 600 "$TMP/run/.env"

step "building worker and migrate"
(cd "$GO_DIR" && go build -o "$TMP/bin/worker" ./cmd/worker && go build -o "$TMP/bin/migrate" ./cmd/migrate)

# env -i: nothing from the caller's environment (or any .env outside $TMP/run) reaches the binaries.
run_clean() { (cd "$TMP/run" && env -i PATH="$PATH" HOME="$HOME" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} "$@"); }

step "running migrations on the throwaway Postgres"
run_clean "$TMP/bin/migrate" up >"$TMP/migrate.log" 2>&1 || { tail -20 "$TMP/migrate.log"; die "migrate failed"; }

step "starting worker"
(cd "$TMP/run" && exec env -i PATH="$PATH" HOME="$HOME" ${DOCKER_HOST:+DOCKER_HOST="$DOCKER_HOST"} "$TMP/bin/worker") >"$TMP/worker.log" 2>&1 &
WORKER_PID=$!
for _ in $(seq 1 60); do grep -q "worker up" "$TMP/worker.log" && break; kill -0 "$WORKER_PID" 2>/dev/null || { tail -20 "$TMP/worker.log"; die "worker exited"; }; sleep 1; done
grep -q "worker up" "$TMP/worker.log" || { tail -20 "$TMP/worker.log"; die "worker did not come up"; }

psql_q() { docker exec -i "$PG" psql -U smoke -d smoke -v ON_ERROR_STOP=1 -tA "$@"; }

step "seeding a managed redis service with two deployments"
psql_q <<SQL >/dev/null
BEGIN;
INSERT INTO "User" (id, email, password) VALUES ('$USER_ID', '$USER_ID@smoke.invalid', 'x');
INSERT INTO "Project" (id, name, "ownerUserId") VALUES ('$PROJECT_ID', 'smoke', '$USER_ID');
INSERT INTO "Service" (id, "projectId", name, "serviceType") VALUES ('$SERVICE_ID', '$PROJECT_ID', 'smoke-redis', 'redis');
INSERT INTO "Deployment" (id, "serviceId", "commitSha", "updatedAt") VALUES ('$DEP1', '$SERVICE_ID', 'latest', now());
INSERT INTO "Deployment" (id, "serviceId", "commitSha", "updatedAt") VALUES ('$DEP2', '$SERVICE_ID', 'latest', now());
INSERT INTO "OutboxEvent" (id, type, payload) VALUES ('$EV1', 'deploy.requested', '{"deploymentId":"$DEP1"}');
INSERT INTO "OutboxEvent" (id, type, payload) VALUES ('$EV2', 'deploy.requested', '{"deploymentId":"$DEP2"}');
COMMIT;
SQL

step "waiting up to ${DEADLINE}s for both deployments to reach deployed"
end=$(( SECONDS + DEADLINE ))
while :; do
  n="$(psql_q -c "SELECT count(*) FROM \"Deployment\" WHERE \"serviceId\"='$SERVICE_ID' AND status='deployed'")"
  [[ "$n" == "2" ]] && break
  f="$(psql_q -c "SELECT count(*) FROM \"Deployment\" WHERE \"serviceId\"='$SERVICE_ID' AND status='failed'")"
  [[ "$f" == "0" ]] || { tail -30 "$TMP/worker.log"; die "a deployment failed"; }
  (( SECONDS < end )) || { psql_q -c "SELECT id, status FROM \"Deployment\" WHERE \"serviceId\"='$SERVICE_ID'"; tail -30 "$TMP/worker.log"; die "deadline: $n of 2 deployed"; }
  kill -0 "$WORKER_PID" 2>/dev/null || { tail -30 "$TMP/worker.log"; die "worker died"; }
  sleep 3
done
step "both deployments are deployed"

# --- assertions ------------------------------------------------------------
running="$(docker ps --filter "label=cp.serviceId=$SERVICE_ID" --format '{{.Names}}')"
count="$(printf '%s' "$running" | grep -c . || true)"
[[ "$count" == "1" ]] || die "expected exactly one running app container, got $count: $running"
APP="$running"
step "exactly one running app container: $APP"

flags="$(docker inspect "$APP" --format 'CapDrop={{.HostConfig.CapDrop}} SecurityOpt={{.HostConfig.SecurityOpt}} Privileged={{.HostConfig.Privileged}} Mem={{.HostConfig.Memory}} NanoCpus={{.HostConfig.NanoCpus}} Pids={{.HostConfig.PidsLimit}} Ports={{json .HostConfig.PortBindings}}')"
echo "smoke: $flags"
[[ "$flags" == *"CapDrop=[ALL]"* ]] || die "CapDrop is not [ALL]"
[[ "$flags" == *"no-new-privileges"* ]] || die "no-new-privileges missing"
[[ "$flags" == *"Privileged=false"* ]] || die "container is privileged"
[[ "$flags" == *"Mem=536870912"* ]] || die "memory limit is not 512m"
[[ "$flags" == *"NanoCpus=500000000"* ]] || die "cpu limit is not 0.5"
[[ "$flags" == *"Pids=256"* ]] || die "pids limit is not 256"
[[ "$flags" != *"0.0.0.0"* ]] || die "a published port is bound to 0.0.0.0"
[[ "$flags" == *'"HostIp":"127.0.0.1"'* ]] || die "published port is not bound to 127.0.0.1"

step "checking metrics on 127.0.0.1:$METRICS_PORT"
ok=""
for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:$METRICS_PORT/metrics" 2>/dev/null | grep -q '^cp_outbox_oldest_pending_age_seconds'; then ok=1; break; fi
  sleep 2
done
[[ -n "$ok" ]] || die "cp_outbox_oldest_pending_age_seconds not found on the metrics endpoint"
curl -fsS "http://127.0.0.1:$METRICS_PORT/metrics" | grep -E '^(cp_outbox_oldest_pending_age_seconds|cp_outbox_events|cp_deployments_total)' || true

grep -E "worker up" "$TMP/worker.log" | cut -c1-600
if grep -qF "$PG_PASS" "$TMP/worker.log" || grep -qF "$ENC_KEY" "$TMP/worker.log"; then die "a secret appears in the worker log"; fi
step "PASS"
