package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gitHarness turns the test harness directory into a clone with an origin
// and a second clone that can push ahead of it.
func gitHarness(t *testing.T, e *env) (origin, other string) {
	t.Helper()
	base := filepath.Dir(e.harness)
	origin = filepath.Join(base, "origin.git")
	other = filepath.Join(base, "other")
	ctx := context.Background()
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := gitRun(ctx, dir, commandTimeout, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("", "init", "-q", "--bare", "-b", "main", origin)
	run(e.harness, "init", "-q", "-b", "main")
	run(e.harness, "-c", "user.name=t", "-c", "user.email=t@x", "add", "-A")
	run(e.harness, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "seed")
	run(e.harness, "remote", "add", "origin", origin)
	run(e.harness, "push", "-q", "-u", "origin", "main")
	run(e.harness, "remote", "set-head", "origin", "main")
	run("", "clone", "-q", origin, other)
	return origin, other
}

func TestUpdatePullsAndHandsOverToDoctor(t *testing.T) {
	e := newEnv(t, "https://github.com/me/app")
	_, other := gitHarness(t, e)
	e.app.Executable = func() (string, error) { return "/opt/ki", nil }

	if err := e.app.Run(context.Background(), []string{"update"}); err != nil {
		t.Fatalf("clean checkout: %v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), "already at") || !strings.HasSuffix(strings.Join(e.execd, " "), "ki doctor") {
		t.Fatalf("expected no-op pull then doctor, got out=%q exec=%v", e.out, e.execd)
	}

	ctx := context.Background()
	write(t, filepath.Join(other, "skills", "new", "SKILL.md"), "# new\n")
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@x", "commit", "-q", "-m", "skill"}, {"push", "-q"}} {
		if out, err := gitRun(ctx, other, commandTimeout, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// tag.gpgSign=true in a global config turns this into a signed tag that needs a message.
	if out, err := gitRun(ctx, other, commandTimeout, "-c", "tag.gpgSign=false", "tag", "v9.9.9"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := gitRun(ctx, other, commandTimeout, "push", "-q", "--tags"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	e.out.Reset()
	if err := e.app.Run(ctx, []string{"update"}); err != nil {
		t.Fatalf("behind origin: %v\n%s", err, e.out)
	}
	if !strings.Contains(e.out.String(), " -> ") {
		t.Fatalf("expected a fast-forward, got %q", e.out)
	}
	if git(ctx, e.harness, "tag", "--list", "v9.9.9") != "v9.9.9" {
		t.Fatal("update must fetch tags, so ki reports the release version")
	}
	if _, err := os.Stat(filepath.Join(e.harness, "skills", "new", "SKILL.md")); err != nil {
		t.Fatal("the new skill did not arrive:", err)
	}
}

func TestUpdateRefusesDirtyOrWrongBranch(t *testing.T) {
	e := newEnv(t, "https://github.com/me/app")
	gitHarness(t, e)
	ctx := context.Background()

	write(t, filepath.Join(e.harness, "scratch.txt"), "x\n")
	err := e.app.Run(ctx, []string{"update"})
	if !isUserError(err) || !strings.Contains(err.Error(), "local changes") {
		t.Fatalf("dirty checkout must refuse: %v", err)
	}
	if err := os.Remove(filepath.Join(e.harness, "scratch.txt")); err != nil {
		t.Fatal(err)
	}

	if out, err := gitRun(ctx, e.harness, commandTimeout, "checkout", "-q", "-b", "feature"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	err = e.app.Run(ctx, []string{"update"})
	if !isUserError(err) || !strings.Contains(err.Error(), `branch "feature"`) {
		t.Fatalf("other branch must refuse: %v", err)
	}
	if len(e.execd) != 0 {
		t.Fatalf("nothing should have been started: %v", e.execd)
	}
}

func TestUpdateNeedsACheckout(t *testing.T) {
	e := newEnv(t, "https://github.com/me/app")
	err := e.app.Run(context.Background(), []string{"update"})
	if !isUserError(err) || !strings.Contains(err.Error(), "not a git checkout") {
		t.Fatalf("got %v", err)
	}
}
