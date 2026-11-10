package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

// localEnv is newEnv with a knowledge-base-free configuration: no config.yaml
// block, no KI_MCP_URL and no device token.
func localEnv(t *testing.T, remote string) *env {
	t.Helper()
	e := newEnv(t, remote)
	write(t, filepath.Join(e.harness, "deploy", "config", "config.yaml"), "# no knowledge base\n")
	if err := os.Remove(filepath.Join(e.vars["KI_CONFIG_DIR"], "token")); err != nil {
		t.Fatal(err)
	}
	e.app.Stderr = &bytes.Buffer{}
	return e
}

func TestSetupConfigWithoutURLLinks(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	dir := configDirFrom(t, e, filepath.Join(t.TempDir(), "cfg"), "")
	write(t, filepath.Join(dir, "config.yaml"), "# no knowledge base\n")
	if err := e.app.Run(context.Background(), []string{"setup", "config", dir}); err != nil {
		t.Fatal(err)
	}
	if out := e.out.String(); !strings.Contains(out, "(no knowledge base: sessions start without ki-kb)") {
		t.Fatalf("linked line: %s", out)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if v, ok := out["knowledge_base"]; !ok || v != nil || out["config_source"] != "link" {
		t.Fatalf("status must report a null knowledge base: %v", out)
	}
}

func TestSessionWithoutKnowledgeBaseStartsWithIdentityOnly(t *testing.T) {
	e := localEnv(t, "https://github.com/work/app")
	stderr := e.app.Stderr.(*bytes.Buffer)
	t.Setenv("PATH", filepath.Dir(fakeAgent(t, "claude"))+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "claude"}); err != nil {
		t.Fatal(err)
	}
	if i := slices.Index(e.execd, "--mcp-config"); i < 0 {
		t.Fatalf("argv %v", e.execd)
	} else if raw, err := os.ReadFile(e.execd[i+1]); err != nil || strings.Contains(string(raw), "ki-kb") {
		t.Fatalf("MCP config %s %v", raw, err)
	}
	if n := strings.Count(stderr.String(), "ki: no knowledge base configured; starting claude with identity and instructions only"); n != 1 {
		t.Fatalf("the notice must print once, got %d:\n%s", n, stderr)
	}

	e.app.Stderr = &bytes.Buffer{}
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh", "--", "-c", "true"}); err != nil {
		t.Fatalf("no token file is needed: %v", err)
	}
	for _, kv := range e.execEnv {
		if strings.HasPrefix(kv, "KI_TOKEN=") || strings.HasPrefix(kv, "KI_MCP_URL=") {
			t.Fatalf("local mode must not export %s", kv)
		}
	}
}

