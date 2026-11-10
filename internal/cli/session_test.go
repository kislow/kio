package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func settingsOf(t *testing.T, argv []string) string {
	t.Helper()
	i := slices.Index(argv, "--settings")
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("no --settings in %v", argv)
	}
	if slices.Contains(argv[i+1:], "--settings") {
		t.Fatalf("a second --settings replaces the first: %v", argv)
	}
	return argv[i+1]
}

func TestInteractiveClaudeSessionRegistersTheSubagentGuard(t *testing.T) {
	for _, tc := range []struct {
		name, machine, want string
	}{
		{"default policy", "", "'/bin/ki' session guard --allow 'Explore' || exit 2"},
		{"listed types", "identities: [work]\nsessions: {subagents: {allow: [Explore, Plan]}}\n", "'/bin/ki' session guard --allow 'Explore' --allow 'Plan' || exit 2"},
		{"none allowed", "identities: [work]\nsessions: {subagents: {allow: []}}\n", "'/bin/ki' session guard || exit 2"},
		{"wildcard turns the guard off", "identities: [work]\nsessions: {subagents: {allow: ['*']}}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "")
			e.app.Executable = func() (string, error) { return "/bin/ki", nil }
			if tc.machine != "" {
				write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), tc.machine)
			}
			if err := e.app.Run(t.Context(), []string{"--force", "--agent", fakeAgent(t, "claude")}); err != nil {
				t.Fatal(err)
			}
			settings := settingsOf(t, e.execd)
			if tc.want == "" {
				if strings.Contains(settings, "hooks") || !strings.Contains(settings, "attribution") {
					t.Errorf("settings %s, want ki's own without a hook", settings)
				}
				return
			}
			var s struct {
				Hooks struct {
					PreToolUse []struct {
						Matcher string
						Hooks   []struct{ Type, Command string }
					}
				}
			}
			if err := json.Unmarshal([]byte(settings), &s); err != nil || len(s.Hooks.PreToolUse) != 1 {
				t.Fatalf("%v: %s", err, settings)
			}
			h := s.Hooks.PreToolUse[0]
			if h.Matcher != "Agent|Task" || len(h.Hooks) != 1 || h.Hooks[0].Command != tc.want || !strings.Contains(settings, `"attribution"`) {
				t.Errorf("settings %s, want a hook %q next to ki's own settings", settings, tc.want)
			}
		})
	}
}

func TestDelegatedClaudeWorkerRegistersTheWorkerGuard(t *testing.T) {
	d := newDelegateEnv(t)
	d.app.Executable = func() (string, error) { return "/bin/ki", nil }
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if got := settingsOf(t, d.args(t)); !strings.Contains(got, "'/bin/ki' session guard --worker --allow 'Explore' || exit 2") {
		t.Errorf("worker settings %s", got)
	}
}

func TestStatusShowsTheSubagentPolicy(t *testing.T) {
	e := newEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "subagents: Explore allowed; others denied") {
		t.Errorf("status %s", e.out)
	}
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nsessions: {subagents: {allow: ['*']}}\n")
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "subagents: guard off") {
		t.Errorf("status %s", e.out)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Subagents []string `json:"subagents"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || !slices.Equal(got.Subagents, []string{"*"}) {
		t.Errorf("%v: %s", err, e.out)
	}
}

func TestSessionGuardCommand(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
		deny  string
	}{
		{"allowed", []string{"--allow", "Explore"}, `{"tool_name":"Agent","tool_input":{"subagent_type":"Explore"}}`, ""},
		{"denied", []string{"--allow", "Explore"}, `{"tool_name":"Agent","tool_input":{"subagent_type":"general-purpose"}}`, "ki delegate"},
		{"denied worker", []string{"--worker", "--allow", "Explore"}, `{"tool_name":"Task","tool_input":{}}`, "do the work yourself"},
		{"malformed fails closed", []string{"--allow", "Explore"}, `{`, "not readable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			a := &App{Stdin: strings.NewReader(tc.stdin), Stdout: &out}
			if err := a.Run(t.Context(), append([]string{"session", "guard"}, tc.args...)); err != nil {
				t.Fatal(err)
			}
			if (tc.deny == "") != (out.Len() == 0) || !strings.Contains(out.String(), tc.deny) {
				t.Errorf("reply %q, want a deny containing %q", out.String(), tc.deny)
			}
		})
	}
	a := &App{Stdin: strings.NewReader("{}"), Stdout: &bytes.Buffer{}}
	for _, args := range [][]string{{"session"}, {"session", "nope"}, {"session", "guard", "--bogus"}, {"session", "guard", "--allow"}} {
		if err := a.Run(t.Context(), args); !isUserError(err) {
			t.Errorf("%v: error %v, want a user error", args, err)
		}
	}
}
