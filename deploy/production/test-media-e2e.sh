#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
joint=no
if [[ ${1:-} == --acceptance ]]; then joint=yes; shift; fi
project="one-nvr-media-e2e-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.e2e-test.yaml -f deploy/production/compose.media-e2e-test.yaml)
if [[ $joint == yes ]]; then
 compose+=(-f deploy/production/compose.acceptance-test.yaml)
 export ONE_NVR_JOINT_ISOLATED=yes
fi
cleanup() {
 local status=$?
 if [[ $status -ne 0 ]]; then
  "${compose[@]}" logs --tail=40 api worker gateway fixture || true
  # Safe domain states only. No source URLs, media keys, credential payloads or raw ZLM logs.
  "${compose[@]}" exec -T postgres psql -U one_nvr_test -d one_nvr_test -At -c "SELECT kind,state,error_code FROM jobs ORDER BY created_at; SELECT kind,phase,state,error_code FROM source_switches ORDER BY created_at; SELECT kind,state,reason_code FROM source_observations ORDER BY observed_at DESC LIMIT 12" || true
 fi
 "${compose[@]}" unpause worker >/dev/null 2>&1 || true
 "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
if [[ ${1:-} != --no-build ]]; then
 for image in app gateway browser; do docker build -f "deploy/production/Dockerfile.$image" -t "one-nvr/$image:ci" .; done
 docker build -f deploy/production/Dockerfile.app --target media-test -t one-nvr/media-test:ci .
 if [[ $joint == yes ]]; then docker build -f deploy/production/Dockerfile.fixture-runner -t one-nvr/fixture-runner:ci .; fi
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
entry=http://127.0.0.1
if [[ $joint == yes ]]; then entry=https://127.0.0.1; fi
for ((attempt=0; attempt<120; attempt++)); do
 if "${compose[@]}" exec -T gateway wget --no-check-certificate -q -O /dev/null "$entry/api/v1/setup/status"; then break; fi
 sleep 1
done
[[ $attempt -lt 120 ]] || { printf 'Actual media/browser entry unavailable.\n' >&2; exit 1; }
"${compose[@]}" run --rm browser
if [[ $joint == yes ]]; then source deploy/production/joint-runtime.sh; fi
mkdir -p media-test-results
"${compose[@]}" create --no-recreate browser >/dev/null
browser=$("${compose[@]}" ps -aq browser)
# Copy only the non-secret acceptance receipt. Missing output is a hard failure.
docker cp "$browser:/results/media-browser.json" media-test-results/media-browser.json

if [[ $joint == yes ]]; then
 for receipt in runtime spool db tls zlm modules boundaries publish-before publish-after; do
  docker cp "$browser:/results/joint-$receipt.json" "media-test-results/joint-$receipt.json"
 done
fi
