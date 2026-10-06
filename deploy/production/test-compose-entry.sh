#!/usr/bin/env bash
# Configuration only: never starts a daemon, pulls an image or writes station data.
set -Eeuo pipefail
cd "$(dirname "$0")/../.."
if [[ $# -gt 0 ]]; then compose=("$@"); else compose=(docker compose); fi
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
export ONE_NVR_COMPOSE_FILE="$scratch/compose.json"
export ONE_NVR_HARDWARE_COMPOSE_FILE="$scratch/hardware.json"
for flags in 'no no' 'yes no' 'no yes' 'yes yes'; do
 read -r frigate openlist <<< "$flags"
 python3 - "$scratch" "$frigate" "$openlist" <<'PY'
import json,sys
from pathlib import Path
p=Path(sys.argv[1]); f=sys.argv[2]=='yes'; o=sys.argv[3]=='yes'
names=['api','worker','postgres','zlm','gateway']+(['frigate','mqtt'] if f else [])+(['openlist'] if o else [])
services={name:{'image':'one-nvr/config-contract:never-run'} for name in names}
services['api']['environment']={'TOKEN':'literal$$value','PLAIN':'unchanged'}
services['api']['volumes']=[{'type':'bind','source':'/srv/pool$$name','target':'/storage'}]
(p/'compose.json').write_text(json.dumps({'services':services,'networks':{'default':{}}}))
(p/'hardware.json').write_text(json.dumps({'services':{'frigate':{'devices':['/dev/dri/renderD128:/dev/dri/renderD128'],'environment':{'PROFILE':'validated-intel'}}}} if f else {'services':{}}))
PY
 "${compose[@]}" --env-file /dev/null -p one-nvr-compose-contract -f compose.yaml config --format json > "$scratch/result.json"
 "${compose[@]}" --env-file /dev/null -p one-nvr-compose-contract -f "$scratch/compose.json" -f "$scratch/hardware.json" config --format json > "$scratch/baseline.json"
 python3 - "$scratch/result.json" "$frigate" "$openlist" <<'PY'
import json,sys
from pathlib import Path
x=json.load(open(sys.argv[1])); f=sys.argv[2]=='yes'; o=sys.argv[3]=='yes'
assert x==json.load(open(Path(sys.argv[1]).with_name('baseline.json'))), 'root entry differs from direct private manifest'
expected={'api','worker','postgres','zlm','gateway'}|({'frigate','mqtt'} if f else set())|({'openlist'} if o else set())
assert set(x['services'])==expected, 'wrong module selection'
assert x['services']['api']['environment']['TOKEN']=='literal$$value','literal credential interpolated'
assert x['services']['api']['volumes'][0]['source']=='/srv/pool$$name','literal path interpolated'
assert x['name']=='one-nvr-compose-contract','wrong project name'
assert x['networks']['default']['name']=='one-nvr-compose-contract_default','include changed network namespace'
if f:
 assert x['services']['frigate']['environment']['PROFILE']=='validated-intel','lost hardware overlay'
 assert len(x['services']['frigate']['devices'])==1,'lost hardware device'
print(f'Root Compose {len(expected)} services, literal values, project and hardware PASS')
PY
done
