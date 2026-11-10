package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// credentialHelper hands git the job's own GitHub token from GH_TOKEN, so a push
// is made as the job's identity and not as whichever account gh has active.
const credentialHelper = `!f() { echo username=x-access-token; echo "password=$GH_TOKEN"; }; f` // #nosec G101 -- a shell snippet that reads GH_TOKEN, no secret

// publishJob is what a sandboxed worker's commits become once it exited: ki
// pushes them and opens the pull request itself. Only a job the worker reported
// done is published, and the worker's own claim of a push or PR is never trusted:
// the status is what ki finds on the branch and on GitHub afterwards.
type publishJob struct {
	dir, branch, last string
	// issue is the issue the job works on, as owner/repo#N, "" for a plain task.
	issue string
	draft bool
	token string
	// secrets are scrubbed from the pull request body.
	secrets []string
}

// publish ends r as PR: <url> when the worktree's commits were pushed and have
// a pull request, leaves it DONE when there is nothing to publish, and ends it
// BLOCKED or FAILED, with the reason, when it cannot publish.
func (a *App) publish(ctx context.Context, p *prepared, r *ki.Result, j publishJob) {
	if r.Status != ki.JobDone && r.Status != ki.JobPR {
		return
	}
	claimed := r.Status == ki.JobPR
	status, detail := a.publishStatus(ctx, p, j, claimed)
	if status != "" {
		r.Status, r.Detail = status, detail
	}
}

