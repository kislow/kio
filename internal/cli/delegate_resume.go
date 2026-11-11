package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// taskFile holds a job's task as given, without the contract ki appends.
const taskFile = "task.md"

// resumable are the outcomes that leave unfinished work in a worktree.
var resumable = map[string]bool{ki.JobQuota: true, ki.JobStalled: true, ki.JobFailed: true}

// resumeRefused are the delegate flags a resume cannot take: the brief,
// the checkout and the issue claim come from the job it continues.
var resumeRefused = map[string]bool{"--task": true, "--issue": true, "--here": true, "--project": true, "--steal": true, "--title": true}

// delegateResume continues a job that stopped (QUOTA, STALLED or FAILED) in
// its own worktree and branch, on the provider the role picks now or on
// --agent, with the original task and a handoff note on what is done.
// args are the job then delegate flags; queued is set when the queue starts it.
func (a *App) delegateResume(ctx context.Context, args []string, queued *ki.QueueEntry) error {
	if err := a.refuseInWorker(); err != nil {
		return err
	}
	if len(args) == 0 || !ki.ValidJobID(args[0]) {
		return fail("usage: ki delegate resume <job> [--role R | --agent A] [--when-available] [--json] (a QUOTA, STALLED or FAILED job from ki delegate ls)")
	}
	prevID, flags := args[0], args[1:]
	for _, f := range flags {
		if resumeRefused[f] {
			return fail("%s does not apply to resume: the job continues %s's task in its worktree", f, prevID)
		}
	}
	prev, err := ki.ReadResult(a.jobsDir(), prevID)
	if err != nil {
		return fail("job %s has no result to resume: %v", prevID, err)
	}
	if !resumable[prev.Status] {
		return fail("job %s ended %s; resume continues a QUOTA, STALLED or FAILED job", prevID, prev.Status)
	}
	if prev.Worktree == "" {
		return fail("job %s ran without a worktree of its own, so there is nothing to continue in", prevID)
	}
	if _, err := os.Stat(prev.Worktree); err != nil {
		return fail("job %s's worktree %s is gone", prevID, prev.Worktree)
	}
	task, draft, err := a.jobTask(prevID)
	if err != nil {
		return err
	}
	da, err := parseDelegateArgs(append([]string{"--task", task}, flags...), orDefault(a.Getenv("KI_AGENT"), "claude"))
	if err != nil {
		return err
	}
	da.queued, da.resumeOf, da.baseTask, da.Title = queued, &prev, task, prev.Title
	da.Draft = da.Draft || draft
	if da.Role == "" && !da.AgentGiven {
		da.Role = prev.Role
	}
	a.Dir = prev.Repo
	p, err := a.prepare(ctx, da.RunArgs)
	if err != nil {
		return err
	}
	id := ki.NewJobID(a.now())
	if queued != nil {
		id = queued.ID
	}
	role, try, err := a.providers(ctx, da, p.machine, id)
	if err != nil {
		return a.queueOrRefuse(ctx, err, da, p, id, append([]string{"resume", prevID}, flags...))
	}
	da.Task = ki.HandoffTask(task, prev, readProgress(ctx, prev.Repo, prev.Worktree, true))
	fmt.Fprintf(a.Stderr, "ki: resuming %s (%s) in %s\n", prevID, prev.Status, prev.Worktree)
	r, err := a.attemptAll(ctx, p, da, try, role, id, prev.Worktree, prev.Branch)
	if err != nil {
		return err
	}
	return a.printResult(r, da.JSON, nil)
}

// jobTask is the task a job was given, and whether its brief asked for a
// draft pull request. A job from before task.md has its brief cut at the
// contract ki appended.
func (a *App) jobTask(id string) (task string, draft bool, err error) {
	dir := filepath.Join(a.jobsDir(), id)
	brief, berr := os.ReadFile(filepath.Join(dir, "brief.md")) // #nosec G304 -- a checked job id under the jobs folder
	draft = berr == nil && strings.Contains(string(brief), "`gh pr create --draft`")
	if data, err := os.ReadFile(filepath.Join(dir, taskFile)); err == nil { // #nosec G304 -- as above
		return strings.TrimSpace(string(data)), draft, nil
	}
	if berr != nil {
		return "", false, fail("job %s has no brief to resume from: %v", id, berr)
	}
	text := string(brief)
	for _, marker := range []string{"\n\n" + ki.WorkerRole, "\n\nYou run in edits mode.", "\n\nIf you open a pull request"} {
		if i := strings.Index(text, marker); i >= 0 {
			text = text[:i]
		}
	}
	return strings.TrimSpace(text), draft, nil
}

// workStart is the base a job's branch left (origin's default branch, else the
// repo's checked-out branch) and the commit where it left it, "" when unknown.
func workStart(ctx context.Context, root, dir string) (base, start string) {
	base = git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if base == "" {
		base = git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	}
	if base != "" {
		start = git(ctx, dir, "merge-base", "HEAD", base)
	}
	return base, start
}

// readProgress is how far the work in dir got: commits since the branch left
// its base (origin's default branch, else the repo's checked-out branch),
// the diff stat including uncommitted changes, untracked files and, with
// diff, the diff itself cut short.
func readProgress(ctx context.Context, root, dir string, diff bool) ki.WorkDone {
	base, start := workStart(ctx, root, dir)
	p := ki.WorkDone{Base: base}
	if u := git(ctx, dir, "ls-files", "--others", "--exclude-standard"); u != "" {
		p.Untracked = strings.Split(u, "\n")
	}
	if start == "" {
		return p
	}
	if log := git(ctx, dir, "log", "--reverse", "--format=%h %s", start+"..HEAD"); log != "" {
		p.Commits = strings.Split(log, "\n")
	}
	p.DiffStat = git(ctx, dir, "diff", "--stat", start)
	if diff {
		p.Diff = ki.CapLines(git(ctx, dir, "diff", start), ki.HandoffDiffLines)
	}
	return p
}

// quotaStop reports whether a job ended on a usage limit after it started
// the task: the agent's error or exit said so and the run was not ended by
// ki. A CLI that died on the limit before any progress line never started
// the task, and stays LAUNCH_FAILED so the next provider runs it.
func quotaStop(out ki.Outcome, status string, runErr error, events *eventStream) bool {
	if out.QuotaFor == 0 || errors.Is(runErr, errTimeout) || errors.Is(runErr, errCancelled) || errors.Is(runErr, errStalled) {
		return false
	}
	switch status {
	case ki.JobFailed:
		return true
	case ki.JobLaunchFailed:
		return events != nil && len(events.lastLines()) > 0
	}
	return false
}

// quotaDetail is the QUOTA job's detail: the limit message and when it resets.
func quotaDetail(detail string, out ki.Outcome, until *time.Time) string {
	msg := strings.TrimSpace(out.Err)
	if msg == "" {
		msg = detail
	}
	if until == nil {
		return msg
	}
	return fmt.Sprintf("%s (over quota until %s)", truncate(msg, 120), until.Local().Format("2006-01-02 15:04"))
}
