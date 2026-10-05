#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
project="one-nvr-gateway-test-${GITHUB_RUN_ID:-local}-$$"
compose=(docker compose -p "$project" -f deploy/production/compose.gateway-test.yaml)
runner_pid=
cleanup() {
  if [[ -n "$runner_pid" ]]; then kill "$runner_pid" 2>/dev/null || true; fi
  "${compose[@]}" unpause worker >/dev/null 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT
trap 'exit 130' INT TERM
phase() {
  local name="$1"
  # The first phase includes three independently bounded 60s TLS operations.
  # Keep the wrapper alive long enough for the runner to report its own failure.
  local phase_deadline=$((SECONDS+240))
  while ((SECONDS<phase_deadline)); do
    if "${compose[@]}" exec -T api test -f "/data/test-phase/$name"; then return; fi
    if ! kill -0 "$runner_pid" 2>/dev/null; then wait "$runner_pid"; return 1; fi
    sleep 0.2
  done
  printf 'Gateway acceptance phase timed out: %s\n' "$name" >&2
  "${compose[@]}" logs --tail=60 api worker gateway || true
  "${compose[@]}" exec -T postgres psql -U one_nvr_test -d one_nvr_test -At -c "SELECT kind,state,error_code FROM jobs WHERE kind='tls.apply' ORDER BY created_at" || true
  return 1
}
mark() { "${compose[@]}" exec -T api touch "/data/test-phase/$1"; }
nginx_signal() {
  # PID is read from this test project's gateway, never from the host.
  "${compose[@]}" exec -T gateway sh -ec 'pid=$(cat /control/nginx.pid); case "$pid" in ""|*[!0-9]*) exit 1;; esac; kill -"$1" "$pid"' sh "$1"
}
"${compose[@]}" config --quiet
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm permissions
"${compose[@]}" run --rm runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayTLSRuntimeInit$' -count=1 -v
"${compose[@]}" up -d api worker gateway
"${compose[@]}" run --rm runner go test -tags gateway_runtime ./tests/integration -run '^TestGatewayTLSRuntimeLifecycle$' -count=1 -v &
runner_pid=$!
phase lifecycle-done
"${compose[@]}" pause worker
mark worker-paused
phase receipt-before-db
"${compose[@]}" kill -s SIGKILL gateway
"${compose[@]}" up -d --no-deps --force-recreate gateway
"${compose[@]}" unpause worker
mark gateway-recreated
phase recovery-done
nginx_signal STOP
mark nginx-stopped-for-failure
phase wrong-fingerprint-rejected
nginx_signal CONT
sleep 0.5
nginx_signal STOP
mark nginx-stopped-for-crash
phase config-switched-before-crash
"${compose[@]}" kill -s SIGKILL gateway
"${compose[@]}" up -d --no-deps --force-recreate gateway
mark crashed-gateway-recreated
phase crash-recovery-done
wait "$runner_pid"
runner_pid=
