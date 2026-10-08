#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
joint=no
peer_reservation=
initial_hook_ip=
initial_worker_ip=
# Job logs need repository admin rights, so a failure that only exists in those
# logs is undiagnosable from outside. Track the stage and keep the browser
# output so the reason travels in the check annotations instead. Only fixed
# stage names and Playwright's own failure block are ever emitted: no source
# URLs, credentials, media keys or page content.
stage=prepare
browser_log=/tmp/one-nvr-media-browser.log
if [[ ${1:-} == --acceptance ]]; then joint=yes; shift; fi
project="one-nvr-media-e2e-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.e2e-test.yaml -f deploy/production/compose.media-e2e-test.yaml)
if [[ $joint == yes ]]; then
 compose+=(-f deploy/production/compose.acceptance-test.yaml)
 export ONE_NVR_JOINT_ISOLATED=yes
fi
# Playwright renders a failure as a test header, the error, its call log and the
# spec location. Those lines are the whole diagnosis and carry no credentials, so
# they are republished as annotations an operator can read without admin rights.
report_browser_failure() {
	[[ -s $browser_log ]] || return 0
	awk '
		/^ *[0-9]+\) / || /^ *Error: / || /^ *Call log:/ || /waiting for / || /locator\./ || /^ *Expected/ || /^ *Received/ || /^ *Timeout/ || /^ *Timed out/ || /^ *- / || /^ *at .*\.spec\.ts:/ {
			if (n < 12) { printf "::error::%s\n", substr($0, 1, 240); n++ }
		}
	' "$browser_log"
}
cleanup() {
 local status=$?
 if [[ $status -ne 0 ]]; then
	"${compose[@]}" logs --tail=40 api worker gateway fixture || true
	# These application messages contain only fixed stages/booleans, never raw
	# completion bodies or upstream credentials; retain the entire fault window.
	"${compose[@]}" logs --no-log-prefix worker 2>/dev/null | awk '/recording completion (received|durable|database deferred|spooled|rejected)/ { print }' || true
  # Private endpoint addresses only, never env or credential/config bodies.
  "${compose[@]}" exec -T gateway getent hosts api || true
  "${compose[@]}" exec -T gateway wget -q -O /dev/null http://api:8081/health/ready || true
  # Report only whether the original firewall peer still identifies Worker.
  if [[ -n $initial_worker_ip ]]; then
   current_worker_ip=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_default\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)" 2>/dev/null) || current_worker_ip=
   [[ -n $current_worker_ip && $current_worker_ip == "$initial_worker_ip" ]] && peer_same=true || peer_same=false
   current_hook_ip=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_recording-hook\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)" 2>/dev/null) || current_hook_ip=
   [[ -n $current_hook_ip && $current_hook_ip == "$initial_hook_ip" ]] && hook_same=true || hook_same=false
   printf 'hook peer diagnostics: ordinary_worker_identity_unchanged=%s fixed_hook_identity_unchanged=%s\n' "$peer_same" "$hook_same"
  fi
  "${compose[@]}" logs --no-log-prefix zlm 2>/dev/null | awk '/hook http:\/\/worker(-hook)?:8083\/on_record_mp4/ && /failed/ {failed++; if (/timeout/) timedout++; if (/refused/) refused++} END {printf "completion transport diagnostics: failed=%d timeout=%d refused=%d\n", failed, timedout, refused}' || true
  # Safe domain states only. No source URLs, media keys, credential payloads or raw ZLM logs.
  "${compose[@]}" exec -T postgres psql -U one_nvr_test -d one_nvr_test -At -c "SELECT kind,attempt,state,error_code FROM jobs ORDER BY created_at; SELECT c.channel_no,s.kind,s.phase,s.state,s.error_code FROM source_switches s JOIN channels c ON c.id=s.channel_id ORDER BY s.created_at; SELECT service,state,reason_code,expires_at>clock_timestamp() FROM storage_pool_checks ORDER BY pool_id,service; SELECT kind,state,reason_code,expires_at>clock_timestamp() FROM source_observations ORDER BY observed_at DESC LIMIT 12; SELECT c.channel_no,b.reason_code,b.healthy_samples FROM recording_capacity_blocks b JOIN channels c ON c.id=b.channel_id ORDER BY c.channel_no" || true
 fi
 report_browser_failure >&2
 [[ -z $peer_reservation ]] || docker rm -f "$peer_reservation" >/dev/null 2>&1 || true
 "${compose[@]}" unpause worker >/dev/null 2>&1 || true
 "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
