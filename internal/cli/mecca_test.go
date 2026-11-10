package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// fakeTmux records every tmux call and answers the few whose output matters.
type fakeTmux struct {
	calls   [][]string
	session bool
	windows []string
	capture string
	fail    map[string]error
}

func (f *fakeTmux) run(_ context.Context, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	if err := f.fail[args[0]]; err != nil {
		return "", err
	}
	switch args[0] {
	case "has-session":
		if !f.session {
			return "", errors.New("no server running")
		}
	case "new-session":
		f.session = true
	case "list-windows":
		return strings.Join(f.windows, "\n") + "\n", nil
	case "new-window":
		return "@7 3\n", nil
	case "capture-pane":
		return f.capture, nil
	}
	return "", nil
}

func (f *fakeTmux) call(name string) []string {
	for _, c := range f.calls {
		if c[0] == name {
			return c
		}
	}
	return nil
}

// Nothing in ki mecca may type into a pane; every test checks it.
func (f *fakeTmux) assertNoInput(t *testing.T) {
	t.Helper()
	for _, c := range f.calls {
		if slices.ContainsFunc(c, func(a string) bool { return a == "send-keys" || a == "send-prefix" || a == "paste-buffer" }) {
			t.Fatalf("ki mecca sent input: %v", c)
		}
	}
}

func window(id string, idx int, name, project string, activity time.Time, dead string) string {
	return strings.Join([]string{
		id, fmt.Sprint(idx), name, project, "claude", "work", "", "", "/r",
		fmt.Sprint(activity.Add(-time.Hour).Unix()), fmt.Sprint(activity.Unix()), dead, "1",
	}, "\t")
}

func meccaEnv(t *testing.T, remote string) (*env, *fakeTmux) {
	t.Helper()
	e := newEnv(t, remote)
	f := &fakeTmux{}
	e.app.Tmux = f.run
	e.app.Executable = func() (string, error) { return "/bin/ki", nil }
	e.vars["PATH"] = "/usr/bin"
	e.vars["GH_TOKEN"] = "must-not-forward"
	t.Cleanup(func() { f.assertNoInput(t) })
	return e, f
}

func realRepo(t *testing.T, e *env) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(e.repo)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMeccaNewOpensWindowRunningKi(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	err := e.app.Run(context.Background(), []string{
		"mecca", "new", e.repo, "--task", "Fix CI", "--mode", "skip",
		"--brain", "extra", "--model", "m", "--", "--resume",
	})
	if err != nil {
		t.Fatal(err)
	}
	if f.call("new-session") == nil || f.call("set-hook") == nil {
		t.Fatalf("session not created: %v", f.calls)
	}
	nw := f.call("new-window")
	joined := strings.Join(nw, " ")
	for _, want := range []string{
		"-n app/fix-ci", "-c " + realRepo(t, e), "-e PATH=/usr/bin", "-t =ki-mecca:",
		"-- /bin/ki --agent claude --mode skip --brain extra --model m -- --resume",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-window lacks %q: %v", want, nw)
		}
	}
	if strings.Contains(joined, "GH_TOKEN") || strings.Contains(joined, "KI_MODE") {
		t.Errorf("GH_TOKEN forwarded, or the mode passed by env instead of --mode: %v", nw)
	}
	set := strings.Join(f.call("set-option"), " ")
	for _, want := range []string{"-t @7 @ki-agent claude", "@ki-identity work", "@ki-mode skip", "@ki-project app", "@ki-task Fix CI"} {
		if !strings.Contains(set, want) {
			t.Errorf("set-option lacks %q: %s", want, set)
		}
	}
	if !strings.Contains(e.out.String(), "opened app/fix-ci (@7): claude as work in") {
		t.Fatalf("output %q", e.out)
	}
}

func TestMeccaNewMakesNamesUniqueAndPrintsJSON(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	f.session = true
	f.windows = []string{window("@1", 1, "app", "app", time.Now(), "0")}
	if err := e.app.Run(context.Background(), []string{"mecca", "new", e.repo, "--json"}); err != nil {
		t.Fatal(err)
	}
	if f.call("new-session") != nil {
		t.Fatal("recreated an existing session")
	}
	var w ki.Window
	if err := json.Unmarshal(e.out.Bytes(), &w); err != nil {
		t.Fatalf("%v: %s", err, e.out)
	}
	if w.Name != "app-2" || w.ID != "@7" || w.Index != 3 || w.Identity != "work" || w.Status != ki.StatusWorking {
		t.Fatalf("%+v", w)
	}
}

