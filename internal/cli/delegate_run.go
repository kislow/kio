package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// attempt runs the brief once on one provider in dir and records the job.
// A worker applies a described change: execution work unless its role names
// another class, so its model is the provider's, else --model, else the
// policy's for that class, never the session's KI_MODEL.
func (a *App) attempt(ctx context.Context, p *prepared, da delegateArgs, prov ki.Provider, role, id, dir, branch string) (ki.Result, error) {
	agent := filepath.Base(prov.Agent)
	allow := p.machine.DelegateAllow()
	var (
		wm    workerMode
		brief string
		err   error
	)
	if da.setup != nil {
		wm, brief, err = da.setup(prov.Agent)
	} else {
		wm, err = workerModeFor(prov.Agent, da, allow)
	}
	if err != nil {
		return ki.Result{}, err
	}
	ra := da.RunArgs
	ra.Agent = prov.Agent
	class := workerClass(orDefault(role, da.class))
	model := workerModel(prov, da.Model, class, p.machine)
	in := p.launchInput(ra, model, wm.args)
	in.Settings = ki.SessionSettings(ki.NewSubagentGuard(a.binary(), p.machine, true))
	in.Effort = p.machine.EffortFor(class)
	if da.ask != nil {
		in.Resume = da.ask.session
	}

	r := ki.Result{ID: id, Title: ki.ResolveTitle(da.Title, da.issueTitle, da.Task), Provider: agent, Role: role, Model: model, ModelRequested: model, Repo: p.here.root, Branch: branch, Started: time.Now()}
	if da.issue.Number != 0 {
		r.Issue = da.issue.String()
	}
	if da.resumeOf != nil {
		r.ResumedFrom, r.Issue = da.resumeOf.ID, da.resumeOf.Issue
	}
	if da.queued != nil {
		q := da.queued.Queued
		r.Queued = &q
	}
	r.Kind, r.PR = da.kind, da.pr
	if da.ask != nil {
		state := da.ask.state
		r.Ask, r.Continues, r.Resumed = &state, da.ask.continues, da.ask.resumed
	}
	if dir != p.here.root && da.setup == nil {
		r.Worktree = dir
	}
	jobDir := filepath.Join(a.jobsDir(), r.ID)
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		return r, fmt.Errorf("creating %s: %w", jobDir, err)
	}
	// A sandboxed worker commits and ki publishes; reviews, asks and skill runs never publish.
	sandboxed := da.setup == nil && da.brief == nil && !da.readOnly && ki.Sandboxed(prov.Agent, wm.ran())
	switch {
	case da.setup != nil:
	case da.brief != nil:
		brief = da.brief(da.Task)
	case sandboxed:
		brief = ki.WrapSandboxedTask(da.Task)
	case wm.edits():
		brief = ki.WrapEditsTask(da.Task, allow, da.Draft)
	default:
		brief = ki.WrapTask(da.Task, da.Draft)
	}
	if err := os.WriteFile(filepath.Join(jobDir, "brief.md"), []byte(brief+"\n"), 0o600); err != nil {
		return r, fmt.Errorf("writing the brief: %w", err)
	}
	if da.setup == nil && da.brief == nil {
		// The task without the contract, so a resume can hand it to another worker.
		if err := os.WriteFile(filepath.Join(jobDir, taskFile), []byte(orDefault(da.baseTask, da.Task)+"\n"), 0o600); err != nil {
			return r, fmt.Errorf("writing the task: %w", err)
		}
	}
	in.Headless = &ki.Headless{Task: brief, LastMessage: filepath.Join(jobDir, "last.txt"), Instructions: p.instructionsText}
	launch := ki.BuildLaunch(in)
	bin, err := exec.LookPath(launch.Argv[0])
	if err != nil {
		return r, fail("agent %q not found on PATH", launch.Argv[0])
	}
	if wm.edits() && agent == "agy" {
		if err := a.ensureAgyAllow(allow); err != nil {
			return r, err
		}
	}
	if agent == "agent" || agent == "cursor-agent" {
		if wm.ran() == ki.ModeAuto {
			if err := a.ensureCursorSandbox(ctx, dir); err != nil {
				return r, err
			}
		}
	}
	issue := ""
	if r.Issue != "" {
		issue = " | issue=" + r.Issue
	}
	fmt.Fprintf(a.Stderr, "ki: %s | %s | %s | brains=%s | mode=%s | %s%s\n", r.ID, p.who(), prov.Name(), strings.Join(p.session.Brains, ","), wm.label(), dir, issue)

	stall := da.Stall
	if stall == 0 {
		stall = p.machine.StallLimit()
	}
	streams := ki.Streams(agent)
	beat := newHeartbeat(filepath.Join(jobDir, "result.json"), r, streams, stall)
	run := workerRun{
		bin: bin, launch: launch, dir: dir, logPath: filepath.Join(jobDir, "output.log"), timeout: da.Timeout, stall: stall,
		started: beat.begin,
	}
	if streams {
		run.events = newEventStream(agent, r.Started, a.progressOut(da.Quiet), a.EventClock)
		run.events.onEvent = beat.beat
	}
	stdout, exit, runErr := a.runWorker(ctx, run)
	last, _ := readOptional(in.Headless.LastMessage)
	out := ki.ParseOutput(agent, wm.ran(), stdout, last)
	if out.QuotaFor == 0 && out.Err == "" && out.Last == "" && exit != 0 {
		// The CLI died without a result: a limit message goes to stderr, which only the log holds.
		out.QuotaFor = ki.QuotaFromText(logTail(run.logPath), time.Now())
	}
	r.DurationS = time.Since(r.Started).Seconds()
	r.Session, r.Tokens, r.CostUSD, r.Exit, r.Last = out.Session, out.Tokens, out.CostUSD, exit, out.Last
	r.Model = ki.ResolvedModel(agent, model, out.Model)
	if out.QuotaFor > 0 {
		until := time.Now().Add(out.QuotaFor)
		r.QuotaUntil = &until
	}
	parse := orStatus(da.parse)
	if sandboxed {
		parse = ki.ParseSandboxedStatus
	}
	r.Status, r.Detail = classifyWith(out, exit, runErr, parse)
	if sandboxed {
		a.publish(ctx, p, &r, publishJob{dir: dir, branch: branch, last: out.Last, issue: r.Issue, draft: da.Draft, token: p.gh, secrets: []string{p.token, p.gh}})
		if ki.AsksBrowserCheck(da.Task) {
			r.Note = ki.BrowserSkipped
		}
	}
	if run.events != nil {
		at := run.events.lastEvent()
		r.LastEventAt = &at
		r.StallS = stall.Seconds()
	}
	r.TimedOut = errors.Is(runErr, errTimeout)
	if quotaStop(out, r.Status, runErr, run.events) {
		r.Status = ki.JobQuota
		r.Detail = quotaDetail(r.Detail, out, r.QuotaUntil)
		if da.setup == nil {
			pr := readProgress(ctx, p.here.root, dir, false)
			r.Commits, r.DiffStat = pr.Commits, pr.DiffStat
		}
	}
	if run.events != nil && (r.Status == ki.JobStalled || r.Status == ki.JobQuota || r.TimedOut) {
		r.Tail = run.events.lastLines()
	}
	if r.Status == ki.JobStalled {
		a.notifyStalled(ctx, p.machine, r)
	}
	scrubToken(&r, run.logPath, p.token)

	if err := ki.WritePrivate(filepath.Join(jobDir, "result.json"), r); err != nil {
		return r, fmt.Errorf("recording job %s: %w", r.ID, err)
	}
	if err := ki.AppendLedger(filepath.Join(a.jobsDir(), ki.LedgerFile), r); err != nil {
		return r, fmt.Errorf("recording job %s: %w", r.ID, err)
	}
	return r, nil
}

