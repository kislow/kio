package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const queueUsage = `usage: ki queue run              start the queued jobs whose role has a provider back, oldest first
       ki queue install          run ki queue run every minute (a launchd agent, macOS)
       ki queue uninstall        remove that agent
       ki queue status [--json]  the agent and the queued jobs`

// queueAgentLabel names the launchd agent that runs the queue.
const queueAgentLabel = "local.ki.queue"

// queueInterval is how often the launchd agent runs the queue, in seconds.
const queueInterval = 60

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) queue(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fail("%s", queueUsage)
	}
	switch args[0] {
	case "run":
		if err := a.refuseInWorker(); err != nil {
			return err
		}
		if len(args) > 1 {
			return fail("%s", queueUsage)
		}
		return a.queueRun(ctx)
	case "start":
		if err := a.refuseInWorker(); err != nil {
			return err
		}
		if len(args) != 2 {
			return fail("usage: ki queue start <job> (ki queue run calls it)")
		}
		return a.queueStart(ctx, args[1])
	case "install", "uninstall":
		if len(args) > 1 {
			return fail("%s", queueUsage)
		}
		return a.queueAgent(ctx, args[0])
	case "status":
		return a.queueStatus(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Fprintln(a.Stdout, queueUsage)
		return nil
	}
	return fail("%s", queueUsage)
}

// queueOrRefuse queues a job whose role had no provider available, when it
// may wait (--when-available, delegate.queue, or a job the queue started);
// every other error is returned as it is.
func (a *App) queueOrRefuse(ctx context.Context, err error, da delegateArgs, p *prepared, id string, args []string) error {
	var np noProviderError
	mayWait := da.WhenAvailable || p.machine.QueueByDefault() || da.queued != nil
	if !errors.As(err, &np) || !mayWait {
		return err
	}
	for _, s := range np.skipped {
		fmt.Fprintf(a.Stderr, "ki: role %s: %s skipped, %s\n", np.role, s.Provider.Name(), s.Reason)
	}
	e := ki.QueueEntry{
		ID: id, Role: np.role, Repo: p.here.root, Issue: da.Issue, Queued: a.now(),
		Args: slices.DeleteFunc(slices.Clone(args), func(s string) bool { return s == "--when-available" }),
	}
	if da.queued != nil {
		e = *da.queued
		e.Starting, e.Pid = nil, 0
	}
	if da.resumeOf != nil {
		e.ResumeOf, e.Title = da.resumeOf.ID, da.resumeOf.Title
	} else if da.queued == nil {
		e.Title = ki.ResolveTitle(da.Title, "", da.Task)
	}
	e.Reason, e.StartAfter = waitReason(np.skipped)
	unlock, err := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if err != nil {
		return fmt.Errorf("queueing %s: %w", id, err)
	}
	defer unlock()
	if err := ki.WriteQueueEntry(a.jobsDir(), e); err != nil {
		return fmt.Errorf("queueing %s: %w", id, err)
	}
	return a.printQueued(e, da.JSON)
}

// waitReason says why a queued job waits and when a provider is expected back.
func waitReason(skipped []ki.Skipped) (string, *time.Time) {
	reasons := make([]string, 0, len(skipped))
	for _, s := range skipped {
		reasons = append(reasons, s.Provider.Name()+" "+s.Reason)
	}
	reason := "no provider available"
	if len(reasons) > 0 {
		reason = strings.Join(reasons, "; ")
	}
	at := ki.BackAt(skipped)
	if at.IsZero() {
		return reason, nil
	}
	return reason, &at
}

func (a *App) printQueued(e ki.QueueEntry, asJSON bool) error {
	if asJSON {
		if err := json.NewEncoder(a.Stdout).Encode(e.Result()); err != nil {
			return fmt.Errorf("printing the result: %w", err)
		}
		return nil
	}
	fmt.Fprintf(a.Stdout, "%s %s %s: %s; %s\n", e.ID, e.Role, ki.JobQueued, e.Reason, expectedStart(e.StartAfter))
	return nil
}

// expectedStart is when a queued job should start, as ls and the queue print it.
func expectedStart(at *time.Time) string {
	if at == nil {
		return "starts when a provider is back"
	}
	return "expected start " + at.Local().Format("2006-01-02 15:04")
}

// queueRun is one pass over the queue: entries whose ki process died go back
// to waiting (or away, when their job ran), and the oldest waiting jobs whose
// role has a provider available are started, at most delegate.queue_parallel
// running at once. Every waiting entry gets a fresh reason.
func (a *App) queueRun(ctx context.Context) error {
	m, err := ki.LoadPolicy(a.machinePath())
	if err != nil {
		return fail("%v", err)
	}
	start, err := a.pickQueued(ctx, m)
	if err != nil {
		return err
	}
	for _, e := range start {
		fmt.Fprintf(a.Stdout, "ki: queue: starting %s (role %s)\n", e.ID, e.Role)
		if err := a.startQueued(ctx, e); err != nil {
			fmt.Fprintf(a.Stderr, "ki: queue: %s did not start: %v\n", e.ID, err)
			a.requeue(ctx, e.ID, "the last start failed: "+err.Error())
		}
	}
	return nil
}

