package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const headSHA = "0123456789abcdef0123456789abcdef01234567"

// pullGH answers gh the way GitHub does for one pull request and records
// every call, so a test can assert on what was written and as whom.
type pullGH struct {
	login, author string
	state         string
	merged, draft bool
	sha           string
	// mergeStates is mergeable_state on each read of the pull request; the last one repeats.
	mergeStates []string
	runs        []map[string]string
	statuses    []map[string]string
	runsTotal   int
	diff        string

	reads    int
	calls    []string
	tokens   map[string]bool
	reviews  []map[string]string
	merges   [][]string
	failPost error
}

func newPullGH() *pullGH {
	return &pullGH{
		login: "alice-work", author: "bob", state: "open", sha: headSHA, mergeStates: []string{"clean"}, tokens: map[string]bool{},
		runs: []map[string]string{{"name": "build", "status": "completed", "conclusion": "success"}},
		diff: "diff --git a/main.go b/main.go\n+++ b/main.go\n+func main() {}\n",
	}
}

func (f *pullGH) writes() int { return len(f.reviews) + len(f.merges) }

func (f *pullGH) run(_ context.Context, token string, stdin []byte, args ...string) ([]byte, error) {
	f.tokens[token] = true
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "pr" && args[1] == "merge" {
		f.merges = append(f.merges, args)
		return nil, nil
	}
	method, accept, path := "GET", "", ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-X":
			method = args[i+1]
			i++
		case "-H":
			accept = args[i+1]
			i++
		case "--input":
			i++
		default:
			path = args[i]
		}
	}
	switch {
	case path == "user":
		return []byte(fmt.Sprintf(`{"login":%q}`, f.login)), nil
	case method == "POST" && strings.HasSuffix(path, "/pulls/5/reviews"):
		if f.failPost != nil {
			return nil, f.failPost
		}
		var body map[string]string
		if err := json.Unmarshal(stdin, &body); err != nil {
			return nil, err
		}
		f.reviews = append(f.reviews, body)
		return []byte(`{"html_url":"https://github.com/work/app/pull/5#pullrequestreview-1"}`), nil
	case strings.HasSuffix(path, "/pulls/5") && strings.Contains(accept, "diff"):
		return []byte(f.diff), nil
	case strings.HasSuffix(path, "/pulls/5"):
		state := f.mergeStates[min(f.reads, len(f.mergeStates)-1)]
		f.reads++
		return json.Marshal(map[string]any{
			"title": "feat: add a thing", "state": f.state, "merged": f.merged, "draft": f.draft, "mergeable_state": state,
			"user": map[string]string{"login": f.author}, "head": map[string]string{"sha": f.sha, "ref": "feat/thing"},
			"base": map[string]string{"ref": "main"},
		})
	case strings.Contains(path, "/check-runs"):
		total := max(f.runsTotal, len(f.runs))
		return json.Marshal(map[string]any{"total_count": total, "check_runs": f.runs})
	case strings.Contains(path, "/status"):
		return json.Marshal(map[string]any{"total_count": len(f.statuses), "statuses": f.statuses})
	}
	return nil, fmt.Errorf("fake gh: unexpected call %v", args)
}

// approveEnv is a ki environment whose gh is the fake, acting as alice-work.
func approveEnv(t *testing.T, f *pullGH) *env {
	t.Helper()
	e := newEnv(t, "")
	e.app.GH = f.run
	e.app.GitHubToken = func(_ context.Context, user string) string { return "token-of-" + user }
	e.app.pollDelay = time.Nanosecond
	return e
}

func runApprove(t *testing.T, e *env, args ...string) (approveResult, error) {
	t.Helper()
	e.out.Reset()
	err := e.app.Run(t.Context(), append([]string{"approve", "work/app#5", "--json"}, args...))
	var res approveResult
	if e.out.Len() > 0 {
		if jerr := json.Unmarshal(e.out.Bytes(), &res); jerr != nil {
			t.Fatalf("not a result: %v\n%s", jerr, e.out.String())
		}
	}
	return res, err
}

