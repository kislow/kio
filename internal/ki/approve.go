package ki

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Kinds a ledger record can have besides a plain delegated job: `ki approve`
// writes one record per run and, with --review, one for the review agent.
const (
	KindApprove = "approve"
	KindReview  = "review"
)

// What a `ki approve` run did, as its ledger record's status.
const (
	ApproveApproved  = "APPROVED"
	ApproveMerged    = "MERGED"
	ApproveCommented = "COMMENTED"
	ApproveChanges   = "CHANGES"
	ApproveRefused   = "REFUSED"
	ApproveDryRun    = "DRY_RUN"
)

// PRRef is a pull request: owner/repo#N.
type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

func (r PRRef) String() string { return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number) }

var (
	prShort = regexp.MustCompile(`^([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#([1-9][0-9]*)$`)
	prLink  = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)(?:[/?#].*)?$`)
)

// ErrPRRef is returned for an argument that is neither a PR URL nor owner/repo#N.
var ErrPRRef = errors.New("invalid pull request")

// ParsePRRef reads a PR URL or owner/repo#N.
func ParsePRRef(arg string) (PRRef, error) {
	arg = strings.TrimSpace(arg)
	m := prLink.FindStringSubmatch(arg)
	if m == nil {
		m = prShort.FindStringSubmatch(arg)
	}
	if m == nil {
		return PRRef{}, fmt.Errorf("%w %q: use a pull request URL or owner/repo#number", ErrPRRef, arg)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return PRRef{}, fmt.Errorf("%w %q: use a pull request URL or owner/repo#number", ErrPRRef, arg)
	}
	return PRRef{Owner: m[1], Repo: m[2], Number: n}, nil
}

// PullRequest is what approval decisions read from GitHub.
type PullRequest struct {
	Title          string
	State          string
	Merged         bool
	Draft          bool
	Author         string
	HeadSHA        string
	HeadRef        string
	BaseRef        string
	MergeableState string
}

// Refusal says why a PR cannot be approved or merged at all, "" when it can.
func (p PullRequest) Refusal() string {
	switch {
	case p.Merged:
		return "the pull request is already merged"
	case !strings.EqualFold(p.State, "open"):
		return "the pull request is closed"
	case p.Draft:
		return "the pull request is a draft"
	}
	return ""
}

// OwnedBy reports whether login wrote the PR; GitHub refuses a self-approval.
func (p PullRequest) OwnedBy(login string) bool {
	return login != "" && strings.EqualFold(p.Author, login)
}

// MergeBlocker says why GitHub would not merge the PR right now without
// bypassing a rule, "" when it would. The mergeable state already folds in
// required reviews, required checks and conflicts, so ki never reads branch
// protection itself and never asks to bypass it.
func (p PullRequest) MergeBlocker() string {
	switch p.MergeableState {
	case "clean", "has_hooks":
		return ""
	case "blocked":
		return "GitHub reports it blocked by branch rules"
	case "behind":
		return "the branch is behind its base"
	case "dirty":
		return "the branch has conflicts with its base"
	case "unstable":
		return "GitHub reports failing checks"
	case "draft":
		return "the pull request is a draft"
	}
	return "GitHub has not reported it mergeable yet (" + orUnknown(p.MergeableState) + ")"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// CheckRun is one check run on the head commit.
type CheckRun struct {
	Name       string
	Status     string
	Conclusion string
}

// CommitStatus is one legacy commit status on the head commit.
type CommitStatus struct {
	Context string
	State   string
}

// Checks are everything GitHub reports on a head commit.
type Checks struct {
	Runs     []CheckRun
	Statuses []CommitStatus
	// Incomplete is true when GitHub has more than ki read, so "all green" cannot be shown.
	Incomplete bool
}

// Summary counts the checks the way the JSON result shows them.
type Summary struct {
	Passed  int      `json:"passed"`
	Skipped int      `json:"skipped"`
	Pending []string `json:"pending,omitempty"`
	Failing []string `json:"failing,omitempty"`
}

// Summarize sorts every check into passed, skipped, pending or failing.
// Success, skipped and neutral count as ready; pending is not ready.
func (c Checks) Summarize() Summary {
	s := Summary{}
	for _, r := range c.Runs {
		switch {
		case r.Status != "completed":
			s.Pending = append(s.Pending, r.Name)
		case r.Conclusion == "success":
			s.Passed++
		case r.Conclusion == "skipped" || r.Conclusion == "neutral":
			s.Skipped++
		default:
			s.Failing = append(s.Failing, r.Name)
		}
	}
	for _, st := range c.Statuses {
		switch st.State {
		case "success":
			s.Passed++
		case "pending":
			s.Pending = append(s.Pending, st.Context)
		default:
			s.Failing = append(s.Failing, st.Context)
		}
	}
	return s
}

// NotReady says why the checks do not allow an approval, "" when they do.
func (c Checks) NotReady() string {
	if c.Incomplete {
		return "there are more checks than ki can read, so it cannot show them all passed"
	}
	s := c.Summarize()
	switch {
	case len(s.Failing) > 0:
		return "checks are failing: " + strings.Join(s.Failing, ", ")
	case len(s.Pending) > 0:
		return "checks are not finished: " + strings.Join(s.Pending, ", ")
	}
	return ""
}

// Verdict is what a review agent concluded.
type Verdict struct {
	Approve bool
	// Reason is the one-line reason of a CHANGES verdict.
	Reason string
	// Security is what the review says touches a security surface, "" when nothing does.
	Security string
	// Findings is the review text before the verdict line.
	Findings string
}

// ErrNoVerdict means the final message did not end in a VERDICT line.
var ErrNoVerdict = errors.New("the final message does not end in VERDICT: APPROVE or VERDICT: CHANGES: <reason>")

var (
	verdictLine  = regexp.MustCompile(`^VERDICT:[ \t]*(APPROVE|CHANGES:[ \t]*(\S.*))$`)
	securityLine = regexp.MustCompile(`(?m)^[-*\s]*SECURITY:[ \t]*(\S.*)$`)
)

// ParseVerdict reads a review's final message. Only its last non-empty line
// counts, so a verdict quoted inside the findings never decides.
func ParseVerdict(last string) (Verdict, error) {
	lines := strings.Split(strings.TrimSpace(last), "\n")
	end := strings.Trim(strings.TrimSpace(lines[len(lines)-1]), "*`")
	m := verdictLine.FindStringSubmatch(strings.TrimSpace(end))
	if m == nil {
		return Verdict{}, ErrNoVerdict
	}
	v := Verdict{Findings: strings.TrimSpace(strings.Join(lines[:len(lines)-1], "\n"))}
	if m[1] == "APPROVE" {
		v.Approve = true
	} else {
		v.Reason = strings.TrimSpace(m[2])
	}
	var found []string
	for _, s := range securityLine.FindAllStringSubmatch(v.Findings, -1) {
		found = append(found, strings.TrimSpace(s[1]))
	}
	v.Security = strings.Join(found, "; ")
	return v, nil
}

// ParseVerdictStatus is ParseStatus for a review job: DONE with the verdict
// line, or FAILED when there is none.
func ParseVerdictStatus(last string) (status, detail string) {
	v, err := ParseVerdict(last)
	if err != nil {
		return JobFailed, "no VERDICT line in the final message"
	}
	if v.Approve {
		return JobDone, "VERDICT: APPROVE"
	}
	return JobDone, "VERDICT: CHANGES: " + v.Reason
}

var (
	diffHeader = regexp.MustCompile(`^diff --git a/(.+) b/(.+)$`)
	// securityPath is a file whose name says it holds access, network or secret configuration.
	securityPath = regexp.MustCompile(`(?i)(^|/)(iam|oidc|sso|waf|rbac|secrets?|network-?polic(y|ies)|security-?groups?|ingress|bucket-?polic(y|ies)|assume-?role|trust-?polic(y|ies))([-_./]|$)`)
	// securityAdded is a line a change adds that grants access, opens the network or holds a secret.
	securityAdded = regexp.MustCompile(`(?i)^\+.*(assume_role_policy|aws_iam_|aws_security_group|aws_s3_bucket_policy|aws_wafv2|oidc|kind:\s*(NetworkPolicy|Secret|Ingress)\b|0\.0\.0\.0/0)`)
)

// SecuritySurface finds IAM, OIDC, SSO, bucket policy, network ingress or
// secret changes in a unified diff, by file name and by added line. It backs
// up the review agent's own SECURITY line: a missed one must not become a
// plain approval. The result names the file, "" when nothing matched.
func SecuritySurface(diff string) string {
	file := ""
	for _, line := range strings.Split(diff, "\n") {
		if m := diffHeader.FindStringSubmatch(line); m != nil {
			file = m[2]
			if securityPath.MatchString(file) {
				return "security-sensitive change in " + file
			}
			continue
		}
		if file != "" && !strings.HasPrefix(line, "+++") && securityAdded.MatchString(line) {
			return "security-sensitive change in " + file
		}
	}
	return ""
}

// tooling matches words that would tell a reader an agent harness wrote the
// text: the tool, its jobs, worktrees and the agents it starts.
var tooling = regexp.MustCompile(`(?i)\bki\b|\bki-[a-z]|j-\d{8}-\d{6}-(?:[0-9a-f]{4}){1,2}|\bworktrees?\b|\b(claude|codex|cursor-agent|agy)\b|\.config/ki`)

// ScrubTooling drops the lines of text that mention the tooling or one of
// terms (a host name, a worktree path), so nothing posted to GitHub names
// where or how it was written.
func ScrubTooling(text string, terms []string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if !MentionsTooling(line, terms) {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// MentionsTooling reports whether s names the tooling or one of terms.
func MentionsTooling(s string, terms []string) bool {
	if tooling.MatchString(s) {
		return true
	}
	low := strings.ToLower(s)
	for _, t := range terms {
		if t = strings.ToLower(strings.TrimSpace(t)); len(t) >= 4 && strings.Contains(low, t) {
			return true
		}
	}
	return false
}

// ApproveBody is the review body of an approval: LGTM, and for a change on a
// security surface the second-reviewer note, so an approval never reads as a
// full sign-off there.
func ApproveBody(security string) string {
	if security == "" {
		return "LGTM"
	}
	return "LGTM, second reviewer recommended: " + security
}

// ReadOnlyArgs are the flags that start an agent unable to change anything,
// and false for an agent ki knows no read-only flags for. Claude Code runs
// without a permission prompt answer, so a tool outside the allow-list is
// denied; Codex runs in its read-only sandbox.
func ReadOnlyArgs(agent string) ([]string, bool) {
	switch filepath.Base(agent) {
	case "claude":
		tools := []string{
			"Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git grep:*)", "Bash(git status:*)", "Bash(git ls-files:*)",
			"mcp__ki-kb__get_context", "mcp__ki-kb__decisions_for", "mcp__ki-kb__search",
		}
		args := make([]string, 0, 3+len(tools))
		args = append(args, "--permission-mode", "manual", "--disallowedTools=Edit,MultiEdit,Write,NotebookEdit")
		for _, tool := range tools {
			args = append(args, "--allowedTools="+tool)
		}
		return args, true
	case "codex":
		return []string{"--sandbox", "read-only"}, true
	}
	return nil, false
}

// ReadOnlySupported reports whether ReadOnlyArgs knows the agent.
func ReadOnlySupported(agent string) bool {
	_, ok := ReadOnlyArgs(agent)
	return ok
}

// reviewRules end every review brief, in place of the delegate status lines.
const reviewRules = `You cannot ask questions, and you cannot change anything: do not edit, create or delete files, do not post to GitHub, do not approve or merge. You have no network; the pull request and its diff are above, and the checkout in your working directory is the head commit.

If the change touches IAM, OIDC, SSO, bucket policies, network ingress or secrets, add a line ` + "`SECURITY: <what and where>`" + ` before the verdict.

Your final message is the review: each finding with its path and line, what is wrong, why it matters and the fix. As its last line, and nothing after it, exactly one of:
- VERDICT: APPROVE   when nothing has to change before this merges
- VERDICT: CHANGES: <one-line reason>   when anything has to change first`

// MaxReviewDiff is how much of a diff goes into the brief; a bigger one is
// read from the checkout instead.
const MaxReviewDiff = 200 << 10

// ReviewProcedure is the part of the ki-review skill a headless reviewer
// follows: reading the change, loading context, reviewing and verifying, not
// the drafting and posting, which ki does itself.
func ReviewProcedure(skill string) string {
	start := strings.Index(skill, "## 1.")
	if start < 0 {
		return strings.TrimSpace(skill)
	}
	end := strings.Index(skill[start:], "## 5.")
	if end < 0 {
		return strings.TrimSpace(skill[start:])
	}
	return strings.TrimSpace(skill[start : start+end])
}

// ReviewBrief is the task a review agent gets: the pull request, its diff
// and the review procedure, then the verdict contract.
func ReviewBrief(ref PRRef, pr PullRequest, diff, procedure string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Review pull request %s: %s\n\nIt merges %s into %s; the head commit is %s.\n\n", ref, pr.Title, pr.HeadRef, pr.BaseRef, pr.HeadSHA)
	b.WriteString("Follow this review procedure. Where it tells you to run `gh`, use the diff below instead, and treat the base branch as `origin/" + pr.BaseRef + "` in this checkout.\n\n")
	b.WriteString(procedure)
	b.WriteString("\n\nWrite the findings as a reviewer talking to a colleague: direct, no praise padding, no emoji, and never mention the knowledge base, ki, brains, decision records or any agent.\n\n")
	if len(diff) > MaxReviewDiff {
		fmt.Fprintf(&b, "The diff is too large to include; read it with `git diff origin/%s...HEAD`.\n\n", pr.BaseRef)
	} else {
		b.WriteString("The diff:\n\n```diff\n" + diff + "\n```\n\n")
	}
	b.WriteString(reviewRules)
	return b.String()
}