// pickQueued marks the entries to start under the queue lock and returns them.
func (a *App) pickQueued(ctx context.Context, m *ki.Machine) ([]ki.QueueEntry, error) {
	unlock, err := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if err != nil {
		return nil, fmt.Errorf("running the queue: %w", err)
	}
	defer unlock()
	entries, err := ki.ReadQueue(a.jobsDir())
	if err != nil {
		return nil, fmt.Errorf("running the queue: %w", err)
	}
	in := ki.PickInput{Now: a.now(), Installed: installed}
	if in.Ledger, err = ki.ReadLedger(filepath.Join(a.jobsDir(), ki.LedgerFile)); err != nil {
		return nil, fmt.Errorf("running the queue: %w", err)
	}
	if in.Reserved, err = ki.ReadReservations(a.jobsDir()); err != nil {
		return nil, fmt.Errorf("running the queue: %w", err)
	}
	limit, active := m.QueueParallel(), 0
	for _, e := range entries {
		if e.Started(in.Now) {
			active++
		}
	}
	var start []ki.QueueEntry
	for _, e := range entries {
		if e.Started(in.Now) {
			continue
		}
		if e.Starting != nil {
			if _, err := os.Stat(filepath.Join(a.jobsDir(), e.ID, "result.json")); err == nil {
				// Its ki died after the job ran; the job folder holds the outcome.
				if err := ki.RemoveQueueEntry(a.jobsDir(), e.ID); err != nil {
					return nil, fmt.Errorf("running the queue: %w", err)
				}
				continue
			}
			e.Starting, e.Pid = nil, 0
		}
		r, ok := m.RoleFor(e.Role)
		in.Role, in.R = e.Role, r
		var try []ki.Provider
		switch {
		case !ok:
			e.Reason, e.StartAfter = fmt.Sprintf("role %s is not in %s; ki delegate cancel %s drops it", e.Role, a.short(a.machinePath()), e.ID), nil
		default:
			var skipped []ki.Skipped
			try, skipped = ki.Candidates(in)
			if len(try) == 0 {
				e.Reason, e.StartAfter = waitReason(skipped)
			}
		}
		if len(try) > 0 && active >= limit {
			e.Reason = fmt.Sprintf("a provider is available; waiting for one of the %d queued jobs running (delegate.queue_parallel)", limit)
			e.StartAfter = nil
			try = nil
		}
		if len(try) > 0 {
			now := in.Now
			e.Starting = &now
			start = append(start, e)
			active++
		}
		if err := ki.WriteQueueEntry(a.jobsDir(), e); err != nil {
			return nil, fmt.Errorf("running the queue: %w", err)
		}
	}
	return start, nil
}

// requeue puts an entry back to waiting with a reason, best effort.
func (a *App) requeue(ctx context.Context, id, reason string) {
	unlock, err := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if err != nil {
		return
	}
	defer unlock()
	e, err := ki.ReadQueueEntry(a.jobsDir(), id)
	if err != nil {
		return
	}
	e.Starting, e.Pid, e.Reason = nil, 0, reason
	_ = ki.WriteQueueEntry(a.jobsDir(), e) // the next run reads the entry again either way
}

// startQueued hands the entry to a ki process of its own, detached, so the
// job outlives this run of the queue; its output goes to the job's queue.log.
func (a *App) startQueued(ctx context.Context, e ki.QueueEntry) error {
	if a.StartQueued != nil {
		return a.StartQueued(ctx, e)
	}
	bin := a.binary()
	if bin == "" {
		return errors.New("cannot locate the ki executable")
	}
	logPath := filepath.Join(a.jobsDir(), e.ID, "queue.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) // #nosec G304 -- under the jobs folder
	if err != nil {
		return fmt.Errorf("opening %s: %w", logPath, err)
	}
	defer log.Close()
	// Without cancel: the job must outlive this run of the queue.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), bin, "queue", "start", e.ID) // #nosec G204 -- this ki binary and a checked job id
	cmd.Dir = e.Repo
	cmd.Env = a.Environ()
	cmd.Stdout, cmd.Stderr = log, log
	// Its own session, so neither launchd nor a closed terminal takes it down with this process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", bin, err)
	}
	return cmd.Process.Release() //nolint:wrapcheck // releasing a started process only frees its handle
}

