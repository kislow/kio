package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/ki"
)

// fakeWorker is one script installed under three agent names. It records its
// arguments and working directory, prints $RESULT, writes $LAST to Codex's
// --output-last-message file, and exits with $EXIT.
const fakeWorker = `#!/bin/sh
printf '%s\0' "$@" > "$ARGS_FILE"
pwd > "$CWD_FILE"
[ -n "$RUNS_FILE" ] && printf 'x\n' >> "$RUNS_FILE"
if [ -n "$FAIL_ON_RESUME" ]; then
  for a in "$@"; do case "$a" in --resume|resume) echo "No conversation found with session ID" >&2; exit 1 ;; esac; done
fi
[ -n "$COPY_CURSOR_MCP" ] && [ -f .cursor/mcp.json ] && cp .cursor/mcp.json "$COPY_CURSOR_MCP" && cp .cursor/cli.json "$COPY_CURSOR_MCP.cli"
prev=""
for a in "$@"; do
  if [ "$prev" = "--output-last-message" ] && [ -n "$LAST" ]; then printf '%s\n' "$LAST" > "$a"; fi
  if [ "$prev" = "--mcp-config" ] && [ -n "$COPY_MCP_CONFIG" ]; then cp "$a" "$COPY_MCP_CONFIG"; fi
  prev="$a"
done
[ -n "$DIRTY_FILE" ] && printf 'x\n' > "$DIRTY_FILE"
[ -n "$COMMIT_MSG" ] && git -c user.name=w -c user.email=w@example.com commit -q --allow-empty -m "$COMMIT_MSG"
[ -n "$SLEEP" ] && sleep "$SLEEP"
if [ -n "$EVENTS_FILE" ]; then
  while IFS= read -r l; do printf '%s\n' "$l"; sleep "${EVENT_GAP:-0}"; done < "$EVENTS_FILE"
fi
[ -n "$SLEEP_AFTER" ] && sleep "$SLEEP_AFTER"
case "$0" in *codex) [ -n "$EXIT_IF_CODEX" ] && exit "$EXIT_IF_CODEX" ;; esac
printf '%s\n' "$RESULT"
[ -n "$SLEEP_END" ] && sleep "$SLEEP_END"
exit ${EXIT:-0}
`

const claudeDone = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: hi","session_id":"9d6e","total_cost_usd":0.007,"usage":{"input_tokens":2,"output_tokens":8,"cache_read_input_tokens":11803}}`

func TestClassifyAgyResultErrorAsFailedRun(t *testing.T) {
	t.Parallel()
	status, detail := classify(ki.Outcome{Session: "started", Err: "quota reached"}, 3, errors.New("exit status 3"))
	if status != ki.JobFailed || detail != "quota reached" {
		t.Fatalf("got %s %q", status, detail)
	}
}

const cursorBlocked = `{"type":"result","subtype":"success","is_error":false,"result":"BLOCKED: which env?","session_id":"366e","usage":{"inputTokens":8773,"outputTokens":1679,"cacheReadTokens":6016}}`

const codexEvents = `{"type":"thread.started","thread_id":"01a1"}
{"type":"item.completed","item":{"type":"agent_message","text":"PR: https://github.com/o/r/pull/1"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":9}}`

type delegateEnv struct {
	*env
	argsFile, cwdFile string
	worker            map[string]string // the worker's environment
}