// tokenRedacted replaces the knowledge base token wherever a worker printed it.
const tokenRedacted = "[redacted]"

// scrubToken keeps the knowledge base token out of a job's record and log:
// a worker sees it in its environment and could print it.
func scrubToken(r *ki.Result, logPath, token string) {
	if token == "" {
		return
	}
	r.Last = strings.ReplaceAll(r.Last, token, tokenRedacted)
	r.Detail = strings.ReplaceAll(r.Detail, token, tokenRedacted)
	for i := range r.Tail {
		r.Tail[i] = strings.ReplaceAll(r.Tail[i], token, tokenRedacted)
	}
	for i := range r.Commits {
		r.Commits[i] = strings.ReplaceAll(r.Commits[i], token, tokenRedacted)
	}
	r.DiffStat = strings.ReplaceAll(r.DiffStat, token, tokenRedacted)
	data, err := os.ReadFile(logPath) // #nosec G304 -- under the jobs folder
	if err != nil || !bytes.Contains(data, []byte(token)) {
		return
	}
	// Best effort: a log that cannot be rewritten keeps its mode 0600.
	_ = os.WriteFile(logPath, bytes.ReplaceAll(data, []byte(token), []byte(tokenRedacted)), 0o600) // #nosec G703 -- under the jobs folder
}

