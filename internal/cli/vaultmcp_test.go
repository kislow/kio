package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// vaultSession connects an in-memory MCP client to the vault server of v.
func vaultSession(t *testing.T, v *vaultEnv) *mcp.ClientSession {
	t.Helper()
	return vaultSessionMode(t, v, false)
}

// vaultSessionMode is vaultSession, read-only or not.
func vaultSessionMode(t *testing.T, v *vaultEnv, readOnly bool) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv, err := v.app.vaultServer(ctx, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cs.Close()
		_ = ss.Wait()
	})
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

func seedVaultLayout(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "templates", "decision.md"), "---\ntitle: {{date:YYYY-MM-DD}} {{title}}\narea: projects\nproject:\ntype: decision\nstatus: active\ncreated: {{date:YYYY-MM-DD}}\n---\n\n## Context\n\nplaceholder\n")
	write(t, filepath.Join(dir, "projects", "acme", "Acme.md"), "---\ntitle: Acme\narea: projects\n---\n\nAbout.\n\n## Notes\n\n- [[Backend]]\n")
	write(t, filepath.Join(dir, "projects", "acme", "Backend.md"), "---\ntitle: Backend\n---\n\nGo and Postgres.\n")
	write(t, filepath.Join(dir, ".obsidian", "app.json"), "{}")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "layout")
	gitIn(t, dir, "push", "-q", "origin", "main")
}

func TestVaultMCPListsItsTools(t *testing.T) {
	v := newVaultEnv(t)
	cs := vaultSession(t, v)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "vault_list,vault_read,vault_search,vault_write" {
		t.Fatalf("tools: %v", names)
	}
}

func TestVaultMCPReadOnlyHasNoWrite(t *testing.T) {
	v := newVaultEnv(t)
	seedVaultLayout(t, v.a)
	cs := vaultSessionMode(t, v, true)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "vault_list,vault_read,vault_search" {
		t.Fatalf("tools: %v", names)
	}
	if out, isErr := call(t, cs, "vault_read", map[string]any{"title": "Backend"}); isErr || !strings.Contains(out, "Go and Postgres.") {
		t.Fatalf("read: %v %s", isErr, out)
	}
	res2, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "vault_write", Arguments: map[string]any{"area": "projects", "title": "X", "body": "x"}})
	if err == nil && !res2.IsError {
		t.Fatal("vault_write answered on the read-only server")
	}
}

