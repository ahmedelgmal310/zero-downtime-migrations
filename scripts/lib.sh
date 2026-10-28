#!/usr/bin/env bash
# Shared helpers for scripts/e2e.sh and scripts/demo-unsafe.sh.
# shellcheck shell=bash

set -euo pipefail

NS=${NS:-zdm}
RELEASE=${RELEASE:-zdm}
CHART=${CHART:-charts/zdm}
VALUES=${VALUES:-e2e/values-e2e.yaml}
RESULTS=${RESULTS:-e2e-results}
SEED_ROWS=${SEED_ROWS:-200000}
ZDM=${ZDM:-./bin/zdm}
K6_IMAGE=${K6_IMAGE:?K6_IMAGE must be set (pinned by digest in the workflow)}
READ_RATE=${READ_RATE:-40}
WRITE_RATE=${WRITE_RATE:-10}
CONTROL_DIR="${RESULTS}/control"

# The tools on the runner reach PostgreSQL through the kind port mapping.
export PGHOST=127.0.0.1 PGPORT=5432 PGUSER=app PGDATABASE=app PGSSLMODE=disable

log() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" >&2; }
die() {
  log "FAIL: $*"
  exit 1
}
now_ms() { date +%s%3N; }

init_results() {
  rm -rf "$RESULTS"
  mkdir -p "$RESULTS/jobs" "$CONTROL_DIR"
  chmod 0777 "$RESULTS" # the k6 container writes here as its own user
}

# setup_platform creates the namespace, a per-run database password and PostgreSQL.
setup_platform() {
  kubectl create namespace "$NS" >/dev/null
  PGPASSWORD=$(openssl rand -hex 24)
  export PGPASSWORD
  if [[ -n ${GITHUB_ACTIONS:-} ]]; then echo "::add-mask::${PGPASSWORD}"; fi
  kubectl -n "$NS" create secret generic postgres-auth --from-literal=password="$PGPASSWORD" >/dev/null
  kubectl -n "$NS" apply -f deploy/postgres.yaml >/dev/null
  kubectl -n "$NS" rollout status statefulset/postgres --timeout=180s >/dev/null
  local i
  for i in $(seq 1 60); do
    if pg_isready -q -t 2; then
      log "postgres ready (${i}s)"
      return 0
    fi
    sleep 1
  done
  die "postgres not reachable on ${PGHOST}:${PGPORT}"
}

# --- step timeline -----------------------------------------------------------
STEP_NAME=""
STEP_START=0
JOB_SEQ=0

step_begin() {
  STEP_NAME=$1
  STEP_START=$(now_ms)
  log "=== step: ${STEP_NAME}"
}

step_end() {
  local end
  end=$(now_ms)
  jq -cn --arg s "$STEP_NAME" --argjson a "$STEP_START" --argjson b "$end" --arg n "${1:-}" \
    '{step: $s, start_ms: $a, end_ms: $b, note: $n}' >>"$RESULTS/timeline.jsonl"
  log "=== done: ${STEP_NAME} ($(((end - STEP_START) / 1000))s)"
}

hold() {
  log "holding ${1}s under load"
  sleep "$1"
}

# --- releases -----------------------------------------------------------------

# release <image tag> <schema target> <step label>: helm upgrade with the
# migration hook, waiting for the rolling update to finish.
release() {
  local tag=$1 target=$2 label=$3
  log "helm upgrade --install ${RELEASE} image.tag=${tag} migrations.target=${target}"
  helm upgrade --install "$RELEASE" "$CHART" -n "$NS" -f "$VALUES" \
    --set image.tag="$tag" --set migrations.target="$target" \
    --wait --timeout 6m >/dev/null || {
    collect_job_log "$label (FAILED)"
    die "helm upgrade to ${tag} (target ${target}) failed"
  }
  collect_job_log "$label"
  assert_fleet "$tag"
}

collect_job_log() {
  JOB_SEQ=$((JOB_SEQ + 1))
  local out
  out="$RESULTS/jobs/$(printf '%02d' "$JOB_SEQ").log"
  {
    echo "# step: $1"
    kubectl -n "$NS" logs "job/${RELEASE}-migrate" 2>/dev/null || true
  } >"$out"
}

