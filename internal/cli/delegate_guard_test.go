package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

func gitShow(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a test fixture
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func executable(t *testing.T, path, body string) {
	t.Helper()
	write(t, path, body)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

const (
	// The publish scan keeps attribution out of the public tree, so the fixtures
	// below are assembled at run time.
	coAuthoredBy     = "Co-authored" + "-by: "
	coAuthoredByCaps = "Co-Authored" + "-By: "
	anthropic        = "anthropic" + ".com"

	cursorTrailer = coAuthoredBy + "Cursor <cursoragent@cursor.com>"
	claudeTrailer = "🤖 Generated with [Claude Code](https://claude.com/claude-code)\n\n" + coAuthoredByCaps + "Claude <noreply@" + anthropic + ">"
	codexTrailer  = coAuthoredBy + "Codex <codex@openai.com>"
	aliceTrailer  = coAuthoredBy + "Alice <alice@example.com>"
)

func TestWorkerCommitsLoseAgentAttributionAndKeepHumanCoAuthors(t *testing.T) {
	tests := []struct{ name, msg, want string }{
		{name: "cursor", msg: "fix: x\n\n" + cursorTrailer, want: "fix: x"},
		{name: "claude", msg: "fix: x\n\n" + claudeTrailer, want: "fix: x"},
		{name: "codex", msg: "fix: x\n\n" + codexTrailer, want: "fix: x"},
		{name: "human", msg: "fix: x\n\n" + aliceTrailer, want: "fix: x\n\n" + aliceTrailer},
		{name: "human and agent", msg: "fix: x\n\n" + aliceTrailer + "\n" + cursorTrailer, want: "fix: x\n\n" + aliceTrailer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := newDelegateEnv(t)
			d.worker["RESULT"], d.worker["COMMIT_MSG"] = claudeDone, tc.msg
			if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
				t.Fatal(err)
			}
			r := d.result(t)
			if r.Status != ki.JobDone {
				t.Fatalf("result %+v", r)
			}
			if got := gitShow(t, r.Worktree, "log", "-1", "--format=%B"); got != tc.want {
				t.Fatalf("commit message %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkersOwnCommitMsgAndOtherHooksStillRun(t *testing.T) {
	d := newDelegateEnv(t)
	marker := filepath.Join(t.TempDir(), "pre-commit-ran")
	hooks := filepath.Join(d.repo, ".git", "hooks")
	executable(t, filepath.Join(hooks, "commit-msg"), "#!/bin/sh\nprintf 'Checked-by: acme-lint\\n' >> \"$1\"\n")
	executable(t, filepath.Join(hooks, "pre-commit"), "#!/bin/sh\ntouch '"+marker+"'\n")
	d.worker["RESULT"], d.worker["COMMIT_MSG"] = claudeDone, "fix: x\n\n"+cursorTrailer
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if got := gitShow(t, r.Worktree, "log", "-1", "--format=%B"); got != "fix: x\nChecked-by: acme-lint" {
		t.Fatalf("the repo's commit-msg hook must run on the stripped message, got %q", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the repo's pre-commit hook did not run: %v", err)
	}
}

func TestWorkersRepoHooksPathIsHonoured(t *testing.T) {
	d := newDelegateEnv(t)
	marker := filepath.Join(t.TempDir(), "ran")
	gitIn(t, d.repo, "config", "core.hooksPath", "team-hooks")
	executable(t, filepath.Join(d.repo, "team-hooks", "commit-msg"), "#!/bin/sh\ntouch '"+marker+"'\n")
	gitIn(t, d.repo, "add", "-A")
	gitIn(t, d.repo, "-c", "core.hooksPath=/dev/null", "commit", "-q", "-m", "chore: hooks")
	d.worker["RESULT"], d.worker["COMMIT_MSG"] = claudeDone, "fix: x\n\n"+cursorTrailer
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the repo's hook under core.hooksPath did not run: %v", err)
	}
	if got := gitShow(t, d.result(t).Worktree, "log", "-1", "--format=%B"); got != "fix: x" {
		t.Fatalf("commit message %q", got)
	}
}

func TestJobLeavesTheRepositoryGitConfigUntouched(t *testing.T) {
	d := newDelegateEnv(t)
	configPath := filepath.Join(d.repo, ".git", "config")
	before := readFile(t, configPath)
	d.worker["RESULT"], d.worker["COMMIT_MSG"] = claudeDone, "fix: x\n\n"+cursorTrailer
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if after := readFile(t, configPath); after != before {
		t.Fatalf(".git/config changed:\n%s\n---\n%s", before, after)
	}
	r := d.result(t)
	gitDir := gitShow(t, r.Worktree, "rev-parse", "--absolute-git-dir")
	if _, err := os.Stat(filepath.Join(gitDir, "config.worktree")); !os.IsNotExist(err) {
		t.Errorf("config.worktree was written: %v", err)
	}
}

func TestWorkerGitIsPointedAtTheJobsHooksThroughTheEnvironment(t *testing.T) {
	d := newDelegateEnv(t)
	envFile := filepath.Join(t.TempDir(), "env")
	d.worker["RESULT"], d.worker["ENV_FILE"] = claudeDone, envFile
	d.worker["GIT_CONFIG_COUNT"], d.worker["GIT_CONFIG_KEY_0"], d.worker["GIT_CONFIG_VALUE_0"] = "1", "http.proxy", "http://127.0.0.1:1"
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, envFile)
	hooks := filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", d.result(t).ID, "hooks")
	for _, want := range []string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=http.proxy", "GIT_CONFIG_KEY_1=core.hooksPath", "GIT_CONFIG_VALUE_1=" + hooks} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("%s missing from the worker's environment:\n%s", want, got)
		}
	}
}

func TestSandboxedJobWithAJobIDInACommitMessageFailsAndPushesNothing(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: rerun of j-20261111-111145-5a25"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobFailed || r.Detail != "leak guard: j-20261111-111145-5a25" {
		t.Fatalf("result %+v", r)
	}
	if len(p.gh.calls) != 0 || strings.Contains(p.remoteBranches(t), "chore/") {
		t.Fatalf("nothing may be pushed: gh %v, origin\n%s", p.gh.calls, p.remoteBranches(t))
	}
}

func TestSandboxedJobWhoseBranchNamesAJobIsNotPublished(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: x"
	if err := p.delegate("--agent", "agent", "--task", "j-20261111-111145-5a25 cleanup", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobFailed || !strings.HasPrefix(r.Detail, "leak guard: j-2026") || len(p.gh.calls) != 0 {
		t.Fatalf("result %+v, gh %v", r, p.gh.calls)
	}
}

func TestSandboxedJobWhoseSummaryNamesAWorktreeIsNotPublished(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"] = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: built in /home/alice/acme-worktrees/x"}`
	p.worker["COMMIT_MSG"] = "fix: x"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobFailed || r.Detail != "leak guard: -worktrees/" || len(p.gh.calls) != 0 {
		t.Fatalf("a pull request body that names a worktree: %+v", r)
	}
}

func TestWorkerPushOfALeakingCommitIsRejectedByThePrePushHook(t *testing.T) {
	p := newPublishEnv(t)
	pushLog := filepath.Join(t.TempDir(), "push.log")
	p.worker["RESULT"], p.worker["COMMIT_MSG"], p.worker["PUSH"], p.worker["PUSH_LOG"] = claudeDone, "fix: rerun of j-20261111-111145-5a25", "1", pushLog
	if err := p.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.remoteBranches(t), "chore/") {
		t.Fatalf("the push went through:\n%s", p.remoteBranches(t))
	}
	if got := readFile(t, pushLog); !strings.Contains(got, "leak guard: j-20261111-111145-5a25") {
		t.Fatalf("the hook did not name the match:\n%s", got)
	}
}

func TestWorkerPushOfACleanBranchGoesThroughAndStaysDone(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"], p.worker["PUSH"] = claudeDone, "fix: x\n\n"+cursorTrailer, "1"
	if err := p.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobDone || !strings.Contains(p.remoteBranches(t), r.Branch) {
		t.Fatalf("result %+v, origin\n%s", r, p.remoteBranches(t))
	}
	if got := gitShow(t, p.bare, "log", "-1", "--format=%B", r.Branch); got != "fix: x" {
		t.Fatalf("the pushed message %q", got)
	}
}

func TestWorkerThatPushedALeakEndsFailedWithThePullRequestURL(t *testing.T) {
	d := newDelegateEnv(t)
	const url = "https://github.com/acme/app/pull/5"
	d.worker["RESULT"], d.worker["COMMIT_MSG"] = `{"type":"result","subtype":"success","is_error":false,"result":"PR: `+url+`"}`, "fix: x\n\non acme-worktrees/x"
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobFailed || r.Detail != "leak guard: -worktrees/ ("+url+")" {
		t.Fatalf("result %+v", r)
	}
}

func TestWorkerThatPushedACleanBranchKeepsItsPullRequest(t *testing.T) {
	d := newDelegateEnv(t)
	const url = "https://github.com/acme/app/pull/5"
	d.worker["RESULT"], d.worker["COMMIT_MSG"] = `{"type":"result","subtype":"success","is_error":false,"result":"PR: `+url+`"}`, "fix: x"
	if err := d.delegate("--agent", "claude", "--task", "fix the build", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobPR || r.Detail != url {
		t.Fatalf("result %+v", r)
	}
}

func TestEveryWorkerBriefCarriesTheNoTracesSentence(t *testing.T) {
	const want = "no agent attribution and no job ids, branch names under `ki/` or worktree paths"
	for name, brief := range map[string]string{
		"plain":     ki.WrapTask("t", false),
		"draft":     ki.WrapTask("t", true),
		"edits":     ki.WrapEditsTask("t", []string{"go"}, false),
		"sandboxed": ki.WrapSandboxedTask("t"),
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("%s brief lacks the sentence:\n%s", name, brief)
		}
	}
}

func TestGuardSubcommand(t *testing.T) {
	d := newDelegateEnv(t)
	msg := filepath.Join(t.TempDir(), "msg")
	write(t, msg, "fix: x\n\n"+cursorTrailer+"\n")
	if err := d.delegate("guard", "--message-file", msg); err == nil || !strings.Contains(err.Error(), "leak guard: "+cursorTrailer) {
		t.Fatalf("a message with a trailer: %v", err)
	}
	if err := d.delegate("guard", "--strip", msg); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, msg); got != "fix: x\n" {
		t.Fatalf("stripped message %q", got)
	}
	if err := d.delegate("guard", "--message-file", msg); err != nil {
		t.Fatalf("a clean message: %v", err)
	}
	if err := d.delegate("guard", "--branch", "fix/12-cache-key"); err != nil {
		t.Fatalf("a clean branch: %v", err)
	}
	if err := d.delegate("guard", "--branch", "ki/j-1"); err == nil || !strings.Contains(err.Error(), "leak guard: ki/j-") {
		t.Fatalf("a ki branch: %v", err)
	}
	for _, bad := range [][]string{{}, {"--branch"}, {"--bogus", "x"}, {"--message-file", filepath.Join(t.TempDir(), "missing")}, {"--strip", filepath.Join(t.TempDir(), "missing")}} {
		if err := d.delegate(append([]string{"guard"}, bad...)...); err == nil {
			t.Errorf("guard %v accepted", bad)
		}
	}
}
