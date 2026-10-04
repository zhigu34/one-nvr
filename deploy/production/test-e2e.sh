#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
project="one-nvr-e2e-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.e2e-test.yaml)
cleanup() { "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT
trap 'exit 130' INT TERM
if [[ ${1:-} != --no-build ]]; then
 for image in app gateway browser; do docker build -f "deploy/production/Dockerfile.$image" -t "one-nvr/$image:ci" .; done
elif [[ $# != 1 ]]; then exit 2; fi
"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm permissions
"${compose[@]}" run --rm runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayTLSRuntimeE2EInit$' -count=1 -v
"${compose[@]}" up -d api worker gateway
wait_entry() {
 for ((attempt=0; attempt<60; attempt++)); do
  if "${compose[@]}" exec -T gateway wget -q -O /dev/null http://127.0.0.1/api/v1/setup/status; then return; fi
  sleep 1
 done
 printf 'Isolated browser API did not become ready.\n' >&2
 return 1
}
wait_entry
"${compose[@]}" run --rm browser
# Recreate actual processes while preserving only this fixture's DB/private volumes.
"${compose[@]}" up -d --no-deps --force-recreate api worker gateway
wait_entry
"${compose[@]}" run --rm -e ONE_NVR_E2E_PHASE=restart browser
