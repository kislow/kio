package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

const projectsYAML = `sensitive_brains: [client, client-*]
identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: false, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app, voice]}
`

type env struct {
	app     *App
	out     *bytes.Buffer
	vars    map[string]string
	harness string
	repo    string
	execd   []string
	execEnv []string
}

// newEnv builds a harness checkout, a home directory and a git repo in temp dirs.
func newEnv(t *testing.T, remote string) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	base := t.TempDir()
	e := &env{out: &bytes.Buffer{}, harness: filepath.Join(base, "harness"), repo: filepath.Join(base, "repo")}
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), projectsYAML)
	write(t, filepath.Join(e.harness, "deploy", "config", "config.yaml"), "knowledge_base:\n  url: https://kb.example/\n")
	write(t, filepath.Join(e.harness, "agent", "instructions.md"), "# Instructions\n")
	home := filepath.Join(base, "home")
	e.vars = map[string]string{"HOME": home, "KI_HOME": e.harness, "KI_CONFIG_DIR": filepath.Join(home, ".config", "ki")}
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "token"), "tok\n")

	gitIn(t, base, "init", "-q", e.repo)
	gitIn(t, e.repo, "config", "user.email", "alice@work.example")
	if remote != "" {
		gitIn(t, e.repo, "remote", "add", "origin", remote)
	}
	e.app = &App{
		Stdout: e.out, Stderr: &bytes.Buffer{}, Version: "test", Dir: e.repo,
		Getenv:  func(k string) string { return e.vars[k] },
		Environ: func() []string { return []string{"PATH=/bin", "GH_TOKEN=stale"} },
		Exec: func(_ string, argv, envv []string) error {
			e.execd, e.execEnv = argv, envv
			return nil
		},
	}
	return e
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func isUserError(err error) bool {
	var ue UserError
	return errors.As(err, &ue)
}

func TestRunRefusesUnmappedRepoWithoutForce(t *testing.T) {
	e := newEnv(t, "https://github.com/someone/else")
	err := e.app.Run(context.Background(), []string{"--agent", "sh"})
	if !isUserError(err) || !strings.Contains(err.Error(), "not in projects.yaml") || e.execd != nil {
		t.Fatalf("want refusal, got %v (exec %v)", err, e.execd)
	}
}

func TestRunUnmappedWithForceIsCortexOnly(t *testing.T) {
	e := newEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh", "--", "-c", "true"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.execEnv, "KI_BRAINS=cortex") || !slices.Contains(e.execEnv, "KI_TOKEN=tok") {
		t.Fatalf("env %v", e.execEnv)
	}
	// GH_TOKEN is not set by ki for an unmapped repo, so the stale one is untouched and appears once.
	if n := countKey(e.execEnv, "GH_TOKEN"); n != 1 {
		t.Fatalf("GH_TOKEN appears %d times", n)
	}
	if !slices.Equal(e.execd, []string{"sh", "-c", "true"}) {
		t.Fatalf("argv %v", e.execd)
	}
}

func TestRunRejectsFamilyBrain(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	err := e.app.Run(context.Background(), []string{"--brain", "client-*", "--agent", "sh"})
	if !errors.Is(err, ki.ErrInvalidBrain) && !strings.Contains(fmtErr(err), "invalid brain name") {
		t.Fatalf("got %v", err)
	}
}

func TestRunChecksIdentity(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	gitIn(t, e.repo, "config", "user.email", "me@personal.example")
	err := e.app.Run(context.Background(), []string{"--agent", "sh"})
	if !isUserError(err) || !strings.Contains(err.Error(), "alice@work.example") {
		t.Fatalf("want identity refusal, got %v", err)
	}
}

func TestRunMappedRepoWithForce(t *testing.T) {
	e := newEnv(t, "git@github.com:work/app.git")
	// --force skips the gh login requirement, which CI cannot meet.
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.execEnv, "KI_BRAINS=app,voice") {
		t.Fatalf("env %v", e.execEnv)
	}
	files, _ := filepath.Glob(filepath.Join(e.vars["KI_CONFIG_DIR"], "run", "mcp-*.json"))
	if len(files) != 1 {
		t.Fatalf("mcp config files: %v", files)
	}
	info, err := os.Stat(files[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mcp config must be private: %v %v", info, err)
	}
}

