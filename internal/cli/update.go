package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// update brings this machine to the harness's current main: pull the
// checkout, reinstall ki when Go is available, relink skills and agents,
// then hand over to `ki doctor`. It refuses a checkout with local changes or
// on another branch, because `make install-cli` would quietly ship them; a
// checkout on a release tag moves to the newest one. Without a checkout (a
// release binary) there is nothing to pull: the binary is upgraded the way it
// was installed, and this relinks the skills it carries.
func (a *App) update(ctx context.Context) error {
	dir, err := a.harness()
	if err != nil {
		return err
	}
	if dir != "" {
		if err := a.pullCheckout(ctx, "kio", dir, true); err != nil {
			return err
		}
	}
	if err := a.pullConfig(ctx, dir); err != nil {
		return err
	}
	if dir == "" {
		fmt.Fprintf(a.Stdout, "ki %s has no checkout: upgrade the binary the way you installed it (your package manager, `go install github.com/kislow/kio/cmd/ki@latest`, or a new release archive)\n", a.Version)
		return a.setupSkills()
	}

	if _, err := os.Stat(filepath.Join(dir, "Makefile")); err == nil {
		targets := []string{"skills"}
		if _, err := exec.LookPath("go"); err == nil {
			targets = []string{"install-cli", "skills"}
		} else {
			fmt.Fprintln(a.Stdout, "go is not installed: skills relinked, download the release binary for ki itself (docs/laptop-setup.md)")
		}
		cmd := exec.CommandContext(ctx, "make", append([]string{"-C", dir, "-s"}, targets...)...) // #nosec G204 -- fixed targets in our own checkout
		cmd.Stdout, cmd.Stderr = a.Stdout, a.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("make %s: %w", strings.Join(targets, " "), err)
		}
	}

	bin := a.binary()
	if bin == "" {
		return nil
	}
	if err := a.Exec(bin, []string{"ki", "doctor"}, a.Environ()); err != nil {
		return fmt.Errorf("starting ki doctor: %w", err)
	}
	return nil
}

// pullConfig pulls a linked configuration that lives in a git checkout; the
// fallback inside the harness checkout moved with the harness.
func (a *App) pullConfig(ctx context.Context, harness string) error {
	cfgDir, source, err := a.configDir(harness)
	if err != nil { // no configuration: nothing to pull, and a release binary needs none to update
		return nil //nolint:nilerr
	}
	if source == ki.ConfigFromHarness {
		return nil
	}
	if top := git(ctx, cfgDir, "rev-parse", "--show-toplevel"); top != "" {
		return a.pullCheckout(ctx, "configuration", top, false)
	}
	return nil
}

var releaseTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// newestReleaseTag is the highest vMAJOR.MINOR.PATCH tag, "" when there is none.
func newestReleaseTag(ctx context.Context, dir string) string {
	for _, tag := range strings.Fields(git(ctx, dir, "tag", "--list", "--sort=-v:refname")) {
		if releaseTag.MatchString(tag) {
			return tag
		}
	}
	return ""
}

// pullCheckout fast-forwards one checkout on its default branch, refusing
// local changes or another branch, because what follows (make install-cli,
// the next session) would quietly ship them. With followTags a checkout
// detached on a release tag, as INSTALL.md used to leave it, moves to the
// newest release tag instead.
func (a *App) pullCheckout(ctx context.Context, label, dir string, followTags bool) error {
	if git(ctx, dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return fail("%s is not a git checkout; clone %s there (docs/laptop-setup.md)", dir, label)
	}
	if dirty := git(ctx, dir, "status", "--porcelain"); dirty != "" {
		return fail("%s has local changes; commit or stash them first:\n%s", a.short(dir), dirty)
	}
	branch := git(ctx, dir, "symbolic-ref", "--short", "HEAD")
	if branch == "" && followTags {
		return a.moveToNewestTag(ctx, label, dir)
	}
	want := strings.TrimPrefix(git(ctx, dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"), "origin/")
	if want == "" {
		want = "main"
	}
	if branch != want {
		return fail("%s is on branch %q; switch to %s before updating", a.short(dir), branch, want)
	}
	before := git(ctx, dir, "rev-parse", "--short", "HEAD")
	// Tags are what `git describe` turns into the version ki reports; a plain
	// pull leaves them behind.
	if out, err := gitRun(ctx, dir, vaultTimeout, "fetch", "-q", "--tags", "origin"); err != nil {
		return fmt.Errorf("fetching tags for %s: %w: %s", a.short(dir), err, strings.TrimSpace(out))
	}
	if out, err := gitRun(ctx, dir, vaultTimeout, "pull", "-q", "--ff-only", "origin", want); err != nil {
		return fmt.Errorf("pulling %s: %w: %s", a.short(dir), err, strings.TrimSpace(out))
	}
	after := git(ctx, dir, "rev-parse", "--short", "HEAD")
	if before == after {
		fmt.Fprintf(a.Stdout, "%s already at %s\n", label, after)
	} else {
		fmt.Fprintf(a.Stdout, "%s %s -> %s\n", label, before, after)
	}
	return nil
}

// moveToNewestTag moves a checkout detached on a release tag to the newest one.
func (a *App) moveToNewestTag(ctx context.Context, label, dir string) error {
	current := git(ctx, dir, "describe", "--tags", "--exact-match", "HEAD")
	if !releaseTag.MatchString(current) {
		return fail("%s is detached at %s, not on a release tag; check out main or a release tag before updating", a.short(dir), git(ctx, dir, "rev-parse", "--short", "HEAD"))
	}
	if out, err := gitRun(ctx, dir, vaultTimeout, "fetch", "-q", "--tags", "origin"); err != nil {
		return fmt.Errorf("fetching tags for %s: %w: %s", a.short(dir), err, strings.TrimSpace(out))
	}
	newest := newestReleaseTag(ctx, dir)
	if newest == "" || newest == current {
		fmt.Fprintf(a.Stdout, "%s already at %s\n", label, current)
		return nil
	}
	if out, err := gitRun(ctx, dir, vaultTimeout, "checkout", "-q", "--detach", newest); err != nil {
		return fmt.Errorf("checking out %s in %s: %w: %s", newest, a.short(dir), err, strings.TrimSpace(out))
	}
	fmt.Fprintf(a.Stdout, "%s %s -> %s\n", label, current, newest)
	return nil
}
