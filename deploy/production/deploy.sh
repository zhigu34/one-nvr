#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
cd "$(dirname "$0")/../.."
env_file=deploy/production/.env
admin_image=one-nvr/app:m1a
check=no
project=one-nvr
for ((i=1; i<=$#; i++)); do
 arg=${!i}
 case "$arg" in
  --check) check=yes ;;
  --env-file|--admin-image|--project) ((i+=1)); [[ $i -le $# ]] || exit 2; value=${!i}; if [[ $arg == --env-file ]]; then env_file=$value; elif [[ $arg == --project ]]; then project=$value; else admin_image=$value; fi ;;
  *) printf 'Unknown option: %s\n' "$arg" >&2; exit 2 ;;
 esac
done
[[ $project =~ ^[a-z0-9][a-z0-9_-]{0,63}$ ]] || { printf 'Invalid project name.\n' >&2; exit 2; }
command -v docker >/dev/null
[[ -f $env_file ]] || { printf 'Missing env file; copy deploy/production/.env.example first.\n' >&2; exit 2; }
env_file=$(cd "$(dirname "$env_file")" && printf '%s/%s' "$PWD" "$(basename "$env_file")")
[[ $admin_image == one-nvr/app:m1a || $admin_image =~ ^[a-zA-Z0-9][a-zA-Z0-9._/:-]*@sha256:[a-f0-9]{64}$ ]] || { printf 'Custom admin image must be digest pinned.\n' >&2; exit 2; }
arch=$(docker info --format '{{.Architecture}}')
[[ $arch == x86_64 || $arch == amd64 ]] || { printf 'M1-A production supports Linux amd64 only.\n' >&2; exit 2; }
source_tree=$(git rev-parse HEAD^{tree} 2>/dev/null || printf unknown)
ensure_image() {
 local image=$1
 if [[ $image == one-nvr/app:m1a || $image == one-nvr/gateway:m1a ]]; then
  local label
  label=$(docker image inspect "$image" --format '{{index .Config.Labels "org.one-nvr.source-tree"}}' 2>/dev/null || true)
  if [[ $label != "$source_tree" || $source_tree == unknown ]]; then
   local kind=${image#one-nvr/}; kind=${kind%:m1a}
   docker build --platform linux/amd64 --label "org.one-nvr.source-tree=$source_tree" -f "deploy/production/Dockerfile.$kind" -t "$image" .
  fi
 fi
 if ! docker image inspect "$image" >/dev/null 2>&1; then
  case "$image" in
   one-nvr/app:m1a) docker build --platform linux/amd64 -f deploy/production/Dockerfile.app -t "$image" . ;;
   one-nvr/gateway:m1a) docker build --platform linux/amd64 -f deploy/production/Dockerfile.gateway -t "$image" . ;;
   *) docker pull --platform linux/amd64 "$image" ;;
  esac
 fi
 [[ $(docker image inspect "$image" --format '{{.Architecture}}') == amd64 ]] || { printf 'Image architecture mismatch: %s\n' "$image" >&2; return 1; }
}
ensure_image "$admin_image"
public_value() { docker run --rm --network none --user "$(id -u):$(id -g)" --mount "type=bind,source=$env_file,target=/settings.env,readonly" --entrypoint /usr/local/bin/admin "$admin_image" env-value --env-file /settings.env --key "$1"; }
data=$(public_value data)
storage=$(public_value storage)
tls=$(public_value tls)
[[ -d $storage ]] || { printf 'Storage root must already exist: %s\n' "$storage" >&2; exit 2; }
[[ -z $tls || -d $tls ]] || { printf 'TLS input directory must already exist; it will not be created.\n' >&2; exit 2; }
# Private temporary validation output only; no production state or services changed.
if [[ $check == yes ]]; then
 tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
 docker run --rm --network none --user "$(id -u):$(id -g)" --mount "type=bind,source=$env_file,target=/settings.env,readonly" --mount "type=bind,source=$tmp,target=/output" --entrypoint /usr/local/bin/admin "$admin_image" render-deployment --env-file /settings.env --output-dir /output --check
 docker compose -p "$project" -f "$tmp/compose.json" config --quiet
 printf 'Configuration valid. Required services:\n'; cat "$tmp/services"
 exit
