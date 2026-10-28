#!/usr/bin/env bash
# Negative test for the lint gate: every file in testdata/unsafe-migrations is
# added, one at a time, as the next migration on top of the real ones, and the
# exact gate CI uses (scripts/lint-migrations.sh) must FAIL and name the rule
# the file declares in its "-- expect:" header. The real migrations alone must
# pass. If an unsafe migration ever slips through, this script fails.
set -euo pipefail

cd "$(dirname "$0")/.."
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
summary=${GITHUB_STEP_SUMMARY:-/dev/null}

echo "== real migrations must pass the gate"
scripts/lint-migrations.sh migrations/*.sql
{
  echo "### Migration lint gate"
  echo
  echo "| Migration | Expected rule(s) | Gate result |"
  echo "|---|---|---|"
  echo "| \`migrations/*.sql\` (the real ones) | none | pass |"
} >>"$summary"

failures=0
for unsafe in testdata/unsafe-migrations/*.sql; do
  expected=$(sed -n 's/^-- expect: *//p' "$unsafe" | head -n1)
  [[ -n $expected ]] || {
    echo "missing '-- expect:' header in $unsafe" >&2
    exit 1
  }
  rm -rf "$work/m" && mkdir -p "$work/m"
  cp migrations/*.sql "$work/m/"
  cp "$unsafe" "$work/m/00099_$(basename "$unsafe")"

  echo "== $unsafe must FAIL the gate with: $expected"
  set +e
  out=$(scripts/lint-migrations.sh "$work"/m/*.sql 2>&1)
  rc=$?
  set -e
  echo "$out" | sed 's/^/    /'
  status="blocked"
  if ((rc == 0)); then
    echo "!! gate passed an unsafe migration: $unsafe" >&2
    status="**NOT BLOCKED**"
    failures=$((failures + 1))
  fi
  IFS=',' read -ra rules <<<"$expected"
  for rule in "${rules[@]}"; do
    rule=$(echo "$rule" | tr -d ' ')
    if ! grep -q -- "$rule" <<<"$out"; then
      echo "!! expected rule '$rule' not reported for $unsafe" >&2
      status="**wrong rule**"
      failures=$((failures + 1))
    fi
  done
  echo "| \`$(basename "$unsafe")\` | ${expected} | ${status} |" >>"$summary"
done

if ((failures > 0)); then
  echo "lint gate negative test: ${failures} problem(s)" >&2
  exit 1
fi
echo "lint gate negative test: every unsafe migration was blocked with the expected rule"