// publishStatus is the status publishing leaves, "" to keep the worker's own.
func (a *App) publishStatus(ctx context.Context, p *prepared, j publishJob, claimed bool) (status, detail string) {
	env := ki.MergeEnv(a.Environ(), map[string]string{"GH_TOKEN": j.token, "GIT_TERMINAL_PROMPT": "0"})
	run := func(args ...string) (string, error) { return publishGit(ctx, j.dir, env, j.token != "", args...) }

	if dirty, err := run("status", "--porcelain"); err != nil {
		return ki.JobFailed, "reading the worktree: " + err.Error()
	} else if dirty != "" {
		return ki.JobBlocked, "uncommitted changes in " + j.dir
	}
	work := readProgress(ctx, p.here.root, j.dir, false)
	if len(work.Commits) == 0 {
		if claimed {
			return ki.JobFailed, "the worker reported a pull request but made no commits"
		}
		return "", ""
	}
	current, err := run("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ki.JobFailed, "cannot publish from a detached HEAD in " + j.dir
	}
	if current != j.branch {
		return ki.JobFailed, fmt.Sprintf("the worktree is on %s, not the job's branch %s", current, j.branch)
	}
	base, _ := run("symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	base = strings.TrimPrefix(base, "origin/")
	if base != "" && current == base {
		return ki.JobBlocked, fmt.Sprintf("the commits are on %s itself; ki publishes a branch, not %s", base, base)
	}
	pushURL, err := run("remote", "get-url", "--push", "origin")
	if err != nil {
		return ki.JobFailed, "reading origin: " + err.Error()
	}
	ahead, err := commitsAhead(run, pushURL, j.branch, len(work.Commits))
	if err != nil {
		return ki.JobFailed, err.Error()
	}
	if ahead == 0 {
		if claimed {
			return ki.JobFailed, "the worker reported a pull request but the branch has nothing new to publish"
		}
		return "", ""
	}
	// Parsed before the push so an origin gh cannot address fails with nothing on the remote.
	remote, _ := run("remote", "get-url", "origin")
	owner, repo, err := githubRepo(remote)
	if err != nil {
		return ki.JobFailed, "not pushed, origin is not a GitHub repository: " + err.Error()
	}
	if _, err := run("push", "origin", "HEAD:refs/heads/"+j.branch); err != nil {
		return ki.JobFailed, "git push " + j.branch + ": " + err.Error()
	}
	url, err := a.pullFor(ctx, j, owner+"/"+repo, base, work.Commits)
	if err != nil {
		return ki.JobFailed, "pushed " + j.branch + " but " + err.Error()
	}
	return ki.JobPR, url
}

// commitsAhead is how many commits the branch has that origin's copy lacks:
// work (its commits since it left the default branch) when origin has no such
// branch, none when origin already holds HEAD. When origin's tip is not known
// locally the push decides, so it counts as ahead.
func commitsAhead(run func(args ...string) (string, error), pushURL, branch string, work int) (int, error) {
	// The branch is looked up where the push goes, which a pushurl may make another place than origin's fetch URL.
	out, err := run("ls-remote", "--heads", pushURL, "refs/heads/"+branch)
	if err != nil {
		return 0, fmt.Errorf("reading origin: %w", err)
	}
	tip, _, _ := strings.Cut(out, "\t")
	if tip == "" {
		return work, nil
	}
	if n, err := run("rev-list", "--count", tip+"..HEAD"); err == nil {
		count, convErr := strconv.Atoi(n)
		if convErr == nil {
			return count, nil
		}
	}
	return max(work, 1), nil
}

// pullFor is the open pull request of the branch, opened with the first commit's
// subject and the worker's final message when there is none yet; an existing
// one is left as it is.
func (a *App) pullFor(ctx context.Context, j publishJob, repo, base string, commits []string) (string, error) {
	gh := a.ghRunner()
	raw, err := gh(ctx, j.token, nil, "pr", "list", "-R", repo, "--head", j.branch, "--state", "open", "--json", "url", "--limit", "1")
	if err != nil {
		return "", fmt.Errorf("could not look up its pull request: %w", err)
	}
	var open []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(raw, &open); err != nil {
		return "", fmt.Errorf("could not read the pull request list: %w", err)
	}
	if len(open) > 0 && open[0].URL != "" {
		return open[0].URL, nil
	}
	args := []string{"pr", "create", "-R", repo, "--head", j.branch, "--title", pullTitle(commits, j.branch), "--body", j.body()}
	if base != "" {
		args = append(args, "--base", base)
	}
	if j.draft {
		args = append(args, "--draft")
	}
	out, err := gh(ctx, j.token, nil, args...)
	if err != nil {
		return "", fmt.Errorf("could not open the pull request: %w", err)
	}
	url := ki.PullURL(string(out))
	if url == "" {
		return "", fmt.Errorf("gh pr create printed no pull request URL: %s", oneLine(string(out)))
	}
	return url, nil
}

// pullTitle is the first commit's subject (commits are `<sha> <subject>`, oldest first).
func pullTitle(commits []string, branch string) string {
	if len(commits) > 0 {
		if _, subject, ok := strings.Cut(commits[0], " "); ok && strings.TrimSpace(subject) != "" {
			return strings.TrimSpace(subject)
		}
	}
	return branch
}

// body is the worker's final message without its status marker and with the
// session's secrets removed, led by a closing reference when the job has an issue.
func (j publishJob) body() string {
	body := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(j.last), ki.JobDone+":"))
	for _, s := range j.secrets {
		if s != "" {
			body = strings.ReplaceAll(body, s, tokenRedacted)
		}
	}
	if j.issue != "" {
		body = "Closes " + j.issue + "\n\n" + body
	}
	return strings.TrimSpace(body)
}

// publishGit runs git in dir with env and returns its trimmed stdout; an error
// carries git's first line of stderr. withHelper makes git authenticate with
// the job's token.
func publishGit(ctx context.Context, dir string, env []string, withHelper bool, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	if withHelper {
		args = append([]string{"-c", "credential.helper=", "-c", "credential.helper=" + credentialHelper}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- fixed binary, arguments from ki itself
	cmd.Dir, cmd.Env = dir, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := oneLine(stderr.String()); msg != "" {
			return "", fmt.Errorf("%s", msg)
		}
		return "", fmt.Errorf("%w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
