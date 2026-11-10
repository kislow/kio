package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

func TestDelegateTitlePrecedence(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = claudeDone
	const brief = "Repo: work/app (this worktree). Add SVG output so other tools can show drawings. Read x first."
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"flag":  {[]string{"--task", brief, "--title", "A  short   title"}, "A short title"},
		"brief": {[]string{"--task", brief}, "Add SVG output so other tools can show drawings"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := d.delegate(append(tc.args, "--force", "--json", "--quiet")...); err != nil {
				t.Fatal(err)
			}
			r := d.result(t)
			if r.Title != tc.want {
				t.Fatalf("title %q, want %q", r.Title, tc.want)
			}
			onDisk, err := ki.ReadResult(d.jobsDir(), r.ID)
			if err != nil || onDisk.Title != tc.want {
				t.Fatalf("result.json %+v %v", onDisk, err)
			}
			ledger := d.ledger(t)
			if last := ledger[len(ledger)-1]; last.ID != r.ID || last.Title != tc.want {
				t.Fatalf("ledger %+v", last)
			}
		})
	}
}

func TestDelegateIssueTitleIsTheTitleWithoutATask(t *testing.T) {
	d := newDelegateEnv(t)
	f := newFakeGH()
	f.addIssue("work", "app", 7, "fix(ci): the build fails", "The tests fail.", "OPEN")
	d.issueServer(t, f)
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--issue", "7", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Title != "fix(ci): the build fails" {
		t.Fatalf("title %q", r.Title)
	}
	if err := d.delegate("--issue", "7", "--task", "Do it differently. Please.", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Title != "Do it differently" {
		t.Fatalf("with --task the brief names the job, got %q", r.Title)
	}
}

func TestDelegateRunningResultAlreadyHasTheTitle(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["EVENT_GAP"] = "0.5"
	_, done := d.startDelegate(t, 0, "--title", "Show drawings as pictures")
	job := d.waitRunning(t)
	if job.Title != "Show drawings as pictures" {
		t.Fatalf("running result.json title %q", job.Title)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDelegateListShowsTheTitleAndFillsOldJobsFromTheBrief(t *testing.T) {
	d := newDelegateEnv(t)
	jobs := d.jobsDir()
	old := ki.Result{ID: "j-20260101-101010-aaaa", Provider: "claude", Status: ki.JobDone, Started: time.Now().Add(-time.Hour)}
	titled := ki.Result{ID: "j-20260102-101010-bbbb", Title: "Already titled", Provider: "claude", Status: ki.JobDone, Started: time.Now().Add(-time.Minute)}
	for _, r := range []ki.Result{old, titled} {
		if err := ki.AppendLedger(filepath.Join(jobs, ki.LedgerFile), r); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(jobs, old.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	brief := "Repo: work/app (this worktree). Fix the flaky test in the cache package.\n\nBLOCKED: x\n"
	if err := os.WriteFile(filepath.Join(jobs, old.ID, "brief.md"), []byte(brief), 0o600); err != nil {
		t.Fatal(err)
	}

	ls := d.runApp(t, "delegate", "ls")
	lines := strings.Split(strings.TrimSpace(ls), "\n")
	if cols := strings.Fields(lines[0]); len(cols) < 2 || cols[0] != "JOB" || cols[1] != "TITLE" {
		t.Fatalf("TITLE must follow JOB: %q", lines[0])
	}
	if !strings.Contains(lines[1], "Already titled") || !strings.Contains(lines[2], "Fix the flaky test in the cache package") {
		t.Fatalf("ls:\n%s", ls)
	}

	var listed []ki.Result
	if err := json.Unmarshal([]byte(d.runApp(t, "delegate", "ls", "--json")), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("ls --json: %v", err)
	}
	if listed[0].Title != "Already titled" || listed[1].Title != "Fix the flaky test in the cache package" {
		t.Fatalf("json titles %q, %q", listed[0].Title, listed[1].Title)
	}
	if raw := d.runApp(t, "delegate", "ls", "--json"); !strings.Contains(raw, `"title":"Already titled"`) {
		t.Fatalf("title missing from %s", raw)
	}
	if got, err := ki.ReadLedger(filepath.Join(jobs, ki.LedgerFile)); err != nil || got[0].Title != "" {
		t.Fatalf("ls must not rewrite the ledger: %+v %v", got, err)
	}
}

func TestDelegateRefusesUnknownFlagsBeforeAnyLaunch(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = claudeDone
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"unknown flag":      {[]string{"--task", "t", "--bogus", "x"}, "unknown flag --bogus"},
		"unknown after ok":  {[]string{"--task", "t", "--force", "--titel", "x"}, "unknown flag --titel"},
		"stray argument":    {[]string{"--task", "t", "stray"}, "unexpected argument"},
		"title without val": {[]string{"--force", "--task", "t", "--title"}, "--title needs a value"},
		"model without val": {[]string{"--force", "--task", "t", "--model"}, "--model needs a value"},
	} {
		t.Run(name, func(t *testing.T) {
			err := d.delegate(tc.args...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("the agent must not have been launched")
			}
			if jobs, _ := os.ReadDir(d.jobsDir()); len(jobs) != 0 {
				t.Fatalf("a refusal must leave no job: %v", jobs)
			}
			if wts, _ := filepath.Glob(filepath.Join(filepath.Dir(d.repo), filepath.Base(d.repo)+"-worktrees", "*")); len(wts) != 0 {
				t.Fatalf("a refusal must leave no worktree: %v", wts)
			}
		})
	}
}

func TestDelegateAgentArgumentsGoAfterTheSeparator(t *testing.T) {
	da, err := parseDelegateArgs([]string{"--task", "t", "--title", "x", "--brain", "work", "--", "--bogus", "v"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if da.Title != "x" || !slices.Equal(da.Brains, []string{"work"}) || !slices.Equal(da.AgentArgs, []string{"--bogus", "v"}) {
		t.Fatalf("got %+v", da)
	}
}
