package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kislow/kio/internal/ki"
)

// A doctor run is a few sections of rows. Each row has a short label (what
// was checked), a detail (what was found) and, when it failed, a fix. Rows
// are collected first and printed once, so the labels line up.
type row struct {
	mark   string // "✓", "✗", "!" for a warning or "·" for a note
	label  string
	detail string
	fix    string
}

type section struct {
	title string
	rows  []row
}

type checks struct {
	out      *App
	sections []*section
	failures int
	passes   int
}

func (c *checks) section(title string) {
	c.sections = append(c.sections, &section{title: title})
}

func (c *checks) add(r row) {
	if len(c.sections) == 0 {
		c.section("Checks")
	}
	s := c.sections[len(c.sections)-1]
	s.rows = append(s.rows, r)
}

func (c *checks) report(ok bool, label, detail, fix string) {
	if ok {
		c.passes++
		c.add(row{mark: "✓", label: label, detail: detail})
		return
	}
	c.failures++
	c.add(row{mark: "✗", label: label, detail: detail, fix: fix})
}

// warn flags something worth acting on that is not a failure, so it leaves
// doctor's exit code alone.
func (c *checks) warn(label, detail string) { c.add(row{mark: "!", label: label, detail: detail}) }

func (c *checks) note(label, detail string) { c.add(row{mark: "·", label: label, detail: detail}) }

// palette is the ANSI styling used when stdout is a terminal; every field is
// empty otherwise, so logs and tests see plain text.
type palette struct{ ok, bad, warn, dim, bold, reset string }

func newPalette(w io.Writer, getenv func(string) string) palette {
	f, isFile := w.(*os.File)
	if !isFile || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return palette{}
	}
	if info, err := f.Stat(); err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return palette{}
	}
	return palette{ok: "\033[32m", bad: "\033[31m", warn: "\033[33m", dim: "\033[2m", bold: "\033[1m", reset: "\033[0m"}
}

func (c *checks) render(version string) {
	p := newPalette(c.out.Stdout, c.out.Getenv)
	width := 0
	for _, s := range c.sections {
		for _, r := range s.rows {
			if n := utf8.RuneCountInString(r.label); n > width {
				width = n
			}
		}
	}
	w := c.out.Stdout
	fmt.Fprintf(w, "%ski doctor%s %s%s%s\n", p.bold, p.reset, p.dim, version, p.reset)
	for _, s := range c.sections {
		if len(s.rows) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s%s%s\n", p.bold, s.title, p.reset)
		for _, r := range s.rows {
			color := p.ok
			switch r.mark {
			case "✗":
				color = p.bad
			case "!":
				color = p.warn
			case "·":
				color = p.dim
			}
			fmt.Fprintf(w, "  %s%s%s %-*s  %s\n", color, r.mark, p.reset, width, r.label, r.detail)
			if r.fix != "" {
				fmt.Fprintf(w, "    %s%*s  fix: %s%s\n", p.dim, width, "", r.fix, p.reset)
			}
		}
	}
	fmt.Fprintln(w)
	total := c.passes + c.failures
	switch c.failures {
	case 0:
		fmt.Fprintf(w, "%s✓ all good%s, %d checks\n", p.ok, p.reset, total)
	case 1:
		fmt.Fprintf(w, "%s✗ 1 problem%s of %d checks\n", p.bad, p.reset, total)
	default:
		fmt.Fprintf(w, "%s✗ %d problems%s of %d checks\n", p.bad, c.failures, p.reset, total)
	}
}

