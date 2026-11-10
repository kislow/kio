package ki

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCheckouts(t *testing.T) {
	c, err := ParseCheckouts([]byte("roots: [~/code, /abs]\ncheckouts:\n  app: ~/x/app\n"), "/home/k")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(c.Roots, ",") != "/home/k/code,/abs" || c.Paths["app"] != "/home/k/x/app" {
		t.Fatalf("%+v", c)
	}
	c, err = ParseCheckouts(nil, "/home/k")
	if err != nil || strings.Join(c.Roots, ",") != "/home/k/code" {
		t.Fatalf("default roots: %+v %v", c, err)
	}
	if _, err := ParseCheckouts([]byte("root: [~/code]\n"), "/home/k"); err == nil {
		t.Fatal("unknown key accepted")
	}
}

func TestLoadCheckouts(t *testing.T) {
	dir := t.TempDir()
	c, err := LoadCheckouts(filepath.Join(dir, "missing.yaml"), "/home/k")
	if err != nil || len(c.Roots) != 1 {
		t.Fatalf("missing file: %+v %v", c, err)
	}
	if _, err := LoadCheckouts(dir, "/home/k"); err == nil {
		t.Fatal("reading a directory succeeded")
	}
	f := filepath.Join(dir, MeccaConfigFile)
	mustWrite(t, f, "roots: [/r]\n")
	if c, err := LoadCheckouts(f, "/home/k"); err != nil || c.Roots[0] != "/r" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestExpandHome(t *testing.T) {
	for in, want := range map[string]string{"~": "/h", "~/a": "/h/a", "/abs": "/abs", "rel": "rel", "~x": "~x"} {
		if got := ExpandHome(in, "/h"); got != want {
			t.Errorf("ExpandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// checkout makes a fake git checkout (a .git directory) and returns its real path.
func checkout(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func TestCheckoutsFind(t *testing.T) {
	root := t.TempDir()
	app := checkout(t, filepath.Join(root, "personal", "app"))
	fork := checkout(t, filepath.Join(root, "other", "app"))
	direct := checkout(t, filepath.Join(root, "direct"))
	renamed := checkout(t, filepath.Join(root, "personal", "repo-name"))
	if err := os.MkdirAll(filepath.Join(root, "personal", "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	origins := map[string]string{
		app:     "git@github.com:me/app.git",
		fork:    "https://github.com/someone/app",
		renamed: "https://github.com/me/repo-name",
	}
	originOf := func(d string) string { return origins[d] }
	c := &Checkouts{Roots: []string{root}, Paths: map[string]string{"mapped": fork, "gone": filepath.Join(root, "nope")}}

	tests := []struct {
		name, project, repo, want, wantErr string
	}{
		{name: "explicit path wins", project: "mapped", want: fork},
		{name: "explicit path missing", project: "gone", wantErr: "not a directory"},
		{name: "repo filters same-named folders", project: "app", repo: "https://github.com/me/app", want: app},
		{name: "unlisted name, two folders", project: "app", wantErr: "matches"},
		{name: "one level deep", project: "direct", want: direct},
		{name: "project name differs from repo", project: "my-project", repo: "https://github.com/me/repo-name", want: renamed},
		{name: "not a git checkout", project: "plain", wantErr: "no checkout found"},
		{name: "nothing", project: "absent", wantErr: "no checkout found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Find(tt.project, tt.repo, originOf)
			if tt.wantErr != "" {
				if !errors.Is(err, ErrNoCheckout) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