func TestApproveWithoutReview(t *testing.T) {
	neutral := []map[string]string{
		{"name": "build", "status": "completed", "conclusion": "success"},
		{"name": "docs", "status": "completed", "conclusion": "skipped"},
		{"name": "lint", "status": "completed", "conclusion": "neutral"},
	}
	tests := []struct {
		name        string
		setup       func(*pullGH)
		args        []string
		wantErr     string
		wantAction  string
		wantReviews int
		wantMerges  int
	}{
		{name: "someone else's pull request is approved", wantAction: "approved", wantReviews: 1},
		{name: "success, skipped and neutral checks are ready", setup: func(f *pullGH) { f.runs = neutral }, wantAction: "approved", wantReviews: 1},
		{name: "no checks at all", setup: func(f *pullGH) { f.runs = nil }, wantAction: "approved", wantReviews: 1},
		{name: "approve and merge when mergeable", args: []string{"--merge"}, wantAction: "approved and merged", wantReviews: 1, wantMerges: 1},
		{
			name: "approved but not merged when GitHub says blocked", args: []string{"--merge"},
			setup:      func(f *pullGH) { f.mergeStates = []string{"clean", "blocked"} },
			wantAction: "approved", wantReviews: 1,
		},
		{
			name: "merge waits for GitHub to compute the state", args: []string{"--merge"},
			setup:      func(f *pullGH) { f.mergeStates = []string{"clean", "unknown", "unknown", "clean"} },
			wantAction: "approved and merged", wantReviews: 1, wantMerges: 1,
		},
		{
			name: "merge is skipped while the state stays unknown", args: []string{"--merge"},
			setup:      func(f *pullGH) { f.mergeStates = []string{"clean", "unknown"} },
			wantAction: "approved", wantReviews: 1,
		},
		{name: "own pull request is not approved", setup: func(f *pullGH) { f.author = "Alice-Work" }, wantErr: "own PR; approval needs another account"},
		{
			name: "own pull request is merged when the repo needs no review", args: []string{"--merge"},
			setup:      func(f *pullGH) { f.author = "alice-work" },
			wantAction: "merged", wantMerges: 1,
		},
		{
			name: "own pull request that needs a review is refused", args: []string{"--merge"},
			setup:   func(f *pullGH) { f.author, f.mergeStates = "alice-work", []string{"blocked"} },
			wantErr: "own PR; approval needs another account",
		},
		{
			name: "own pull request behind its base is refused", args: []string{"--merge"},
			setup:   func(f *pullGH) { f.author, f.mergeStates = "alice-work", []string{"behind"} },
			wantErr: "behind its base",
		},
		{name: "failing check", setup: func(f *pullGH) { f.runs[0]["conclusion"] = "failure" }, wantErr: "checks are failing: build"},
		{name: "cancelled check", setup: func(f *pullGH) { f.runs[0]["conclusion"] = "cancelled" }, wantErr: "checks are failing: build"},
		{name: "pending check run", setup: func(f *pullGH) { f.runs[0]["status"], f.runs[0]["conclusion"] = "in_progress", "" }, wantErr: "checks are not finished: build"},
		{
			name:    "pending commit status",
			setup:   func(f *pullGH) { f.statuses = []map[string]string{{"context": "ci/legacy", "state": "pending"}} },
			wantErr: "checks are not finished: ci/legacy",
		},
		{
			name:    "failing commit status",
			setup:   func(f *pullGH) { f.statuses = []map[string]string{{"context": "ci/legacy", "state": "error"}} },
			wantErr: "checks are failing: ci/legacy",
		},
		{name: "more checks than ki can read", setup: func(f *pullGH) { f.runsTotal = 250 }, wantErr: "more checks than ki can read"},
		{name: "draft", setup: func(f *pullGH) { f.draft = true }, wantErr: "is a draft"},
		{name: "closed", setup: func(f *pullGH) { f.state = "closed" }, wantErr: "is closed"},
		{name: "merged", setup: func(f *pullGH) { f.state, f.merged = "closed", true }, wantErr: "already merged"},
		{name: "dry run writes nothing", args: []string{"--dry-run", "--merge"}, wantAction: "none"},
		{
			name: "dry run of an own pull request", args: []string{"--dry-run", "--merge"},
			setup: func(f *pullGH) { f.author = "alice-work" }, wantAction: "none",
		},
		{
			name: "dry run still refuses", args: []string{"--dry-run"},
			setup: func(f *pullGH) { f.draft = true }, wantErr: "is a draft",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPullGH()
			if tt.setup != nil {
				tt.setup(f)
			}
			e := approveEnv(t, f)
			res, err := runApprove(t, e, tt.args...)
			if tt.wantErr != "" {
				if !isUserError(err) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want refusal %q, got %v", tt.wantErr, err)
				}
				if res.Action != "none" || res.Refusal == "" {
					t.Fatalf("refusal result: %+v", res)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if res.Action != orDefault(tt.wantAction, "none") {
				t.Errorf("action %q, want %q", res.Action, tt.wantAction)
			}
			if len(f.reviews) != tt.wantReviews || len(f.merges) != tt.wantMerges {
				t.Errorf("wrote %d reviews and %d merges, want %d and %d: %v", len(f.reviews), len(f.merges), tt.wantReviews, tt.wantMerges, f.calls)
			}
			if tt.wantErr != "" && f.writes() > 0 {
				t.Errorf("a refusal wrote to GitHub: %v", f.calls)
			}
			if strings.Contains(strings.Join(f.calls, "\n"), "--admin") {
				t.Errorf("--admin used: %v", f.calls)
			}
			for tok := range f.tokens {
				if tok != "token-of-alice-work" {
					t.Errorf("gh ran with %q, want the identity's token only", tok)
				}
			}
			if res.Identity != "work" || res.PR != "work/app#5" || res.Mode != "approve" || res.Job == "" {
				t.Errorf("result %+v", res)
			}
		})
	}
}