// logTail is the end of a job log, where a dying CLI prints why.
func logTail(path string) string {
	const n = 4096
	f, err := os.Open(path) // #nosec G304 -- under the jobs folder
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	buf := make([]byte, min(st.Size(), n))
	if _, err := f.ReadAt(buf, st.Size()-int64(len(buf))); err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	return string(buf)
}

// ensureCursorSandbox lets the sandboxed worker reach api.github.com. The
// policy file is hidden from git through the repo's info/exclude, so it never
// lands in a commit of the target repo.
func (a *App) ensureCursorSandbox(ctx context.Context, dir string) error {
	wrote, err := ki.EnsureCursorSandbox(dir)
	if err != nil {
		return fail("%v", err)
	}
	if !wrote {
		return nil
	}
	exclude := git(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if exclude == "" {
		return nil
	}
	if err := ki.AppendLineOnce(exclude, ki.CursorSandboxFile); err != nil {
		return fail("%v", err)
	}
	return nil
}

// Parallel delegations in one repo can still meet on git's shared lock files.
const (
	worktreeAttempts   = 5
	worktreeRetryDelay = 200 * time.Millisecond
	fetchTimeout       = 8 * time.Second
)

// fetchDefault updates origin/<branch>. Parallel jobs fetch the same ref, so a
// ref lock held by a sibling is retried like the worktree lock is.
func fetchDefault(ctx context.Context, root, branch string) error {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	var out []byte
	for attempt := 0; attempt < worktreeAttempts; attempt++ {
		cmd := exec.CommandContext(ctx, "git", "fetch", "origin", branch, "--quiet") // #nosec G204 -- fixed binary, arguments from ki itself
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		var err error
		if out, err = cmd.CombinedOutput(); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return errors.New("timed out")
		}
		if !bytes.Contains(out, []byte("cannot lock")) && !bytes.Contains(out, []byte("could not lock")) {
			break
		}
		time.Sleep(worktreeRetryDelay)
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	return errors.New(msg)
}

// workerClass is the class a worker's policy defaults come from: its role,
// else execution.
func workerClass(role string) string { return orDefault(role, ki.ClassExecution) }

// workerModel is the model a provider runs: its own, else --model, else the
// machine's model for that class and agent.
func workerModel(prov ki.Provider, requested, class string, m *ki.Machine) string {
	return orDefault(orDefault(prov.Model, requested), m.ModelFor(class, filepath.Base(prov.Agent)))
}

// errTimeout marks a worker ki killed for running past --timeout; errCancelled
// one killed because ki itself was interrupted (Ctrl-C).
var (
	errTimeout   = errors.New("timed out")
	errCancelled = errors.New("cancelled")
	// errStalled marks a worker ki killed for printing no event for the stall limit.
	errStalled = errors.New("stalled")
	// errLingered marks a worker still running a grace after its final result.
	errLingered = errors.New("kept running after its final result")
)

// classify turns what came back into the job's status. A worker ki ended for
// its silence is STALLED; a killed one and an agent-level error are FAILED; no final message with a non-zero exit
// means the agent never ran the task; otherwise the worker's own line decides.
func classify(out ki.Outcome, exit int, runErr error) (status, detail string) {
	return classifyWith(out, exit, runErr, ki.ParseStatus)
}

func orStatus(parse func(string) (string, string)) func(string) (string, string) {
	if parse == nil {
		return ki.ParseStatus
	}
	return parse
}

// classifyWith is classify reading the worker's own line with parse.
func classifyWith(out ki.Outcome, exit int, runErr error, parse func(string) (string, string)) (status, detail string) {
	switch {
	case errors.Is(runErr, errStalled):
		return ki.JobStalled, runErr.Error()
	case errors.Is(runErr, errTimeout), errors.Is(runErr, errCancelled):
		return ki.JobFailed, runErr.Error()
	case out.Err != "":
		return ki.JobFailed, out.Err
	case out.Last == "" && exit != 0:
		return ki.JobLaunchFailed, runErr.Error()
	case exit != 0:
		return ki.JobFailed, fmt.Sprintf("exit %d: %s", exit, runErr)
	}
	return parse(out.Last)
}

// freeBranch is name, suffixed with the job id when a local or origin branch
// already has it.
func freeBranch(ctx context.Context, root, name, id string) string {
	return ki.UniqueBranch(name, id, func(b string) bool {
		return git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b) != "" ||
			git(ctx, root, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+b) != ""
	})
}

