#!/usr/bin/env bash
# Bootstrap build settings before the Go admin image exists. Literal values only;
# quoting/comments match config.ReadEnv, never eval or source the user's env file.
trim_build_value() {
 local text=$1
 text="${text#"${text%%[![:space:]]*}"}"
 text="${text%"${text##*[![:space:]]}"}"
 printf '%s' "$text"
}
validate_forward_proxy() {
 local key=$1 value=$2 port
 local pattern='^https?://([a-zA-Z0-9][a-zA-Z0-9._-]*|\[[a-fA-F0-9:]+\])(:([0-9]{1,5}))?/?$'
 [[ -n $value ]] || return 0
 [[ $value =~ $pattern ]] || { printf '%s requires an HTTP/HTTPS proxy endpoint without credentials.\n' "$key" >&2; return 2; }
 port=${BASH_REMATCH[3]}
 [[ -z $port ]] || ((10#$port > 0 && 10#$port <= 65535)) || { printf 'Invalid port in %s.\n' "$key" >&2; return 2; }
}
read_build_settings() {
 local input=$1 line key raw quote rest tail value seen='|' number=0
 one_nvr_build_proxy=https://goproxy.cn,direct
 one_nvr_http_proxy=
 one_nvr_https_proxy=
 one_nvr_no_proxy=localhost,127.0.0.1,::1
 while IFS= read -r line || [[ -n $line ]]; do
  ((number+=1))
  [[ $line =~ ^[[:space:]]*(ONE_NVR_GOPROXY|ONE_NVR_HTTP_PROXY|ONE_NVR_HTTPS_PROXY|ONE_NVR_NO_PROXY)[[:space:]]*= ]] || continue
  key=${BASH_REMATCH[1]}
  [[ $seen != *"|$key|"* ]] || { printf 'Duplicate %s at env line %s.\n' "$key" "$number" >&2; return 2; }
  seen+="$key|"
  raw=$(trim_build_value "${line#*=}")
  if [[ $raw == \"* || $raw == \'* ]]; then
   quote=${raw:0:1}; rest=${raw:1}
   [[ $rest == *"$quote"* ]] || { printf 'Unterminated %s quote.\n' "$key" >&2; return 2; }
   value=${rest%%"$quote"*}; tail=$(trim_build_value "${rest#*"$quote"}")
   [[ -z $tail || $tail == \#* ]] || { printf 'Trailing %s characters.\n' "$key" >&2; return 2; }
  else
   value=$(trim_build_value "${raw%%[[:space:]]\#*}")
   [[ $value != \#* ]] || value=
  fi
  case "$key" in
   ONE_NVR_GOPROXY) [[ -z $value ]] || one_nvr_build_proxy=$value ;;
   ONE_NVR_HTTP_PROXY) one_nvr_http_proxy=$value ;;
   ONE_NVR_HTTPS_PROXY) one_nvr_https_proxy=$value ;;
   ONE_NVR_NO_PROXY) [[ -z $value ]] || one_nvr_no_proxy=$value ;;
  esac
 done < "$input"
 [[ $one_nvr_build_proxy =~ ^https://[a-zA-Z0-9._:-]+(/[a-zA-Z0-9._~:/-]*)?(,direct)?$ ]] || {
  printf 'ONE_NVR_GOPROXY requires an HTTPS proxy URL, optionally followed by ,direct.\n' >&2
  return 2
 }
 validate_forward_proxy ONE_NVR_HTTP_PROXY "$one_nvr_http_proxy" || return 2
 validate_forward_proxy ONE_NVR_HTTPS_PROXY "$one_nvr_https_proxy" || return 2
 local bypass_pattern='^[a-zA-Z0-9.,_:/\*-]+$'
 [[ $one_nvr_no_proxy =~ $bypass_pattern ]] || { printf 'ONE_NVR_NO_PROXY requires comma-separated hosts, IPs or CIDRs.\n' >&2; return 2; }
 # HTTP and mixed proxy ports also tunnel HTTPS via CONNECT.
 [[ -n $one_nvr_https_proxy ]] || one_nvr_https_proxy=$one_nvr_http_proxy
}
