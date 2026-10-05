#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
project="one-nvr-media-e2e-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.e2e-test.yaml -f deploy/production/compose.media-e2e-test.yaml)
cleanup() {
 local status=$?
 if [[ $status -ne 0 ]]; then
  "${compose[@]}" logs --tail=40 api worker gateway fixture || true
  # Safe domain states only. No source URLs, media keys, credential payloads or raw ZLM logs.
  "${compose[@]}" exec -T postgres psql -U one_nvr_test -d one_nvr_test -At -c "SELECT kind,state,error_code FROM jobs ORDER BY created_at; SELECT kind,phase,state,error_code FROM source_switches ORDER BY created_at; SELECT kind,state,reason_code FROM source_observations ORDER BY observed_at DESC LIMIT 12" || true
 fi
 "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
if [[ ${1:-} != --no-build ]]; then
 for image in app gateway browser; do docker build -f "deploy/production/Dockerfile.$image" -t "one-nvr/$image:ci" .; done
 docker build -f deploy/production/Dockerfile.app --target media-test -t one-nvr/media-test:ci .
elif [[ $# != 1 ]]; then exit 2; fi
"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm permissions
"${compose[@]}" run --rm media-permissions
"${compose[@]}" up -d camera
export ONE_NVR_E2E_CAMERA_CIDR
camera_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$("${compose[@]}" ps -q camera)")
[[ "$camera_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 1
ONE_NVR_E2E_CAMERA_CIDR="$camera_ip/32"
"${compose[@]}" run --rm runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayMediaRuntimeE2EInit$' -count=1 -v
"${compose[@]}" run --rm runner go build -trimpath -buildvcs=false -o /media-config/media-launcher ./cmd/media-launcher
sleep 2
"${compose[@]}" up -d fixture api worker gateway zlm
for ((attempt=0; attempt<60; attempt++)); do
 if "${compose[@]}" exec -T gateway wget -q -O /dev/null http://127.0.0.1/api/v1/setup/status; then break; fi
 sleep 1
done
[[ $attempt -lt 60 ]] || { printf 'Actual media/browser entry unavailable.\n' >&2; exit 1; }
"${compose[@]}" run --rm browser
mkdir -p media-test-results
"${compose[@]}" create --no-deps browser >/dev/null
browser=$("${compose[@]}" ps -aq browser)
# Copy only the non-secret acceptance receipt. Missing output is a hard failure.
docker cp "$browser:/results/media-browser.json" media-test-results/media-browser.json
