package ki

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func loadSessions(t *testing.T, body string) (*Machine, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "machine.yaml")
	if err := os.WriteFile(path, []byte("identities: [me]\n"+body), 0o600); err != nil {
		t.Fatal(err)
	}
	return LoadMachine(path, map[string]struct{}{"me": {}})
}

func TestLoadMachineSessions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, yaml string
		want       []string
		wantErr    string
	}{
		{"absent is the default", "", []string{"Explore"}, ""},
		{"list", "sessions:\n  subagents:\n    allow: [Explore, Plan]\n", []string{"Explore", "Plan"}, ""},
		{"plugin type", "sessions: {subagents: {allow: ['acme:scout']}}\n", []string{"acme:scout"}, ""},
		{"wildcard", "sessions: {subagents: {allow: ['*']}}\n", []string{"*"}, ""},
		{"empty list allows none", "sessions: {subagents: {allow: []}}\n", []string{}, ""},
		{"no allow key", "sessions: {subagents: {}}\n", nil, "sessions.subagents.allow must be a list"},
		{"null allow", "sessions: {subagents: {allow: }}\n", nil, "sessions.subagents.allow must be a list"},
		{"no subagents key", "sessions: {}\n", nil, "sessions.subagents.allow must be a list"},
		{"unknown key", "sessions: {subagents: {allow: [Explore], deny: [x]}}\n", nil, "deny"},
		{"unknown section key", "sessions: {agents: {allow: [Explore]}}\n", nil, "agents"},
		{"wildcard with others", "sessions: {subagents: {allow: ['*', Explore]}}\n", nil, "must be the only entry"},
		{"bad name", "sessions: {subagents: {allow: ['Ex plore']}}\n", nil, "not a subagent type name"},
		{"glob other than wildcard", "sessions: {subagents: {allow: ['Exp*']}}\n", nil, "not a subagent type name"},
		{"duplicate", "sessions: {subagents: {allow: [Explore, Explore]}}\n", nil, "listed twice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := loadSessions(t, tc.yaml)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := m.SubagentAllow(); !slices.Equal(got, tc.want) {
				t.Errorf("allow %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubagentPolicyOnNilMachine(t *testing.T) {
	t.Parallel()
	var m *Machine
	if !slices.Equal(m.SubagentAllow(), DefaultSubagents) || !strings.Contains(m.SubagentPolicy(), "Explore allowed") {
		t.Errorf("nil machine: %v, %q", m.SubagentAllow(), m.SubagentPolicy())
	}
}

func TestRenderMachineKeepsSessions(t *testing.T) {
	t.Parallel()
	for _, allow := range [][]string{{"Explore", "acme:scout"}, {"*"}, {}} {
		m := Machine{Identities: []string{"me"}, Sections: map[string]any{"sessions": Sessions{Subagents: allow}}}
		path := filepath.Join(t.TempDir(), "machine.yaml")
		if err := os.WriteFile(path, []byte(RenderMachine(m)), 0o600); err != nil {
			t.Fatal(err)
		}
		back, err := LoadMachine(path, map[string]struct{}{"me": {}})
		if err != nil {
			t.Fatalf("%v: %v\n%s", allow, err, RenderMachine(m))
		}
		if !slices.Equal(back.SubagentAllow(), allow) {
			t.Errorf("policy %v read back as %v", allow, back.SubagentAllow())
		}
	}
}
