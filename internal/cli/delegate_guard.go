package cli

import (
	"fmt"
	"os"

	"github.com/kislow/kio/internal/ki"
)

// delegateGuard is the hidden `ki delegate guard` the hooks of a worker's git
// call: --strip FILE removes agent attribution from a commit message in place,
// --branch NAME and --message-file FILE fail naming what matches the leak guard.
func (a *App) delegateGuard(args []string) error {
	if len(args) != 2 {
		return fail("usage: ki delegate guard (--strip FILE | --branch NAME | --message-file FILE)")
	}
	flag, value := args[0], args[1]
	switch flag {
	case "--strip":
		return stripMessageFile(value)
	case "--branch":
		return leakError(ki.LeakMatch(value))
	case "--message-file":
		msg, err := os.ReadFile(value) // #nosec G304 G703 -- a path the hook was given
		if err != nil {
			return fmt.Errorf("reading %s: %w", value, err)
		}
		return leakError(ki.LeakMatch(string(msg)))
	}
	return fail("ki delegate guard: unknown flag %s", flag)
}

// leakError is the refusal for a leak guard match, nil for "".
func leakError(match string) error {
	if match == "" {
		return nil
	}
	return fail("leak guard: %s", match)
}

func stripMessageFile(path string) error {
	msg, err := os.ReadFile(path) // #nosec G304 G703 -- the message file git hands the hook
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	stripped := ki.StripAttribution(string(msg))
	if stripped == string(msg) {
		return nil
	}
	if err := os.WriteFile(path, []byte(stripped), 0o600); err != nil { // #nosec G703 -- the message file git hands the hook
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
