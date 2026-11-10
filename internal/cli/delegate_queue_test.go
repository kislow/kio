package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const queueRolesYAML = "identities: [work]\nroles:\n  execution:\n    strategy: ordered\n    providers: [claude:sonnet, codex:gpt-5-mini]\n"

// claudeQuota is Claude Code's result when the account runs out mid-run.
const claudeQuota = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your usage limit · resets in 2h","session_id":"q1"}`

// fakeClock makes the app's clock settable; it starts at now.
func (d *delegateEnv) fakeClock() *time.Time {
	now := time.Now()
	d.app.Now = func() time.Time { return now }
	return &now
}

// startQueuedInline runs a started queue entry in this process, the way the
// detached `ki queue start` would.
func (d *delegateEnv) startQueuedInline() {
	d.app.StartQueued = func(ctx context.Context, e ki.QueueEntry) error { return d.app.queueStart(ctx, e.ID) }
}

// overQuota records a job on the agent that left it over quota until the time given.
func (d *delegateEnv) overQuota(t *testing.T, agent, model string, until time.Time) {
	t.Helper()
	r := ki.Result{ID: ki.NewJobID(time.Now()), Provider: agent, ModelRequested: model, Role: "execution", Status: ki.JobFailed, Started: time.Now().Add(-time.Minute), QuotaUntil: &until}
	if err := ki.AppendLedger(filepath.Join(d.jobsDir(), ki.LedgerFile), r); err != nil {
		t.Fatal(err)
	}
}

func (d *delegateEnv) run(args ...string) error {
	d.out.Reset()
	return d.app.Run(context.Background(), args)
}

func (d *delegateEnv) queued(t *testing.T) []ki.QueueEntry {
	t.Helper()
	q, err := ki.ReadQueue(d.jobsDir())
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestWhenAvailableQueuesAndStartsAfterTheReset(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	clock := d.fakeClock()
	reset := clock.Add(time.Hour)
	d.overQuota(t, "claude", "sonnet", reset)
	d.overQuota(t, "codex", "gpt-5-mini", reset.Add(30*time.Minute))
	d.worker["RESULT"] = claudeDone

	if err := d.delegate("--task", "fix the build", "--force", "--json", "--quiet", "--when-available"); err != nil {
		t.Fatal(err)
	}
	q := d.result(t)
	if q.Status != ki.JobQueued || q.Role != "execution" || q.StartAfter == nil || !q.StartAfter.Equal(reset) || !strings.Contains(q.Detail, "claude:sonnet over quota until") || q.Title != "fix the build" {
		t.Fatalf("queued %+v", q)
	}
	if _, err := os.Stat(d.argsFile); err == nil {
		t.Fatal("no worker may run while every provider is over quota")
	}
	if err := d.delegate("ls"); err != nil {
		t.Fatal(err)
	}
	if out := d.out.String(); !strings.Contains(out, q.ID) || !strings.Contains(out, "QUEUED") || !strings.Contains(out, "role execution") || !strings.Contains(out, "expected start") {
		t.Fatalf("ls:\n%s", out)
	}

	d.startQueuedInline()
	*clock = clock.Add(30 * time.Minute)
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.argsFile); err == nil || len(d.queued(t)) != 1 {
		t.Fatal("before the reset the job keeps waiting")
	}

	*clock = reset.Add(time.Minute)
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.out.String(), "starting "+q.ID) {
		t.Fatalf("queue run: %s", d.out.String())
	}
	if q := d.queued(t); len(q) != 0 {
		t.Fatalf("the started job leaves the queue: %+v", q)
	}
	jobs := d.ledger(t)
	last := jobs[len(jobs)-1]
	if last.ID != q.ID || last.Provider != "claude" || last.Status != ki.JobDone || last.Queued == nil || last.Worktree == "" {
		t.Fatalf("the queued job ran under its id: %+v", last)
	}
	if args := d.args(t); !strings.HasPrefix(args[len(args)-1], "fix the build\n\n") {
		t.Fatalf("brief %q", args[len(args)-1])
	}
	if err := d.delegate("ls", "--json"); err != nil {
		t.Fatal(err)
	}
	var listed []ki.Result
	if err := json.Unmarshal(d.out.Bytes(), &listed); err != nil || slices.ContainsFunc(listed, func(r ki.Result) bool { return r.Status == ki.JobQueued }) {
		t.Fatalf("ls --json: %v %s", err, d.out.String())
	}
}

