package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

const skillUsage = `usage: ki skill run <skill> [args...] [--role writing|execution|judgement|search | --agent claude|codex|cursor-agent [--model M]]
                    [--repo PATH] [--checkout PR|BRANCH] [--allow TOOL]... [--identity NAME] [--brain B]...
                    [--timeout 20m] [--json] [--quiet] [-- skill args]

Runs one skill once, headless, through the agent its role picks (ki-role in the skill's
frontmatter, else execution), with the same skips, health and failover as ki delegate. The skill
is <name> from the harness's skills/, ~/.claude/skills or ~/.agents/skills, or a path to it.

The agent may read, and use only the skill's ki-tools plus --allow; everything else is denied.
A skill without ki-tools runs read-only. Only Claude Code takes an allow-list, so Codex and
Cursor run read-only skills only; ki-agents limits the agents further. git push needs
--allow "git push --force-with-lease": a skill's own list cannot grant it.

The run happens in an empty scratch folder; --checkout cuts a throwaway worktree of a pull
request's head or a branch from --repo (else the repo you are in), removed after the run.
Arguments for the skill that look like flags go after --.`

// skillTimeout bounds one skill run.
const skillTimeout = 20 * time.Minute

type skillArgs struct {
	skill                                        string
	args, allow, brains                          []string
	role, agent, model, identity, repo, checkout string
	agentGiven, json, quiet                      bool
	timeout                                      time.Duration
}

var skillValueFlags = map[string]bool{
	"--role": true, "--agent": true, "--model": true, "--repo": true, "--checkout": true,
	"--allow": true, "--identity": true, "--brain": true, "--timeout": true,
}

func parseSkillArgs(args []string, defaultAgent string) (skillArgs, error) {
	sa := skillArgs{agent: defaultAgent, timeout: skillTimeout}
	var pos []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			pos = append(pos, args[i+1:]...)
			i = len(args)
		case skillValueFlags[a] && i+1 < len(args):
			if err := sa.set(a, args[i+1]); err != nil {
				return sa, err
			}
			i++
		case skillValueFlags[a]:
			return sa, fail("%s needs a value", a)
		case a == "--json":
			sa.json = true
		case a == "--quiet":
			sa.quiet = true
		case strings.HasPrefix(a, "-"):
			return sa, fail("unknown flag %s: arguments for the skill go after --\n\n%s", a, skillUsage)
		default:
			pos = append(pos, a)
		}
	}
	switch {
	case len(pos) == 0:
		return sa, fail("%s", skillUsage)
	case sa.role != "" && sa.agentGiven:
		return sa, fail("--role and --agent are exclusive: the role's policy picks the agent")
	case sa.model != "" && !sa.agentGiven:
		return sa, fail("--model goes with --agent; a role's provider names its own model")
	}
	sa.skill, sa.args = pos[0], pos[1:]
	for _, t := range sa.allow {
		if err := ki.CheckSkillTool(t, true); err != nil {
			return sa, fail("--allow: %v", err)
		}
	}
	return sa, nil
}

func (sa *skillArgs) set(flag, v string) error {
	switch flag {
	case "--role":
		sa.role = v
	case "--agent":
		sa.agent, sa.agentGiven = v, true
	case "--model":
		sa.model = v
	case "--repo":
		sa.repo = v
	case "--checkout":
		sa.checkout = v
	case "--allow":
		if v = strings.TrimSpace(v); v != "" {
			sa.allow = append(sa.allow, v)
		}
	case "--identity":
		sa.identity = v
	case "--brain":
		sa.brains = append(sa.brains, v)
	case "--timeout":
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fail("--timeout %q: use a duration like 20m", v)
		}
		sa.timeout = d
	}
	return nil
}

// skillOutput is what `ki skill run --json` prints.
type skillOutput struct {
	ID     string `json:"id"`
	Skill  string `json:"skill"`
	Agent  string `json:"agent"`
	Model  string `json:"model"`
	Role   string `json:"role,omitempty"`
	Status string `json:"status"`
	// Detail is the skill's last status line, or why it did not run to the end.
	Detail    string    `json:"detail"`
	Output    string    `json:"output"`
	Tokens    ki.Tokens `json:"tokens"`
	CostUSD   float64   `json:"cost_usd"`
	DurationS float64   `json:"duration_s"`
}

func (a *App) skill(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(a.Stdout, skillUsage)
		return nil
	}
	switch args[0] {
	case "run":
		return a.skillRun(ctx, args[1:])
	case "guard":
		return a.skillGuard()
	}
	return fail("unknown skill command %q\n\n%s", args[0], skillUsage)
}

// skillGuard is the PreToolUse hook of a Claude Code skill run: it reads the
// hook's JSON on stdin and prints a deny reply for a command the run may not
// run. Started by the agent, not by hand.
func (a *App) skillGuard() error {
	in := a.Stdin
	if in == nil {
		in = os.Stdin
	}
	if err := ki.RunSkillGuard(in, a.Stdout); err != nil {
		return fmt.Errorf("skill guard: %w", err)
	}
	return nil
}

