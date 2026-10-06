#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
root=$(mktemp -d /tmp/one-nvr-test.XXXXXX)
admin=one-nvr/app:m1a
project="one-nvr-deployment-test-${GITHUB_RUN_ID:-local}-$$"
optional_container="$project-optional"
cleanup() {
 if [[ -f $root/env ]]; then
  docker run --rm --network none --user 0:0 --mount "type=bind,source=$root/data/runtime,target=/state,readonly" --entrypoint cat "$admin" /state/compose.json > "$root/cleanup.json" 2>/dev/null || true
  if [[ -s $root/cleanup.json ]]; then docker compose -p "$project" -f "$root/cleanup.json" down --remove-orphans >/dev/null 2>&1 || true; fi
 fi
 docker run --rm --network none --user 0:0 --mount "type=bind,source=$root,target=/fixture" --entrypoint sh "$admin" -ec 'rm -rf /fixture/data /fixture/storage' || true
 rm -rf "$root"
}
trap cleanup EXIT
mkdir "$root/storage"
chmod 755 "$root" "$root/storage"
cat > "$root/env" <<ENV
ONE_NVR_PUBLIC_URL=https://127.0.0.1:18443
ONE_NVR_HTTPS_PORT=18443
ONE_NVR_RTC_PORT=18000
ONE_NVR_DATA_DIR=$root/data
ONE_NVR_STORAGE_ROOT=$root/storage
ONE_NVR_FRIGATE_ENABLE=no
ONE_NVR_OPENLIST_ENABLE=no
ENV
./deploy.sh --project "$project" --env-file "$root/env" --check
[[ ! -e $root/data ]] || { printf 'Check created production state.\n' >&2; exit 1; }
./deploy.sh --project "$project" --env-file "$root/env"
# Root Compose must expose the complete selected services.
services=$(./deploy.sh --project "$project" --env-file "$root/env" compose config --services | sort)
[[ $services == $(printf '%s\n' api gateway postgres worker zlm | sort) ]]
./deploy.sh --project "$project" --env-file "$root/env" compose ps --all
ids=$(docker ps -q --filter "label=com.docker.compose.project=$project")
[[ $(printf '%s\n' "$ids" | wc -l) -eq 5 ]]
zlm_before=$(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=zlm)
secret_before=$(docker run --rm --network none --user 0:0 --mount "type=bind,source=$root/data,target=/data,readonly" --entrypoint sha256sum "$admin" /data/secrets/one-nvr.json)
# Simulate file permissions left by filesystem defaults or a failed first run.
# The real deployment must repair only metadata, preserving every key byte.
docker run --rm --network none --user 0:0 --mount "type=bind,source=$root/data,target=/data" --entrypoint chmod "$admin" 644 /data/secrets/one-nvr.json
./deploy.sh --project "$project" --env-file "$root/env"
secret_after=$(docker run --rm --network none --user 0:0 --mount "type=bind,source=$root/data,target=/data,readonly" --entrypoint sha256sum "$admin" /data/secrets/one-nvr.json)
[[ $secret_before == "$secret_after" ]]
secret_mode=$(docker run --rm --network none --user 0:0 --mount "type=bind,source=$root/data,target=/data,readonly" --entrypoint stat "$admin" -c %a /data/secrets/one-nvr.json)
[[ $secret_mode == 600 ]]
[[ $zlm_before == $(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=zlm) ]]
# Only configuration changes; actual certificate activation remains verified by the gateway suite.
for flags in 'yes no' 'no yes' 'yes yes'; do
 read -r f o <<< "$flags"
 sed -e "s/ONE_NVR_FRIGATE_ENABLE=no/ONE_NVR_FRIGATE_ENABLE=$f/" -e "s/ONE_NVR_OPENLIST_ENABLE=no/ONE_NVR_OPENLIST_ENABLE=$o/" "$root/env" > "$root/check-env"
 ./deploy.sh --project "$project" --env-file "$root/check-env" --check
done
# An optional container failure remains outside the core; disabling stops only it.
docker run -d --name "$optional_container" --label "com.docker.compose.project=$project" --label com.docker.compose.service=openlist --entrypoint sleep "$admin" 300 >/dev/null
./deploy.sh --project "$project" --env-file "$root/env"
[[ $(docker inspect "$optional_container" --format '{{.State.Running}}') == false ]]
[[ $zlm_before == $(docker ps -q --filter "label=com.docker.compose.project=$project" --filter label=com.docker.compose.service=zlm) ]]
docker rm "$optional_container" >/dev/null
printf 'Production core, repeat deploy, module matrix and scoped disable PASS. Media continuity remains untested.\n'
