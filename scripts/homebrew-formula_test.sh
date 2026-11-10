#!/usr/bin/env bash
#
# homebrew-formula_test.sh - check that homebrew-formula.sh renders a valid formula.
#
# Usage: homebrew-formula_test.sh
#
# Renders a fixture version and digest, checks every placeholder was replaced,
# the url and sha256 landed, bad input is refused, and (when ruby is present)
# that the result parses with `ruby -c`.
set -Eeuo pipefail
IFS=$'\n\t'

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
readonly RENDER="$ROOT/scripts/homebrew-formula.sh"
readonly VERSION=1.2.3
readonly SHA256=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
TMP="$(mktemp -d)"
readonly TMP
trap 'rm -rf -- "$TMP"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

formula="$TMP/kio.rb"
"$RENDER" "$VERSION" "$SHA256" >"$formula"

! grep -q '@[A-Z0-9_]*@' "$formula" || fail "a placeholder was left in the formula"
grep -qF "https://github.com/kislow/kio/archive/refs/tags/v${VERSION}.tar.gz" "$formula" || fail "url missing the version"
grep -qF "sha256 \"${SHA256}\"" "$formula" || fail "sha256 missing"
grep -q '^class Kio < Formula$' "$formula" || fail "class name"

upper="$(printf '%s' "$SHA256" | tr 'a-f' 'A-F')"
refuse() {
  if "$RENDER" "$@" >/dev/null 2>&1; then fail "accepted bad input: $*"; fi
}
refuse
refuse "$VERSION"
refuse 1.2 "$SHA256"
refuse v1.2.3 "$SHA256"
refuse "$VERSION" abc
refuse "$VERSION" "$upper"

if command -v ruby >/dev/null 2>&1; then
  ruby -c "$formula" >/dev/null || fail "ruby -c rejected the formula"
else
  printf 'skip: ruby not found, syntax not checked\n' >&2
fi
printf 'ok\n'