func (a *App) skillRun(ctx context.Context, args []string) error {
	sa, err := parseSkillArgs(args, orDefault(a.Getenv("KI_AGENT"), "claude"))
	if err != nil {
		return err
	}
	if sa.repo != "" {
		dir, err := filepath.Abs(sa.repo)
		if err != nil {
			return fmt.Errorf("resolving --repo: %w", err)
		}
		a.Dir = dir
	}
	s, err := a.loadSkill(sa.skill)
	if err != nil {
		return err
	}
	s.Tools = appendNew(s.Tools, sa.allow...)
	p, err := a.prepareSkill(ctx, sa, s)
	if err != nil {
		return err
	}

	id := ki.NewJobID(time.Now())
	jobDir := filepath.Join(a.jobsDir(), id)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", jobDir, err)
	}
	p.mcpPath = filepath.Join(jobDir, "mcp.json")
	defer func() { _ = os.Remove(p.mcpPath) }() // best effort: the run already ended; it holds the token

	dir, ws, cleanup, err := a.skillWorkspace(ctx, p.here.root, id, jobDir, sa.checkout)
	if err != nil {
		return err
	}
	defer cleanup()
	ws.Repo = repoSlug(ctx, p.here.root)

	da := delegateArgs{
		RunArgs: ki.RunArgs{Agent: sa.agent, Model: sa.model, Brains: sa.brains},
		Task:    s.Name, Title: strings.Join(append([]string{s.Name}, sa.args...), " "),
		Role: sa.role, AgentGiven: sa.agentGiven, Timeout: sa.timeout, Quiet: sa.quiet,
		kind: ki.KindSkill, parse: ki.ParseSkillStatus,
	}
	brief := ki.SkillBrief(s, sa.args, ws)
	da.setup = func(agent string) (workerMode, string, error) {
		wm, err := a.skillAgentSetup(p, s, agent, dir)
		return wm, brief, err
	}
	role, try, err := a.skillProviders(ctx, &da, s, p.machine, id)
	if err != nil {
		return err
	}
	r, err := a.attemptAll(ctx, p, da, try, role, id, dir, ws.Branch)
	if err != nil {
		return err
	}
	return a.printSkill(r, s.Name, sa.json)
}

// loadSkill finds a skill by name in the checkout's skills (else the embedded
// ones), then the ones linked for Claude Code and for the other agents, or by path.
func (a *App) loadSkill(name string) (*ki.Skill, error) {
	var dirs []string
	if root, _, err := a.skillsRoot(); err == nil {
		dirs = append(dirs, root)
	}
	dirs = append(dirs, filepath.Join(a.home(), ".claude", "skills"), filepath.Join(a.home(), ".agents", "skills"))
	path, err := ki.FindSkill(name, dirs)
	if err != nil {
		return nil, fail("%v", err)
	}
	s, err := ki.LoadSkill(path)
	if err != nil {
		return nil, fail("%v", err)
	}
	return s, nil
}

// prepareSkill resolves identity, brains, token and gh login as `ki ask`
// does: a repo is optional. A skill that runs gh needs the identity's login,
// so it never acts as whichever account gh has active.
func (a *App) prepareSkill(ctx context.Context, sa skillArgs, s *ki.Skill) (*prepared, error) {
	sources := ki.AskSources
	h, err := a.current(ctx, nil)
	if err != nil {
		return nil, err
	}
	local := a.mcpURL(h) == ""
	if local {
		sources = slices.DeleteFunc(slices.Clone(sources), func(s string) bool { return s == ki.SourceKB })
	}
	p, err := a.prepareAsk(ctx, askArgs{identity: sa.identity, brains: sa.brains, sources: sources}, "ki skill run")
	if err != nil {
		return nil, err
	}
	if local {
		fmt.Fprintf(a.Stderr, "ki: no knowledge base configured; running %s with identity and instructions only\n", s.Name)
	}
	p.here.root = git(ctx, a.Dir, "rev-parse", "--show-toplevel")
	if sa.repo != "" && p.here.root == "" {
		return nil, fail("--repo %s is not a git checkout", sa.repo)
	}
	if usesGH(s.Tools) && p.gh == "" {
		return nil, fail("the skill runs gh, and gh is not logged in as %s; run `gh auth login`", p.session.Identity.GitHubUser)
	}
	if err := a.useInstructions(p); err != nil {
		return nil, err
	}
	return p, nil
}

func usesGH(tools []string) bool {
	return slices.ContainsFunc(tools, func(t string) bool {
		r := ki.ClaudeToolRule(t)
		return strings.HasPrefix(r, "Bash(gh ") || strings.HasPrefix(r, "Bash(gh:")
	})
}

// repoSlug is owner/repo of a checkout's GitHub origin, "" otherwise.
func repoSlug(ctx context.Context, root string) string {
	if root == "" {
		return ""
	}
	url, err := projects.NormalizeRepo(git(ctx, root, "remote", "get-url", "origin"))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(url, "https://github.com/")
}

