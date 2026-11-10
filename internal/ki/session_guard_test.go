package ki

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestSubagentDenied(t *testing.T) {
	t.Parallel()
	explore := []string{"Explore"}
	for _, tc := range []struct {
		name   string
		in     string
		allow  []string
		worker bool
		want   string // substring of the reason, "" for allowed
	}{
		{"allowed type", `{"tool_name":"Agent","tool_input":{"subagent_type":"Explore","prompt":"x"}}`, explore, false, ""},
		{"older tool name", `{"tool_name":"Task","tool_input":{"subagent_type":"Explore"}}`, explore, false, ""},
		{"denied type", `{"tool_name":"Agent","tool_input":{"subagent_type":"general-purpose"}}`, explore, false, `"general-purpose" subagent is not allowed`},
		{"absent type is general-purpose", `{"tool_name":"Agent","tool_input":{"prompt":"x"}}`, explore, false, `"general-purpose"`},
		{"absent type allowed when listed", `{"tool_name":"Agent","tool_input":{}}`, []string{"general-purpose"}, false, ""},
		{"empty type is general-purpose", `{"tool_name":"Task","tool_input":{"subagent_type":""}}`, explore, false, `"general-purpose"`},
		{"type match is exact", `{"tool_name":"Agent","tool_input":{"subagent_type":"explore"}}`, explore, false, `"explore"`},
		{"wildcard", `{"tool_name":"Agent","tool_input":{"subagent_type":"general-purpose"}}`, []string{"*"}, false, ""},
		{"empty policy denies all", `{"tool_name":"Agent","tool_input":{"subagent_type":"Explore"}}`, nil, false, "not allowed"},
		{"another tool is not decided", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, explore, false, ""},
		{"missing tool name is checked", `{"tool_input":{"subagent_type":"general-purpose"}}`, explore, false, "not allowed"},
		{"not json", `nope`, explore, false, "not readable"},
		{"empty input", ``, explore, false, "not readable"},
		{"type is not a string", `{"tool_name":"Agent","tool_input":{"subagent_type":7}}`, explore, false, "not readable"},
		{"interactive reason names ki delegate", `{"tool_name":"Agent","tool_input":{}}`, explore, false, "`ki delegate --task"},
		{"interactive reason names the review command", `{"tool_name":"Agent","tool_input":{}}`, explore, false, "ki skill run ki-review"},
		{"interactive reason names what stays", `{"tool_name":"Agent","tool_input":{}}`, explore, false, "read-only search: Explore"},
		{"worker reason does the work itself", `{"tool_name":"Agent","tool_input":{}}`, explore, true, "do the work yourself"},
	} {
		got := SubagentDenied(strings.NewReader(tc.in), tc.allow, tc.worker)
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: reason %q, want %q", tc.name, got, tc.want)
		}
		if tc.worker && strings.Contains(got, "ki delegate") {
			t.Errorf("%s: a worker is told to delegate: %q", tc.name, got)
		}
	}
}

func TestRunSessionGuardReply(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	if err := RunSessionGuard(strings.NewReader(`{"tool_name":"Agent","tool_input":{"subagent_type":"Explore"}}`), &out, []string{"Explore"}, false); err != nil || out.Len() != 0 {
		t.Fatalf("an allowed type printed %q (err %v), want nothing", out.String(), err)
	}
	if err := RunSessionGuard(strings.NewReader(`{"tool_name":"Agent","tool_input":{}}`), &out, []string{"Explore"}, false); err != nil {
		t.Fatal(err)
	}
	var h skillGuardHook
	if err := json.Unmarshal([]byte(out.String()), &h); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if h.Specific.Event != "PreToolUse" || h.Specific.Decision != "deny" || !strings.HasPrefix(h.Specific.Reason, "ki session: ") {
		t.Errorf("reply %+v", h)
	}
}

type settingsJSON struct {
	Attribution map[string]string `json:"attribution"`
	Hooks       struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct{ Type, Command string }
		}
	}
}

func parseSettings(t *testing.T, s string) settingsJSON {
	t.Helper()
	var out settingsJSON
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	if _, ok := out.Attribution["commit"]; !ok {
		t.Errorf("ki's own settings are lost: %s", s)
	}
	return out
}

