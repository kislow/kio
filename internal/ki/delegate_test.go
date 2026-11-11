package ki

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		last, status, detail string
	}{
		"pr":                          {"Opened it.\nPR: https://github.com/o/r/pull/3", JobPR, "https://github.com/o/r/pull/3"},
		"done":                        {"DONE: renamed the flag", JobDone, "renamed the flag"},
		"blocked":                     {"BLOCKED: which env?", JobBlocked, "which env?"},
		"failed":                      {"FAILED: tests red", JobFailed, "tests red"},
		"last wins":                   {"DONE: first\nFAILED: later", JobFailed, "later"},
		"markdown bold":               {"**DONE: bold**", JobDone, "bold"},
		"missing":                     {"I did things.", JobFailed, "no status line in the final message"},
		"cursor joined messages":      {"I'll read the notes first.Bumped both pins.PR: https://github.com/o/r/pull/30", JobPR, "https://github.com/o/r/pull/30"},
		"cursor text after the url":   {"I'll branch here.PR: https://github.com/o/r/pull/194Lint and tests passed: https://github.com/o/r/pull/194", JobPR, "https://github.com/o/r/pull/194"},
		"mid-line done":               {"All good. DONE: pins bumped", JobDone, "pins bumped"},
		"last marker wins in a line":  {"BLOCKED: first? Then I went on. DONE: finished", JobDone, "finished"},
		"word prefix is not a marker": {"UNDONE: not a status", JobFailed, "no status line in the final message"},
		"trailing punctuation kept":   {"DONE: go.mod declares Go 1.27.3 (`go 1.27.3`, line 3); nothing edited.", JobDone, "go.mod declares Go 1.27.3 (`go 1.27.3`, line 3); nothing edited."},
		"empty":                       {"", JobFailed, "no status line in the final message"},
	} {
		t.Run(name, func(t *testing.T) {
			s, d := ParseStatus(tc.last)
			if s != tc.status || d != tc.detail {
				t.Fatalf("got %s %q, want %s %q", s, d, tc.status, tc.detail)
			}
		})
	}
}

func TestEveryBriefVariantStatesTheWorkerRules(t *testing.T) {
	t.Parallel()
	const role = "You are the worker ki started for this task: do it yourself in this worktree. Instructions about delegating with ki delegate are for the planner and do not apply to you."
	const foreground = "Run tests, linters and builds in the foreground and wait for them; your turn ends the job, so never leave a command running in the background."
	const bodyFile = "Write a pull request body to a file and pass it with gh pr create --body-file, so backticks and quotes reach GitHub unescaped."
	briefs := map[string]string{
		"plain":     WrapTask("fix the build", false),
		"draft":     WrapTask("fix the build", true),
		"sandboxed": WrapSandboxedTask("fix the build"),
		"edits":     WrapEditsTask("fix the build", []string{"git"}, false),
	}
	for name, w := range briefs {
		if !strings.Contains(w, "fix the build\n\n"+role+" "+foreground+" "+bodyFile+"\n\n") {
			t.Errorf("%s: the worker paragraph does not follow the task: %q", name, w)
		}
	}
}

func TestWrapTaskKeepsTheContractLast(t *testing.T) {
	w := WrapTask("  fix the build  \n", false)
	if !strings.HasPrefix(w, "fix the build\n\n") || !strings.HasSuffix(w, "FAILED: <reason> when something broke that you could not fix") {
		t.Fatalf("got %q", w)
	}
}

func TestWrapTaskStatesTheDraftChoice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		draft     bool
		want, not string
	}{
		{name: "ready by default", want: prReady, not: prDraft},
		{name: "draft on request", draft: true, want: prDraft, not: prReady},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := WrapTask("fix the build", tt.draft)
			if !strings.Contains(w, tt.want) || strings.Contains(w, tt.not) {
				t.Fatalf("brief %q: want %q and not %q", w, tt.want, tt.not)
			}
			if !strings.HasSuffix(w, statusLines) {
				t.Fatalf("the status lines must stay last: %q", w)
			}
		})
	}
}

