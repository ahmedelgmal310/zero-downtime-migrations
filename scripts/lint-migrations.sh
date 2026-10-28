#!/usr/bin/env bash
# The migration lint gate. Fails on any squawk violation that is not
# explicitly waived with a reviewed "-- squawk-ignore <rule>" comment.
#
#   scripts/lint-migrations.sh                 # lint migrations/*.sql
#   scripts/lint-migrations.sh path/to/*.sql   # lint specific files
set -euo pipefail

SQUAWK=${SQUAWK:-squawk}
CONFIG=${SQUAWK_CONFIG:-.squawk.toml}

if (($# == 0)); then
  set -- migrations/*.sql
fi
"$SQUAWK" --config "$CONFIG" "$@"
