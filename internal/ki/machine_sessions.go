package ki

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

//nolint:gochecknoinits // the file registers its own section, so deleting it removes the section
func init() {
	registerMachineSection(machineSection{
		key: "sessions",
		parse: func(path string, node ast.Node) (any, error) {
			var raw rawSessions
			if err := yaml.NodeToValue(node, &raw, yaml.Strict()); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			return parseSessions(path, raw)
		},
		render: func(b *strings.Builder, v any) {
			s := v.(Sessions)
			entries := make([]string, len(s.Subagents))
			for i, e := range s.Subagents {
				entries[i] = yamlScalar(e)
			}
			fmt.Fprintf(b, "%ssessions:\n  subagents:\n    allow: [%s]\n", sessionsComment("# "), strings.Join(entries, ", "))
		},
	})
}

// Sessions is the machine's policy for the agents ki starts.
type Sessions struct {
	// Subagents lists the built-in subagent types a Claude Code session may
	// start; ["*"] allows every type. Empty allows none.
	Subagents []string
}

type rawSessions struct {
	Subagents *struct {
		Allow *[]string `yaml:"allow"`
	} `yaml:"subagents"`
}

// subagentName is a subagent type: Claude Code names are words, plugins add `plugin:name`.
var subagentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

func parseSessions(path string, raw rawSessions) (Sessions, error) {
	if raw.Subagents == nil || raw.Subagents.Allow == nil {
		return Sessions{}, fmt.Errorf("%s: sessions.subagents.allow must be a list of subagent types (`[Explore]`, `[]` for none, `[\"*\"]` for all)", path)
	}
	allow := *raw.Subagents.Allow
	seen := map[string]bool{}
	for i, e := range allow {
		switch {
		case e == AllowAllSubagents && len(allow) > 1:
			return Sessions{}, fmt.Errorf("%s: sessions.subagents.allow[%d]: \"*\" allows every type, so it must be the only entry", path, i)
		case e != AllowAllSubagents && !subagentName.MatchString(e):
			return Sessions{}, fmt.Errorf("%s: sessions.subagents.allow[%d]: %q is not a subagent type name (letters, digits, '.', '_', ':', '-') or \"*\"", path, i, e)
		case seen[e]:
			return Sessions{}, fmt.Errorf("%s: sessions.subagents.allow: %q is listed twice", path, e)
		}
		seen[e] = true
	}
	return Sessions{Subagents: allow}, nil
}

// SubagentAllow is the subagent types a ki Claude Code session may start:
// the machine's list, else DefaultSubagents.
func (m *Machine) SubagentAllow() []string {
	if m != nil {
		if s, ok := m.Sections["sessions"].(Sessions); ok {
			return s.Subagents
		}
	}
	return DefaultSubagents
}

// SubagentPolicy is SubagentAllow in one line for ki status.
func (m *Machine) SubagentPolicy() string {
	allow := m.SubagentAllow()
	switch {
	case slices.Contains(allow, AllowAllSubagents):
		return "guard off, every type allowed (sessions.subagents.allow: [\"*\"])"
	case len(allow) == 0:
		return "none allowed; agent work goes to ki delegate and ki skill run"
	}
	return strings.Join(allow, ", ") + " allowed; others denied, agent work goes to ki delegate and ki skill run"
}

func sessionsComment(prefix string) string {
	return prefix + "Built-in subagent types a Claude Code session may start (default [Explore],\n" +
		prefix + "read-only search); the rest are denied, so agent work goes to ki delegate and\n" +
		prefix + "reviews to ki skill run. [] allows none, [\"*\"] turns the guard off.\n"
}
