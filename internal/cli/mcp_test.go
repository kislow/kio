package cli

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

var cliSecrets = []string{"sk-live-1234", "hunter2", "ghp_abcdef", "s3cr3t", "tok_in_args"}

const fakeClaudeList = `Checking MCP server health...

claude.ai Gmail: https://gmail.mcp.claude.com/mcp?key=sk-live-1234 - ✓ Connected
claude.ai Linear: https://mcp.linear.app/mcp - ! Needs authentication
plugin:github:github: https://api.githubcopilot.com/mcp/ (HTTP) - ✗ Failed to connect
shared: https://shared.example/mcp (HTTP) - ✓ Connected
mine: /usr/bin/mine --token tok_in_args - ✓ Connected
`

// mcpEnv is a ki environment with a Claude list, a Cursor and a Codex config,
// each holding secrets in headers, env, args and URLs.
func mcpEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, "https://github.com/work/app")
	e.vars["KI_VAULT"] = filepath.Join(t.TempDir(), "vault")
	e.app.Executable = func() (string, error) { return "/opt/bin/ki", nil }
	e.app.ClaudeMCPList = func(context.Context) (string, error) { return fakeClaudeList, nil }
	home := e.vars["HOME"]
	write(t, filepath.Join(e.repo, ".mcp.json"), `{"mcpServers":{"shared":{"type":"http","url":"https://shared.example/mcp","headers":{"Authorization":"Bearer sk-live-1234"}}}}`)
	write(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers":{
	  "ki-kb":{"url":"https://kb.example/mcp","headers":{"Authorization":"Bearer sk-live-1234"}},
	  "figma":{"url":"https://user:hunter2@figma.example/sse?token=ghp_abcdef"}}}`)
	write(t, filepath.Join(e.repo, ".cursor", "mcp.json"), `{"mcpServers":{"repo-tool":{"command":"npx","args":["--key","tok_in_args"],"env":{"K":"s3cr3t"}}}}`)
	write(t, filepath.Join(home, ".codex", "config.toml"), `[mcp_servers.ki-kb]
url = "https://kb.example/mcp"
env_http_headers = { "X-Ki-Brains" = "KI_BRAINS" }

