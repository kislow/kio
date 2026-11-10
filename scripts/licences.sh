#!/usr/bin/env bash
#
# licences.sh - list the licence of every runtime dependency (Go, kb/, diagram/)
# and fail on a licence class the project does not ship: GPL, AGPL, LGPL, SSPL,
# BUSL, unknown or custom, unless the package is named in .licence-allow.txt.
#
# Usage: licences.sh
#
# Needs go, uv and npm. Prints "ecosystem  package  licence" per dependency, the
# allow-listed ones with their reason, and a count per ecosystem.
set -Eeuo pipefail
IFS=$'\n\t'

readonly GO_LICENSES=github.com/google/go-licenses@v1.6.0
readonly PIP_LICENSES=pip-licenses==5.5.5
readonly LICENSE_CHECKER=license-checker@25.0.1
readonly DENIED='GPL|SSPL|BUSL|BUSINESS SOURCE|UNKNOWN|UNLICENSED|CUSTOM|SEE LICENSE'

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT
TMP=""

log() { printf 'licences: %s\n' "$*" >&2; }
cleanup() { [[ -z "$TMP" ]] || rm -rf "$TMP"; }

# Each collector prints "package<TAB>licence" lines.
go_licences() {
  # Built for the host, run for linux: the release and the images are linux and
  # darwin, and procfs only compiles in on linux.
  GOBIN="$TMP/bin" go install "$GO_LICENSES"
  (cd "$ROOT" && GOOS=linux "$TMP/bin/go-licenses" report ./... 2>/dev/null) |
    awk -F, '{ print $1 "\t" $3 }'
}

python_licences() {
  (cd "$ROOT/kb" && uv sync --frozen --no-dev --quiet)
  uvx "$PIP_LICENSES" --python "$ROOT/kb/.venv/bin/python" --format=csv |
    awk -F'","' 'NR > 1 { gsub(/^"|"$/, "", $3); print $1 "\t" $3 }'
}

node_licences() {
  (cd "$ROOT/diagram" && npm ci --no-audit --no-fund --silent &&
    npx --yes "$LICENSE_CHECKER" --production --csv) |
    awk -F'","' '{ gsub(/^"/, "", $1); print $1 "\t" $2 }'
}

# Reads "ecosystem<TAB>package<TAB>licence" on stdin. A package is allowed when
# the allow-list names it, or a parent path of it (Go modules), ignoring a
# trailing @version (npm).
judge() {
  awk -F'\t' -v denied="$DENIED" '
    NR == FNR { sub(/#.*/, ""); if (NF) { split($0, w, /[ \t]+/); reason[w[1]] = substr($0, length(w[1]) + 3) } next }
    {
      name = tolower($2); sub(/@[^@\/]*$/, "", name); if (name == "") name = tolower($2)
      count[$1]++
      if (toupper($3) !~ denied && $3 != "") next
      allowed = ""
      for (a in reason) if (name == tolower(a) || index(name, tolower(a) "/") == 1) allowed = a
      if (allowed != "") { printf "allowed  %s  %s  %s: %s\n", $1, $2, $3, reason[allowed]; next }
      printf "DENIED   %s  %s  %s\n", $1, $2, ($3 == "" ? "none" : $3); bad++
    }
    END {
      for (e in count) printf "%s: %d dependencies\n", e, count[e]
      exit bad ? 1 : 0
    }' "$ROOT/.licence-allow.txt" -
}

main() {
  TMP="$(mktemp -d)"
  trap cleanup EXIT
  {
    go_licences | awk -F'\t' '{ print "go\t" $1 "\t" $2 }'
    python_licences | awk -F'\t' '{ print "python\t" $1 "\t" $2 }'
    node_licences | awk -F'\t' '{ print "node\t" $1 "\t" $2 }'
  } >"$TMP/all.tsv"
  [[ -s "$TMP/all.tsv" ]] || { log "no dependencies found"; return 1; }
  if judge <"$TMP/all.tsv"; then log "ok"; else log "a dependency has a licence the project does not ship; fix it or allow-list it with a reason in .licence-allow.txt"; return 1; fi
}

main "$@"
