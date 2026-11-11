#!/usr/bin/env bash
#
# setup.sh - build the fictional world the README demo runs in.
#
# Usage: source docs/demo/setup.sh <dir>
#
# <dir> is an empty directory the caller made with mktemp -d, holding a ki binary at
# <dir>/bin/ki. Everything the demo shows lives under it: a HOME, a ki configuration
# with one project (webapp, owned by acme), a local git repository, a machine.yaml
# whose execution role is claude, and a stub claude that replays a short stream.
# Sourced, it leaves HOME, PATH, KI_CONFIG, KI_CONFIG_DIR and the working directory
# pointing into that world, so nothing of the machine it runs on shows up.
#
# The real ~/.config/ki and ~/.claude are never read: HOME is replaced first, and
# PATH holds only the demo's bin and the system tools, so no real agent or gh is found.

demo_world() {
  local root="$1"
  [[ -x "$root/bin/ki" ]] || { echo "setup.sh: $root/bin/ki missing; build ki first" >&2; return 1; }

  local home="$root/home"
  local conf="$home/.config/ki" repo="$home/src/acme/webapp"
  mkdir -p "$conf/jobs" "$root/config" "$repo"

  demo_config "$root/config"
  demo_machine "$conf"
  demo_repo "$repo"
  demo_stub_agent "$root/bin/claude"
  demo_stub_gh "$root/bin/gh"

  DEMO_HOMES="$home|$(cd "$home" && pwd -P)"
  export HOME="$home" KI_CONFIG="$root/config" KI_CONFIG_DIR="$conf"
  export PATH="$root/bin:/usr/bin:/bin"
  unset GH_TOKEN GITHUB_TOKEN KI_AGENT KI_MODEL KI_MODE KI_HOME KI_MCP_URL KI_VAULT
  export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
  cd "$home" || return 1
}

demo_config() {
  local dir="$1"
  cat >"$dir/projects.yaml" <<'YAML'
identities:
  alice:
    github_user: alice
    email: alice@example.com
    credential: alice
    ingest: false

projects:
  - name: webapp
    repo: https://github.com/acme/webapp
    identity: alice
    brains: [cortex]
YAML
  printf '{ "brains": {} }\n' >"$dir/mcp-config.json"
  printf '{}\n' >"$dir/aliases.yaml"
  : >"$dir/models.env"
  : >"$dir/config.yaml"
}

demo_machine() {
  cat >"$1/machine.yaml" <<'YAML'
identities: [alice]
roles:
  execution:
    providers: [claude:sonnet]
YAML
  printf 'roots: [~/src]\n' >"$1/mecca.yaml"
}

demo_repo() {
  local repo="$1"
  git init -q -b main "$repo"
  git -C "$repo" config user.name alice
  git -C "$repo" config user.email alice@example.com
  git -C "$repo" remote add origin https://github.com/acme/webapp
  cat >"$repo/retry.go" <<'GO'
package webapp

import "time"

func Retry(attempts int, fn func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
GO
  cat >"$repo/retry_test.go" <<'GO'
package webapp

import (
	"errors"
	"testing"
	"time"
)

func TestRetryGivesUpAfterTheBudget(t *testing.T) {
	start := time.Now()
	Retry(3, func() error { return errors.New("down") })
	if time.Since(start) > 25*time.Millisecond {
		t.Fatal("retries took too long")
	}
}
GO
  git -C "$repo" add -A
  git -C "$repo" commit -q -m "feat: add retry helper"
}

# The stub prints the events ki parses from `claude --output-format stream-json`
# (assistant messages with text and tool_use blocks, then one result), edits the
# test in the worktree it was started in and commits the edit.
demo_stub_agent() {
  cat >"$1" <<'STUB'
#!/bin/sh
event() { printf '%s\n' "$1"; sleep "${2:-1}"; }
tool() { event "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"tool_use\",\"name\":\"$1\",\"input\":{\"$2\":\"$3\"}}]}}" "${4:-1}"; }
say() { event "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"$1\"}]}}" "${2:-1}"; }

sleep 1
say "Looking at the flaky retry test"
tool Read file_path retry_test.go
tool Read file_path retry.go
say "It times the retries against 25ms, so a slow runner fails it"
sed 's|time.Since(start) > 25\*time.Millisecond|calls != 3|; s|start := time.Now()|calls := 0|; s|Retry(3, func() error { return errors.New("down") })|Retry(3, func() error { calls++; return errors.New("down") })|; /"time"/d' retry_test.go >retry_test.go.new
mv retry_test.go.new retry_test.go
tool Edit file_path retry_test.go
tool Bash command "go test ./... -count=20" 2
git add retry_test.go
git commit -q -m "fix(retry): count attempts in the retry test instead of timing them"
tool Bash command "git commit -m 'fix(retry): count attempts in the retry test'"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"PR: https://github.com/acme/webapp/pull/42","session_id":"3f6c1a9e","total_cost_usd":0.04,"usage":{"input_tokens":1210,"output_tokens":486,"cache_read_input_tokens":9320}}'
STUB
  chmod +x "$1"
}

# ki asks gh for the identity's token; the demo has no GitHub account behind it.
demo_stub_gh() {
  cat >"$1" <<'STUB'
#!/bin/sh
[ "$1 $2" = "auth token" ] && echo demo-token && exit 0
echo "gh: not available in the demo" >&2
exit 1
STUB
  chmod +x "$1"
}

# ki prints the worktree's absolute path on its first line. The wrapper runs the
# real binary and shows the throwaway home as ~, so the temp directory never
# appears on screen.
ki() {
  local home
  { command ki "$@" 2>&1 1>&3 3>&- | while IFS= read -r line; do
    for home in "${DEMO_HOMES##*|}" "${DEMO_HOMES%%|*}"; do line="${line//"$home"/~}"; done
    printf '%s\n' "$line"
  done >&2; } 3>&1
}

demo_world "${1:?usage: source setup.sh <dir>}"