func TestStatusJSON(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	if err := e.app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Project  string         `json:"project"`
		Brains   []string       `json:"brains"`
		Unmapped bool           `json:"unmapped"`
		Harness  string         `json:"harness"`
		Identity statusIdentity `json:"identity"`
		OK       bool           `json:"identity_ok"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, e.out)
	}
	if got.Project != "app" || got.Unmapped || !got.OK || got.Identity.Vault || !got.Identity.Ingest || got.Harness == "" {
		t.Fatalf("status %+v", got)
	}
}

func TestSetupClaude(t *testing.T) {
	e := newEnv(t, "")
	path := filepath.Join(e.vars["HOME"], ".claude", "settings.json")
	write(t, path, `{"model":"opus"}`)
	for i := range 2 {
		e.out.Reset()
		if err := e.app.Run(context.Background(), []string{"setup", "claude"}); err != nil {
			t.Fatal(err)
		}
		if i == 1 && !strings.Contains(e.out.String(), "already") {
			t.Fatalf("second run should be a no-op: %s", e.out)
		}
	}
	raw, _ := os.ReadFile(path)
	if !ki.ClaudeAttributionIsOff(raw) || !strings.Contains(string(raw), `"model": "opus"`) {
		t.Fatalf("settings %s", raw)
	}
}

func TestSetupCodex(t *testing.T) {
	e := newEnv(t, "")
	e.vars["CODEX_HOME"] = filepath.Join(e.vars["HOME"], "codex")
	if err := e.app.Run(context.Background(), []string{"setup", "codex"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "config.toml"))
	agents, _ := os.ReadFile(filepath.Join(e.vars["CODEX_HOME"], "AGENTS.md"))
	if !strings.Contains(string(cfg), ki.CodexTable) || !strings.Contains(string(agents), "# Instructions") {
		t.Fatalf("config.toml:\n%s\nAGENTS.md:\n%s", cfg, agents)
	}
	if err := e.app.Run(context.Background(), []string{"setup", "nope"}); !isUserError(err) {
		t.Fatalf("unknown target: %v", err)
	}
}

func TestCursorNeedsIgnoredFiles(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	// git check-ignore also reads the developer's global ignore, which ki doctor
	// asks to list the Cursor files; point git at the test's empty home instead.
	t.Setenv("HOME", e.vars["HOME"])
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(e.vars["HOME"], ".config"))
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(e.vars["HOME"], ".gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	if err := e.app.Run(context.Background(), []string{"cursor"}); !isUserError(err) || !strings.Contains(err.Error(), "not git-ignored") {
		t.Fatalf("want refusal, got %v", err)
	}
	write(t, filepath.Join(e.repo, ".gitignore"), ".cursor/\n")
	if err := e.app.Run(context.Background(), []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.repo, cursorMCP)); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	e := newEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"validate"}); err != nil || !strings.Contains(e.out.String(), "ok: 1 project(s)") {
		t.Fatalf("%v %s", err, e.out)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	write(t, bad, "identities: {x: {github_user: a, email: b, credential: c}}\n")
	if err := e.app.Run(context.Background(), []string{"validate", bad}); !isUserError(err) {
		t.Fatalf("want a user error, got %v", err)
	}
}

func TestHelpAndVersion(t *testing.T) {
	e := newEnv(t, "")
	for _, args := range [][]string{{"help"}, {"version"}} {
		if err := e.app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(e.out.String(), "ki setup claude") || !strings.Contains(e.out.String(), "test") {
		t.Fatalf("output %s", e.out)
	}
}

func TestBareCommandsPrintHelpOrRefuseArguments(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"update", "doctor", "cursor"} {
		for _, flag := range []string{"help", "-h", "--help"} {
			e := newEnv(t, "")
			if err := e.app.Run(context.Background(), []string{name, flag}); err != nil {
				t.Fatalf("%s %s: %v", name, flag, err)
			}
			if got := e.out.String(); !strings.HasPrefix(got, "  ki "+name) || strings.Contains(got, "ki doctor") != (name == "doctor") {
				t.Fatalf("%s %s printed %q, want its usage entry alone", name, flag, got)
			}
		}
		e := newEnv(t, "")
		err := e.app.Run(context.Background(), []string{name, "--force"})
		if !isUserError(err) || !strings.Contains(err.Error(), "takes no arguments") || e.out.Len() != 0 {
			t.Fatalf("%s --force: want a refusal before running, got %v, output %q", name, err, e.out)
		}
	}
}

func TestUsageLineKeepsContinuationLines(t *testing.T) {
	t.Parallel()
	got := usageLine("update")
	if !strings.Contains(got, "release binary:") || strings.Contains(got, "ki doctor") {
		t.Fatalf("update entry: %q", got)
	}
}

func countKey(envv []string, key string) int {
	n := 0
	for _, kv := range envv {
		if strings.HasPrefix(kv, key+"=") {
			n++
		}
	}
	return n
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestDoctorReportsProblems(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	kb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	t.Cleanup(kb.Close)
	e.vars["KI_MCP_URL"] = kb.URL
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "token-sensitive"), "p\n")
	if err := os.Chmod(filepath.Join(e.vars["KI_CONFIG_DIR"], "token-sensitive"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := e.app.Run(context.Background(), []string{"doctor"})

	out := e.out.String()
	for _, want := range []string{"✓ checkout", "✓ device token", "✓ knowledge base", "✗ sensitive token", "token-sensitive is readable by others", "fix: chmod 600", "global ignore", ".cursor/mcp.json", "✓ commit email", "alice@work.example in this repo", "problem", "checks\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if !isUserError(err) {
		t.Fatalf("doctor with problems must fail: %v", err)
	}
}

func TestStatusText(t *testing.T) {
	e := newEnv(t, "")
	if err := e.app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "unmapped repo: ki needs --force here") {
		t.Fatalf("status %s", e.out)
	}
}

func TestTokenProblems(t *testing.T) {
	e := newEnv(t, "")
	tok := filepath.Join(e.vars["KI_CONFIG_DIR"], "token")
	write(t, tok, "  \n")
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); !isUserError(err) || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty token: %v", err)
	}
	if err := os.Remove(tok); err != nil {
		t.Fatal(err)
	}
	if err := e.app.Run(context.Background(), []string{"--force", "--agent", "sh"}); !isUserError(err) || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing token: %v", err)
	}
}

func TestSetupClaudeRefusesBrokenSettings(t *testing.T) {
	e := newEnv(t, "")
	write(t, filepath.Join(e.vars["HOME"], ".claude", "settings.json"), `{"attribution": "off"}`)
	if err := e.app.Run(context.Background(), []string{"setup", "claude"}); !isUserError(err) {
		t.Fatalf("want a user error, got %v", err)
	}
}

func TestHarnessFindsCheckoutUnderAnyName(t *testing.T) {
	for _, name := range []string{"kio", "old-checkout"} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, "")
			dir := filepath.Join(filepath.Dir(e.harness), name)
			if err := os.Rename(e.harness, dir); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(e.vars["KI_CONFIG_DIR"], "harness")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}
			want, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, home := range []string{dir, ""} {
				e.vars["KI_HOME"] = home
				if got, err := e.app.harness(); err != nil || got != want {
					t.Fatalf("KI_HOME=%q: got %q, %v; want %q", home, got, err, want)
				}
			}
		})
	}
}

// fakeAgent is an executable named like a real agent, so ki maps its mode flags
// without the agent being installed.
func fakeAgent(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- test executable
		t.Fatal(err)
	}
	return path
}

func TestRunMode(t *testing.T) {
	tests := []struct {
		name     string
		kiMode   string
		args     []string
		wantArgv []string
		banner   string
		errText  string
	}{
		{"default is ask", "", nil, []string{"--sandbox", "workspace-write", "--ask-for-approval", "on-request"}, "mode=ask", ""},
		{"KI_MODE sets the default", "skip", nil, []string{"--dangerously-bypass-approvals-and-sandbox"}, "mode=skip", ""},
		{"--mode beats KI_MODE", "skip", []string{"--mode", "auto"}, []string{"--approve-for-me"}, "mode=auto", ""},
		{"agent args win", "", []string{"--mode", "auto", "--", "--sandbox", "read-only"}, []string{"--sandbox", "read-only"}, "mode=set by agent args", ""},
		{"unknown mode", "", []string{"--mode", "yolo"}, nil, "", `unknown mode "yolo"`},
		{"bad KI_MODE", "always", nil, nil, "", `unknown mode "always"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "")
			e.vars["KI_MODE"] = tt.kiMode
			codex := fakeAgent(t, "codex")
			err := e.app.Run(t.Context(), append([]string{"--force", "--agent", codex}, tt.args...))
			if tt.errText != "" {
				if !isUserError(err) || !strings.Contains(err.Error(), tt.errText) || e.execd != nil {
					t.Fatalf("want %q, got %v (exec %v)", tt.errText, err, e.execd)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if want := append([]string{codex}, tt.wantArgv...); !slices.Equal(e.execd, want) {
				t.Fatalf("argv %v, want %v", e.execd, want)
			}
			if banner := e.app.Stderr.(*bytes.Buffer).String(); !strings.Contains(banner, tt.banner) {
				t.Fatalf("banner %q, want %q", banner, tt.banner)
			}
		})
	}
}