func TestApproveReviewBodiesStartWithLGTM(t *testing.T) {
	f := newPullGH()
	e := approveEnv(t, f)
	if _, err := runApprove(t, e, "--merge"); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews) != 1 {
		t.Fatalf("reviews %v", f.reviews)
	}
	r := f.reviews[0]
	if r["event"] != "APPROVE" || r["body"] != "LGTM" || r["commit_id"] != headSHA {
		t.Fatalf("review %v", r)
	}
	if len(f.merges) != 1 {
		t.Fatalf("merges %v", f.merges)
	}
	m := strings.Join(f.merges[0], " ")
	for _, want := range []string{"-R work/app", "--squash", "--delete-branch", "--match-head-commit " + headSHA} {
		if !strings.Contains(m, want) {
			t.Errorf("merge %q lacks %q", m, want)
		}
	}
}

func TestApproveSubmitFailureIsAnError(t *testing.T) {
	f := newPullGH()
	f.failPost = errors.New("HTTP 422")
	e := approveEnv(t, f)
	res, err := runApprove(t, e)
	if err == nil || !strings.Contains(err.Error(), "HTTP 422") || res.Action != "none" || res.Refusal == "" {
		t.Fatalf("%v %+v", err, res)
	}
}

func TestApproveRefusalsBeforeAnyCall(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no pull request", []string{}, "usage: ki approve"},
		{"not a pull request reference", []string{"work/app"}, "use a pull request URL or owner/repo#number"},
		{"two pull requests", []string{"work/app#5", "work/app#6"}, "one pull request at a time"},
		{"unknown option", []string{"work/app#5", "--frobnicate"}, "unknown option"},
		{"admin merge", []string{"work/app#5", "--merge", "--admin"}, "never bypasses branch protection"},
		{"bypass", []string{"work/app#5", "--bypass-branch-protection"}, "never bypasses branch protection"},
		{"comment without review", []string{"work/app#5", "--comment"}, "applies to --review"},
		{"agent without review", []string{"work/app#5", "--agent", "claude"}, "apply to --review"},
		{"bad timeout", []string{"work/app#5", "--review", "--timeout", "soon"}, "use a duration"},
		{"unmapped repo", []string{"someone/else#5"}, "not in projects.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newPullGH()
			e := approveEnv(t, f)
			err := e.app.Run(t.Context(), append([]string{"approve"}, tt.args...))
			if !isUserError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
			if len(f.calls) != 0 {
				t.Fatalf("gh was called: %v", f.calls)
			}
		})
	}
}

