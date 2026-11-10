package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// skillWorkspace is where a skill runs: an empty scratch folder in the job,
// or with checkout a throwaway worktree of that pull request or branch.
// cleanup removes either; the user's own checkout is never touched.
func (a *App) skillWorkspace(ctx context.Context, root, id, jobDir, checkout string) (dir string, ws ki.SkillWorkspace, cleanup func(), err error) {
	if checkout == "" {
		dir = filepath.Join(jobDir, "scratch")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", ws, nil, fmt.Errorf("creating %s: %w", dir, err)
		}
		return dir, ws, func() { _ = os.RemoveAll(dir) }, nil // best effort: nothing in it is kept
	}
	if root == "" {
		return "", ws, nil, fail("--checkout needs a repo: run ki skill run inside the checkout, or name it with --repo PATH")
	}
	dir, ws, err = a.addSkillWorktree(ctx, root, id, checkout)
	if err != nil {
		return "", ws, nil, err
	}
	return dir, ws, func() {
		if out, err := gitOut(ctx, root, "worktree", "remove", "--force", dir); err != nil {
			fmt.Fprintf(a.Stderr, "ki: could not remove the skill's worktree %s: %s\n", dir, oneLine(out))
		}
	}, nil
}

// addSkillWorktree fetches the pull request's head or the branch from origin
// into a ref of the job's own, so parallel runs never read each other's
// FETCH_HEAD, and checks that commit out detached next to the repo. A branch
// origin does not have falls back to the local one.
func (a *App) addSkillWorktree(ctx context.Context, root, id, target string) (string, ki.SkillWorkspace, error) {
	var ws ki.SkillWorkspace
	src := ""
	if n, ok := pullNumber(target); ok {
		src, ws.Checkout = fmt.Sprintf("refs/pull/%d/head", n), fmt.Sprintf("pull request #%d", n)
	} else {
		if strings.HasPrefix(target, "-") || git(ctx, root, "check-ref-format", "--branch", target) == "" {
			return "", ws, fail("--checkout %q: give a pull request number or a branch name", target)
		}
		src, ws.Checkout, ws.Branch = "refs/heads/"+target, "branch "+target, target
	}
	const prefix = "refs/ki-skill/"
	ref := prefix + id
	defer func() { _, _ = gitOut(ctx, root, "update-ref", "-d", prefix+id) }() // best effort: a stale ref holds no work
	if out, err := gitTimed(ctx, root, pullFetchTimeout, "fetch", "--quiet", "--no-tags", "origin", "+"+src+":"+ref); err != nil {
		if ws.Branch == "" || git(ctx, root, "rev-parse", "--verify", "--quiet", src) == "" {
			return "", ws, fail("fetching %s from origin: %s", ws.Checkout, oneLine(out))
		}
		fmt.Fprintf(a.Stderr, "ki: could not fetch %s (%s); using the local branch\n", ws.Checkout, oneLine(out))
		ref = src
	}
	ws.SHA = git(ctx, root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if ws.SHA == "" {
		return "", ws, fail("%s is not a commit after fetching", ws.Checkout)
	}
	unlock, err := lockWorktrees(ctx, root)
	if err != nil {
		return "", ws, err
	}
	defer unlock()
	dir := filepath.Join(filepath.Dir(root), filepath.Base(root)+"-worktrees", id)
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return "", ws, fmt.Errorf("creating %s: %w", filepath.Dir(dir), err)
	}
	if out, err := gitOut(ctx, root, "worktree", "add", "--quiet", "--detach", dir, ws.SHA); err != nil {
		return "", ws, fail("git worktree add %s at %s: %s", dir, ws.SHA, oneLine(out))
	}
	return dir, ws, nil
}

// pullNumber reads a pull request number: 12 or #12.
func pullNumber(s string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(s, "#"))
	return n, err == nil && n > 0
}
