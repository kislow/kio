#!/usr/bin/env bash
#
# homebrew-formula.sh - render the Homebrew formula for a release.
#
# Usage: homebrew-formula.sh <version> <sha256>
#
# <version> is MAJOR.MINOR.PATCH without the v; <sha256> is the digest of the
# tag's source tarball (https://github.com/kislow/kio/archive/refs/tags/v<version>.tar.gz).
# Prints Formula/kio.rb for the tap to stdout. No network.
set -Eeuo pipefail
IFS=$'\n\t'

readonly TEMPLATE="${KI_FORMULA_TEMPLATE:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/packaging/homebrew/kio.rb.tmpl}"

die() {
  printf 'homebrew-formula: %s\n' "$*" >&2
  exit 1
}

main() {
  [[ $# -eq 2 ]] || die "usage: homebrew-formula.sh <version> <sha256>"
  local version="$1" sha256="$2"
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must be MAJOR.MINOR.PATCH, got '$version'"
  [[ "$sha256" =~ ^[0-9a-f]{64}$ ]] || die "sha256 must be 64 lowercase hex characters, got '$sha256'"
  [[ -r "$TEMPLATE" ]] || die "cannot read $TEMPLATE"
  sed -e "s/@VERSION@/${version}/g" -e "s/@SHA256@/${sha256}/g" "$TEMPLATE"
}

main "$@"