// newDelegateEnv is newEnv with a first commit (worktrees need one), the fake
// agents on a PATH that holds no real ones, and a controllable worker environment.
func newDelegateEnv(t *testing.T) *delegateEnv {
	t.Helper()
	e := newEnv(t, "")
	gitIn(t, e.repo, "config", "user.name", "Alice")
	gitIn(t, e.repo, "commit", "-q", "--allow-empty", "-m", "init")
	bin := filepath.Join(t.TempDir(), "bin")
	for _, name := range []string{"claude", "codex", "agent", "agy"} {
		write(t, filepath.Join(bin, name), fakeWorker)
		if err := os.Chmod(filepath.Join(bin, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", strings.Join([]string{bin, "/usr/bin", "/bin"}, string(os.PathListSeparator)))
	// The fake github.com origins must fail at once, not reach the network.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "http.proxy")
	t.Setenv("GIT_CONFIG_VALUE_0", "http://127.0.0.1:1")
	d := &delegateEnv{env: e, argsFile: filepath.Join(t.TempDir(), "args"), cwdFile: filepath.Join(t.TempDir(), "cwd"), worker: map[string]string{}}
	e.app.Environ = func() []string {
		out := make([]string, 0, 3+len(d.worker))
		out = append(out, "PATH="+os.Getenv("PATH"), "ARGS_FILE="+d.argsFile, "CWD_FILE="+d.cwdFile)
		for k, v := range d.worker {
			out = append(out, k+"="+v)
		}
		return out
	}
	return d
}

func (d *delegateEnv) args(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(d.argsFile)
	if err != nil {
		t.Fatalf("the worker did not run: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
}

func (d *delegateEnv) cwd(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(d.cwdFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(raw))
}

func (d *delegateEnv) result(t *testing.T) ki.Result {
	t.Helper()
	var r ki.Result
	if err := json.Unmarshal(d.out.Bytes(), &r); err != nil {
		t.Fatalf("not a result: %v\n%s", err, d.out.String())
	}
	return r
}

func (d *delegateEnv) delegate(args ...string) error {
	d.out.Reset()
	return d.app.Run(context.Background(), append([]string{"delegate"}, args...))
}

func TestDelegateRunsClaudeInAWorktreeAndRecordsTheJob(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nmodels:\n  execution: {claude: sonnet}\neffort:\n  execution: medium\n")
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--task", "fix the build", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Status != ki.JobDone || r.Detail != "hi" || r.Provider != "claude" || r.Model != "sonnet" || r.Session != "9d6e" || r.Exit != 0 {
		t.Fatalf("result %+v", r)
	}
	if r.Tokens != (ki.Tokens{Input: 2, Output: 8, CacheRead: 11803}) || r.CostUSD != 0.007 || r.Last != "DONE: hi" {
		t.Fatalf("usage not read: %+v", r)
	}
	args := d.args(t)
	for _, want := range []string{"--model", "sonnet", "--effort", "medium", "--permission-mode", "auto", "--print", "--output-format", "stream-json", "--verbose"} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	if task := args[len(args)-1]; !strings.HasPrefix(task, "fix the build\n\n") || !strings.Contains(task, "BLOCKED: <the one question") {
		t.Errorf("task is not the wrapped brief: %q", task)
	}
	wantDir := filepath.Join(filepath.Dir(d.repo), filepath.Base(d.repo)+"-worktrees", r.ID)
	if mustEval(t, r.Worktree) != mustEval(t, wantDir) || r.Branch != "chore/"+r.ID {
		t.Fatalf("worktree %s on %s, want %s", r.Worktree, r.Branch, wantDir)
	}
	if cwd, err := filepath.EvalSymlinks(d.cwd(t)); err != nil || cwd != mustEval(t, wantDir) {
		t.Fatalf("worker ran in %s, want %s", cwd, wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, ".git")); err != nil {
		t.Fatalf("no worktree: %v", err)
	}
	jobDir := filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", r.ID)
	for _, f := range []string{"brief.md", "result.json", "output.log"} {
		if _, err := os.Stat(filepath.Join(jobDir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != 1 || jobs[0].ID != r.ID || jobs[0].Last != "" {
		t.Fatalf("ledger %+v, %v", jobs, err)
	}

	if err := d.delegate("ls"); err != nil {
		t.Fatal(err)
	}
	if out := d.out.String(); !strings.Contains(out, r.ID) || !strings.Contains(out, "DONE") || !strings.HasPrefix(out, "JOB") {
		t.Fatalf("ls: %s", out)
	}
	if err := d.delegate("ls", "--json"); err != nil {
		t.Fatal(err)
	}
	var listed []ki.Result
	if err := json.Unmarshal(d.out.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("ls --json: %v %s", err, d.out.String())
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDelegateHereRunsInTheRepo(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--task", "t", "--force", "--here"); err != nil {
		t.Fatal(err)
	}
	if cwd := mustEval(t, d.cwd(t)); cwd != mustEval(t, d.repo) {
		t.Fatalf("ran in %s", cwd)
	}
	if out := d.out.String(); !strings.HasPrefix(out, "j-") || !strings.HasSuffix(out, " claude DONE: hi\n") {
		t.Fatalf("text output: %q", out)
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(d.repo), filepath.Base(d.repo)+"-worktrees")); len(entries) != 0 {
		t.Fatal("--here must not add a worktree")
	}
}

func TestDelegateCodexUsesExecAndTheLastMessageFile(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"], d.vars["LAST"] = codexEvents, "PR: https://github.com/o/r/pull/1"
	if err := d.delegate("--agent", "codex", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Status != ki.JobPR || r.Detail != "https://github.com/o/r/pull/1" || r.Session != "01a1" || r.Tokens.CacheRead != 40 {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	if args[0] != "exec" || !slices.Contains(args, "--approve-for-me") || !slices.Contains(args, "--json") {
		t.Fatalf("args %v", args)
	}
	i := slices.Index(args, "--output-last-message")
	if i < 0 || args[i+1] != filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", r.ID, "last.txt") {
		t.Fatalf("last message file: %v", args)
	}
}

// A delegated Cursor worker runs in its sandbox, never with --force, which
// would override the sandbox.
func TestDelegateCursorGetsTrustAndSandbox(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = cursorBlocked
	if err := d.delegate("--agent", "agent", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobBlocked || r.Detail != "which env?" || r.Provider != "agent" {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	for _, want := range []string{"--sandbox", "enabled", "--print", "--trust"} {
		if !slices.Contains(args, want) {
			t.Errorf("%q missing from %v", want, args)
		}
	}
	if slices.Contains(args, "--force") {
		t.Errorf("--force overrides the sandbox: %v", args)
	}
}

// Auto mode leaves the worktree with a sandbox policy for api.github.com that
// git ignores, so the worker's commits cannot pick it up.
func TestDelegateCursorAutoAllowsGitHubAPIOutsideGit(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = cursorBlocked
	if err := d.delegate("--agent", "agent", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	data, err := os.ReadFile(filepath.Join(r.Worktree, ".cursor", "sandbox.json"))
	if err != nil || !strings.Contains(string(data), "api.github.com") {
		t.Fatalf("policy: %v %s", err, data)
	}
	cmd := exec.CommandContext(t.Context(), "git", "status", "--porcelain")
	cmd.Dir = r.Worktree
	if out, err := cmd.Output(); err != nil || strings.Contains(string(out), ".cursor") {
		t.Errorf("sandbox.json is visible to git: %v %q", err, out)
	}
}

func TestDelegateAgyRunsHeadlessWithPermissionsSkipped(t *testing.T) {
	d := newDelegateEnv(t)
	d.worker["RESULT"] = `{"conversation_id":"e209","status":"SUCCESS","response":"DONE: hi\n","usage":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":0}}`
	if err := d.delegate("--agent", "agy", "--model", "gemini-3.8-flash-low", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobDone || r.Session != "e209" || r.Provider != "agy" || r.Tokens.Input != 10 {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	want := []string{"--model", "gemini-3.8-flash-low", "--dangerously-skip-permissions", "--output-format", "json", "--print"}
	if !slices.Equal(args[:len(want)], want) || !strings.HasPrefix(args[len(want)], "t\n\n") {
		t.Fatalf("args %v", args)
	}
}

func TestDelegateOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		vars           map[string]string
		args           []string
		status, detail string
		wantErr        string
	}{
		"launch failure":       {map[string]string{"RESULT": "", "EXIT": "1"}, nil, ki.JobLaunchFailed, "exit status 1", "did not start a run"},
		"agent error":          {map[string]string{"RESULT": `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","session_id":"s"}`}, nil, ki.JobFailed, "max_turns", ""},
		"bad exit after a run": {map[string]string{"RESULT": claudeDone, "EXIT": "2"}, nil, ki.JobFailed, "exit 2: exit status 2", ""},
		"no status line":       {map[string]string{"RESULT": `{"type":"result","result":"did things","session_id":"s"}`}, nil, ki.JobFailed, "no status line in the final message", ""},
		"timeout":              {map[string]string{"RESULT": claudeDone, "SLEEP": "3"}, []string{"--timeout", "200ms"}, ki.JobFailed, "timed out: killed after 200ms", ""},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			d.worker = tc.vars
			err := d.delegate(append([]string{"--task", "t", "--force", "--here", "--json"}, tc.args...)...)
			if tc.wantErr == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" && (!isUserError(err) || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("want error %q, got %v", tc.wantErr, err)
			}
			r := d.result(t)
			if r.Status != tc.status || r.Detail != tc.detail {
				t.Fatalf("got %s %q, want %s %q", r.Status, r.Detail, tc.status, tc.detail)
			}
			jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
			if err != nil || len(jobs) != 1 || jobs[0].Status != tc.status {
				t.Fatalf("ledger %+v %v", jobs, err)
			}
		})
	}
}

func TestDelegateRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no task":        {[]string{"--force"}, "usage: ki delegate"},
		"ask mode":       {[]string{"--task", "t", "--mode", "ask", "--force"}, "cannot answer prompts"},
		"unknown agent":  {[]string{"--task", "t", "--agent", "sh", "--force"}, "headless"},
		"bad timeout":    {[]string{"--task", "t", "--timeout", "soon", "--force"}, "--timeout"},
		"unmapped repo":  {[]string{"--task", "t"}, "not in projects.yaml"},
		"missing binary": {[]string{"--task", "t", "--agent", "cursor-agent", "--force"}, "not found on PATH"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			err := d.delegate(tc.args...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("no worker may run")
			}
		})
	}
}

func TestParseDelegateArgs(t *testing.T) {
	da, err := parseDelegateArgs([]string{"--task", "do", "--timeout", "5m", "--here", "--json", "--agent", "codex", "--model", "m", "--", "--x"}, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if da.Task != "do" || da.Timeout != 5*time.Minute || !da.Here || !da.JSON || da.Agent != "codex" || da.Model != "m" || !reflect.DeepEqual(da.AgentArgs, []string{"--x"}) {
		t.Fatalf("got %+v", da)
	}
	if da, err := parseDelegateArgs([]string{"--agent", "codex"}, "claude"); err == nil {
		t.Fatalf("no task must fail, got %+v", da)
	}
	if da, err := parseDelegateArgs(nil, "claude"); err == nil || da.Timeout != delegateTimeout {
		t.Fatalf("defaults: %+v %v", da, err)
	}
}

func TestDelegateMode(t *testing.T) {
	t.Parallel()
	allow := []string{"git", "go"}
	tests := []struct {
		name             string
		agent, requested string
		agentArgs, allow []string
		want             ki.Mode
		wantArgs         []string
		wantErr          string
	}{
		{"claude defaults to auto", "claude", "", nil, nil, ki.ModeAuto, []string{"--permission-mode", "auto"}, ""},
		{"claude keeps auto with an allow-list", "claude", "", nil, allow, ki.ModeAuto, []string{"--permission-mode", "auto"}, ""},
		{"cursor defaults to its sandbox", "cursor-agent", "", nil, nil, ki.ModeAuto, []string{"--sandbox", "enabled"}, ""},
		{"cursor keeps the sandbox with an allow-list", "cursor-agent", "", nil, allow, ki.ModeAuto, []string{"--sandbox", "enabled"}, ""},
		{"cursor explicit skip", "cursor-agent", "skip", nil, nil, ki.ModeSkip, []string{"--force"}, ""},
		{"agy falls back to skip", "agy", "", nil, nil, ki.ModeSkip, []string{"--dangerously-skip-permissions"}, ""},
		{"agy defaults to edits with an allow-list", "agy", "", nil, allow, ki.ModeEdits, []string{"--mode", "accept-edits"}, ""},
		{"agy auto refused", "agy", "auto", nil, allow, "", nil, "agy has no auto mode"},
		{"explicit skip", "codex", "skip", nil, nil, ki.ModeSkip, []string{"--dangerously-bypass-approvals-and-sandbox"}, ""},
		{"explicit skip beats the allow-list", "agy", "skip", nil, allow, ki.ModeSkip, []string{"--dangerously-skip-permissions"}, ""},
		{"claude edits", "claude", "edits", nil, allow, ki.ModeEdits, []string{"--permission-mode", "acceptEdits", "--allowedTools=Bash(git:*)", "--allowedTools=Bash(go:*)"}, ""},
		{"edits without an allow-list", "agy", "edits", nil, nil, "", nil, "list them under delegate.allow in machine.yaml"},
		{"claude edits without an allow-list", "claude", "edits", nil, nil, "", nil, "delegate.allow"},
		{"edits overridden needs no allow-list", "agy", "edits", []string{"--mode", "plan"}, nil, ki.ModeEdits, nil, ""},
		{"codex edits refused", "codex", "edits", nil, allow, "", nil, "codex has no edits mode"},
		{"cursor edits refused", "cursor-agent", "edits", nil, allow, "", nil, "cursor-agent has no edits mode"},
		{"ask refused", "claude", "ask", nil, nil, "", nil, "use --mode auto, edits or skip"},
		{"cursor edits refused", "cursor-agent", "edits", nil, allow, "", nil, "no edits mode"},
		{"unknown", "claude", "yolo", nil, nil, "", nil, "use ask, auto, edits or skip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			wm, err := delegateMode(tt.agent, tt.requested, tt.agentArgs, tt.allow)
			if tt.wantErr != "" {
				if !isUserError(err) || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil || wm.mode != tt.want || !slices.Equal(wm.args, tt.wantArgs) {
				t.Fatalf("got %+v, %v", wm, err)
			}
			if wm.overridden != (tt.agentArgs != nil) || wm.edits() != (tt.want == ki.ModeEdits && tt.agentArgs == nil) {
				t.Fatalf("overridden %v, edits %v", wm.overridden, wm.edits())
			}
		})
	}
}

// editsYAML gives the test machine an allow-list.
const editsYAML = "identities: [work]\ndelegate:\n  allow: [git, go]\n"

func TestDelegateAgyDefaultsToEdits(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), editsYAML)
	settings := filepath.Join(t.TempDir(), "agy", "settings.json")
	d.vars["KI_AGY_SETTINGS"] = settings
	write(t, settings, `{"colorScheme":"dark","permissions":{"allow":["command(git)"],"deny":["command(rm)"]}}`)
	d.worker["RESULT"] = `{"conversation_id":"e7","status":"SUCCESS","response":"DONE: edited\n"}`
	if err := d.delegate("--agent", "agy", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Status != ki.JobDone || r.Detail != "edited" {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	want := []string{"--mode", "accept-edits", "--output-format", "json", "--print"}
	if !slices.Equal(args[:len(want)], want) || slices.Contains(args, "--dangerously-skip-permissions") {
		t.Fatalf("args %v", args)
	}
	brief := args[len(want)]
	for _, s := range []string{"t\n\nYou run in edits mode.", "git, go", "not the shell", "ends the job without a result", "FAILED: <reason>"} {
		if !strings.Contains(brief, s) {
			t.Errorf("brief misses %q: %q", s, brief)
		}
	}
	got, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		ColorScheme string `json:"colorScheme"`
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(got, &s); err != nil {
		t.Fatal(err)
	}
	if s.ColorScheme != "dark" || !slices.Equal(s.Permissions.Allow, []string{"command(git)", "command(go)"}) || !slices.Equal(s.Permissions.Deny, []string{"command(rm)"}) {
		t.Fatalf("settings %s", got)
	}
	stderr := d.app.Stderr.(*bytes.Buffer).String()
	if !strings.Contains(stderr, "ki: added 1 command rule to agy settings: go\n") || !strings.Contains(stderr, "mode=edits") {
		t.Fatalf("stderr %q", stderr)
	}
}

func TestDelegateAgyEditsRefusedCommand(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), editsYAML)
	d.vars["KI_AGY_SETTINGS"] = filepath.Join(t.TempDir(), "settings.json")
	d.worker["RESULT"] = `{"conversation_id":"e8","status":"SUCCESS","response":"","denied_actions":[{"action":"command","display_name":"RunCommand"}]}`
	if err := d.delegate("--agent", "agy", "--mode", "edits", "--task", "t", "--force", "--here", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Status != ki.JobFailed || !strings.Contains(r.Detail, "not on the allow-list") || !strings.Contains(r.Detail, "delegate.allow") {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(d.app.Stderr.(*bytes.Buffer).String(), "ki: added 2 command rules to agy settings: git, go\n") {
		t.Fatalf("stderr %q", d.app.Stderr)
	}
}

func TestDelegateClaudeEditsPassesTheAllowList(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), editsYAML)
	settings := filepath.Join(t.TempDir(), "settings.json")
	d.vars["KI_AGY_SETTINGS"] = settings
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--agent", "claude", "--mode", "edits", "--task", "t", "--force", "--here", "--json"); err != nil {
		t.Fatal(err)
	}
	args := d.args(t)
	for _, want := range []string{"acceptEdits", "--allowedTools=Bash(git:*)", "--allowedTools=Bash(go:*)"} {
		if !slices.Contains(args, want) {
			t.Errorf("%q missing from %v", want, args)
		}
	}
	if !strings.Contains(args[len(args)-1], "You run in edits mode.") {
		t.Fatalf("brief %q", args[len(args)-1])
	}
	if _, err := os.Stat(settings); err == nil {
		t.Fatal("claude must not touch agy's settings")
	}
}

func TestDelegateEditsRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		machine, settings string
		args              []string
		want              string
	}{
		"no allow-list":       {"identities: [work]\n", "", []string{"--agent", "agy", "--mode", "edits"}, "delegate.allow"},
		"codex has no edits":  {editsYAML, "", []string{"--agent", "codex", "--mode", "edits"}, "codex has no edits mode"},
		"bad agy settings":    {editsYAML, "{not json", []string{"--agent", "agy"}, "is not valid JSON"},
		"wildcard allow-list": {"identities: [work]\ndelegate:\n  allow: ['*']\n", "", []string{"--agent", "agy"}, "wildcards are refused"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), tc.machine)
			settings := filepath.Join(t.TempDir(), "settings.json")
			d.vars["KI_AGY_SETTINGS"] = settings
			if tc.settings != "" {
				write(t, settings, tc.settings)
			}
			err := d.delegate(append(tc.args, "--task", "t", "--force", "--here")...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("no worker may run")
			}
			if tc.settings != "" {
				if got, _ := os.ReadFile(settings); string(got) != tc.settings {
					t.Fatalf("settings changed: %q", got)
				}
			}
		})
	}
}

