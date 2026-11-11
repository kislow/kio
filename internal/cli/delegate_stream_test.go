package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// lockedBuffer is a writer a test can read while the command still writes.
type lockedBuffer struct {
	lock sync.Mutex
	buf  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.lock.Lock()
	defer b.lock.Unlock()
	return b.buf.String()
}

const streamEvents = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"docs/x.md"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"edited it"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
`

// startDelegate runs a streaming claude job in the background and returns what it printed.
func (d *delegateEnv) startDelegate(t *testing.T, stall time.Duration, args ...string) (stderr *lockedBuffer, done chan error) {
	t.Helper()
	d.worker["EVENTS_FILE"] = filepath.Join(t.TempDir(), "events")
	write(t, d.worker["EVENTS_FILE"], streamEvents)
	d.worker["RESULT"] = claudeDone
	stderr, done = &lockedBuffer{}, make(chan error, 1)
	app := *d.app
	app.Stdout, app.Stderr, app.StallAfter = &bytes.Buffer{}, stderr, stall
	go func() {
		done <- app.Run(context.Background(), append([]string{"delegate", "--task", "t", "--force", "--json"}, args...))
	}()
	return stderr, done
}

func (d *delegateEnv) jobsDir() string { return filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs") }

func (d *delegateEnv) waitRunning(t *testing.T) ki.Result {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if running, err := ki.ReadRunning(d.jobsDir()); err == nil && len(running) == 1 {
			return running[0]
		}
	}
	t.Fatal("the job never showed as RUNNING")
	return ki.Result{}
}

func (d *delegateEnv) runApp(t *testing.T, args ...string) string {
	t.Helper()
	app, out := *d.app, &bytes.Buffer{}
	app.Stdout = out
	if err := app.Run(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestDelegateStreamsProgressAndShowsTheRunningJob(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["EVENT_GAP"] = "0.5"
	stderr, done := d.startDelegate(t, 0)
	job := d.waitRunning(t)
	if job.Status != ki.JobRunning || job.Pid == 0 || job.Provider != "claude" {
		t.Fatalf("running job %+v", job)
	}
	ls := d.runApp(t, "delegate", "ls")
	if lines := strings.Split(ls, "\n"); !strings.Contains(lines[1], job.ID) || !strings.Contains(lines[1], "RUNNING") || !strings.Contains(lines[1], "pid ") {
		t.Fatalf("ls must list the running job first:\n%s", ls)
	}
	var listed []ki.Result
	if err := json.Unmarshal([]byte(d.runApp(t, "delegate", "ls", "--json")), &listed); err != nil || len(listed) != 1 || listed[0].Status != ki.JobRunning {
		t.Fatalf("ls --json: %+v %v", listed, err)
	}
	watched := d.runApp(t, "delegate", "watch", job.ID)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Minute):
		t.Fatal("delegate did not finish")
	}
	progress := regexp.MustCompile(`(?m)^ki: \d\d:\d\d claude  (edit docs/x.md|message: edited it|bash go test \./\.\.\.)$`)
	if got := progress.FindAllString(stderr.String(), -1); len(got) != 3 {
		t.Fatalf("want three progress lines on stderr, got %d:\n%s", len(got), stderr.String())
	}
	// A line already in the log when watch starts carries --:--; the rest carry the clock.
	replayed := regexp.MustCompile(`(?m)^ki: (--:--|\d\d:\d\d) claude  (edit docs/x.md|message: edited it|bash go test \./\.\.\.)$`)
	if got := replayed.FindAllString(watched, -1); len(got) != 3 || !strings.HasSuffix(strings.TrimSpace(watched), "DONE: hi") {
		t.Fatalf("watch must print the three lines and the final status:\n%s", watched)
	}
	final, err := ki.ReadResult(d.jobsDir(), job.ID)
	if err != nil || final.Status != ki.JobDone || final.Pid != 0 {
		t.Fatalf("result.json after the run: %+v %v", final, err)
	}
	if ledger, err := ki.ReadLedger(filepath.Join(d.jobsDir(), ki.LedgerFile)); err != nil || len(ledger) != 1 || ledger[0].Status != ki.JobDone {
		t.Fatalf("the ledger is appended once, at the end: %+v %v", ledger, err)
	}
	log, _ := os.ReadFile(filepath.Join(d.jobsDir(), job.ID, "output.log"))
	if !strings.Contains(string(log), `"file_path":"docs/x.md"`) || !strings.Contains(string(log), `"type":"result"`) {
		t.Fatalf("every event goes to output.log:\n%s", log)
	}
	if again := d.runApp(t, "delegate", "watch", job.ID); strings.Count(again, "ki: --:-- claude  ") != 3 || !strings.Contains(again, job.ID+" claude DONE: hi") {
		t.Fatalf("watching a finished job replays its lines:\n%s", again)
	}
}

func TestDelegateQuietKeepsStderrFreeOfProgress(t *testing.T) {
	d := newDelegateEnv(t)
	stderr, done := d.startDelegate(t, 0, "--quiet")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "claude  ") {
		t.Fatalf("--quiet printed progress:\n%s", stderr.String())
	}
}

func TestDelegateWarnsWhenAWorkerGoesSilent(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["SLEEP_AFTER"] = "1"
	stderr, done := d.startDelegate(t, 200*time.Millisecond, "--quiet")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "no event for") {
		t.Fatalf("no stall warning, even under --quiet:\n%s", stderr.String())
	}
}

func TestDelegateAgyHasNothingToStream(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = `{"conversation_id":"e2","status":"SUCCESS","response":"DONE: hi"}`
	app := *d.app
	stderr := &lockedBuffer{}
	app.Stdout, app.Stderr, app.StallAfter = &bytes.Buffer{}, stderr, time.Millisecond
	d.worker["SLEEP"] = "0.3"
	if err := app.Run(context.Background(), []string{"delegate", "--task", "t", "--force", "--agent", "agy", "--mode", "skip"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "no event") {
		t.Fatalf("agy prints once at the end; a stall warning is noise:\n%s", stderr.String())
	}
}

func TestDelegateWatchRefusals(t *testing.T) {
	d := newDelegateEnv(t)
	for _, args := range [][]string{{"watch"}, {"watch", "../x"}, {"watch", "j-20261026-070000-aaaa"}} {
		if err := d.delegate(args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestDelegateShowsAJobWhoseWorkerDiedAsLost(t *testing.T) {
	d := newDelegateEnv(t)
	id := "j-20261026-070000-aaaa"
	if err := ki.WritePrivate(filepath.Join(d.jobsDir(), id, "result.json"), ki.Result{ID: id, Provider: "claude", Status: ki.JobRunning, Pid: 0, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(d.jobsDir(), id, "output.log"), "")
	if ls := d.runApp(t, "delegate", "ls"); !strings.Contains(ls, "LOST") {
		t.Fatalf("ls:\n%s", ls)
	}
	if w := d.runApp(t, "delegate", "watch", id); !strings.Contains(w, id+" claude LOST: worker pid 0 is gone") {
		t.Fatalf("watch:\n%s", w)
	}
}