func TestSessionWithoutKnowledgeBaseWritesNoKBServer(t *testing.T) {
	e := localEnv(t, "https://github.com/work/app")
	ctx := context.Background()
	e.vars["KI_VAULT"] = t.TempDir()
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }

	p, err := e.app.prepare(ctx, ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	var cfg ki.MCPConfig
	raw, err := os.ReadFile(p.mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCPServers[ki.MCPServerName]; ok || len(cfg.MCPServers) != 1 {
		t.Fatalf("only the vault server is kept: %s", raw)
	}
	if _, ok := cfg.MCPServers[ki.VaultServerName]; !ok {
		t.Fatalf("ki-vault is independent of the knowledge base: %s", raw)
	}
	launch := ki.BuildLaunch(p.launchInput(ki.RunArgs{Agent: "codex"}, "", nil))
	for _, k := range []string{"KI_TOKEN", "KI_MCP_URL", "KI_BRAINS"} {
		if _, ok := launch.Env[k]; ok {
			t.Errorf("%s must not be exported without a knowledge base", k)
		}
	}

	delete(e.vars, "KI_VAULT")
	p, err = e.app.prepare(ctx, ki.RunArgs{Agent: "claude", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(p.mcpPath)
	if strings.Contains(string(raw), "ki-kb") || strings.Contains(string(raw), "Bearer") {
		t.Fatalf("no server, no bearer: %s", raw)
	}
}

func TestDelegateWithoutKnowledgeBaseRunsTheWorker(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.harness, "deploy", "config", "config.yaml"), "# no knowledge base\n")
	if err := os.Remove(filepath.Join(d.vars["KI_CONFIG_DIR"], "token")); err != nil {
		t.Fatal(err)
	}
	mcp := filepath.Join(t.TempDir(), "mcp.json")
	d.worker["RESULT"] = claudeDone
	d.worker["COPY_MCP_CONFIG"] = mcp
	if err := d.delegate("--task", "fix the build", "--force", "--agent", "claude", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	raw, err := os.ReadFile(mcp)
	if err != nil || strings.Contains(string(raw), "ki-kb") {
		t.Fatalf("the headless worker gets no ki-kb entry: %s %v", raw, err)
	}
}

func TestInstructionsIncludeTheKnowledgeBasePartOnlyWithAURL(t *testing.T) {
	for name, tc := range map[string]struct {
		url  string
		want string
	}{
		"with a URL":    {"https://kb.example", "# Instructions\n\n## KB\n"},
		"without a URL": {"", "# Instructions\n"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "")
			write(t, filepath.Join(e.harness, ki.KnowledgeBaseFile), "## KB\n")
			e.vars["KI_MCP_URL"] = tc.url
			if tc.url == "" {
				write(t, filepath.Join(e.harness, "deploy", "config", "config.yaml"), "# none\n")
			}
			h, err := e.app.current(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			got, err := h.instructionsText()
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			p, err := e.app.prepare(context.Background(), ki.RunArgs{Agent: "claude", Force: true})
			if err != nil {
				t.Fatal(err)
			}
			harnessFile := filepath.Join(h.harness, ki.InstructionsFile)
			if (tc.url != "") == (p.instructions == harnessFile) {
				t.Fatalf("with the KB part Claude gets the run file, without it the harness file: %s", p.instructions)
			}
			if body, _ := os.ReadFile(p.instructions); tc.url != "" && string(body) != tc.want {
				t.Fatalf("run file %q", body)
			}
		})
	}
}

func TestAskAndKBRefuseWithoutAKnowledgeBase(t *testing.T) {
	e := localEnv(t, "https://github.com/work/app")
	for _, args := range [][]string{{"ask", "what is this?"}, {"kb", "search", "x"}} {
		err := e.app.Run(context.Background(), args)
		want := "ki " + args[0] + " needs a knowledge base: set knowledge_base.url in "
		if !isUserError(err) || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "config.yaml or export KI_MCP_URL (see docs/self-hosting.md)") {
			t.Errorf("ki %s: %v", args[0], err)
		}
	}
}

func TestSetupCodexAndCursorWithoutKnowledgeBase(t *testing.T) {
	e := localEnv(t, "https://github.com/work/app")
	e.vars["CODEX_HOME"] = filepath.Join(e.vars["HOME"], "codex")
	t.Setenv("HOME", e.vars["HOME"])
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.vars["HOME"], ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.vars["HOME"], ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	write(t, filepath.Join(e.repo, ".gitignore"), ".cursor/\n")
	write(t, filepath.Join(e.harness, ki.KnowledgeBaseFile), "## KB only\n")
	ctx := context.Background()

	for _, args := range [][]string{{"setup", "codex"}, {"cursor"}} {
		if err := e.app.Run(ctx, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := os.Stat(filepath.Join(e.vars["CODEX_HOME"], "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("no servers to write, so Codex's config.toml stays untouched: %v", err)
	}
	agents, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "AGENTS.md"))
	rule, _ := os.ReadFile(filepath.Join(e.repo, cursorRule))
	mcp, _ := os.ReadFile(filepath.Join(e.repo, cursorMCP))
	for name, got := range map[string]string{"AGENTS.md": string(agents), "cursor rule": string(rule)} {
		if !strings.Contains(got, "# Instructions") || strings.Contains(got, "KB only") {
			t.Errorf("%s carries the harness text without the KB part:\n%s", name, got)
		}
	}
	if strings.Contains(string(mcp), "ki-kb") {
		t.Fatalf("cursor MCP: %s", mcp)
	}

	e.vars["KI_VAULT"] = t.TempDir()
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }
	if err := e.app.Run(ctx, []string{"setup", "codex"}); err != nil {
		t.Fatal(err)
	}
	toml, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "config.toml"))
	if strings.Contains(string(toml), ki.CodexTable) || !strings.Contains(string(toml), ki.CodexVaultTable) {
		t.Fatalf("only the vault server is written:\n%s", toml)
	}
	if !strings.Contains(e.out.String(), "ki-vault MCP server") {
		t.Fatalf("output: %s", e.out)
	}

	e.vars["KI_MCP_URL"] = "https://kb.example"
	if err := e.app.Run(ctx, []string{"setup", "codex"}); err != nil {
		t.Fatal(err)
	}
	agents, _ = os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "AGENTS.md"))
	if !strings.Contains(string(agents), "KB only") {
		t.Fatalf("with a URL the same assembly adds the KB part:\n%s", agents)
	}
}