fi
# Only the dedicated application root is created. Pool and TLS input roots are never created.
mkdir -p "$data"
[[ ! -L $data ]] || { printf 'Data root must not be a symbolic link.\n' >&2; exit 2; }
docker run --rm --network none --user 0:0 --mount "type=bind,source=$data,target=/data" --entrypoint /usr/local/bin/admin "$admin_image" init-runtime
runtime=$data/runtime
previous=()
if [[ -f $runtime/services ]]; then
 while IFS= read -r name; do previous+=("$name"); done < <(docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/services)
fi
docker run --rm --network none --user 0:0 --mount "type=bind,source=$env_file,target=/settings.env,readonly" --mount "type=bind,source=$data,target=/data" --mount "type=bind,source=$runtime,target=/output" --entrypoint /usr/local/bin/admin "$admin_image" render-deployment --env-file /settings.env --output-dir /output
services=(); images=()
while IFS= read -r name; do services+=("$name"); done < <(docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/services)
while IFS= read -r image; do images+=("$image"); done < <(docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/images)
# Read private manifest via Docker. The local caller owns this short-lived copy.
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/compose.json > "$tmp/compose.json"
compose=(docker compose -p "$project" -f "$tmp/compose.json")
"${compose[@]}" config --quiet
for image in "${images[@]}"; do ensure_image "$image"; done
# Identify only disabled optional containers by project/service labels, never prune or down.
for name in frigate mqtt openlist; do
 selected=no; for wanted in "${services[@]}"; do [[ $wanted != "$name" ]] || selected=yes; done
 if [[ $selected == no ]]; then
  ids=$(docker ps -aq --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=$name")
  if [[ -n $ids ]]; then while IFS= read -r container; do docker stop "$container" >/dev/null; done <<< "$ids"; fi
 fi
done
# Store actual IDs/digests, including explicitly labelled offline loads, without secrets.
for image in "${images[@]}"; do
 docker image inspect "$image" --format '{{.Id}} {{.Architecture}} {{json .RepoDigests}}'
done | docker run --rm -i --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output" --entrypoint sh "$admin_image" -ec 'cat > /output/image-evidence.txt; chmod 600 /output/image-evidence.txt'
"${compose[@]}" up -d --wait postgres
"${compose[@]}" run --rm --no-deps --entrypoint /usr/local/bin/admin api migrate
"${compose[@]}" run --rm --no-deps --entrypoint /usr/local/bin/admin api bootstrap-tls
"${compose[@]}" up -d --wait api worker zlm gateway
"${compose[@]}" run --rm --no-deps --entrypoint /usr/local/bin/admin api verify-entry
optional_failed=no
if [[ " ${services[*]} " == *' frigate '* ]]; then
 # Hash the existing authentication file once; no password is exposed as a command argument.
 mqtt_image=$(docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/image-mqtt)
 docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/work" --mount "type=bind,source=$PWD/deploy/production/hash-mqtt.sh,target=/hash-mqtt.sh,readonly" --entrypoint sh "$mqtt_image" -ec 'sh /hash-mqtt.sh /work/mqtt.passwd; chown 1883:1883 /work/mqtt.passwd; chmod 600 /work/mqtt.passwd'
 if ! ./deploy/production/probe-hardware.sh "$admin_image" "$data" "$tmp/compose.json" "$project"; then optional_failed=yes; printf 'Intelligence hardware validation failed; core remains available.\n' >&2
 else
  docker run --rm --network none --user 0:0 --mount "type=bind,source=$runtime,target=/output,readonly" --entrypoint cat "$admin_image" /output/hardware.compose.json > "$tmp/hardware.compose.json"
  compose+=(-f "$tmp/hardware.compose.json")
  "${compose[@]}" config --quiet
  "${compose[@]}" up -d mqtt frigate || optional_failed=yes
 fi
fi
if [[ " ${services[*]} " == *' openlist '* ]]; then "${compose[@]}" up -d openlist || optional_failed=yes; fi
if [[ $optional_failed == yes ]]; then exit 1; fi
"${compose[@]}" run --rm --no-deps --entrypoint /usr/local/bin/admin api verify-optional
printf 'M1-A services started. Get the one-time token with:\n'
printf 'docker compose -p %q -f %q run --rm --entrypoint /usr/local/bin/admin api setup-token\n' "$project" "$runtime/compose.json"
