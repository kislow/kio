package gitisolate

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func TestIsolateKeepsGitAwayFromTheSentinelRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	// Registered with t.Setenv first so the environment is restored after Isolate rewrites it.
	vars := []string{"HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CEILING_DIRECTORIES"}
	for _, v := range append(vars, repoVars...) {
		t.Setenv(v, os.Getenv(v))
	}
	// Scrub whatever git variables the outer run carries so the fixture itself is clean.
	cleanFixture, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanFixture)
	sentinel := filepath.Join(t.TempDir(), "sentinel")
	git(t, t.TempDir(), "init", "-q", sentinel)

	configBefore, err := os.ReadFile(filepath.Join(sentinel, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	branchesBefore := git(t, sentinel, "branch", "-a")
	worktreesBefore := git(t, sentinel, "worktree", "list", "--porcelain")

	t.Setenv("GIT_DIR", filepath.Join(sentinel, ".git"))
	t.Setenv("GIT_WORK_TREE", sentinel)

	cleanup, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	work := t.TempDir()
	git(t, work, "init", "-q", "repo")
	repo := filepath.Join(work, "repo")
	git(t, repo, "config", "user.email", "alice@work.example")
	git(t, repo, "config", "core.bare", "true")
	git(t, repo, "worktree", "list")

	configAfter, err := os.ReadFile(filepath.Join(sentinel, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(configBefore) {
		t.Errorf("sentinel config changed:\n%s", configAfter)
	}
	if got := git(t, sentinel, "branch", "-a"); got != branchesBefore {
		t.Errorf("sentinel branches changed: %q", got)
	}
	if got := git(t, sentinel, "worktree", "list", "--porcelain"); got != worktreesBefore {
		t.Errorf("sentinel worktrees changed: %q", got)
	}
}