// skillProviders resolves who runs the skill: --agent, else --role, else the
// skill's ki-role when the machine defines it, else the execution role or
// the default agent as for a delegated job. Only agents ki can hold to the
// skill's tools take part.
func (a *App) skillProviders(ctx context.Context, da *delegateArgs, s *ki.Skill, m *ki.Machine, id string) (string, []ki.Provider, error) {
	if da.Role == "" && !da.AgentGiven && s.Role != "" {
		if _, ok := m.RoleFor(s.Role); ok {
			da.Role = s.Role
		} else {
			fmt.Fprintf(a.Stderr, "ki: the skill asks for role %s, which %s does not define; using the default\n", s.Role, a.short(a.machinePath()))
		}
	}
	if da.AgentGiven {
		if why := ki.SkillUnsupported(da.Agent, s.Tools, s.Agents); why != "" {
			return "", nil, fail("%s cannot run %s: %s", filepath.Base(da.Agent), s.Name, why)
		}
	}
	role, try, err := a.providers(ctx, *da, m, id)
	if err != nil {
		return "", nil, err
	}
	var ok []ki.Provider
	for _, p := range try {
		why := ki.SkillUnsupported(p.Agent, s.Tools, s.Agents)
		if why == "" {
			ok = append(ok, p)
			continue
		}
		fmt.Fprintf(a.Stderr, "ki: %s skipped for %s, %s\n", p.Name(), s.Name, why)
	}
	if len(ok) == 0 {
		return "", nil, fail("no agent of %s can run %s with its tools; name one with --agent claude", orDefault(role, "the default"), s.Name)
	}
	return role, ok, nil
}

// skillAgentSetup writes the MCP config one agent reads and returns its
// flags: Claude Code's allow-list, or the read-only setup of `ki ask` for
// Codex and Cursor.
func (a *App) skillAgentSetup(p *prepared, s *ki.Skill, agent, dir string) (workerMode, error) {
	set := a.askSetup(p, skillSources(a, p))
	if ki.AgentFamily(agent) == "claude" {
		cfg := ki.AskMCPConfig(set, p.token, false)
		if ki.SkillVaultWrites(s.Tools) && set.Vault.Command != "" {
			cfg.MCPServers[ki.VaultServerName] = ki.VaultServer(a.binary(), a.Getenv("KI_VAULT"))
		}
		if err := ki.WritePrivate(p.mcpPath, cfg); err != nil {
			return workerMode{}, fmt.Errorf("writing MCP config: %w", err)
		}
		bin := a.binary()
		if bin == "" {
			return workerMode{}, errors.New("cannot find the ki binary, which checks the skill's shell commands")
		}
		args := ki.SkillClaudeArgs(s.Tools, []string{s.Dir}, bin, ki.NewSubagentGuard(bin, p.machine, true))
		return workerMode{args: args, readOnly: len(s.Tools) == 0, allowList: len(s.Tools) > 0}, nil
	}
	if ki.AgentFamily(agent) == "cursor" {
		files := map[string]any{ki.CursorMCPFile: ki.AskMCPConfig(set, p.token, true), ki.CursorCLIFile: ki.AskCursorPermissions(set.Sources)}
		for name, v := range files {
			if err := ki.WritePrivate(filepath.Join(dir, filepath.FromSlash(name)), v); err != nil {
				return workerMode{}, fmt.Errorf("writing Cursor's %s: %w", name, err)
			}
		}
	}
	return workerMode{args: ki.AskArgs(agent, set), readOnly: true}, nil
}

// skillSources are the MCP sources a skill run reads: the knowledge base
// when one is configured, and the vault when this machine has one.
func skillSources(a *App, p *prepared) []string {
	var src []string
	if p.url != "" {
		src = append(src, ki.SourceKB)
	}
	if a.Getenv("KI_VAULT") != "" && a.binary() != "" {
		src = append(src, ki.SourceVault)
	}
	return src
}

// printSkill prints the skill's final message, or the JSON object, and
// fails for a run that did not finish.
func (a *App) printSkill(r ki.Result, skill string, asJSON bool) error {
	if asJSON {
		out := skillOutput{
			ID: r.ID, Skill: skill, Agent: r.Provider, Model: r.Model, Role: r.Role, Status: r.Status, Detail: r.Detail,
			Output: strings.TrimSpace(r.Last), Tokens: r.Tokens, CostUSD: r.CostUSD, DurationS: r.DurationS,
		}
		if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
			return fmt.Errorf("printing the result: %w", err)
		}
	} else if r.Last != "" {
		fmt.Fprintln(a.Stdout, strings.TrimSpace(r.Last))
	}
	if r.Status != ki.JobDone && r.Status != ki.JobPR {
		return fail("%s %s %s: %s (see %s)", r.ID, skill, r.Status, r.Detail, filepath.Join(a.jobsDir(), r.ID, "output.log"))
	}
	return nil
}