// addWorktree gives the job its own checkout next to the repo, named after
// the job, on branch, from origin's default branch (else HEAD). The default
// branch is fetched first so a job never starts behind merged work; an
// unreachable remote falls back to the last fetch with a note on stderr.
func (a *App) addWorktree(ctx context.Context, root, id, branch string) (dir string, err error) {
	unlock, err := lockWorktrees(ctx, root)
	if err != nil {
		return "", err
	}
	defer unlock()
	base := git(ctx, root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if base == "" {
		base = "HEAD"
	} else if err := fetchDefault(ctx, root, strings.TrimPrefix(base, "origin/")); err != nil {
		fmt.Fprintf(a.Stderr, "ki: could not fetch %s (%s); starting from the last fetch\n", base, err)
	}
	dir = filepath.Join(filepath.Dir(root), filepath.Base(root)+"-worktrees", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	// --no-track: a tracking branch writes .git/config, and parallel delegations
	// in one repo then race for its lock; the worker pushes with -u itself.
	// The worktree list itself is a shared file too, so a lock there is retried.
	var out []byte
	for attempt := 0; attempt < worktreeAttempts; attempt++ {
		cmd := exec.CommandContext(ctx, "git", "worktree", "add", "--quiet", "--no-track", "-b", branch, dir, base) // #nosec G204 -- fixed binary, arguments from ki itself
		cmd.Dir = root
		var err error
		if out, err = cmd.CombinedOutput(); err == nil {
			return dir, nil
		}
		if !bytes.Contains(out, []byte("could not lock")) {
			break
		}
		time.Sleep(worktreeRetryDelay)
	}
	return "", fail("git worktree add %s from %s: %s", dir, base, strings.TrimSpace(string(out)))
}

// workerModeFor is the permission setup of a worker: the read-only flags for
// a review, else the delegate mode.
func workerModeFor(agent string, da delegateArgs, allow []string) (workerMode, error) {
	if !da.readOnly {
		return delegateMode(agent, da.Mode, da.AgentArgs, allow)
	}
	args, ok := ki.ReadOnlyArgs(agent)
	if !ok {
		return workerMode{}, fail("ki knows no read-only flags for %s; a review runs on claude or codex", filepath.Base(agent))
	}
	if len(da.AgentArgs) > 0 {
		return workerMode{}, fail("a review takes no agent arguments: they could lift the read-only flags")
	}
	return workerMode{args: args, readOnly: true}, nil
}
