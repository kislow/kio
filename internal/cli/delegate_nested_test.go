package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

const nestedRefusal = "ki delegate: refused inside job j-20261111-000000-abcd: you are the worker, do the task yourself"

func TestDelegateWorkerEnvironmentCarriesItsJobID(t *testing.T) {
	d := newDelegateEnv(t)
	envFile := filepath.Join(t.TempDir(), "env")
	d.worker["RESULT"], d.worker["ENV_FILE"] = claudeDone, envFile
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if want := ki.JobEnv + "=" + d.result(t).ID + "\n"; !strings.Contains(readFile(t, envFile), want) {
		t.Fatalf("%q missing from the worker's environment:\n%s", want, readFile(t, envFile))
	}
}

func TestAskAndSkillRunWorkersCarryNoJobID(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	envFile := filepath.Join(t.TempDir(), "env")
	d.worker["RESULT"], d.worker["ENV_FILE"] = claudeAnswer("Fine.\n\nDONE: ok"), envFile
	if err := d.ask("What is the vault pusher?", "--json"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, envFile); strings.Contains(got, ki.JobEnv+"=") {
		t.Fatalf("an ask worker has %s:\n%s", ki.JobEnv, got)
	}
	if err := os.Remove(envFile); err != nil {
		t.Fatal(err)
	}
	if err := d.skillRun("summary", "main", "--json"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, envFile); strings.Contains(got, ki.JobEnv+"=") {
		t.Fatalf("a skill run worker has %s:\n%s", ki.JobEnv, got)
	}
}

func TestDelegateRefusesInsideAWorkerAndStartsNothing(t *testing.T) {
	for _, args := range [][]string{
		{"--task", "x", "--force"},
		{"--issue", "7", "--force"},
		{"resume", "j-20261111-111111-aaaa"},
	} {
		t.Run(args[0], func(t *testing.T) {
			d := newDelegateEnv(t)
			d.vars["KI_JOB"] = "j-20261111-000000-abcd"
			err := d.delegate(args...)
			if !isUserError(err) || err.Error() != nestedRefusal {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat(d.argsFile); !os.IsNotExist(err) {
				t.Errorf("a worker started: %v", err)
			}
			if _, err := os.Stat(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs")); !os.IsNotExist(err) {
				t.Errorf("a job folder or ledger line was written: %v", err)
			}
			if _, err := os.Stat(d.repo + "-worktrees"); !os.IsNotExist(err) {
				t.Errorf("a worktree was added: %v", err)
			}
		})
	}
}

func TestQueueStartsRefuseInsideAWorker(t *testing.T) {
	d := newDelegateEnv(t)
	d.vars["KI_JOB"] = "j-20261111-000000-abcd"
	for _, args := range [][]string{{"queue", "run"}, {"queue", "start", "j-20261111-111111-aaaa"}} {
		if err := d.app.Run(context.Background(), args); err == nil || err.Error() != nestedRefusal {
			t.Errorf("%v: got %v", args, err)
		}
	}
}

func TestDelegateReadsStayAllowedInsideAWorker(t *testing.T) {
	d := newDelegateEnv(t)
	d.vars["KI_JOB"] = "j-20261111-000000-abcd"
	for _, args := range [][]string{{"ls", "--json"}, {"health", "--json"}, {"help"}} {
		if err := d.delegate(args...); err != nil {
			t.Errorf("ki delegate %v refused: %v", args, err)
		}
	}
}