func TestRunRefusesModeTheAgentLacks(t *testing.T) {
	e := newEnv(t, "")
	err := e.app.Run(t.Context(), []string{"--force", "--agent", fakeAgent(t, "agy"), "--mode", "auto"})
	if !isUserError(err) || !strings.Contains(err.Error(), "agy has no auto mode") || e.execd != nil {
		t.Fatalf("got %v (exec %v)", err, e.execd)
	}
}

func TestRunEditsMode(t *testing.T) {
	const allowYAML = "identities: [work]\ndelegate:\n  allow: [git, gh pr]\n"
	tests := []struct {
		name, agent, machine, kiMode string
		args                         []string
		want                         []string // in the argv, in this order
		absent                       []string
		agyRules                     []string // in agy's settings afterwards; nil: no file
		errText                      string
	}{
		{
			"claude with an allow-list", "claude", allowYAML, "",
			[]string{"--mode", "edits"},
			[]string{"--permission-mode", "acceptEdits", "--allowedTools=Bash(git:*)", "--allowedTools=Bash(gh pr:*)"},
			nil, nil, "",
		},
		{
			"claude without one prompts for commands", "claude", "", "edits", nil,
			[]string{"--permission-mode", "acceptEdits"},
			[]string{"--allowedTools=Bash(git:*)"},
			nil, "",
		},
		{
			"agy with an allow-list", "agy", allowYAML, "",
			[]string{"--mode", "edits"},
			[]string{"--mode", "accept-edits"},
			[]string{"--allowedTools=Bash(git:*)"},
			[]string{"command(git)", "command(gh pr)"},
			"",
		},
		{"agy without one", "agy", "", "", []string{"--mode", "edits"}, []string{"--mode", "accept-edits"}, nil, nil, ""},
		{"agy, agent args win", "agy", allowYAML, "", []string{"--mode", "edits", "--", "--mode", "plan"}, []string{"--mode", "plan"}, []string{"accept-edits"}, nil, ""},
		{"codex refused", "codex", allowYAML, "", []string{"--mode", "edits"}, nil, nil, nil, "codex has no edits mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, "")
			e.vars["KI_MODE"] = tt.kiMode
			settings := filepath.Join(t.TempDir(), "agy.json")
			e.vars["KI_AGY_SETTINGS"] = settings
			if tt.machine != "" {
				write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), tt.machine)
			}
			agent := fakeAgent(t, tt.agent)
			err := e.app.Run(t.Context(), append([]string{"--force", "--agent", agent}, tt.args...))
			if tt.errText != "" {
				if !isUserError(err) || !strings.Contains(err.Error(), tt.errText) || e.execd != nil {
					t.Fatalf("want %q, got %v (exec %v)", tt.errText, err, e.execd)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			argv := strings.Join(e.execd, "\x00")
			if !strings.Contains(argv, strings.Join(tt.want, "\x00")) {
				t.Fatalf("argv %v, want %v in it", e.execd, tt.want)
			}
			for _, a := range tt.absent {
				if slices.Contains(e.execd, a) {
					t.Fatalf("%q must not be in %v", a, e.execd)
				}
			}
			raw, readErr := os.ReadFile(settings)
			if tt.agyRules == nil {
				if readErr == nil {
					t.Fatalf("agy's settings must stay untouched: %s", raw)
				}
				return
			}
			var s struct {
				Permissions struct {
					Allow []string `json:"allow"`
				} `json:"permissions"`
			}
			if err := json.Unmarshal(raw, &s); err != nil || !slices.Equal(s.Permissions.Allow, tt.agyRules) {
				t.Fatalf("settings %s (%v)", raw, err)
			}
			if banner := e.app.Stderr.(*bytes.Buffer).String(); !strings.Contains(banner, "mode=edits") || !strings.Contains(banner, "added 2 command rules") {
				t.Fatalf("banner %q", banner)
			}
		})
	}
}

func TestStatusShowsMode(t *testing.T) {
	e := newEnv(t, "")
	e.vars["KI_MODE"] = "auto"
	if err := e.app.Run(t.Context(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Mode *string `json:"mode"`
	}
	if err := json.Unmarshal(e.out.Bytes(), &got); err != nil || got.Mode == nil || *got.Mode != "auto" {
		t.Fatalf("status %s (%v)", e.out, err)
	}

	e.out.Reset()
	e.vars["KI_MODE"] = "edits"
	if err := e.app.Run(t.Context(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), "mode:      edits\n") {
		t.Fatalf("status %s", e.out)
	}

	e.out.Reset()
	e.vars["KI_MODE"] = "yolo"
	if err := e.app.Run(t.Context(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.out.String(), `mode:      KI_MODE: unknown mode "yolo"`) {
		t.Fatalf("status %s", e.out)
	}
}