const rolesYAML = "identities: [work]\nroles:\n  execution:\n    strategy: ordered\n    providers: [codex:gpt-5-mini, claude:sonnet]\n"

func TestDelegateRoleFallsThroughOnLaunchFailure(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), rolesYAML)
	// The fake script serves every agent; codex is told to die before producing anything, claude answers.
	d.worker["RESULT"] = claudeDone
	d.worker["EXIT_IF_CODEX"] = "1"
	if err := d.delegate("--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Provider != "claude" || r.Model != "sonnet" || r.Role != "execution" || r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != 2 || jobs[0].Provider != "codex" || jobs[0].Status != ki.JobLaunchFailed || jobs[0].Role != "execution" || jobs[1].ID != r.ID {
		t.Fatalf("ledger %+v %v", jobs, err)
	}
	if jobs[0].Worktree != jobs[1].Worktree || jobs[0].Worktree == "" {
		t.Fatalf("both attempts share the worktree: %+v", jobs)
	}

	// The breaker now skips codex without trying it; the explicit agent path still names it.
	d.worker["EXIT_IF_CODEX"] = ""
	if err := d.delegate("--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "claude" {
		t.Fatalf("breaker: %+v", r)
	}
	if !strings.Contains(d.app.Stderr.(*bytes.Buffer).String(), "codex:gpt-5-mini skipped, launch failed") {
		t.Fatalf("stderr %s", d.app.Stderr.(*bytes.Buffer).String())
	}
	d.worker["RESULT"], d.worker["LAST"] = codexEvents, "DONE: codex ran"
	if err := d.delegate("--task", "t", "--force", "--agent", "codex", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Provider != "codex" || r.Role != "" || r.Detail != "codex ran" {
		t.Fatalf("--agent: %+v", r)
	}
}

