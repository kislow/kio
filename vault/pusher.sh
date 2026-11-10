#!/usr/bin/env bash
# Commits and pushes the synced vault with ki vault push once nothing has changed for QUIET_SECONDS,
# so a note that is still being written is never committed.
#
# git is attached to the synced folder without touching its files. A fresh clone would give every
# note a new timestamp, and Obsidian Sync could take those older notes as newer than the remote ones.
set -Eeuo pipefail

readonly VAULT_DIR="${VAULT_DIR:?VAULT_DIR must be set}"
readonly STATE_DIR="${STATE_DIR:?STATE_DIR must be set}"
readonly REMOTE="${VAULT_REMOTE:?VAULT_REMOTE must be set}"
readonly BRANCH="${VAULT_BRANCH:-main}"
readonly QUIET_SECONDS="${QUIET_SECONDS:-300}"
readonly INTERVAL_SECONDS="${INTERVAL_SECONDS:-60}"
readonly MAX_DELETIONS="${MAX_DELETIONS:-25}"
readonly KEY_SOURCE="${KEY_SOURCE:-/etc/vault-key/id_ed25519}"
readonly KEY="${KEY:-/tmp/vault-key}"

log() { printf '%s pusher: %s\n' "$(date -u +%FT%TZ)" "$*" >&2; }

wait_for() {
  local what=$1
  shift
  until "$@"; do
    log "waiting for ${what}"
    sleep "$INTERVAL_SECONDS"
  done
}

have_key() { [[ -s $KEY_SOURCE ]]; }
first_sync_done() { [[ -f "${STATE_DIR}/initial-sync-done" ]]; }

# ssh refuses a private key that others can read, and the secret volume is group-readable.
install_key() { install -m 0600 "$KEY_SOURCE" "$KEY"; }

# gitignore-style pattern for one literal path: escape what git would read as a wildcard.
literal() { printf '%s' "$1" | sed -e 's/[][*?\\]/\\&/g' -e 's/ $/\\ /'; }

# Obsidian Sync never carries dotfiles (.gitignore), config sync is off here (your main machine commits
# .obsidian/), and some tracked files may be of a type this device does not sync. Leaving them out of the
# sparse checkout keeps them exactly as they are in git instead of committing them as deleted.
keep_unsynced_out() {
  {
    printf '/*\n!/.*\n'
    git -C "$VAULT_DIR" ls-files --deleted -z | while IFS= read -r -d '' path; do
      printf '!/%s\n' "$(literal "$path")"
    done
  } | git -C "$VAULT_DIR" sparse-checkout set --no-cone --stdin
}

attach_git() {
  [[ -f "${STATE_DIR}/git-attached" ]] && return 0
  rm -rf "${VAULT_DIR}/.git" # a half-finished attach from an earlier start
  log "attaching git to the synced vault"
  git -C "$VAULT_DIR" init -q -b "$BRANCH"
  git -C "$VAULT_DIR" remote add origin "$REMOTE"
  git -C "$VAULT_DIR" fetch -q origin "$BRANCH"
  # Moves HEAD and the index to the remote branch; the files stay exactly as Obsidian Sync wrote them.
  git -C "$VAULT_DIR" reset -q "origin/${BRANCH}"
  git -C "$VAULT_DIR" branch -q --set-upstream-to "origin/${BRANCH}"
  keep_unsynced_out
  touch "${STATE_DIR}/git-attached"
}

# Epoch seconds of the most recently modified changed file; deleted files have none.
newest_change() {
  local newest=0 entry path mtime
  while IFS= read -r -d '' entry; do
    path="${VAULT_DIR}/${entry:3}"
    [[ -e $path ]] || continue
    mtime=$(stat -c %Y "$path")
    ((mtime > newest)) && newest=$mtime
  done < <(git -C "$VAULT_DIR" status --porcelain -z --untracked-files=all)
  printf '%s\n' "$newest"
}

# Notes are only ever deleted a few at a time by hand; many at once means something went wrong with sync.
deletions() { git -C "$VAULT_DIR" ls-files --deleted | wc -l; }

push_when_quiet() {
  [[ -n $(git -C "$VAULT_DIR" status --porcelain) ]] || return 0
  local deleted
  deleted=$(deletions)
  if ((deleted > MAX_DELETIONS)); then
    log "refusing to push: ${deleted} files would be deleted (limit ${MAX_DELETIONS}); check Obsidian Sync, then push by hand"
    return 0
  fi
  local quiet=$(($(date +%s) - $(newest_change)))
  if ((quiet < QUIET_SECONDS)); then
    log "changes still settling (${quiet}s quiet of ${QUIET_SECONDS}s)"
    return 0
  fi
  (cd / && ki vault push -m "vault: $(date -u '+%F %H:%M') from the cluster")
}

main() {
  wait_for "the deploy key (secret vault-deploy-key)" have_key
  install_key
  wait_for "the first full sync from Obsidian Sync" first_sync_done
  attach_git
  while true; do
    push_when_quiet || log "push failed; trying again in ${INTERVAL_SECONDS}s"
    sleep "$INTERVAL_SECONDS"
  done
}

main "$@"