func TestMachineQueueIsTheDefaultAndRefusalStaysWithout(t *testing.T) {
	d := newDelegateEnv(t)
	d.overQuota(t, "claude", "sonnet", time.Now().Add(time.Hour))
	d.overQuota(t, "codex", "gpt-5-mini", time.Now().Add(time.Hour))
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	if err := d.delegate("--task", "t", "--force"); !isUserError(err) || !strings.Contains(err.Error(), "no provider available") {
		t.Fatalf("without the flag or the setting the job is refused: %v", err)
	}
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML+"delegate:\n  queue: true\n")
	if err := d.delegate("--task", "t", "--force"); err != nil {
		t.Fatal(err)
	}
	if out := d.out.String(); !strings.Contains(out, " execution QUEUED: ") || !strings.Contains(out, "expected start") {
		t.Fatalf("out %s", out)
	}
	q := d.queued(t)
	if len(q) != 1 || !slices.Equal(q[0].Args, []string{"--task", "t", "--force"}) || q[0].Repo == "" {
		t.Fatalf("queue %+v", q)
	}
}

func TestQueueRunsAtMostQueueParallelAndCancelDropsAWaitingJob(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML+"delegate:\n  queue: true\n  queue_parallel: 1\n")
	clock := d.fakeClock()
	d.overQuota(t, "claude", "sonnet", clock.Add(time.Hour))
	d.overQuota(t, "codex", "gpt-5-mini", clock.Add(time.Hour))
	for _, task := range []string{"first", "second"} {
		if err := d.delegate("--task", task, "--force", "--json"); err != nil {
			t.Fatal(err)
		}
		*clock = clock.Add(time.Second)
	}
	var started []string
	d.app.StartQueued = func(_ context.Context, e ki.QueueEntry) error {
		started = append(started, e.ID)
		return nil
	}
	*clock = clock.Add(2 * time.Hour)
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	q := d.queued(t)
	if len(started) != 1 || started[0] != q[0].ID || q[0].Starting == nil || q[1].Starting != nil || !strings.Contains(q[1].Reason, "waiting for one of the 1 queued jobs") {
		t.Fatalf("started %v queue %+v", started, q)
	}
	if err := d.delegate("cancel", q[0].ID); !isUserError(err) || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("a started job is not cancelled: %v", err)
	}
	if err := d.delegate("cancel", q[1].ID); err != nil || !strings.Contains(d.out.String(), "cancelled") {
		t.Fatalf("cancel: %v %s", err, d.out.String())
	}
	if err := d.delegate("cancel", q[1].ID); !isUserError(err) || !strings.Contains(err.Error(), "not queued") {
		t.Fatalf("cancelled twice: %v", err)
	}
	if err := d.delegate("cancel", "nope"); !isUserError(err) {
		t.Fatalf("bad id: %v", err)
	}

	// The process that took the first one died before the job ran: it waits again, then starts again.
	e := q[0]
	e.Pid = 1 << 22
	if err := ki.WriteQueueEntry(d.jobsDir(), e); err != nil {
		t.Fatal(err)
	}
	started = nil
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0] != e.ID {
		t.Fatalf("a dead start is retried: %v", started)
	}
	// One whose job ran before its ki died is dropped: the job folder has the outcome.
	if err := ki.WriteQueueEntry(d.jobsDir(), e); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.jobsDir(), e.ID, "result.json"), "{}")
	started = nil
	if err := d.run("queue", "run"); err != nil || len(started) != 0 || len(d.queued(t)) != 0 {
		t.Fatalf("started %v queue %+v err %v", started, d.queued(t), err)
	}
}