func TestDelegateRoundRobinTakesTurns(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"),
		"identities: [work]\nroles:\n  execution:\n    strategy: round-robin\n    providers: [claude:sonnet, agy:gemini]\n")
	d.worker["RESULT"] = claudeDone // agy's parser falls back to the raw text, which has the status line inside
	got := make([]string, 0, 3)
	for range 3 {
		if err := d.delegate("--role", "execution", "--task", "t", "--force", "--json"); err != nil {
			t.Fatal(err)
		}
		got = append(got, d.result(t).Provider)
	}
	if !slices.Equal(got, []string{"claude", "agy", "claude"}) {
		t.Fatalf("turns %v", got)
	}
}

func TestDelegateRoleRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		machine string
		args    []string
		want    string
	}{
		"role with agent":  {rolesYAML, []string{"--role", "execution", "--agent", "codex"}, "exclusive"},
		"role with model":  {rolesYAML, []string{"--role", "execution", "--model", "x"}, "--model with --role"},
		"unknown role":     {rolesYAML, []string{"--role", "search"}, `role "search" is not in`},
		"no roles defined": {"identities: [work]\n", []string{"--role", "execution"}, "defined: none"},
		"headless unknown": {"identities: [work]\nroles:\n  execution: {providers: [aider]}\n", nil, "cannot run headless"},
		"none available":   {"identities: [work]\nroles:\n  execution: {providers: [cursor-agent]}\n", nil, "no provider available"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), tc.machine)
			err := d.delegate(append([]string{"--task", "t", "--force"}, tc.args...)...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("no worker may run")
			}
		})
	}
}