[mcp_servers.docs]
url = "https://docs.example/mcp?key=sk-live-1234"
http_headers = { Authorization = "Bearer ghp_abcdef" }
`)
	return e
}

func runMCP(t *testing.T, e *env, args ...string) (mcpOutput, string) {
	t.Helper()
	e.out.Reset()
	if err := e.app.Run(context.Background(), append([]string{"mcp", "ls"}, args...)); err != nil {
		t.Fatalf("ki mcp ls %v: %v", args, err)
	}
	text := e.out.String()
	var out mcpOutput
	if len(args) > 0 && args[len(args)-1] == "--json" || len(args) > 1 && args[0] == "--json" {
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, text)
		}
	}
	return out, text
}

func assertCLINoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, s := range cliSecrets {
		if strings.Contains(text, s) {
			t.Errorf("secret %q in output:\n%s", s, text)
		}
	}
}

func find(out mcpOutput, agent, name string) (ki.MCPEntry, bool) {
	for _, s := range out.Servers {
		if s.Agent == agent && s.Name == name {
			return s, true
		}
	}
	return ki.MCPEntry{}, false
}

func TestMCPLsJSONListsEveryAgent(t *testing.T) {
	e := mcpEnv(t)
	out, text := runMCP(t, e, "--json")
	assertCLINoSecrets(t, text)
	if len(out.Errors) != 0 {
		t.Fatalf("errors: %+v", out.Errors)
	}

	want := []struct{ agent, name, source, transport, target, status string }{
		{"claude", "claude.ai Gmail", "claude.ai", "http", "gmail.mcp.claude.com", "connected"},
		{"claude", "claude.ai Linear", "claude.ai", "http", "mcp.linear.app", "needs-auth"},
		{"claude", "plugin:github:github", "plugin", "http", "api.githubcopilot.com", "failed"},
		{"claude", "shared", "project", "http", "shared.example", "connected"},
		{"claude", "mine", "local", "stdio", "mine", "connected"},
		{"claude", "ki-kb", "ki", "http", "kb.example", "unknown"},
		{"claude", "ki-vault", "ki", "stdio", "ki", "unknown"},
		{"cursor", "figma", "local", "sse", "figma.example", "unknown"},
		{"cursor", "ki-kb", "ki", "http", "kb.example", "unknown"},
		{"cursor", "repo-tool", "project", "stdio", "npx", "unknown"},
		{"codex", "docs", "local", "http", "docs.example", "unknown"},
		{"codex", "ki-kb", "ki", "http", "kb.example", "unknown"},
	}
	for _, w := range want {
		got, ok := find(out, w.agent, w.name)
		if !ok || got.Source != w.source || got.Transport != w.transport || got.Target != w.target || got.Status != w.status {
			t.Errorf("%s/%s = %+v (found %v), want %+v", w.agent, w.name, got, ok, w)
		}
	}
	// ki's vault server is added for cursor and codex too.
	for _, agent := range []string{"cursor", "codex"} {
		if _, ok := find(out, agent, "ki-vault"); !ok {
			t.Errorf("%s has no ki-vault", agent)
		}
	}
	// The repo's session passes the brains of its project.
	for _, agent := range []string{"claude", "cursor", "codex"} {
		kb, _ := find(out, agent, "ki-kb")
		if got := strings.Join(kb.Brains, ","); got != "app,voice" {
			t.Errorf("%s ki-kb brains = %q", agent, got)
		}
	}
}

func TestMCPLsAgentFilter(t *testing.T) {
	e := mcpEnv(t)
	out, _ := runMCP(t, e, "--agent", "codex", "--json")
	if len(out.Servers) == 0 {
		t.Fatal("no servers")
	}
	for _, s := range out.Servers {
		if s.Agent != "codex" {
			t.Fatalf("got %s/%s with --agent codex", s.Agent, s.Name)
		}
	}
}

func TestMCPLsOneAgentFailingDoesNotFailTheList(t *testing.T) {
	e := mcpEnv(t)
	e.app.ClaudeMCPList = func(context.Context) (string, error) { return "", errors.New("claude is not on PATH") }
	write(t, filepath.Join(e.vars["HOME"], ".codex", "config.toml"), "[mcp_servers.docs]\nurl = \"https://docs.example/mcp\"\n")
	write(t, filepath.Join(e.vars["HOME"], ".cursor", "mcp.json"), "{broken")

	out, _ := runMCP(t, e, "--json")
	agents := map[string]string{}
	for _, p := range out.Errors {
		agents[p.Agent] = p.Error
	}
	if !strings.Contains(agents["claude"], "claude is not on PATH") || !strings.Contains(agents["cursor"], "mcp.json") {
		t.Fatalf("errors = %+v", out.Errors)
	}
	if _, ok := agents["codex"]; ok {
		t.Fatalf("codex read fine: %+v", out.Errors)
	}
	if _, ok := find(out, "codex", "docs"); !ok {
		t.Error("codex servers missing")
	}
	// ki's servers still list for the agents that failed.
	if _, ok := find(out, "claude", "ki-kb"); !ok {
		t.Error("claude ki-kb missing")
	}
}

func TestMCPLsTimeoutIsPerAgent(t *testing.T) {
	e := mcpEnv(t)
	e.app.ClaudeMCPList = func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	e.out.Reset()
	_ = e.app.Run(ctx, []string{"mcp", "ls", "--json"})
	var out mcpOutput
	if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, e.out.String())
	}
	if _, ok := find(out, "codex", "docs"); !ok {
		t.Error("codex servers should list even when claude timed out")
	}
}

func TestMCPLsTable(t *testing.T) {
	e := mcpEnv(t)
	_, text := runMCP(t, e)
	assertCLINoSecrets(t, text)
	for _, want := range []string{"AGENT", "claude.ai Gmail", "needs-auth", "gmail.mcp.claude.com", "ki-kb", "(brains: "} {
		if !strings.Contains(text, want) {
			t.Errorf("table lacks %q:\n%s", want, text)
		}
	}
}

func TestMCPLsEmptyJSONHasArrays(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.app.ClaudeMCPList = func(context.Context) (string, error) { return "No MCP servers configured.\n", nil }
	_, text := runMCP(t, e, "--agent", "cursor", "--json")
	if !strings.Contains(text, `"errors": []`) || !strings.Contains(text, `"servers": [`) {
		t.Fatalf("want arrays, got %s", text)
	}
}

func TestMCPLsRejectsBadArguments(t *testing.T) {
	e := newEnv(t, "")
	for _, args := range [][]string{{"mcp", "ls", "--agent", "agy"}, {"mcp", "ls", "--agent"}, {"mcp", "ls", "--bogus"}, {"mcp", "rm"}} {
		if err := e.app.Run(context.Background(), args); !isUserError(err) {
			t.Errorf("%v: want a user error, got %v", args, err)
		}
	}
}
