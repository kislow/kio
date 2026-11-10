package ki

import (
	"encoding/json"
	"strings"
	"testing"
)

// secrets are planted in every place a config can hold one; none may reach an entry.
var mcpSecrets = []string{"sk-live-1234", "hunter2", "ghp_abcdef", "s3cr3t", "tok_in_args"}

func assertNoSecrets(t *testing.T, entries any) {
	t.Helper()
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range mcpSecrets {
		if strings.Contains(string(raw), s) {
			t.Errorf("secret %q reached the output: %s", s, raw)
		}
	}
}

func TestRedactTarget(t *testing.T) {
	cases := map[string]string{
		"https://user:hunter2@mcp.example.com:8443/v1/mcp?key=sk-live-1234": "mcp.example.com:8443",
		"https://kb.example/mcp":                   "kb.example",
		"/usr/local/bin/ki vault mcp":              "ki",
		"npx -y @scope/server --token tok_in_args": "npx",
		"uvx":         "uvx",
		"":            "",
		"https://%zz": "",
	}
	for in, want := range cases {
		if got := RedactTarget(in); got != want {
			t.Errorf("RedactTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

const claudeList = `Checking MCP server health...

claude.ai Gmail: https://gmail.mcp.claude.com/mcp?key=sk-live-1234 - ✓ Connected
claude.ai Linear: https://mcp.linear.app/mcp - ! Needs authentication
plugin:github:github: https://api.githubcopilot.com/mcp/ (HTTP) - ✗ Failed to connect
plugin:ctx:context7: npx -y @upstash/context7-mcp --api-key s3cr3t - ✓ Connected
ki-kb: https://kb.example/mcp (HTTP) - ✓ Connected
ki-vault: /opt/bin/ki vault mcp - ✓ Connected
docs: https://docs.example/sse (SSE) - ✓ Connected
shared: https://shared.example/mcp (HTTP) - ✓ Connected
mine: /usr/bin/mine --token tok_in_args - ? Pending
`

func TestParseClaudeList(t *testing.T) {
	got := ParseClaudeList(claudeList, []string{"shared"})
	want := []MCPEntry{
		{"claude", "claude.ai Gmail", "claude.ai", "http", "gmail.mcp.claude.com", "connected", nil},
		{"claude", "claude.ai Linear", "claude.ai", "http", "mcp.linear.app", "needs-auth", nil},
		{"claude", "plugin:github:github", "plugin", "http", "api.githubcopilot.com", "failed", nil},
		{"claude", "plugin:ctx:context7", "plugin", "stdio", "npx", "connected", nil},
		{"claude", "ki-kb", "ki", "http", "kb.example", "connected", nil},
		{"claude", "ki-vault", "ki", "stdio", "ki", "connected", nil},
		{"claude", "docs", "local", "sse", "docs.example", "connected", nil},
		{"claude", "shared", "project", "http", "shared.example", "connected", nil},
		{"claude", "mine", "local", "stdio", "mine", "unknown", nil},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Agent != want[i].Agent || got[i].Name != want[i].Name || got[i].Source != want[i].Source ||
			got[i].Transport != want[i].Transport || got[i].Target != want[i].Target || got[i].Status != want[i].Status {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	assertNoSecrets(t, got)
}

func TestParseClaudeListIgnoresNoise(t *testing.T) {
	if got := ParseClaudeList("Checking MCP server health...\n\nNo MCP servers configured.\n", nil); len(got) != 0 {
		t.Fatalf("want none, got %+v", got)
	}
}

func TestParseMCPJSON(t *testing.T) {
	const cursor = `{"mcpServers": {
	  "ki-kb": {"url": "https://kb.example/mcp", "headers": {"Authorization": "Bearer sk-live-1234", "X-Ki-Brains": "a,b"}},
	  "ki-vault": {"command": "/opt/bin/ki", "args": ["vault", "mcp"], "env": {"KI_VAULT": "/v", "TOKEN": "s3cr3t"}},
	  "events": {"url": "https://user:hunter2@events.example/sse?token=ghp_abcdef"},
	  "typed": {"type": "sse", "url": "https://typed.example/stream"},
	  "tool": {"command": "npx", "args": ["-y", "tool", "--key", "tok_in_args"]}
	}}`
	got, err := ParseMCPJSON("cursor", MCPSourceLocal, []byte(cursor))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]MCPEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	checks := map[string][3]string{ // source, transport, target
		"ki-kb":    {"ki", "http", "kb.example"},
		"ki-vault": {"ki", "stdio", "ki"},
		"events":   {"local", "sse", "events.example"},
		"typed":    {"local", "sse", "typed.example"},
		"tool":     {"local", "stdio", "npx"},
	}
	for name, w := range checks {
		e := byName[name]
		if e.Source != w[0] || e.Transport != w[1] || e.Target != w[2] || e.Status != "unknown" || e.Agent != "cursor" {
			t.Errorf("%s = %+v, want %v", name, e, w)
		}
	}
	assertNoSecrets(t, got)
}

func TestParseMCPJSONRejectsGarbage(t *testing.T) {
	if _, err := ParseMCPJSON("cursor", MCPSourceLocal, []byte("{not json")); err == nil {
		t.Fatal("want an error")
	}
}

func TestParseCodexTOML(t *testing.T) {
	const toml = `model = "gpt"

# >>> ki managed block >>>
[mcp_servers.ki-kb]
url = "https://kb.example/mcp"
bearer_token_env_var = "KI_TOKEN"
env_http_headers = { "X-Ki-Brains" = "KI_BRAINS" }

[mcp_servers.ki-vault]
command = "/opt/bin/ki"
args = ["vault", "mcp"]
env = { KI_VAULT = "/v" }
# <<< ki managed block <<<

[mcp_servers."dotted.name"]
url = "https://user:hunter2@dotted.example/mcp?key=sk-live-1234" # trailing comment
http_headers = { Authorization = "Bearer ghp_abcdef" }

[mcp_servers.local-tool]
command = 'tool'
args = ["--token", "tok_in_args"]

[mcp_servers.local-tool.env]
SECRET = "s3cr3t"

[profiles.x]
url = "https://not-a-server.example"
`
	got := ParseCodexTOML(MCPSourceLocal, []byte(toml))
	byName := map[string]MCPEntry{}
	for _, e := range got {
		byName[e.Name] = e
	}
	if len(got) != 4 {
		t.Fatalf("want 4 servers, got %+v", got)
	}
	checks := map[string][3]string{
		"ki-kb":       {"ki", "http", "kb.example"},
		"ki-vault":    {"ki", "stdio", "ki"},
		"dotted.name": {"local", "http", "dotted.example"},
		"local-tool":  {"local", "stdio", "tool"},
	}
	for name, w := range checks {
		e := byName[name]
		if e.Source != w[0] || e.Transport != w[1] || e.Target != w[2] || e.Status != "unknown" || e.Agent != "codex" {
			t.Errorf("%s = %+v, want %v", name, e, w)
		}
	}
	assertNoSecrets(t, got)
}

func TestWithKiServers(t *testing.T) {
	existing := []MCPEntry{{Agent: "claude", Name: "ki-kb", Source: "local", Transport: "http", Target: "kb.example", Status: "connected"}}
	got := WithKiServers(existing, "claude", "https://kb.example", []string{"cortex", "app"}, true)
	if len(got) != 2 {
		t.Fatalf("want ki-kb merged and ki-vault added, got %+v", got)
	}
	if kb := got[0]; kb.Source != "ki" || kb.Status != "connected" || strings.Join(kb.Brains, ",") != "cortex,app" {
		t.Errorf("ki-kb = %+v", kb)
	}
	if v := got[1]; v.Name != "ki-vault" || v.Source != "ki" || v.Transport != "stdio" || v.Target != "ki" {
		t.Errorf("ki-vault = %+v", v)
	}
	if got := WithKiServers(nil, "codex", "https://kb.example", nil, false); len(got) != 1 || got[0].Name != "ki-kb" {
		t.Errorf("without a vault only ki-kb: %+v", got)
	}
}

func TestSortMCP(t *testing.T) {
	es := []MCPEntry{{Agent: "codex", Name: "a"}, {Agent: "claude", Name: "z"}, {Agent: "claude", Name: "b"}, {Agent: "cursor", Name: "m"}}
	SortMCP(es)
	order := make([]string, 0, len(es))
	for _, e := range es {
		order = append(order, e.Agent+"/"+e.Name)
	}
	if got := strings.Join(order, " "); got != "claude/b claude/z cursor/m codex/a" {
		t.Fatalf("order = %s", got)
	}
}
