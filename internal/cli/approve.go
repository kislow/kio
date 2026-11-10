package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

const approveUsage = `usage: ki approve <pr> [--review [--comment]] [--merge] [--dry-run] [--json] [--force]
                  [--role execution | --agent claude|codex] [--model M] [--brain B]... [--timeout 30m] [--quiet]

<pr> is a pull request URL or owner/repo#N. ki acts as the identity projects.yaml gives the repo,
with that identity's gh token only.

Without --review it submits one APPROVE review ("LGTM") right away. With --review a read-only
agent reviews the head commit in a fresh worktree first, and only VERDICT: APPROVE approves;
VERDICT: CHANGES prints the findings and stops (--comment also posts them as one COMMENT review).
A review that touches IAM, OIDC, SSO, bucket policies, network ingress or secrets approves with
"LGTM, second reviewer recommended: <reason>" and never merges.

--merge squash-merges and deletes the branch once GitHub reports the pull request mergeable.
Your own pull request cannot be approved: with --merge it is merged when the repo does not
require a review, else refused. ki never bypasses branch protection.

A closed, merged or draft pull request, and one whose checks are not all successful, skipped or
neutral, is refused. --dry-run reads from GitHub and writes nothing; it does not start the review.`

type approveArgs struct {
	ki.RunArgs
	Ref        ki.PRRef
	Review     bool
	Comment    bool
	Merge      bool
	DryRun     bool
	JSON       bool
	Quiet      bool
	Role       string
	AgentGiven bool
	Timeout    time.Duration
}

var bypassFlag = regexp.MustCompile(`^--(admin|bypass[a-z-]*|override[a-z-]*|force-merge)(=.*)?$`)

func parseApproveArgs(args []string) (approveArgs, error) {
	ap := approveArgs{Timeout: delegateTimeout}
	var rest []string
	var pr string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case bypassFlag.MatchString(a):
			return ap, fail("ki approve never bypasses branch protection: %s is not available", a)
		case a == "--review":
			ap.Review = true
		case a == "--comment":
			ap.Comment = true
		case a == "--merge":
			ap.Merge = true
		case a == "--dry-run":
			ap.DryRun = true
		case a == "--json":
			ap.JSON = true
		case a == "--quiet":
			ap.Quiet = true
		case a == "--role" && i+1 < len(args):
			ap.Role = args[i+1]
			i++
		case a == "--timeout" && i+1 < len(args):
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				return ap, fail("--timeout %q: use a duration like 30m", args[i+1])
			}
			ap.Timeout = d
			i++
		case a == "--agent" && i+1 < len(args):
			ap.AgentGiven = true
			rest = append(rest, a, args[i+1])
			i++
		case (a == "--model" || a == "--brain") && i+1 < len(args):
			rest = append(rest, a, args[i+1])
			i++
		case a == "--force":
			rest = append(rest, a)
		case strings.HasPrefix(a, "-"):
			return ap, fail("unknown option %s\n%s", a, approveUsage)
		case pr == "":
			pr = a
		default:
			return ap, fail("one pull request at a time\n%s", approveUsage)
		}
	}
	if pr == "" {
		return ap, fail("%s", approveUsage)
	}
	ref, err := ki.ParsePRRef(pr)
	if err != nil {
		return ap, fail("%v", err)
	}
	ap.Ref = ref
	ap.RunArgs = ki.ParseRunArgs(rest, "")
	switch {
	case ap.Comment && !ap.Review:
		return ap, fail("--comment posts a review's findings and applies to --review")
	case !ap.Review && (ap.AgentGiven || ap.Role != "" || ap.Model != "" || len(ap.Brains) > 0):
		return ap, fail("--agent, --role, --model and --brain choose the review agent and apply to --review")
	}
	return ap, nil
}

// actor is the account ki acts as on GitHub: the repo's identity and its token.
type actor struct {
	name    string
	login   string
	token   string
	session ki.Session
	cfg     *projects.Config
}

// approveResult is the one object --json prints.
type approveResult struct {
	PR        string      `json:"pr"`
	URL       string      `json:"url"`
	Author    string      `json:"author"`
	Identity  string      `json:"identity"`
	Mode      string      `json:"mode"`
	Merge     bool        `json:"merge"`
	DryRun    bool        `json:"dry_run"`
	Checks    *ki.Summary `json:"checks"`
	Verdict   string      `json:"verdict,omitempty"`
	Action    string      `json:"action"`
	Refusal   string      `json:"refusal,omitempty"`
	Note      string      `json:"note,omitempty"`
	Findings  string      `json:"findings,omitempty"`
	ReviewJob string      `json:"review_job,omitempty"`
	Job       string      `json:"job"`
	// status is the ledger record's status.
	status string
}

