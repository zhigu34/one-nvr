#!/usr/bin/env bash
set -Eeuo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"

fail() { printf '%s\n' "$*" >&2; exit 1; }
normalize_arch() {
  case "$1" in
    x86_64|amd64) printf '%s\n' amd64 ;;
    aarch64|arm64) printf '%s\n' arm64 ;;
    *) printf '%s\n' "$1" ;;
  esac
}

command -v docker >/dev/null || fail '需要 Docker Engine、Compose v2 和 Buildx。'
[[ -f .env ]] || fail '请先复制 .env.example 为 .env 并填写服务器与摄像头配置。'
# Do not source .env: camera URLs are data and may contain shell metacharacters.
docker compose config --quiet

image_arch="$(docker image inspect python:3.12-slim --format '{{.Architecture}}' 2>/dev/null)" \
  || fail '本地缺少 python:3.12-slim；请先 docker load 或通过可用网络 docker pull。脚本不会自动拉取。'
engine_arch="$(docker info --format '{{.Architecture}}')"
[[ "$(normalize_arch "$image_arch")" == "$(normalize_arch "$engine_arch")" ]] \
  || fail '本地 python:3.12-slim 与 Docker Engine 架构不匹配，请加载对应架构镜像。'

# The context's built-in docker driver shares the Engine image store. An
# isolated docker-container builder cannot rely on those local base images.
builder="$(docker context show)"
# Consume the complete output: an early awk exit can SIGPIPE buildx and make
# this assignment fail with status 141 under pipefail, even for a valid driver.
driver="$(docker buildx inspect "$builder" | awk '$1 == "Driver:" && !seen {print $2; seen=1}')"
[[ "$driver" == docker ]] || fail '未找到当前 Docker context 的 docker 驱动构建器，请检查 docker buildx ls。'
build_help="$(docker compose build --help)"
[[ "$build_help" == *--builder* ]] || fail '请升级 Compose v2：当前版本不支持 build --builder。'

printf '使用本地 python:3.12-slim，构建器 %s（docker 驱动），只构建一次共享工具镜像。\n' "$builder"
export COMPOSE_BAKE=false
export BUILDX_BUILDER="$builder"
docker compose build --builder "$builder" init
# Runtime images must already be present (including their pinned digests).
docker compose up -d --no-build --pull never
