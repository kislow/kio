package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const (
	cursorDone     = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: fixed the build","session_id":"366e","usage":{"inputTokens":8,"outputTokens":2,"cacheReadTokens":1}}`
	cursorNoStatus = `{"type":"result","subtype":"success","is_error":false,"result":"Fixed the build and pushed it.\nThe branch is up to date.","session_id":"366e"}`
	cursorClaimsPR = `{"type":"result","subtype":"success","is_error":false,"result":"PR: https://github.com/work/app/pull/99","session_id":"366e"}`
	newPullURL     = "https://github.com/work/app/pull/7"
)

// ghStub answers the gh calls publishing makes and records them.
type ghStub struct {
	lock   sync.Mutex
	calls  [][]string
	tokens []string
	open   string // URL `gh pr list` finds, "" for none
}

func (g *ghStub) run(_ context.Context, token string, _ []byte, args ...string) ([]byte, error) {
	g.lock.Lock()
	defer g.lock.Unlock()
	g.calls, g.tokens = append(g.calls, args), append(g.tokens, token)
	switch {
	case args[0] == "pr" && args[1] == "list":
		if g.open == "" {
			return []byte("[]"), nil
		}
		return json.Marshal([]map[string]string{{"url": g.open}})
	case args[0] == "pr" && args[1] == "create":
		return []byte(newPullURL + "\n"), nil
	}
	return nil, errors.New("unexpected gh " + strings.Join(args, " "))
}

func (g *ghStub) called(verb string) [][]string {
	g.lock.Lock()
	defer g.lock.Unlock()
	var out [][]string
	for _, c := range g.calls {
		if c[0] == "pr" && c[1] == verb {
			out = append(out, c)
		}
	}
	return out
}

type publishEnv struct {
	*delegateEnv
	gh   *ghStub
	bare string
}

// newPublishEnv is a delegate env whose origin is a real bare repository that
// takes the pushes to https://github.com/work/app, with main pushed, and a stubbed gh.
func newPublishEnv(t *testing.T) *publishEnv {
	t.Helper()
	d := newDelegateEnv(t)
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, d.repo, "branch", "-M", "main")
	gitIn(t, d.repo, "init", "--bare", "-q", bare)
	gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	// Only pushes go to the bare repo: origin's URL stays the GitHub one the project is mapped by.
	gitIn(t, d.repo, "config", "url."+bare+".pushInsteadOf", "https://github.com/work/app")
	gitIn(t, d.repo, "push", "-q", "origin", "main")
	gitIn(t, d.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, d.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	p := &publishEnv{delegateEnv: d, gh: &ghStub{}, bare: bare}
	d.app.GH = p.gh.run
	return p
}

func (p *publishEnv) remoteBranches(t *testing.T) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", p.bare, "branch", "--list").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func (p *publishEnv) cursor(extra ...string) error {
	return p.delegate(append([]string{"--agent", "agent", "--task", "fix the build", "--force", "--json", "--quiet"}, extra...)...)
}

func TestSandboxedWorkerCommitsAreCommittedByTheWorkerAndPublishedByKi(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix(build): repair the cache key"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobPR || r.Detail != newPullURL {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(p.remoteBranches(t), r.Branch) {
		t.Fatalf("%s was not pushed:\n%s", r.Branch, p.remoteBranches(t))
	}
	creates := p.gh.called("create")
	if len(creates) != 1 {
		t.Fatalf("gh pr create calls: %v", creates)
	}
	args := creates[0]
	for flag, want := range map[string]string{"-R": "work/app", "--head": r.Branch, "--title": "fix(build): repair the cache key", "--base": "main", "--body": "fixed the build"} {
		if i := slices.Index(args, flag); i < 0 || args[i+1] != want {
			t.Errorf("%s: want %q in %v", flag, want, args)
		}
	}
	if slices.Contains(args, "--draft") || slices.Contains(args, "--force") {
		t.Errorf("a ready PR, never forced: %v", args)
	}
}

func TestSandboxedWorkerPullRequestIsADraftWhenAsked(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: x"
	if err := p.cursor("--draft"); err != nil {
		t.Fatal(err)
	}
	if creates := p.gh.called("create"); len(creates) != 1 || !slices.Contains(creates[0], "--draft") {
		t.Fatalf("gh pr create calls: %v", creates)
	}
}

func TestSandboxedWorkerWithoutCommitsPublishesNothing(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"] = cursorDone
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobDone || r.Detail != "fixed the build" {
		t.Fatalf("result %+v", r)
	}
	if len(p.gh.calls) != 0 || strings.Contains(p.remoteBranches(t), "chore/") {
		t.Fatalf("nothing to publish, yet gh %v and origin\n%s", p.gh.calls, p.remoteBranches(t))
	}
}

