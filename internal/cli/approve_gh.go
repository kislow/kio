package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// ghRun runs gh as one account: token goes in GH_TOKEN, "" leaves gh's own
// active account. stdin is the request body of `gh api --input -`.
type ghRun func(ctx context.Context, token string, stdin []byte, args ...string) ([]byte, error)

func (a *App) ghRunner() ghRun {
	if a.GH != nil {
		return a.GH
	}
	return func(ctx context.Context, token string, stdin []byte, args ...string) ([]byte, error) {
		if _, err := exec.LookPath("gh"); err != nil {
			return nil, errors.New("gh is not on PATH")
		}
		ctx, cancel := context.WithTimeout(ctx, githubTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "gh", args...) // #nosec G204 -- fixed binary, arguments from ki itself
		extra := map[string]string{"GH_PROMPT_DISABLED": "1"}
		if token != "" {
			extra["GH_TOKEN"] = token
		}
		cmd.Env = ki.MergeEnv(a.Environ(), extra)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("gh %s: %w: %s", args[0], err, githubSnippet(stderr.Bytes()))
		}
		return stdout.Bytes(), nil
	}
}

// pullClient is one pull request as one account sees it. Every call is a gh
// call made with the account's own token.
type pullClient struct {
	run   ghRun
	token string
	ref   ki.PRRef
}

func (c pullClient) api(ctx context.Context, out any, args ...string) error {
	raw, err := c.run(ctx, c.token, nil, append([]string{"api"}, args...)...)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding GitHub response: %w", err)
	}
	return nil
}

func (c pullClient) path(suffix string) string {
	return fmt.Sprintf("repos/%s/%s/%s", c.ref.Owner, c.ref.Repo, suffix)
}

// Login is the account the token belongs to.
func (c pullClient) Login(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := c.api(ctx, &u, "user"); err != nil {
		return "", fmt.Errorf("reading the account: %w", err)
	}
	return u.Login, nil
}

// Pull reads the pull request.
func (c pullClient) Pull(ctx context.Context) (ki.PullRequest, error) {
	var raw struct {
		Title  string `json:"title"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		Draft  bool   `json:"draft"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		MergeableState string `json:"mergeable_state"`
	}
	if err := c.api(ctx, &raw, c.path(fmt.Sprintf("pulls/%d", c.ref.Number))); err != nil {
		return ki.PullRequest{}, fmt.Errorf("reading %s: %w", c.ref, err)
	}
	return ki.PullRequest{
		Title: raw.Title, State: raw.State, Merged: raw.Merged, Draft: raw.Draft, Author: raw.User.Login,
		HeadSHA: raw.Head.SHA, HeadRef: raw.Head.Ref, BaseRef: raw.Base.Ref, MergeableState: raw.MergeableState,
	}, nil
}

// maxChecks is how many check runs and statuses ki reads of one commit.
const maxChecks = 100

// Checks reads the check runs and commit statuses of sha.
func (c pullClient) Checks(ctx context.Context, sha string) (ki.Checks, error) {
	var runs struct {
		Total int `json:"total_count"`
		Runs  []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	if err := c.api(ctx, &runs, c.path(fmt.Sprintf("commits/%s/check-runs?per_page=%d", sha, maxChecks))); err != nil {
		return ki.Checks{}, fmt.Errorf("reading the checks of %s: %w", c.ref, err)
	}
	var statuses struct {
		Total    int `json:"total_count"`
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := c.api(ctx, &statuses, c.path(fmt.Sprintf("commits/%s/status?per_page=%d", sha, maxChecks))); err != nil {
		return ki.Checks{}, fmt.Errorf("reading the statuses of %s: %w", c.ref, err)
	}
	out := ki.Checks{Incomplete: runs.Total > len(runs.Runs) || statuses.Total > len(statuses.Statuses)}
	for _, r := range runs.Runs {
		out.Runs = append(out.Runs, ki.CheckRun{Name: r.Name, Status: r.Status, Conclusion: r.Conclusion})
	}
	for _, s := range statuses.Statuses {
		out.Statuses = append(out.Statuses, ki.CommitStatus{Context: s.Context, State: s.State})
	}
	return out, nil
}

// Diff is the pull request's unified diff.
func (c pullClient) Diff(ctx context.Context) (string, error) {
	raw, err := c.run(ctx, c.token, nil, "api", "-H", "Accept: application/vnd.github.diff", c.path(fmt.Sprintf("pulls/%d", c.ref.Number)))
	if err != nil {
		return "", fmt.Errorf("reading the diff of %s: %w", c.ref, err)
	}
	return string(raw), nil
}

// Review submits one review on the commit sha and returns its URL.
func (c pullClient) Review(ctx context.Context, sha, event, body string) (string, error) {
	payload, err := json.Marshal(map[string]string{"commit_id": sha, "event": event, "body": body})
	if err != nil {
		return "", fmt.Errorf("encoding the review: %w", err)
	}
	raw, err := c.run(ctx, c.token, payload, "api", "-X", "POST", c.path(fmt.Sprintf("pulls/%d/reviews", c.ref.Number)), "--input", "-")
	if err != nil {
		return "", fmt.Errorf("submitting the review on %s: %w", c.ref, err)
	}
	var resp struct {
		URL string `json:"html_url"`
	}
	// The review landed whatever the body says; only its link would be missing.
	_ = json.Unmarshal(raw, &resp)
	return resp.URL, nil
}

// Merge squash-merges the pull request and deletes its branch, only if its
// head is still sha. There is no admin override: GitHub refuses what the
// branch rules refuse.
func (c pullClient) Merge(ctx context.Context, sha string) error {
	_, err := c.run(ctx, c.token, nil, "pr", "merge", fmt.Sprint(c.ref.Number), "-R", c.ref.Owner+"/"+c.ref.Repo,
		"--squash", "--delete-branch", "--match-head-commit", sha)
	if err != nil {
		return fmt.Errorf("merging %s: %w", c.ref, err)
	}
	return nil
}

// pullURL is the pull request's web address.
func (c pullClient) pullURL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", c.ref.Owner, c.ref.Repo, c.ref.Number)
}

// oneLine keeps an error to its first line for a note.
func oneLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}