func hookFor(t *testing.T, s settingsJSON, matcher string) string {
	t.Helper()
	for _, m := range s.Hooks.PreToolUse {
		if m.Matcher == matcher && len(m.Hooks) == 1 && m.Hooks[0].Type == "command" {
			return m.Hooks[0].Command
		}
	}
	t.Fatalf("no %q hook in %+v", matcher, s)
	return ""
}

func TestSessionSettings(t *testing.T) {
	t.Parallel()
	if got := SessionSettings(nil); got != ClaudeSettings {
		t.Errorf("with the guard off the settings are %s, want ClaudeSettings", got)
	}
	g := &SubagentGuard{Binary: "/opt/my ki/ki'x", Allow: []string{"Explore", "acme:scout"}}
	s := parseSettings(t, SessionSettings(g))
	if len(s.Hooks.PreToolUse) != 1 {
		t.Fatalf("settings %+v", s)
	}
	want := `'/opt/my ki/ki'\''x' session guard --allow 'Explore' --allow 'acme:scout' || exit 2`
	if got := hookFor(t, s, "Agent|Task"); got != want {
		t.Errorf("interactive hook %q, want %q", got, want)
	}
	g.Worker = true
	if got := hookFor(t, parseSettings(t, SessionSettings(g)), "Agent|Task"); !strings.Contains(got, "session guard --worker --allow 'Explore'") || !strings.HasSuffix(got, "|| exit 2") {
		t.Errorf("worker hook %q", got)
	}
}

func TestSkillRunSettingsCarryBothHooks(t *testing.T) {
	t.Parallel()
	s := parseSettings(t, SkillGuardSettings("/bin/ki", &SubagentGuard{Binary: "/bin/ki", Allow: DefaultSubagents, Worker: true}))
	if got := hookFor(t, s, "Bash"); got != "'/bin/ki' skill guard || exit 2" {
		t.Errorf("bash hook %q", got)
	}
	if got := hookFor(t, s, "Agent|Task"); got != "'/bin/ki' session guard --worker --allow 'Explore' || exit 2" {
		t.Errorf("subagent hook %q", got)
	}
	args := SkillClaudeArgs(nil, nil, "/bin/ki", &SubagentGuard{Binary: "/bin/ki", Allow: DefaultSubagents, Worker: true})
	if n := len(slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a != "--settings" })); n != 1 {
		t.Errorf("%d --settings flags in %v, want 1", n, args)
	}
}

func TestNewSubagentGuardFollowsThePolicy(t *testing.T) {
	t.Parallel()
	if g := NewSubagentGuard("/bin/ki", nil, false); g == nil || !slices.Equal(g.Allow, DefaultSubagents) || g.Worker {
		t.Errorf("no machine: %+v, want the default policy", g)
	}
	off := &Machine{Sections: map[string]any{"sessions": Sessions{Subagents: []string{"*"}}}}
	if g := NewSubagentGuard("/bin/ki", off, true); g != nil {
		t.Errorf("wildcard still registers a guard: %+v", g)
	}
	two := &Machine{Sections: map[string]any{"sessions": Sessions{Subagents: []string{"Explore", "Plan"}}}}
	if g := NewSubagentGuard("/bin/ki", two, true); g == nil || !g.Worker || !slices.Equal(g.Allow, []string{"Explore", "Plan"}) {
		t.Errorf("listed types: %+v", g)
	}
}

func TestBuildLaunchTakesTheSessionSettings(t *testing.T) {
	t.Parallel()
	settings := SessionSettings(&SubagentGuard{Binary: "/bin/ki", Allow: DefaultSubagents})
	l := BuildLaunch(LaunchInput{Agent: "claude", MCPConfigPath: "/m.json", Settings: settings})
	i := slices.Index(l.Argv, "--settings")
	if i < 0 || l.Argv[i+1] != settings || slices.Contains(l.Argv, ClaudeSettings) {
		t.Errorf("argv %v does not carry the session settings", l.Argv)
	}
}
