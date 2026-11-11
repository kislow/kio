package ki

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/pkg/projects"
)

func loadFixture(t *testing.T) *projects.Config {
	t.Helper()
	cfg, err := projects.Load("../../testdata/projects/valid/full.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestResolve(t *testing.T) {
	cfg := loadFixture(t)
	tests := []struct {
		name      string
		remote    string
		extra     []string
		brains    []string
		sensitive bool
		identity  string
		unmapped  bool
	}{
		{"known repo", "git@github.com:client-a/acme.git", nil, []string{"acme", "voice"}, false, "client-a", false},
		{"repo listed twice merges", "https://github.com/client-a/payments", nil, []string{"acme", "client"}, true, "client-a", false},
		{"unknown repo gets cortex only", "https://github.com/someone/else", nil, []string{"cortex"}, false, "", true},
		{"no remote gets cortex only", "", nil, []string{"cortex"}, false, "", true},
		{"unknown repo with an explicit brain", "https://github.com/someone/else", []string{"voice"}, []string{"cortex", "voice"}, false, "", true},
		{"extra brain can make it sensitive", "https://github.com/client-a/acme", []string{"client"}, []string{"acme", "voice", "client"}, true, "client-a", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Resolve(cfg, tt.remote, tt.extra)
			if !reflect.DeepEqual(s.Brains, tt.brains) || s.Sensitive != tt.sensitive || s.Unmapped != tt.unmapped {
				t.Fatalf("got brains=%v sensitive=%v unmapped=%v", s.Brains, s.Sensitive, s.Unmapped)
			}
			got := ""
			if s.Identity != nil {
				got = s.Identity.Name
			}
			if got != tt.identity {
				t.Fatalf("identity %q, want %q", got, tt.identity)
			}
		})
	}
}

func TestTokenFile(t *testing.T) {
	if f := TokenFile("/c", Session{Sensitive: true}); f != "/c/token-sensitive" {
		t.Fatal(f)
	}
	if f := TokenFile("/c", Session{}); f != "/c/token" {
		t.Fatal(f)
	}
}

func TestTokenFileFallsBackToTheLegacySensitiveName(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacySensitiveTokenName)
	if err := os.WriteFile(legacy, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := TokenFile(dir, Session{Sensitive: true}); f != legacy {
		t.Fatalf("legacy only: got %s", f)
	}
	if f := TokenFile(dir, Session{}); f != filepath.Join(dir, "token") {
		t.Fatalf("plain session must not use the legacy file: %s", f)
	}
	current := filepath.Join(dir, "token-sensitive")
	if err := os.WriteFile(current, []byte("t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := TokenFile(dir, Session{Sensitive: true}); f != current {
		t.Fatalf("both present: got %s", f)
	}
}

func TestIdentityProblem(t *testing.T) {
	s := Resolve(loadFixture(t), "https://github.com/client-a/acme", nil)
	if p := IdentityProblem(s, "Alice@Client-A.com"); p != "" {
		t.Fatalf("case-insensitive match expected, got %q", p)
	}
	if p := IdentityProblem(s, "me@example.com"); !strings.Contains(p, "client-a") {
		t.Fatalf("mismatch should name the identity: %q", p)
	}
	if p := IdentityProblem(s, ""); !strings.Contains(p, "<unset>") {
		t.Fatalf("missing email should be reported: %q", p)
	}
	if p := IdentityProblem(Session{}, "anything"); p != "" {
		t.Fatal("unmapped repos are not checked")
	}
}

func TestBuildLaunch(t *testing.T) {
	s := Session{Brains: []string{"acme"}}
	l := BuildLaunch(LaunchInput{
		Agent: "claude", AgentArgs: []string{"-p", "hi"}, Session: s, Token: "t",
		MCPURL: "u", MCPConfigPath: "/m.json", InstructionsPath: "/i.md", GHToken: "gh",
	})
	want := []string{"claude", "--mcp-config", "/m.json", "--append-system-prompt-file", "/i.md", "--settings", ClaudeSettings, "-p", "hi"}
	if !reflect.DeepEqual(l.Argv, want) || l.Env["GH_TOKEN"] != "gh" || l.Env["KI_BRAINS"] != "acme" {
		t.Fatalf("got %+v", l)
	}
	other := BuildLaunch(LaunchInput{Agent: "codex", Session: s, Token: "t"})
	if !reflect.DeepEqual(other.Argv, []string{"codex"}) || other.Env["GH_TOKEN"] != "" || other.Env["KI_MODEL"] != "" {
		t.Fatalf("other agents get env only: %+v", other)
	}
	for agent, want := range map[string][]string{
		"claude": {"claude", "--mcp-config", "/m.json", "--append-system-prompt-file", "/i.md", "--settings", ClaudeSettings, "--model", "sonnet", "-p", "hi"},
		"codex":  {"codex", "--model", "sonnet", "-p", "hi"},
		"cursor": {"cursor", "-p", "hi"},
	} {
		l := BuildLaunch(LaunchInput{Agent: agent, AgentArgs: []string{"-p", "hi"}, Session: s, Token: "t", MCPConfigPath: "/m.json", InstructionsPath: "/i.md", Model: "sonnet"})
		if !reflect.DeepEqual(l.Argv, want) || l.Env["KI_MODEL"] != "sonnet" {
			t.Errorf("%s with a model: got %v, want %v (env %v)", agent, l.Argv, want, l.Env["KI_MODEL"])
		}
	}
}

func TestParseRunArgs(t *testing.T) {
	r := ParseRunArgs([]string{"--brain", "client", "--agent", "codex", "--model", "o3", "--mode", "skip", "--force", "-c", "--", "--brain", "x"}, "claude")
	if !reflect.DeepEqual(r.Brains, []string{"client"}) || r.Agent != "codex" || r.Model != "o3" || r.Mode != "skip" || !r.Force {
		t.Fatalf("got %+v", r)
	}
	if !reflect.DeepEqual(r.AgentArgs, []string{"-c", "--brain", "x"}) {
		t.Fatalf("after -- everything goes to the agent: %v", r.AgentArgs)
	}
}

func TestMCPConfigWrittenPrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "m.json")
	if err := WritePrivate(path, NewMCPConfig("http://kb:8787/", "secret", []string{"a", "b"})); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, %v", info.Mode().Perm(), err)
	}
	var cfg MCPConfig
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	srv := cfg.MCPServers[MCPServerName]
	if srv.URL != "http://kb:8787/mcp" || srv.Headers["Authorization"] != "Bearer secret" || srv.Headers["X-Ki-Brains"] != "a,b" {
		t.Fatalf("got %+v", srv)
	}
}