func TestDoctorShowsRoles(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"), rolesYAML)
	_ = e.app.Run(context.Background(), []string{"doctor"}) // other checks may fail in a temp env
	if out := e.out.String(); !strings.Contains(out, "ordered over codex:gpt-5-mini, claude:sonnet") {
		t.Fatalf("doctor:\n%s", out)
	}
}

// Four delegations at once in one repo: the worktrees must all be created,
// which fails when each branch writes tracking config into .git/config.
func TestDelegateRunsInParallelInOneRepo(t *testing.T) {
	d := newDelegateEnv(t)
	gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	gitIn(t, d.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, d.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	d.worker["RESULT"] = claudeDone
	const n = 4
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			app := *d.app
			app.Stdout, app.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
			errs <- app.Run(context.Background(), []string{"delegate", "--task", "t", "--force", "--json"})
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != n {
		t.Fatalf("ledger %d jobs, %v", len(jobs), err)
	}
	for _, j := range jobs {
		if j.Status != ki.JobDone {
			t.Errorf("%s: %s %s", j.ID, j.Status, j.Detail)
		}
		if _, err := os.Stat(filepath.Join(j.Worktree, ".git")); err != nil {
			t.Errorf("%s: no worktree: %v", j.ID, err)
		}
	}
	if out, _ := exec.CommandContext(t.Context(), "git", "-C", d.repo, "config", "--get-regexp", "^branch\\.ki/").CombinedOutput(); len(out) != 0 {
		t.Fatalf("no tracking config may be written:\n%s", out)
	}
}

func TestDelegateProjectRunsFromAnywhere(t *testing.T) {
	d := newDelegateEnv(t)
	gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], ki.MeccaConfigFile), "roots: ["+filepath.Dir(d.repo)+"]\n")
	if err := os.Rename(d.repo, filepath.Join(filepath.Dir(d.repo), "app")); err != nil {
		t.Fatal(err)
	}
	d.repo = filepath.Join(filepath.Dir(d.repo), "app")
	d.app.Dir = t.TempDir() // somewhere that is not a repo
	d.worker["RESULT"] = claudeDone

	err := d.delegate("--task", "t", "--force", "--json")
	if !isUserError(err) || !strings.Contains(err.Error(), "not inside a git repo") {
		t.Fatalf("outside a repo without --project: %v", err)
	}
	if err := d.delegate("--project", "app", "--task", "t", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if mustEval(t, r.Repo) != mustEval(t, d.repo) || mustEval(t, filepath.Dir(filepath.Dir(r.Worktree))) != mustEval(t, filepath.Dir(d.repo)) {
		t.Fatalf("result %+v", r)
	}
	if err := d.delegate("--project", d.repo, "--task", "t", "--force", "--here", "--json"); err != nil {
		t.Fatalf("a path works too: %v", err)
	}
	if err := d.delegate("--project", "nowhere", "--task", "t", "--force"); !isUserError(err) {
		t.Fatalf("unknown project: %v", err)
	}
}

// issueServer points delegate at a fake GitHub and the work/app origin projects.yaml knows.
func (d *delegateEnv) issueServer(t *testing.T, f *issueAPI) {
	t.Helper()
	if _, err := exec.CommandContext(t.Context(), "git", "-C", d.repo, "remote", "get-url", "origin").Output(); err != nil {
		gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	}
	d.vars["KI_GITHUB_GRAPHQL_URL"] = f.server(t).URL
	d.app.GitHubToken = func(context.Context, string) string { return f.token }
}

func TestParseDelegateIssueFlags(t *testing.T) {
	da, err := parseDelegateArgs([]string{"--issue", "12", "--steal", "--here", "--json"}, "claude")
	if err != nil || da.Issue != "12" || !da.Steal || !da.Here || !da.JSON || da.Task != "" {
		t.Fatalf("got %+v %v", da, err)
	}
	for _, tc := range []struct{ args, want string }{
		{args: "--steal", want: "--steal applies"},
		{args: "--issue", want: "--issue needs"},
	} {
		_, err := parseDelegateArgs(strings.Fields(tc.args), "claude")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: %v", tc.args, err)
		}
	}
}

