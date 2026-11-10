// Package gitisolate keeps tests that run git away from the repository they run in.
package gitisolate

import (
	"fmt"
	"os"
	"path/filepath"
)

// repoVars point git at a specific repository; inherited from a hook or alias
// they would make a test's git calls act on the real checkout.
var repoVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX",
	"GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG_PARAMETERS",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
}

// Isolate scrubs the process environment for git and returns a cleanup func.
// Call it from TestMain before m.Run.
func Isolate() (func(), error) {
	root, err := os.MkdirTemp("", "ki-git-isolate-")
	if err != nil {
		return nil, fmt.Errorf("%w: creating isolated git root", err)
	}
	// Resolve symlinks so the ceiling matches the paths git computes (macOS /var).
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	empty := filepath.Join(root, "empty.gitconfig")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return nil, fmt.Errorf("%w: writing empty git config", err)
	}
	for _, v := range repoVars {
		if err := os.Unsetenv(v); err != nil {
			return nil, fmt.Errorf("%w: unsetting %s", err, v)
		}
	}
	set := map[string]string{
		"GIT_CONFIG_GLOBAL":       empty,
		"GIT_CONFIG_SYSTEM":       empty,
		"GIT_CONFIG_NOSYSTEM":     "1",
		"HOME":                    root,
		"XDG_CONFIG_HOME":         filepath.Join(root, ".config"),
		"GIT_CEILING_DIRECTORIES": root,
	}
	for k, v := range set {
		if err := os.Setenv(k, v); err != nil {
			return nil, fmt.Errorf("%w: setting %s", err, k)
		}
	}
	return func() { _ = os.RemoveAll(root) }, nil
}

// Main runs a package's tests inside an isolated git environment.
func Main(run func() int) int {
	cleanup, err := Isolate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitisolate:", err)
		return 1
	}
	defer cleanup()
	return run()
}