func TestMeccaNewResolvesProjectNames(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], ki.MeccaConfigFile), "roots: ["+filepath.Dir(e.repo)+"]\n")
	// The checkout folder is "repo"; projects.yaml calls the project "app".
	if err := os.Rename(e.repo, filepath.Join(filepath.Dir(e.repo), "app")); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "new", "app", "--name", "w"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.call("new-window"), " "), "-n w -c ") {
		t.Fatalf("%v", f.call("new-window"))
	}

	err := e.app.Run(context.Background(), []string{"mecca", "new", "absent"})
	if !isUserError(err) || !strings.Contains(err.Error(), "mecca.yaml") {
		t.Fatalf("want a mapping hint, got %v", err)
	}
}

// Employer projects are named work-<repo> in projects.yaml while their checkouts keep
// the bare repo name, next to forks with the same folder name.
func TestMeccaNewFindsPrefixedProjectByOrigin(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), projectsYAML+
		"  - {name: work-service, repo: https://github.com/acme/service, identity: work, brains: [work]}\n")
	repos := filepath.Join(filepath.Dir(e.repo), "repos")
	for dir, origin := range map[string]string{
		"infra": "git@github.com:acme/service.git",
		"forks": "https://github.com/someone/service",
	} {
		co := filepath.Join(repos, dir, "service")
		gitIn(t, filepath.Dir(e.repo), "init", "-q", co)
		gitIn(t, co, "remote", "add", "origin", origin)
	}
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], ki.MeccaConfigFile), "roots: ["+repos+"]\n")

	if err := e.app.Run(context.Background(), []string{"mecca", "new", "work-service", "--task", "plan"}); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(repos, "infra", "service"))
	if err != nil {
		t.Fatal(err)
	}
	nw := f.call("new-window")
	if !slices.Contains(nw, want) || !slices.Contains(nw, "work-service/plan") {
		t.Fatalf("new-window %v, want dir %s", nw, want)
	}
	if !strings.Contains(strings.Join(f.call("set-option"), " "), "@ki-project work-service") {
		t.Fatalf("set-option %v", f.call("set-option"))
	}
}

func TestMeccaNewRelativeAndHomePaths(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	e.app.Dir = filepath.Dir(e.repo)
	if err := e.app.Run(context.Background(), []string{"mecca", "new", "./repo"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(f.call("new-window"), realRepo(t, e)) {
		t.Fatalf("%v", f.call("new-window"))
	}
	err := e.app.Run(context.Background(), []string{"mecca", "new", "~/missing"})
	if !isUserError(err) || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("got %v", err)
	}
}

func TestMeccaNewRefusals(t *testing.T) {
	tests := []struct {
		name, remote string
		args         []string
		want         string
	}{
		{"unmapped repo", "https://github.com/someone/else", []string{"--agent", "sh"}, "not in projects.yaml"},
		{"bad mode", "https://github.com/work/app", []string{"--mode", "yolo"}, "unknown mode"},
		{"family brain", "https://github.com/work/app", []string{"--brain", "client-*"}, "invalid brain name"},
		{"bad mecca.yaml", "https://github.com/work/app", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, f := meccaEnv(t, tt.remote)
			target := e.repo
			if tt.want == "" {
				write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], ki.MeccaConfigFile), "bogus: 1\n")
				target, tt.want = "app", "parsing mecca.yaml"
			}
			err := e.app.Run(context.Background(), append([]string{"mecca", "new", target}, tt.args...))
			if !isUserError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
			if f.call("new-window") != nil {
				t.Fatal("a window opened")
			}
		})
	}
}

func TestMeccaNewUnmappedWithForce(t *testing.T) {
	e, f := meccaEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"mecca", "new", e.repo, "--force"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.call("set-option"), " "), "@ki-identity unmapped") {
		t.Fatalf("%v", f.call("set-option"))
	}
	if !strings.HasSuffix(strings.Join(f.call("new-window"), " "), "/bin/ki --agent claude --force") {
		t.Fatalf("%v", f.call("new-window"))
	}
}

