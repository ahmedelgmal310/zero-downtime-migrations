#!/usr/bin/env bash
# DEMO (expected to produce errors): what happens when the contract step runs
# while the old release is still serving.
#
#   1. v1 serves under load; the expand migrations run (safe: zero errors).
#   2. The contract migration is attempted while v1 pods are alive:
#      the fleet guard REFUSES it (asserted).
#   3. The guard is bypassed with --unsafe-skip-guards: "name" is dropped
#      under v1, and v1's reads and writes fail (asserted: errors > 0).
#
# The job passes when both failure modes are reproduced. It is the evidence
# for why the contract step is gated, not a test of the happy path.

# shellcheck source=scripts/lib.sh
source "$(dirname "$0")/lib.sh"

trap 'rc=$?; if ((rc != 0)); then diagnostics; fi' EXIT

init_results
setup_platform
release v1 1 "install v1 (baseline schema)"
"$ZDM" seed --rows "$SEED_ROWS" >&2
check_step "seeded (v1, baseline)" --seed-rows "$SEED_ROWS" --require-phase baseline
start_load

step_begin "1. v1 steady state"
hold 15
step_end

step_begin "2. expand, backfill, NOT NULL while v1 runs (safe)"
{
  echo "# step: ${STEP_NAME}"
  "$ZDM" migrate --target 3
  "$ZDM" backfill --pause 20ms
  "$ZDM" migrate --target 5
} >"$RESULTS/jobs/01.log"
hold 10
step_end

step_begin "3. contract attempted while v1 runs: guard refuses"
if "$ZDM" migrate --target 6 >"$RESULTS/guard.log"; then
  die "the guard allowed dropping 'name' while v1 pods are alive"
fi
grep -q '"msg":"guard_refused"' "$RESULTS/guard.log" || die "migrate failed, but not because of the guard"
log "guard refused: $(grep -o '"alive":\[[^]]*\]' "$RESULTS/guard.log")"
hold 10
step_end "guard refused"

step_begin "4. contract FORCED with --unsafe-skip-guards while v1 runs"
{
  echo "# step: ${STEP_NAME}"
  "$ZDM" migrate --target 6 --unsafe-skip-guards
} >"$RESULTS/jobs/02.log"
hold 20
step_end "old code reads a dropped column"

stop_load
"$ZDM" report --dir "$RESULTS" --slo-p95-ms 100 --no-gate \
  --markdown "$RESULTS/report.md" --json "$RESULTS/results.json"

safe_errors=$(jq '[.steps[] | select(.step | startswith("4.") | not) | .latency.errors] | add' "$RESULTS/results.json")
forced_errors=$(jq '.steps[] | select(.step | startswith("4.")) | .latency.errors' "$RESULTS/results.json")
forced_total=$(jq '.steps[] | select(.step | startswith("4.")) | .latency.requests' "$RESULTS/results.json")

if [[ -n ${GITHUB_STEP_SUMMARY:-} ]]; then
  {
    echo "## DEMO: unguarded contract migration under load (errors expected)"
    echo
    echo "The guard refused the contract while v1 was running. Forcing it anyway failed"
    echo "**${forced_errors} of ${forced_total} requests** in step 4; steps 1-3 had **${safe_errors}** failures."
    echo
    cat "$RESULTS/report.md"
  } >>"$GITHUB_STEP_SUMMARY"
fi
cat "$RESULTS/report.md"

((safe_errors == 0)) || die "errors before the forced contract step: ${safe_errors}"
((forced_errors > 0)) || die "dropping the column under v1 caused no errors; the demo proves nothing"
log "demo reproduced: guard refused, forced contract failed ${forced_errors}/${forced_total} requests"
