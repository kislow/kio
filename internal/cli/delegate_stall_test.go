package cli

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const stallRolesYAML = "identities: [work]\nroles:\n  execution:\n    strategy: ordered\n    providers: [agent, claude:sonnet]\n"

// stallAfterEvents makes the worker print the stream events, commit, then go silent.
func (d *delegateEnv) stallAfterEvents(t *testing.T) {
	t.Helper()
	old := killedWorkerGrace
	killedWorkerGrace = 100 * time.Millisecond
	t.Cleanup(func() { killedWorkerGrace = old })
	d.worker["EVENTS_FILE"] = filepath.Join(t.TempDir(), "events")
	write(t, d.worker["EVENTS_FILE"], streamEvents)
	d.worker["COMMIT_MSG"], d.worker["SLEEP_AFTER"], d.worker["RESULT"] = "wip stalled", "30", claudeDone
	d.app.StallAfter = 100 * time.Millisecond
}

func (d *delegateEnv) calm() {
	delete(d.worker, "SLEEP_AFTER")
	delete(d.worker, "COMMIT_MSG")
}

func (d *delegateEnv) ledger(t *testing.T) []ki.Result {
	t.Helper()
	jobs, err := ki.ReadLedger(filepath.Join(d.jobsDir(), ki.LedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

// skewClock is the real time plus whatever a test adds, so a test ends a
// silence by moving the clock instead of waiting for it.
type skewClock struct{ skew atomic.Int64 }

func (c *skewClock) now() time.Time          { return time.Now().Add(time.Duration(c.skew.Load())) }
func (c *skewClock) advance(d time.Duration) { c.skew.Add(int64(d)) }

func TestDelegateEndsASilentWorkerAsStalledAndKeepsItsWork(t *testing.T) {
	d := newDelegateEnv(t)
	d.stallAfterEvents(t)
	clock := &skewClock{}
	d.app.EventClock = clock.now
	stderr, done := d.startDelegate(t, 50*time.Millisecond, "--stall", "1h")
	progress := regexp.MustCompile(`(?m)^ki: \d\d:\d\d claude  (edit docs/x.md|message: edited it|bash go test \./\.\.\.)$`)
	for deadline := time.Now().Add(time.Minute); len(progress.FindAllString(stderr.String(), -1)) < 3; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the worker never printed its events:\n%s", stderr.String())
		}
	}
	// All three events are in and the worker sleeps on: an hour of silence passes at once.
	clock.advance(2 * time.Hour)
	if err := <-done; err != nil {
		t.Fatalf("a stalled job is not a launch failure: %v", err)
	}
	jobs := d.ledger(t)
	if len(jobs) != 1 {
		t.Fatalf("one job in the ledger: %+v", jobs)
	}
	r, err := ki.ReadResult(d.jobsDir(), jobs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != ki.JobStalled || !strings.HasPrefix(r.Detail, "stalled: no event for ") || r.TimedOut || r.Exit != -1 {
		t.Fatalf("result %+v", r)
	}
	if r.LastEventAt == nil || r.StallS != 3600 || len(r.Tail) != 3 || !strings.HasSuffix(r.Tail[0], "edit docs/x.md") || !strings.HasSuffix(r.Tail[2], "bash go test ./...") {
		t.Fatalf("heartbeat and the last progress lines are saved: %+v", r)
	}
	if r.Worktree == "" {
		t.Fatalf("no worktree: %+v", r)
	}
	if out, err := exec.CommandContext(t.Context(), "git", "-C", r.Worktree, "log", "-1", "--format=%s").Output(); err != nil || strings.TrimSpace(string(out)) != "wip stalled" {
		t.Fatalf("the worker's commit stays in the worktree: %q %v", out, err)
	}
	final, err := ki.ReadResult(d.jobsDir(), r.ID)
	if err != nil || final.Status != ki.JobStalled || len(final.Tail) != 3 || final.Pid != 0 {
		t.Fatalf("result.json %+v %v", final, err)
	}
	if jobs := d.ledger(t); len(jobs) != 1 || jobs[0].Status != ki.JobStalled || jobs[0].Tail != nil {
		t.Fatalf("the ledger records the status, not the tail: %+v", jobs)
	}
}

func TestDelegateTimeoutStaysFailedAndIsMarked(t *testing.T) {
	d := newDelegateEnv(t)
	d.stallAfterEvents(t)
	delete(d.worker, "EVENTS_FILE")
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet", "--timeout", "400ms", "--stall", "1h"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobFailed || !r.TimedOut || !strings.Contains(r.Detail, "timed out") {
		t.Fatalf("a timeout is FAILED and marked: %+v", r)
	}
}

func TestDelegateStallLimitComesFromTheMachineUnlessTheJobSetsIt(t *testing.T) {
	d := newDelegateEnv(t)
	d.stallAfterEvents(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\ndelegate:\n  stall: 400ms\n  notify_stall: true\n")
	var notes []string
	d.app.Notify = func(title, body string) { notes = append(notes, title+": "+body) }
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Status != ki.JobStalled || r.StallS != 0.4 {
		t.Fatalf("delegate.stall applies: %+v", r)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], r.ID) || !strings.Contains(notes[0], "STALLED") {
		t.Fatalf("notify_stall notifies once: %v", notes)
	}
	d.calm()
	notes = nil
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet", "--stall", "1h"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobDone || r.StallS != 3600 || len(notes) != 0 {
		t.Fatalf("--stall wins over the machine and a healthy job notifies nobody: %+v %v", r, notes)
	}
}

