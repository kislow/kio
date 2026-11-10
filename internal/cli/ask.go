package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
	"github.com/kislow/kio/pkg/scope"
)

const askUsage = `usage: ki ask "<question>" [--continue <job-id>] [--role search|writing|judgement|execution | --agent claude|codex|cursor-agent [--model M]]
              [--identity NAME] [--brain B]... [--source kb,vault,gh] [--timeout 10m] [--json] [--quiet]

One read-only headless run that answers the question from the knowledge base, the vault and
GitHub, citing a source for every fact. No worktree, no branch, no commit: the agent runs in a
scratch folder under the job and cannot write. The agent comes from roles.search in machine.yaml
(or --role, or --agent), picked as ki delegate picks; agy is never used, ki cannot hold it read-only.

The scope is a session's in this repo when it belongs to the identity; otherwise every brain of
the identity's projects that is not sensitive. The identity is --identity, else the repo's, else
the machine's only one. --brain names the brains instead. --source limits where the answer may come from (default
all three); GitHub is read through gh on Claude Code only.

--continue <job-id> asks a follow-up in the conversation of an earlier ask on this machine: same agent,
model, identity, brains and sources (so none of those flags may be given), resuming its agent session. When
the session cannot be resumed, a fresh run gets the earlier questions and answers in its prompt
("ki: resumed by transcript"). --json adds "continues" and "resumed" ("session" or "transcript").`

// askTimeout bounds one question.
const askTimeout = 10 * time.Minute

type askArgs struct {
	question, role, agent, model, identity, cont string
	brains, sources                              []string
	agentGiven, sourcesGiven, json, quiet        bool
	timeout                                      time.Duration
	// scoped are the flags of the conversation's scope that were given, which --continue refuses.
	scoped []string
}

// askValueFlags are the flags that take a value, for the error when it is missing.
var askValueFlags = map[string]bool{
	"--role": true, "--agent": true, "--model": true, "--identity": true, "--brain": true, "--source": true, "--timeout": true,
	"--continue": true,
}

// askScopeFlags fix who answers and from what; a conversation keeps them.
var askScopeFlags = map[string]bool{"--role": true, "--agent": true, "--model": true, "--identity": true, "--brain": true, "--source": true}

func parseAskArgs(args []string, defaultAgent string) (askArgs, error) {
	aa := askArgs{agent: defaultAgent, sources: ki.AskSources, timeout: askTimeout}
	var question []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case askValueFlags[a] && i+1 < len(args):
			if err := aa.set(a, args[i+1]); err != nil {
				return aa, err
			}
			if askScopeFlags[a] && !slices.Contains(aa.scoped, a) {
				aa.scoped = append(aa.scoped, a)
			}
			i++
		case askValueFlags[a]:
			return aa, fail("%s needs a value", a)
		case a == "--json":
			aa.json = true
		case a == "--quiet":
			aa.quiet = true
		case strings.HasPrefix(a, "-"):
			return aa, fail("unknown flag %s\n\n%s", a, askUsage)
		default:
			question = append(question, a)
		}
	}
	aa.question = strings.TrimSpace(strings.Join(question, " "))
	switch {
	case aa.question == "":
		return aa, fail("%s", askUsage)
	case aa.cont != "" && len(aa.scoped) > 0:
		return aa, fail("--continue keeps the conversation's scope: drop %s", strings.Join(aa.scoped, ", "))
	case aa.role != "" && aa.agentGiven:
		return aa, fail("--role and --agent are exclusive: the role's policy picks the agent")
	case aa.model != "" && !aa.agentGiven:
		return aa, fail("--model goes with --agent; a role's provider names its own model")
	}
	return aa, nil
}

// set takes one flag's value.
func (aa *askArgs) set(flag, v string) error {
	switch flag {
	case "--role":
		aa.role = v
	case "--agent":
		aa.agent, aa.agentGiven = v, true
	case "--model":
		aa.model = v
	case "--identity":
		aa.identity = v
	case "--brain":
		aa.brains = append(aa.brains, v)
	case "--source":
		src, err := ki.ParseSources(v)
		if err != nil {
			return fail("%v", err)
		}
		aa.sources, aa.sourcesGiven = src, true
	case "--continue":
		if !ki.ValidJobID(v) {
			return fail("--continue %q is not a job id (like j-20261107-063902-d85f)", v)
		}
		aa.cont = v
	case "--timeout":
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fail("--timeout %q: use a duration like 10m", v)
		}
		aa.timeout = d
	}
	return nil
}