func TestMeccaNewTmuxErrors(t *testing.T) {
	for _, step := range []string{"new-session", "set-hook", "list-windows", "new-window", "set-option"} {
		t.Run(step, func(t *testing.T) {
			e, f := meccaEnv(t, "https://github.com/work/app")
			f.fail = map[string]error{step: errors.New("boom")}
			err := e.app.Run(context.Background(), []string{"mecca", "new", e.repo})
			if err == nil || !strings.Contains(err.Error(), "boom") {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestMeccaNewAttachInsideTmuxSwitchesClient(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	e.vars["TMUX"] = "/tmp/tmux-501/default,1,0"
	if err := e.app.Run(context.Background(), []string{"mecca", "new", e.repo, "--attach"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.call("select-window"), []string{"select-window", "-t", "@7"}) ||
		!slices.Equal(f.call("switch-client"), []string{"switch-client", "-t", "=ki-mecca"}) {
		t.Fatalf("%v", f.calls)
	}
	if e.execd != nil {
		t.Fatal("exec'd a nested tmux")
	}
}

// fakeTmuxOnPath puts an executable named tmux first on PATH, for the code
// paths that look tmux up themselves.
func fakeTmuxOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "tmux"), "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(filepath.Join(dir, "tmux"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestMeccaAttachExecsTmux(t *testing.T) {
	e, f := meccaEnv(t, "")
	fakeTmuxOnPath(t)
	f.session = true
	f.windows = []string{window("@4", 2, "app/fix", "app", time.Now(), "0")}
	e.vars["KI_MECCA_SOCKET"] = "test"
	e.vars["TMUX"] = "/tmp/tmux-501/default,1,0"
	e.app.Environ = func() []string { return []string{"PATH=/bin", "TMUX=/tmp/x,1,0"} }
	if err := e.app.Run(context.Background(), []string{"mecca", "attach", "app/fix"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.call("select-window"), []string{"select-window", "-t", "@4"}) {
		t.Fatalf("%v", f.calls)
	}
	if !slices.Equal(e.execd, []string{"tmux", "-L", "test", "attach-session", "-t", "=ki-mecca"}) {
		t.Fatalf("argv %v", e.execd)
	}
	if slices.ContainsFunc(e.execEnv, func(kv string) bool { return strings.HasPrefix(kv, "TMUX=") }) {
		t.Fatalf("TMUX kept: %v", e.execEnv)
	}
}

func TestMeccaAttachErrors(t *testing.T) {
	e, f := meccaEnv(t, "")
	f.session = true
	if err := e.app.Run(context.Background(), []string{"mecca", "attach", "a", "b"}); !isUserError(err) {
		t.Fatalf("two windows: %v", err)
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "attach", "nope"}); !isUserError(err) {
		t.Fatalf("unknown window: %v", err)
	}
	f.fail = map[string]error{"list-windows": errors.New("boom")}
	if err := e.app.Run(context.Background(), []string{"mecca", "attach", "x"}); err == nil {
		t.Fatal("list failure ignored")
	}
	f.fail = map[string]error{"switch-client": errors.New("boom")}
	e.vars["TMUX"] = "/tmp/tmux-501/default,1,0"
	if err := e.app.Run(context.Background(), []string{"mecca"}); err == nil {
		t.Fatal("switch failure ignored")
	}
}

func TestMeccaWithoutTmux(t *testing.T) {
	e := newEnv(t, "")
	t.Setenv("PATH", t.TempDir())
	for _, args := range [][]string{{"mecca"}, {"mecca", "ls"}, {"mecca", "kill", "x"}, {"mecca", "new", e.repo}} {
		err := e.app.Run(context.Background(), args)
		if !isUserError(err) || !strings.Contains(err.Error(), "brew install tmux") {
			t.Fatalf("%v: got %v", args, err)
		}
	}
}

func TestDoctorNotesTmux(t *testing.T) {
	tests := []struct {
		name          string
		install       bool
		mark, details string
	}{
		{"missing is a note", false, "·", "not installed: ki mecca needs it (brew install tmux)"},
		{"installed passes", true, "✓", ", for ki mecca"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "https://github.com/work/app")
			if tt.install {
				fakeTmuxOnPath(t)
			} else {
				t.Setenv("PATH", t.TempDir())
			}
			// Other checks fail without git on PATH; only the tmux row matters.
			if err := e.app.Run(context.Background(), []string{"doctor"}); err != nil && strings.Contains(err.Error(), "tmux") {
				t.Fatalf("tmux counted as a problem: %v", err)
			}
			for l := range strings.Lines(e.out.String()) {
				if strings.Contains(l, " tmux ") {
					if !strings.Contains(l, tt.mark) || !strings.Contains(l, tt.details) {
						t.Fatalf("tmux row %q, want %s ... %s", l, tt.mark, tt.details)
					}
					return
				}
			}
			t.Fatalf("no tmux row in:\n%s", e.out)
		})
	}
}

func TestMeccaList(t *testing.T) {
	e, f := meccaEnv(t, "")
	now := time.Now()
	f.session = true
	f.windows = []string{
		window("@1", 1, "app/fix", "app", now, "0"),
		window("@2", 2, "app/review", "app", now.Add(-time.Minute), "0"),
		window("@3", 3, "app/broken", "app", now.Add(-time.Minute), "1"),
	}
	f.capture = "output\n⏺ NEEDS USER: merge or wait?\n> \n"
	if err := e.app.Run(context.Background(), []string{"mecca", "ls"}); err != nil {
		t.Fatal(err)
	}
	out := e.out.String()
	for _, want := range []string{"WINDOW", "1:app/fix", "working", "needs-user: merge or wait?", "exited: exit status 1", " 1h "} {
		if !strings.Contains(out, want) {
			t.Errorf("ls lacks %q:\n%s", want, out)
		}
	}
	captured := 0
	for _, c := range f.calls {
		if c[0] == "capture-pane" {
			captured++
		}
	}
	if captured != 1 {
		t.Fatalf("captured %d panes, want only the quiet live one", captured)
	}

	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"mecca", "ls", "--json"}); err != nil {
		t.Fatal(err)
	}
	var ws []ki.Window
	if err := json.Unmarshal(e.out.Bytes(), &ws); err != nil || len(ws) != 3 || ws[1].Status != ki.StatusNeedsUser {
		t.Fatalf("%v %s", err, e.out)
	}
}

