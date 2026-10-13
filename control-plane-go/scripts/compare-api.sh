#!/usr/bin/env bash
# Compares read-only (GET) responses of the Node API and the Go API against the
# same database. Uses a Go-issued token (it has exp; Node accepts it when both
# run with the same JWT_SECRET). The token is never printed.
#
# Intended differences that are normalised away before comparing:
#   - stoppedAt (new in Go) is removed everywhere;
#   - null-valued fields are dropped (null and missing count as the same);
#   - ISO-8601 timestamps are compared as whole epoch seconds, so "Z" vs
#     "+00:00" and ms vs us precision do not matter;
#   - arrays are sorted by their JSON text, so row order does not matter
#     (the order of /deployments is checked separately, by id);
#   - env vars: Go masks values (and encrypts at rest), Node returned plaintext,
#     so only the sorted key set is compared.
#
# Env: NODE (default http://localhost:8080), GO (default http://localhost:8081),
#      TOKEN (optional, skips /dev/login), PROJECT / SERVICE (optional ids).
# Exit: 0 all same, 1 any DIFF or ERROR, 2 prerequisites missing.
set -euo pipefail

NODE=${NODE:-http://localhost:8080}
GO=${GO:-http://localhost:8081}

die() { echo "compare-api: $*" >&2; exit 2; }

for tool in curl jq diff mktemp; do
  command -v "$tool" >/dev/null 2>&1 || die "'$tool' is required but not installed (macOS: brew install $tool)"
done

TMP=$(mktemp -d) || die "mktemp -d failed"
trap 'rm -rf "$TMP"' EXIT
chmod 700 "$TMP"

curl_opts=(--silent --show-error --connect-timeout 5 --max-time 30)

if [[ -z "${TOKEN:-}" ]]; then
  login_code=$(curl "${curl_opts[@]}" -o "$TMP/login.json" -w '%{http_code}' -X POST "$GO/dev/login" 2>"$TMP/login.err") || login_code=000
  if [[ "$login_code" != 200 ]]; then
    die "cannot log in at $GO/dev/login (HTTP $login_code). Is the Go API running with APP_ENV=development? Or set TOKEN=<jwt>."
  fi
  TOKEN=$(jq -r '.token // empty' "$TMP/login.json" 2>/dev/null) || TOKEN=""
  rm -f "$TMP/login.json"
fi
[[ -n "$TOKEN" ]] || die "no token obtained from $GO/dev/login"

# The token goes through a 0600 header file, not argv, so it is not in `ps`.
umask 077
printf 'Authorization: Bearer %s\n' "$TOKEN" >"$TMP/auth.hdr"
unset TOKEN

# fetch BASE PATH OUTFILE -> prints the HTTP code; never fails the script.
fetch() {
  local code
  code=$(curl "${curl_opts[@]}" -H "@$TMP/auth.hdr" -o "$3" -w '%{http_code}' "$1$2" 2>/dev/null) || code=000
  echo "$code"
}

pick() { # path, jq expression -> first match or empty; never fails
  local code
  code=$(fetch "$GO" "$1" "$TMP/pick.json")
  if [[ "$code" == 200 ]]; then
    jq -r "$2 // empty" "$TMP/pick.json" 2>/dev/null || true
  fi
}

PROJECT=${PROJECT:-$(pick "/projects" '.[0].id')}
[[ -n "$PROJECT" ]] || die "no project found for the dev user. Log in as the dev user in the UI and create a project with a service first."
SERVICE=${SERVICE:-$(pick "/services?projectId=$PROJECT" '.[0].id')}
[[ -n "$SERVICE" ]] || die "project $PROJECT has no service. Create a service (with an env var and a deployment) first."

NORMALIZE='
  def ts: if type == "string" and test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|\\+00:00)$")
          then (sub("\\.[0-9]+"; "") | sub("\\+00:00$"; "Z") | fromdateiso8601) else . end;
  walk(
    if type == "object" then del(.stoppedAt) | with_entries(select(.value != null))
    elif type == "array" then sort_by(tojson)
    else ts end
  )'

status=0

check() { # path, jq filter, optional label
  local path=$1 filter=$2 label=${3:-$1} nc gc
  nc=$(fetch "$NODE" "$path" "$TMP/node.raw")
  gc=$(fetch "$GO" "$path" "$TMP/go.raw")
  if [[ "$nc" != 2* || "$gc" != 2* ]]; then
    echo "ERROR $label (node HTTP $nc, go HTTP $gc)"
    status=1
    return 0
  fi
  if ! jq -S "$filter" "$TMP/node.raw" >"$TMP/node.json" 2>"$TMP/jq.err" ||
     ! jq -S "$filter" "$TMP/go.raw" >"$TMP/go.json" 2>>"$TMP/jq.err"; then
    echo "ERROR $label (response is not valid JSON for the filter: $(head -c 200 "$TMP/jq.err"))"
    status=1
    return 0
  fi
  if diff -u "$TMP/node.json" "$TMP/go.json" >"$TMP/compare.diff"; then
    echo "same  $label"
  else
    echo "DIFF  $label  (--- node, +++ go)"
    cat "$TMP/compare.diff"
    status=1
  fi
}

check "/projects" "$NORMALIZE"
check "/projects/$PROJECT" "$NORMALIZE"
check "/services?projectId=$PROJECT" "$NORMALIZE"
check "/deployments?serviceId=$SERVICE" "$NORMALIZE"
check "/deployments?serviceId=$SERVICE" '[.[].id]' "/deployments?serviceId=$SERVICE (order)"
check "/projects/$PROJECT/canvas" "$NORMALIZE"
check "/services/$SERVICE/env" '[.[].key] | sort'
exit $status