// askOutput is what `ki ask --json` prints.
type askOutput struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Model     string    `json:"model"`
	Role      string    `json:"role,omitempty"`
	Answer    string    `json:"answer"`
	Sources   []string  `json:"sources"`
	Tokens    ki.Tokens `json:"tokens"`
	CostUSD   float64   `json:"cost_usd"`
	DurationS float64   `json:"duration_s"`
	Status    string    `json:"status"`
	// Detail says why a question was not answered; empty when it was.
	Detail string `json:"detail,omitempty"`
	// Continues and Resumed are set on a follow-up: the job it continues and
	// whether the agent's session or the earlier turns carried the conversation.
	Continues string `json:"continues,omitempty"`
	Resumed   string `json:"resumed,omitempty"`
}

// askRun is the part of an ask job its attempts share: what the job records
// for a follow-up, and how the earlier conversation reaches the agent.
type askRun struct {
	state ki.AskState
	// continues is the job this follows; resumed is how ("" for a first question),
	// session the agent session to reopen when resumed is "session", history the
	// earlier turns to put in the prompt when it is "transcript".
	continues, resumed, session, history string
}

// continuation is the earlier ask job a follow-up continues.
type continuation struct {
	prev ki.Result
}

// loadContinuation reads the job --continue names. Jobs live in this machine's
// jobs folder, so another machine's id is simply unknown here.
func (a *App) loadContinuation(id string) (*continuation, error) {
	prev, err := ki.ReadResult(a.jobsDir(), id)
	switch {
	case err != nil:
		return nil, fail("job %s is not on this machine, and only this machine's ask jobs can be continued", id)
	case prev.Kind != ki.KindAsk:
		return nil, fail("job %s was not an ask, so there is no conversation to continue", id)
	case prev.Status != ki.JobDone:
		return nil, fail("job %s ended %s: there is no answer to continue from", id, prev.Status)
	case prev.Ask == nil:
		return nil, fail("job %s predates --continue and did not record its scope; ask the question again", id)
	}
	return &continuation{prev: prev}, nil
}

// apply gives the follow-up the scope of the job it continues.
func (c *continuation) apply(aa *askArgs) {
	aa.agent, aa.agentGiven, aa.model = c.prev.Provider, true, c.prev.ModelRequested
	aa.identity, aa.brains, aa.sources = c.prev.Ask.Identity, c.prev.Ask.Brains, c.prev.Ask.Sources
}

// ownScratch is whether dir is the scratch folder of a job in this machine's jobs
// folder, the only kind ki removes after a run.
func (a *App) ownScratch(dir string) bool {
	return filepath.Base(dir) == "scratch" && filepath.Dir(filepath.Dir(dir)) == a.jobsDir() && ki.ValidJobID(filepath.Base(filepath.Dir(dir)))
}

// followUpRunning is whether a running ask already uses the scratch folder.
func (a *App) followUpRunning(scratch string) bool {
	running, _ := ki.ReadRunning(a.jobsDir()) // an unreadable jobs folder has no run to protect
	return slices.ContainsFunc(running, func(r ki.Result) bool {
		return r.Status == ki.JobRunning && r.Ask != nil && r.Ask.Scratch == scratch
	})
}

// sessionResumable is whether the earlier job left a session its agent can reopen.
func (c *continuation) sessionResumable() bool {
	return ki.ResumeArgs(c.prev.Provider, c.prev.Session) != nil
}

// sessionLost is whether a run that reopened a session failed in a way that
// a fresh run with the transcript may fix: not a timeout, a stall or a usage limit.
func sessionLost(r ki.Result) bool {
	return (r.Status == ki.JobFailed || r.Status == ki.JobLaunchFailed) && !r.TimedOut && r.QuotaUntil == nil
}

