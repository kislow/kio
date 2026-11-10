package ki

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestParsePRRef(t *testing.T) {
	tests := []struct {
		in      string
		want    PRRef
		wantErr bool
	}{
		{in: "work/app#5", want: PRRef{"work", "app", 5}},
		{in: " work/app#5 ", want: PRRef{"work", "app", 5}},
		{in: "https://github.com/work/app/pull/5", want: PRRef{"work", "app", 5}},
		{in: "https://github.com/work/app/pull/5/files", want: PRRef{"work", "app", 5}},
		{in: "https://github.com/work/app/pull/5#issuecomment-1", want: PRRef{"work", "app", 5}},
		{in: "5", wantErr: true},
		{in: "work/app", wantErr: true},
		{in: "work/app#0", wantErr: true},
		{in: "https://github.com/work/app/issues/5", wantErr: true},
		{in: "https://example.com/work/app/pull/5", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParsePRRef(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrPRRef) {
				t.Errorf("%q: err %v", tt.in, err)
			}
			continue
		}
		if err != nil || got != tt.want || got.String() == "" {
			t.Errorf("%q: %+v %v", tt.in, got, err)
		}
	}
}

func TestPullRequestRefusalAndMergeBlocker(t *testing.T) {
	open := PullRequest{State: "open", Author: "Bob"}
	tests := []struct {
		name        string
		pr          PullRequest
		refusal     string
		blocker     string
		ownedBy     string
		wantOwned   bool
		wantUnowned string
	}{
		{name: "open clean", pr: PullRequest{State: "OPEN", MergeableState: "clean"}},
		{name: "has hooks", pr: PullRequest{State: "open", MergeableState: "has_hooks"}},
		{name: "merged", pr: PullRequest{State: "closed", Merged: true}, refusal: "already merged", blocker: "(unknown)"},
		{name: "closed", pr: PullRequest{State: "closed"}, refusal: "is closed", blocker: "(unknown)"},
		{name: "draft", pr: PullRequest{State: "open", Draft: true, MergeableState: "draft"}, refusal: "is a draft", blocker: "is a draft"},
		{name: "blocked", pr: PullRequest{State: "open", MergeableState: "blocked"}, blocker: "blocked by branch rules"},
		{name: "behind", pr: PullRequest{State: "open", MergeableState: "behind"}, blocker: "behind its base"},
		{name: "dirty", pr: PullRequest{State: "open", MergeableState: "dirty"}, blocker: "conflicts"},
		{name: "unstable", pr: PullRequest{State: "open", MergeableState: "unstable"}, blocker: "failing checks"},
		{name: "unknown", pr: PullRequest{State: "open", MergeableState: "unknown"}, blocker: "not reported it mergeable yet (unknown)"},
		{name: "empty", pr: PullRequest{State: "open"}, blocker: "(unknown)"},
	}
	for _, tt := range tests {
		if got := tt.pr.Refusal(); (tt.refusal == "") != (got == "") || !strings.Contains(got, tt.refusal) {
			t.Errorf("%s: refusal %q, want %q", tt.name, got, tt.refusal)
		}
		if got := tt.pr.MergeBlocker(); (tt.blocker == "") != (got == "") || !strings.Contains(got, tt.blocker) {
			t.Errorf("%s: blocker %q, want %q", tt.name, got, tt.blocker)
		}
	}
	if !open.OwnedBy("bob") || open.OwnedBy("alice") || open.OwnedBy("") {
		t.Error("OwnedBy compares logins case-insensitively and never matches an empty one")
	}
}