// queueStart runs one queued job in this process: it records its pid on the
// entry, runs the job under the entry's id, and drops the entry when done.
// A job whose role has no provider again goes back to waiting; one that
// cannot start for another reason is recorded FAILED and dropped.
func (a *App) queueStart(ctx context.Context, id string) error {
	e, err := a.claimQueued(ctx, id)
	if err != nil {
		return err
	}
	a.Dir = e.Repo
	if e.ResumeOf != "" {
		err = a.delegateResume(ctx, e.Args[1:], &e)
	} else {
		err = a.delegateTask(ctx, e.Args, &e)
	}
	unlock, lerr := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if lerr != nil {
		return errors.Join(err, fmt.Errorf("finishing queued job %s: %w", id, lerr))
	}
	defer unlock()
	cur, rerr := ki.ReadQueueEntry(a.jobsDir(), id)
	if rerr != nil || cur.Starting == nil {
		return err // waiting again, or cancelled
	}
	if _, serr := os.Stat(filepath.Join(a.jobsDir(), id, "result.json")); err != nil && serr != nil { // #nosec G703 -- id passed ValidJobID in claimQueued
		a.recordUnstarted(e, err)
	}
	return errors.Join(err, ki.RemoveQueueEntry(a.jobsDir(), id))
}

// claimQueued records this process on the entry, refusing one another ki holds.
func (a *App) claimQueued(ctx context.Context, id string) (ki.QueueEntry, error) {
	if !ki.ValidJobID(id) {
		return ki.QueueEntry{}, fail("%q is not a job id", id)
	}
	unlock, err := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if err != nil {
		return ki.QueueEntry{}, fmt.Errorf("starting queued job %s: %w", id, err)
	}
	defer unlock()
	e, err := ki.ReadQueueEntry(a.jobsDir(), id)
	if err != nil {
		return e, fail("job %s is not queued", id)
	}
	if e.Pid != 0 && e.Pid != os.Getpid() && ki.Alive(e.Pid) {
		return e, fail("job %s is already being started by pid %d", id, e.Pid)
	}
	if len(e.Args) == 0 || (e.ResumeOf != "" && (len(e.Args) < 2 || e.Args[0] != "resume")) {
		return e, fail("queued job %s has no arguments to run", id)
	}
	now := a.now()
	if e.Starting == nil {
		e.Starting = &now
	}
	e.Pid = os.Getpid()
	if err := ki.WriteQueueEntry(a.jobsDir(), e); err != nil {
		return e, fmt.Errorf("starting queued job %s: %w", id, err)
	}
	return e, nil
}

// recordUnstarted puts a queued job that could not start into the ledger as
// FAILED, so ls shows what happened to it instead of it vanishing.
func (a *App) recordUnstarted(e ki.QueueEntry, cause error) {
	q := e.Queued
	r := ki.Result{
		ID: e.ID, Title: e.Title, Role: e.Role, Repo: e.Repo, Issue: e.Issue, Status: ki.JobFailed,
		Detail: "queued job could not start: " + cause.Error(), Started: a.now(), Queued: &q, ResumedFrom: e.ResumeOf,
	}
	if err := ki.AppendLedger(filepath.Join(a.jobsDir(), ki.LedgerFile), r); err != nil {
		fmt.Fprintf(a.Stderr, "ki: recording %s: %v\n", e.ID, err)
	}
}

// delegateCancel drops a queued job that no ki process has started.
func (a *App) delegateCancel(ctx context.Context, args []string) error {
	if len(args) != 1 || !ki.ValidJobID(args[0]) {
		return fail("usage: ki delegate cancel <job> (a QUEUED job from ki delegate ls)")
	}
	id := args[0]
	unlock, err := ki.LockQueue(ctx, a.jobsDir(), ki.PickLockWait)
	if err != nil {
		return fmt.Errorf("cancelling %s: %w", id, err)
	}
	defer unlock()
	e, err := ki.ReadQueueEntry(a.jobsDir(), id)
	if err != nil {
		return fail("job %s is not queued", id)
	}
	if e.Started(a.now()) {
		return fail("job %s has already started; it shows as RUNNING in ki delegate ls", id)
	}
	if err := ki.RemoveQueueEntry(a.jobsDir(), id); err != nil {
		return fmt.Errorf("cancelling %s: %w", id, err)
	}
	fmt.Fprintf(a.Stdout, "%s cancelled\n", id)
	return nil
}

// queuedJobs are the queue's entries as ls shows them: a started one is left
// out once its job folder has a result, which ls lists as RUNNING.
func (a *App) queuedJobs() ([]ki.Result, error) {
	entries, err := ki.ReadQueue(a.jobsDir())
	if err != nil {
		return nil, fmt.Errorf("reading the queue: %w", err)
	}
	out := make([]ki.Result, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if _, err := os.Stat(filepath.Join(a.jobsDir(), e.ID, "result.json")); err == nil {
			continue
		}
		out = append(out, e.Result())
	}
	return out, nil
}