func TestVaultWriteRoundTrip(t *testing.T) {
	v := newVaultEnv(t)
	seedVaultLayout(t, v.a)
	cs := vaultSession(t, v)

	out, isErr := call(t, cs, "vault_write", map[string]any{
		"area": "projects", "project": "acme", "kind": "decision", "title": "Use Postgres",
		"body": "# Use Postgres\n\n## Decision\n\nPostgres, managed.\n",
	})
	if isErr {
		t.Fatalf("write failed: %s", out)
	}
	for _, want := range []string{"wrote projects/acme/decisions/", " Use Postgres.md", "listed in projects/acme/Acme.md", "committed 2 file(s): decision: Use Postgres", "vault at "} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(v.a, "projects", "acme", "decisions", "*Use Postgres.md"))
	if len(matches) != 1 {
		t.Fatalf("note not written: %v", matches)
	}
	note, _ := os.ReadFile(matches[0])
	if !strings.Contains(string(note), "project: acme\ntype: decision\n") || strings.Contains(string(note), "placeholder") || strings.Contains(string(note), "# Use Postgres\n") {
		t.Fatalf("note content:\n%s", note)
	}
	folder, _ := os.ReadFile(filepath.Join(v.a, "projects", "acme", "Acme.md"))
	if !strings.Contains(string(folder), "## Decisions\n\n- [[") {
		t.Fatalf("folder note:\n%s", folder)
	}

	// The other machine receives it: the write went through the push path.
	gitIn(t, v.b, "pull", "-q", "--rebase", "origin", "main")
	if _, err := os.Stat(filepath.Join(v.b, "projects", "acme", "decisions", filepath.Base(matches[0]))); err != nil {
		t.Fatalf("b did not receive the note: %v", err)
	}

	// Same name again is refused without overwrite, accepted with it.
	args := map[string]any{"area": "projects", "project": "acme", "kind": "decision", "title": "Use Postgres", "body": "second"}
	if out, isErr := call(t, cs, "vault_write", args); !isErr || !strings.Contains(out, "pass overwrite") {
		t.Fatalf("duplicate: %v %s", isErr, out)
	}
	args["overwrite"] = true
	if out, isErr := call(t, cs, "vault_write", args); isErr {
		t.Fatalf("overwrite: %s", out)
	}
	// A note of that name elsewhere is a refusal even with overwrite: names are unique.
	args = map[string]any{"area": "engineering", "title": "Backend", "body": "x", "overwrite": true}
	if out, isErr := call(t, cs, "vault_write", args); !isErr || !strings.Contains(out, "already exists at projects/acme/Backend.md") {
		t.Fatalf("name clash: %v %s", isErr, out)
	}

	// Read, list and search see the result.
	if out, isErr := call(t, cs, "vault_read", map[string]any{"title": "Backend"}); isErr || !strings.Contains(out, "Go and Postgres.") {
		t.Fatalf("read: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_read", map[string]any{"title": "Nope"}); !isErr || !strings.Contains(out, "no note named") {
		t.Fatalf("read missing: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_read", map[string]any{"title": " "}); !isErr {
		t.Fatalf("read empty: %s", out)
	}
	if out, isErr := call(t, cs, "vault_list", map[string]any{"folder": "projects/acme"}); isErr || !strings.Contains(out, "Acme\n") || !strings.Contains(out, "decisions/\n") {
		t.Fatalf("list: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_list", map[string]any{}); isErr || strings.Contains(out, ".obsidian") || !strings.Contains(out, "projects/") {
		t.Fatalf("root list: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_list", map[string]any{"folder": ".obsidian"}); !isErr {
		t.Fatalf("obsidian listed: %s", out)
	}
	if out, isErr := call(t, cs, "vault_search", map[string]any{"query": "second"}); isErr || !strings.Contains(out, "Use Postgres.md:") {
		t.Fatalf("search: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_search", map[string]any{"query": "zzz-nothing"}); isErr || !strings.Contains(out, "No notes match") {
		t.Fatalf("search miss: %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "vault_search", map[string]any{"query": ""}); !isErr {
		t.Fatalf("empty search: %s", out)
	}
}

func TestVaultWriteRefusals(t *testing.T) {
	v := newVaultEnv(t)
	seedVaultLayout(t, v.a)
	cs := vaultSession(t, v)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"secret", map[string]any{"area": "ideas", "title": "Keys", "body": "token: ghp_" + strings.Repeat("x", 36)}, "looks like a credential"},
		{"bad area", map[string]any{"area": "daily", "title": "X", "body": "b"}, "unknown area"},
		{"blank title", map[string]any{"area": "ideas", "title": "  ", "body": "b"}, "title is required"},
		{"frontmatter body", map[string]any{"area": "ideas", "title": "X", "body": "---\ntitle: X\n---\nb"}, "must not start with frontmatter"},
		{"kind outside projects", map[string]any{"area": "writing", "kind": "runbook", "title": "X", "body": "b"}, "only exists under projects"},
	}
	for _, c := range cases {
		out, isErr := call(t, cs, "vault_write", c.args)
		if !isErr || !strings.Contains(out, c.want) {
			t.Errorf("%s: isErr=%v out=%s", c.name, isErr, out)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(v.a, "ideas")); len(entries) != 0 {
		t.Fatalf("a refused write left files: %v", entries)
	}
}

func TestVaultWriteRefusesFromVaultFalseRepo(t *testing.T) {
	v := newVaultEnv(t)
	gitIn(t, v.repo, "remote", "add", "origin", "https://github.com/work/app")
	cs := vaultSession(t, v)
	out, isErr := call(t, cs, "vault_write", map[string]any{"area": "ideas", "title": "X", "body": "b"})
	if !isErr || !strings.Contains(out, "vault: false") {
		t.Fatalf("want identity refusal, got %v %s", isErr, out)
	}
}

func TestVaultMCPNeedsAVault(t *testing.T) {
	e := newEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"vault", "mcp"}); !isUserError(err) || !strings.Contains(err.Error(), "KI_VAULT is not set") {
		t.Fatalf("no KI_VAULT: %v", err)
	}
	e.vars["KI_VAULT"] = t.TempDir()
	if err := e.app.Run(context.Background(), []string{"vault", "mcp"}); !isUserError(err) || !strings.Contains(err.Error(), "not a git repo") {
		t.Fatalf("not a clone: %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"vault", "mcp", "extra"}); !isUserError(err) {
		t.Fatalf("usage: %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"vault", "nope"}); !isUserError(err) {
		t.Fatalf("unknown subcommand: %v", err)
	}
}

func TestRunOffersTheVaultServerWhenSet(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }
	// sh stands in for the agent: CI has no claude binary, and the session
	// config is written for every agent.
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil {
		t.Fatal(err)
	}
	cfg := readMCPConfig(t, e)
	if _, ok := cfg.MCPServers[ki.VaultServerName]; ok {
		t.Fatal("no KI_VAULT, no vault server")
	}
	e.vars["KI_VAULT"] = "/Users/me/vault"
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil {
		t.Fatal(err)
	}
	cfg = readMCPConfig(t, e)
	srv, ok := cfg.MCPServers[ki.VaultServerName]
	if !ok || srv.Command != "/opt/ki" || srv.Env["KI_VAULT"] != "/Users/me/vault" {
		t.Fatalf("vault server in session config: %+v", cfg)
	}
}

// readMCPConfig reads the session config ki wrote for this repo (one file per repo).
func readMCPConfig(t *testing.T, e *env) ki.MCPConfig {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(e.vars["KI_CONFIG_DIR"], "run", "mcp-*.json"))
	if len(matches) != 1 {
		t.Fatalf("expected one session config, got %v", matches)
	}
	var cfg ki.MCPConfig
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSetupCodexAndCursorCarryTheVaultServer(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }
	e.vars["CODEX_HOME"] = filepath.Join(e.vars["HOME"], "codex")
	e.vars["KI_VAULT"] = "/Users/me/vault"
	if err := e.app.Run(context.Background(), []string{"setup", "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "config.toml"))
	if !strings.Contains(string(cfg), ki.CodexVaultTable) || !strings.Contains(string(cfg), `KI_VAULT = "/Users/me/vault"`) || !strings.Contains(e.out.String(), "ki-kb and ki-vault") {
		t.Fatalf("config.toml:\n%s\nout:\n%s", cfg, e.out.String())
	}

	t.Setenv("HOME", e.vars["HOME"])
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.vars["HOME"], ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.vars["HOME"], ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	write(t, filepath.Join(e.repo, ".gitignore"), ".cursor/\n")
	if err := e.app.Run(context.Background(), []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(e.repo, cursorMCP))
	var mcpCfg ki.MCPConfig
	if err := json.Unmarshal(raw, &mcpCfg); err != nil {
		t.Fatal(err)
	}
	srv, ok := mcpCfg.MCPServers[ki.VaultServerName]
	if !ok || srv.Type != "" || srv.Command != "/opt/ki" {
		t.Fatalf("cursor config:\n%s", raw)
	}
}