# assert_fleet <tag>: wait until every API pod (old ones finish draining after
# `helm --wait` returns) runs the given image.
assert_fleet() {
  local want="zdm:$1" images="" i
  for i in $(seq 1 90); do
    images=$(kubectl -n "$NS" get pods -l app.kubernetes.io/instance="$RELEASE",app.kubernetes.io/component!=migrate,app.kubernetes.io/component!=backfill \
      -o jsonpath='{range .items[*]}{.spec.containers[0].image}{"\n"}{end}' | sort -u)
    if [[ $images == "$want" ]]; then
      log "fleet: all API pods on ${want} (${i}s)"
      return 0
    fi
    sleep 1
  done
  die "expected all API pods on ${want}, got: ${images//$'\n'/ }"
}

# rollback <revision> <tag> <step label>: helm rollback of the app only (no hook
# runs on rollback; the schema stays expanded, which v1 tolerates).
rollback() {
  log "helm rollback ${RELEASE} ${1}"
  helm rollback "$RELEASE" "$1" -n "$NS" --wait --timeout 6m >/dev/null || die "helm rollback failed"
  assert_fleet "$2"
}

# check_step <step> [zdm check flags]: data consistency check, recorded for the report.
check_step() {
  local step=$1
  shift
  "$ZDM" check --step "$step" --state "$RESULTS/check-state.json" --out "$RESULTS/checks.jsonl" "$@" >&2 ||
    die "consistency check failed after: ${step}"
}

# --- load ---------------------------------------------------------------------

start_load() {
  python3 -m http.server 8099 --bind 127.0.0.1 --directory "$CONTROL_DIR" >"$RESULTS/control-server.log" 2>&1 &
  CONTROL_PID=$!
  "$ZDM" lockwatch --out "$RESULTS/lockwatch.jsonl" >"$RESULTS/lockwatch.log" 2>&1 &
  LOCKWATCH_PID=$!
  docker run -d --name zdm-k6 --network host -u "$(id -u):$(id -g)" \
    -v "$PWD/loadtest:/scripts:ro" -v "$PWD/$RESULTS:/out" \
    -e SEED_ROWS="$SEED_ROWS" -e READ_RATE="$READ_RATE" -e WRITE_RATE="$WRITE_RATE" \
    "$K6_IMAGE" run --quiet --no-color --log-format raw --log-output file=/out/requests.log \
    /scripts/load.js >/dev/null
  local i
  for i in $(seq 1 30); do
    if [[ -s "$RESULTS/requests.log" ]] && grep -q '^R,' "$RESULTS/requests.log"; then
      log "k6 load running (${READ_RATE} reads/s + ${WRITE_RATE} writes/s)"
      return 0
    fi
    sleep 1
  done
  docker logs zdm-k6 >&2 || true
  die "k6 did not start sending requests"
}

assert_load_running() {
  [[ $(docker inspect -f '{{.State.Running}}' zdm-k6 2>/dev/null) == "true" ]] || die "k6 load generator stopped early"
}

# stop_load stops issuing requests first, waits for in-flight ones, then ends k6
# so its summary (request and error totals) is written.
stop_load() {
  touch "$CONTROL_DIR/stop"
  sleep 6
  docker kill --signal=SIGINT zdm-k6 >/dev/null 2>&1 || true
  docker wait zdm-k6 >/dev/null 2>&1 || true
  docker logs zdm-k6 >"$RESULTS/k6-stdout.log" 2>&1 || true
  kill -INT "$LOCKWATCH_PID" 2>/dev/null || true
  wait "$LOCKWATCH_PID" 2>/dev/null || true
  kill "$CONTROL_PID" 2>/dev/null || true
  # PostgreSQL logs every lock wait longer than deadlock_timeout (100 ms).
  kubectl -n "$NS" logs statefulset/postgres >"$RESULTS/postgres.log" 2>&1 || true
  LOCK_WAITS_LOGGED=$(grep -c 'still waiting for' "$RESULTS/postgres.log" || true)
  log "lock waits > 100 ms logged by PostgreSQL: ${LOCK_WAITS_LOGGED}"
  [[ -s "$RESULTS/k6-summary.json" ]] || die "k6 did not write its summary"
  log "load stopped: $(tr -d '\n ' <"$RESULTS/k6-summary.json")"
}

row_count() {
  psql -XAtqc 'SELECT count(*) FROM users'
}

diagnostics() {
  kubectl get pods -A -o wide || true
  kubectl -n "$NS" get events --sort-by=.lastTimestamp | tail -40 || true
  kubectl -n "$NS" logs "job/${RELEASE}-migrate" --tail=50 || true
  kubectl -n "$NS" logs "job/${RELEASE}-backfill" --tail=50 || true
  kubectl -n "$NS" logs -l app.kubernetes.io/name=zdm --tail=30 --prefix || true
  kubectl -n "$NS" logs statefulset/postgres --tail=80 || true
}
