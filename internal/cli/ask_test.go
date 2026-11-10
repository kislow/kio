package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// askToken is the knowledge base token in ask tests: distinct enough that
// finding it anywhere it should not be is a real leak.
const askToken = "kbtoken-5f0c1e"

// askAnswer is a cited answer as the agent's final message.
const askAnswer = "The vault pusher commits and pushes notes.\n\nSources:\n- vault/Vault Pusher.md\n- docs/cluster-setup.md"

func claudeAnswer(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": text, "session_id": "a1",
		"total_cost_usd": 0.01, "usage": map[string]int{"input_tokens": 5, "output_tokens": 9},
	})
	return string(b)
}

func cursorAnswer(text string) string {
	b, _ := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": text, "session_id": "c1",
		"usage": map[string]int{"inputTokens": 7, "outputTokens": 3},
	})
	return string(b)
}

// newAskEnv is a delegate environment set up for questions: a token worth
// looking for, a vault, the work identity carried and a gh login.
func newAskEnv(t *testing.T, machine string) *delegateEnv {
	t.Helper()
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "token"), askToken+"\n")
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), machine)
	d.vars["KI_VAULT"] = t.TempDir()
	d.app.GitHubToken = func(context.Context, string) string { return "ghtoken-77" }
	return d
}

func (d *delegateEnv) ask(args ...string) error {
	d.out.Reset()
	return d.app.Run(context.Background(), append([]string{"ask"}, args...))
}

func (d *delegateEnv) askResult(t *testing.T) askOutput {
	t.Helper()
	var r askOutput
	if err := json.Unmarshal(d.out.Bytes(), &r); err != nil {
		t.Fatalf("not an answer: %v\n%s", err, d.out.String())
	}
	return r
}

