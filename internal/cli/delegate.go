package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

const delegateUsage = `usage: ki delegate (--task "<brief>" | --issue N | --issue owner/repo#N) [--project NAME|PATH]
                   [--role execution|writing|search|judgement | --agent claude|codex|cursor-agent|agy] [--model M]
                   [--mode auto|edits|skip] [--brain B]... [--title "<text>"] [--here] [--draft] [--timeout 30m] [--stall 15m] [--steal]
                   [--when-available] [--force] [--json] [--quiet] [-- agent args]
       ki delegate resume <job> [--role R | --agent A] [--model M] [--when-available] [--force] [--json] [--quiet] ...
       ki delegate cancel <job>
       ki delegate ls [--json]
       ki delegate health [--json]
       ki delegate watch <job>
       ki delegate log [--date YYYY-MM-DD] [--vault]

Flags ki does not know are refused; arguments for the agent go after --.

The job runs in the repo you are in, or in --project's checkout: a project name from projects.yaml
found through ~/.config/ki/mecca.yaml (roots, checkouts), or a path.

--issue claims the issue with the "agent: working" label before the worker starts, so a second
machine refuses; the job removes it when it ends. Nothing else is written to the issue: who holds
a claim is in the local ledger (ki delegate ls). Without --task the issue's title and body are the
brief. --steal takes over a labelled issue. A crash leaves the label.

The worktree's branch is <type>/<issue>-<slug> from the issue title's Conventional Commits prefix
(chore without one), or chore/<job> without --issue. The worker opens its PR ready for review;
--draft makes it a draft.

Claude Code, Codex and Cursor stream, so ki prints one progress line per tool call or message to
stderr (ki: 02:14 claude  edit docs/x.md); --quiet turns them off. A worker silent for five
minutes gets a warning line, and one silent for --stall (default 15 minutes, delegate.stall in
machine.yaml) is ended as STALLED with its worktree and commits kept. ki delegate ls lists running
jobs first with the age of their last event; watch <job> follows one; health shows each provider's
recent outcomes and whether the next pick skips or demotes it.

A role picks the agent from roles.<role> in machine.yaml (ordered or round-robin over its
providers, skipping one that is missing, failed to launch in the last 10 minutes, stalled or timed
out in the last 30, or is over its daily cap, and moving one that hung twice in a day to the end). Without --role or --agent, the execution role is used when the machine defines it.
A worker without a model of its own takes models.<role> (models.execution without a role).

--when-available (delegate.queue: true in machine.yaml makes it the default) queues a job whose
role has no provider available instead of refusing it: ki delegate ls shows it QUEUED with the
reason and the expected start, ki queue run starts the oldest ones once a provider is back (at
most delegate.queue_parallel at once, default 2), and ki queue install runs that every minute.
ki delegate cancel <job> drops a queued job.

Cursor runs sandboxed (it cannot push or open a PR), so its brief says "commit only; ki publishes": when
the worker ends DONE with commits ahead of origin, ki pushes the branch (never forced) with the job's token
and opens the PR, or keeps the branch's open PR, and the job ends PR: <url>. Uncommitted changes end it
BLOCKED. A brief that asks for a browser check adds "checks skipped: browser (sandbox)" to the result.

A worker that stops on a usage limit after it started ends QUOTA, with its worktree, commits, diff
stat and last progress lines kept. ki delegate resume <job> continues it in the same worktree on
another provider (the role's pick, which skips the one over quota, or --agent) with the original
brief and a handoff note; --when-available queues the resume.

Every job has a short title for ki delegate ls: --title, else the issue's title (--issue without
--task), else the first sentence of the brief.

Every job records model_requested (what ki passed) and model (what the agent reported it ran,
else the requested one, else "auto" for Cursor); ki delegate ls shows the latter.`

// delegateTimeout bounds a job: a worker that is still going after this is killed and recorded as FAILED.
const delegateTimeout = 30 * time.Minute

