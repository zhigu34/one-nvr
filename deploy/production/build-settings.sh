#!/usr/bin/env bash
# Read the one bootstrap build setting before the Go admin image exists.
# This accepts the same literal quoting/comments as config.ReadEnv; no eval/source.
read_build_proxy() {
 local input=$1 line key raw quote rest tail value seen=no number=0
 one_nvr_build_proxy=https://goproxy.cn,direct
 trim_build_value() {
  local text=$1
  text="${text#"${text%%[![:space:]]*}"}"
  text="${text%"${text##*[![:space:]]}"}"
  printf '%s' "$text"
 }
 while IFS= read -r line || [[ -n $line ]]; do
  ((number+=1))
  [[ $line =~ ^[[:space:]]*ONE_NVR_GOPROXY[[:space:]]*= ]] || continue
  [[ $seen == no ]] || { printf 'Duplicate ONE_NVR_GOPROXY at env line %s.\n' "$number" >&2; return 2; }
  seen=yes
  raw=$(trim_build_value "${line#*=}")
  if [[ $raw == \"* || $raw == \'* ]]; then
   quote=${raw:0:1}; rest=${raw:1}
   [[ $rest == *"$quote"* ]] || { printf 'Unterminated ONE_NVR_GOPROXY quote.\n' >&2; return 2; }
   value=${rest%%"$quote"*}; tail=$(trim_build_value "${rest#*"$quote"}")
   [[ -z $tail || $tail == \#* ]] || { printf 'Trailing ONE_NVR_GOPROXY characters.\n' >&2; return 2; }
  else
   value=$(trim_build_value "${raw%%[[:space:]]\#*}")
   [[ $value != \#* ]] || value=
  fi
  [[ -z $value ]] || one_nvr_build_proxy=$value
 done < "$input"
 # One HTTPS proxy, optionally followed by ,direct. Credentials and expansions
 # do not belong in the public env file or Docker build arguments.
 [[ $one_nvr_build_proxy =~ ^https://[a-zA-Z0-9._:-]+(/[a-zA-Z0-9._~:/-]*)?(,direct)?$ ]] || {
  printf 'ONE_NVR_GOPROXY requires an HTTPS proxy URL, optionally followed by ,direct.\n' >&2
  return 2
 }
}