func TestApproveURLAndForce(t *testing.T) {
	f := newPullGH()
	f.login = "someone-else"
	e := approveEnv(t, f)
	e.out.Reset()
	// The fake serves /pulls/5 for any repo: an unmapped repo with --force acts as gh's active account.
	if err := e.app.Run(t.Context(), []string{"approve", "https://github.com/other/thing/pull/5/files", "--force", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "acting as gh active account") {
		t.Fatalf("output %s", e.out.String())
	}
	if !f.tokens[""] || len(f.tokens) != 1 {
		t.Fatalf("tokens %v: an unmapped repo runs with no token of ki's", f.tokens)
	}
	if !slices.Contains(f.calls, "api user") {
		t.Fatalf("the acting login is read from gh: %v", f.calls)
	}
}

func TestApproveNeedsTheIdentitysGHLogin(t *testing.T) {
	f := newPullGH()
	e := approveEnv(t, f)
	e.app.GitHubToken = func(context.Context, string) string { return "" }
	_, err := runApprove(t, e)
	if !isUserError(err) || !strings.Contains(err.Error(), "gh is not logged in as alice-work") || len(f.calls) != 0 {
		t.Fatalf("%v %v", err, f.calls)
	}
}

func TestApproveTextOutputAndLedger(t *testing.T) {
	f := newPullGH()
	e := approveEnv(t, f)
	e.out.Reset()
	if err := e.app.Run(t.Context(), []string{"approve", "work/app#5"}); err != nil {
		t.Fatal(err)
	}
	out := e.out.String()
	for _, want := range []string{"work/app#5 by bob, acting as work (approve)", "checks: 1 passed, 0 skipped", "action: approved", "pullrequestreview-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	jobs, err := ki.ReadLedger(filepath.Join(e.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ledger %+v %v", jobs, err)
	}
	if j := jobs[0]; j.Kind != ki.KindApprove || j.PR != "work/app#5" || j.Status != ki.ApproveApproved || !ki.ValidJobID(j.ID) {
		t.Fatalf("record %+v", j)
	}

	// A refusal and a dry run are recorded too.
	f.draft = true
	if _, err := runApprove(t, e); err == nil {
		t.Fatal("a draft must be refused")
	}
	f.draft = false
	if _, err := runApprove(t, e, "--dry-run"); err != nil {
		t.Fatal(err)
	}
	jobs, _ = ki.ReadLedger(filepath.Join(e.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	got := make([]string, 0, len(jobs))
	for _, j := range jobs {
		got = append(got, j.Status)
	}
	if !slices.Equal(got, []string{ki.ApproveApproved, ki.ApproveRefused, ki.ApproveDryRun}) {
		t.Fatalf("statuses %v", got)
	}

	e.out.Reset()
	if err := e.app.Run(t.Context(), []string{"delegate", "ls"}); err != nil {
		t.Fatal(err)
	}
	if ls := e.out.String(); !strings.Contains(ls, "work/app#5") || !strings.Contains(ls, "APPROVED") || !strings.Contains(ls, "REFUSED") {
		t.Fatalf("ls %s", ls)
	}
	e.out.Reset()
	if err := e.app.Run(t.Context(), []string{"delegate", "log"}); err != nil {
		t.Fatal(err)
	}
}

func TestApproveHelp(t *testing.T) {
	e := approveEnv(t, newPullGH())
	e.out.Reset()
	if err := e.app.Run(t.Context(), []string{"approve", "help"}); err != nil || !strings.Contains(e.out.String(), "usage: ki approve") {
		t.Fatalf("%v %s", err, e.out.String())
	}
	if !strings.Contains(usage(), "ki approve <pr>") {
		t.Fatal("ki help does not list approve")
	}
}

// reviewEnv is a delegate environment (fake agents on PATH, a repo whose
// origin is work/app) whose gh is the fake, with the pull request's head
// being the repo's own commit so no network is needed.
type reviewEnv struct {
	*delegateEnv
	gh *pullGH
}

func newReviewEnv(t *testing.T) *reviewEnv {
	t.Helper()
	d := newDelegateEnv(t)
	gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	head, err := exec.CommandContext(t.Context(), "git", "-C", d.repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	f := newPullGH()
	f.sha = strings.TrimSpace(string(head))
	d.app.GH = f.run
	d.app.GitHubToken = func(_ context.Context, user string) string { return "token-of-" + user }
	d.app.pollDelay = time.Nanosecond
	d.app.hostname = func() string { return "buildbox-7.example.net" }
	skill := "---\nname: ki-review\n---\n\nReview pull request $ARGUMENTS.\n\n## 1. Read the change\n\nRead it.\n\n## 3. Review\n\nCheck correctness.\n\n## 5. Show the draft and wait\n\nDo not post.\n"
	write(t, filepath.Join(d.harness, "skills", "ki-review", "SKILL.md"), skill)
	return &reviewEnv{delegateEnv: d, gh: f}
}

// claudeSays is Claude Code's result event for a final message.
func claudeSays(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": text, "session_id": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r *reviewEnv) approve(t *testing.T, args ...string) (approveResult, error) {
	t.Helper()
	r.out.Reset()
	err := r.app.Run(t.Context(), append([]string{"approve", "work/app#5", "--review", "--json", "--force"}, args...))
	var res approveResult
	if r.out.Len() > 0 {
		if jerr := json.Unmarshal(r.out.Bytes(), &res); jerr != nil {
			t.Fatalf("not a result: %v\n%s", jerr, r.out.String())
		}
	}
	return res, err
}

func (r *reviewEnv) jobs(t *testing.T) []ki.Result {
	t.Helper()
	jobs, err := ki.ReadLedger(filepath.Join(r.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil {
		t.Fatal(err)
	}
	return jobs
}

// tooling is what nothing posted to GitHub may contain.
var tooling = regexp.MustCompile(`(?i)\bki\b|j-\d{8}-\d{6}|worktree|buildbox|claude|codex|\.config`)

func TestApproveReviewApprove(t *testing.T) {
	r := newReviewEnv(t)
	headFile := filepath.Join(t.TempDir(), "head")
	r.worker["HEAD_FILE"] = headFile
	reviewer := "#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$ARGS_FILE\"\npwd > \"$CWD_FILE\"\ngit rev-parse HEAD > \"$HEAD_FILE\"\nprintf '%s\\n' \"$RESULT\"\n"
	installAgent(t, "claude", reviewer)
	r.worker["RESULT"] = claudeSays(t, "Looks right.\nVERDICT: APPROVE")

	res, err := r.approve(t, "--merge")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "approved and merged" || res.Verdict != "APPROVE" || res.Mode != "review" || res.ReviewJob == "" {
		t.Fatalf("result %+v", res)
	}
	if len(r.gh.reviews) != 1 || r.gh.reviews[0]["body"] != "LGTM" || r.gh.reviews[0]["event"] != "APPROVE" || len(r.gh.merges) != 1 {
		t.Fatalf("writes %v %v", r.gh.reviews, r.gh.merges)
	}

	args := r.args(t)
	for _, want := range []string{"--permission-mode", "manual", "--disallowedTools=Edit,MultiEdit,Write,NotebookEdit", "--print"} {
		if !slices.Contains(args, want) {
			t.Errorf("claude was not started with %q: %v", want, args)
		}
	}
	for _, bad := range []string{"auto", "acceptEdits", "--dangerously-skip-permissions"} {
		if slices.Contains(args, bad) {
			t.Errorf("review runs with %q", bad)
		}
	}
	brief := args[len(args)-1]
	for _, want := range []string{"Review pull request work/app#5: feat: add a thing", "+func main() {}", "## 1. Read the change", "VERDICT: CHANGES: <one-line reason>", "SECURITY:"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q", want)
		}
	}
	if strings.Contains(brief, "Show the draft") || strings.Contains(brief, "BLOCKED:") {
		t.Errorf("brief carries the posting step or the delegate contract:\n%s", brief)
	}
	if raw, err := os.ReadFile(headFile); err != nil || strings.TrimSpace(string(raw)) != r.gh.sha {
		t.Fatalf("the reviewer saw %q, want the pull request head %s (%v)", raw, r.gh.sha, err)
	}
	cwd := r.cwd(t)
	if !strings.Contains(cwd, "-worktrees") {
		t.Fatalf("review ran in %s", cwd)
	}
	if _, err := os.Stat(cwd); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the clean review worktree was not removed: %v", err)
	}

	jobs := r.jobs(t)
	if len(jobs) != 2 || jobs[0].Kind != ki.KindReview || jobs[0].PR != "work/app#5" || jobs[0].Status != ki.JobDone || jobs[1].Kind != ki.KindApprove || jobs[1].Status != ki.ApproveMerged {
		t.Fatalf("ledger %+v", jobs)
	}
	if jobs[0].Detail != "VERDICT: APPROVE" || jobs[0].ID != res.ReviewJob {
		t.Fatalf("review record %+v", jobs[0])
	}
	assertClean(t, r.gh)
}

func TestApproveReviewChanges(t *testing.T) {
	findings := "main.go:3 drops the error from Close, wrap it.\n" +
		"Checked on buildbox-7 in /x/app-worktrees/j-20261106-191817-3e0c.\n" +
		"The ki delegate job j-20261106-191817-3e0c also ran.\n" +
		"Add a test for the empty case."
	tests := []struct {
		name         string
		args         []string
		wantComments int
	}{
		{name: "nothing is posted by default"},
		{name: "--comment posts one COMMENT review", args: []string{"--comment"}, wantComments: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReviewEnv(t)
			r.worker["RESULT"] = claudeSays(t, findings+"\nVERDICT: CHANGES: the error is dropped")
			res, err := r.approve(t, append(tt.args, "--merge")...)
			if !isUserError(err) || !strings.Contains(err.Error(), "asks for changes: the error is dropped") {
				t.Fatalf("err %v", err)
			}
			if res.Verdict != "CHANGES: the error is dropped" || !strings.Contains(res.Findings, "drops the error from Close") {
				t.Fatalf("result %+v", res)
			}
			if len(r.gh.merges) != 0 || len(r.gh.reviews) != tt.wantComments {
				t.Fatalf("writes %v %v", r.gh.reviews, r.gh.merges)
			}
			if tt.wantComments == 1 {
				c := r.gh.reviews[0]
				if c["event"] != "COMMENT" || strings.HasPrefix(c["body"], "LGTM") || !strings.Contains(c["body"], "drops the error from Close") || !strings.Contains(c["body"], "Add a test") {
					t.Fatalf("comment %v", c)
				}
				if res.Action != "commented" {
					t.Fatalf("action %q", res.Action)
				}
			}
			assertClean(t, r.gh)
			if jobs := r.jobs(t); jobs[len(jobs)-1].Status != ki.ApproveChanges && tt.wantComments == 0 {
				t.Fatalf("ledger %+v", jobs)
			}
		})
	}
}

func TestApproveReviewSecuritySurface(t *testing.T) {
	tests := []struct {
		name string
		say  string
		diff string
		want string
	}{
		{
			name: "the review reports it",
			say:  "Fine.\nSECURITY: widens the trust policy of the deploy role in infra/roles.tf\nVERDICT: APPROVE",
			want: "LGTM, second reviewer recommended: widens the trust policy of the deploy role in infra/roles.tf",
		},
		{
			name: "ki catches a miss by the file name",
			say:  "Fine.\nVERDICT: APPROVE",
			diff: "diff --git a/infra/iam/main.tf b/infra/iam/main.tf\n+++ b/infra/iam/main.tf\n+x = 1\n",
			want: "LGTM, second reviewer recommended: security-sensitive change in infra/iam/main.tf",
		},
		{
			name: "ki catches a miss by an added line",
			say:  "Fine.\nVERDICT: APPROVE",
			diff: "diff --git a/net.tf b/net.tf\n+++ b/net.tf\n+  cidr_blocks = [\"0.0.0.0/0\"]\n",
			want: "LGTM, second reviewer recommended: security-sensitive change in net.tf",
		},
		{
			name: "a reason that names the tooling is replaced",
			say:  "Fine.\nSECURITY: ki delegate touched the secrets in buildbox-7\nVERDICT: APPROVE",
			want: "LGTM, second reviewer recommended: security-sensitive change",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReviewEnv(t)
			if tt.diff != "" {
				r.gh.diff = tt.diff
			}
			r.worker["RESULT"] = claudeSays(t, tt.say)
			res, err := r.approve(t, "--merge")
			if err != nil {
				t.Fatal(err)
			}
			if res.Action != "approved" || !strings.Contains(res.Note, "second reviewer recommended") {
				t.Fatalf("result %+v", res)
			}
			if len(r.gh.merges) != 0 || len(r.gh.reviews) != 1 || r.gh.reviews[0]["body"] != tt.want || r.gh.reviews[0]["event"] != "APPROVE" {
				t.Fatalf("writes %v %v, want body %q", r.gh.reviews, r.gh.merges, tt.want)
			}
			assertClean(t, r.gh)
		})
	}
}

func TestApproveReviewSecurityNeverMergesAnOwnPullRequest(t *testing.T) {
	r := newReviewEnv(t)
	r.gh.author = "alice-work"
	r.worker["RESULT"] = claudeSays(t, "SECURITY: opens ingress to the internet\nVERDICT: APPROVE")
	res, err := r.approve(t, "--merge")
	if !isUserError(err) || !strings.Contains(err.Error(), "second reviewer recommended, not merged") || res.Action != "none" {
		t.Fatalf("%v %+v", err, res)
	}
	if r.gh.writes() != 0 {
		t.Fatalf("wrote %v %v", r.gh.reviews, r.gh.merges)
	}
}

func TestApproveReviewOfAnOwnPullRequestMerges(t *testing.T) {
	r := newReviewEnv(t)
	r.gh.author = "alice-work"
	r.worker["RESULT"] = claudeSays(t, "VERDICT: APPROVE")
	res, err := r.approve(t, "--merge")
	if err != nil || res.Action != "merged" || len(r.gh.reviews) != 0 || len(r.gh.merges) != 1 {
		t.Fatalf("%v %+v %v %v", err, res, r.gh.reviews, r.gh.merges)
	}
	// Without --merge an own pull request is refused before any agent starts.
	r2 := newReviewEnv(t)
	r2.gh.author = "alice-work"
	if _, err := r2.approve(t); !isUserError(err) || !strings.Contains(err.Error(), "own PR; approval needs another account") {
		t.Fatalf("err %v", err)
	}
	if _, serr := os.Stat(r2.argsFile); serr == nil {
		t.Fatal("the agent ran for a pull request that cannot be approved")
	}
}

func TestApproveReviewDryRunStartsNothing(t *testing.T) {
	r := newReviewEnv(t)
	res, err := r.approve(t, "--dry-run", "--merge")
	if err != nil {
		t.Fatal(err)
	}
	if res.Action != "none" || !strings.Contains(res.Note, "review the head in a read-only worktree") || r.gh.writes() != 0 {
		t.Fatalf("%+v %v", res, r.gh.calls)
	}
	if _, serr := os.Stat(r.argsFile); serr == nil {
		t.Fatal("a dry run started the review agent")
	}
	if jobs := r.jobs(t); len(jobs) != 1 || jobs[0].Status != ki.ApproveDryRun {
		t.Fatalf("ledger %+v", jobs)
	}
}

func TestApproveReviewNeverApprovesAnUnfinishedReview(t *testing.T) {
	tests := []struct {
		name    string
		say     string
		agent   string
		wantErr string
	}{
		{name: "no verdict line", say: "Looks fine to me.", wantErr: "without a verdict"},
		{name: "verdict not last", say: "VERDICT: APPROVE\nbut wait", wantErr: "without a verdict"},
		{
			name: "the agent changed the worktree", say: "VERDICT: APPROVE", wantErr: "changed files in a read-only checkout",
			agent: "#!/bin/sh\ntouch fixed.txt\nprintf '%s\\n' \"$RESULT\"\n",
		},
		{
			name: "the agent moved the checkout", say: "VERDICT: APPROVE", wantErr: "moved the checkout off",
			agent: "#!/bin/sh\ngit commit -q --allow-empty -m x\nprintf '%s\\n' \"$RESULT\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReviewEnv(t)
			if tt.agent != "" {
				installAgent(t, "claude", tt.agent)
			}
			r.worker["RESULT"] = claudeSays(t, tt.say)
			_, err := r.approve(t, "--merge")
			if !isUserError(err) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err %v, want %q", err, tt.wantErr)
			}
			if r.gh.writes() != 0 {
				t.Fatalf("wrote %v %v", r.gh.reviews, r.gh.merges)
			}
		})
	}
}

