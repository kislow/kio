package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const folderProjectsYAML = `sensitive_brains: [client, client-*]
identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: false, ingest: true}
  other: {github_user: alice-other, email: alice@other.example, credential: other, vault: true, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app, voice]}
  - {name: lib, repo: https://github.com/work/lib, identity: work, brains: [lib, client]}
  - {name: client-api, repo: https://github.com/other/api, identity: other, brains: [client]}
`

// fakeGHAnyone puts a gh on PATH that prints a token for every account.
func fakeGHAnyone(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho tok-$4\n")
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil { // #nosec G302 -- the fake gh must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// initRepo creates a git repo at dir, with an origin remote when one is given.
func initRepo(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	if remote != "" {
		gitIn(t, dir, "remote", "add", "origin", remote)
	}
}

// folderEnv is an env whose current directory is a folder outside any repo,
// with one child repo per remote given ("" is a repo without an origin).
func folderEnv(t *testing.T, remotes map[string]string) (*env, string) {
	t.Helper()
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), folderProjectsYAML)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "token-sensitive"), "tok-sensitive\n")
	folder := filepath.Join(filepath.Dir(e.repo), "folder")
	for name, remote := range remotes {
		initRepo(t, filepath.Join(folder, name), remote)
	}
	e.app.Dir = folder
	fakeGHAnyone(t)
	return e, folder
}

func (e *env) session(t *testing.T, args ...string) (stderr string, err error) {
	t.Helper()
	e.app.Stderr = &bytes.Buffer{}
	err = e.app.Run(context.Background(), append(args, "--agent", "sh", "--", "-c", "true"))
	return e.app.Stderr.(*bytes.Buffer).String(), err
}

func TestFolderOfOneIdentityStartsAsIt(t *testing.T) {
	e, folder := folderEnv(t, map[string]string{
		"app": "git@github.com:work/app.git", "lib": "https://github.com/work/lib", "elsewhere": "https://github.com/someone/else",
		"noorigin": "", ".hidden": "https://github.com/other/api",
	})
	// Job worktrees live next to a checkout; their repos are not part of the folder.
	initRepo(t, filepath.Join(folder, "app-worktrees", "j-1"), "https://github.com/other/api")
	// Only direct children count.
	initRepo(t, filepath.Join(folder, "group", "nested"), "https://github.com/other/api")

	stderr, err := e.session(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"KI_BRAINS=app,voice,lib,client", "KI_TOKEN=tok-sensitive", "GH_TOKEN=tok-alice-work"} {
		if !slices.Contains(e.execEnv, want) {
			t.Fatalf("want %s in %v", want, e.execEnv)
		}
	}
	if !strings.Contains(stderr, "ki: not in a git repo; identity work, projects app, lib") || strings.Contains(stderr, "no identity") {
		t.Fatalf("stderr:\n%s", stderr)
	}
}

func TestFolderMixingIdentitiesIsRefusedUntilOneIsNamed(t *testing.T) {
	e, _ := folderEnv(t, map[string]string{"app": "https://github.com/work/app", "api": "https://github.com/other/api"})
	if _, err := e.session(t); !isUserError(err) || !strings.Contains(err.Error(), "work, other") && !strings.Contains(err.Error(), "other, work") ||
		!strings.Contains(err.Error(), "--identity NAME") || e.execd != nil {
		t.Fatalf("want refusal naming both identities, got %v (exec %v)", err, e.execd)
	}

	stderr, err := e.session(t, "--identity", "work")
	if err != nil {
		t.Fatal(err)
	}
	// A named identity gets the non-sensitive brains of all its projects, never the sensitive ones.
	for _, want := range []string{"KI_BRAINS=cortex,app,voice,lib", "KI_TOKEN=tok", "GH_TOKEN=tok-alice-work"} {
		if !slices.Contains(e.execEnv, want) {
			t.Fatalf("want %s in %v", want, e.execEnv)
		}
	}
	if !strings.Contains(stderr, "identity work (--identity)") {
		t.Fatalf("stderr:\n%s", stderr)
	}

	if _, err := e.session(t, "--identity", "nobody"); !isUserError(err) || !strings.Contains(err.Error(), `unknown identity "nobody" (choose other, work)`) {
		t.Fatalf("got %v", err)
	}
	if _, err := e.session(t, "--identity", "work", "--brain", "client"); err != nil || !slices.Contains(e.execEnv, "KI_TOKEN=tok-sensitive") {
		t.Fatalf("a named sensitive brain must switch the token: %v %v", err, e.execEnv)
	}
}

func TestFolderWithoutMappedReposStartsWithoutIdentity(t *testing.T) {
	e, _ := folderEnv(t, map[string]string{"elsewhere": "https://github.com/someone/else"})
	stderr, err := e.session(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "ki: not in a git repo; no identity, gh's active account\n") || !strings.Contains(stderr, "ki: no identity, gh active account") {
		t.Fatalf("stderr:\n%s", stderr)
	}
	if !slices.Contains(e.execEnv, "KI_BRAINS=cortex") || !slices.Contains(e.execEnv, "KI_TOKEN=tok") || countKey(e.execEnv, "GH_TOKEN") != 1 || slices.Contains(e.execEnv, "GH_TOKEN=tok-alice-work") {
		t.Fatalf("env %v", e.execEnv)
	}
}