type delegateArgs struct {
	ki.RunArgs
	Task    string
	Title   string
	Role    string
	Project string
	Issue   string
	Here    bool
	Draft   bool
	JSON    bool
	Quiet   bool
	Steal   bool
	// WhenAvailable queues the job when its role has no provider available.
	WhenAvailable bool
	Timeout       time.Duration
	// Stall is --stall; zero means the machine's delegate.stall, else ki.DefaultStall.
	Stall time.Duration
	// AgentGiven is whether --agent was passed, as opposed to defaulted.
	AgentGiven bool
	// issue is --issue resolved to owner/repo#N, set once the repo is known.
	issue ki.IssueRef
	// issueTitle is the issue's title when the issue is the brief.
	issueTitle string
	// A review job (`ki approve --review`) differs from a task in these: its own
	// brief and way of reading the answer, read-only flags instead of a mode, and
	// the kind and pull request its ledger record carries.
	brief    func(task string) string
	parse    func(last string) (status, detail string)
	readOnly bool
	kind, pr string
	// setup, set for `ki ask` and `ki skill run`, gives one agent its read-only flags and brief
	// in place of the mode and brief a task or review gets.
	setup func(agent string) (workerMode, string, error)
	// ask is what an ask job records and the session it reopens, nil for any other job.
	ask *askRun
	// class is the policy class without a role, "" for execution.
	class string
	// queued is the queue entry this run starts, nil for a job started at once.
	queued *ki.QueueEntry
	// resumeOf is the job a resume continues, and baseTask its task without
	// the handoff note, which is what a later resume starts from again.
	resumeOf *ki.Result
	baseTask string
}

// delegateValueFlags are the flags that take a value, for the error when it is missing.
var delegateValueFlags = map[string]bool{
	"--task": true, "--title": true, "--timeout": true, "--stall": true, "--role": true, "--project": true,
	"--agent": true, "--brain": true, "--model": true, "--mode": true,
}

// parseDelegateArgs takes delegate's own flags and hands the rest to ParseRunArgs.
func parseDelegateArgs(args []string, defaultAgent string) (delegateArgs, error) {
	da := delegateArgs{Timeout: delegateTimeout}
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--task" && i+1 < len(args):
			da.Task = args[i+1]
			i++
		case a == "--title" && i+1 < len(args):
			da.Title = args[i+1]
			i++
		case a == "--issue" && i+1 < len(args):
			da.Issue = args[i+1]
			i++
		case a == "--issue":
			return da, fail("--issue needs a number or owner/repo#number")
		case a == "--steal":
			da.Steal = true
		case a == "--when-available":
			da.WhenAvailable = true
		case a == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return da, fail("--timeout %q: use a duration like 30m", args[i+1])
			}
			da.Timeout = d
			i++
		case a == "--stall" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return da, fail("--stall %q: use a duration like 15m", args[i+1])
			}
			da.Stall = d
			i++
		case a == "--role" && i+1 < len(args):
			da.Role = args[i+1]
			i++
		case a == "--project" && i+1 < len(args):
			da.Project = args[i+1]
			i++
		case a == "--agent" && i+1 < len(args):
			da.AgentGiven = true
			rest = append(rest, a, args[i+1])
			i++
		case (a == "--brain" || a == "--model" || a == "--mode") && i+1 < len(args):
			rest = append(rest, a, args[i+1])
			i++
		case a == "--force":
			rest = append(rest, a)
		case a == "--here":
			da.Here = true
		case a == "--draft":
			da.Draft = true
		case a == "--json":
			da.JSON = true
		case a == "--quiet":
			da.Quiet = true
		case a == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		case strings.HasPrefix(a, "-") && delegateValueFlags[a]:
			return da, fail("%s needs a value", a)
		case strings.HasPrefix(a, "-"):
			return da, fail("unknown flag %s: agent arguments go after --\n\n%s", a, delegateUsage)
		default:
			return da, fail("unexpected argument %q: agent arguments go after --\n\n%s", a, delegateUsage)
		}
	}
	da.RunArgs = ki.ParseRunArgs(rest, defaultAgent)
	if da.Steal && da.Issue == "" {
		return da, fail("--steal applies to --issue")
	}
	if strings.TrimSpace(da.Task) == "" && da.Issue == "" {
		return da, fail("%s", delegateUsage)
	}
	return da, nil
}

