#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
project="one-nvr-e2e-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.e2e-test.yaml)
cleanup() { local status=$?; if [[ $status -ne 0 ]]; then "${compose[@]}" logs --tail=60 api worker gateway || true; "${compose[@]}" exec -T postgres psql -U one_nvr_test -d one_nvr_test -At -c "SELECT kind,state,error_code FROM jobs WHERE kind='tls.apply' ORDER BY created_at" || true; fi; "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true; }
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
"${compose[@]}" run --rm -e ONE_NVR_E2E_PHASE=sources browser
# Recreate actual processes while preserving only this fixture's DB/private volumes.
"${compose[@]}" up -d --no-deps --force-recreate api worker gateway
wait_entry
"${compose[@]}" run --rm -e ONE_NVR_E2E_PHASE=restart browser
# One same-browser protocol transition, using the actual Nginx TLS listener.
if [[ -z ${ONE_NVR_E2E_TLS_DIR:-} && ${ONE_NVR_E2E_MODULES:-no} == no ]]; then
 "${compose[@]}" stop worker
 "${compose[@]}" run --rm --no-deps -e ONE_NVR_PUBLIC_URL=https://gateway --entrypoint /usr/local/bin/admin api bootstrap-tls
 export ONE_NVR_E2E_PUBLIC_URL=https://gateway
 "${compose[@]}" up -d --no-deps --force-recreate api worker gateway
 for ((attempt=0; attempt<60; attempt++)); do
  if "${compose[@]}" exec -T gateway wget --no-check-certificate -q -O /dev/null https://gateway/api/v1/setup/status; then break; fi
  sleep 1
 done
 [[ $attempt -lt 60 ]] || { printf 'Isolated TLS listener unavailable.\n' >&2; exit 1; }
 "${compose[@]}" run --rm -e ONE_NVR_E2E_PHASE=protocol browser &
 browser_pid=$!
 for ((attempt=0; attempt<60; attempt++)); do
  if "${compose[@]}" run --rm --no-deps --entrypoint sh browser -c 'test -f /results/protocol-ready'; then break; fi
  kill -0 "$browser_pid" 2>/dev/null || { wait "$browser_pid"; exit 1; }
  sleep 1
 done
 [[ $attempt -lt 60 ]] || { printf 'Isolated HTTPS login unavailable.\n' >&2; exit 1; }
 export ONE_NVR_E2E_PUBLIC_URL=http://gateway
 "${compose[@]}" up -d --no-deps --force-recreate api worker gateway
 wait_entry
 "${compose[@]}" run --rm --no-deps --entrypoint sh browser -c 'touch /results/protocol-http'
 wait "$browser_pid"
fi
