package ki

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Hooks ki gives a worker's git, so a commit carries no agent attribution and a
// push no job detail. They are reached through the environment (core.hooksPath
// in GIT_CONFIG_*), never written to a git config file, and live in the job's
// folder, so nothing lands in a commit or in the repository's own config.

// HookGuardCommand is the hidden subcommand the hooks call.
const HookGuardCommand = "delegate guard"

const hookHead = `#!/bin/sh
KI=%s
[ -x "$KI" ] || { echo "ki: $KI not found, the leak guard cannot run" >&2; exit 1; }
`

// commitMsgHook strips attribution from the message, then runs the repo's own hook.
const commitMsgHook = hookHead + `"$KI" ` + HookGuardCommand + ` --strip "$1" || exit 1
` + chainRepoHook

// prePushHook rejects a push whose branch name or commit messages match the leak
// guard, then runs the repo's own hook with the same stdin.
const prePushHook = hookHead + `in=$(mktemp) || exit 1
msg=$(mktemp) || exit 1
trap 'rm -f "$in" "$msg"' EXIT
cat > "$in"
while read -r local_ref local_sha remote_ref remote_sha; do
  case "$local_sha" in *[!0]*) ;; *) continue ;; esac
  case "$remote_ref" in
    refs/heads/*) "$KI" ` + HookGuardCommand + ` --branch "${remote_ref#refs/heads/}" || exit 1 ;;
  esac
  commits=$(git rev-list "$remote_sha..$local_sha" 2>/dev/null) || commits=$(git rev-list "$local_sha" --not --remotes) || exit 1
  for c in $commits; do
    git log -1 --format=%%B "$c" > "$msg" || exit 1
    "$KI" ` + HookGuardCommand + ` --message-file "$msg" || exit 1
  done
done < "$in"
exec < "$in"
` + chainRepoHook

// chainRepoHook runs the repository's hook of the same name, when it has one.
const chainRepoHook = `REPO=%s
if [ -x "$REPO" ]; then "$REPO" "$@"; exit $?; fi
exit 0
`

// chainHook hands a hook ki does not own to the repository's own copy.
const chainHook = `#!/bin/sh
exec %s "$@"
`

// WriteHooks writes the job's hooks into dir: the two ki owns and a pass-through
// for every other executable hook in repoHooks (the directory the repository's
// hooks come from), so pointing git at dir loses none of them.
func WriteHooks(dir, kiBinary, repoHooks string) error {
	if kiBinary == "" {
		return errors.New("the ki binary is unknown, so the leak guard hooks cannot call it")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	owned := map[string]string{
		"commit-msg": commitMsgHook,
		"pre-push":   prePushHook,
	}
	own := func(name string) string { return shellQuote(filepath.Join(repoHooks, name)) }
	for name, body := range owned {
		script := fmt.Sprintf(body, shellQuote(kiBinary), own(name))
		if err := writeHook(filepath.Join(dir, name), script); err != nil {
			return err
		}
	}
	entries, _ := os.ReadDir(repoHooks) // no hooks folder means nothing to pass through
	for _, e := range entries {
		name := e.Name()
		info, err := e.Info()
		if _, ours := owned[name]; ours || err != nil || info.IsDir() || info.Mode()&0o111 == 0 || strings.HasSuffix(name, ".sample") {
			continue
		}
		if err := writeHook(filepath.Join(dir, name), fmt.Sprintf(chainHook, own(name))); err != nil {
			return err
		}
	}
	return nil
}

func writeHook(path, script string) error {
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- a hook must be executable
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// HooksEnv is the environment that points git at the hooks in dir: one more
// config entry after any GIT_CONFIG_COUNT already in base.
func HooksEnv(base []string, dir string) map[string]string {
	n := 0
	for _, kv := range base {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			if c, err := strconv.Atoi(v); err == nil && c > 0 {
				n = c
			}
		}
	}
	idx := strconv.Itoa(n)
	return map[string]string{
		"GIT_CONFIG_COUNT":        strconv.Itoa(n + 1),
		"GIT_CONFIG_KEY_" + idx:   "core.hooksPath",
		"GIT_CONFIG_VALUE_" + idx: dir,
	}
}