// short writes a path under the home directory as ~/....
func (a *App) short(path string) string {
	home := a.home()
	if home != "" && home != "." && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

func (a *App) doctor(ctx context.Context) error {
	c := &checks{out: a}
	c.section("Harness")
	h, err := a.current(ctx, nil)
	if err != nil {
		c.report(false, "checkout", err.Error(), "link a configuration directory: ki setup config <dir> (INSTALL.md, step 3)")
		c.render(a.Version)
		return fail("1 problem")
	}
	if h.harness == "" {
		c.report(true, "checkout", fmt.Sprintf("embedded (version %s), %d projects", a.Version, len(h.cfg.Projects)), "")
	} else {
		c.report(true, "checkout", fmt.Sprintf("%s, %d projects", a.short(h.harness), len(h.cfg.Projects)), "")
	}
	m, merr := a.machine(h.cfg)
	switch {
	case merr != nil:
		c.report(false, "machine", merr.Error(), "fix "+a.short(a.machinePath())+" or rewrite it: ki setup machine <identity>...")
	case m == nil:
		c.note("machine", "no "+a.short(a.machinePath())+": every identity in projects.yaml is expected here (ki setup machine <identity>... to narrow)")
	default:
		c.report(true, "machine", "carries "+strings.Join(m.Identities, ", "), "")
		if len(m.Models) == 0 {
			c.note("model policy", "none in "+a.short(a.machinePath())+": agents use their own defaults")
		} else {
			parts := []string{}
			for _, class := range ki.Classes {
				for agent, model := range m.Models[class] {
					line := class + " " + agent + "=" + model
					if e := m.EffortFor(class); e != "" {
						line += "/" + e
					}
					parts = append(parts, line)
				}
			}
			sort.Strings(parts)
			c.report(true, "model policy", strings.Join(parts, ", "), "")
		}
		for _, name := range m.RoleNames() {
			r := m.Roles[name]
			names := make([]string, 0, len(r.Providers))
			for _, p := range r.Providers {
				names = append(names, p.Name())
			}
			c.report(true, "role "+name, r.Strategy+" over "+strings.Join(names, ", "), "")
		}
		if _, ok := m.RoleFor(ki.RolePlanner); ok {
			a.checkPlanner(c, m)
		}
		if len(m.Roles) > 0 {
			if health, err := a.providerHealth(); err != nil {
				c.warn("delegate health", err.Error())
			} else {
				c.note("delegate health", healthSummary(health))
			}
		}
	}
	switch h.configSource {
	case ki.ConfigFromHarness:
		c.note("config", a.short(h.config)+", the harness's own copy; `ki setup config <dir>` links a configuration of your own")
	default:
		c.report(true, "config", a.short(h.config)+" ("+h.configSource+")", "")
	}
	if f := h.configInstructions(); f != "" {
		c.note("instructions", a.short(f)+", added after the harness's agent instructions for every agent")
	}
	a.checkBehind(ctx, c, h)
	a.checkTokens(c, h)
	a.checkKB(ctx, c, h)

	c.section("Git and GitHub")
	a.checkTools(ctx, c, h, m)
	if h.root != "" && h.session.Identity != nil {
		email := git(ctx, h.root, "config", "user.email")
		p := ki.IdentityProblem(h.session, email)
		c.report(p == "", "commit email", orDefault(email, "<unset>")+" in this repo", p)
	}

	if a.vaultConfigured(h) {
		c.section("Vault")
		a.checkVault(ctx, c)

		c.section("Diagrams")
		a.checkDiagrams(ctx, c)
	}

	c.section("Agents")
	a.checkAgents(c, h)
	a.checkTmux(c)
	for _, e := range extensions {
		if e.doctor != nil {
			e.doctor(a, c)
		}
	}
	a.checkGitIgnore(ctx, c)

	c.render(a.Version)
	if c.failures > 0 {
		return fail("%d problem(s)", c.failures)
	}
	return nil
}

func (a *App) checkTokens(c *checks, h *here) {
	withKB := a.mcpURL(h) != ""
	needsSensitive := false
	for _, p := range h.cfg.Projects {
		for _, b := range p.Brains {
			needsSensitive = needsSensitive || h.cfg.IsSensitive(b)
		}
	}
	for _, t := range []struct {
		path, label string
		required    bool
	}{
		{ki.TokenFile(a.confDir(), ki.Session{}), "device token", withKB},
		{ki.TokenFile(a.confDir(), ki.Session{Sensitive: true}), "sensitive token", withKB && needsSensitive},
	} {
		path := t.path
		info, err := os.Stat(path)
		switch {
		case err == nil:
			loose := info.Mode().Perm()&0o077 != 0
			detail := a.short(path)
			if loose {
				detail += " is readable by others"
			}
			if filepath.Base(path) == ki.LegacySensitiveTokenName {
				detail += " (deprecated name, rename to token-sensitive)"
			}
			c.report(!loose, t.label, detail, "chmod 600 "+path)
		case t.required:
			c.report(false, t.label, a.short(path)+" missing", "see docs/laptop-setup.md, step 'KB token'")
		}
	}
}

func (a *App) checkKB(ctx context.Context, c *checks, h *here) {
	url := a.mcpURL(h)
	if url == "" {
		c.note("knowledge base", "not configured (optional)")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok := false
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/healthz", http.NoBody)
	if err == nil {
		client := http.Client{Timeout: 3 * time.Second}
		if resp, err := client.Do(req); err == nil {
			ok = resp.StatusCode == http.StatusOK
			_ = resp.Body.Close() // nothing to do if closing a health response fails
		}
	}
	detail := url + " answers"
	if !ok {
		detail = url + " unreachable"
	}
	c.report(ok, "knowledge base", detail, "connect the VPN the server sits behind, or set KI_MCP_URL")
}

func (a *App) checkTools(ctx context.Context, c *checks, h *here, m *ki.Machine) {
	for _, tool := range []struct{ bin, fix string }{
		{"git", "install git"}, {"gh", "install the GitHub CLI"},
	} {
		path, err := exec.LookPath(tool.bin)
		c.report(err == nil, tool.bin, orDefault(a.short(path), "not installed"), tool.fix)
	}
	if _, err := exec.LookPath("gh"); err == nil {
		for _, id := range h.cfg.Identities {
			detail := fmt.Sprintf("%s (%s)", id.GitHubUser, id.Name)
			if !m.Carries(id.Name) {
				c.note("gh login", detail+" not on this machine")
				continue
			}
			ok := ghToken(ctx, id.GitHubUser) != ""
			if !ok {
				detail += " not logged in"
			}
			c.report(ok, "gh login", detail, "gh auth login  # sign in as "+id.GitHubUser)
		}
	}
}

// vaultConfigured is whether this machine opted into the vault integration:
// KI_VAULT is exported, or projects.yaml has a workspace with vault folders.
// Without either, doctor says nothing about the vault or the diagram builder.
func (a *App) vaultConfigured(h *here) bool {
	return a.Getenv("KI_VAULT") != "" || len(h.cfg.Workspaces) > 0
}

// checkVault tells whether `ki vault push`, and so the note-writing skills,
// can work on this machine once a vault is configured. A folder that is not a
// git clone is a supported read-only vault, not a failure.
func (a *App) checkVault(ctx context.Context, c *checks) {
	dir := a.Getenv("KI_VAULT")
	if dir == "" {
		c.note("KI_VAULT", "not set: the skills will not save notes, and ki vault push does nothing here")
		return
	}
	if info, err := os.Stat(dir); err == nil && info.IsDir() && !a.vaultIsClone(ctx) {
		c.report(true, "KI_VAULT", a.short(dir)+" read-only (not a git clone)", "")
		c.report(a.binary() != "", "vault server", "ki vault mcp --read-only is offered to ki sessions", "reinstall ki: make install-cli")
		return
	}
	ok := a.vaultIsClone(ctx) && git(ctx, dir, "remote", "get-url", "origin") != ""
	detail := a.short(dir)
	if !ok {
		detail += " is not a git clone with an origin"
	}
	c.report(ok, "KI_VAULT", detail, "clone the vault repo there; see docs/laptop-setup.md")
	if ok {
		c.report(a.binary() != "", "vault server", "ki vault mcp is offered to ki sessions", "reinstall ki: make install-cli")
	}
}

func (a *App) checkGitIgnore(ctx context.Context, c *checks) {
	ignore := git(ctx, "", "config", "--global", "core.excludesFile")
	if ignore == "" {
		ignore = filepath.Join(a.home(), ".config", "git", "ignore")
	} else if strings.HasPrefix(ignore, "~/") {
		ignore = filepath.Join(a.home(), ignore[2:])
	}
	content, _ := readOptional(ignore) // a missing ignore file is reported below
	for _, f := range cursorFiles() {
		detail := a.short(ignore) + " ignores " + f
		if !strings.Contains(content, f) {
			detail = a.short(ignore) + " does not ignore " + f
		}
		c.report(strings.Contains(content, f), "global ignore", detail, "echo "+f+" >> "+ignore)
	}
}

func (a *App) checkAgents(c *checks, h *here) {
	found := []string{}
	for _, agent := range []string{"claude", "codex", "cursor-agent", "cursor", "agy"} {
		if _, err := exec.LookPath(agent); err == nil {
			found = append(found, agent)
		}
	}
	c.report(len(found) > 0, "on PATH", orDefault(strings.Join(found, ", "), "none"), "install Claude Code, Codex, Cursor or agy")

	if _, err := exec.LookPath("claude"); err == nil {
		settings, _ := readOptional(a.claudeSettingsPath()) // unreadable counts as not set
		ok := ki.ClaudeAttributionIsOff([]byte(settings))
		c.report(ok, "Claude Code", map[bool]string{true: "no Co-Authored-By or PR attribution", false: "still adds Co-Authored-By or PR attribution"}[ok], "ki setup claude")
	}
	if _, err := exec.LookPath("codex"); err == nil {
		cfg, _ := readOptional(filepath.Join(a.codexHome(), "config.toml"))
		agents, _ := readOptional(filepath.Join(a.codexHome(), "AGENTS.md"))
		_, overrideErr := os.Stat(filepath.Join(a.codexHome(), "AGENTS.override.md"))
		kb := a.mcpURL(h) != ""
		if !kb {
			c.note("Codex", "no knowledge base configured")
		}
		for _, t := range []struct {
			ok       bool
			yes, no  string
			fix      string
			relevant bool
		}{
			{ki.HasBlock(cfg, ki.TOMLComment()), "knows the ki-kb server", "does not know the ki-kb server", "ki setup codex", kb},
			{strings.Contains(cfg, ki.CodexVaultTable), "knows the ki-vault server", "does not know the ki-vault server", "ki setup codex", a.Getenv("KI_VAULT") != ""},
			{ki.HasBlock(agents, ki.MarkdownComment()), "has the agent instructions", "lacks the agent instructions", "ki setup codex", true},
			{overrideErr != nil, "no AGENTS.override.md hiding AGENTS.md", "AGENTS.override.md hides AGENTS.md", "move the ki block into AGENTS.override.md", true},
		} {
			if !t.relevant {
				continue
			}
			c.report(t.ok, "Codex", map[bool]string{true: t.yes, false: t.no}[t.ok], t.fix)
		}
	}
	for _, agent := range []string{"cursor-agent", "cursor"} {
		if _, err := exec.LookPath(agent); err == nil {
			c.note("Cursor", "turn off both switches under Settings > Agents > Attribution (ki cannot check them)")
			break
		}
	}
}

// checkPlanner reports the agent a bare `ki` would start and why. A role with
// no eligible provider is a warning: sessions named with --agent still start.
func (a *App) checkPlanner(c *checks, m *ki.Machine) {
	sa, err := a.resolveSession("", m)
	if err != nil {
		c.warn("planner", err.Error())
		return
	}
	line := sa.Agent + " via " + sa.Source
	if model := sa.Model("", a.Getenv("KI_MODEL"), m); model != "" {
		line = sa.Agent + ":" + model + " via " + sa.Source
	}
	for _, n := range sa.Notes() {
		line += "; " + n.Provider + " " + n.Reason
	}
	c.note("planner", line)
}
