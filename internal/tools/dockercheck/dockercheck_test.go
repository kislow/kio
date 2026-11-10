// Package dockercheck holds a repository test: every Dockerfile that builds Go
// copies every top-level directory holding the module's Go packages. The
// 0.29.0 images failed to build because pkg/ was added and the Dockerfiles
// still copied only cmd/ and internal/; PR CI builds no images, so this test
// is what catches it before a release.
package dockercheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var copyRE = regexp.MustCompile(`(?m)^COPY\s+(?:--\S+\s+)*(\S+)\s+\S+\s*$`)

// goSourceDirs returns the top-level directories of the repo that hold Go
// packages of the main module (directories with their own go.mod are skipped).
func goSourceDirs(t *testing.T, root string) map[string]bool {
	t.Helper()
	dirs := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			name := d.Name()
			if rel != "." && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "testdata") {
				return filepath.SkipDir
			}
			if rel != "." {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && strings.Contains(rel, string(filepath.Separator)) {
			dirs[strings.SplitN(rel, string(filepath.Separator), 2)[0]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

func TestGoDockerfilesCopyEveryGoSourceDir(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	want := goSourceDirs(t, root)
	for _, df := range []string{"cmd/ki-mcp/Dockerfile", "vault/Dockerfile"} {
		raw, err := os.ReadFile(filepath.Join(root, df))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "go build") {
			t.Fatalf("%s no longer runs go build; update this test", df)
		}
		copied := map[string]bool{}
		for _, m := range copyRE.FindAllStringSubmatch(string(raw), -1) {
			copied[strings.TrimSuffix(strings.TrimPrefix(m[1], "./"), "/")] = true
		}
		for dir := range want {
			if dir == "kb" || dir == "evals" || dir == "diagram" {
				continue // not Go packages of the module's binaries
			}
			if !copied[dir] && !copied["."] {
				t.Errorf("%s does not COPY %s/, which holds Go packages", df, dir)
			}
		}
	}
}