// workerMode is the permission mode a worker runs in and the agent flags for it.
type workerMode struct {
	args []string
	mode ki.Mode
	// overridden means the agent args set permissions themselves, so ki added none.
	overridden bool
	// readOnly is a review worker: flags that let it change nothing, no mode.
	readOnly bool
	// allowList is a skill run held to its tool list instead of a mode.
	allowList bool
}

// edits reports whether ki's edits mode applies: requested and not overridden.
func (w workerMode) edits() bool { return w.mode == ki.ModeEdits && !w.overridden }

// ran is ki's mode as the worker ran it, "" when the agent args set permissions.
func (w workerMode) ran() ki.Mode {
	if w.overridden || w.readOnly || w.allowList {
		return ""
	}
	return w.mode
}

// label is the mode as the job banner shows it.
func (w workerMode) label() string {
	if w.allowList {
		return "allow-list"
	}
	if w.readOnly {
		return "read-only"
	}
	if w.overridden {
		return "set by agent args"
	}
	return string(w.mode)
}

// delegateMode is the permission mode a headless worker runs in: --mode, else
// auto, else edits when the agent has it and the machine has an allow-list,
// else skip. ask is refused: nobody is there to answer a prompt. allow is
// delegate.allow from the machine file.
func delegateMode(agent, requested string, agentArgs, allow []string) (workerMode, error) {
	m, err := ki.ParseMode(requested)
	if err != nil {
		return workerMode{}, fail("%v", err)
	}
	if requested == "" {
		switch {
		case ki.SupportsMode(agent, ki.ModeAuto):
			m = ki.ModeAuto
		case len(allow) > 0 && ki.SupportsMode(agent, ki.ModeEdits):
			m = ki.ModeEdits
		default:
			m = ki.ModeSkip
		}
	}
	if m == ki.ModeAsk {
		return workerMode{}, fail("a delegated job runs headless and cannot answer prompts; use --mode auto, edits or skip")
	}
	args, overridden, err := ki.ModeArgsAllow(agent, m, agentArgs, allow)
	if err != nil {
		return workerMode{}, fail("%v", err)
	}
	if m == ki.ModeEdits && !overridden && len(allow) == 0 {
		return workerMode{}, fail("--mode edits needs the commands a worker may run: list them under delegate.allow in %s, for example `delegate: {allow: [git, gh, go]}`", ki.MachineFile)
	}
	return workerMode{args: args, mode: m, overridden: overridden}, nil
}

// agySettingsPath is agy's settings file: KI_AGY_SETTINGS, else under the home dir.
func (a *App) agySettingsPath() string {
	return orDefault(a.Getenv("KI_AGY_SETTINGS"), ki.AgySettingsPath(a.home()))
}

// ensureAgyAllow adds the allow-list to agy's settings, which is where agy
// reads it, and says so on stderr when it added anything.
func (a *App) ensureAgyAllow(allow []string) error {
	added, err := ki.EnsureAgyAllow(a.agySettingsPath(), allow)
	if err != nil {
		return fail("%v", err)
	}
	if n := len(added); n > 0 {
		rules := "rules"
		if n == 1 {
			rules = "rule"
		}
		fmt.Fprintf(a.Stderr, "ki: added %d command %s to agy settings: %s\n", n, rules, strings.Join(added, ", "))
	}
	return nil
}

func (a *App) jobsDir() string { return filepath.Join(a.confDir(), "jobs") }

