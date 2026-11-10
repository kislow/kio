#!/usr/bin/env bash
#
# try-image.sh - run a pushed image on the cluster without cutting a release.
#
# Usage: try-image.sh [--dry-run] <kb|mcp|vault> <sha|tag>
#        try-image.sh [--dry-run] --restore
#
# Pauses the Argo CD application that manages the harness, sets the component's
# deployments to <registry>/<prefix>-<component>:<tag> and waits for the
# rollout. --restore lets Argo CD reconcile again; with self-heal on, it puts
# the released images back. A sha becomes sha-<sha>: main builds are tagged
# with the full sha, releases with the short one.
#
# Pausing uses the argocd.argoproj.io/skip-reconcile annotation (Argo CD 2.7+),
# not the sync policy: when an app-of-apps manages the application with
# self-heal, it would put a changed sync policy back within seconds.
#
# Requires: kubectl in PATH, pointed at the cluster (current context).
# Optional: KI_IMAGE_REGISTRY (ghcr.io/kislow, where the publish workflow pushes
# by default; set it to the registry your fork pushes to, such as
# <account>.dkr.ecr.<region>.amazonaws.com when it pushes to ECR),
# KI_ARGO_APP (harness), KI_ARGO_NAMESPACE (argocd), KI_NAMESPACE (harness),
# KI_ROLLOUT_TIMEOUT (5m), KI_IMAGE_PREFIX (kio; the prefix of your registry
# repositories, the same as the IMAGE_PREFIX the publish workflow pushes with).
#
set -Eeuo pipefail
IFS=$'\n\t'

readonly SCRIPT_NAME="${0##*/}"
readonly ARGO_APP="${KI_ARGO_APP:-harness}"
readonly ARGO_NS="${KI_ARGO_NAMESPACE:-argocd}"
readonly NS="${KI_NAMESPACE:-harness}"
readonly TIMEOUT="${KI_ROLLOUT_TIMEOUT:-5m}"
readonly PREFIX="${KI_IMAGE_PREFIX:-kio}"
readonly REGISTRY="${KI_IMAGE_REGISTRY:-ghcr.io/kislow}"
readonly SKIP='argocd.argoproj.io/skip-reconcile'
DRY_RUN=0
PAUSED=0

log() {
  local IFS=' '
  printf '%s %s: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$SCRIPT_NAME" "$*" >&2
}
die() {
  log "ERROR: $*"
  if ((PAUSED)); then
    log "Argo CD application $ARGO_NS/$ARGO_APP is still paused; undo with: $0 --restore"
  fi
  exit 1
}
trap 'die "failed at line $LINENO"' ERR

usage() {
  printf 'Usage: %s [--dry-run] <kb|mcp|vault> <sha|tag>\n       %s [--dry-run] --restore\n' "$SCRIPT_NAME" "$SCRIPT_NAME" >&2
  exit 2
}

run() {
  if ((DRY_RUN)); then
    log "dry-run:$(printf ' %q' "$@")"
  else
    "$@"
  fi
}

deployments_for() {
  case "$1" in
  kb) printf '%s\n' kb-worker kb-query ;;
  mcp) printf '%s\n' ki-mcp ;;
  vault) printf '%s\n' vault-pusher ;;
  *) return 1 ;;
  esac
}

# image_tag: a 7-40 character hex sha becomes sha-<sha>, vX.Y.Z becomes X.Y.Z,
# any other valid tag (sha-<...>, X.Y.Z) is used as it is; prints nothing for
# anything else (no failing status: it runs in a command substitution).
image_tag() {
  local ref="$1" hex='^[0-9a-f]{7,40}$' release='^v[0-9]+\.[0-9]+\.[0-9]+$' tag='^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$'
  if [[ "$ref" =~ $hex ]]; then
    printf 'sha-%s\n' "$ref"
  elif [[ "$ref" =~ $release ]]; then
    printf '%s\n' "${ref#v}"
  elif [[ "$ref" =~ $tag ]]; then
    printf '%s\n' "$ref"
  fi
}