func TestApproveReviewRefusesWhenTheHeadMoves(t *testing.T) {
	r := newReviewEnv(t)
	r.worker["RESULT"] = claudeSays(t, "VERDICT: APPROVE")
	moved := r.gh
	r.app.GH = func(ctx context.Context, token string, stdin []byte, args ...string) ([]byte, error) {
		// The review ran; from the next read of the pull request the head is a new commit.
		if moved.reads >= 1 {
			moved.sha = "ffffffffffffffffffffffffffffffffffffffff"
		}
		return moved.run(ctx, token, stdin, args...)
	}
	_, err := r.approve(t)
	if !isUserError(err) || !strings.Contains(err.Error(), "the head moved during the review") || r.gh.writes() != 0 {
		t.Fatalf("%v %v %v", err, r.gh.reviews, r.gh.merges)
	}
}

func TestApproveReviewAgentChoice(t *testing.T) {
	t.Run("an agent without read-only flags is refused", func(t *testing.T) {
		r := newReviewEnv(t)
		_, err := r.approve(t, "--agent", "agy")
		if !isUserError(err) || !strings.Contains(err.Error(), "no read-only flags for agy") || r.gh.writes() != 0 {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("a role skips providers that cannot run read-only", func(t *testing.T) {
		r := newReviewEnv(t)
		write(t, filepath.Join(r.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nroles:\n  execution:\n    strategy: ordered\n    providers:\n      - agent\n      - claude:sonnet\n")
		r.worker["RESULT"] = claudeSays(t, "VERDICT: APPROVE")
		res, err := r.approve(t)
		if err != nil || res.Action != "approved" {
			t.Fatalf("%v %+v", err, res)
		}
		if jobs := r.jobs(t); jobs[0].Provider != "claude" || jobs[0].Role != "execution" {
			t.Fatalf("ledger %+v", jobs)
		}
		if !strings.Contains(r.app.Stderr.(interface{ String() string }).String(), "agent skipped for the review") {
			t.Fatalf("stderr %s", r.app.Stderr.(interface{ String() string }).String())
		}
	})
	t.Run("a role with nothing read-only is refused", func(t *testing.T) {
		r := newReviewEnv(t)
		write(t, filepath.Join(r.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nroles:\n  execution:\n    strategy: ordered\n    providers:\n      - agent\n")
		_, err := r.approve(t)
		if !isUserError(err) || !strings.Contains(err.Error(), "roles.execution has no agent that can run read-only") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("codex runs in its read-only sandbox", func(t *testing.T) {
		r := newReviewEnv(t)
		r.worker["RESULT"], r.worker["LAST"] = codexEvents, "VERDICT: APPROVE"
		res, err := r.approve(t, "--agent", "codex")
		if err != nil || res.Action != "approved" {
			t.Fatalf("%v %+v", err, res)
		}
		args := r.args(t)
		if i := slices.Index(args, "--sandbox"); i < 0 || args[i+1] != "read-only" {
			t.Fatalf("codex args %v", args)
		}
	})
	t.Run("no checkout of the repository", func(t *testing.T) {
		r := newReviewEnv(t)
		r.app.Dir = t.TempDir()
		_, err := r.approve(t)
		if !isUserError(err) || !strings.Contains(err.Error(), "no checkout of work/app found") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("a head ki cannot fetch", func(t *testing.T) {
		r := newReviewEnv(t)
		r.gh.sha = "ffffffffffffffffffffffffffffffffffffffff"
		_, err := r.approve(t)
		if !isUserError(err) || !strings.Contains(err.Error(), "fetching the head of #5") || r.gh.writes() != 0 {
			t.Fatalf("err %v", err)
		}
	})
}

// installAgent replaces a fake agent on PATH with script.
func installAgent(t *testing.T, name, script string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, script)
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// assertClean fails when anything ki wrote to GitHub names the tooling, and
// when an approval does not start with LGTM.
func assertClean(t *testing.T, f *pullGH) {
	t.Helper()
	for _, r := range f.reviews {
		if tooling.MatchString(r["body"]) {
			t.Errorf("review body names the tooling: %q", r["body"])
		}
		if r["event"] == "APPROVE" && !strings.HasPrefix(r["body"], "LGTM") {
			t.Errorf("approval body %q does not start with LGTM", r["body"])
		}
		if r["event"] == "COMMENT" && strings.HasPrefix(r["body"], "LGTM") {
			t.Errorf("a comment says LGTM: %q", r["body"])
		}
	}
	for _, c := range f.merges {
		if tooling.MatchString(strings.Join(c, " ")) {
			t.Errorf("merge names the tooling: %v", c)
		}
	}
}
