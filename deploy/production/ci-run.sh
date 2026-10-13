#!/usr/bin/env bash
# Run one CI acceptance command and publish its failure as an annotation.
#
# Job logs are not readable without authentication, so a failing step otherwise
# shows only "Process completed with exit code 1" — the reason this suite spent
# several rounds red with no diagnosable cause. The command's output still goes
# to the log for anyone signed in; the last lines also ride the annotation, so
# the cause is visible from the checks page alone.
#
# Only failures annotate as errors: an unconditional ::error makes a passing
# command report red, which is the same trap in the other direction.
set -uo pipefail
[[ $# -ge 2 ]] || { printf '%s\n' 'Usage: ci-run.sh <title> <command> [args...]' >&2; exit 2; }
title=$1
shift
out=$("$@" 2>&1)
status=$?
printf '%s\n' "$out"
if [[ $status -ne 0 ]]; then
  summary=$(printf '%s' "$out" | tail -25 | tr '\n' '|')
  printf '::error title=%s failed::%s\n' "$title" "$summary"
fi
exit "$status"