func TestAskOnClaudeIsReadOnlyCitedAndLeavesNoToken(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  search: {claude: haiku}\n")
	mcpCopy := filepath.Join(t.TempDir(), "mcp.json")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	d.worker["COPY_MCP_CONFIG"] = mcpCopy
	if err := d.ask("What is the vault pusher and how is it deployed?", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if r.Status != ki.JobDone || r.Agent != "claude" || r.Model != "haiku" || r.Answer != "The vault pusher commits and pushes notes." ||
		!slices.Equal(r.Sources, []string{"vault/Vault Pusher.md", "docs/cluster-setup.md"}) || r.Tokens.Output != 9 || r.CostUSD != 0.01 || r.Detail != "" {
		t.Fatalf("answer %+v", r)
	}

	args := d.args(t)
	for _, want := range []string{
		"--strict-mcp-config", "--permission-mode", "dontAsk", "--model", "haiku", "--print",
		"--allowedTools=mcp__ki-kb__search", "--allowedTools=mcp__ki-vault__vault_search", "--allowedTools=Bash(gh pr view:*)",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	if slices.Contains(args, "--append-system-prompt-file") || slices.Contains(args, "auto") {
		t.Errorf("session instructions or a write mode passed: %v", args)
	}
	if task := args[len(args)-1]; !strings.HasPrefix(task, "Answer this question:\n\nWhat is the vault pusher") || !strings.Contains(task, "Sources:") {
		t.Errorf("brief %q", task)
	}

	jobDir := filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", r.ID)
	if cwd := d.cwd(t); filepath.Base(cwd) != "scratch" || !strings.HasSuffix(filepath.Dir(cwd), r.ID) {
		t.Errorf("ran in %s, want the job's scratch folder", cwd)
	}
	for _, gone := range []string{"scratch", "mcp.json"} {
		if _, err := os.Stat(filepath.Join(jobDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", gone, err)
		}
	}
	raw, err := os.ReadFile(mcpCopy)
	if err != nil {
		t.Fatal(err)
	}
	var cfg ki.MCPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if kb := cfg.MCPServers[ki.MCPServerName]; kb.Headers["X-Ki-Brains"] != "cortex,app,voice" || kb.Headers["Authorization"] != "Bearer "+askToken {
		t.Errorf("kb server %+v", kb)
	}
	if v := cfg.MCPServers[ki.VaultServerName]; !slices.Equal(v.Args, []string{"vault", "mcp", "--read-only"}) {
		t.Errorf("vault server is not the read-only one: %+v", v)
	}

	jobs := d.ledger(t)
	if len(jobs) != 1 {
		t.Fatalf("ledger %+v", jobs)
	}
	j := jobs[0]
	if j.Kind != ki.KindAsk || j.Title != "What is the vault pusher and how is it deployed?" || j.Repo != "" || j.Worktree != "" ||
		j.Status != ki.JobDone || j.Detail != "answered, 2 sources" || j.Model != "haiku" || j.Provider != "claude" {
		t.Fatalf("ledger record %+v", j)
	}

	// The token reaches the agent through its MCP config only.
	leaks := map[string]string{"argv": strings.Join(args, " "), "stdout": d.out.String(), "stderr": d.app.Stderr.(interface{ String() string }).String()}
	for _, f := range []string{ki.LedgerFile, filepath.Join(r.ID, "result.json"), filepath.Join(r.ID, "output.log"), filepath.Join(r.ID, "brief.md")} {
		b, _ := os.ReadFile(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", f))
		leaks[f] = string(b)
	}
	for where, text := range leaks {
		if strings.Contains(text, askToken) {
			t.Errorf("the token is in %s", where)
		}
	}
}

func TestAskScrubsATokenTheAgentPrinted(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer("The token is " + askToken + ".\n\nSources:\n- none")
	if err := d.ask("print the token", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if strings.Contains(d.out.String(), askToken) || !strings.Contains(r.Answer, "[redacted]") {
		t.Fatalf("answer %+v", r)
	}
	for _, f := range []string{filepath.Join(r.ID, "result.json"), filepath.Join(r.ID, "output.log")} {
		if b, _ := os.ReadFile(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", f)); strings.Contains(string(b), askToken) {
			t.Errorf("the token is in %s", f)
		}
	}
}

func TestAskOnCursorRunsInAskModeWithoutGitHub(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	mcpCopy := filepath.Join(t.TempDir(), "cursor-mcp.json")
	d.worker["RESULT"] = cursorAnswer(askAnswer)
	d.worker["COPY_CURSOR_MCP"] = mcpCopy
	if err := d.ask("What is the vault pusher?", "--agent", "agent", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if r.Status != ki.JobDone || r.Agent != "agent" || len(r.Sources) != 2 {
		t.Fatalf("answer %+v", r)
	}
	args := d.args(t)
	for _, want := range []string{"--mode", "ask", "--sandbox", "enabled", "--approve-mcps", "--trust"} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	if slices.Contains(args, "--force") || slices.Contains(args, "--yolo") {
		t.Errorf("Cursor allowed to run everything: %v", args)
	}
	raw, err := os.ReadFile(mcpCopy)
	if err != nil {
		t.Fatalf("no Cursor MCP config in the scratch folder: %v", err)
	}
	if strings.Contains(string(raw), askToken) || !strings.Contains(string(raw), "${env:KI_TOKEN}") || !strings.Contains(string(raw), "--read-only") {
		t.Errorf("Cursor MCP config %s", raw)
	}
	if perms, err := os.ReadFile(mcpCopy + ".cli"); err != nil || !strings.Contains(string(perms), "Mcp(ki-kb:search)") || strings.Contains(string(perms), "Shell") {
		t.Errorf("Cursor permissions %s %v", perms, err)
	}
	if task := args[len(args)-1]; strings.Contains(task, "gh search") {
		t.Errorf("GitHub offered to an agent without network: %s", task)
	}
	if !strings.Contains(d.app.Stderr.(interface{ String() string }).String(), "gh left out") {
		t.Error("no note that GitHub was left out")
	}
	if strings.Contains(strings.Join(args, " "), askToken) {
		t.Error("the token is in argv")
	}
}

func TestAskOnCodexDefinesReadOnlyServers(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = codexEvents
	d.worker["LAST"] = "It pushes.\nSources:\n- vault/Pusher.md"
	if err := d.ask("q?", "--agent", "codex", "--source", "kb,vault", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.askResult(t); r.Status != ki.JobDone || r.Answer != "It pushes." {
		t.Fatalf("answer %+v", r)
	}
	joined := strings.Join(d.args(t), " ")
	for _, want := range []string{"exec", "--sandbox read-only", "--skip-git-repo-check", `mcp_servers.ki-vault.args=["vault", "mcp", "--read-only"]`, `bearer_token_env_var="KI_TOKEN"`} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, askToken) {
		t.Error("the token is in argv")
	}
}

func TestAskRefusesASourceTheAgentCannotRead(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	err := d.ask("q?", "--agent", "agent", "--source", "gh")
	if !isUserError(err) || !strings.Contains(err.Error(), "--source gh") {
		t.Fatalf("want refusal, got %v", err)
	}
	delete(d.vars, "KI_VAULT")
	err = d.ask("q?", "--source", "vault")
	if !isUserError(err) || !strings.Contains(err.Error(), "KI_VAULT is not set") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestAskPicksFromTheSearchRoleAndSkipsAgy(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nroles:\n  search:\n    providers: [agy, claude:sonnet]\n  execution:\n    providers: [codex]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask("q?", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if r.Agent != "claude" || r.Model != "sonnet" || r.Role != ki.ClassSearch {
		t.Fatalf("answer %+v", r)
	}
	if !strings.Contains(d.app.Stderr.(interface{ String() string }).String(), "agy skipped for the question") {
		t.Error("agy not reported as skipped")
	}
}

func TestAskWithoutASearchRoleUsesTheDefaultAgent(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nroles:\n  execution:\n    providers: [codex]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask("q?", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.askResult(t); r.Agent != "claude" || r.Role != "" {
		t.Fatalf("answer %+v", r)
	}
}

func TestAskFailsWithoutAnAnswer(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer("")
	err := d.ask("q?", "--json")
	if !isUserError(err) || !strings.Contains(err.Error(), "FAILED") {
		t.Fatalf("want failure, got %v", err)
	}
	if r := d.askResult(t); r.Status != ki.JobFailed || r.Detail == "" || r.Sources == nil {
		t.Fatalf("answer %+v", r)
	}
}

func TestAskPrintsMarkdownWithoutJSON(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask("what", "is", "it?"); err != nil {
		t.Fatal(err)
	}
	if d.out.String() != askAnswer+"\n" {
		t.Fatalf("output %q", d.out.String())
	}
	if task := d.args(t)[len(d.args(t))-1]; !strings.Contains(task, "\n\nwhat is it?\n\n") {
		t.Fatalf("question not joined: %q", task)
	}
}

func TestDelegateListLeavesAsksOut(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask("q?", "--json"); err != nil {
		t.Fatal(err)
	}
	id := d.askResult(t).ID
	if err := d.delegate("ls"); err != nil || strings.Contains(d.out.String(), id) {
		t.Fatalf("ls shows the ask: %v\n%s", err, d.out.String())
	}
	if err := d.delegate("ls", "--all"); err != nil || !strings.Contains(d.out.String(), id) || strings.Contains(d.out.String(), "DONE  .") {
		t.Fatalf("ls --all hides the ask: %v\n%s", err, d.out.String())
	}
	if err := d.delegate("ls", "--json", "--all"); err != nil || !strings.Contains(d.out.String(), `"kind":"ask"`) {
		t.Fatalf("ls --json --all: %v\n%s", err, d.out.String())
	}
	if err := d.delegate("ls", "--bogus"); !isUserError(err) {
		t.Fatalf("want usage error, got %v", err)
	}
}

func TestAskBrainsAndIdentity(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	mcpCopy := filepath.Join(t.TempDir(), "mcp.json")
	d.worker["COPY_MCP_CONFIG"] = mcpCopy
	if err := d.ask("q?", "--brain", "voice", "--identity", "work", "--json"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mcpCopy)
	if !strings.Contains(string(raw), `"X-Ki-Brains": "cortex,voice"`) {
		t.Fatalf("brains not from --brain: %s", raw)
	}
	if err := d.ask("q?", "--identity", "nobody"); !isUserError(err) || !strings.Contains(err.Error(), `unknown identity "nobody"`) {
		t.Fatalf("want refusal, got %v", err)
	}
	if err := d.ask("q?", "--brain", "a:b"); !isUserError(err) {
		t.Fatalf("want refusal of a bad brain, got %v", err)
	}
}

func TestAskBrainsLeaveSensitiveOut(t *testing.T) {
	t.Parallel()
	cfg := mustProjects(t, `sensitive_brains: [client]
identities:
  work: {github_user: w, email: w@x.example, credential: work, vault: true, ingest: true}
  me: {github_user: m, email: m@x.example, credential: me, vault: true, ingest: true}
projects:
  - {name: a, repo: https://github.com/w/a, identity: work, brains: [acme, client]}
  - {name: b, repo: https://github.com/w/b, identity: work, brains: [acme, voice]}
  - {name: c, repo: https://github.com/m/c, identity: me, brains: [home]}
`)
	if got := askBrains(cfg, ki.Session{}, "work", nil); !slices.Equal(got, []string{"cortex", "acme", "voice"}) {
		t.Fatalf("brains %v", got)
	}
	if got := askBrains(cfg, ki.Session{}, "work", []string{"client"}); !slices.Equal(got, []string{"cortex", "client"}) {
		t.Fatalf("named brains %v", got)
	}
	work := cfg.Identities["work"]
	repo := ki.Session{Identity: &work, Brains: []string{"acme", "client"}}
	if got := askBrains(cfg, repo, "work", nil); !slices.Equal(got, []string{"cortex", "acme", "client"}) {
		t.Fatalf("the repo's session brains not used: %v", got)
	}
	if got := askBrains(cfg, repo, "me", nil); !slices.Equal(got, []string{"cortex", "home"}) {
		t.Fatalf("another identity's repo decided the brains: %v", got)
	}
}

func TestAskIdentityNeedsAChoiceWithTwo(t *testing.T) {
	d := newAskEnv(t, "identities: [work, me]\n")
	write(t, filepath.Join(d.harness, "deploy", "config", "projects.yaml"), strings.Replace(projectsYAML,
		"identities:\n", "identities:\n  me: {github_user: me, email: me@x.example, credential: me, vault: true, ingest: true}\n", 1))
	if err := d.ask("q?"); !isUserError(err) || !strings.Contains(err.Error(), "pass --identity (work, me)") {
		t.Fatalf("want a choice, got %v", err)
	}
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [me]\n")
	if err := d.ask("q?", "--identity", "work"); !isUserError(err) || !strings.Contains(err.Error(), "not carried") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func mustProjects(t *testing.T, yaml string) *projects.Config {
	t.Helper()
	cfg, err := projects.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParseAskArgs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{nil, "usage: ki ask"},
		{[]string{"q", "--role", "search", "--agent", "claude"}, "exclusive"},
		{[]string{"q", "--model", "haiku"}, "--model goes with --agent"},
		{[]string{"q", "--bogus"}, "unknown flag --bogus"},
		{[]string{"q", "--source", "web"}, `unknown source "web"`},
		{[]string{"q", "--timeout", "soon"}, "--timeout"},
		{[]string{"q", "--brain"}, "--brain needs a value"},
		{[]string{"q", "--continue", "latest"}, "not a job id"},
		{[]string{"q", "--continue"}, "--continue needs a value"},
	} {
		if _, err := parseAskArgs(tc.args, "claude"); err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.err)
		}
	}
	aa, err := parseAskArgs([]string{"--agent", "codex", "--model", "m", "why?", "--source", "kb", "--quiet"}, "claude")
	if err != nil || aa.question != "why?" || aa.agent != "codex" || !aa.agentGiven || aa.model != "m" || !slices.Equal(aa.sources, []string{"kb"}) || !aa.sourcesGiven || !aa.quiet {
		t.Fatalf("parsed %+v %v", aa, err)
	}
}

func TestStatusJSONListsRoles(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  search: {claude: haiku}\neffort:\n  search: low\nroles:\n  search:\n    strategy: round-robin\n    providers: [claude, missing-agent:x]\n")
	if err := d.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Roles  map[string]ki.RoleState      `json:"roles"`
		Models map[string]map[string]string `json:"models"`
		Effort map[string]string            `json:"effort"`
	}
	if err := json.Unmarshal(d.out.Bytes(), &st); err != nil {
		t.Fatalf("%v\n%s", err, d.out.String())
	}
	search := st.Roles[ki.ClassSearch]
	if search.Strategy != ki.StrategyRoundRobin || len(search.Providers) != 2 {
		t.Fatalf("search role %+v", search)
	}
	if p := search.Providers[0]; p != (ki.RoleProvider{Agent: "claude", Model: "haiku", Available: true, Health: ki.HealthOK, Ask: true}) {
		t.Errorf("claude %+v", p)
	}
	if p := search.Providers[1]; p.Available || p.Health != ki.HealthSkipped || p.Reason != "not on PATH" || p.Model != "x" || p.Ask {
		t.Errorf("missing agent %+v", p)
	}
	if st.Models[ki.ClassSearch]["claude"] != "haiku" || st.Effort[ki.ClassSearch] != "low" {
		t.Errorf("models %v effort %v", st.Models, st.Effort)
	}
}

func TestParseAskArgsContinueRefusesTheScope(t *testing.T) {
	t.Parallel()
	const id = "j-20261107-063902-d85f"
	for _, flags := range [][]string{
		{"--role", "search"}, {"--agent", "claude"}, {"--model", "haiku"}, {"--brain", "app"}, {"--identity", "work"}, {"--source", "kb"},
	} {
		_, err := parseAskArgs(append([]string{"and prod?", "--continue", id}, flags...), "claude")
		want := "--continue keeps the conversation's scope: drop " + flags[0]
		if err == nil || err.Error() != want {
			t.Errorf("%v: %v, want %q", flags, err, want)
		}
	}
	aa, err := parseAskArgs([]string{"--continue", id, "and prod?", "--json"}, "claude")
	if err != nil || aa.cont != id || aa.question != "and prod?" || !aa.json {
		t.Fatalf("parsed %+v %v", aa, err)
	}
}

const (
	firstQuestion = "What is the vault pusher?"
	followUp      = "and for prod?"
)

// claudeAnswerAs is claudeAnswer with a chosen session id; "" leaves it out.
func claudeAnswerAs(text, session string) string {
	m := map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "total_cost_usd": 0.01, "usage": map[string]int{"input_tokens": 5, "output_tokens": 9}}
	if session != "" {
		m["session_id"] = session
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (d *delegateEnv) runs(t *testing.T, file string) int {
	t.Helper()
	b, _ := os.ReadFile(file)
	return strings.Count(string(b), "x")
}

func TestAskContinueResumesTheSessionWithTheSameScope(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  search: {claude: haiku}\n")
	mcpCopy := filepath.Join(t.TempDir(), "mcp.json")
	d.worker["COPY_MCP_CONFIG"] = mcpCopy
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask(firstQuestion, "--json"); err != nil {
		t.Fatal(err)
	}
	first := d.askResult(t)
	if first.Continues != "" || first.Resumed != "" || strings.Contains(d.out.String(), "continues") || strings.Contains(d.out.String(), "resumed") {
		t.Fatalf("a first question gained the follow-up fields: %s", d.out.String())
	}
	firstCwd := d.cwd(t)

	d.worker["RESULT"] = claudeAnswerAs("In prod it runs the same.\n\nSources:\n- docs/operations.md", "a2")
	if err := d.ask(followUp, "--continue", first.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if r.Status != ki.JobDone || r.Continues != first.ID || r.Resumed != ki.ResumedSession || r.ID == first.ID || r.Agent != "claude" || r.Model != "haiku" ||
		r.Answer != "In prod it runs the same." {
		t.Fatalf("follow-up %+v", r)
	}
	args := d.args(t)
	at := slices.Index(args, "--resume")
	if at < 0 || args[at+1] != "a1" {
		t.Errorf("session not resumed: %v", args)
	}
	for _, want := range []string{"--strict-mcp-config", "dontAsk", "--model", "haiku", "--allowedTools=mcp__ki-kb__search"} {
		if !slices.Contains(args, want) {
			t.Errorf("read-only argument %q missing from %v", want, args)
		}
	}
	if slices.Contains(args, "--allowedTools=mcp__ki-vault__vault_write") || slices.Contains(args, "acceptEdits") {
		t.Errorf("a write tool or mode on a follow-up: %v", args)
	}
	if cwd := d.cwd(t); cwd != firstCwd || !strings.HasSuffix(cwd, filepath.Join(first.ID, "scratch")) {
		t.Errorf("ran in %s, want the conversation's scratch folder %s", cwd, firstCwd)
	}
	if task := args[len(args)-1]; !strings.HasPrefix(task, "Answer this question:\n\n"+followUp) || !strings.Contains(task, "Sources:") {
		t.Errorf("follow-up brief %q", task)
	}
	raw, err := os.ReadFile(mcpCopy)
	if err != nil {
		t.Fatal(err)
	}
	var cfg ki.MCPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if kb := cfg.MCPServers[ki.MCPServerName]; kb.Headers["X-Ki-Brains"] != "cortex,app,voice" {
		t.Errorf("brains %+v", kb)
	}
	if strings.Contains(d.stderr(), "resumed by transcript") {
		t.Errorf("a session resume reported a transcript: %s", d.stderr())
	}

	jobs := d.ledger(t)
	if len(jobs) != 2 || jobs[1].Continues != first.ID || jobs[1].Resumed != ki.ResumedSession || jobs[1].Kind != ki.KindAsk || jobs[1].Ask != nil {
		t.Fatalf("ledger %+v", jobs)
	}
	saved, err := ki.ReadResult(d.jobsDir(), r.ID)
	if err != nil || saved.Continues != first.ID || saved.Ask == nil || saved.Ask.Question != followUp || saved.Ask.Identity != "work" ||
		!slices.Equal(saved.Ask.Sources, []string{"kb", "vault", "gh"}) || !strings.HasSuffix(firstCwd, saved.Ask.Scratch) {
		t.Fatalf("saved %+v %v", saved, err)
	}
}

func TestAskContinueByTranscriptWhenTheAgentLeftNoSession(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswerAs(askAnswer, "")
	if err := d.ask(firstQuestion, "--json"); err != nil {
		t.Fatal(err)
	}
	first := d.askResult(t)
	if err := d.ask(followUp, "--continue", first.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.askResult(t)
	if r.Resumed != ki.ResumedTranscript || r.Continues != first.ID || r.Status != ki.JobDone {
		t.Fatalf("follow-up %+v", r)
	}
	args := d.args(t)
	if slices.Contains(args, "--resume") {
		t.Errorf("resumed a session that was never recorded: %v", args)
	}
	task := args[len(args)-1]
	for _, want := range []string{"Question: " + firstQuestion, "The vault pusher commits and pushes notes.", "Sources:\n- vault/Vault Pusher.md", followUp} {
		if !strings.Contains(task, want) {
			t.Errorf("%q missing from the prompt %q", want, task)
		}
	}
	if !strings.HasPrefix(task, "This continues an earlier conversation") || strings.Index(task, firstQuestion) > strings.Index(task, followUp) {
		t.Errorf("turns do not come first: %q", task)
	}
	if got := strings.Count(d.stderr(), "ki: resumed by transcript"); got != 1 {
		t.Errorf("stderr says it %d times: %s", got, d.stderr())
	}
}

func TestAskContinueFallsBackToTheTranscriptWhenTheSessionIsGone(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	runs := filepath.Join(t.TempDir(), "runs")
	d.worker["RUNS_FILE"] = runs
	d.worker["RESULT"] = claudeAnswerAs("It pushes.\n\nSources:\n- a.md", "s1")
	if err := d.ask(firstQuestion, "--json"); err != nil {
		t.Fatal(err)
	}
	first := d.askResult(t)
	d.worker["RESULT"] = claudeAnswerAs("Same in prod.\n\nSources:\n- b.md", "s2")
	if err := d.ask(followUp, "--continue", first.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	second := d.askResult(t)
	if second.Resumed != ki.ResumedSession {
		t.Fatalf("second turn %+v", second)
	}

	d.worker["FAIL_ON_RESUME"] = "1"
	d.worker["RESULT"] = claudeAnswerAs("Staging differs.\n\nSources:\n- c.md", "s3")
	if err := d.ask("and staging?", "--continue", second.ID, "--json"); err != nil {
		t.Fatal(err)
	}
	third := d.askResult(t)
	if third.Resumed != ki.ResumedTranscript || third.Continues != second.ID || third.Status != ki.JobDone || third.Answer != "Staging differs." {
		t.Fatalf("third turn %+v", third)
	}
	if n := d.runs(t, runs); n != 4 {
		t.Errorf("agent ran %d times, want 4 (two asks, the failed resume, the transcript run)", n)
	}
	task := d.args(t)
	prompt := task[len(task)-1]
	q1, q2, q3 := strings.Index(prompt, firstQuestion), strings.Index(prompt, followUp), strings.Index(prompt, "and staging?")
	if q1 < 0 || q2 < q1 || q3 < q2 {
		t.Errorf("chain not oldest first: %q", prompt)
	}
	if !strings.Contains(d.stderr(), "ki: resumed by transcript") {
		t.Errorf("stderr %s", d.stderr())
	}
}

func TestAskContinueRefusals(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask(firstQuestion, "--json"); err != nil {
		t.Fatal(err)
	}
	first := d.askResult(t)
	record := func(r ki.Result) string {
		t.Helper()
		if err := ki.WritePrivate(filepath.Join(d.jobsDir(), r.ID, "result.json"), r); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	delegated := record(ki.Result{ID: "j-20261107-000001-aaaa", Status: ki.JobDone, Provider: "claude"})
	failed := record(ki.Result{ID: "j-20261107-000002-aaaa", Kind: ki.KindAsk, Status: ki.JobFailed, Provider: "claude", Ask: &ki.AskState{Question: "q"}})
	old := record(ki.Result{ID: "j-20261107-000003-aaaa", Kind: ki.KindAsk, Status: ki.JobDone, Provider: "claude"})
	for _, tc := range []struct{ id, err string }{
		{"j-20261107-999999-ffff", "is not on this machine"},
		{delegated, "was not an ask"},
		{failed, "ended FAILED"},
		{old, "predates --continue"},
	} {
		d.worker["RUNS_FILE"] = filepath.Join(t.TempDir(), "runs")
		err := d.ask(followUp, "--continue", tc.id)
		if err == nil || !strings.Contains(err.Error(), tc.err) {
			t.Errorf("%s: %v, want %q", tc.id, err, tc.err)
		}
		if d.runs(t, d.worker["RUNS_FILE"]) != 0 {
			t.Errorf("%s: an agent ran for a refused follow-up", tc.id)
		}
	}
	if err := d.ask(followUp, "--continue", first.ID, "--agent", "codex"); err == nil || !strings.Contains(err.Error(), "keeps the conversation's scope") {
		t.Errorf("scope flag accepted: %v", err)
	}
}

func TestAskContinueRefusesWhileAnotherFollowUpRuns(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask(firstQuestion, "--json"); err != nil {
		t.Fatal(err)
	}
	first := d.askResult(t)
	prev, err := ki.ReadResult(d.jobsDir(), first.ID)
	if err != nil || prev.Ask == nil {
		t.Fatalf("saved %+v %v", prev, err)
	}
	running := ki.Result{
		ID: "j-20261107-000009-aaaa", Kind: ki.KindAsk, Status: ki.JobRunning, Provider: "claude", Pid: os.Getpid(),
		Continues: first.ID, Ask: &ki.AskState{Question: "other", Scratch: prev.Ask.Scratch},
	}
	write := func() {
		t.Helper()
		if err := ki.WritePrivate(filepath.Join(d.jobsDir(), running.ID, "result.json"), running); err != nil {
			t.Fatal(err)
		}
	}
	write()
	d.worker["RUNS_FILE"] = filepath.Join(t.TempDir(), "runs")
	err = d.ask(followUp, "--continue", first.ID)
	if err == nil || !strings.Contains(err.Error(), "a follow-up of "+first.ID+" is still running") {
		t.Fatalf("second follow-up accepted: %v", err)
	}
	if d.runs(t, d.worker["RUNS_FILE"]) != 0 {
		t.Error("an agent ran for a refused follow-up")
	}

	running.Pid = 0 // a worker that died leaves a RUNNING file behind; it must not block the conversation
	write()
	if err := d.ask(followUp, "--continue", first.ID, "--json"); err != nil {
		t.Fatalf("a dead follow-up blocked the conversation: %v", err)
	}
}