func TestChecks(t *testing.T) {
	run := func(status, conclusion string) CheckRun {
		return CheckRun{Name: "c", Status: status, Conclusion: conclusion}
	}
	tests := []struct {
		name    string
		c       Checks
		want    string
		passed  int
		skipped int
	}{
		{name: "none", c: Checks{}},
		{name: "ready", c: Checks{Runs: []CheckRun{run("completed", "success"), run("completed", "skipped"), run("completed", "neutral")}, Statuses: []CommitStatus{{"s", "success"}}}, passed: 2, skipped: 2},
		{name: "failure", c: Checks{Runs: []CheckRun{run("completed", "failure")}}, want: "failing: c"},
		{name: "timed out", c: Checks{Runs: []CheckRun{run("completed", "timed_out")}}, want: "failing: c"},
		{name: "action required", c: Checks{Runs: []CheckRun{run("completed", "action_required")}}, want: "failing: c"},
		{name: "queued", c: Checks{Runs: []CheckRun{run("queued", "")}}, want: "not finished: c"},
		{name: "pending status", c: Checks{Statuses: []CommitStatus{{"s", "pending"}}}, want: "not finished: s"},
		{name: "failed status", c: Checks{Statuses: []CommitStatus{{"s", "failure"}}}, want: "failing: s"},
		{name: "failing beats pending", c: Checks{Runs: []CheckRun{run("queued", ""), run("completed", "failure")}}, want: "failing"},
		{name: "incomplete", c: Checks{Incomplete: true}, want: "more checks"},
	}
	for _, tt := range tests {
		got := tt.c.NotReady()
		if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
		if s := tt.c.Summarize(); s.Passed != tt.passed || s.Skipped != tt.skipped {
			t.Errorf("%s: summary %+v", tt.name, s)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name         string
		in           string
		wantErr      bool
		approve      bool
		reason       string
		security     string
		findingsPart string
	}{
		{name: "approve", in: "VERDICT: APPROVE", approve: true},
		{name: "approve after findings", in: "Fine.\n\nVERDICT: APPROVE\n\n", approve: true, findingsPart: "Fine."},
		{name: "changes", in: "main.go:3 drops an error.\nVERDICT: CHANGES: the error is dropped", reason: "the error is dropped", findingsPart: "main.go:3"},
		{name: "decorated", in: "**VERDICT: APPROVE**", approve: true},
		{name: "security lines", in: "- SECURITY: widens the role in iam.tf\nSECURITY: opens ingress\nVERDICT: APPROVE", approve: true, security: "widens the role in iam.tf; opens ingress"},
		{name: "empty", in: "", wantErr: true},
		{name: "no verdict", in: "Looks good", wantErr: true},
		{name: "verdict not last", in: "VERDICT: APPROVE\nthanks", wantErr: true},
		{name: "changes needs a reason", in: "VERDICT: CHANGES:", wantErr: true},
		{name: "changes without colon", in: "VERDICT: CHANGES", wantErr: true},
		{name: "lowercase", in: "verdict: approve", wantErr: true},
	}
	for _, tt := range tests {
		v, err := ParseVerdict(tt.in)
		if tt.wantErr {
			if !errors.Is(err, ErrNoVerdict) {
				t.Errorf("%s: err %v", tt.name, err)
			}
			if s, _ := ParseVerdictStatus(tt.in); s != JobFailed {
				t.Errorf("%s: status %s", tt.name, s)
			}
			continue
		}
		if err != nil || v.Approve != tt.approve || v.Reason != tt.reason || v.Security != tt.security || !strings.Contains(v.Findings, tt.findingsPart) {
			t.Errorf("%s: %+v %v", tt.name, v, err)
		}
		if s, d := ParseVerdictStatus(tt.in); s != JobDone || !strings.HasPrefix(d, "VERDICT: ") {
			t.Errorf("%s: status %s %q", tt.name, s, d)
		}
	}
}

func TestSecuritySurface(t *testing.T) {
	tests := []struct {
		name, diff, want string
	}{
		{name: "plain code", diff: "diff --git a/main.go b/main.go\n+++ b/main.go\n+func main() {}\n"},
		{name: "iam directory", diff: "diff --git a/infra/iam/main.tf b/infra/iam/main.tf\n+x\n", want: "infra/iam/main.tf"},
		{name: "oidc file", diff: "diff --git a/oidc_trust.tf b/oidc_trust.tf\n+x\n", want: "oidc_trust.tf"},
		{name: "secrets file", diff: "diff --git a/deploy/secrets.yaml b/deploy/secrets.yaml\n+x\n", want: "deploy/secrets.yaml"},
		{name: "ingress file", diff: "diff --git a/charts/web/ingress.yaml b/charts/web/ingress.yaml\n+x\n", want: "ingress.yaml"},
		{name: "added open cidr", diff: "diff --git a/net.tf b/net.tf\n+++ b/net.tf\n+ cidr = \"0.0.0.0/0\"\n", want: "net.tf"},
		{name: "added trust policy", diff: "diff --git a/r.tf b/r.tf\n+  assume_role_policy = data.x.json\n", want: "r.tf"},
		{name: "removed line is not a grant", diff: "diff --git a/net.tf b/net.tf\n- cidr = \"0.0.0.0/0\"\n"},
		{name: "a name that only contains the word", diff: "diff --git a/secretsmanager.go b/secretsmanager.go\n+x\n"},
	}
	for _, tt := range tests {
		got := SecuritySurface(tt.diff)
		if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestScrubTooling(t *testing.T) {
	text := "main.go:3 drops the error.\nran the ki delegate job\nsee j-20261106-191817-3e0c\nin /x/app-worktrees/j\nrun by claude\nhost buildbox-7 said so\nUse a context.\n.config/ki/jobs holds it"
	got := ScrubTooling(text, []string{"buildbox-7", "ab"})
	if got != "main.go:3 drops the error.\nUse a context." {
		t.Fatalf("got %q", got)
	}
	if MentionsTooling("a cursor over rows, kilobytes, skills", nil) {
		t.Error("ordinary words must not match")
	}
	if MentionsTooling("the ab tag", []string{"ab"}) {
		t.Error("a term shorter than four characters is ignored")
	}
}

func TestApproveBody(t *testing.T) {
	if ApproveBody("") != "LGTM" || ApproveBody("iam") != "LGTM, second reviewer recommended: iam" {
		t.Fatal("approve bodies start with LGTM")
	}
}

func TestReadOnlyArgs(t *testing.T) {
	claude, ok := ReadOnlyArgs("/usr/local/bin/claude")
	if !ok || !slices.Contains(claude, "--disallowedTools=Edit,MultiEdit,Write,NotebookEdit") || slices.Contains(claude, "--dangerously-skip-permissions") {
		t.Fatalf("claude %v", claude)
	}
	for _, a := range claude {
		if strings.HasPrefix(a, "--allowedTools=Bash(") && !strings.HasPrefix(a, "--allowedTools=Bash(git ") {
			t.Errorf("a review may only run git reads: %s", a)
		}
	}
	if codex, ok := ReadOnlyArgs("codex"); !ok || !slices.Equal(codex, []string{"--sandbox", "read-only"}) {
		t.Fatalf("codex %v", codex)
	}
	for _, agent := range []string{"agy", "agent", "cursor-agent", "sh"} {
		if ReadOnlySupported(agent) {
			t.Errorf("%s has no known read-only flags", agent)
		}
	}
}

func TestReviewProcedureAndBrief(t *testing.T) {
	skill := "---\nname: x\n---\n\nintro\n\n## 1. Read\n\nread it\n\n## 4. Verify\n\nverify\n\n## 5. Show\n\nnever\n"
	if got := ReviewProcedure(skill); !strings.HasPrefix(got, "## 1. Read") || !strings.HasSuffix(got, "verify") || strings.Contains(got, "never") {
		t.Fatalf("procedure %q", got)
	}
	if got := ReviewProcedure("## 1. Only\n\nbody"); got != "## 1. Only\n\nbody" {
		t.Fatalf("no step 5: %q", got)
	}
	if got := ReviewProcedure("plain text\n"); got != "plain text" {
		t.Fatalf("no steps: %q", got)
	}
	ref, pr := PRRef{"work", "app", 5}, PullRequest{Title: "feat: x", HeadRef: "feat/x", BaseRef: "main", HeadSHA: "abc"}
	small := ReviewBrief(ref, pr, "diff --git a/f b/f\n", "## 1. Read")
	if !strings.Contains(small, "```diff\ndiff --git a/f b/f") || !strings.HasSuffix(small, "when anything has to change first") {
		t.Fatalf("brief %s", small)
	}
	big := ReviewBrief(ref, pr, strings.Repeat("x", MaxReviewDiff+1), "## 1. Read")
	if strings.Contains(big, "```diff") || !strings.Contains(big, "git diff origin/main...HEAD") {
		t.Fatal("a diff over the limit is read from the checkout")
	}
}