func TestQueueRunKeepsAJobWhoseRoleIsGoneAndRequeuesAFailedStart(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	clock := d.fakeClock()
	gone := ki.QueueEntry{ID: ki.NewJobID(*clock), Role: "search", Repo: d.repo, Args: []string{"--task", "x"}, Queued: *clock}
	if err := ki.WriteQueueEntry(d.jobsDir(), gone); err != nil {
		t.Fatal(err)
	}
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	if q := d.queued(t); len(q) != 1 || !strings.Contains(q[0].Reason, "role search is not in") {
		t.Fatalf("queue %+v", q)
	}
	if err := ki.RemoveQueueEntry(d.jobsDir(), gone.ID); err != nil {
		t.Fatal(err)
	}
	e := ki.QueueEntry{ID: ki.NewJobID(*clock), Role: "execution", Repo: d.repo, Args: []string{"--task", "x"}, Queued: *clock}
	if err := ki.WriteQueueEntry(d.jobsDir(), e); err != nil {
		t.Fatal(err)
	}
	d.app.StartQueued = func(context.Context, ki.QueueEntry) error { return os.ErrPermission }
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	if q := d.queued(t); len(q) != 1 || q[0].Starting != nil || !strings.Contains(q[0].Reason, "the last start failed") {
		t.Fatalf("queue %+v", q)
	}
	if err := d.run("queue", "bogus"); !isUserError(err) {
		t.Fatalf("usage: %v", err)
	}
	if err := d.run("queue"); !isUserError(err) {
		t.Fatalf("usage: %v", err)
	}
	if err := d.run("queue", "help"); err != nil || !strings.Contains(d.out.String(), "ki queue run") {
		t.Fatalf("help: %v", err)
	}
}