func TestDelegateWithoutNotifyStallNotifiesNobody(t *testing.T) {
	d := newDelegateEnv(t)
	d.stallAfterEvents(t)
	called := false
	d.app.Notify = func(string, string) { called = true }
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet", "--stall", "300ms"); err != nil {
		t.Fatal(err)
	}
	if called || d.result(t).Status != ki.JobStalled {
		t.Fatalf("the notification is opt-in: called %v", called)
	}
}

func TestDelegateRefusesABadStall(t *testing.T) {
	d := newDelegateEnv(t)
	for _, v := range []string{"0", "0s", "-5m", "soon"} {
		if err := d.delegate("--task", "t", "--force", "--stall", v); err == nil || !strings.Contains(err.Error(), "--stall") {
			t.Errorf("--stall %s: %v", v, err)
		}
	}
}

func TestDelegateAgyIsNeverStalled(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"], d.worker["SLEEP"] = `{"conversation_id":"e2","status":"SUCCESS","response":"DONE: hi"}`, "0.6"
	if err := d.delegate("--task", "t", "--force", "--agent", "agy", "--mode", "skip", "--stall", "100ms", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobDone || r.LastEventAt != nil || r.StallS != 0 {
		t.Fatalf("agy prints once at the end, so there is nothing to stall on: %+v", r)
	}
}

func TestDelegateRunningJobCarriesItsHeartbeat(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["EVENT_GAP"] = "0.5"
	_, done := d.startDelegate(t, 0, "--stall", "20m")
	job := d.waitRunning(t)
	if job.LastEventAt == nil || job.LastEventAt.Before(job.Started) || job.StallS != 1200 {
		t.Fatalf("running record %+v", job)
	}
	var listed []ki.Result
	if err := json.Unmarshal([]byte(d.runApp(t, "delegate", "ls", "--json")), &listed); err != nil || len(listed) != 1 || listed[0].LastEventAt == nil {
		t.Fatalf("ls --json carries last_event_at: %+v %v", listed, err)
	}
	ls := d.runApp(t, "delegate", "ls")
	lines := strings.Split(ls, "\n")
	if !strings.HasSuffix(strings.TrimSpace(lines[0]), "LAST") || !strings.Contains(lines[1], job.ID) || strings.HasSuffix(strings.TrimSpace(lines[1]), "-") {
		t.Fatalf("ls shows the age of the last event:\n%s", ls)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if final, err := ki.ReadResult(d.jobsDir(), job.ID); err != nil || final.LastEventAt == nil || final.Status != ki.JobDone {
		t.Fatalf("the final record keeps the last event: %+v %v", final, err)
	}
}

func TestLastEventIsRedPastTheStallLimit(t *testing.T) {
	t.Parallel()
	if got := lastEvent(ki.Result{}, true); got != "-" {
		t.Fatalf("no events: %q", got)
	}
	for name, tc := range map[string]struct {
		quiet  time.Duration
		stallS float64
		color  bool
		want   string
	}{
		"within the limit":   {3 * time.Minute, 900, true, "03:00"},
		"past the limit":     {68*time.Minute + 8*time.Second, 900, true, "\x1b[31m1:08:08\x1b[0m"},
		"past it, no colour": {68*time.Minute + 8*time.Second, 900, false, "1:08:08"},
		"limit not recorded": {68 * time.Minute, 0, true, "1:08:00"},
		"exactly the limit":  {15 * time.Minute, 900, true, "\x1b[31m15:00\x1b[0m"},
		"just under it":      {14*time.Minute + 59*time.Second, 900, true, "14:59"},
	} {
		if got := silenceLabel(tc.quiet, tc.stallS, tc.color); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// seedJob writes a job on a provider into the ledger, ended ago before now.
func (d *delegateEnv) seedJob(t *testing.T, agent, model, status string, ago time.Duration, timedOut bool) {
	t.Helper()
	dur := 10 * time.Minute
	r := ki.Result{
		ID: ki.NewJobID(time.Now().Add(-ago)), Provider: agent, ModelRequested: model, Role: "execution", Status: status, TimedOut: timedOut,
		Started: time.Now().Add(-ago - dur), DurationS: dur.Seconds(),
	}
	if err := ki.AppendLedger(filepath.Join(d.jobsDir(), ki.LedgerFile), r); err != nil {
		t.Fatal(err)
	}
}

func (d *delegateEnv) stderr() string { return d.app.Stderr.(interface{ String() string }).String() }

func TestNextPickSkipsAProviderThatStalledThenForgetsIt(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), stallRolesYAML)
	d.stallAfterEvents(t)
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet", "--stall", "300ms"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "agent" || r.Status != ki.JobStalled {
		t.Fatalf("the first pick is the ordered role's first provider: %+v", r)
	}
	d.calm()
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "claude" || r.Status != ki.JobDone {
		t.Fatalf("the next pick skips the stalled provider: %+v", r)
	}
	if !strings.Contains(d.stderr(), "role execution: agent skipped, stalled ") || !strings.Contains(d.stderr(), "cooling down for 30m") {
		t.Fatalf("the skip says why:\n%s", d.stderr())
	}
}

func TestADemotedProviderGoesLastUntilAJobOnItSucceeds(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), stallRolesYAML)
	d.seedJob(t, "agent", "", ki.JobStalled, 5*time.Hour, false)
	d.seedJob(t, "agent", "", ki.JobFailed, 2*time.Hour, true)
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "claude" {
		t.Fatalf("claude goes first: %+v", r)
	}
	if !strings.Contains(d.stderr(), "role execution: agent demoted, 2 jobs hung in the last 24h") {
		t.Fatalf("the demotion says why:\n%s", d.stderr())
	}
	// A job on the demoted provider that ends DONE clears it.
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet", "--agent", "agent"); err != nil {
		t.Fatal(err)
	}
	d.app.Stderr.(interface{ Reset() }).Reset()
	if err := d.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "agent" || strings.Contains(d.stderr(), "demoted") {
		t.Fatalf("the demotion is gone: %+v\n%s", r, d.stderr())
	}
}

