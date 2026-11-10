package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// pullFetchTimeout bounds fetching a pull request's head: a whole branch can come with it.
const pullFetchTimeout = 60 * time.Second

// review is a finished review: the agent's verdict and where it came from.
type review struct {
	verdict ki.Verdict
	// security is what makes this a second-reviewer change, "" when nothing does.
	security string
	job      string
	// terms are strings of this run that must not appear in anything posted.
	terms []string
}

// reviewPull runs one read-only review agent on the head of the pull request
// in a fresh worktree and returns its verdict. Anything but a clean verdict
// is an error: the caller must never approve on a review that did not finish.
func (a *App) reviewPull(ctx context.Context, ap approveArgs, who *actor, c pullClient, pr ki.PullRequest) (review, error) {
	dir, err := a.approveCheckout(ctx, who, c.ref)
	if err != nil {
		return review{}, err
	}
	a.Dir = dir
	ra := ap.RunArgs
	ra.Agent = orDefault(ra.Agent, orDefault(a.Getenv("KI_AGENT"), "claude"))
	p, err := a.prepare(ctx, ra)
	if err != nil {
		return review{}, err
	}
	diff, err := c.Diff(ctx)
	if err != nil {
		return review{}, err
	}
	skill, err := readData(p.here.data, "skills/ki-review/SKILL.md")
	if err != nil {
		return review{}, err
	}
	da := delegateArgs{
		RunArgs: ra, Task: ki.ReviewBrief(c.ref, pr, diff, ki.ReviewProcedure(skill)), Role: ap.Role,
		AgentGiven: ap.AgentGiven, Timeout: ap.Timeout, Quiet: ap.Quiet,
		brief: func(task string) string { return task }, parse: ki.ParseVerdictStatus,
		Title: "Review " + c.ref.String(), readOnly: true, kind: ki.KindReview, pr: c.ref.String(),
	}
	id := ki.NewJobID(time.Now())
	role, try, err := a.providers(ctx, da, p.machine, id)
	if err != nil {
		return review{}, err
	}
	if try, err = a.readOnlyProviders(try, role); err != nil {
		return review{}, err
	}
	wt, err := a.addReviewWorktree(ctx, p.here.root, id, c.ref.Number, pr)
	if err != nil {
		return review{}, err
	}
	r, err := a.attemptAll(ctx, p, da, try, role, id, wt, "")
	if err != nil {
		return review{}, err
	}
	switch r.Status {
	case ki.JobLaunchFailed:
		return review{}, fail("%s did not start a review: %s", r.Provider, r.Detail)
	case ki.JobDone:
	default:
		return review{}, fail("the review of %s ended without a verdict (%s %s); its worktree is kept at %s", c.ref, r.Status, r.Detail, wt)
	}
	if err := a.reviewLeftNothing(ctx, wt, pr.HeadSHA); err != nil {
		return review{}, fail("%v; its worktree is kept at %s", err, wt)
	}
	v, err := ki.ParseVerdict(r.Last)
	if err != nil {
		return review{}, fail("the review of %s: %v", c.ref, err)
	}
	if out, err := gitOut(ctx, p.here.root, "worktree", "remove", "--force", wt); err != nil {
		fmt.Fprintf(a.Stderr, "ki: could not remove the review worktree %s: %s\n", wt, oneLine(out))
	}
	sec := v.Security
	if sec == "" {
		sec = ki.SecuritySurface(diff)
	}
	return review{verdict: v, security: sec, job: r.ID, terms: []string{wt}}, nil
}

// readOnlyProviders keeps the providers of a role that ki can start read-only:
// a review must not be able to change the checkout it reads. An agent named
// outright is not filtered; starting it fails with the reason.
func (a *App) readOnlyProviders(try []ki.Provider, role string) ([]ki.Provider, error) {
	if role == "" {
		return try, nil
	}
	var ok []ki.Provider
	for _, p := range try {
		if ki.ReadOnlySupported(p.Agent) {
			ok = append(ok, p)
			continue
		}
		fmt.Fprintf(a.Stderr, "ki: %s skipped for the review, ki knows no read-only flags for it\n", p.Name())
	}
	if len(ok) == 0 {
		return nil, fail("roles.%s has no agent that can run read-only (claude or codex); name one with --agent claude", role)
	}
	return ok, nil
}

