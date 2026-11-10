package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNode puts a `node` on PATH that answers like the diagram tool: it
// prints its arguments, fails on "fail", and answers "requirements" with
// the given JSON.
func fakeNode(t *testing.T, requirementsJSON string) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$2" in
  requirements) printf '%s\n' '` + requirementsJSON + `'; exit 1 ;;
  fail) echo "boom" >&2; exit 3 ;;
  *) shift; echo "args: $*"; pwd ;;
esac
`
	write(t, filepath.Join(dir, "node"), script)
	if err := os.Chmod(filepath.Join(dir, "node"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func withDiagramTool(t *testing.T, e *env, installed bool) {
	t.Helper()
	write(t, filepath.Join(e.harness, "diagram", "bin", "ki-diagram.mjs"), "// tool\n")
	if installed {
		write(t, filepath.Join(e.harness, "diagram", "node_modules", ".package-lock.json"), "{}\n")
	}
}

func TestDiagramRunsTheToolWhereKiWasStarted(t *testing.T) {
	e := newEnv(t, "")
	withDiagramTool(t, e, true)
	fakeNode(t, "[]")
	if err := e.app.Run(context.Background(), []string{"diagram", "build", "x.diagram.yaml", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	out := e.out.String()
	if !strings.Contains(out, "args: build x.diagram.yaml --dry-run") {
		t.Fatalf("arguments not passed through: %q", out)
	}
	if wd, _ := filepath.EvalSymlinks(e.repo); !strings.Contains(out, wd) && !strings.Contains(out, e.repo) {
		t.Fatalf("tool did not run in %s: %q", e.repo, out)
	}
}

func TestDiagramErrors(t *testing.T) {
	cases := []struct {
		name      string
		tool      bool
		node      bool
		args      []string
		wantError string
	}{
		{"no tool in the checkout", false, true, []string{"check"}, "has no diagram tool"},
		{"no node", true, false, []string{"check"}, "needs node 22 or newer"},
		{"tool fails", true, true, []string{"fail"}, "ki diagram failed (exit 3)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "")
			if c.tool {
				withDiagramTool(t, e, true)
			}
			if c.node {
				fakeNode(t, "[]")
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			err := e.app.Run(context.Background(), append([]string{"diagram"}, c.args...))
			if !isUserError(err) || !strings.Contains(err.Error(), c.wantError) {
				t.Fatalf("got %v, want %q", err, c.wantError)
			}
		})
	}
}

func TestDoctorDiagramChecks(t *testing.T) {
	cases := []struct {
		name      string
		installed bool
		node      bool
		reqs      string
		want      []string
	}{
		{"no node is a note", true, false, "[]", []string{"· ki diagram", "node is not installed"}},
		{"not set up is a note", false, true, "[]", []string{"· ki diagram", "not set up; for tldraw drawings run: make -C"}},
		{
			"set up reports each requirement", true, true,
			`[{"name":"packages","ok":true,"detail":"pinned","fix":""},{"name":"browser","ok":false,"detail":"no headless Chromium","fix":"make diagram"}]`,
			[]string{"✓ diagram packages", "✗ diagram browser", "no headless Chromium", "fix: make diagram"},
		},
		{"no answer is a problem", true, true, "not json", []string{"✗ ki diagram", "requirements check gave no answer"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, "")
			e.vars["KI_VAULT"] = t.TempDir()
			withDiagramTool(t, e, c.installed)
			if c.node {
				fakeNode(t, c.reqs)
			} else {
				t.Setenv("PATH", "/usr/bin:/bin")
				if _, err := os.Stat("/usr/bin/node"); err == nil {
					t.Skip("node is installed system-wide")
				}
			}
			_ = e.app.Run(context.Background(), []string{"doctor"}) // other sections fail in a temp home; only this one matters
			_, section, found := strings.Cut(e.out.String(), "\nDiagrams\n")
			if !found {
				t.Fatalf("doctor printed no Diagrams section:\n%s", e.out)
			}
			for _, w := range c.want {
				if !strings.Contains(section, w) {
					t.Errorf("Diagrams section lacks %q:\n%s", w, section)
				}
			}
		})
	}
}