func TestDelegateHealthListsOutcomesAndTheNextPick(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), stallRolesYAML)
	d.seedJob(t, "agent", "", ki.JobDone, 3*time.Hour, false)
	d.seedJob(t, "agent", "", ki.JobStalled, 10*time.Minute, false)
	d.seedJob(t, "claude", "sonnet", ki.JobPR, time.Hour, false)
	out := d.runApp(t, "delegate", "health")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("a header and one row per provider:\n%s", out)
	}
	for _, want := range []string{"PROVIDER", "JOBS", "PR", "DONE", "STALLED", "LAUNCH_FAILED", "MEDIAN", "NEXT PICK"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header lacks %s:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(lines[1], "agent ") || !strings.Contains(lines[1], "10:00") || !strings.Contains(lines[1], "skipped: stalled 10m ago") {
		t.Fatalf("agent row:\n%s", out)
	}
	if !strings.HasPrefix(lines[2], "claude:sonnet ") || !strings.HasSuffix(lines[2], "ok") {
		t.Fatalf("claude row:\n%s", out)
	}
	var health []ki.ProviderHealth
	if err := json.Unmarshal([]byte(d.runApp(t, "delegate", "health", "--json")), &health); err != nil || len(health) != 2 {
		t.Fatalf("--json: %+v %v", health, err)
	}
	if h := health[0]; h.Provider != "agent" || h.Jobs != 2 || h.Outcomes[ki.JobStalled] != 1 || h.MedianS != 600 || !strings.Contains(h.Skip, "stalled") {
		t.Fatalf("agent %+v", h)
	}
	if err := d.delegate("health", "--nope"); err == nil {
		t.Fatal("unknown flag accepted")
	}
}

func TestDelegateHealthWithNoHistoryStillListsTheRoleProviders(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), stallRolesYAML)
	out := d.runApp(t, "delegate", "health")
	if !strings.Contains(out, "agent") || !strings.Contains(out, "claude:sonnet") || strings.Count(out, "ok") != 2 {
		t.Fatalf("no jobs yet:\n%s", out)
	}
}

func TestDoctorSummarisesProviderHealth(t *testing.T) {
	e := newDelegateEnv(t)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), stallRolesYAML)
	e.seedJob(t, "agent", "", ki.JobStalled, 10*time.Minute, false)
	e.out.Reset()
	_ = e.app.Run(t.Context(), []string{"doctor"}) // other checks may fail in a temp env
	if out := e.out.String(); !strings.Contains(out, "delegate health") || !strings.Contains(out, "agent skipped: stalled 10m ago") {
		t.Fatalf("doctor:\n%s", out)
	}
}