func TestDelegateIssueClaimsAndRecordsThePR(t *testing.T) {
	d := newDelegateEnv(t)
	f := newFakeGH()
	f.addIssue("work", "app", 7, "fix(ci): the build fails", "The tests fail.", "OPEN")
	d.issueServer(t, f)
	d.worker["RESULT"] = `{"type":"result","result":"PR: https://github.com/work/app/pull/12","session_id":"s"}`
	if err := d.delegate("--issue", "7", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Status != ki.JobPR || r.Detail != "https://github.com/work/app/pull/12" || r.Issue != "work/app#7" {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(d.args(t)[len(d.args(t))-1], "Issue work/app#7: fix(ci): the build fails") || !strings.Contains(d.args(t)[len(d.args(t))-1], "The tests fail.") {
		t.Fatalf("brief %q", d.args(t)[len(d.args(t))-1])
	}
	if !strings.Contains(d.app.Stderr.(*bytes.Buffer).String(), "issue=work/app#7") {
		t.Fatalf("stderr %s", d.app.Stderr)
	}
	if labels := f.view("work", "app", 7); len(labels) != 0 {
		t.Fatalf("labels %v", labels)
	}
	f.mu.Lock()
	ops := strings.Join(f.ops, ",")
	f.mu.Unlock()
	if ops != "read,claim,finish" {
		t.Fatalf("ops %s", ops)
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != 1 || jobs[0].Issue != "work/app#7" {
		t.Fatalf("ledger %+v %v", jobs, err)
	}
	if err := d.delegate("ls"); err != nil {
		t.Fatal(err)
	}
	if out := d.out.String(); !strings.Contains(out, "ISSUE") || !strings.Contains(out, "work/app#7") || !strings.Contains(out, r.ID) {
		t.Fatalf("ls: %s", out)
	}
}

func TestDelegateIssueNamesTheBranchFromTheTitle(t *testing.T) {
	for name, tc := range map[string]struct {
		taken bool
		want  func(id string) string
	}{
		"free":  {want: func(string) string { return "fix/7-the-build-fails" }},
		"taken": {taken: true, want: func(id string) string { return "fix/7-the-build-fails-" + id[strings.LastIndex(id, "-")+1:] }},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			f := newFakeGH()
			f.addIssue("work", "app", 7, "fix(ci): the build fails", "The tests fail.", "OPEN")
			d.issueServer(t, f)
			if tc.taken {
				gitIn(t, d.repo, "branch", "fix/7-the-build-fails")
			}
			d.worker["RESULT"] = claudeDone
			if err := d.delegate("--issue", "7", "--force", "--json"); err != nil {
				t.Fatal(err)
			}
			r := d.result(t)
			want := tc.want(r.ID)
			if r.Branch != want {
				t.Fatalf("branch %q, want %q", r.Branch, want)
			}
			if got := git(context.Background(), r.Worktree, "branch", "--show-current"); got != want {
				t.Fatalf("worktree on %q, want %q", got, want)
			}
		})
	}
}

func TestDelegateDraftIsStatedInTheBrief(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"ready by default": {args: []string{"--task", "fix the build", "--force", "--json"}, want: "without `--draft`"},
		"draft on request": {args: []string{"--task", "fix the build", "--draft", "--force", "--json"}, want: "`gh pr create --draft`"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			d.worker["RESULT"] = claudeDone
			if err := d.delegate(tc.args...); err != nil {
				t.Fatal(err)
			}
			if task := d.args(t)[len(d.args(t))-1]; !strings.Contains(task, tc.want) {
				t.Fatalf("brief %q lacks %q", task, tc.want)
			}
		})
	}
}

func TestDelegateIssueFinishStates(t *testing.T) {
	for name, tc := range map[string]struct{ result, status, detail string }{
		"done":    {claudeDone, ki.JobDone, "hi"},
		"blocked": {cursorBlocked, ki.JobBlocked, "which env?"},
		"failed":  {`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","session_id":"s"}`, ki.JobFailed, "max_turns"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			f := newFakeGH()
			f.addIssue("work", "app", 7, "T", "B", "OPEN")
			d.issueServer(t, f)
			d.worker["RESULT"] = tc.result
			if err := d.delegate("--issue", "7", "--here", "--force", "--json"); err != nil {
				t.Fatal(err)
			}
			r := d.result(t)
			if r.Status != tc.status || r.Detail != tc.detail || r.Issue != "work/app#7" {
				t.Fatalf("result %+v", r)
			}
			if labels := f.view("work", "app", 7); len(labels) != 0 {
				t.Fatalf("label %v", labels)
			}
		})
	}
}

func TestDelegateIssueTaskOverridesTheBody(t *testing.T) {
	d := newDelegateEnv(t)
	f := newFakeGH()
	f.addIssue("work", "app", 7, "Fix the build", "The tests fail.", "OPEN")
	d.issueServer(t, f)
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--issue", "other/thing#3", "--task", "do this instead", "--here", "--force", "--json"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("wrong repo should miss: %v", err)
	}
	f.addIssue("other", "thing", 3, "Elsewhere", "secret body", "OPEN")
	if err := d.delegate("--issue", "other/thing#3", "--task", "do this instead", "--here", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if r := d.result(t); r.Issue != "other/thing#3" || r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	brief := d.args(t)[len(d.args(t))-1]
	if !strings.Contains(brief, "do this instead") || strings.Contains(brief, "secret body") {
		t.Fatalf("brief %q", brief)
	}
}

func TestDelegateIssueCrashLeavesTheLabelUntilSteal(t *testing.T) {
	d := newDelegateEnv(t)
	f := newFakeGH()
	f.addIssue("work", "app", 7, "T", "B", "OPEN")
	f.failFinish = 1
	d.issueServer(t, f)
	d.worker["RESULT"] = claudeDone
	runs := filepath.Join(t.TempDir(), "runs")
	d.worker["RUNS_FILE"] = runs
	err := d.delegate("--issue", "7", "--here", "--force", "--json")
	if !isUserError(err) || !strings.Contains(err.Error(), "could not remove the agent: working label") {
		t.Fatalf("finish failure: %v", err)
	}
	r := d.result(t)
	if r.Status != ki.JobDone || r.Issue != "work/app#7" {
		t.Fatalf("the job still finished: %+v", r)
	}
	if labels := f.view("work", "app", 7); len(labels) != 1 {
		t.Fatalf("crashed claim: labels %v", labels)
	}
	err = d.delegate("--issue", "7", "--here", "--force")
	if !isUserError(err) || !strings.Contains(err.Error(), "is claimed") || !strings.Contains(err.Error(), "--steal") {
		t.Fatalf("second shell: %v", err)
	}
	if err := d.delegate("--issue", "7", "--steal", "--here", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 0 {
		t.Fatalf("label after steal: %v", labels)
	}
	raw, err := os.ReadFile(runs)
	if err != nil || strings.Count(string(raw), "x") != 2 {
		t.Fatalf("runs %q (the refused shell must not start) %v", raw, err)
	}
}

func TestDelegateIssueMissingAndClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		state, want string
		here        bool
	}{
		"missing": {want: "was not found"},
		"closed":  {state: "CLOSED", want: "is closed"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			f := newFakeGH()
			if tc.state != "" {
				f.addIssue("work", "app", 7, "T", "B", tc.state)
			}
			d.issueServer(t, f)
			err := d.delegate("--issue", "7", "--force", "--json")
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("worker ran")
			}
			wt := filepath.Join(filepath.Dir(d.repo), filepath.Base(d.repo)+"-worktrees")
			if entries, _ := os.ReadDir(wt); len(entries) != 0 {
				t.Fatalf("refusal must not create a worktree: %v", entries)
			}
		})
	}
}

