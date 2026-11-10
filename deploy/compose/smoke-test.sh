#!/usr/bin/env bash
#
# smoke-test.sh - bring the local stack up, ingest this checkout's origin
# (README.md and docs/ on its main branch) and ask ki-mcp for a context pack.
#
# Needs a model key in config/models.env and a read-only GitHub token in
# local/git-tokens/me. The stack runs as its own compose project (ki-local-smoke)
# with its own volumes, and is torn down on exit, volumes included, unless
# KEEP_STACK=1. docs/local-stack.md has the expected output.
#
# Environment:
#   KI_SMOKE_REPO   GitHub URL to ingest (default: the origin of this checkout)
#   SMOKE_TIMEOUT   seconds to wait for documents to reach cortex (default 1200)
#   KEEP_STACK      1 leaves the stack running afterwards
#
set -Eeuo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly REPO_ROOT="$SCRIPT_DIR/../.."
readonly LOCAL_DIR="$SCRIPT_DIR/local"
readonly PROJECT_NAME="ki-local-smoke"
readonly PROJECTS_SRC="$SCRIPT_DIR/config/projects.yaml"
readonly SMOKE_PROJECTS="$LOCAL_DIR/smoke-projects.yaml"
readonly SMOKE_PACK="$LOCAL_DIR/smoke-pack.md"
readonly TOKEN_FILE="$LOCAL_DIR/ki-token"
readonly CREDENTIAL_FILE="$LOCAL_DIR/git-tokens/me"
readonly MCP_BASE="http://127.0.0.1:${KI_MCP_PORT:-8787}"
readonly TASK="How does ki-mcp authenticate a request?"
STARTED=0

log() {
  printf '%s smoke: %s\n' "$(date -u +%H:%M:%SZ)" "$*" >&2
}

die() {
  log "FAIL: $*"
  exit 1
}

compose() {
  KI_LOCAL_PROJECTS="$SMOKE_PROJECTS" docker compose -p "$PROJECT_NAME" -f "$SCRIPT_DIR/compose.yaml" "$@"
}

mcp() {
  python3 "$SCRIPT_DIR/mcp_check.py" --url "$MCP_BASE/mcp" --token-file "$TOKEN_FILE" "$@"
}

# github_url <origin>: the https form of a GitHub remote, or fail.
github_url() {
  local url="$1"
  case "$url" in
    git@github.com:*) url="https://github.com/${url#git@github.com:}" ;;
    ssh://git@github.com/*) url="https://github.com/${url#ssh://git@github.com/}" ;;
  esac
  url="${url%.git}"
  [[ "$url" == https://github.com/*/* ]] || return 1
  printf '%s\n' "$url"
}

preflight() {
  command -v docker >/dev/null || die "docker is not installed"
  docker compose version >/dev/null 2>&1 || die "docker compose (v2) is not available"
  command -v python3 >/dev/null || die "python3 is required for mcp_check.py"
  command -v curl >/dev/null || die "curl is required"
  [[ -f "$SCRIPT_DIR/.env" && -f "$SCRIPT_DIR/config/models.env" && -s "$TOKEN_FILE" ]] ||
    die "run deploy/compose/setup.sh first"
  if grep -q 'REPLACE-ME' "$SCRIPT_DIR/config/models.env"; then
    die "config/models.env still has REPLACE-ME values: set KI_LLM_BASE_URL, KI_LLM_MODEL and KI_LLM_API_KEY"
  fi
  [[ -s "$CREDENTIAL_FILE" ]] || die "put a read-only GitHub token in local/git-tokens/me (docs/local-stack.md)"
}

# write_projects <repo>: the example projects file with this run's repository.
write_projects() {
  sed "s#^    repo: .*#    repo: $1#" "$PROJECTS_SRC" >"$SMOKE_PROJECTS"
}

cleanup() {
  local status=$?
  if [[ "$STARTED" == 1 && "${KEEP_STACK:-0}" != 1 ]]; then
    log "tearing down the stack and its volumes"
    compose down --volumes --remove-orphans >&2 || log "WARNING: compose down failed"
  fi
  rm -f "$SMOKE_PROJECTS"
  exit "$status"
}

trigger_sync() {
  local _
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
    if compose exec -T temporal temporal schedule trigger --schedule-id ki-sync-projects >/dev/null 2>&1; then
      log "sync triggered"
      return 0
    fi
    sleep 5
  done
  die "the ki-sync-projects schedule did not appear; see: docker compose -p $PROJECT_NAME logs kb-worker"
}

wait_ki_mcp() {
  local _
  for _ in $(seq 1 60); do
    if curl --silent --fail --max-time 3 "$MCP_BASE/healthz" >/dev/null; then
      log "ki-mcp answers on $MCP_BASE"
      return 0
    fi
    sleep 2
  done
  die "ki-mcp did not answer on $MCP_BASE/healthz"
}

main() {
  [[ $# -eq 0 ]] || die "usage: smoke-test.sh (takes no arguments)"
  preflight

  local repo
  repo="$(github_url "${KI_SMOKE_REPO:-$(git -C "$REPO_ROOT" remote get-url origin 2>/dev/null || true)}")" ||
    die "the origin is not a GitHub repository: set KI_SMOKE_REPO=https://github.com/OWNER/REPO"
  write_projects "$repo"
  trap cleanup EXIT

  log "starting the stack; ingesting $repo (README.md and docs/)"
  STARTED=1
  compose up -d --build --wait
  wait_ki_mcp
  trigger_sync

  mcp wait-ingest --timeout "${SMOKE_TIMEOUT:-1200}" || die "no documents reached cortex; see: docker compose -p $PROJECT_NAME logs kb-worker"
  mcp context "$TASK" >"$SMOKE_PACK" || die "get_context returned no pack"
  log "PASS: documents ingested into cortex"
  log "PASS: get_context returned a pack of $(wc -c <"$SMOKE_PACK" | tr -d ' ') bytes, written to $SMOKE_PACK"
}

main "$@"
