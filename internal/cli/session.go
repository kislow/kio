package cli

import (
	"fmt"
	"os"

	"github.com/kislow/kio/internal/ki"
)

// session holds the hidden commands the agents' hooks start.
func (a *App) session(args []string) error {
	if len(args) > 0 && args[0] == "guard" {
		return a.sessionGuard(args[1:])
	}
	return fail("unknown session command: only the hook command `ki session guard` exists")
}

// sessionGuard is the PreToolUse hook on Claude Code's subagent tool: it reads
// the hook's JSON on stdin and prints a deny reply for a subagent type the
// session may not start. The types come as --allow flags, written into the
// hook command when the session starts. Started by the agent, not by hand.
func (a *App) sessionGuard(args []string) error {
	var allow []string
	worker := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--worker":
			worker = true
		case args[i] == "--allow" && i+1 < len(args):
			allow = append(allow, args[i+1])
			i++
		default:
			return fail("session guard: unknown argument %q", args[i])
		}
	}
	in := a.Stdin
	if in == nil {
		in = os.Stdin
	}
	if err := ki.RunSessionGuard(in, a.Stdout, allow, worker); err != nil {
		return fmt.Errorf("session guard: %w", err)
	}
	return nil
}