func TestQueueStartRequeuesWithoutAProviderAndRecordsAJobThatCannotStart(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	clock := d.fakeClock()
	d.overQuota(t, "claude", "sonnet", clock.Add(time.Hour))
	d.overQuota(t, "codex", "gpt-5-mini", clock.Add(time.Hour))
	e := ki.QueueEntry{ID: ki.NewJobID(*clock), Role: "execution", Repo: d.repo, Args: []string{"--task", "x", "--force"}, Queued: *clock, Starting: clock}
	if err := ki.WriteQueueEntry(d.jobsDir(), e); err != nil {
		t.Fatal(err)
	}
	if err := d.run("queue", "start", e.ID); err != nil {
		t.Fatal(err)
	}
	if q := d.queued(t); len(q) != 1 || q[0].Starting != nil || q[0].Pid != 0 || q[0].StartAfter == nil || !q[0].Queued.Equal(e.Queued) {
		t.Fatalf("no provider again puts it back to waiting: %+v", q)
	}
	if _, err := os.Stat(d.argsFile); err == nil {
		t.Fatal("no worker may run")
	}

	bad := ki.QueueEntry{ID: ki.NewJobID(clock.Add(time.Second)), Role: "execution", Repo: d.repo, Args: []string{"--bogus"}, Queued: *clock, Title: "broken"}
	if err := ki.WriteQueueEntry(d.jobsDir(), bad); err != nil {
		t.Fatal(err)
	}
	if err := d.run("queue", "start", bad.ID); !isUserError(err) {
		t.Fatalf("a job that cannot start fails: %v", err)
	}
	jobs := d.ledger(t)
	if last := jobs[len(jobs)-1]; last.ID != bad.ID || last.Status != ki.JobFailed || !strings.HasPrefix(last.Detail, "queued job could not start: unknown flag --bogus") || last.Queued == nil {
		t.Fatalf("ledger %+v", last)
	}
	if q := d.queued(t); len(q) != 1 || q[0].ID != e.ID {
		t.Fatalf("the failed one leaves the queue: %+v", q)
	}
	for _, args := range [][]string{{"queue", "start"}, {"queue", "start", "../x"}, {"queue", "start", ki.NewJobID(time.Now())}} {
		if err := d.run(args...); !isUserError(err) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestQuotaMidRunEndsQuotaAndResumeContinuesOnAnotherAgent(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	d.worker["EVENTS_FILE"] = filepath.Join(t.TempDir(), "events")
	write(t, d.worker["EVENTS_FILE"], streamEvents)
	d.worker["COMMIT_MSG"], d.worker["RESULT"], d.worker["EXIT"] = "wip: half done", claudeQuota, "1"
	if err := d.delegate("--task", "fix the thing", "--force", "--json", "--quiet", "--draft"); err != nil {
		t.Fatalf("a quota stop is not a launch failure: %v", err)
	}
	stopped := d.result(t)
	if stopped.Status != ki.JobQuota || stopped.Provider != "claude" || stopped.QuotaUntil == nil || !strings.Contains(stopped.Detail, "hit your usage limit") || !strings.Contains(stopped.Detail, "over quota until") {
		t.Fatalf("stopped %+v", stopped)
	}
	if len(stopped.Tail) != 3 || len(stopped.Commits) != 1 || !strings.HasSuffix(stopped.Commits[0], " wip: half done") || stopped.Worktree == "" {
		t.Fatalf("how far it got: %+v", stopped)
	}
	if jobs := d.ledger(t); jobs[len(jobs)-1].Commits != nil || jobs[len(jobs)-1].Status != ki.JobQuota {
		t.Fatalf("the ledger keeps the status, not the progress: %+v", jobs[len(jobs)-1])
	}
	write(t, filepath.Join(stopped.Worktree, "notes.txt"), "draft\n")

	for k := range d.worker {
		delete(d.worker, k)
	}
	d.worker["RESULT"], d.worker["LAST"] = codexEvents, "DONE: finished"
	if err := d.delegate("resume", stopped.ID, "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Provider != "codex" || r.Status != ki.JobDone || r.ResumedFrom != stopped.ID || r.Worktree != stopped.Worktree || r.Branch != stopped.Branch || r.Role != "execution" {
		t.Fatalf("resumed %+v", r)
	}
	if cwd := mustEval(t, d.cwd(t)); cwd != mustEval(t, stopped.Worktree) {
		t.Fatalf("worker ran in %s, want %s", cwd, stopped.Worktree)
	}
	args := d.args(t)
	brief := args[len(args)-1]
	for _, want := range []string{"fix the thing\n\n## Handoff", "claude (sonnet) in job " + stopped.ID, "wip: half done", "New files not yet added: notes.txt", "edit docs/x.md", "`gh pr create --draft`", "DONE: <one-line summary>"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("brief misses %q:\n%s", want, brief)
		}
	}
	task, err := os.ReadFile(filepath.Join(d.jobsDir(), r.ID, taskFile))
	if err != nil || strings.TrimSpace(string(task)) != "fix the thing" {
		t.Fatalf("the resumed job keeps the original task for the next resume: %q %v", task, err)
	}
}

func TestResumeWhenAvailableQueuesUntilAProviderIsBack(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), queueRolesYAML)
	clock := d.fakeClock()
	d.worker["EVENTS_FILE"] = filepath.Join(t.TempDir(), "events")
	write(t, d.worker["EVENTS_FILE"], streamEvents)
	d.worker["RESULT"], d.worker["EXIT"] = claudeQuota, "1"
	if err := d.delegate("--task", "fix the thing", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	stopped := d.result(t)
	d.overQuota(t, "codex", "gpt-5-mini", clock.Add(3*time.Hour))
	if err := d.delegate("resume", stopped.ID, "--force", "--json"); !isUserError(err) || !strings.Contains(err.Error(), "no provider available") {
		t.Fatalf("without --when-available a resume is refused: %v", err)
	}
	if err := d.delegate("resume", stopped.ID, "--force", "--json", "--when-available"); err != nil {
		t.Fatal(err)
	}
	q := d.result(t)
	if q.Status != ki.JobQueued || q.ResumedFrom != stopped.ID || q.StartAfter == nil {
		t.Fatalf("queued resume %+v", q)
	}
	entry := d.queued(t)[0]
	if !slices.Equal(entry.Args, []string{"resume", stopped.ID, "--force", "--json"}) || entry.ResumeOf != stopped.ID {
		t.Fatalf("entry %+v", entry)
	}

	for k := range d.worker {
		delete(d.worker, k)
	}
	d.worker["RESULT"] = claudeDone
	d.startQueuedInline()
	*clock = clock.Add(4 * time.Hour)
	if err := d.run("queue", "run"); err != nil {
		t.Fatal(err)
	}
	jobs := d.ledger(t)
	last := jobs[len(jobs)-1]
	if last.ID != q.ID || last.ResumedFrom != stopped.ID || last.Status != ki.JobDone || last.Worktree != stopped.Worktree {
		t.Fatalf("the queued resume ran: %+v", last)
	}
	if args := d.args(t); !strings.Contains(args[len(args)-1], "## Handoff") {
		t.Fatalf("brief %q", args[len(args)-1])
	}
}

