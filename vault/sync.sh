#!/usr/bin/env bash
# Keeps VAULT_DIR identical to the remote Obsidian vault. Until someone has logged in once
# (docs/operations.md, "Vault pusher"), it waits and says how; then it pulls everything once,
# marks the first sync done for the pusher, and stays in continuous sync.
set -Eeuo pipefail

readonly VAULT_DIR="${VAULT_DIR:?VAULT_DIR must be set}"
readonly STATE_DIR="${STATE_DIR:?STATE_DIR must be set}"
readonly RETRY_SECONDS="${RETRY_SECONDS:-60}"
# The remote vault's name in Obsidian Sync and the device name this pod registers as.
readonly OBSIDIAN_VAULT="${OBSIDIAN_VAULT:-vault}"
readonly OBSIDIAN_DEVICE="${OBSIDIAN_DEVICE:-cluster}"

log() { printf '%s sync: %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }

configured() { ob sync-list-local 2>/dev/null | grep -qF -- "$VAULT_DIR"; }

wait_for_setup() {
  until configured; do
    log "not set up yet; run once: kubectl -n harness exec -it deploy/vault-pusher -c sync -- ob login"
    log "then: kubectl -n harness exec -it deploy/vault-pusher -c sync -- ob sync-setup --vault ${OBSIDIAN_VAULT} --path ${VAULT_DIR} --device-name ${OBSIDIAN_DEVICE}"
    sleep "$RETRY_SECONDS"
  done
}

main() {
  mkdir -p "$VAULT_DIR" "$STATE_DIR"
  wait_for_setup
  log "first sync of ${VAULT_DIR}"
  ob sync --path "$VAULT_DIR"
  touch "${STATE_DIR}/initial-sync-done"
  log "continuous sync"
  exec ob sync --path "$VAULT_DIR" --continuous
}

main "$@"
