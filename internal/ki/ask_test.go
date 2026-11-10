package ki

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseSources(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   string
		want []string
		err  string
	}{
		{in: "kb", want: []string{"kb"}},
		{in: "vault, kb,vault", want: []string{"vault", "kb"}},
		{in: "kb,gh,vault", want: []string{"kb", "gh", "vault"}},
		{in: "", err: "needs one or more"},
		{in: "kb,web", err: `unknown source "web"`},
	} {
		got, err := ParseSources(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%q: error %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%q: %v %v, want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestAskSupported(t *testing.T) {
	t.Parallel()
	for agent, want := range map[string]bool{"claude": true, "/bin/codex": true, "agent": true, "cursor-agent": true, "agy": false, "sh": false} {
		if AskSupported(agent) != want {
			t.Errorf("%s: want %v", agent, want)
		}
	}
	if !AskGitHub("claude") || AskGitHub("codex") || AskGitHub("agent") {
		t.Error("only Claude Code reads GitHub in an ask")
	}
}

func askTestSetup(sources ...string) AskSetup {
	return AskSetup{
		Sources: sources, MCPURL: "https://kb.example/", Brains: []string{"cortex", "app"},
		Vault: ReadOnlyVaultServer("/bin/ki", "/vault"),
	}
}

func TestAskArgsClaudeAllowsOnlyTheSourceTools(t *testing.T) {
	t.Parallel()
	args := AskArgs("claude", askTestSetup(SourceKB, SourceVault, SourceGitHub))
	for _, want := range []string{
		"--strict-mcp-config", "--permission-mode", "dontAsk",
		"--allowedTools=mcp__ki-kb__get_context", "--allowedTools=mcp__ki-kb__search", "--allowedTools=mcp__ki-kb__decisions_for",
		"--allowedTools=mcp__ki-vault__vault_read", "--allowedTools=Bash(gh issue view:*)", "--allowedTools=Bash(gh project item-list:*)",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("missing %q in %v", want, args)
		}
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--disallowedTools=Edit,Write,NotebookEdit") || !strings.Contains(joined, "mcp__ki-vault__vault_write") {
		t.Errorf("write tools not denied: %v", args)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--allowedTools=") && (strings.Contains(a, "create") || strings.Contains(a, "vault_write") || a == "--allowedTools=Bash") {
			t.Errorf("a writing tool is allowed: %s", a)
		}
	}

	kbOnly := strings.Join(AskArgs("claude", askTestSetup(SourceKB)), " ")
	if strings.Contains(kbOnly, "Bash(") || strings.Contains(kbOnly, "--allowedTools=mcp__ki-vault") {
		t.Errorf("sources not left out: %s", kbOnly)
	}
}

func TestAskArgsCodexIsReadOnlyWithTheTokenInTheEnvironment(t *testing.T) {
	t.Parallel()
	args := AskArgs("codex", askTestSetup(SourceKB, SourceVault))
	if !slices.Equal(args[:3], []string{"--sandbox", "read-only", "--skip-git-repo-check"}) {
		t.Fatalf("not read-only: %v", args)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		`mcp_servers.ki-kb.url="https://kb.example/mcp"`, `mcp_servers.ki-kb.bearer_token_env_var="KI_TOKEN"`,
		`mcp_servers.ki-vault.args=["vault", "mcp", "--read-only"]`, `mcp_servers.ki-vault.env={ KI_VAULT = "/vault" }`,
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}

	noVault := askTestSetup(SourceKB)
	noVault.Vault, noVault.CodexVault = MCPServer{}, true
	if joined := strings.Join(AskArgs("codex", noVault), " "); !strings.Contains(joined, `mcp_servers.ki-vault.args=["vault", "mcp", "--read-only"]`) || strings.Contains(joined, "ki-vault.command") {
		t.Errorf("Codex's own vault server not made read-only: %s", joined)
	}
}

func TestAskArgsCursorRunsInAskModeInItsSandbox(t *testing.T) {
	t.Parallel()
	if got := AskArgs("cursor-agent", askTestSetup(SourceKB)); !slices.Equal(got, []string{"--mode", "ask", "--sandbox", "enabled", "--approve-mcps"}) {
		t.Fatalf("args %v", got)
	}
	perms, _ := json.Marshal(AskCursorPermissions([]string{SourceKB, SourceVault, SourceGitHub}))
	if string(perms) != `{"permissions":{"allow":["Mcp(ki-kb:get_context)","Mcp(ki-kb:decisions_for)","Mcp(ki-kb:search)","Mcp(ki-vault:vault_read)","Mcp(ki-vault:vault_list)","Mcp(ki-vault:vault_search)"],"deny":[]}}` {
		t.Fatalf("permissions %s", perms)
	}
	if kbOnly := AskCursorPermissions([]string{SourceKB}); len(kbOnly.Permissions.Allow) != 3 {
		t.Fatalf("vault not left out: %+v", kbOnly)
	}
	if AskArgs("agy", askTestSetup(SourceKB)) != nil {
		t.Fatal("agy has no read-only flags")
	}
}