func TestHeadlessLaunch(t *testing.T) {
	h := &Headless{Task: "do it", LastMessage: "/j/last.txt"}
	s := Session{Brains: []string{"acme"}}
	for agent, want := range map[string][]string{
		"claude": {
			"claude", "--mcp-config", "/m.json", "--append-system-prompt-file", "/i.md", "--settings", ClaudeSettings,
			"--model", "sonnet", "--effort", "medium", "--permission-mode", "auto", "--print", "--output-format", "stream-json", "--verbose", "do it",
		},
		"codex":        {"codex", "exec", "--model", "sonnet", "--permission-mode", "auto", "--json", "--output-last-message", "/j/last.txt", "do it"},
		"cursor-agent": {"cursor-agent", "--permission-mode", "auto", "--print", "--trust", "--output-format", "stream-json", "do it"},
		"agy":          {"agy", "--model", "sonnet", "--effort", "medium", "--permission-mode", "auto", "--output-format", "json", "--print", "do it"},
		"other":        {"other", "--permission-mode", "auto", "do it"},
	} {
		l := BuildLaunch(LaunchInput{
			Agent: agent, Session: s, Token: "t", MCPConfigPath: "/m.json", InstructionsPath: "/i.md",
			Model: "sonnet", Effort: "medium", ModeArgs: []string{"--permission-mode", "auto"}, Headless: h,
		})
		if !reflect.DeepEqual(l.Argv, want) {
			t.Errorf("%s: got %v, want %v", agent, l.Argv, want)
		}
	}
	for agent, want := range map[string]bool{"claude": true, "/opt/bin/codex": true, "agent": true, "cursor-agent": true, "agy": true, "sh": false} {
		if HeadlessSupported(agent) != want {
			t.Errorf("HeadlessSupported(%s) = %v", agent, !want)
		}
	}
}

const claudeResult = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: hi","session_id":"9d6e","num_turns":1,"total_cost_usd":0.007,` +
	`"usage":{"input_tokens":2,"cache_creation_input_tokens":35115,"cache_read_input_tokens":11803,"output_tokens":8}}`

const cursorResult = `{"type":"result","subtype":"success","is_error":false,"duration_ms":30776,"result":"DONE: hi","session_id":"366e",` +
	`"request_id":"3335","usage":{"inputTokens":8773,"outputTokens":1679,"cacheReadTokens":6016,"cacheWriteTokens":0}}`

const agyResult = `{"conversation_id":"e209","status":"SUCCESS","response":"DONE: hi\n","duration_seconds":3.33,"num_turns":1,` +
	`"usage":{"input_tokens":6822,"output_tokens":62,"thinking_tokens":59,"cache_read_tokens":0,"total_tokens":6884}}`

const codexEvents = `{"type":"thread.started","thread_id":"01a1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"working"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"DONE: hi"}}
{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":9}}
`

const codexAuthFailure = `{"type":"thread.started","thread_id":"01a1"}
{"type":"error","message":"Your access token could not be refreshed. Please log out and sign in again."}
{"type":"turn.failed","error":{"message":"Your access token could not be refreshed. Please log out and sign in again."}}
`