func (r *approveResult) refuse(format string, args ...any) error {
	r.status, r.Refusal = ki.ApproveRefused, fmt.Sprintf(format, args...)
	return fail("%s: %s", r.PR, r.Refusal)
}

func (a *App) approve(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		fmt.Fprintln(a.Stdout, approveUsage)
		return nil
	}
	ap, err := parseApproveArgs(args)
	if err != nil {
		return err
	}
	who, err := a.approveActor(ctx, ap)
	if err != nil {
		return err
	}
	c := pullClient{run: a.ghRunner(), token: who.token, ref: ap.Ref}
	if who.login == "" {
		if who.login, err = c.Login(ctx); err != nil {
			return err
		}
	}
	res := &approveResult{
		PR: ap.Ref.String(), URL: c.pullURL(), Identity: who.name, Mode: "approve", Merge: ap.Merge, DryRun: ap.DryRun,
		Action: "none", Job: ki.NewJobID(time.Now()),
	}
	if ap.Review {
		res.Mode = "review"
	}
	started := time.Now()
	runErr := a.approveRun(ctx, ap, who, c, res)
	return a.approveFinish(ap, res, started, runErr)
}

// approveActor resolves who acts on this repo: the identity projects.yaml gives
// it, as `ki` resolves it, and that identity's gh token.
func (a *App) approveActor(ctx context.Context, ap approveArgs) (*actor, error) {
	h, err := a.harness()
	if err != nil {
		return nil, err
	}
	dir, _, err := a.configDir(h)
	if err != nil {
		return nil, err
	}
	cfg, err := projects.Load(filepath.Join(dir, ki.ProjectsFile))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", filepath.Join(dir, ki.ProjectsFile), err)
	}
	s := ki.Resolve(cfg, fmt.Sprintf("https://github.com/%s/%s", ap.Ref.Owner, ap.Ref.Repo), nil)
	if s.Unmapped && !ap.Force {
		return nil, fail("%s/%s is not in projects.yaml, so ki cannot choose its gh account; add it there, or pass --force to act as gh's active account", ap.Ref.Owner, ap.Ref.Repo)
	}
	m, err := a.machine(cfg)
	if err != nil {
		return nil, err
	}
	who := &actor{name: "gh active account", session: s, cfg: cfg}
	if s.Identity == nil {
		return who, nil
	}
	if !ap.Force && !m.Carries(s.Identity.Name) {
		return nil, fail("this repo belongs to identity %q, which this machine does not carry; add it with `ki setup machine %s ...` (or pass --force)",
			s.Identity.Name, strings.Join(append(m.Identities, s.Identity.Name), " "))
	}
	tokenFor := a.GitHubToken
	if tokenFor == nil {
		tokenFor = ghToken
	}
	who.token = tokenFor(ctx, s.Identity.GitHubUser)
	if who.token == "" && !ap.Force {
		return nil, fail("gh is not logged in as %s; run `gh auth login`", s.Identity.GitHubUser)
	}
	who.name = s.Identity.Name
	if who.token != "" {
		who.login = s.Identity.GitHubUser
	}
	return who, nil
}

// settledPull is the pull request once GitHub has worked out its mergeable
// state, which it does lazily after a push or a review.
func (a *App) settledPull(ctx context.Context, c pullClient, pr ki.PullRequest) (ki.PullRequest, error) {
	delay := a.pollDelay
	if delay == 0 {
		delay = time.Second
	}
	for i := 0; i < 5 && pr.MergeableState == "unknown"; i++ {
		select {
		case <-ctx.Done():
			return pr, fmt.Errorf("waiting for GitHub: %w", ctx.Err())
		case <-time.After(delay):
		}
		var err error
		if pr, err = c.Pull(ctx); err != nil {
			return pr, err
		}
	}
	return pr, nil
}