func (a *App) ask(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(a.Stdout, askUsage)
		return nil
	}
	aa, err := parseAskArgs(args, orDefault(a.Getenv("KI_AGENT"), "claude"))
	if err != nil {
		return err
	}
	var conv *continuation
	if aa.cont != "" {
		if conv, err = a.loadContinuation(aa.cont); err != nil {
			return err
		}
		conv.apply(&aa)
	}
	p, err := a.prepareAsk(ctx, aa, "ki ask")
	if err != nil {
		return err
	}
	id := ki.NewJobID(time.Now())
	jobDir := filepath.Join(a.jobsDir(), id)
	scratch := filepath.Join(jobDir, "scratch")
	if conv != nil && a.ownScratch(conv.prev.Ask.Scratch) {
		// A second follow-up would share the folder the first removes when it ends, and reopen the same session.
		if a.followUpRunning(conv.prev.Ask.Scratch) {
			return fail("a follow-up of %s is still running; continue once it ends", aa.cont)
		}
		// An agent files its sessions under the folder it ran in, so a follow-up runs where the conversation began.
		scratch = conv.prev.Ask.Scratch
	}
	if err := os.MkdirAll(scratch, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", scratch, err)
	}
	// The MCP config holds the token: it lives next to the scratch folder,
	// outside what the agent reads, and goes with it when the run ends.
	p.mcpPath = filepath.Join(jobDir, "mcp.json")
	defer func() {
		_ = os.Remove(p.mcpPath)  // best effort: the run already ended
		_ = os.RemoveAll(scratch) // best effort: nothing in it is kept
	}()

	src := aa.sources
	run := &askRun{state: ki.AskState{
		Question: aa.question, Identity: p.session.Identity.Name, Brains: p.session.Brains, Scratch: scratch,
	}}
	if conv != nil {
		run.continues, run.resumed = conv.prev.ID, ki.ResumedTranscript
		if conv.sessionResumable() {
			run.resumed, run.session = ki.ResumedSession, conv.prev.Session
		} else {
			run.history = ki.AskHistory(ki.AskChain(a.jobsDir(), conv.prev), ki.AskHistoryMax)
		}
	}
	da := delegateArgs{
		RunArgs: ki.RunArgs{Agent: aa.agent, Model: aa.model, Brains: aa.brains},
		Task:    aa.question, Title: aa.question, Role: aa.role, AgentGiven: aa.agentGiven,
		Timeout: aa.timeout, Quiet: aa.quiet, kind: ki.KindAsk, class: ki.ClassSearch,
		parse: ki.ParseAskStatus, ask: run,
	}
	da.setup = func(agent string) (workerMode, string, error) {
		var err error
		src, err = a.askAgentSetup(p, aa, agent, scratch)
		if err != nil {
			return workerMode{}, "", err
		}
		run.state.Sources = src
		args := ki.AskArgs(agent, a.askSetup(p, src))
		return workerMode{args: args, readOnly: true}, ki.AskBriefAfter(aa.question, src, run.history), nil
	}
	role, try, err := a.askProviders(ctx, &da, p.machine, id)
	if err != nil {
		return err
	}
	if conv != nil {
		role = conv.prev.Role
		if run.resumed == ki.ResumedTranscript {
			fmt.Fprintln(a.Stderr, "ki: resumed by transcript")
		}
	}
	r, err := a.attemptAll(ctx, p, da, try, role, id, scratch, "")
	if err != nil {
		return err
	}
	if run.resumed == ki.ResumedSession && sessionLost(r) {
		run.resumed, run.session = ki.ResumedTranscript, ""
		run.history = ki.AskHistory(ki.AskChain(a.jobsDir(), conv.prev), ki.AskHistoryMax)
		fmt.Fprintln(a.Stderr, "ki: resumed by transcript")
		if r, err = a.attemptAll(ctx, p, da, try, role, ki.NewJobID(a.now()), scratch, ""); err != nil {
			return err
		}
	}
	return a.printAsk(r, src, aa.json)
}

// printAsk prints the answer, as markdown or as the JSON object, and fails
// for a question that was not answered.
func (a *App) printAsk(r ki.Result, src []string, asJSON bool) error {
	answer, sources := ki.SplitAnswer(r.Last)
	if asJSON {
		out := askOutput{
			ID: r.ID, Agent: r.Provider, Model: r.Model, Role: r.Role, Answer: answer, Sources: sources,
			Tokens: r.Tokens, CostUSD: r.CostUSD, DurationS: r.DurationS, Status: r.Status,
			Continues: r.Continues, Resumed: r.Resumed,
		}
		if r.Status != ki.JobDone {
			out.Detail = r.Detail
		}
		if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
			return fmt.Errorf("printing the answer: %w", err)
		}
	} else if r.Status == ki.JobDone {
		fmt.Fprintln(a.Stdout, strings.TrimSpace(r.Last))
	}
	if r.Status != ki.JobDone {
		return fail("%s %s: %s (sources %s; see %s)", r.ID, r.Status, r.Detail, strings.Join(src, ","), filepath.Join(a.jobsDir(), r.ID, "output.log"))
	}
	return nil
}

