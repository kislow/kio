#!/usr/bin/env bash
#
# script.sh - one-line description of what this does.
#
# Usage: script.sh [--dry-run] <target>
#
# Requires: TOOL_NAME in PATH; EXAMPLE_VAR in the environment.
#
set -Eeuo pipefail
IFS=$'\n\t'

readonly SCRIPT_NAME="${0##*/}"
DRY_RUN=0
TMPDIR_WORK=""

# IFS is newline/tab above, so "$*" would join arguments with newlines; join with spaces here.
log() {
  local IFS=' '
  printf '%s %s: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$SCRIPT_NAME" "$*" >&2
}
die() {
  log "ERROR: $*"
  exit 1
}

cleanup() {
  if [[ -n "$TMPDIR_WORK" && -d "$TMPDIR_WORK" ]]; then
    rm -rf -- "$TMPDIR_WORK"
  fi
}
trap cleanup EXIT
trap 'die "failed at line $LINENO"' ERR

usage() {
  printf 'Usage: %s [--dry-run] <target>\n' "$SCRIPT_NAME" >&2
  exit 2
}

# run: print the command in dry-run mode, execute it otherwise.
run() {
  if ((DRY_RUN)); then
    log "dry-run:$(printf ' %q' "$@")"
  else
    "$@"
  fi
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

main() {
  local target=""

  while (($#)); do
    case "$1" in
    --dry-run) DRY_RUN=1 ;;
    -h | --help) usage ;;
    -*) die "unknown option: $1" ;;
    *) target="$1" ;;
    esac
    shift
  done

  [[ -n "$target" ]] || usage
  : "${EXAMPLE_VAR:?EXAMPLE_VAR must be set}"
  require_cmd curl

  TMPDIR_WORK="$(mktemp -d)"
  log "working on $target (dry-run=$DRY_RUN)"

  # Guard destructive paths: non-empty and inside the expected directory.
  local out_dir="/var/tmp/example/$target"
  [[ -n "$target" && "$out_dir" == /var/tmp/example/* ]] || die "refusing unsafe path: $out_dir"
  run mkdir -p -- "$out_dir"

  # Always give network calls a timeout.
  run curl --fail --silent --show-error --max-time 30 \
    --output "$TMPDIR_WORK/response.json" "https://example.invalid/$target"

  log "done"
}

main "$@"
