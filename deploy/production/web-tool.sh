#!/bin/sh
set -eu
export npm_config_registry="${ONE_NVR_NPM_REGISTRY:-https://registry.npmjs.org}"
if [ ! -x /tools/node_modules/.bin/pnpm ]; then
 npm install --prefix /tools --ignore-scripts --no-audit --no-fund pnpm@10.12.4
fi
[ "$(/tools/node_modules/.bin/pnpm --version)" = '10.12.4' ] || { echo 'Incorrect pnpm tool version' >&2; exit 1; }
exec /tools/node_modules/.bin/pnpm --store-dir /pnpm/store "$@"
