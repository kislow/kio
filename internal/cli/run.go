package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// runFileMaxAge is how long a generated MCP config (it holds a token) is kept.
const runFileMaxAge = 24 * time.Hour

func (a *App) run(ctx context.Context, args []string) error {
	shorthand, args := ki.SplitShorthand(args)
	ra := ki.ParseRunArgs(args, "")
	if shorthand != "" {
		if ra.AgentGiven {
			return fail("ki %s and --agent both name the agent; use one", shorthand)
		}
		ra.Agent = shorthand
	}
	p, err := a.prepare(ctx, ra)
	if err != nil {
		return err
	}
	sa, err := a.resolveSession(ra.Agent, p.machine)
	printSkipped(a.Stderr, ki.RolePlanner, sa.Skipped)
	if err != nil {
		return err
	}
	ra.Agent = sa.Agent
	modeArgs, modeLabel, err := a.mode(ra)
	if err != nil {
		return err
	}
	if modeLabel == string(ki.ModeEdits) {
		if modeArgs, err = a.editsMode(ra, p.machine.DelegateAllow()); err != nil {
			return err
		}
	}
	// The session is judgement work: --model, then KI_MODEL, then the planner's pick, then the policy.
	model := sa.Model(ra.Model, a.Getenv("KI_MODEL"), p.machine)
	in := p.launchInput(ra, model, modeArgs)
	in.Settings = ki.SessionSettings(ki.NewSubagentGuard(a.binary(), p.machine, false))
	launch := ki.BuildLaunch(in)

	shown := ""
	if model != "" {
		shown = " | model=" + model
	}
	if sa.Source == ki.SourcePlanner {
		shown += " | planner=" + sa.Provider.Name()
	}
	if p.url == "" {
		fmt.Fprintf(a.Stderr, "ki: no knowledge base configured; starting %s with identity and instructions only\n", filepath.Base(ra.Agent))
		fmt.Fprintf(a.Stderr, "ki: %s | mode=%s%s\n", p.who(), modeLabel, shown)
	} else {
		fmt.Fprintf(a.Stderr, "ki: %s | brains=%s | token=%s | mode=%s%s\n", p.who(), strings.Join(p.session.Brains, ","), filepath.Base(p.tokenPath), modeLabel, shown)
	}

	bin, err := exec.LookPath(launch.Argv[0])
	if err != nil {
		return fail("agent %q not found on PATH", launch.Argv[0])
	}
	if err := a.Exec(bin, launch.Argv, ki.MergeEnv(a.Environ(), launch.Env)); err != nil {
		return fmt.Errorf("starting %s: %w", launch.Argv[0], err)
	}
	return nil
}

// prepared is everything a launch needs from the repo, the configuration and
// the machine, after every refusal `ki` makes: unmapped repo, wrong git
// identity, an identity this machine does not carry, no gh login, no token.
type prepared struct {
	here      *here
	session   ki.Session
	machine   *ki.Machine
	token     string
	tokenPath string
	gh        string
	mcpPath   string
	url       string
	// instructions is the file Claude Code appends to its system prompt, "" for none.
	instructions string
	// instructionsText is the same text for agents that take it in the prompt, "" for none.
	instructionsText string
}