trap 'printf "::error::media acceptance failed at stage=%s\n" "$stage" >&2' ERR
if [[ ${1:-} != --no-build ]]; then
 for image in app gateway browser; do docker build -f "deploy/production/Dockerfile.$image" -t "one-nvr/$image:ci" .; done
 docker build -f deploy/production/Dockerfile.app --target media-test -t one-nvr/media-test:ci .
 if [[ $joint == yes ]]; then docker build -f deploy/production/Dockerfile.fixture-runner -t one-nvr/fixture-runner:ci .; fi
elif [[ $# != 1 ]]; then exit 2; fi
stage=compose-config
"${compose[@]}" config --quiet
stage=postgres
"${compose[@]}" up -d --wait postgres
stage=permissions
"${compose[@]}" run --rm permissions
"${compose[@]}" run --rm media-permissions
stage=camera
"${compose[@]}" up -d camera
export ONE_NVR_E2E_CAMERA_CIDR
camera_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$("${compose[@]}" ps -q camera)")
[[ "$camera_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { printf '::error::isolated camera address unavailable\n' >&2; exit 1; }
ONE_NVR_E2E_CAMERA_CIDR="$camera_ip/32"
stage=media-init
"${compose[@]}" run --rm runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayMediaRuntimeE2EInit$' -count=1 -v
stage=media-launcher
"${compose[@]}" run --rm runner go build -trimpath -buildvcs=false -o /media-config/media-launcher ./cmd/media-launcher
sleep 2
stage=stack-up
"${compose[@]}" up -d fixture api worker gateway zlm
initial_worker_ip=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_default\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)")
initial_hook_ip=$(docker inspect --format "{{range \$name, \$network := .NetworkSettings.Networks}}{{if eq \$name \"${project}_recording-hook\"}}{{\$network.IPAddress}}{{end}}{{end}}" "$("${compose[@]}" ps -q worker)")
[[ "$initial_hook_ip" == 172.30.254.2 ]] || { printf '::error::recording hook peer address mismatch\n' >&2; exit 1; }
[[ "$initial_worker_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || { printf '::error::worker network identity unavailable\n' >&2; exit 1; }
entry=http://127.0.0.1
if [[ $joint == yes ]]; then entry=https://127.0.0.1; fi
stage=entry
for ((attempt=0; attempt<120; attempt++)); do
 if "${compose[@]}" exec -T gateway wget --no-check-certificate -q -O /dev/null "$entry/api/v1/setup/status"; then break; fi
 sleep 1
done
[[ $attempt -lt 120 ]] || { printf '::error::media entry unavailable\n' >&2; printf 'Actual media/browser entry unavailable.\n' >&2; exit 1; }
stage=browser
"${compose[@]}" run --rm browser 2>&1 | tee "$browser_log"
stage=joint-runtime
if [[ $joint == yes ]]; then source deploy/production/joint-runtime.sh; fi
stage=receipts
mkdir -p media-test-results
"${compose[@]}" create --no-recreate browser >/dev/null
browser=$("${compose[@]}" ps -aq browser)
# Copy only the non-secret acceptance receipt. Missing output is a hard failure.
docker cp "$browser:/results/media-browser.json" media-test-results/media-browser.json

if [[ $joint == yes ]]; then
 for receipt in runtime spool db tls zlm modules boundaries publish-before publish-after rollback-failure rollback-recovery; do
  docker cp "$browser:/results/joint-$receipt.json" "media-test-results/joint-$receipt.json"
 done
fi