func TestDoctorTreatsAMissingKnowledgeBaseAsOptional(t *testing.T) {
	e := localEnv(t, "https://github.com/work/app")
	_ = e.app.Run(context.Background(), []string{"doctor"})
	out := e.out.String()
	if !strings.Contains(out, "· knowledge base") || !strings.Contains(out, "not configured (optional)") || strings.Contains(out, "✗ device token") {
		t.Fatalf("doctor:\n%s", out)
	}
}

// withoutKnowledgeBase turns a delegate environment into a local one: no
// knowledge_base block in config.yaml and no device token.
func withoutKnowledgeBase(t *testing.T, d *delegateEnv) {
	t.Helper()
	write(t, filepath.Join(d.harness, "deploy", "config", "config.yaml"), "# no knowledge base\n")
	if err := os.Remove(filepath.Join(d.vars["KI_CONFIG_DIR"], "token")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// copyEnvAgent is a fake Claude Code that keeps its MCP config and its
// environment for the test to read.
const copyEnvAgent = `#!/bin/sh
prev=""
for a in "$@"; do
  if [ "$prev" = "--mcp-config" ]; then cp "$a" "$COPY_MCP_CONFIG"; fi
  prev="$a"
done
env > "$ENV_FILE"
printf '%s\n' "$RESULT"
`

func TestSkillRunWithoutKnowledgeBaseHasNoKBServerOrToken(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  execution: {claude: sonnet}\n")
	withoutKnowledgeBase(t, d)
	d.addSkill(t, "summary", readOnlySkill)
	installAgent(t, "claude", copyEnvAgent)
	mcp, envFile := filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "env")
	d.worker["COPY_MCP_CONFIG"], d.worker["ENV_FILE"] = mcp, envFile
	d.worker["RESULT"] = claudeAnswer("Three changes.\n\nDONE: summarized main")
	stderr := &bytes.Buffer{}
	d.app.Stderr = stderr

	if err := d.skillRun("summary", "main", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.skillResult(t); r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	raw, err := os.ReadFile(mcp)
	if err != nil {
		t.Fatal(err)
	}
	var cfg ki.MCPConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.MCPServers[ki.MCPServerName]; ok || strings.Contains(string(raw), askToken) {
		t.Fatalf("no ki-kb entry and no bearer without a knowledge base: %s", raw)
	}
	if _, ok := cfg.MCPServers[ki.VaultServerName]; !ok {
		t.Fatalf("ki-vault is independent of the knowledge base: %s", raw)
	}
	env, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"KI_TOKEN=", "KI_BRAINS=", "KI_MCP_URL="} {
		if strings.Contains(string(env), k) {
			t.Errorf("the skill's environment has %s", k)
		}
	}
	if n := strings.Count(stderr.String(), "ki: no knowledge base configured; running summary with identity and instructions only"); n != 1 {
		t.Fatalf("the notice must print once, got %d:\n%s", n, stderr)
	}
}

func TestSkillRunWithAURLKeepsTheKnowledgeBase(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  execution: {claude: sonnet}\n")
	d.addSkill(t, "summary", readOnlySkill)
	installAgent(t, "claude", copyEnvAgent)
	mcp, envFile := filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "env")
	d.worker["COPY_MCP_CONFIG"], d.worker["ENV_FILE"] = mcp, envFile
	d.worker["RESULT"] = claudeAnswer("Three changes.\n\nDONE: summarized main")
	stderr := &bytes.Buffer{}
	d.app.Stderr = stderr

	if err := d.skillRun("summary", "main", "--json"); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(mcp)
	env, _ := os.ReadFile(envFile)
	if !strings.Contains(string(raw), "ki-kb") || !strings.Contains(string(raw), askToken) || !strings.Contains(string(env), "KI_TOKEN="+askToken) {
		t.Fatalf("with a URL the skill keeps ki-kb and the token:\n%s\n%s", raw, env)
	}
	if strings.Contains(stderr.String(), "no knowledge base configured") {
		t.Fatalf("local-mode notice with a URL: %s", stderr)
	}
}

