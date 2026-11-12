// Package gitisolate keeps tests that run git away from the repository they run in.
package gitisolate

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// repoVars point git at a specific repository; inherited from a hook or alias
// they would make a test's git calls act on the real checkout.
var repoVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_PREFIX",
	"GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG_PARAMETERS",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
}

// configCount and configPrefixes name the counted git config entries a parent
// process may pass down (GIT_CONFIG_COUNT, GIT_CONFIG_KEY_n, GIT_CONFIG_VALUE_n).
// Inherited, they would give a test the hooks of the process that started it.
const configCount = "GIT_CONFIG_COUNT"

var configPrefixes = []string{"GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"}

// jobVars mark the process as a job of a worker, which hands git its hooks
// through the config entries above.
var jobVars = []string{"KI_JOB"}

// inheritedVars lists every variable Isolate removes from the environment.
func inheritedVars() []string {
	vars := slices.Concat(repoVars, []string{configCount}, jobVars)
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		for _, p := range configPrefixes {
			if strings.HasPrefix(name, p) {
				vars = append(vars, name)
			}
		}
	}
	return vars
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
	for _, v := range inheritedVars() {
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
