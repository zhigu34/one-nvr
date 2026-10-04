#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
command -v docker >/dev/null || { printf '%s\n' 'Docker with Compose is required.' >&2; exit 1; }
[[ $# -gt 0 ]] || { printf '%s\n' 'Usage: dev.sh test-go|test-db|web|e2e [arguments]' >&2; exit 2; }
mode=$1; shift
compose=(docker compose --project-name one-nvr-dev --file deploy/production/compose.test.yaml)
case "$mode" in
 test-go)
  [[ $# -gt 0 ]] || set -- ./...
  "${compose[@]}" run --rm go go test "$@"
  ;;
 test-db)
  [[ -z ${DATABASE_URL:-} && -z ${ONE_NVR_DATABASE_URL:-} && -z ${TEST_DATABASE_URL:-} ]] || { printf '%s\n' 'Refusing external database variables: test-db creates an isolated database.' >&2; exit 2; }
  test_project="one-nvr-test-$(date +%s)-$$"
  compose=(docker compose --project-name "$test_project" --file deploy/production/compose.test.yaml)
  cleanup() { "${compose[@]}" down --volumes --remove-orphans; }
  trap cleanup EXIT
  "${compose[@]}" up --detach --wait postgres
  [[ $# -gt 0 ]] || set -- ./tests/integration
  "${compose[@]}" run --rm -e TEST_DATABASE_URL=postgres://one_nvr_test:isolated-test-only@postgres:5432/one_nvr_test?sslmode=disable go go test "$@"
  ;;
 web) "${compose[@]}" run --rm web "$@" ;;
 e2e) exec ./deploy/production/test-e2e.sh "$@" ;;
 *) printf '%s\n' 'Unknown development command.' >&2; exit 2 ;;
esac
