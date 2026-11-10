package ki

import (
	"fmt"
	"testing"
	"time"
)

func TestReadSettledSeesAJobThatFinishedWhilePidWasChecked(t *testing.T) {
	t.Parallel()
	const id = "j-20261026-070000-aaaa"
	dir := t.TempDir()
	rec := Result{ID: id, Provider: "claude", Status: JobRunning, Pid: 4242, Started: time.Now()}
	if err := WritePrivate(dir+"/"+id+"/result.json", rec); err != nil {
		t.Fatal(err)
	}
	// The worker writes its final status, then exits: the pid is found gone only after the record changed.
	exited := func(int) bool {
		rec.Status, rec.Detail, rec.Pid = JobDone, "hi", 0
		if err := WritePrivate(dir+"/"+id+"/result.json", rec); err != nil {
			t.Fatal(err)
		}
		return false
	}
	got, err := readSettled(dir, id, exited)
	if err != nil || got.Status != JobDone || got.Detail != "hi" {
		t.Fatalf("a finished job must show its status, not LOST: %+v %v", got, err)
	}
}

func TestReadSettledCallsAGoneWorkerLost(t *testing.T) {
	t.Parallel()
	const id = "j-20261026-070000-bbbb"
	dir := t.TempDir()
	if err := WritePrivate(dir+"/"+id+"/result.json", Result{ID: id, Status: JobRunning, Pid: 4242, KiPid: 4243, Started: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		alive func(int) bool
		want  string
	}{
		"both gone":                {func(int) bool { return false }, JobLost},
		"worker alive":             {func(pid int) bool { return pid == 4242 }, JobRunning},
		"worker gone, ki finishes": {func(pid int) bool { return pid == 4243 }, JobRunning},
	} {
		got, err := readSettled(dir, id, tc.alive)
		if err != nil || got.Status != tc.want {
			t.Errorf("%s: %+v %v", name, got, err)
		}
		if tc.want == JobLost && got.Detail != fmt.Sprintf("worker pid %d is gone", 4242) {
			t.Errorf("%s: detail %q", name, got.Detail)
		}
	}
}