func TestSandboxedWorkerClaimOfAPullRequestIsNeverTrusted(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"] = cursorClaimsPR
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobFailed || !strings.Contains(r.Detail, "made no commits") {
		t.Fatalf("a PR claimed without commits: %+v", r)
	}
}

func TestSandboxedWorkerClaimOfAPullRequestIsReplacedByTheOneKiOpened(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorClaimsPR, "fix: x"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobPR || r.Detail != newPullURL {
		t.Fatalf("result %+v", r)
	}
}

func TestSandboxedWorkerUncommittedChangesAreNotPublished(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"], p.worker["DIRTY_FILE"] = cursorDone, "fix: x", "notes.txt"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobBlocked || r.Detail != "uncommitted changes in "+r.Worktree {
		t.Fatalf("result %+v", r)
	}
	if len(p.gh.calls) != 0 || strings.Contains(p.remoteBranches(t), "chore/") {
		t.Fatalf("nothing may be pushed: gh %v, origin\n%s", p.gh.calls, p.remoteBranches(t))
	}
}

func TestSandboxedWorkerThatReportedFailureIsNotPublished(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = `{"type":"result","subtype":"success","is_error":false,"result":"FAILED: tests do not pass"}`, "wip"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobFailed || r.Detail != "tests do not pass" || len(p.gh.calls) != 0 {
		t.Fatalf("result %+v, gh %v", r, p.gh.calls)
	}
}

func TestSandboxedHereJobPushesToItsBranchAndKeepsTheExistingPullRequest(t *testing.T) {
	p := newPublishEnv(t)
	gitIn(t, p.repo, "switch", "-q", "-c", "feat/7-thing")
	gitIn(t, p.repo, "push", "-q", "origin", "feat/7-thing")
	p.gh.open = "https://github.com/work/app/pull/3"
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: more"
	if err := p.cursor("--here"); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobPR || r.Detail != p.gh.open {
		t.Fatalf("result %+v", r)
	}
	if len(p.gh.called("create")) != 0 {
		t.Fatalf("the open PR stays: %v", p.gh.calls)
	}
	tip, err := exec.CommandContext(t.Context(), "git", "-C", p.bare, "rev-parse", "feat/7-thing").Output()
	if err != nil {
		t.Fatal(err)
	}
	if head, _ := exec.CommandContext(t.Context(), "git", "-C", p.repo, "rev-parse", "HEAD").Output(); string(head) != string(tip) {
		t.Fatalf("origin has %s, the worktree %s", tip, head)
	}
}

func TestSandboxedHereJobOnTheDefaultBranchIsNotPublished(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: x"
	if err := p.cursor("--here"); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobBlocked || !strings.Contains(r.Detail, "main itself") {
		t.Fatalf("result %+v", r)
	}
}

func TestSandboxedWorkerPushUsesTheJobsIdentityToken(t *testing.T) {
	p := newPublishEnv(t)
	fakeGH(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: x"
	if err := p.delegate("--agent", "agent", "--task", "t", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobPR {
		t.Fatalf("result %+v", r)
	}
	for _, tok := range p.gh.tokens {
		if tok != "tok" {
			t.Fatalf("gh ran with token %q, want the identity's", tok)
		}
	}
}

func TestSandboxedWorkerWithoutAStatusLineEndsFromItsExitCode(t *testing.T) {
	for _, tc := range []struct {
		name, exit, status string
	}{
		{"clean exit", "0", ki.JobDone},
		{"failing exit", "3", ki.JobFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPublishEnv(t)
			p.worker["RESULT"], p.worker["EXIT"] = cursorNoStatus, tc.exit
			if err := p.cursor(); err != nil {
				t.Fatal(err)
			}
			r := p.result(t)
			if r.Status != tc.status {
				t.Fatalf("result %+v", r)
			}
			if tc.status == ki.JobDone && r.Detail != "Fixed the build and pushed it." {
				t.Fatalf("the summary is the final message's first line: %q", r.Detail)
			}
		})
	}
}

func TestWorkerThatKeepsRunningAfterItsResultIsEndedWithinTheGrace(t *testing.T) {
	p := newPublishEnv(t)
	old := killedWorkerGrace
	killedWorkerGrace = 100 * time.Millisecond
	t.Cleanup(func() { killedWorkerGrace = old })
	p.app.ResultGrace = 200 * time.Millisecond
	p.worker["RESULT"], p.worker["COMMIT_MSG"], p.worker["SLEEP_END"] = cursorNoStatus, "fix: x", "30"
	started := time.Now()
	if err := p.cursor("--stall", "10m"); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("the hung worker was left running for %v", took)
	}
	if r := p.result(t); r.Status != ki.JobPR || r.Detail != newPullURL {
		t.Fatalf("result %+v", r)
	}
}