func TestMeccaListEdgeCases(t *testing.T) {
	e, f := meccaEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"mecca", "ls"}); err != nil || !strings.Contains(e.out.String(), "no ki-mecca windows") {
		t.Fatalf("%v %q", err, e.out)
	}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"mecca", "ls", "--json"}); err != nil || strings.TrimSpace(e.out.String()) != "[]" {
		t.Fatalf("%v %q", err, e.out)
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "ls", "-l"}); !isUserError(err) {
		t.Fatalf("bad flag: %v", err)
	}
	f.session = true
	f.windows = []string{window("@1", 1, "w", "", time.Now().Add(-time.Minute), "0")}
	f.fail = map[string]error{"capture-pane": errors.New("gone")}
	e.out.Reset()
	if err := e.app.Run(context.Background(), []string{"mecca", "ls"}); err != nil || !strings.Contains(e.out.String(), "idle") {
		t.Fatalf("%v %q", err, e.out)
	}
	f.fail = map[string]error{"list-windows": errors.New("boom")}
	if err := e.app.Run(context.Background(), []string{"mecca", "ls"}); err == nil {
		t.Fatal("list failure ignored")
	}
}

func TestMeccaKill(t *testing.T) {
	e, f := meccaEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"mecca", "kill", "x"}); !isUserError(err) || !strings.Contains(err.Error(), "no ki-mecca session") {
		t.Fatalf("no session: %v", err)
	}
	f.session = true
	f.windows = []string{window("@1", 1, "app/fix", "app", time.Now(), "0"), window("@2", 2, "other", "o", time.Now(), "0")}
	if err := e.app.Run(context.Background(), []string{"mecca", "kill", "app/fix"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.call("kill-window"), []string{"kill-window", "-t", "@1"}) || !strings.Contains(e.out.String(), "closed app/fix (@1)") {
		t.Fatalf("%v %q", f.calls, e.out)
	}
	for _, args := range [][]string{{"mecca", "kill"}, {"mecca", "kill", "nope"}} {
		if err := e.app.Run(context.Background(), args); !isUserError(err) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	f.fail = map[string]error{"kill-window": errors.New("boom")}
	if err := e.app.Run(context.Background(), []string{"mecca", "kill", "other"}); err == nil {
		t.Fatal("kill failure ignored")
	}
}

func TestMeccaHelpAndUnknown(t *testing.T) {
	e, _ := meccaEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"mecca", "help"}); err != nil || !strings.Contains(e.out.String(), "ki mecca new") {
		t.Fatalf("%v %q", err, e.out)
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "frob"}); !isUserError(err) {
		t.Fatalf("got %v", err)
	}
}