func TestDelegateIssueRefusals(t *testing.T) {
	for name, tc := range map[string]struct {
		remote bool
		token  string
		args   []string
		want   string
	}{
		"bad issue":      {remote: true, token: "tok", args: []string{"--issue", "nope"}, want: "invalid issue"},
		"no origin":      {token: "tok", args: []string{"--issue", "7"}, want: "no GitHub origin"},
		"unmapped":       {token: "tok", args: []string{"--issue", "work/app#7"}, want: "projects.yaml"},
		"no token":       {remote: true, args: []string{"--issue", "7"}, want: "gh auth login"},
		"steal no issue": {args: []string{"--steal", "--task", "t"}, want: "--steal applies"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			f := newFakeGH()
			f.addIssue("work", "app", 7, "T", "B", "OPEN")
			if tc.remote {
				gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
			}
			d.vars["KI_GITHUB_GRAPHQL_URL"] = f.server(t).URL
			token := tc.token
			d.app.GitHubToken = func(context.Context, string) string { return token }
			err := d.delegate(append(tc.args, "--force", "--here")...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if _, err := os.Stat(d.argsFile); err == nil {
				t.Fatal("worker ran")
			}
		})
	}
}

func TestDelegateIssueRoleAndProject(t *testing.T) {
	d := newDelegateEnv(t)
	gitIn(t, d.repo, "remote", "add", "origin", "https://github.com/work/app")
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), "identities: [work]\nroles:\n  execution:\n    providers: [claude:sonnet]\n"+
		"delegate:\n  allow: [git, go]\n")
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], ki.MeccaConfigFile), "roots: ["+filepath.Dir(d.repo)+"]\n")
	if err := os.Rename(d.repo, filepath.Join(filepath.Dir(d.repo), "app")); err != nil {
		t.Fatal(err)
	}
	d.repo = filepath.Join(filepath.Dir(d.repo), "app")
	d.app.Dir = t.TempDir()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "Fix the build", "The tests fail.", "OPEN")
	d.vars["KI_GITHUB_GRAPHQL_URL"] = f.server(t).URL
	d.app.GitHubToken = func(context.Context, string) string { return f.token }
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--project", "app", "--role", "execution", "--issue", "7", "--mode", "edits", "--here", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Provider != "claude" || r.Model != "sonnet" || r.Role != "execution" || r.Issue != "work/app#7" || r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	if cwd := mustEval(t, d.cwd(t)); cwd != mustEval(t, d.repo) {
		t.Fatalf("ran in %s, want %s", cwd, d.repo)
	}
	brief := d.args(t)[len(d.args(t))-1]
	for _, s := range []string{"Issue work/app#7: Fix the build", "The tests fail.", "You run in edits mode.", "git, go"} {
		if !strings.Contains(brief, s) {
			t.Errorf("brief misses %q: %q", s, brief)
		}
	}
}

// Ctrl-C while a worker runs is not a provider failure: the job is FAILED, not
// LAUNCH_FAILED, no other provider is tried, and the breaker stays untouched.
func TestDelegateInterruptedIsNotALaunchFailure(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), rolesYAML)
	d.worker["RESULT"], d.worker["SLEEP"] = claudeDone, "5"
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	d.out.Reset()
	err := d.app.Run(ctx, []string{"delegate", "--task", "t", "--force", "--json"})
	if !isUserError(err) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("want an interrupted error, got %v", err)
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != 1 || jobs[0].Status != ki.JobFailed || !strings.Contains(jobs[0].Detail, "cancelled") || jobs[0].Provider != "codex" {
		t.Fatalf("ledger %+v %v", jobs, err)
	}
	try, skipped := ki.Candidates(ki.PickInput{Role: "execution", R: mustRole(t, d), Ledger: jobs, Now: time.Now(), Installed: func(string) bool { return true }})
	if len(try) != 2 || len(skipped) != 0 {
		t.Fatalf("the breaker must not trip: try %v skipped %v", try, skipped)
	}
}