func TestValidateBrains(t *testing.T) {
	tests := []struct {
		in []string
		ok bool
	}{
		{nil, true},
		{[]string{"acme", "client-acme"}, true},
		{[]string{"client-*"}, false},
		{[]string{"brain:client"}, false},
		{[]string{"Voice"}, false},
	}
	for _, tt := range tests {
		err := ValidateBrains(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("ValidateBrains(%v) = %v", tt.in, err)
		}
		if err != nil && !errors.Is(err, ErrInvalidBrain) {
			t.Errorf("want ErrInvalidBrain, got %v", err)
		}
	}
}

func TestMergeEnv(t *testing.T) {
	base := []string{"PATH=/bin", "GH_TOKEN=stale", "HOME=/h", "GH_TOKEN=stale2", "NOEQUALS"}
	got := MergeEnv(base, map[string]string{"GH_TOKEN": "fresh", "KI_TOKEN": "k"})
	want := []string{"PATH=/bin", "HOME=/h", "NOEQUALS", "GH_TOKEN=fresh", "KI_TOKEN=k"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestPruneRunFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for name, age := range map[string]time.Duration{
		"mcp-old.json": 48 * time.Hour, "mcp-new.json": time.Hour, "keep.txt": 48 * time.Hour,
		"instructions-old.md": 48 * time.Hour, "instructions-new.md": time.Hour,
	} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	n, err := PruneRunFiles(dir, 24*time.Hour, now)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, err %v", n, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 3 {
		t.Fatalf("left %v", left)
	}
	if n, err := PruneRunFiles(filepath.Join(dir, "missing"), time.Hour, now); n != 0 || err != nil {
		t.Fatalf("missing dir: %d %v", n, err)
	}
}

func TestVaultServerIsAStdioEntryForTheKiBinary(t *testing.T) {
	v := VaultServer("/usr/local/bin/ki", "/vault")
	if v.Type != "stdio" || v.Command != "/usr/local/bin/ki" || len(v.Args) != 2 || v.Args[1] != "mcp" || v.Env["KI_VAULT"] != "/vault" || v.URL != "" {
		t.Fatalf("vault server: %+v", v)
	}
}

func TestBuildLaunchWithoutKnowledgeBaseExportsNoKBVariables(t *testing.T) {
	l := BuildLaunch(LaunchInput{Agent: "codex", Session: Session{Brains: []string{"acme"}}, GHToken: "gh", Model: "m"})
	for _, k := range []string{"KI_TOKEN", "KI_BRAINS", "KI_MCP_URL"} {
		if _, ok := l.Env[k]; ok {
			t.Errorf("%s is set without a knowledge base", k)
		}
	}
	if l.Env["GH_TOKEN"] != "gh" || l.Env["KI_MODEL"] != "m" {
		t.Fatalf("the rest of the environment stays: %v", l.Env)
	}
}
