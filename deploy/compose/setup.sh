#!/usr/bin/env bash
#
# setup.sh - prepare the local knowledge-base stack (deploy/compose) once.
#
# Creates what compose needs and overwrites nothing that already exists:
#   .env                   FALKORDB_PASSWORD and EMBEDDING_API_KEY, random, mode 0600,
#                          and EMBEDDINGS_TAG for this machine's architecture
#   config/models.env      copied from config/models.env.example; you fill it in
#   local/tokens.json      ki-mcp's token store: one device token, as its SHA-256
#   local/ki-token         the same token in clear, for ~/.config/ki/token, mode 0600
#   local/git-tokens/      empty; one read-only GitHub token per credential goes here
#
# Usage: setup.sh
#
set -Eeuo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly SCRIPT_DIR
readonly ENV_FILE="$SCRIPT_DIR/.env"
readonly MODELS_FILE="$SCRIPT_DIR/config/models.env"
readonly MODELS_EXAMPLE="$SCRIPT_DIR/config/models.env.example"
readonly LOCAL_DIR="$SCRIPT_DIR/local"
readonly TOKENS_FILE="$LOCAL_DIR/tokens.json"
readonly KI_TOKEN_FILE="$LOCAL_DIR/ki-token"
readonly TOKEN_BRAINS='["cortex"]'

log() {
  printf '%s setup: %s\n' "$(date -u +%H:%M:%SZ)" "$*" >&2
}

die() {
  log "ERROR: $*"
  exit 1
}

require() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

random_hex() {
  openssl rand -hex 32
}

# expiry_one_year: the same instant next year, in UTC, for the token's expires field.
expiry_one_year() {
  date -u -v+1y +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 year' +%Y-%m-%dT%H:%M:%SZ
}

# write_private <path> <content>: a file only the current user can read.
write_private() {
  (umask 077 && printf '%s' "$2" >"$1")
}

# text-embeddings-inference publishes its CPU image per architecture, with no
# multi-arch manifest, so Apple Silicon and other arm64 hosts need their own tag.
embeddings_tag() {
  case "$(uname -m)" in
    arm64 | aarch64) printf 'cpu-arm64-1.9.4' ;;
    *) printf 'cpu-1.9.4' ;;
  esac
}

ensure_env_file() {
  if [[ -f "$ENV_FILE" ]]; then
    if ! grep -q '^FALKORDB_PASSWORD=.' "$ENV_FILE" || ! grep -q '^EMBEDDING_API_KEY=.' "$ENV_FILE"; then
      die "$ENV_FILE lacks FALKORDB_PASSWORD or EMBEDDING_API_KEY; fix it or remove it and run again"
    fi
    log "kept $ENV_FILE"
    return
  fi
  write_private "$ENV_FILE" "$(printf 'FALKORDB_PASSWORD=%s\nEMBEDDING_API_KEY=%s\nEMBEDDINGS_TAG=%s\n' "$(random_hex)" "$(random_hex)" "$(embeddings_tag)")"
  log "wrote $ENV_FILE (secrets, mode 0600)"
}

ensure_models_file() {
  if [[ -f "$MODELS_FILE" ]]; then
    log "kept $MODELS_FILE"
    return
  fi
  cp "$MODELS_EXAMPLE" "$MODELS_FILE"
  chmod 0600 "$MODELS_FILE"
  log "copied the example to $MODELS_FILE: fill in every REPLACE-ME"
}

ensure_tokens() {
  if [[ -f "$TOKENS_FILE" ]]; then
    [[ -s "$KI_TOKEN_FILE" ]] || die "$TOKENS_FILE exists without $KI_TOKEN_FILE; remove $LOCAL_DIR and run again"
    log "kept $TOKENS_FILE and $KI_TOKEN_FILE"
    return
  fi
  local token hash expires
  token="$(random_hex)"
  hash="$(printf '%s' "$token" | shasum -a 256 | cut -d ' ' -f 1)"
  expires="$(expiry_one_year)"
  # 0644 is safe: the file holds only a digest, and the container user must read it.
  printf '[\n  {\n    "id": "local-ki",\n    "sha256": "%s",\n    "brains": %s,\n    "expires": "%s"\n  }\n]\n' \
    "$hash" "$TOKEN_BRAINS" "$expires" >"$TOKENS_FILE"
  chmod 0644 "$TOKENS_FILE"
  write_private "$KI_TOKEN_FILE" "$token"
  log "wrote $TOKENS_FILE (digest, mode 0644) and $KI_TOKEN_FILE (mode 0600), expiring $expires"
}

warn_placeholders() {
  if grep -q 'REPLACE-ME' "$MODELS_FILE"; then
    log "WARNING: $MODELS_FILE still has REPLACE-ME values; ingest and search cannot run until they are set"
  fi
}

main() {
  [[ $# -eq 0 ]] || die "usage: setup.sh (takes no arguments)"
  require openssl
  require shasum
  # git-tokens stays 0755: the worker's uid must traverse it through the mount.
  mkdir -p "$LOCAL_DIR/git-tokens"
  chmod 0700 "$LOCAL_DIR"
  ensure_env_file
  ensure_models_file
  ensure_tokens
  warn_placeholders
  cat >&2 <<EOF

Next:
  1. set KI_LLM_BASE_URL, KI_LLM_MODEL and KI_LLM_API_KEY in $MODELS_FILE
  2. put a read-only GitHub token for each credential in $LOCAL_DIR/git-tokens/ (docs/local-stack.md)
  3. cd $SCRIPT_DIR && docker compose up -d --wait
  4. replaces an existing ki device token and configuration; back them up first:
     mkdir -p ~/.config/ki && install -m 0600 $KI_TOKEN_FILE ~/.config/ki/token
     then: ki setup config $SCRIPT_DIR/config
EOF
}

main "$@"