// queueStatus prints whether the agent is installed and the queued jobs.
func (a *App) queueStatus(ctx context.Context, args []string) error {
	asJSON := false
	for _, arg := range args {
		if arg != "--json" {
			return fail("usage: ki queue status [--json]")
		}
		asJSON = true
	}
	jobs, err := a.queuedJobs()
	if err != nil {
		return err
	}
	agent := a.queueAgentState(ctx)
	if asJSON {
		out := struct {
			Agent string      `json:"agent"`
			Jobs  []ki.Result `json:"jobs"`
		}{agent, jobs}
		if err := json.NewEncoder(a.Stdout).Encode(out); err != nil {
			return fmt.Errorf("printing the queue: %w", err)
		}
		return nil
	}
	fmt.Fprintf(a.Stdout, "agent: %s\nqueued: %d\n", agent, len(jobs))
	for _, j := range jobs {
		fmt.Fprintf(a.Stdout, "  %s  %s  %s  %s; %s\n", j.ID, j.Role, orDefault(truncate(j.Title, 50), "-"), j.Detail, expectedStart(j.StartAfter))
	}
	return nil
}

func (a *App) queueAgentPath() string {
	return filepath.Join(a.home(), "Library", "LaunchAgents", queueAgentLabel+".plist")
}

// queueAgentState is installed and loaded, installed but not loaded, or not installed.
func (a *App) queueAgentState(ctx context.Context) string {
	if a.goos() != "darwin" {
		return "not available (macOS only; run `ki queue run` every minute from cron or a systemd timer)"
	}
	if !fileExists(a.queueAgentPath()) {
		return "not installed (ki queue install)"
	}
	if _, err := a.serviceOutput(ctx, "launchctl", "list", queueAgentLabel); err != nil {
		return "installed, not loaded"
	}
	return "running every minute"
}

// queueAgent installs or removes the launchd agent that runs the queue every
// minute. launchd starts it with a bare environment, so the PATH and ki's
// own variables of this shell go into the plist: the agents must be found.
func (a *App) queueAgent(ctx context.Context, action string) error {
	if a.goos() != "darwin" {
		return fail("ki queue %s is macOS only; elsewhere run `ki queue run` every minute from cron or a systemd timer", action)
	}
	unit := a.queueAgentPath()
	if action == "uninstall" {
		if !fileExists(unit) {
			fmt.Fprintln(a.Stdout, "ki: queue agent not installed")
			return nil
		}
		_ = a.serviceCommand(ctx, "launchctl", "unload", unit) // an agent that is not loaded is still removed
		if err := os.Remove(unit); err != nil {
			return fmt.Errorf("removing %s: %w", unit, err)
		}
		fmt.Fprintf(a.Stdout, "ki: removed %s\n", a.short(unit))
		return nil
	}
	bin := a.binary()
	if bin == "" {
		return fail("cannot locate the ki executable")
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(unit), err)
	}
	if err := os.MkdirAll(a.jobsDir(), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", a.jobsDir(), err)
	}
	if err := os.WriteFile(unit, []byte(a.queuePlist(bin)), 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", unit, err)
	}
	_ = a.serviceCommand(ctx, "launchctl", "unload", unit) // reinstalling replaces a loaded agent
	if err := a.serviceCommand(ctx, "launchctl", "load", unit); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "ki: installed %s: ki queue run every minute, log in %s\n", a.short(unit), a.short(filepath.Join(a.jobsDir(), "queue.log")))
	return nil
}

// queuePlist is the launchd agent: ki queue run every minute and at load, its
// started jobs left running when the run ends (AbandonProcessGroup).
func (a *App) queuePlist(bin string) string {
	env := [][2]string{{"PATH", orDefault(a.Getenv("PATH"), "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")}}
	for _, k := range []string{"KI_HOME", "KI_CONFIG_DIR", "KI_AGENT"} {
		if v := a.Getenv(k); v != "" {
			env = append(env, [2]string{k, v})
		}
	}
	var vars strings.Builder
	for _, kv := range env {
		fmt.Fprintf(&vars, "<key>%s</key><string>%s</string>", kv[0], xmlEscape(kv[1]))
	}
	log := xmlEscape(filepath.Join(a.jobsDir(), "queue.log"))
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>queue</string><string>run</string></array>
<key>StartInterval</key><integer>%d</integer>
<key>RunAtLoad</key><true/>
<key>AbandonProcessGroup</key><true/>
<key>EnvironmentVariables</key><dict>%s</dict>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, queueAgentLabel, xmlEscape(bin), queueInterval, vars.String(), log, log)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