// askProviders resolves who answers: --agent, else --role, else the search
// role when the machine defines one, else the default agent. Only agents ki
// can start read-only take part.
func (a *App) askProviders(ctx context.Context, da *delegateArgs, m *ki.Machine, id string) (string, []ki.Provider, error) {
	if da.Role == "" && !da.AgentGiven {
		if _, ok := m.RoleFor(ki.ClassSearch); ok {
			da.Role = ki.ClassSearch
		} else {
			// No search role: the default agent, never the execution role delegate would take.
			da.AgentGiven = true
		}
	}
	if da.AgentGiven && !ki.AskSupported(da.Agent) {
		return "", nil, fail("ki cannot start %s read-only; ask with claude, codex or cursor-agent", da.Agent)
	}
	role, try, err := a.providers(ctx, *da, m, id)
	if err != nil {
		return "", nil, err
	}
	var ok []ki.Provider
	for _, p := range try {
		if ki.AskSupported(p.Agent) {
			ok = append(ok, p)
			continue
		}
		fmt.Fprintf(a.Stderr, "ki: %s skipped for the question, ki cannot start it read-only\n", p.Name())
	}
	if len(ok) == 0 {
		return "", nil, fail("roles.%s has no agent ki can start read-only (claude, codex or cursor-agent); name one with --agent claude", role)
	}
	return role, ok, nil
}

// prepareAsk resolves the identity, brains, token and sources of a question.
// The device token is read only for the knowledge base source.
// It does not need a repo: outside one the identity comes from --identity or
// the machine.
func (a *App) prepareAsk(ctx context.Context, aa askArgs, command string) (*prepared, error) {
	if err := ki.ValidateBrains(aa.brains); err != nil {
		return nil, fail("%v", err)
	}
	h, err := a.current(ctx, nil)
	if err != nil {
		return nil, err
	}
	m, err := a.machine(h.cfg)
	if err != nil {
		return nil, err
	}
	id, err := askIdentity(h, m, aa.identity)
	if err != nil {
		return nil, err
	}
	s := ki.Session{Identity: id, Brains: askBrains(h.cfg, h.session, id.Name, aa.brains)}
	for _, b := range s.Brains {
		s.Sensitive = s.Sensitive || h.cfg.IsSensitive(b)
	}
	for _, pr := range h.cfg.Projects {
		if pr.Identity.Name == id.Name {
			s.Projects = append(s.Projects, pr.Name)
		}
	}
	url := a.mcpURL(h)
	if url == "" && slices.Contains(aa.sources, ki.SourceKB) {
		return nil, noURL(h, command)
	}
	tokenPath, token := "", ""
	if slices.Contains(aa.sources, ki.SourceKB) {
		tokenPath = a.tokenFile(s)
		if token, err = readToken(tokenPath); err != nil {
			return nil, err
		}
	} else {
		url = "" // no knowledge base source: the launch exports no KI_TOKEN, KI_BRAINS or KI_MCP_URL
	}
	gh := ""
	if slices.Contains(aa.sources, ki.SourceGitHub) {
		tokenFor := a.GitHubToken
		if tokenFor == nil {
			tokenFor = ghToken
		}
		gh = tokenFor(ctx, id.GitHubUser)
	}
	hh := *h
	hh.root = "" // a question belongs to no repo
	return &prepared{here: &hh, session: s, machine: m, token: token, tokenPath: tokenPath, gh: gh, url: url}, nil
}