func mustRole(t *testing.T, d *delegateEnv) ki.Role {
	t.Helper()
	m, err := ki.LoadMachine(filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"), map[string]struct{}{"work": {}})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := m.RoleFor("execution")
	if !ok {
		t.Fatal("no execution role")
	}
	return r
}

// remoteEnv gives the repo a real origin whose main is one commit ahead of the
// last fetch, and returns that commit.
func remoteEnv(t *testing.T, d *delegateEnv) string {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, d.repo, "branch", "-M", "main")
	gitIn(t, filepath.Dir(origin), "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, d.repo, "remote", "add", "origin", origin)
	gitIn(t, d.repo, "push", "-q", "origin", "main")
	gitIn(t, d.repo, "fetch", "-q", "origin")
	gitIn(t, d.repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", origin, other)
	gitIn(t, other, "-c", "user.name=Bob", "-c", "user.email=b@x", "commit", "-q", "--allow-empty", "-m", "merged meanwhile")
	gitIn(t, other, "push", "-q", "origin", "main")
	out, err := exec.CommandContext(t.Context(), "git", "-C", other, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func worktreeHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestDelegateStartsFromTheRemoteHead(t *testing.T) {
	d := newDelegateEnv(t)
	want := remoteEnv(t, d)
	d.worker["RESULT"] = claudeDone
	if err := d.app.Run(context.Background(), []string{"delegate", "--task", "t", "--force", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := worktreeHead(t, d.result(t).Worktree); got != want {
		t.Fatalf("worktree at %s, want the remote head %s", got, want)
	}
}

func TestDelegateFallsBackWhenTheFetchFails(t *testing.T) {
	d := newDelegateEnv(t)
	want := remoteEnv(t, d)
	gitIn(t, d.repo, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	d.worker["RESULT"] = claudeDone
	var stderr bytes.Buffer
	d.app.Stderr = &stderr
	if err := d.app.Run(context.Background(), []string{"delegate", "--task", "t", "--force", "--json"}); err != nil {
		t.Fatal(err)
	}
	if got := worktreeHead(t, d.result(t).Worktree); got == want {
		t.Fatal("an unreachable remote cannot move the base")
	}
	if n := strings.Count(stderr.String(), "starting from the last fetch"); n != 1 {
		t.Fatalf("want one fallback note, got %d:\n%s", n, stderr.String())
	}
}

func TestDelegateParallelJobsShareOneFetch(t *testing.T) {
	d := newDelegateEnv(t)
	want := remoteEnv(t, d)
	d.worker["RESULT"] = claudeDone
	const n = 4
	errs := make(chan error, n)
	bufs := make([]*bytes.Buffer, n)
	for i := range bufs {
		bufs[i] = &bytes.Buffer{}
		go func(b *bytes.Buffer) {
			app := *d.app
			app.Stdout, app.Stderr = &bytes.Buffer{}, b
			errs <- app.Run(context.Background(), []string{"delegate", "--task", "t", "--force", "--json"})
		}(bufs[i])
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bufs {
		if strings.Contains(b.String(), "could not fetch") {
			t.Fatalf("parallel fetch raced:\n%s", b)
		}
	}
	jobs, err := ki.ReadLedger(filepath.Join(d.vars["KI_CONFIG_DIR"], "jobs", ki.LedgerFile))
	if err != nil || len(jobs) != n {
		t.Fatalf("ledger %d jobs, %v", len(jobs), err)
	}
	for _, j := range jobs {
		if got := worktreeHead(t, j.Worktree); got != want {
			t.Errorf("%s at %s, want %s", j.ID, got, want)
		}
	}
}

func TestDelegateRecordsTheModelThatRan(t *testing.T) {
	const (
		claudeWithUsage = `{"type":"system","subtype":"init","model":"claude-sonnet-5-5"}
{"type":"result","subtype":"success","is_error":false,"result":"DONE: hi","session_id":"9d6e","usage":{"input_tokens":2,"output_tokens":8},"modelUsage":{"claude-sonnet-5-5":{"outputTokens":8}}}`
		cursorAuto = `{"type":"system","subtype":"init","model":"Auto Balance"}
{"type":"result","subtype":"success","is_error":false,"result":"DONE: ok","session_id":"366e"}`
		cursorSilent = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: ok","session_id":"366e"}`
	)
	for name, tc := range map[string]struct {
		args                 []string
		result               string
		model, requested, ls string
	}{
		"claude reports what answered": {[]string{"--agent", "claude", "--model", "sonnet"}, claudeWithUsage, "claude-sonnet-5-5", "sonnet", "claude-sonnet-5-5"},
		"claude says nothing":          {[]string{"--agent", "claude", "--model", "sonnet"}, claudeDone, "sonnet", "sonnet", "sonnet"},
		"cursor chooses for itself":    {[]string{"--agent", "agent"}, cursorAuto, "Auto Balance", "", "Auto Balance"},
		"cursor reports nothing":       {[]string{"--agent", "agent"}, cursorSilent, ki.CursorAuto, "", ki.CursorAuto},
		"codex without a model":        {[]string{"--agent", "codex"}, codexEvents, "", "", "-"},
	} {
		t.Run(name, func(t *testing.T) {
			d := newDelegateEnv(t)
			d.worker["RESULT"] = tc.result
			if err := d.delegate(append([]string{"--task", "t", "--force", "--json"}, tc.args...)...); err != nil {
				t.Fatal(err)
			}
			if r := d.result(t); r.Model != tc.model || r.ModelRequested != tc.requested {
				t.Fatalf("model %q requested %q, want %q %q", r.Model, r.ModelRequested, tc.model, tc.requested)
			}
			if err := d.delegate("ls", "--json"); err != nil {
				t.Fatal(err)
			}
			var raw []map[string]any
			if err := json.Unmarshal(d.out.Bytes(), &raw); err != nil || len(raw) != 1 {
				t.Fatalf("ls --json: %v %s", err, d.out.String())
			}
			if _, ok := raw[0]["model_requested"]; !ok || (tc.model != "" && raw[0]["model"] != tc.model) {
				t.Fatalf("ls --json carries both models: %v", raw[0])
			}
			if err := d.delegate("ls"); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(d.out.String()), "\n")
			if len(lines) != 2 || !strings.Contains(lines[0], "MODEL") || !strings.Contains(lines[1], " "+tc.ls+" ") {
				t.Fatalf("ls shows %q:\n%s", tc.ls, d.out.String())
			}
		})
	}
}

func TestDelegateWritingRole(t *testing.T) {
	d := newDelegateEnv(t)
	write(t, filepath.Join(d.vars["KI_CONFIG_DIR"], "machine.yaml"),
		"identities: [work]\nmodels:\n  execution: {claude: opus}\n  writing: {claude: haiku}\neffort:\n  execution: high\n  writing: low\nroles:\n  execution:\n    providers: [codex:gpt-5-mini]\n  writing:\n    providers: [claude]\n")
	d.worker["RESULT"] = claudeDone
	if err := d.delegate("--role", "writing", "--task", "write the changelog", "--force", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.result(t)
	if r.Provider != "claude" || r.Role != "writing" || r.ModelRequested != "haiku" || r.Model != "haiku" {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	if i := slices.Index(args, "--model"); i < 0 || args[i+1] != "haiku" {
		t.Fatalf("the writing class's model is passed: %v", args)
	}
	if i := slices.Index(args, "--effort"); i < 0 || args[i+1] != "low" {
		t.Fatalf("the writing class's effort is passed: %v", args)
	}
}

func TestDoctorAcceptsTheWritingClass(t *testing.T) {
	e := newEnv(t, "https://github.com/work/app")
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "machine.yaml"),
		"identities: [work]\nmodels:\n  writing: {claude: haiku}\neffort:\n  writing: low\nroles:\n  writing:\n    providers: [claude:haiku]\n")
	_ = e.app.Run(context.Background(), []string{"doctor"}) // other checks may fail in a temp env
	out := e.out.String()
	for _, want := range []string{"writing claude=haiku/low", "ordered over claude:haiku"} {
		if !strings.Contains(out, want) {
			t.Fatalf("doctor lacks %q:\n%s", want, out)
		}
	}
}
