package ki

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Mode is how far an agent may act without asking: ask prompts before each
// action, auto lets the agent's own reviewer approve actions, edits accepts
// file edits and runs only the shell commands on an allow-list, skip never
// asks.
type Mode string

// The modes `ki --mode` and KI_MODE accept.
const (
	ModeAsk   Mode = "ask"
	ModeAuto  Mode = "auto"
	ModeEdits Mode = "edits"
	ModeSkip  Mode = "skip"
)

var (
	// ErrUnknownMode is returned for a mode that is not ask, auto, edits or skip.
	ErrUnknownMode = errors.New("unknown mode")
	// ErrModeUnsupported means the agent has no equivalent of the mode.
	ErrModeUnsupported = errors.New("mode not supported")
)

// ParseMode validates a mode; empty means ask.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case "":
		return ModeAsk, nil
	case ModeAsk, ModeAuto, ModeEdits, ModeSkip:
		return m, nil
	}
	return "", fmt.Errorf("%w %q: use ask, auto, edits or skip", ErrUnknownMode, s)
}

// permissions maps modes to one agent's flags. A mode missing from modes has
// no equivalent; owned are the agent's flags that also decide permissions.
// allow, when set, turns one entry of the edits mode's allow-list into the
// agent's own flags; an agent without it reads the list from elsewhere.
type permissions struct {
	modes map[Mode][]string
	owned []string
	allow func(entry string) []string
}

func permissionsFor(agent string) (permissions, bool) {
	switch filepath.Base(agent) {
	case "claude":
		return permissions{
			modes: map[Mode][]string{
				ModeAsk:   {"--permission-mode", "manual"},
				ModeAuto:  {"--permission-mode", "auto"},
				ModeEdits: {"--permission-mode", "acceptEdits"},
				ModeSkip:  {"--dangerously-skip-permissions"},
			},
			owned: []string{"--permission-mode", "--dangerously-skip-permissions"},
			// --allowedTools takes several values; the joined form takes exactly
			// one, so it cannot swallow an agent arg or the prompt after it.
			allow: func(entry string) []string { return []string{"--allowedTools=Bash(" + entry + ":*)"} },
		}, true
	case "codex":
		return permissions{
			modes: map[Mode][]string{
				// Explicit approval policy, so a config.toml default of never cannot turn ask off.
				ModeAsk: {"--sandbox", "workspace-write", "--ask-for-approval", "on-request"},
				// The workspace-write sandbox keeps network access off unless set, and
				// then every request waits on the automatic reviewer; Claude Code has
				// network access in auto mode, so Codex gets it too.
				ModeAuto: {"--approve-for-me", "-c", "sandbox_workspace_write.network_access=true"},
				ModeSkip: {"--dangerously-bypass-approvals-and-sandbox"},
			},
			owned: []string{"--sandbox", "-s", "--ask-for-approval", "-a", "--approve-for-me", "--dangerously-bypass-approvals-and-sandbox"},
		}, true
	case "cursor-agent", "agent":
		return permissions{
			// cursor-agent prompts by default and has no classifier mode. Its sandbox
			// runs commands without asking but lets them write only inside the
			// workspace. Auto never adds --force: it overrides the sandbox (a write
			// outside the workspace succeeds with both, cursor-agent 2026.10.22).
			modes: map[Mode][]string{ModeAsk: {}, ModeAuto: {"--sandbox", "enabled"}, ModeSkip: {"--force"}},
			owned: []string{"--force", "-f", "--yolo", "--sandbox"},
		}, true
	case "agy":
		return permissions{
			// agy (Antigravity's CLI) prompts by default. Its --mode auto depends on the
			// account and silently falls back to prompting, which headless mode turns into
			// auto-denied tools, so ki does not offer it: a mode must mean what it says.
			// Its allow-list lives in its settings file (EnsureAgyAllow), not in flags.
			modes: map[Mode][]string{
				ModeAsk:   {},
				ModeEdits: {"--mode", "accept-edits"},
				ModeSkip:  {"--dangerously-skip-permissions"},
			},
			owned: []string{"--mode", "--dangerously-skip-permissions"},
		}, true
	}
	return permissions{}, false
}

// ModeArgs returns the flags that start agent in mode m. When the user's own
// agent args already set permissions, nothing is added and overridden is true,
// so a flag after `--` always wins and never clashes with ki's. Edits mode
// gets no allow-list here: use ModeArgsAllow.
func ModeArgs(agent string, m Mode, agentArgs []string) (args []string, overridden bool, err error) {
	return ModeArgsAllow(agent, m, agentArgs, nil)
}

// ModeArgsAllow is ModeArgs with the edits mode's allow-list (delegate.allow
// in the machine file, already validated): each entry becomes the agent's
// own flag where it takes one on the command line. Other modes ignore it.
func ModeArgsAllow(agent string, m Mode, agentArgs, allow []string) (args []string, overridden bool, err error) {
	p, known := permissionsFor(agent)
	if known && setsFlag(agentArgs, p.owned) {
		return nil, true, nil
	}
	if !known {
		if m == ModeAsk {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("%w: ki does not know %s's flags for %s mode; use --mode ask and pass the agent's own flag after --",
			ErrModeUnsupported, agent, m)
	}
	flags, ok := p.modes[m]
	if !ok {
		return nil, false, fmt.Errorf("%w: %s has no %s mode; use %s", ErrModeUnsupported, filepath.Base(agent), m, p.supported())
	}
	if m != ModeEdits || p.allow == nil {
		return flags, false, nil
	}
	out := slices.Clone(flags)
	for _, e := range allow {
		out = append(out, p.allow(e)...)
	}
	return out, false, nil
}

// SupportsMode reports whether ki knows agent's flags for m.
func SupportsMode(agent string, m Mode) bool {
	p, known := permissionsFor(agent)
	if !known {
		return m == ModeAsk
	}
	_, ok := p.modes[m]
	return ok
}

func (p permissions) supported() string {
	var names []string
	for _, m := range []Mode{ModeAsk, ModeAuto, ModeEdits, ModeSkip} {
		if _, ok := p.modes[m]; ok {
			names = append(names, string(m))
		}
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

func setsFlag(args, flags []string) bool {
	for _, a := range args {
		name, _, _ := strings.Cut(a, "=")
		if slices.Contains(flags, name) {
			return true
		}
	}
	return false
}
