package ki

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestQueueEntriesRoundTripOldestFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if q, err := ReadQueue(filepath.Join(dir, "missing")); err != nil || q != nil {
		t.Fatalf("no jobs folder is an empty queue: %v %v", q, err)
	}
	base := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	newer := QueueEntry{ID: NewJobID(base.Add(time.Minute)), Role: "execution", Repo: "/r", Args: []string{"--task", "b"}, Queued: base.Add(time.Minute), Reason: "claude over quota"}
	older := QueueEntry{ID: NewJobID(base), Role: "execution", Repo: "/r", Args: []string{"--task", "a"}, Queued: base}
	for _, e := range []QueueEntry{newer, older} {
		if err := WriteQueueEntry(dir, e); err != nil {
			t.Fatal(err)
		}
	}
	// A job folder without an entry (a job that ran) is not in the queue.
	if err := os.MkdirAll(filepath.Join(dir, NewJobID(base.Add(-time.Hour))), 0o700); err != nil {
		t.Fatal(err)
	}
	q, err := ReadQueue(dir)
	if err != nil || len(q) != 2 || q[0].ID != older.ID || q[1].ID != newer.ID || q[1].Reason != newer.Reason {
		t.Fatalf("queue %+v %v", q, err)
	}
	if err := RemoveQueueEntry(dir, older.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, older.ID)); !os.IsNotExist(err) {
		t.Fatalf("an emptied job folder is removed: %v", err)
	}
	if err := RemoveQueueEntry(dir, older.ID); err != nil {
		t.Fatalf("removing twice is fine: %v", err)
	}
	if err := WriteQueueEntry(dir, QueueEntry{ID: "../x"}); err == nil {
		t.Fatal("a bad id is refused")
	}
	if _, err := ReadQueueEntry(dir, "../x"); err == nil {
		t.Fatal("a bad id is refused")
	}
	if err := RemoveQueueEntry(dir, "../x"); err == nil {
		t.Fatal("a bad id is refused")
	}
	if err := os.WriteFile(filepath.Join(dir, newer.ID, QueueEntryFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadQueue(dir); err == nil {
		t.Fatal("a torn entry is an error")
	}
}

func TestQueueEntryStartedAndResult(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	back := now.Add(time.Hour)
	e := QueueEntry{ID: "j-20261107-090000-abcd", Title: "t", Role: "execution", Queued: now.Add(-time.Minute), Reason: "claude:sonnet over quota", StartAfter: &back, ResumeOf: "j-20261107-080000-abcd"}
	if !e.Waiting() || e.Started(now) {
		t.Fatal("a fresh entry waits")
	}
	r := e.Result()
	if r.Status != JobQueued || r.Detail != e.Reason || r.StartAfter == nil || !r.StartAfter.Equal(back) || r.Queued == nil || r.ResumedFrom != e.ResumeOf {
		t.Fatalf("result %+v", r)
	}
	starting := now.Add(-time.Minute)
	e.Starting = &starting
	if e.Waiting() || !e.Started(now) || e.Result().Detail != "starting" {
		t.Fatal("inside the grace an entry without a pid is started")
	}
	if e.Started(now.Add(QueueStartGrace)) {
		t.Fatal("past the grace it is not")
	}
	e.Pid = os.Getpid()
	if !e.Started(now.Add(time.Hour)) {
		t.Fatal("a live pid holds it")
	}
}

func TestLockQueueTimesOutNamingTheQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	unlock, err := LockQueue(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := LockQueue(context.Background(), dir, 50*time.Millisecond); err == nil || !strings.Contains(err.Error(), "change the queue") {
		t.Fatalf("got %v", err)
	}
}

func TestSkippedProvidersSayWhenTheyAreBack(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	claude := Provider{Agent: "claude", Model: "sonnet", Weight: 1}
	codex := Provider{Agent: "codex", Weight: 1}
	agent := Provider{Agent: "agent", Weight: 1}
	capped := Provider{Agent: "agy", Weight: 1, DailyJobs: 1}
	missing := Provider{Agent: "gone", Weight: 1}
	in := PickInput{
		Role: "execution", R: Role{Strategy: StrategyOrdered, Providers: []Provider{claude, codex, agent, capped, missing}}, Now: now,
		Installed: func(a string) bool { return a != "gone" },
		Ledger: []Result{
			{ID: "1", Provider: "claude", ModelRequested: "sonnet", Status: JobFailed, Started: now.Add(-time.Hour), QuotaUntil: &reset},
			{ID: "2", Provider: "codex", Status: JobLaunchFailed, Started: now.Add(-4 * time.Minute)},
			{ID: "3", Provider: "agent", Status: JobStalled, Started: now.Add(-20 * time.Minute), DurationS: 600},
			{ID: "4", Provider: "agy", Status: JobDone, Started: now.Add(-time.Hour)},
		},
	}
	try, skipped := Candidates(in)
	if len(try) != 0 || len(skipped) != 5 {
		t.Fatalf("try %v skipped %+v", try, skipped)
	}
	want := map[string]time.Time{
		"claude:sonnet": reset,
		"codex":         now.Add(BreakerCooldown - 4*time.Minute),
		"agent":         now.Add(-10 * time.Minute).Add(HangCooldown),
		"agy":           time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC),
		"gone":          {},
	}
	for _, s := range skipped {
		if !s.Until.Equal(want[s.Provider.Name()]) {
			t.Fatalf("%s back at %v, want %v (%s)", s.Provider.Name(), s.Until, want[s.Provider.Name()], s.Reason)
		}
	}
	if at := BackAt(skipped); !at.Equal(now.Add(BreakerCooldown - 4*time.Minute)) {
		t.Fatalf("the earliest is the breaker's end: %v", at)
	}
	if at := BackAt([]Skipped{{Provider: missing, Reason: "not on PATH"}, {Provider: claude, Demoted: true, Until: reset}}); !at.IsZero() {
		t.Fatalf("no time known: %v", at)
	}
}
