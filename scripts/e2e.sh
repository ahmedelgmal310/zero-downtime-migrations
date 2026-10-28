#!/usr/bin/env bash
# Full expand -> migrate -> contract rollout on kind under continuous k6 load:
#
#   v1 steady -> expand + v1->v2 -> backfill running + ROLLBACK v2->v1 ->
#   roll forward v1->v2 -> backfill completes -> v2->v3 -> contract -> v3 steady
#
# A consistency check runs after every step; `zdm report` then slices the k6
# request log into these steps and fails the job unless every gate holds
# (zero failed requests, p95 under the SLO per step, all checks passed, every
# acknowledged write stored).
#
# Expects: a kind cluster with e2e/kind-config.yaml, images zdm:v1..v3 loaded,
# ./bin/zdm built for the runner, helm, kubectl, jq, psql, docker.

# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"

SLO_P95_MS=${SLO_P95_MS:-100}

trap 'rc=$?; if ((rc != 0)); then diagnostics; fi' EXIT

init_results
setup_platform

# --- v1 + seed (before the load starts) ---------------------------------------
release v1 1 "install v1 (baseline schema)"
log "seeding ${SEED_ROWS} users"
"$ZDM" seed --rows "$SEED_ROWS" >&2
check_step "seeded (v1, baseline)" --seed-rows "$SEED_ROWS" --require-phase baseline

start_load

step_begin "1. v1 steady state"
hold 30
assert_load_running
check_step "$STEP_NAME" --require-phase baseline
step_end

step_begin "2. expand migration + rolling update v1→v2"
release v2 3 "$STEP_NAME"
hold 15
assert_load_running
check_step "$STEP_NAME" --require-phase expanded
step_end

step_begin "3. backfill running + rollback v2→v1"
helm template "$RELEASE" "$CHART" -n "$NS" -f "$VALUES" --set image.tag=v2 --set backfill.enabled=true \
  --show-only templates/backfill-job.yaml | kubectl -n "$NS" apply -f - >/dev/null
kubectl -n "$NS" wait --for=condition=Ready pod -l app.kubernetes.io/component=backfill --timeout=60s >/dev/null
sleep 5
rollback 1 v1
backfill_state=$(kubectl -n "$NS" get job "${RELEASE}-backfill" -o jsonpath='{.status.active}')
[[ ${backfill_state:-0} -ge 1 ]] || die "backfill finished before the rollback completed; slow it down (backfill.pause)"
log "rollback complete while the backfill was still running"
hold 10
assert_load_running
check_step "$STEP_NAME" --require-phase expanded
step_end "backfill still running when the rollback finished"

step_begin "4. roll forward v1→v2"
release v2 3 "$STEP_NAME"
hold 10
assert_load_running
check_step "$STEP_NAME" --require-phase expanded
step_end

step_begin "5. backfill completes"
kubectl -n "$NS" wait --for=condition=Complete "job/${RELEASE}-backfill" --timeout=15m >/dev/null ||
  die "backfill job did not complete"
kubectl -n "$NS" logs "job/${RELEASE}-backfill" >"$RESULTS/backfill.log"
hold 5
check_step "$STEP_NAME" --require-phase expanded --require-backfill
step_end

step_begin "6. rolling update v2→v3"
release v3 3 "$STEP_NAME"
hold 15
assert_load_running
check_step "$STEP_NAME" --require-phase expanded --require-backfill
step_end

step_begin "7. contract migration (guarded)"
release v3 6 "$STEP_NAME"
hold 20
assert_load_running
check_step "$STEP_NAME" --require-phase contracted
step_end

step_begin "8. v3 steady state (contracted)"
hold 20
assert_load_running
step_end

stop_load
final_rows=$(row_count)
check_step "final (load stopped)" --require-phase contracted

rc=0
"$ZDM" report --dir "$RESULTS" --slo-p95-ms "$SLO_P95_MS" --seed-rows "$SEED_ROWS" --final-rows "$final_rows" \
  --markdown "$RESULTS/report.md" --json "$RESULTS/results.json" || rc=$?
if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  {
    echo "## Zero-downtime rollout under load (kind)"
    echo
    cat "$RESULTS/report.md"
  } >>"$GITHUB_STEP_SUMMARY"
fi
cat "$RESULTS/report.md"
exit "$rc"