func (a *App) prepare(ctx context.Context, ra ki.RunArgs) (*prepared, error) {
	if err := ki.ValidateBrains(ra.Brains); err != nil {
		return nil, fail("%v", err)
	}
	h, err := a.current(ctx, ra.Brains)
	if err != nil {
		return nil, err
	}
	s := h.session
	switch {
	case ra.Identity != "":
		if s, err = sessionIdentity(h, ra.Identity, ra.Force, ra.Brains); err != nil {
			return nil, err
		}
	case len(h.mixed) > 0:
		return nil, h.mixedProblem()
	}
	if s.Folder {
		fmt.Fprintln(a.Stderr, folderNotice(s))
	} else {
		if s.Unmapped && !ra.Force {
			return nil, fail("this repo is not in projects.yaml, so ki cannot check its identity or choose its gh account; " +
				"add it there, or pass --force for a session without an identity, acting as gh's active account")
		}
		if p := ki.IdentityProblem(s, git(ctx, h.root, "config", "user.email")); p != "" && !ra.Force {
			return nil, fail("%s (or pass --force)", p)
		}
	}
	m, err := a.machine(h.cfg)
	if err != nil {
		return nil, err
	}
	if s.Identity != nil && !ra.Force && !m.Carries(s.Identity.Name) {
		subject := "this repo"
		if s.Folder {
			subject = "this session"
		}
		return nil, fail("%s belongs to identity %q, which this machine does not carry; add it with `ki setup machine %s ...` (or pass --force)",
			subject, s.Identity.Name, strings.Join(append(m.Identities, s.Identity.Name), " "))
	}
	url := a.mcpURL(h)
	tokenPath, token := "", ""
	if url != "" {
		tokenPath = a.tokenFile(s)
		if token, err = readToken(tokenPath); err != nil {
			return nil, err
		}
	}
	gh := ""
	if s.Identity != nil {
		gh = ghToken(ctx, s.Identity.GitHubUser)
		if gh == "" && !ra.Force {
			return nil, fail("gh is not logged in as %s; run `gh auth login`", s.Identity.GitHubUser)
		}
	}

	a.pruneRunFiles()
	runDir := filepath.Join(a.confDir(), "run")
	sum := sha256.Sum256([]byte(h.runKey(s)))
	mcpPath := filepath.Join(runDir, "mcp-"+hex.EncodeToString(sum[:8])+".json")
	cfg := ki.MCPConfig{MCPServers: map[string]ki.MCPServer{}}
	if url != "" {
		cfg = ki.NewMCPConfig(url, token, s.Brains)
	}
	cfg = ki.AddVaultServer(cfg, a.binary(), a.Getenv("KI_VAULT"))
	if err := ki.WritePrivate(mcpPath, cfg); err != nil {
		return nil, fmt.Errorf("writing MCP config: %w", err)
	}
	p := &prepared{here: h, session: s, machine: m, token: token, tokenPath: tokenPath, gh: gh, mcpPath: mcpPath, url: url}
	if err := a.useInstructions(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *prepared) launchInput(ra ki.RunArgs, model string, modeArgs []string) ki.LaunchInput {
	return ki.LaunchInput{
		Agent: ra.Agent, AgentArgs: ra.AgentArgs, Session: p.session, Token: p.token, MCPURL: p.url,
		MCPConfigPath: p.mcpPath, InstructionsPath: p.instructions, GHToken: p.gh,
		Model: model, ModeArgs: modeArgs,
	}
}

// who names the session's identity for banners and job records.
func (p *prepared) who() string {
	if p.session.Identity != nil {
		return p.session.Identity.Name
	}
	if p.session.Folder {
		return "no identity, gh active account"
	}
	return "unmapped repo, gh active account"
}

// mode resolves --mode, else KI_MODE, else ask, into the agent's flags and the
// label the session banner shows.
func (a *App) mode(ra ki.RunArgs) (args []string, label string, err error) {
	m, err := ki.ParseMode(orDefault(ra.Mode, a.Getenv("KI_MODE")))
	if err != nil {
		return nil, "", fail("%v", err)
	}
	args, overridden, err := ki.ModeArgs(ra.Agent, m, ra.AgentArgs)
	if err != nil {
		return nil, "", fail("%v", err)
	}
	if overridden {
		return nil, "set by agent args", nil
	}
	return args, string(m), nil
}

// editsMode is the session's flags in edits mode once the machine file is
// read: the agent runs the commands in delegate.allow without asking and
// prompts for any other, since a person is there to answer. agy reads its
// allow-list from its settings file, so ki adds the entries there first.
func (a *App) editsMode(ra ki.RunArgs, allow []string) ([]string, error) {
	args, _, err := ki.ModeArgsAllow(ra.Agent, ki.ModeEdits, ra.AgentArgs, allow)
	if err != nil {
		return nil, fail("%v", err)
	}
	if filepath.Base(ra.Agent) == "agy" {
		if err := a.ensureAgyAllow(allow); err != nil {
			return nil, err
		}
	}
	return args, nil
}