// approveCheckout finds the checkout the review worktree is cut from: the
// current one when it is this repo, else the project's, found as ki mecca finds it.
func (a *App) approveCheckout(ctx context.Context, who *actor, ref ki.PRRef) (string, error) {
	if root := git(ctx, a.Dir, "rev-parse", "--show-toplevel"); root != "" && originIs(ctx, root, ref) {
		return root, nil
	}
	for _, name := range who.session.Projects {
		dir, err := a.meccaCheckout(ctx, name, who.cfg)
		if err == nil && originIs(ctx, dir, ref) {
			return dir, nil
		}
	}
	return "", fail("no checkout of %s/%s found: run ki approve inside one, or map it under checkouts: in %s", ref.Owner, ref.Repo, ki.MeccaConfigFile)
}

func originIs(ctx context.Context, dir string, ref ki.PRRef) bool {
	got, err := projects.NormalizeRepo(git(ctx, dir, "remote", "get-url", "origin"))
	return err == nil && got == strings.ToLower(fmt.Sprintf("https://github.com/%s/%s", ref.Owner, ref.Repo))
}

// addReviewWorktree checks the pull request's head commit out, detached, in a
// new worktree next to the repo. The head is fetched by the pull request's own
// ref, so a fork's branch works too, and must be the commit that was read.
func (a *App) addReviewWorktree(ctx context.Context, root, id string, number int, pr ki.PullRequest) (string, error) {
	if _, err := gitOut(ctx, root, "cat-file", "-e", pr.HeadSHA+"^{commit}"); err != nil {
		if out, err := gitTimed(ctx, root, pullFetchTimeout, "fetch", "origin", fmt.Sprintf("refs/pull/%d/head", number), "--quiet"); err != nil {
			return "", fail("fetching the head of #%d: %s", number, oneLine(out))
		}
		if _, err := gitOut(ctx, root, "cat-file", "-e", pr.HeadSHA+"^{commit}"); err != nil {
			return "", fail("the head of #%d is no longer %s; run it again", number, pr.HeadSHA)
		}
	}
	if err := fetchDefault(ctx, root, pr.BaseRef); err != nil {
		fmt.Fprintf(a.Stderr, "ki: could not fetch %s (%s); the review sees the last fetch of it\n", pr.BaseRef, err)
	}
	unlock, err := lockWorktrees(ctx, root)
	if err != nil {
		return "", err
	}
	defer unlock()
	dir := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-worktrees", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
	}
	if out, err := gitOut(ctx, root, "worktree", "add", "--quiet", "--detach", dir, pr.HeadSHA); err != nil {
		return "", fail("git worktree add %s at %s: %s", dir, pr.HeadSHA, oneLine(out))
	}
	return dir, nil
}

// reviewLeftNothing checks the read-only promise after the fact, for the
// agents whose flags ki cannot fully trust: the worktree is still the head
// commit, with no changed and no new file.
func (a *App) reviewLeftNothing(ctx context.Context, dir, sha string) error {
	head, err := gitOut(ctx, dir, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(head) != sha {
		return errors.New("the review moved the checkout off the pull request's head, so its verdict is not trusted")
	}
	status, err := gitOut(ctx, dir, "status", "--porcelain")
	if err != nil || strings.TrimSpace(status) != "" {
		return errors.New("the review changed files in a read-only checkout, so its verdict is not trusted")
	}
	return nil
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	return gitTimed(ctx, dir, commandTimeout, args...)
}

// gitTimed is git with an error: the combined output and whether it failed.
func gitTimed(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary, arguments from ki itself
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
