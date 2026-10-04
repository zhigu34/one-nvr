#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
[[ ${1:---contract} == --contract ]] || { echo 'Media acceptance stage not implemented' >&2; exit 2; }
project="one-nvr-media-test-${GITHUB_RUN_ID:-local}-$$"
export ONE_NVR_MEDIA_TEST_DIR
ONE_NVR_MEDIA_TEST_DIR=$(mktemp -d)
compose=(docker compose -p "$project" -f deploy/production/compose.media-test.yaml)
cleanup(){ "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true; rm -rf "$ONE_NVR_MEDIA_TEST_DIR"; }
trap cleanup EXIT
mkdir -p "$ONE_NVR_MEDIA_TEST_DIR/storage/pool/.work/zlm" "$ONE_NVR_MEDIA_TEST_DIR/evidence"
chmod -R a+rwX "$ONE_NVR_MEDIA_TEST_DIR"
cat > "$ONE_NVR_MEDIA_TEST_DIR/camera.ini" <<'INI'
[api]
apiDebug=0
secret=isolated-media-fixture-only
[http]
port=80
sslport=0
[rtsp]
port=554
sslport=0
[protocol]
enable_mp4=0
enable_hls=0
auto_close=0
continue_push_ms=0
[general]
mediaServerId=one-nvr-synthetic-camera
INI
cp "$ONE_NVR_MEDIA_TEST_DIR/camera.ini" "$ONE_NVR_MEDIA_TEST_DIR/zlm.ini"
cat >> "$ONE_NVR_MEDIA_TEST_DIR/zlm.ini" <<'INI'
[general]
mediaServerId=one-nvr-media-contract
[hook]
enable=1
on_record_mp4=http://runner:8083/complete
retry=3
retry_delay=0.2
INI
"${compose[@]}" config --quiet
"${compose[@]}" up -d camera zlm
sleep 2
"${compose[@]}" up -d fixture
status=0
"${compose[@]}" run --rm --use-aliases runner || status=$?
mkdir -p media-test-results
if [[ -f "$ONE_NVR_MEDIA_TEST_DIR/evidence/contract.json" ]]; then cp "$ONE_NVR_MEDIA_TEST_DIR/evidence/contract.json" media-test-results/; fi
# Synthetic secrets must not appear even in private component stdout.
if "${compose[@]}" logs zlm | grep -F 'isolated-media-fixture-only'; then echo 'Media log leaked fixture secret' >&2; exit 1; fi
exit "$status"