func (a *App) approveRun(ctx context.Context, ap approveArgs, who *actor, c pullClient, res *approveResult) error {
	pr, err := c.Pull(ctx)
	if err != nil {
		return err
	}
	res.Author = pr.Author
	if why := pr.Refusal(); why != "" {
		return res.refuse("%s", why)
	}
	checks, err := c.Checks(ctx, pr.HeadSHA)
	if err != nil {
		return err
	}
	sum := checks.Summarize()
	res.Checks = &sum
	if why := checks.NotReady(); why != "" {
		return res.refuse("%s", why)
	}
	own := pr.OwnedBy(who.login)
	if own {
		if !ap.Merge {
			return res.refuse("own PR; approval needs another account")
		}
		if pr, err = a.settledPull(ctx, c, pr); err != nil {
			return err
		}
		if err := ownMergeable(res, pr); err != nil {
			return err
		}
	}
	if ap.DryRun {
		res.status, res.Note = ki.ApproveDryRun, dryRunPlan(ap, own)
		return nil
	}

	rv := review{}
	if ap.Review {
		if rv, err = a.reviewPull(ctx, ap, who, c, pr); err != nil {
			return err
		}
		res.ReviewJob = rv.job
		if !rv.verdict.Approve {
			return a.reviewChanges(ctx, ap, c, pr, rv, res)
		}
		res.Verdict = "APPROVE"
		if pr, err = a.sameHead(ctx, c, pr, res); err != nil {
			return err
		}
	}
	security := a.cleanSecurity(rv.security, rv.terms)
	if own {
		return a.mergeOwn(ctx, c, pr, security, res)
	}
	return a.approveAndMerge(ctx, ap, c, pr, security, res)
}

// ownMergeable refuses an own pull request GitHub would not merge. Blocked is
// the rule GitHub states as "review required": only another account fixes it.
func ownMergeable(res *approveResult, pr ki.PullRequest) error {
	if pr.MergeableState == "blocked" {
		return res.refuse("own PR; approval needs another account (%s)", pr.MergeBlocker())
	}
	if why := pr.MergeBlocker(); why != "" {
		return res.refuse("own PR; not mergeable: %s", why)
	}
	return nil
}

func dryRunPlan(ap approveArgs, own bool) string {
	var steps []string
	if ap.Review {
		steps = append(steps, "review the head in a read-only worktree, stopping on VERDICT: CHANGES")
	}
	if own {
		steps = append(steps, "squash-merge and delete the branch (own PR: no approval)")
	} else {
		steps = append(steps, "submit an APPROVE review starting with LGTM")
		if ap.Merge {
			steps = append(steps, "squash-merge and delete the branch if GitHub reports it mergeable")
		}
	}
	return "dry run, nothing written: would " + strings.Join(steps, ", then ")
}

// reviewChanges ends a run whose review asked for changes: the findings are
// printed, and posted as one COMMENT review only with --comment.
func (a *App) reviewChanges(ctx context.Context, ap approveArgs, c pullClient, pr ki.PullRequest, rv review, res *approveResult) error {
	v := rv.verdict
	res.status, res.Verdict, res.Findings = ki.ApproveChanges, "CHANGES: "+v.Reason, v.Findings
	if ap.Comment {
		url, err := c.Review(ctx, pr.HeadSHA, "COMMENT", a.commentBody(v, rv.terms))
		if err != nil {
			return err
		}
		res.Action, res.URL, res.status = "commented", orDefault(url, res.URL), ki.ApproveCommented
	}
	return fail("%s: the review asks for changes: %s", res.PR, v.Reason)
}

// commentBody is the findings as the owner's own words: lines that name the
// tooling are dropped, and an empty remainder falls back to the reason.
func (a *App) commentBody(v ki.Verdict, terms []string) string {
	terms = append(terms, a.leakTerms()...)
	body := ki.ScrubTooling(v.Findings, terms)
	if body == "" {
		body = ki.ScrubTooling(v.Reason, terms)
	}
	if body == "" {
		body = "Changes needed before this merges."
	}
	return body
}

func (a *App) cleanSecurity(reason string, terms []string) string {
	if reason == "" {
		return ""
	}
	if ki.MentionsTooling(reason, append(terms, a.leakTerms()...)) {
		return "security-sensitive change"
	}
	return reason
}

// leakTerms are the strings that must not reach GitHub: where this machine and its jobs live.
func (a *App) leakTerms() []string {
	host := a.hostname
	if host == nil {
		host = func() string { h, _ := os.Hostname(); return h }
	}
	h := host()
	short, _, _ := strings.Cut(h, ".")
	return []string{h, short, a.jobsDir(), a.confDir()}
}

