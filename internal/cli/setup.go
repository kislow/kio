package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

func (a *App) codexHome() string {
	return orDefault(a.Getenv("CODEX_HOME"), filepath.Join(a.home(), ".codex"))
}

func (a *App) claudeSettingsPath() string {
	return filepath.Join(a.claudeDir(), "settings.json")
}

func (a *App) setup(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "config" {
		return a.setupConfig(args[1:])
	}
	if len(args) > 0 && args[0] == "machine" {
		h, err := a.current(ctx, nil)
		if err != nil {
			return err
		}
		return a.setupMachine(h, args[1:])
	}
	switch strings.Join(args, " ") {
	case "claude":
		return a.setupClaude()
	case "codex":
		return a.setupCodex(ctx)
	case "skills":
		return a.setupSkills()
	default:
		return fail("usage: ki setup claude|codex|skills|machine <identity>...|config <dir>   (Cursor is set up per repo with `ki cursor`)")
	}
}

// setupClaude turns off Claude Code's Co-Authored-By trailer and PR line in the
// user settings, so sessions started without ki leave no attribution either.
func (a *App) setupClaude() error {
	path := a.claudeSettingsPath()
	current, err := readOptional(path)
	if err != nil {
		return err
	}
	updated, changed, err := ki.ClaudeAttributionOff([]byte(current))
	if err != nil {
		return fail("%s: %v; fix the file by hand and run again", path, err)
	}
	if !changed {
		fmt.Fprintf(a.Stdout, "%s already has commit and PR attribution off\n", path)
		return nil
	}
	if err := writeFile(path, string(updated), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "updated %s: attribution.commit and attribution.pr are empty\n", path)
	return nil
}

func (a *App) setupCodex(ctx context.Context) error {
	here, err := a.current(ctx, nil)
	if err != nil {
		return err
	}
	url := a.mcpURL(here)
	dir := a.codexHome()

	configPath := filepath.Join(dir, "config.toml")
	current, err := readOptional(configPath)
	if err != nil {
		return err
	}
	vault := a.sessionVaultServer(ctx, nil)
	servers := codexServers(url, vault)
	wroteConfig := false
	if block := ki.CodexMCPBlock(url, vault); block != "" || ki.HasBlock(current, ki.TOMLComment()) {
		conflict := ""
		if url != "" {
			conflict = ki.CodexTable
		}
		updated, err := ki.UpsertBlock(current, block, ki.TOMLComment(), conflict)
		if err != nil {
			return fail("%s: %v; remove your own %s table and run again", configPath, err, ki.CodexTable)
		}
		if err := writeFile(configPath, updated, 0o600); err != nil {
			return err
		}
		wroteConfig = true
	}

	instructions, err := here.instructionsText()
	if err != nil {
		return err
	}
	agentsPath := filepath.Join(dir, "AGENTS.md")
	currentAgents, err := readOptional(agentsPath)
	if err != nil {
		return err
	}
	updatedAgents, err := ki.UpsertBlock(currentAgents, instructions, ki.MarkdownComment(), "")
	if err != nil {
		return fail("%s: %v", agentsPath, err)
	}
	if err := writeFile(agentsPath, updatedAgents, 0o644); err != nil {
		return err
	}

	if wroteConfig {
		fmt.Fprintf(a.Stdout, "updated %s (%s)\n", configPath, servers)
	}
	fmt.Fprintf(a.Stdout, "updated %s (agent instructions)\n", agentsPath)
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.override.md")); err == nil {
		fmt.Fprintln(a.Stdout, "warning: AGENTS.override.md exists, so Codex ignores AGENTS.md; move the ki block into it")
	}
	fmt.Fprintln(a.Stdout, "start Codex with `ki --agent codex` so it gets this repo's token and brains")
	return nil
}

// codexServers names the MCP servers ki writes into Codex's config.
func codexServers(url string, vault ki.MCPServer) string {
	var names []string
	if url != "" {
		names = append(names, "ki-kb")
	}
	if vault.Command != "" {
		names = append(names, "ki-vault")
	}
	if len(names) == 0 {
		return "no MCP servers"
	}
	return strings.Join(names, " and ") + " MCP server" + map[bool]string{true: "s", false: ""}[len(names) > 1]
}

func (a *App) cursor(ctx context.Context) error {
	h, err := a.current(ctx, nil)
	if err != nil {
		return err
	}
	if h.root == "" {
		return fail("run this inside a git repo")
	}
	if h.session.Unmapped {
		return fail("this repo is not in projects.yaml; add it there first")
	}
	for _, f := range cursorFiles() {
		checkCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		check := exec.CommandContext(checkCtx, "git", "check-ignore", "-q", filepath.Join(h.root, f)) // #nosec G204 -- fixed arguments
		check.Dir = h.root
		err := check.Run()
		cancel()
		if err != nil {
			return fail("%s is not git-ignored; add it to your global git ignore first (see ki doctor)", f)
		}
	}
	url := a.mcpURL(h)
	token := ""
	if url != "" {
		var err error
		if token, err = readToken(a.tokenFile(h.session)); err != nil {
			return err
		}
	}
	cfg := ki.MCPConfig{MCPServers: map[string]ki.MCPServer{}}
	if url != "" {
		cfg = ki.NewMCPConfig(url, token, h.session.Brains)
	}
	cfg = a.addVaultServer(ctx, cfg, identityReadPaths(h.session))
	for name, srv := range cfg.MCPServers {
		srv.Type = "" // Cursor infers the transport from url or command
		cfg.MCPServers[name] = srv
	}
	if err := ki.WritePrivate(filepath.Join(h.root, cursorMCP), cfg); err != nil {
		return fmt.Errorf("writing %s: %w", cursorMCP, err)
	}
	instructions, err := h.instructionsText()
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(h.root, cursorRule), ki.CursorRule(instructions), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "wrote %s and %s (brains=%s)\n", cursorMCP, cursorRule, strings.Join(h.session.Brains, ","))
	fmt.Fprintln(a.Stdout, "Cursor's terminal uses gh's active account; run `gh auth switch` when you change identity.")
	return nil
}

func (a *App) validate(args []string) error {
	file := ""
	if len(args) > 0 {
		file = args[0]
	} else {
		h, err := a.harness()
		if err != nil {
			return err
		}
		file, err = a.configPath(h, ki.ProjectsFile)
		if err != nil {
			return err
		}
	}
	cfg, err := projects.Load(file)
	if errors.Is(err, projects.ErrInvalid) {
		return fail("%v", err)
	}
	if err != nil {
		return fmt.Errorf("loading %s: %w", file, err)
	}
	for _, p := range cfg.Projects {
		fmt.Fprintf(a.Stdout, "%s: %s [%s] ingest rules=%d\n", p.Name, p.Repo, p.Identity.Name, len(p.Ingest))
	}
	fmt.Fprintf(a.Stdout, "ok: %d project(s), %d identit(ies)\n", len(cfg.Projects), len(cfg.Identities))
	return nil
}