// TestMeccaRealTmux runs the real tmux runner against a private server.
func TestMeccaRealTmux(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	socket := fmt.Sprintf("ki-mecca-test-%d", os.Getpid())
	t.Cleanup(func() {
		if err := exec.CommandContext(context.Background(), bin, "-L", socket, "kill-server").Run(); err != nil { // #nosec G204 -- test socket
			t.Logf("killing the test tmux server: %v", err)
		}
	})
	app := &App{
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Getenv: func(k string) string { return map[string]string{"KI_MECCA_SOCKET": socket, "HOME": t.TempDir()}[k] },
	}
	tm, err := app.tmux()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := app.ensureSession(ctx, tm); err != nil {
		t.Fatal(err)
	}
	ws, err := listWindows(ctx, tm)
	if err != nil || len(ws) != 1 || ws[0].Name != "home" {
		t.Fatalf("%+v %v", ws, err)
	}
	if _, err := tm(ctx, "kill-window", "-t", "@999"); err == nil || !strings.Contains(err.Error(), "tmux kill-window") {
		t.Fatalf("want a wrapped tmux error, got %v", err)
	}
}

func TestMeccaNewReopensAJob(t *testing.T) {
	e, f := meccaEnv(t, "https://github.com/work/app")
	gitIn(t, e.repo, "config", "user.name", "Alice")
	gitIn(t, e.repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(filepath.Dir(e.repo), "repo-worktrees", "j-20261025-120000-ab12")
	gitIn(t, e.repo, "worktree", "add", "-q", "-b", "ki/j-20261025-120000-ab12", wt)
	ledger := filepath.Join(e.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile)
	for _, r := range []ki.Result{
		{ID: "j-20261025-120000-ab12", Provider: "codex", Session: "01a1", Status: ki.JobBlocked, Detail: "which env?", Repo: e.repo, Worktree: wt},
		{ID: "j-20261025-120100-cd34", Provider: "claude", Session: "s2", Status: ki.JobDone, Repo: e.repo},
		{ID: "j-20261025-120200-ef56", Provider: "agy", Session: "c3", Status: ki.JobBlocked, Repo: e.repo, Worktree: filepath.Join(wt, "gone")},
	} {
		if err := ki.AppendLedger(ledger, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "new", "j-20261025-120000-ab12", "--mode", "skip"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.call("new-window"), " ")
	for _, want := range []string{"-c " + realRepo(t, e) + "-worktrees/j-20261025-120000-ab12", "-- /bin/ki --agent codex --mode skip -- resume 01a1", "-n app/j-20261025-120000-ab12"} {
		if !strings.Contains(joined, want) {
			t.Errorf("new-window lacks %q: %s", want, joined)
		}
	}
	if set := strings.Join(f.call("set-option"), " "); !strings.Contains(set, "@ki-task j-20261025-120000-ab12") || !strings.Contains(set, "@ki-agent codex") {
		t.Errorf("set-option: %s", set)
	}
	for name, tc := range map[string]struct{ job, want string }{
		"unknown":  {"j-20261025-999999-0000", "not in the ledger"},
		"ran here": {"j-20261025-120100-cd34", "ran with --here"},
		"gone":     {"j-20261025-120200-ef56", "worktree is gone"},
	} {
		t.Run(name, func(t *testing.T) {
			err := e.app.Run(context.Background(), []string{"mecca", "new", tc.job})
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	if err := e.app.Run(context.Background(), []string{"mecca", "new", "j-20261025-120000-ab12", "--mode", "auto", "--agent", "agy"}); err == nil {
		t.Fatal("the job's agent, not --agent, must be checked against --mode")
	}
}