// sameHead re-reads the pull request after a review: what was reviewed must
// be what gets approved or merged.
func (a *App) sameHead(ctx context.Context, c pullClient, was ki.PullRequest, res *approveResult) (ki.PullRequest, error) {
	now, err := c.Pull(ctx)
	if err != nil {
		return was, err
	}
	if now.HeadSHA != was.HeadSHA {
		return was, res.refuse("the head moved during the review (%s to %s); run it again", short(was.HeadSHA), short(now.HeadSHA))
	}
	if why := now.Refusal(); why != "" {
		return was, res.refuse("%s", why)
	}
	return now, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// mergeOwn merges a pull request of the acting account, which GitHub will not
// let it approve; a security-surface change is left for a second reviewer.
func (a *App) mergeOwn(ctx context.Context, c pullClient, pr ki.PullRequest, security string, res *approveResult) error {
	if security != "" {
		return res.refuse("own PR; %s; second reviewer recommended, not merged", security)
	}
	pr, err := a.settledPull(ctx, c, pr)
	if err != nil {
		return err
	}
	if err := ownMergeable(res, pr); err != nil {
		return err
	}
	if err := c.Merge(ctx, pr.HeadSHA); err != nil {
		return err
	}
	res.Action, res.status = "merged", ki.ApproveMerged
	return nil
}

// approveAndMerge submits the approval and, with --merge, merges when GitHub
// says it is mergeable now. A change on a security surface is approved with
// the second-reviewer note and never merged.
func (a *App) approveAndMerge(ctx context.Context, ap approveArgs, c pullClient, pr ki.PullRequest, security string, res *approveResult) error {
	url, err := c.Review(ctx, pr.HeadSHA, "APPROVE", ki.ApproveBody(security))
	if err != nil {
		return err
	}
	res.Action, res.URL, res.status = "approved", orDefault(url, res.URL), ki.ApproveApproved
	if !ap.Merge {
		return nil
	}
	if security != "" {
		res.Note = "not merged: second reviewer recommended"
		return nil
	}
	// The approval changes what GitHub says about mergeability, so read it again.
	if pr, err = c.Pull(ctx); err != nil {
		return err
	}
	if pr, err = a.settledPull(ctx, c, pr); err != nil {
		return err
	}
	if why := pr.MergeBlocker(); why != "" {
		res.Note = "approved, not merged: " + why
		return nil
	}
	if err := c.Merge(ctx, pr.HeadSHA); err != nil {
		return err
	}
	res.Action, res.status = "approved and merged", ki.ApproveMerged
	return nil
}

// approveFinish prints the result, records the run in the job ledger and
// returns the run's error.
func (a *App) approveFinish(ap approveArgs, res *approveResult, started time.Time, runErr error) error {
	if runErr != nil && res.status == "" {
		res.status, res.Refusal = ki.JobFailed, oneLine(runErr.Error())
	}
	if err := a.printApprove(ap, res); err != nil {
		return err
	}
	detail := orDefault(res.Refusal, orDefault(res.Note, res.Action))
	rec := ki.Result{
		ID: res.Job, Kind: ki.KindApprove, PR: res.PR, Status: res.status, Detail: detail, Started: started,
		DurationS: time.Since(started).Seconds(),
	}
	if err := ki.AppendLedger(filepath.Join(a.jobsDir(), ki.LedgerFile), rec); err != nil {
		if runErr != nil {
			return runErr
		}
		return fmt.Errorf("recording %s: %w", res.Job, err)
	}
	return runErr
}

func (a *App) printApprove(ap approveArgs, res *approveResult) error {
	if ap.JSON {
		if err := json.NewEncoder(a.Stdout).Encode(res); err != nil {
			return fmt.Errorf("printing the result: %w", err)
		}
		return nil
	}
	fmt.Fprintf(a.Stdout, "%s by %s, acting as %s (%s)\n", res.PR, orDefault(res.Author, "unknown"), res.Identity, res.Mode)
	if c := res.Checks; c != nil {
		fmt.Fprintf(a.Stdout, "checks: %d passed, %d skipped\n", c.Passed, c.Skipped)
	}
	if res.Findings != "" {
		fmt.Fprintf(a.Stdout, "\n%s\n\n", res.Findings)
	}
	if res.Verdict != "" {
		fmt.Fprintf(a.Stdout, "verdict: %s\n", res.Verdict)
	}
	fmt.Fprintf(a.Stdout, "action: %s\n", res.Action)
	for _, s := range []string{res.Note, res.Refusal} {
		if s != "" {
			fmt.Fprintln(a.Stdout, s)
		}
	}
	if res.Action != "none" {
		fmt.Fprintln(a.Stdout, res.URL)
	}
	return nil
}
