package ki

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// RolePlanner is the role a bare `ki` reads to choose the session's agent.
// It takes the same shape as the other roles and the same eligibility rules
// as a delegate pick, but only reads: nothing is reserved or recorded, since
// a session is not a job.
const RolePlanner = "planner"

// DefaultSessionAgent starts when nothing else names an agent.
const DefaultSessionAgent = "claude"

// Where the session's agent came from, highest precedence first.
const (
	SourceFlag    = "flag"
	SourceEnv     = "KI_AGENT"
	SourcePlanner = "roles.planner"
	SourceDefault = "default"
)

// roleNames are the names a roles block accepts: the task classes and the planner.
var roleNames = append(append([]string(nil), Classes...), RolePlanner)

var roleList = strings.Join(roleNames, ", ")

func isRole(s string) bool {
	for _, r := range roleNames {
		if r == s {
			return true
		}
	}
	return false
}

// modelClass is the class whose model policy a role's providers fall back to:
// a planner is a session, which is judgement work.
func modelClass(role string) string {
	if role == RolePlanner {
		return ClassJudgement
	}
	return role
}

// SessionAgent is the agent a session starts and why.
type SessionAgent struct {
	Agent  string
	Source string
	// Provider is the planner role's pick, set only when Source is SourcePlanner.
	Provider Provider
	// Skipped are the planner providers passed over, set when the role was read.
	Skipped []Skipped
}

// SkipNote is a skipped planner provider as status reports it.
type SkipNote struct {
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
	Demoted  bool   `json:"demoted,omitempty"`
}

// Notes lists the skipped providers for status output.
func (s SessionAgent) Notes() []SkipNote {
	out := make([]SkipNote, 0, len(s.Skipped))
	for _, k := range s.Skipped {
		out = append(out, SkipNote{Provider: k.Provider.Name(), Reason: k.Reason, Demoted: k.Demoted})
	}
	return out
}

// NoPlannerError is the refusal when every provider of roles.planner is skipped.
type NoPlannerError struct {
	Skipped []Skipped
	Now     time.Time
}

func (e NoPlannerError) Error() string {
	reasons := make([]string, 0, len(e.Skipped))
	for _, s := range e.Skipped {
		reasons = append(reasons, s.Provider.Name()+" "+s.Reason)
	}
	msg := "roles.planner: no provider available (" + strings.Join(reasons, "; ") + ")"
	if at := BackAt(e.Skipped); !at.IsZero() {
		msg += fmt.Sprintf("; earliest back at %s", at.In(e.Now.Location()).Format(time.RFC3339))
	}
	return msg + "; name an agent with --agent to start one anyway"
}

// ResolveSessionAgent picks the session's agent: the agent given on the
// command line, else KI_AGENT, else the first eligible provider of
// roles.planner, else claude. in is read only for the planner role.
func ResolveSessionAgent(flag, envAgent string, m *Machine, in PickInput) (SessionAgent, error) {
	switch {
	case flag != "":
		return SessionAgent{Agent: flag, Source: SourceFlag}, nil
	case envAgent != "":
		return SessionAgent{Agent: envAgent, Source: SourceEnv}, nil
	}
	r, ok := m.RoleFor(RolePlanner)
	if !ok {
		return SessionAgent{Agent: DefaultSessionAgent, Source: SourceDefault}, nil
	}
	in.Role, in.R = RolePlanner, r
	try, skipped := Candidates(in)
	if len(try) == 0 {
		return SessionAgent{Skipped: skipped}, NoPlannerError{Skipped: skipped, Now: in.Now}
	}
	return SessionAgent{Agent: try[0].Agent, Source: SourcePlanner, Provider: try[0], Skipped: skipped}, nil
}

// Model is the session's model: --model, then KI_MODEL, then the picked
// provider's own, then models.judgement.<agent>, "" for the agent's default.
func (s SessionAgent) Model(flag, envModel string, m *Machine) string {
	for _, model := range []string{flag, envModel, s.Provider.Model} {
		if model != "" {
			return model
		}
	}
	return m.ModelFor(ClassJudgement, filepath.Base(s.Agent))
}

// shorthandAgents are the agents `ki <agent>` names positionally. cursor is
// not one: `ki cursor` writes Cursor's MCP config. agent is the binary name of
// Cursor's CLI, the same vendor as cursor-agent.
var shorthandAgents = []string{"claude", "codex", "agy", "cursor-agent", "agent"}

// ShorthandAgents lists the names `ki <agent>` accepts.
func ShorthandAgents() []string { return slices.Clone(shorthandAgents) }

// SplitShorthand takes a leading agent name off a session's arguments:
// `ki codex --mode auto` is `ki --agent codex --mode auto`.
func SplitShorthand(args []string) (agent string, rest []string) {
	if len(args) > 0 {
		for _, a := range shorthandAgents {
			if args[0] == a {
				return a, args[1:]
			}
		}
	}
	return "", args
}
