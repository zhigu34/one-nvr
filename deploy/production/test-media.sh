#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
mode=${1:---contract}
[[ $mode == --contract || $mode == --probe || $mode == --publish || $mode == --switch ]] || { echo 'Unknown media acceptance stage' >&2; exit 2; }
export ONE_NVR_MEDIA_TEST_MODE=${mode#--}
export ONE_NVR_MEDIA_TEST_LOG_LEVEL=0
if [[ $mode != --contract ]]; then ONE_NVR_MEDIA_TEST_LOG_LEVEL=4; fi
project="one-nvr-media-test-${GITHUB_RUN_ID:-local}-$$"
export ONE_NVR_MEDIA_TEST_DIR
ONE_NVR_MEDIA_TEST_DIR=$(mktemp -d)
compose=(docker compose -p "$project" -f deploy/production/compose.media-test.yaml)
if [[ $mode != --contract ]]; then compose+=(--profile probe); fi
helper=
cleanup(){ "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true; if [[ -n "$helper" ]]; then docker rm -f "$helper" >/dev/null 2>&1 || true; fi; docker run --rm --user 0:0 --network none --mount "type=bind,src=$ONE_NVR_MEDIA_TEST_DIR,dst=/test-cleanup" --entrypoint sh one-nvr/media-test:ci -c 'chmod -R a+rwX /test-cleanup' >/dev/null 2>&1 || true; rm -rf "$ONE_NVR_MEDIA_TEST_DIR"; }
trap cleanup EXIT
mkdir -p "$ONE_NVR_MEDIA_TEST_DIR/storage/pool" "$ONE_NVR_MEDIA_TEST_DIR/evidence"
if [[ $mode == --contract ]]; then mkdir -p "$ONE_NVR_MEDIA_TEST_DIR/storage/pool/.work/zlm"; fi
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
helper=$(docker create --entrypoint /usr/local/bin/media-launcher one-nvr/media-test:ci)
docker cp "$helper:/usr/local/bin/media-launcher" "$ONE_NVR_MEDIA_TEST_DIR/launcher"
docker rm "$helper" >/dev/null
helper=
chmod 755 "$ONE_NVR_MEDIA_TEST_DIR/launcher"
"${compose[@]}" up -d camera
camera_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$("${compose[@]}" ps -q camera)")
[[ "$camera_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 1
sleep 2
if [[ $mode != --contract ]]; then
 "${compose[@]}" up -d --wait postgres
 "${compose[@]}" run --rm --no-deps --entrypoint /usr/local/bin/media-test runner "prepare-$ONE_NVR_MEDIA_TEST_MODE"
 cp "$ONE_NVR_MEDIA_TEST_DIR/evidence/zlm-probe.ini" "$ONE_NVR_MEDIA_TEST_DIR/zlm.ini"
fi
"${compose[@]}" up -d fixture runner
fixture_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$("${compose[@]}" ps -q fixture)")
[[ "$fixture_ip" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 1
printf '{"camera_cidrs":"%s/32,%s/32","denied_hosts":["runner","zlm"],"hook_host":"runner"}\n' "$camera_ip" "$fixture_ip" > "$ONE_NVR_MEDIA_TEST_DIR/egress.json"
"${compose[@]}" up -d zlm
sleep 1
"${compose[@]}" exec -T zlm /usr/local/bin/media-launcher dns-check runner
status=0
"${compose[@]}" wait runner || status=$?
"${compose[@]}" logs --no-log-prefix runner
if [[ "$status" -ne 0 ]]; then
 "${compose[@]}" exec -T zlm /usr/local/bin/media-launcher dns-check runner || true
 "${compose[@]}" logs --tail=80 zlm
fi
mkdir -p media-test-results
if [[ -f "$ONE_NVR_MEDIA_TEST_DIR/evidence/$ONE_NVR_MEDIA_TEST_MODE.json" ]]; then cp "$ONE_NVR_MEDIA_TEST_DIR/evidence/$ONE_NVR_MEDIA_TEST_MODE.json" media-test-results/; fi
# Synthetic secrets must not appear even in private component stdout.
if "${compose[@]}" logs zlm | grep -F 'isolated-media-fixture-only'; then echo 'Media log leaked fixture secret' >&2; exit 1; fi
exit "$status"