# app <verb> [args]: kubectl on the Argo CD application (resource first, as annotate needs).
app() {
  local verb="$1"
  shift
  kubectl -n "$ARGO_NS" "$verb" "applications.argoproj.io/$ARGO_APP" "$@"
}

restore() {
  app get >/dev/null || die "no Argo CD application $ARGO_NS/$ARGO_APP in context $(kubectl config current-context)"
  run app annotate "${SKIP}-"
  run app annotate --overwrite argocd.argoproj.io/refresh=normal
  log "resumed $ARGO_NS/$ARGO_APP; Argo CD now reconciles it"
  if [[ "$(app get -o 'jsonpath={.spec.syncPolicy.automated.selfHeal}')" != "true" ]]; then
    log "self-heal is off for $ARGO_APP: sync it in Argo CD to put the released images back"
  fi
  log "watch the images return: kubectl -n $NS get deployments -o wide"
}

try_image() {
  local component="$1" ref="$2" tag image names d line cname cimage
  local -a deployments=() changes=()
  deployments_for "$component" >/dev/null || die "unknown component: $component (kb, mcp or vault)"
  tag="$(image_tag "$ref")"
  [[ -n "$tag" ]] || die "not a sha or an image tag: $ref"
  image="${REGISTRY%/}/$PREFIX-$component:$tag"
  for d in $(deployments_for "$component"); do deployments+=("$d"); done
  names="$(printf '%s ' "${deployments[@]}")"
  names="${names% }"

  log "context $(kubectl config current-context): deployments $names in $NS -> $image"
  app get >/dev/null || die "no Argo CD application $ARGO_NS/$ARGO_APP; set KI_ARGO_APP"
  for d in "${deployments[@]}"; do
    kubectl -n "$NS" get "deployment/$d" >/dev/null || die "no deployment $NS/$d"
  done

  run app annotate --overwrite "$SKIP=true"
  if ((! DRY_RUN)); then
    PAUSED=1
    log "paused $ARGO_NS/$ARGO_APP"
  fi

  for d in "${deployments[@]}"; do
    changes=()
    # Only the containers that run this component's image; any other container keeps its own.
    for line in $(kubectl -n "$NS" get "deployment/$d" -o 'jsonpath={range .spec.template.spec.containers[*]}{.name} {.image}{"\n"}{end}'); do
      cname="${line%% *}"
      cimage="${line#* }"
      [[ "$cimage" == *"$PREFIX-$component:"* || "$cimage" == *"$PREFIX-$component@"* ]] && changes+=("$cname=$image")
    done
    ((${#changes[@]})) || die "deployment $NS/$d has no $PREFIX-$component container"
    run kubectl -n "$NS" set image "deployment/$d" "${changes[@]}"
  done

  if ((DRY_RUN)); then
    log "dry-run: nothing changed"
    return 0
  fi
  for d in "${deployments[@]}"; do
    kubectl -n "$NS" rollout status "deployment/$d" --timeout "$TIMEOUT" ||
      die "deployment $NS/$d did not roll out; check: kubectl -n $NS get pods"
    [[ "$(kubectl -n "$NS" get "deployment/$d" -o 'jsonpath={.spec.template.spec.containers[*].image}')" == *"$image"* ]] ||
      die "deployment $NS/$d no longer runs $image: Argo CD put it back (does it honour $SKIP?)"
  done
  log "running $image in $names ($NS)"
  log "undo: $0 --restore (Argo CD self-heal puts the released images back)"
}

main() {
  local restore_only=0
  local -a args=()
  while (($#)); do
    case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --restore) restore_only=1 ;;
    -h | --help) usage ;;
    -*) die "unknown option: $1" ;;
    *) args+=("$1") ;;
    esac
    shift
  done
  command -v kubectl >/dev/null 2>&1 || die "missing required command: kubectl"

  if ((restore_only)); then
    ((${#args[@]} == 0)) || usage
    restore
  else
    ((${#args[@]} == 2)) || usage
    try_image "${args[0]}" "${args[1]}"
  fi
}

main "$@"