func TestParseOutput(t *testing.T) {
	for name, tc := range map[string]struct {
		agent, stdout, last string
		want                Outcome
	}{
		"claude": {"claude", "noise\n" + claudeResult, "", Outcome{Last: "DONE: hi", Session: "9d6e", CostUSD: 0.007, Tokens: Tokens{Input: 2, Output: 8, CacheRead: 11803}}},
		"cursor": {"agent", cursorResult, "", Outcome{Last: "DONE: hi", Session: "366e", Tokens: Tokens{Input: 8773, Output: 1679, CacheRead: 6016}}},
		"claude error": {
			"claude", `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","session_id":"s","usage":{}}`, "",
			Outcome{Session: "s", Err: "max_turns"},
		},
		"codex":           {"codex", codexEvents, "", Outcome{Last: "DONE: hi", Session: "01a1", Tokens: Tokens{Input: 100, Output: 9, CacheRead: 40}}},
		"codex last file": {"codex", codexEvents, "DONE: from file\n", Outcome{Last: "DONE: from file", Session: "01a1", Tokens: Tokens{Input: 100, Output: 9, CacheRead: 40}}},
		"codex auth":      {"codex", codexAuthFailure, "", Outcome{Session: "01a1", Err: "Your access token could not be refreshed. Please log out and sign in again."}},
		"not json":        {"claude", "plain text\nDONE: ok\n", "", Outcome{Last: "plain text\nDONE: ok"}},
		"agy":             {"agy", agyResult, "", Outcome{Last: "DONE: hi", Session: "e209", Tokens: Tokens{Input: 6822, Output: 62}}},
		"agy failure":     {"agy", `{"conversation_id":"e2","status":"ERROR","response":""}`, "", Outcome{Session: "e2", Err: "error"}},
		"agy not json":    {"agy", "plain\n", "", Outcome{Last: "plain"}},
		"agy denied": {
			"agy", `{"conversation_id":"e3","status":"SUCCESS","response":"","denied_actions":[{"action":"read_file","display_name":"Read"},{"action":"run_command"}]}`, "",
			Outcome{Session: "e3", Err: "tools denied without a prompt: read_file, run_command (run with --mode skip)"},
		},
		"claude cwd reset": {"claude", claudeResult + "\nShell cwd was reset\n", "", Outcome{Last: "DONE: hi", Session: "9d6e", CostUSD: 0.007, Tokens: Tokens{Input: 2, Output: 8, CacheRead: 11803}}},
	} {
		t.Run(name, func(t *testing.T) {
			got := ParseOutput(tc.agent, ModeAuto, []byte(tc.stdout), tc.last)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestParseAgyQuotaErrorFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/agy_quota_error.json")
	if err != nil {
		t.Fatal(err)
	}
	out := ParseOutput("agy", ModeEdits, raw, "")
	wantErr := "Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 147h25m10s."
	if out.Err != wantErr || out.Last != "" || out.QuotaFor != 147*time.Hour+25*time.Minute+10*time.Second {
		t.Fatalf("got %+v", out)
	}
}

func TestParseAgyShortErrorQuota(t *testing.T) {
	const output = `{"status":"ERROR","short_error":"RESOURCE_EXHAUSTED (code 429): quota reached. Resets in 1h30m.","response":""}`
	out := ParseOutput("agy", ModeEdits, []byte(output), "")
	if out.Err != "RESOURCE_EXHAUSTED (code 429): quota reached. Resets in 1h30m." || out.QuotaFor != 90*time.Minute {
		t.Fatalf("got %+v", out)
	}
}

// agyCommandDenied is what agy prints when headless mode refuses a command
// that is not on its allow-list.
const agyCommandDenied = `{"conversation_id":"e4","status":"SUCCESS","response":"","denied_actions":[{"action":"command","display_name":"RunCommand"}]}`

func TestParseAgyDenials(t *testing.T) {
	t.Parallel()
	const editsErr = "agy refused a command that is not on the allow-list (denied: command); add it under delegate.allow in machine.yaml, or run with --mode skip"
	tests := []struct {
		name   string
		mode   Mode
		stdout string
		want   string
	}{
		{"edits, command refused", ModeEdits, agyCommandDenied, editsErr},
		{"skip keeps the old message", ModeSkip, agyCommandDenied, "tools denied without a prompt: command (run with --mode skip)"},
		{"no mode keeps the old message", "", agyCommandDenied, "tools denied without a prompt: command (run with --mode skip)"},
		{
			"edits, a tool that is not a command", ModeEdits,
			`{"conversation_id":"e5","status":"SUCCESS","response":"","denied_actions":[{"action":"read_file"}]}`,
			"tools denied without a prompt: read_file (run with --mode skip)",
		},
		{
			"edits, a denial after an answer is no error", ModeEdits,
			`{"conversation_id":"e6","status":"SUCCESS","response":"DONE: ok","denied_actions":[{"action":"command"}]}`,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseOutput("agy", tt.mode, []byte(tt.stdout), ""); got.Err != tt.want {
				t.Fatalf("Err %q, want %q", got.Err, tt.want)
			}
		})
	}
}

func TestWrapEditsTask(t *testing.T) {
	t.Parallel()
	w := WrapEditsTask("  fix the build \n", []string{"git", "go", "gh pr"}, false)
	for _, want := range []string{
		"fix the build\n\n" + workerRules + "\n\nYou run in edits mode.",
		"Edit files with your file tools, not the shell.",
		"The only shell commands you may run are: git, go, gh pr",
		"Any other command is refused and ends the job without a result.",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("%q missing from %q", want, w)
		}
	}
	if !strings.HasSuffix(w, contractRules(false)) {
		t.Fatalf("the status contract must stay last: %q", w)
	}
}

func TestJobIDs(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 18, 5, 0, time.UTC)
	id := NewJobID(now)
	if !strings.HasPrefix(id, "j-20261025-161805-") || !ValidJobID(id) {
		t.Fatalf("got %s", id)
	}
	seen := map[string]bool{}
	for range 64 {
		id := NewJobID(now)
		if seen[id] || !ValidJobID(id) || len(id) != len("j-20261025-161805-")+8 {
			t.Fatalf("ids in one second must be distinct 8-hex ids, got %s after %d", id, len(seen))
		}
		seen[id] = true
	}
	if !ValidJobID("j-20261025-161805-ab12") {
		t.Error("an id from before the suffix grew to 8 hex must stay valid")
	}
	for _, bad := range []string{"", "j-1", "../etc", "j-20261025-161805-zzzz", "j-20261025-161805-ab12/x", "j-20261025-161805-ab123", "j-20261025-161805-ab12cd34ef"} {
		if ValidJobID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLedgerRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs", LedgerFile)
	if got, err := ReadLedger(path); err != nil || got != nil {
		t.Fatalf("missing ledger: %v %v", got, err)
	}
	started := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	for i, r := range []Result{
		{ID: "j-20261025-160000-0001", Provider: "codex", Status: JobDone, Detail: "a", Last: "secret long transcript", Started: started, Exit: 0},
		{ID: "j-20261025-160100-0002", Provider: "claude", Status: JobLaunchFailed, Detail: "not logged in", Started: started.Add(time.Minute), Exit: 1},
	} {
		if err := AppendLedger(path, r); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	got, err := ReadLedger(path)
	if err != nil || len(got) != 2 || got[0].ID != "j-20261025-160000-0001" || got[1].Status != JobLaunchFailed {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].Last != "" {
		t.Fatal("the ledger must not carry the transcript")
	}
	if err := os.WriteFile(path, []byte("{broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLedger(path); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("want a line error, got %v", err)
	}
}

func TestResumeArgs(t *testing.T) {
	for agent, want := range map[string][]string{
		"claude": {"--resume", "s1"}, "/opt/codex": {"resume", "s1"}, "agent": {"--resume", "s1"}, "cursor-agent": {"--resume", "s1"}, "agy": {"--conversation", "s1"}, "aider": nil,
	} {
		if got := ResumeArgs(agent, "s1"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", agent, got, want)
		}
	}
	if ResumeArgs("claude", "") != nil {
		t.Fatal("no session, nothing to resume")
	}
}

func TestParseOutputUsageLimit(t *testing.T) {
	t.Parallel()
	const claudeLimit = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets in 2h30m","session_id":"s1","usage":{}}`
	const claudeLimitNoReset = `{"type":"result","subtype":"success","is_error":true,"result":"Claude usage limit reached.","session_id":"s1","usage":{}}`
	const cursorLimit = `{"type":"result","subtype":"error","is_error":true,"result":"You've hit your usage limit. Resets in 45 minutes.","session_id":"s2","usage":{}}`
	const workerMentionsLimit = `{"type":"result","subtype":"success","is_error":false,"result":"DONE: added rate limit handling","session_id":"s3","usage":{}}`
	const workerFailedOnLimit = `{"type":"result","subtype":"success","is_error":false,"result":"FAILED: the API hit its usage limit","session_id":"s3","usage":{}}`
	tests := []struct {
		name, agent, stdout string
		wantFor             time.Duration
		wantErr, wantLast   string
	}{
		{"claude with a reset", "claude", claudeLimit, 150 * time.Minute, "You've hit your limit · resets in 2h30m", ""},
		{"claude without a reset", "claude", claudeLimitNoReset, DefaultQuotaCooldown, "Claude usage limit reached.", ""},
		{"cursor with a reset", "agent", cursorLimit, 45 * time.Minute, "You've hit your usage limit. Resets in 45 minutes.", ""},
		{"plain text from a dying CLI", "agent", "You've hit your usage limit.\n", DefaultQuotaCooldown, "", "You've hit your usage limit."},
		{"a worker that finished is never a quota hit", "claude", workerMentionsLimit, 0, "", "DONE: added rate limit handling"},
		{"a worker's own FAILED line is its verdict", "claude", workerFailedOnLimit, 0, "", "FAILED: the API hit its usage limit"},
		{"an error that is no limit keeps its reason", "claude", `{"type":"result","subtype":"error_max_turns","is_error":true,"result":"","usage":{}}`, 0, "max_turns", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := ParseOutput(tt.agent, ModeAuto, []byte(tt.stdout), "")
			if out.QuotaFor != tt.wantFor || out.Err != tt.wantErr || out.Last != tt.wantLast {
				t.Fatalf("got QuotaFor=%s Err=%q Last=%q\nwant QuotaFor=%s Err=%q Last=%q", out.QuotaFor, out.Err, out.Last, tt.wantFor, tt.wantErr, tt.wantLast)
			}
		})
	}
}

func TestQuotaFromText(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	if got := QuotaFromText("error: usage limit reached, resets at 12:00", now); got != 2*time.Hour {
		t.Fatalf("got %s", got)
	}
	if got := QuotaFromText("panic: nil pointer", now); got != 0 {
		t.Fatalf("got %s", got)
	}
}

func TestParseSandboxedStatusReadsAFinalMessageWithoutAStatusLineAsDone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, last, status, detail string }{
		{"own line wins", "Changed it.\nBLOCKED: which env?", JobBlocked, "which env?"},
		{"no line, summary is the first line", "Fixed the cache key.\nTests pass.", JobDone, "Fixed the cache key."},
		{"no message is still a contract failure", "", JobFailed, noStatusLine},
		{"a PR claim is read as claimed", "PR: https://github.com/o/r/pull/4", JobPR, "https://github.com/o/r/pull/4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if status, detail := ParseSandboxedStatus(tc.last); status != tc.status || detail != tc.detail {
				t.Fatalf("got %s %q, want %s %q", status, detail, tc.status, tc.detail)
			}
		})
	}
}

func TestSandboxedIsCursorInAutoMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		agent string
		mode  Mode
		want  bool
	}{
		{"agent", ModeAuto, true},
		{"/usr/local/bin/cursor-agent", ModeAuto, true},
		{"agent", ModeSkip, false},
		{"agent", "", false},
		{"claude", ModeAuto, false},
		{"codex", ModeAuto, false},
	} {
		if got := Sandboxed(tc.agent, tc.mode); got != tc.want {
			t.Errorf("Sandboxed(%q, %q) = %v", tc.agent, tc.mode, got)
		}
	}
}

func TestAsksBrowserCheck(t *testing.T) {
	t.Parallel()
	for task, want := range map[string]bool{
		"verify it in the browser":          true,
		"run the Playwright suite":          true,
		"open Chromium and screenshot":      true,
		"fix the build":                     false,
		"the browserslist config is stale":  false,
		"bump the cross-browser-test image": true,
	} {
		if got := AsksBrowserCheck(task); got != want {
			t.Errorf("AsksBrowserCheck(%q) = %v", task, got)
		}
	}
}

func TestWrapSandboxedTaskEndsWithTheStatusLinesWithoutAPullRequest(t *testing.T) {
	t.Parallel()
	brief := WrapSandboxedTask("  fix it  ")
	if !strings.HasPrefix(brief, "fix it\n\n") || !strings.HasSuffix(brief, sandboxedStatusLines) {
		t.Fatalf("brief:\n%s", brief)
	}
	if strings.Contains(brief, "- PR:") || !strings.Contains(brief, "Commit only; ki publishes") || !strings.Contains(brief, BrowserSkipped) {
		t.Fatalf("brief:\n%s", brief)
	}
}

func TestPullURLCutsTheURLOutOfText(t *testing.T) {
	t.Parallel()
	if got := PullURL("created https://github.com/o/r/pull/12\n"); got != "https://github.com/o/r/pull/12" {
		t.Fatalf("got %q", got)
	}
	if got := PullURL("nothing here"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestEveryBriefForbidsGitConfigWrites(t *testing.T) {
	for name, brief := range map[string]string{
		"normal":    WrapTask("t", false),
		"draft":     WrapTask("t", true),
		"edits":     WrapEditsTask("t", []string{"go"}, false),
		"sandboxed": WrapSandboxedTask("t"),
	} {
		if !strings.Contains(brief, gitConfigRule) {
			t.Errorf("%s brief lacks the git config rule: %q", name, brief)
		}
	}
}