func TestFolderIdentityMustBeCarriedByTheMachine(t *testing.T) {
	e, _ := folderEnv(t, map[string]string{"app": "https://github.com/work/app"})
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [other]\n")
	if _, err := e.session(t); !isUserError(err) || !strings.Contains(err.Error(), "does not carry") || e.execd != nil {
		t.Fatalf("got %v", err)
	}
	if _, err := e.session(t, "--identity", "other"); err != nil {
		t.Fatalf("a carried identity starts: %v", err)
	}
}

func TestIdentityFlagInsideARepo(t *testing.T) {
	e, _ := folderEnv(t, nil)
	e.app.Dir = e.repo
	gitIn(t, e.repo, "config", "user.email", "alice@work.example")

	if _, err := e.session(t, "--identity", "other"); !isUserError(err) || !strings.Contains(err.Error(), `belongs to identity "work", not "other"`) || e.execd != nil {
		t.Fatalf("got %v", err)
	}
	if _, err := e.session(t, "--identity", "work"); err != nil || !slices.Contains(e.execEnv, "KI_BRAINS=app,voice") {
		t.Fatalf("the repo's own identity changes nothing: %v %v", err, e.execEnv)
	}
	if _, err := e.session(t, "--identity", "other", "--force"); err != nil || !slices.Contains(e.execEnv, "GH_TOKEN=tok-alice-other") {
		t.Fatalf("--force lets the flag win: %v %v", err, e.execEnv)
	}
	if _, err := e.session(t, "--identity", "nobody"); !isUserError(err) || !strings.Contains(err.Error(), "unknown identity") {
		t.Fatalf("got %v", err)
	}
}

func TestIdentityFlagDoesNotRescueAnUnmappedRepo(t *testing.T) {
	e, _ := folderEnv(t, nil)
	e.app.Dir = e.repo
	gitIn(t, e.repo, "remote", "set-url", "origin", "https://github.com/someone/else")
	if _, err := e.session(t, "--identity", "work"); !isUserError(err) || !strings.Contains(err.Error(), "not in projects.yaml") {
		t.Fatalf("got %v", err)
	}
}

func TestStatusReportsTheFolder(t *testing.T) {
	e, folder := folderEnv(t, map[string]string{"app": "https://github.com/work/app", "lib": "https://github.com/work/lib"})
	status := func() map[string]any {
		e.out.Reset()
		if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(e.out.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := status()
	id, _ := out["identity"].(map[string]any)
	if out["folder"] != true || out["repo"] != nil || id["name"] != "work" || out["unmapped"] != false || out["identity_ok"] != true ||
		out["sensitive"] != true || !slices.Equal(anyStrings(out["projects"]), []string{"app", "lib"}) || out["folder_identities"] != nil {
		t.Fatalf("got %v", out)
	}

	initRepo(t, filepath.Join(folder, "api"), "https://github.com/other/api")
	out = status()
	if out["folder"] != true || out["identity"] != nil || !slices.Equal(anyStrings(out["folder_identities"]), []string{"other", "work"}) {
		t.Fatalf("got %v", out)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"status"}); err != nil || !strings.Contains(e.out.String(), "several identities (other, work)") {
		t.Fatalf("%v\n%s", err, e.out)
	}

	e.app.Dir = e.repo
	if out = status(); out["folder"] != false || out["repo"] == nil {
		t.Fatalf("a repo is not a folder: %v", out)
	}
}

func anyStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, x.(string))
	}
	return out
}

func TestMeccaNewAcceptsAFolderOfOneIdentityOrNone(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	folder := filepath.Join(filepath.Dir(e.repo), "folder")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), folderProjectsYAML)
	initRepo(t, filepath.Join(folder, "app"), "https://github.com/work/app")

	if err := e.app.Run(context.Background(), []string{"mecca", "new", folder}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.call("set-option"), " "), "@ki-identity work") || f.call("new-window") == nil {
		t.Fatalf("%v", f.calls)
	}

	empty := filepath.Join(filepath.Dir(e.repo), "empty")
	if err := os.MkdirAll(empty, 0o750); err != nil {
		t.Fatal(err)
	}
	f.calls = nil
	if err := e.app.Run(context.Background(), []string{"mecca", "new", empty}); err != nil {
		t.Fatalf("a folder with no mapped repos starts without --force: %v", err)
	}
	if !strings.Contains(strings.Join(f.call("set-option"), " "), "@ki-identity unmapped") {
		t.Fatalf("%v", f.call("set-option"))
	}

	initRepo(t, filepath.Join(folder, "api"), "https://github.com/other/api")
	f.calls = nil
	err := e.app.Run(context.Background(), []string{"mecca", "new", folder})
	if !isUserError(err) || !strings.Contains(err.Error(), "several identities") || f.call("new-window") != nil {
		t.Fatalf("got %v", err)
	}
}