func TestSandboxedContractTellsTheWorkerKiPublishes(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"] = cursorDone
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	brief := strings.Join(p.args(t), "\n")
	for _, want := range []string{"Commit only; ki publishes", "Do not run `git push` or `gh pr create`", "never trusted", "- DONE: <one-line summary>", "- BLOCKED:", "- FAILED:"} {
		if !strings.Contains(brief, want) {
			t.Errorf("%q missing from the brief:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, "- PR:") || strings.Contains(brief, "gh pr create` without") {
		t.Errorf("a sandboxed worker is never asked for a PR:\n%s", brief)
	}
}

func TestUnsandboxedContractStillAsksForThePullRequest(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"] = claudeDone
	if err := p.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	brief := strings.Join(p.args(t), "\n")
	if !strings.Contains(brief, "- PR: <url>") || strings.Contains(brief, "ki publishes") {
		t.Fatalf("brief:\n%s", brief)
	}
}

func TestSandboxedBrowserCheckIsSkippedAndNoted(t *testing.T) {
	for _, tc := range []struct {
		task, note string
	}{
		{"fix the build, then check it in the browser with Playwright", ki.BrowserSkipped},
		{"fix the build", ""},
	} {
		t.Run(tc.note, func(t *testing.T) {
			p := newPublishEnv(t)
			p.worker["RESULT"] = cursorDone
			if err := p.delegate("--agent", "agent", "--task", tc.task, "--force", "--json", "--quiet"); err != nil {
				t.Fatal(err)
			}
			if r := p.result(t); r.Note != tc.note {
				t.Fatalf("note %q, want %q", r.Note, tc.note)
			}
			if !strings.Contains(strings.Join(p.args(t), "\n"), "skip it and say so") {
				t.Errorf("the contract tells the worker to skip a browser check")
			}
		})
	}
}

func TestUnsandboxedWorkerIsNeverPublishedByKi(t *testing.T) {
	p := newPublishEnv(t)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: committed"}`, "fix: x"
	if err := p.delegate("--task", "t", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobDone || len(p.gh.calls) != 0 || strings.Contains(p.remoteBranches(t), "chore/") {
		t.Fatalf("result %+v, gh %v", r, p.gh.calls)
	}
}

func TestSandboxedIssueJobPullRequestClosesItsIssue(t *testing.T) {
	p := newPublishEnv(t)
	f := newFakeGH()
	f.addIssue("work", "app", 7, "fix(ci): the build fails", "The tests fail.", "OPEN")
	p.issueServer(t, f)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix(ci): repair the build"
	if err := p.delegate("--agent", "agent", "--issue", "7", "--force", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if r := p.result(t); r.Status != ki.JobPR || r.Issue != "work/app#7" {
		t.Fatalf("result %+v", r)
	}
	creates := p.gh.called("create")
	if len(creates) != 1 {
		t.Fatalf("gh pr create calls: %v", creates)
	}
	if i := slices.Index(creates[0], "--body"); i < 0 || creates[0][i+1] != "Closes work/app#7\n\nfixed the build" {
		t.Fatalf("body in %v", creates[0])
	}
}

func TestPublishBodyWithoutStatusMarkerOrIssue(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  publishJob
		want string
	}{
		{"marker stripped", publishJob{last: "DONE: fixed it"}, "fixed it"},
		{"no marker kept", publishJob{last: "Fixed it.\nSecond line."}, "Fixed it.\nSecond line."},
		{"issue only", publishJob{last: "DONE:", issue: "work/app#7"}, "Closes work/app#7"},
		{"secret scrubbed", publishJob{last: "DONE: used s3cret", secrets: []string{"s3cret", ""}, issue: "a/b#1"}, "Closes a/b#1\n\nused " + tokenRedacted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.job.body(); got != tc.want {
				t.Fatalf("body %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSandboxedWorkerOriginGhCannotAddressIsNotPushed(t *testing.T) {
	p := newPublishEnv(t)
	gitIn(t, p.repo, "remote", "set-url", "origin", "git-alias:work/app.git")
	gitIn(t, p.repo, "config", "--unset", "url."+p.bare+".pushInsteadOf")
	gitIn(t, p.repo, "remote", "set-url", "--push", "origin", p.bare)
	p.worker["RESULT"], p.worker["COMMIT_MSG"] = cursorDone, "fix: x"
	if err := p.cursor(); err != nil {
		t.Fatal(err)
	}
	r := p.result(t)
	if r.Status != ki.JobFailed || !strings.Contains(r.Detail, "not pushed") {
		t.Fatalf("result %+v", r)
	}
	if out := p.remoteBranches(t); strings.Contains(out, r.Branch) {
		t.Fatalf("%s reached origin:\n%s", r.Branch, out)
	}
	if len(p.gh.calls) != 0 {
		t.Fatalf("gh calls %v", p.gh.calls)
	}
}