func TestAskMCPConfig(t *testing.T) {
	t.Parallel()
	set := askTestSetup(SourceKB, SourceVault)
	claude := AskMCPConfig(set, "secret", false)
	kb := claude.MCPServers[MCPServerName]
	if kb.Headers["Authorization"] != "Bearer secret" || kb.Headers["X-Ki-Brains"] != "cortex,app" || kb.Type != "http" {
		t.Fatalf("kb %+v", kb)
	}
	if v := claude.MCPServers[VaultServerName]; !slices.Equal(v.Args, []string{"vault", "mcp", "--read-only"}) || v.Env["KI_VAULT"] != "/vault" {
		t.Fatalf("vault %+v", v)
	}
	cursor, _ := json.Marshal(AskMCPConfig(set, "secret", true))
	if strings.Contains(string(cursor), "secret") || !strings.Contains(string(cursor), "Bearer ${env:KI_TOKEN}") || strings.Contains(string(cursor), `"type"`) {
		t.Fatalf("cursor config %s", cursor)
	}
	if kbOnly := AskMCPConfig(askTestSetup(SourceKB), "s", false); len(kbOnly.MCPServers) != 1 {
		t.Fatalf("vault not left out: %+v", kbOnly)
	}
}

func TestAskBrief(t *testing.T) {
	t.Parallel()
	b := AskBrief("  What is the vault pusher? ", []string{SourceVault, SourceKB})
	for _, want := range []string{"What is the vault pusher?", "the knowledge base", "the vault of notes", "read-only", "Sources:", "last four digits"} {
		if !strings.Contains(b, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(b, "gh search") || strings.Index(b, "knowledge base") > strings.Index(b, "vault of notes") {
		t.Errorf("sources wrong or out of order:\n%s", b)
	}
}

func TestSplitAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		last, answer string
		sources      []string
	}{
		{"It pushes notes.\n\nSources:\n- vault/Pusher.md\n- https://github.com/o/r/pull/1", "It pushes notes.", []string{"vault/Pusher.md", "https://github.com/o/r/pull/1"}},
		{"Nothing answers this.\n\n**Sources:**\n- none", "Nothing answers this.", []string{}},
		{"A.\n## Sources\n* docs/x.md\n", "A.", []string{"docs/x.md"}},
		{"No list here.", "No list here.", []string{}},
	} {
		answer, sources := SplitAnswer(tc.last)
		if answer != tc.answer || !slices.Equal(sources, tc.sources) {
			t.Errorf("%q: %q %q", tc.last, answer, sources)
		}
	}
}

func TestParseAskStatus(t *testing.T) {
	t.Parallel()
	for last, want := range map[string]string{
		"":                       JobFailed + " no answer in the final message",
		"A.\nSources:\n- none":   JobDone + " answered, no source cited",
		"A.\nSources:\n- a":      JobDone + " answered, 1 source",
		"A.\nSources:\n- a\n- b": JobDone + " answered, 2 sources",
	} {
		if s, d := ParseAskStatus(last); s+" "+d != want {
			t.Errorf("%q: %s %s", last, s, d)
		}
	}
}

func TestBuildLaunchWithoutInstructions(t *testing.T) {
	t.Parallel()
	l := BuildLaunch(LaunchInput{Agent: "claude", MCPConfigPath: "/m.json", Token: "t"})
	if slices.Contains(l.Argv, "--append-system-prompt-file") || !slices.Contains(l.Argv, "/m.json") {
		t.Fatalf("argv %v", l.Argv)
	}
}

func TestBuildLaunchKeepsOneSettingsFlagWhenTheModeBringsItsOwn(t *testing.T) {
	t.Parallel()
	mine := SkillGuardSettings("/bin/ki", nil)
	l := BuildLaunch(LaunchInput{Agent: "claude", MCPConfigPath: "/m.json", ModeArgs: []string{"--settings", mine}})
	n := 0
	for i, a := range l.Argv {
		if a == "--settings" {
			n++
			if l.Argv[i+1] != mine {
				t.Errorf("settings %q, want the mode's", l.Argv[i+1])
			}
		}
	}
	if n != 1 {
		t.Errorf("%d --settings flags in %v, want 1", n, l.Argv)
	}
	if l := BuildLaunch(LaunchInput{Agent: "claude", MCPConfigPath: "/m.json"}); !slices.Contains(l.Argv, ClaudeSettings) {
		t.Errorf("a session lost ki's settings: %v", l.Argv)
	}
}

func TestRoleStates(t *testing.T) {
	t.Parallel()
	m := &Machine{
		Models: map[string]map[string]string{ClassSearch: {"codex": "gpt-5-mini"}},
		Roles: map[string]Role{ClassSearch: {Providers: []Provider{
			{Agent: "claude", Model: "haiku", Weight: 1},
			{Agent: "codex", Weight: 1},
			{Agent: "agy", Weight: 1},
		}}},
	}
	now := time.Now()
	in := PickInput{Now: now, Installed: func(a string) bool { return a != "agy" }, Ledger: []Result{
		{Provider: "codex", Status: JobLaunchFailed, Started: now.Add(-time.Minute)},
	}}
	got := RoleStates(m, in)
	if len(got) != 1 {
		t.Fatalf("roles %v", got)
	}
	st := got[ClassSearch]
	want := []RoleProvider{
		{Agent: "claude", Model: "haiku", Available: true, Health: HealthOK, Ask: true},
		{Agent: "codex", Model: "gpt-5-mini", Available: true, Health: HealthSkipped, Ask: true},
		{Agent: "agy", Health: HealthSkipped, Reason: "not on PATH"},
	}
	if st.Strategy != StrategyOrdered || len(st.Providers) != 3 {
		t.Fatalf("state %+v", st)
	}
	for i, w := range want {
		p := st.Providers[i]
		if p.Reason != "" && w.Reason == "" {
			w.Reason = p.Reason
		}
		if p != w {
			t.Errorf("provider %d: %+v, want %+v", i, p, w)
		}
	}
	if st.Providers[1].Reason == "" {
		t.Errorf("no reason for the breaker: %+v", st.Providers[1])
	}
	if RoleStates(nil, in) == nil || len(RoleStates(nil, in)) != 0 {
		t.Error("a machine without roles has none")
	}
}