func TestResumeRefusals(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	done := d.result(t)
	if err := d.delegate("--task", "t", "--force", "--json", "--here"); err != nil {
		t.Fatal(err)
	}
	here := d.result(t)
	failedHere := here
	failedHere.Status = ki.JobFailed
	if err := ki.WritePrivate(filepath.Join(d.jobsDir(), here.ID, "result.json"), failedHere); err != nil {
		t.Fatal(err)
	}
	gone := done
	gone.ID, gone.Status, gone.Worktree = ki.NewJobID(time.Now().Add(time.Second)), ki.JobQuota, filepath.Join(t.TempDir(), "missing")
	if err := ki.WritePrivate(filepath.Join(d.jobsDir(), gone.ID, "result.json"), gone); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no job":        {nil, "usage: ki delegate resume"},
		"bad id":        {[]string{"../x"}, "usage: ki delegate resume"},
		"unknown job":   {[]string{ki.NewJobID(time.Now().Add(-time.Hour))}, "has no result to resume"},
		"finished job":  {[]string{done.ID}, "ended DONE"},
		"no worktree":   {[]string{here.ID}, "without a worktree of its own"},
		"worktree gone": {[]string{gone.ID}, "is gone"},
		"task flag":     {[]string{done.ID, "--task", "x"}, "--task does not apply to resume"},
		"issue flag":    {[]string{done.ID, "--issue", "3"}, "--issue does not apply to resume"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := d.delegate(append([]string{"resume"}, tc.args...)...); !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}

func TestJobTaskFallsBackToTheBriefWithoutItsContract(t *testing.T) {
	d := newDelegateEnv(t)
	id := ki.NewJobID(time.Now())
	write(t, filepath.Join(d.jobsDir(), id, "brief.md"), ki.WrapEditsTask("old task", []string{"git"}, true)+"\n")
	task, draft, err := d.app.jobTask(id)
	if err != nil || task != "old task" || !draft {
		t.Fatalf("task %q draft %v err %v", task, draft, err)
	}
	if _, _, err := d.app.jobTask(ki.NewJobID(time.Now().Add(time.Hour))); !isUserError(err) {
		t.Fatalf("no brief: %v", err)
	}
}

func TestQueueInstallWritesALaunchdAgent(t *testing.T) {
	d := newDelegateEnv(t)
	bin := t.TempDir()
	write(t, filepath.Join(bin, "launchctl"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "launchctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	d.vars["PATH"] = "/opt/agents/bin:/usr/bin"
	d.app.GOOS = "darwin"
	d.app.Executable = func() (string, error) { return "/opt/ki & co/ki", nil }
	if err := d.run("queue", "status"); err != nil || !strings.Contains(d.out.String(), "not installed") {
		t.Fatalf("status before install: %v %s", err, d.out.String())
	}
	if err := d.run("queue", "install"); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(d.app.queueAgentPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>local.ki.queue</string>", "<string>/opt/ki &amp; co/ki</string><string>queue</string><string>run</string>",
		"<key>StartInterval</key><integer>60</integer>", "<key>AbandonProcessGroup</key><true/>",
		"<key>PATH</key><string>/opt/agents/bin:/usr/bin</string>", "<key>KI_CONFIG_DIR</key>", "queue.log",
	} {
		if !strings.Contains(string(plist), want) {
			t.Fatalf("plist misses %q:\n%s", want, plist)
		}
	}
	if err := d.run("queue", "status", "--json"); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Agent string      `json:"agent"`
		Jobs  []ki.Result `json:"jobs"`
	}
	if err := json.Unmarshal(d.out.Bytes(), &st); err != nil || st.Agent != "running every minute" || len(st.Jobs) != 0 {
		t.Fatalf("status %+v %v", st, err)
	}
	if err := d.run("queue", "uninstall"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.app.queueAgentPath()); !os.IsNotExist(err) {
		t.Fatalf("plist remains: %v", err)
	}
	if err := d.run("queue", "uninstall"); err != nil || !strings.Contains(d.out.String(), "not installed") {
		t.Fatalf("uninstall twice: %v", err)
	}
	if err := d.run("queue", "status", "--bogus"); !isUserError(err) {
		t.Fatalf("status usage: %v", err)
	}
	d.app.GOOS = "linux"
	if err := d.run("queue", "install"); !isUserError(err) || !strings.Contains(err.Error(), "macOS only") {
		t.Fatalf("linux: %v", err)
	}
	if err := d.run("queue", "status"); err != nil || !strings.Contains(d.out.String(), "cron or a systemd timer") {
		t.Fatalf("linux status: %v %s", err, d.out.String())
	}
}
