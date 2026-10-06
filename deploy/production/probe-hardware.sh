#!/usr/bin/env bash
set -Eeuo pipefail
admin_image=$1; data=$2; compose_file=$3; project=$4
on_error() {
 docker run --rm --network none --user 0:0 --mount "type=bind,source=$data,target=/data" --entrypoint /usr/local/bin/admin "$admin_image" hardware-failed || true
}
trap on_error ERR
root=$(cd "$(dirname "$0")/../.." && pwd)
docker run --rm --network none --user 0:0 --mount type=bind,source=/sys,target=/host/sys,readonly --entrypoint /usr/local/bin/admin "$admin_image" discover-hardware | docker run --rm -i --network none --user 0:0 --mount "type=bind,source=$data,target=/data" --entrypoint sh "$admin_image" -ec 'cat > /data/hardware/inventory.json; chmod 600 /data/hardware/inventory.json'
frigate_image=$(docker run --rm --network none --user 0:0 --mount "type=bind,source=$data/runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/image-frigate)
probe() {
 local identifier=$1 node=$2 result=$data/hardware/result-$1.json
 local devices=(); [[ $node == none ]] || devices=(--device "$node:$node")
 # A bounded process inside the pinned image; no network, streams or Docker socket.
 docker run --rm --network none --cpus 2 --memory 2g --pids-limit 128 "${devices[@]}" --mount "type=bind,source=$root/deploy/production/hardware-probe.py,target=/probe.py,readonly" --entrypoint python3 "$frigate_image" /probe.py "$identifier" "$node" | docker run --rm -i --network none --user 0:0 --mount "type=bind,source=$data,target=/data" --entrypoint sh "$admin_image" -ec 'cat > "$1"; chmod 600 "$1"' sh "/data/hardware/result-$identifier.json"
}
probe cpu none
while IFS=$'\t' read -r identifier node; do [[ -z $identifier ]] || probe "$identifier" "$node"; done < <(docker run --rm --network none --user 0:0 --mount "type=bind,source=$data,target=/data,readonly" --entrypoint /usr/local/bin/admin "$admin_image" hardware-nodes)
# Reuse precisely the API service's validated deployment environment.
# Selection writes its own detailed failure report.
trap - ERR
docker compose --env-file /dev/null -p "$project" -f "$compose_file" run --rm --no-deps --user 0:0 --entrypoint /usr/local/bin/admin api select-hardware