func (a *App) delegate(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "log" {
		return a.delegateLog(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "ls" {
		return a.delegateList(args[1:])
	}
	if len(args) > 0 && args[0] == "watch" {
		return a.delegateWatch(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "health" {
		return a.delegateHealth(args[1:])
	}
	if len(args) > 0 && args[0] == "cancel" {
		return a.delegateCancel(ctx, args[1:])
	}
	if len(args) > 0 && args[0] == "resume" {
		return a.delegateResume(ctx, args[1:], nil)
	}
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(a.Stdout, delegateUsage)
		return nil
	}
	return a.delegateTask(ctx, args, nil)
}

// delegateTask runs `ki delegate --task|--issue ...`; queued is the queue
// entry when `ki queue run` starts it, so it runs under the queued job's id.
func (a *App) delegateTask(ctx context.Context, args []string, queued *ki.QueueEntry) error {
	da, err := parseDelegateArgs(args, orDefault(a.Getenv("KI_AGENT"), "claude"))
	if err != nil {
		return err
	}
	da.queued = queued
	if da.Project != "" {
		if err := a.useProject(ctx, da.Project); err != nil {
			return err
		}
	}
	p, err := a.prepare(ctx, da.RunArgs)
	if err != nil {
		return err
	}
	if p.here.root == "" {
		return fail("not inside a git repo: run ki delegate in the checkout, or name it with --project <name|path>")
	}
	// The first attempt's job names the worktree; a fall-through attempt gets its own job in the same worktree.
	id := ki.NewJobID(a.now())
	if queued != nil {
		id = queued.ID
	}
	role, try, err := a.providers(ctx, da, p.machine, id)
	if err != nil {
		return a.queueOrRefuse(ctx, err, da, p, id, args)
	}
	branch := ""
	if da.Here {
		branch = git(ctx, p.here.root, "rev-parse", "--abbrev-ref", "HEAD")
	} else if da.Issue == "" {
		branch = freeBranch(ctx, p.here.root, ki.TaskBranch(id), id)
	}
	// Claim before the worktree exists, so a refusal leaves nothing behind.
	// An issue's branch is named from its title, which only the claim reads;
	// a crash skips finish and the label stays.
	var held *issueHold
	if da.Issue != "" {
		var brief string
		held, brief, err = a.claimIssue(ctx, p, da, id, branch)
		if err != nil {
			return err
		}
		da.issue, branch = held.ref, held.branch
		if strings.TrimSpace(da.Task) == "" {
			da.Task, da.issueTitle = brief, held.title
		}
	}
	dir := p.here.root
	if !da.Here {
		if dir, err = a.addWorktree(ctx, p.here.root, id, branch); err != nil {
			if held != nil {
				_ = a.finishIssue(ctx, held)
			}
			return err
		}
	}
	r, runErr := a.attemptAll(ctx, p, da, try, role, id, dir, branch)
	var finishErr error
	if held != nil {
		finishErr = a.finishIssue(ctx, held)
	}
	if runErr != nil {
		return runErr
	}
	return a.printResult(r, da.JSON, finishErr)
}

// printResult prints a finished job, one line or --json, and turns a launch
// failure into the command's error.
func (a *App) printResult(r ki.Result, asJSON bool, finishErr error) error {
	if asJSON {
		if err := json.NewEncoder(a.Stdout).Encode(r); err != nil {
			return fmt.Errorf("printing the result: %w", err)
		}
	} else {
		fmt.Fprintf(a.Stdout, "%s %s %s: %s\n", r.ID, r.Provider, r.Status, r.Detail)
		if r.Note != "" {
			fmt.Fprintln(a.Stdout, r.Note)
		}
	}
	if finishErr != nil {
		return finishErr
	}
	if r.Status == ki.JobLaunchFailed {
		return fail("%s did not start a run: %s (see %s)", r.Provider, r.Detail, filepath.Join(a.jobsDir(), r.ID, "output.log"))
	}
	return nil
}

// useProject points this run at a project's checkout, resolved the way
// ki mecca new resolves it, so a job can be started from anywhere.
func (a *App) useProject(ctx context.Context, target string) error {
	h, err := a.harness()
	if err != nil {
		return err
	}
	pf, err := a.configPath(h, ki.ProjectsFile)
	if err != nil {
		return err
	}
	cfg, err := projects.Load(pf)
	if err != nil {
		return fmt.Errorf("loading %s: %w", pf, err)
	}
	dir, err := a.meccaCheckout(ctx, target, cfg)
	if err != nil {
		return err
	}
	a.Dir = dir
	return nil
}

// providers resolves who may run the job: the --agent given, else the
// --role's candidates from the machine's policy (execution by default when
// the machine defines it), else the default agent. The agents must be ones
// ki can run headless. A role's pick reserves its turn for job id, so
// delegations started together take turns instead of all choosing alike.
func (a *App) providers(ctx context.Context, da delegateArgs, m *ki.Machine, id string) (role string, try []ki.Provider, err error) {
	role = da.Role
	if role == "" && !da.AgentGiven {
		if _, ok := m.RoleFor(ki.ClassExecution); ok {
			role = ki.ClassExecution
		}
	}
	if role == "" {
		if !ki.HeadlessSupported(da.Agent) {
			return "", nil, fail("ki does not know how to run %s headless; use claude, codex, cursor-agent or agy", da.Agent)
		}
		return "", []ki.Provider{{Agent: da.Agent, Model: da.Model}}, nil
	}
	switch {
	case da.AgentGiven:
		return "", nil, fail("--role and --agent are exclusive: the role's policy picks the agent")
	case da.Model != "":
		return "", nil, fail("--model with --role: put the model on the provider in %s instead", a.short(a.machinePath()))
	}
	if role == ki.RolePlanner {
		return "", nil, fail("role %q picks the agent a bare `ki` starts, not a headless worker; use execution, search, writing or judgement", role)
	}
	r, ok := m.RoleFor(role)
	if !ok {
		return "", nil, fail("role %q is not in %s (defined: %s)", role, a.short(a.machinePath()), orDefault(strings.Join(m.RoleNames(), ", "), "none"))
	}
	for _, p := range r.Providers {
		if !ki.HeadlessSupported(p.Agent) {
			return "", nil, fail("roles.%s lists %s, which ki cannot run headless", role, p.Agent)
		}
	}
	try, skipped, err := ki.PickAndReserve(ctx, a.jobsDir(), ki.PickInput{Role: role, R: r, Now: a.now(), Installed: installed}, id, ki.PickLockWait)
	if err != nil {
		return "", nil, fmt.Errorf("picking a provider for role %s: %w", role, err)
	}
	printSkipped(a.Stderr, role, skipped)
	if len(try) == 0 {
		return role, nil, noProviderError{UserError: UserError{fmt.Sprintf("role %s: no provider available", role)}, role: role, skipped: skipped}
	}
	return role, try, nil
}

// installed reports whether an agent is on PATH.
func installed(agent string) bool {
	_, err := exec.LookPath(agent)
	return err == nil
}

// noProviderError is the refusal when every provider of a role is skipped; a job
// that may wait is queued with the reasons instead.
type noProviderError struct {
	UserError
	role    string
	skipped []ki.Skipped
}

func (e noProviderError) Unwrap() error { return e.UserError }

// attemptAll runs the job on the first provider and, only when it never
// started a run (LAUNCH_FAILED), on the next, each attempt its own job in the
// same checkout. A worker that ran is never re-run elsewhere. id names the
// first attempt.
func (a *App) attemptAll(ctx context.Context, p *prepared, da delegateArgs, try []ki.Provider, role, id, dir, branch string) (ki.Result, error) {
	var r ki.Result
	for i, prov := range try {
		var err error
		r, err = a.attempt(ctx, p, da, prov, role, id, dir, branch)
		id = ki.NewJobID(a.now())
		if err != nil {
			return r, err
		}
		if ctx.Err() != nil {
			// Interrupted: the job is recorded as FAILED, nothing else is tried.
			return r, fail("interrupted; %s recorded as FAILED, its worktree kept at %s", r.ID, dir)
		}
		if r.Status != ki.JobLaunchFailed {
			return r, nil
		}
		if i+1 < len(try) {
			fmt.Fprintf(a.Stderr, "ki: %s did not start a run (%s); trying %s\n", prov.Name(), r.Detail, try[i+1].Name())
		}
	}
	return r, nil
}