// lookupIdentity is the identity projects.yaml defines under this name.
func lookupIdentity(cfg *projects.Config, name string) (*projects.Identity, error) {
	id, ok := cfg.Identities[name]
	if ok {
		return &id, nil
	}
	names := make([]string, 0, len(cfg.Identities))
	for n := range cfg.Identities {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fail("unknown identity %q (choose %s)", name, strings.Join(names, ", "))
}

// askIdentity is --identity, else the repo's identity, else the one identity
// the machine carries. The machine must carry it, as for a session.
func askIdentity(h *here, m *ki.Machine, flag string) (*projects.Identity, error) {
	names := make([]string, 0, len(h.cfg.Identities))
	for n := range h.cfg.Identities {
		names = append(names, n)
	}
	sort.Strings(names)
	var carried []string
	if m != nil {
		carried = m.Identities
	}
	var id projects.Identity
	switch {
	case flag != "":
		found, err := lookupIdentity(h.cfg, flag)
		if err != nil {
			return nil, err
		}
		id = *found
	case h.session.Identity != nil:
		id = *h.session.Identity
	case len(carried) == 1:
		found, ok := h.cfg.Identities[carried[0]]
		if !ok {
			return nil, fail("this machine carries identity %q, which projects.yaml does not define", carried[0])
		}
		id = found
	default:
		return nil, fail("which identity's brains? pass --identity (%s)", strings.Join(orAll(carried, names), ", "))
	}
	if !m.Carries(id.Name) {
		return nil, fail("identity %q is not carried by this machine; add it with `ki setup machine`", id.Name)
	}
	return &id, nil
}

func orAll(list, all []string) []string {
	if len(list) > 0 {
		return list
	}
	return all
}

// askBrains is cortex plus --brain; else, in a repo of the identity, the
// brains a session there gets; else cortex plus every brain of the
// identity's projects that is not sensitive: a sensitive brain is never in
// a default scope and has to be named.
func askBrains(cfg *projects.Config, here ki.Session, identity string, named []string) []string {
	brains := []string{scope.Cortex}
	if len(named) > 0 {
		return appendNew(brains, named...)
	}
	if here.Identity != nil && here.Identity.Name == identity {
		return appendNew(brains, here.Brains...)
	}
	for _, p := range cfg.Projects {
		if p.Identity.Name != identity {
			continue
		}
		for _, b := range p.Brains {
			if !cfg.IsSensitive(b) {
				brains = appendNew(brains, b)
			}
		}
	}
	return brains
}

func appendNew(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

// askAgentSetup is the sources one agent can use, after writing the MCP
// config it reads: Claude Code's next to the scratch folder, Cursor's in it.
// A source asked for by name that the agent cannot reach is refused; a
// default one is left out with a note.
func (a *App) askAgentSetup(p *prepared, aa askArgs, agent, scratch string) ([]string, error) {
	var src []string
	for _, s := range aa.sources {
		if why := a.askUnreachable(p, agent, s); why != "" {
			if aa.sourcesGiven {
				return nil, fail("--source %s: %s", s, why)
			}
			fmt.Fprintf(a.Stderr, "ki: %s left out: %s\n", s, why)
			continue
		}
		src = append(src, s)
	}
	if len(src) == 0 {
		return nil, fail("no source left to answer from")
	}
	set := a.askSetup(p, src)
	switch filepath.Base(agent) {
	case "claude":
		if err := ki.WritePrivate(p.mcpPath, ki.AskMCPConfig(set, p.token, false)); err != nil {
			return nil, fmt.Errorf("writing MCP config: %w", err)
		}
	case "cursor-agent", "agent":
		files := map[string]any{ki.CursorMCPFile: ki.AskMCPConfig(set, p.token, true), ki.CursorCLIFile: ki.AskCursorPermissions(src)}
		for name, v := range files {
			if err := ki.WritePrivate(filepath.Join(scratch, filepath.FromSlash(name)), v); err != nil {
				return nil, fmt.Errorf("writing Cursor's %s: %w", name, err)
			}
		}
	}
	return src, nil
}

// askUnreachable says why the agent cannot read a source here, "" when it can.
func (a *App) askUnreachable(p *prepared, agent, source string) string {
	switch source {
	case ki.SourceVault:
		if a.Getenv("KI_VAULT") == "" || a.binary() == "" {
			return "KI_VAULT is not set"
		}
	case ki.SourceGitHub:
		if !ki.AskGitHub(agent) {
			return filepath.Base(agent) + " runs without network when read-only, so it cannot run gh"
		}
		if p.gh == "" && p.session.Identity != nil {
			return "gh is not logged in as " + p.session.Identity.GitHubUser
		}
	}
	return ""
}

// askSetup is the read-only setup for the given sources.
func (a *App) askSetup(p *prepared, src []string) ki.AskSetup {
	set := ki.AskSetup{Sources: src, MCPURL: p.url, Brains: p.session.Brains}
	if dir := a.Getenv("KI_VAULT"); dir != "" && a.binary() != "" {
		set.Vault = ki.ReadOnlyVaultServer(a.binary(), dir)
	}
	cfg, _ := readOptional(filepath.Join(a.codexHome(), "config.toml")) // no config: nothing to point at the read-only server
	set.CodexVault = strings.Contains(cfg, ki.CodexVaultTable)
	return set
}
