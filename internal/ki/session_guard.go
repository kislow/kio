package ki

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// A ki session keeps agent work out of Claude Code's built-in subagents: they
// run on the session's vendor only, outside a worktree, the identity, the
// vendor rotation and the job ledger. A PreToolUse hook on the subagent tool
// lets the types the machine policy allows through and denies the rest.

// sessionGuardCommand is the ki subcommand the hook runs.
const sessionGuardCommand = "session guard"

// AllowAllSubagents turns the guard off when it is the only entry of the policy.
const AllowAllSubagents = "*"

// defaultSubagent is the type Claude Code runs when a call names none.
const defaultSubagent = "general-purpose"

// subagentMatcher covers the subagent tool under its current name and the one older versions use.
const subagentMatcher = "Agent|Task"

// DefaultSubagents is the policy of a machine that sets none: read-only search only.
var DefaultSubagents = []string{"Explore"}

// SubagentGuard describes the hook of one session.
type SubagentGuard struct {
	// Binary is the absolute path of the running ki.
	Binary string
	// Allow lists the subagent types the session may start.
	Allow []string
	// Worker marks a headless session (ki delegate, ask, skill run), whose
	// denial must not point at ki delegate: a worker does the work itself.
	Worker bool
}

// NewSubagentGuard is the guard the machine policy asks for, nil when it
// allows every type and no hook is registered.
func NewSubagentGuard(kiBinary string, m *Machine, worker bool) *SubagentGuard {
	allow := m.SubagentAllow()
	if slices.Contains(allow, AllowAllSubagents) {
		return nil
	}
	return &SubagentGuard{Binary: kiBinary, Allow: allow, Worker: worker}
}

// command fails closed like the skill guard's: a guard that cannot start
// blocks the call, where Claude Code would otherwise let it through.
func (g *SubagentGuard) command() string {
	cmd := shellQuote(g.Binary) + " " + sessionGuardCommand
	if g.Worker {
		cmd += " --worker"
	}
	for _, name := range g.Allow {
		cmd += " --allow " + shellQuote(name)
	}
	return cmd + " || exit 2"
}

type settingsHook struct {
	Matcher string
	Command string
}

// claudeSettingsWith is ClaudeSettings plus PreToolUse command hooks, in one
// object because Claude Code keeps only the last --settings it is given.
func claudeSettingsWith(hooks ...settingsHook) string {
	if len(hooks) == 0 {
		return ClaudeSettings
	}
	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	type matcher struct {
		Matcher string `json:"matcher"`
		Hooks   []hook `json:"hooks"`
	}
	var settings map[string]any
	_ = json.Unmarshal([]byte(ClaudeSettings), &settings) // a constant
	pre := make([]matcher, 0, len(hooks))
	for _, h := range hooks {
		pre = append(pre, matcher{Matcher: h.Matcher, Hooks: []hook{{Type: "command", Command: h.Command}}})
	}
	settings["hooks"] = map[string][]matcher{"PreToolUse": pre}
	out, _ := json.Marshal(settings)
	return string(out)
}

// SessionSettings is the --settings JSON of a Claude Code session: ClaudeSettings
// plus the subagent hook, or ClaudeSettings alone when g is nil.
func SessionSettings(g *SubagentGuard) string {
	return claudeSettingsWith(g.hooks()...)
}

func (g *SubagentGuard) hooks() []settingsHook {
	if g == nil {
		return nil
	}
	return []settingsHook{{Matcher: subagentMatcher, Command: g.command()}}
}

// RunSessionGuard answers one PreToolUse hook call for the subagent tool: it
// reads the hook's JSON from in and writes a deny reply to out for a type that
// allow does not list, nothing for one it does. Input it cannot read is denied:
// the type cannot be checked, and an allow would let any subagent through.
func RunSessionGuard(in io.Reader, out io.Writer, allow []string, worker bool) error {
	reason := SubagentDenied(in, allow, worker)
	if reason == "" {
		return nil
	}
	return writeDeny(out, "ki session", reason)
}

// SubagentDenied says why a subagent call may not run, "" when it may.
func SubagentDenied(in io.Reader, allow []string, worker bool) string {
	var call struct {
		Tool  string `json:"tool_name"`
		Input struct {
			Type string `json:"subagent_type"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&call); err != nil {
		return subagentReason("the hook input is not readable, so the subagent type cannot be checked", allow, worker)
	}
	if call.Tool != "" && call.Tool != "Agent" && call.Tool != "Task" {
		return ""
	}
	kind := call.Input.Type
	if kind == "" {
		kind = defaultSubagent
	}
	if slices.Contains(allow, AllowAllSubagents) || slices.Contains(allow, kind) {
		return ""
	}
	return subagentReason(fmt.Sprintf("a %q subagent is not allowed here", kind), allow, worker)
}

func subagentReason(why string, allow []string, worker bool) string {
	var b strings.Builder
	b.WriteString(why + ". ")
	if worker {
		b.WriteString("This is a delegated worker: do the work yourself in this session.")
	} else {
		b.WriteString("Run implementation, fixes, issues and pull requests with `ki delegate --task \"<brief>\"` and reviews with `ki skill run ki-review`.")
	}
	if len(allow) > 0 {
		fmt.Fprintf(&b, " Subagents stay available for read-only search: %s.", strings.Join(allow, ", "))
	}
	return b.String()
}
