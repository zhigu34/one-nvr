#!/usr/bin/env bash
# Local fast-feedback runner. Everything here runs on a development machine
# without Docker, which is what makes the edit/test loop seconds long instead
# of waiting on a CI round trip.
#
#   ./deploy/production/verify.sh          # fast tier, ~35s: types, lint, unit
#   ./deploy/production/verify.sh --full   # adds the integration suite (~15min)
#
# CI runs the same checks for the record; this script is the loop you actually
# work in. Media acceptance (real ZLM, real browser decode, 60s segments) needs
# Docker and is not covered here — see AGENTS.md "常用验证".
set -Eeuo pipefail
cd "$(dirname "$0")/../.."

full=no
case "${1:-}" in
 --full) full=yes ;;
 '') ;;
 *) printf '%s\n' 'Usage: verify.sh [--full]' >&2; exit 2 ;;
esac

# Node and Go are installed outside the default PATH on some workstations
# (~/.local/toolchain, Homebrew kegs). Find them rather than making every
# invocation remember the export.
if ! command -v node >/dev/null 2>&1; then
 for candidate in /opt/homebrew/opt/node@*/bin /usr/local/opt/node@*/bin "$HOME/.local/toolchain/node/bin"; do
  if [[ -x $candidate/node ]]; then PATH="$candidate:$PATH"; break; fi
 done
fi
if ! command -v go >/dev/null 2>&1; then
 for candidate in "$HOME/.local/toolchain/go/bin" /usr/local/go/bin; do
  if [[ -x $candidate/go ]]; then PATH="$candidate:$PATH"; break; fi
 done
fi
command -v node >/dev/null 2>&1 || { printf '%s\n' 'node not found. Install it or add it to PATH.' >&2; exit 2; }
command -v pnpm >/dev/null 2>&1 || { printf '%s\n' 'pnpm not found. Run: corepack enable' >&2; exit 2; }
command -v go >/dev/null 2>&1 || { printf '%s\n' 'go not found. Install it or add it to PATH.' >&2; exit 2; }

web=apps/web
results=()
failed=0
started=$SECONDS

# Each check reports its own verdict and timing; the summary at the end is the
# only thing that decides the exit code, so one failure never hides the others.
run() {
 local name=$1; shift
 local begin=$SECONDS
 printf '\n\033[1m▶ %s\033[0m\n' "$name"
 if "$@"; then
  local took=$((SECONDS - begin))
  results+=("PASS  ${took}s  ${name}")
 else
  local took=$((SECONDS - begin))
  results+=("FAIL  ${took}s  ${name}")
  failed=1
 fi
}

# 1. The generated API types must match the OpenAPI contract. CI fails the
#    build on drift; catching it here keeps the two in step while editing.
api_drift() {
 pnpm --dir "$web" api:generate >/dev/null
 if ! git diff --exit-code --quiet -- "$web/src/lib/api-types.ts"; then
  printf '%s\n' 'api-types.ts is stale: the OpenAPI contract changed. The regenerated file is in your tree; commit it.' >&2
  return 1
 fi
}
tsc() { pnpm --dir "$web" exec tsc -b --force; }
eslint() { pnpm --dir "$web" exec eslint src; }
vitest() { pnpm --dir "$web" test; }
go_unit() { go test -race ./internal/... ./cmd/... ./migrations; }
go_vet() { go vet ./...; }
# Two separate hazards, and the first version of this check only caught the
# second.
#
# What keeps happening: `gofmt -w` on a whole tree reformats files nobody edited,
# and `git add -A` sweeps that drift into a functional commit — four times now.
# The guard that looked for "modified file that is not gofmt-clean" cannot see
# it, because the sweep itself is what marked those files modified, and it left
# them clean. The question that actually catches it is different: are the files
# staged for this commit the ones the change was about?
#
# So this reports two things and fails on neither by default:
#   - staged Go files that are not gofmt-clean (the original check);
#   - staged Go files whose only difference from HEAD is whitespace, which is
#     the signature of a formatting sweep.
# The second is a warning rather than a failure: a deliberate formatting commit
# is legitimate, it just has to be deliberate.
gofmt_touched() {
 local staged
 staged=$(git diff --cached --name-only -- '*.go')
 local dirty
 if [[ -n $staged ]]; then
  dirty=$(printf '%s\n' "$staged" | tr '\n' '\0' | xargs -0 gofmt -l 2>/dev/null)
 fi
 if [[ -n $dirty ]]; then
  printf '%s\n' "These staged files are not gofmt-clean:"
  printf '%s\n' "$dirty"
  return 1
 fi
 # Whitespace-only staged changes: the drift signature. Compare each staged
 # file's content ignoring all whitespace against HEAD.
 local swept=()
 local file
 while IFS= read -r file; do
  [[ -n $file ]] || continue
  if git cat-file -e "HEAD:$file" 2>/dev/null; then
   local now before
   now=$(tr -d '[:space:]' < "$file" | sha256sum)
   before=$(git show "HEAD:$file" | tr -d '[:space:]' | sha256sum)
   [[ $now == "$before" ]] && swept+=("$file")
  fi
 done <<< "$staged"
 if [[ ${#swept[@]} -gt 0 ]]; then
  printf '%s\n' "note: staged files whose only change is whitespace (formatting sweep, not the change itself):"
  printf '  %s\n' "${swept[@]}"
 fi
}
run 'go vet' go_vet
run 'gofmt on modified files' gofmt_touched
if [[ $full == yes ]]; then
 run 'Integration suite (PostgreSQL)' integration
fi

printf '\n\033[1m── verify summary (%ss) ──\033[0m\n' "$((SECONDS - started))"
for line in "${results[@]}"; do
 if [[ $line == FAIL* ]]; then printf '\033[31m%s\033[0m\n' "$line"; else printf '\033[32m%s\033[0m\n' "$line"; fi
done
if [[ $full == no ]]; then
 printf '%s\n' "Run './deploy/production/verify.sh --full' for the integration suite."
fi
exit "$failed"