func TestAskWithoutKBSourceNeedsNoURLOrToken(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  search: {claude: haiku}\n")
	withoutKnowledgeBase(t, d)
	installAgent(t, "claude", copyEnvAgent)
	mcp, envFile := filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "env")
	d.worker["COPY_MCP_CONFIG"], d.worker["ENV_FILE"] = mcp, envFile
	d.worker["RESULT"] = claudeAnswer(askAnswer)

	if err := d.ask("What is open?", "--source", "gh", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.askResult(t); r.Status != ki.JobDone {
		t.Fatalf("answer %+v", r)
	}
	raw, _ := os.ReadFile(mcp)
	env, _ := os.ReadFile(envFile)
	if strings.Contains(string(raw), "ki-kb") {
		t.Fatalf("ki-kb in the MCP config: %s", raw)
	}
	for _, k := range []string{"KI_TOKEN=", "KI_BRAINS=", "KI_MCP_URL="} {
		if strings.Contains(string(env), k) {
			t.Errorf("the ask's environment has %s", k)
		}
	}

	err := d.ask("What is open?")
	want := "ki ask needs a knowledge base: set knowledge_base.url in "
	if !isUserError(err) || !strings.Contains(err.Error(), want) {
		t.Fatalf("default sources must still refuse: %v", err)
	}
}

func TestAskWithAURLButNoKBSourceReadsNoToken(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  search: {claude: haiku}\n")
	if err := os.Remove(filepath.Join(d.vars["KI_CONFIG_DIR"], "token")); err != nil {
		t.Fatal(err)
	}
	d.worker["RESULT"] = claudeAnswer(askAnswer)
	if err := d.ask("What is open?", "--source", "gh", "--json"); err != nil {
		t.Fatalf("a missing token file only matters to the kb source: %v", err)
	}
	if err := d.ask("What is open?"); err == nil {
		t.Fatal("the kb source still needs the token file")
	}
}

func TestApproveReviewWithoutKnowledgeBaseRuns(t *testing.T) {
	r := newReviewEnv(t)
	withoutKnowledgeBase(t, r.delegateEnv)
	installAgent(t, "claude", copyEnvAgent)
	mcp, envFile := filepath.Join(t.TempDir(), "mcp.json"), filepath.Join(t.TempDir(), "env")
	r.worker["COPY_MCP_CONFIG"], r.worker["ENV_FILE"] = mcp, envFile
	r.worker["RESULT"] = claudeSays(t, "Looks right.\nVERDICT: APPROVE")

	res, err := r.approve(t)
	if err != nil {
		t.Fatalf("the review needs no knowledge base: %v", err)
	}
	if res.Verdict != "APPROVE" || len(r.gh.reviews) != 1 {
		t.Fatalf("result %+v reviews %v", res, r.gh.reviews)
	}
	raw, _ := os.ReadFile(mcp)
	env, _ := os.ReadFile(envFile)
	if strings.Contains(string(raw), "ki-kb") || strings.Contains(string(env), "KI_TOKEN=") {
		t.Fatalf("no ki-kb tools without a knowledge base:\n%s\n%s", raw, env)
	}
}
