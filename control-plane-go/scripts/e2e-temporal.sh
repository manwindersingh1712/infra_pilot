#!/usr/bin/env bash
# e2e-temporal.sh - run the Temporal e2e tests against a THROWAWAY Temporal server.
#
# Usage: control-plane-go/scripts/e2e-temporal.sh [--record]
#
# Starts one container, <project>_temporal, from the already-local image
# temporalio/temporal:latest (it never pulls) running `server start-dev` with an
# in-memory database, publishes its gRPC port on 127.0.0.1 only, exports
# TEMPORAL_E2E_ADDR and runs the e2e tests in internal/workflows and
# internal/worker/outbox. The container is removed on exit, by exact name.
#
# The teardown cases (TestE2ETeardown*) include TestE2ETeardownRealDocker: it
# deletes services that have a running deploy against real Temporal, real Docker
# (busybox:1.36 and nginx:alpine, both must be local or it skips), and a Postgres
# testcontainer, then checks the deploy ended failed, containers, route, volume
# (kept or purged), env rows and the Completed workflow. It names every Docker
# object after fresh ids (label cp.e2e=teardown) and removes only those.
#
# --record also rewrites internal/workflows/testdata/deploy_history.json from a
# fresh real DeployWorkflow run (RECORD_DEPLOY_HISTORY=1).
#
# Environment (optional):
#   E2E_PROJECT        name prefix (default t27e2e; never control-plane / cp)
#   E2E_TEMPORAL_PORT  host port (default 37233; never 7233 or 8233)
#   E2E_GO_TEST_ARGS   extra `go test` args (e.g. "-run TestE2EKill")
set -euo pipefail

P="${E2E_PROJECT:-t27e2e}"
PORT="${E2E_TEMPORAL_PORT:-37233}"
RECORD=0
case "${1:-}" in
  "") ;;
  --record) RECORD=1 ;;
  *) echo "usage: $0 [--record]" >&2; exit 2 ;;
esac

die() { echo "e2e: $*" >&2; exit 1; }
step() { echo "e2e: $*"; }

[[ "$P" =~ ^[a-z][a-z0-9]{2,30}$ ]] || die "E2E_PROJECT must match ^[a-z][a-z0-9]{2,30}\$ (got '$P')"
case "$P" in control-plane|controlplane|cp) die "refusing to use the dev project name '$P'";; esac
[[ "$PORT" =~ ^[0-9]+$ ]] || die "port '$PORT' is not a number"
case "$PORT" in 5432|6379|7233|8123|8233|80) die "refusing dev port $PORT";; esac
if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then die "port $PORT is already in use"; fi
command -v docker >/dev/null || die "docker is required"
command -v go >/dev/null || die "go is required"
docker image inspect temporalio/temporal:latest >/dev/null 2>&1 || die "image temporalio/temporal:latest is not present locally (this script never pulls)"
NAME="${P}_temporal"
if [[ -n "$(docker ps -aq --filter "name=^${NAME}\$")" ]]; then die "container '$NAME' already exists; pick another E2E_PROJECT or remove it"; fi

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_DIR="$(cd "$HERE/.." && pwd)"

cleanup() {
  local rc=$?
  set +e
  step "tearing down $NAME"
  docker rm -f -v "$NAME" >/dev/null 2>&1
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

step "starting $NAME on 127.0.0.1:$PORT (in-memory dev server)"
docker run -d --pull never --name "$NAME" --label "cp.e2e=$P" \
  -p "127.0.0.1:$PORT:7233" \
  temporalio/temporal:latest server start-dev --ip 0.0.0.0 --headless >/dev/null

ready=""
for _ in $(seq 1 90); do
  if docker logs "$NAME" 2>&1 | grep -q "Frontend.*7233\|Temporal server is running\|Server:.*7233"; then
    if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then ready=1; break; fi
  fi
  [[ "$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null)" == "true" ]] || { docker logs "$NAME" 2>&1 | tail -15; die "$NAME exited"; }
  sleep 1
done
[[ -n "$ready" ]] || { docker logs "$NAME" 2>&1 | tail -15; die "temporal did not become ready"; }
sleep 2 # the default namespace is registered just after the port opens

export TEMPORAL_E2E_ADDR="127.0.0.1:$PORT"
[[ "$RECORD" == "1" ]] && export RECORD_DEPLOY_HISTORY=1

step "running e2e tests against $TEMPORAL_E2E_ADDR (includes the teardown cases)"
cd "$GO_DIR"
# shellcheck disable=SC2086
go test -count=1 -timeout 15m -race -v -run 'TestE2E' ./internal/workflows ./internal/worker/outbox ${E2E_GO_TEST_ARGS:-}
step "PASS"
