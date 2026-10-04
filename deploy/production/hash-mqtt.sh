#!/bin/sh
set -eu
path=$1
# Do not convert an existing Mosquitto hash into a hash of that hash.
case "$(head -c 20 "$path")" in
  'one_nvr:$'*) ;;
  *) mosquitto_passwd -U "$path" ;;
esac
