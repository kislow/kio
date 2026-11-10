package ki

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A delegated job is one task run headless on one agent, in its own
// worktree, that answers with a single status line. The statuses are the
// ki-resolver contract, so a planner reads every worker the same way
// whichever vendor ran it.
const (
	JobPR      = "PR"
	JobDone    = "DONE"
	JobBlocked = "BLOCKED"
	JobFailed  = "FAILED"
	// JobRunning is the placeholder status in result.json while the worker is alive.
	JobRunning = "RUNNING"
	// JobLaunchFailed means the agent exited before producing any result
	// (not logged in, bad flags, missing binary). It is kept apart from
	// FAILED so a routing policy can read it as "this provider is down".
	JobLaunchFailed = "LAUNCH_FAILED"
	// JobStalled means the worker printed no event for the stall limit and ki
	// ended it. Its worktree and commits are kept. A job killed at --timeout
	// is FAILED instead.
	JobStalled = "STALLED"
	// JobQuota means the worker stopped on a usage limit after it started
	// the task. Its worktree, commits and last progress lines are kept, so
	// `ki delegate resume` can continue it on another provider.
	JobQuota = "QUOTA"
	// JobQueued is a job waiting in the queue for a provider of its role;
	// it is never in the ledger, only in its job folder's queue.json.
	JobQueued = "QUEUED"
)

// DefaultStall is how long a streaming worker may stay silent before ki ends
// it, unless --stall or delegate.stall in machine.yaml says otherwise.
const DefaultStall = 15 * time.Minute

// statusLines end every brief so the worker ends the way ParseStatus reads.
const statusLines = `You cannot ask questions. Your final message must be exactly one line, nothing after it:
- PR: <url> when you opened a pull request
- DONE: <one-line summary> when the task is complete without a pull request
- BLOCKED: <the one question you need answered> when you cannot continue without a person
- FAILED: <reason> when something broke that you could not fix`

// The PR line is stated either way, so prose elsewhere in a brief never decides it.
const (
	prReady = "If you open a pull request, open it ready for review: `gh pr create` without `--draft`."
	prDraft = "If you open a pull request, open it as a draft: `gh pr create --draft`."
)

// sandboxedStatusLines end the brief of a sandboxed worker: it never reports a PR,
// ki opens it after the worker exits.
const sandboxedStatusLines = `You cannot ask questions. Your final message must be exactly one line, nothing after it:
- DONE: <one-line summary> when the task is complete
- BLOCKED: <the one question you need answered> when you cannot continue without a person
- FAILED: <reason> when something broke that you could not fix`

// sandboxedRules tell a worker whose commands run in a sandbox what it cannot do
// and who does it instead.
const sandboxedRules = "You run in a sandbox without network access to git remotes and without a browser. " +
	"Commit only; ki publishes. Do not run `git push` or `gh pr create`, ki pushes your commits and opens the pull request once you exit, and a push or PR you report is never trusted. " +
	"Commit everything: uncommitted changes are not published and end the job as BLOCKED. " +
	"If the task asks for a browser check (Playwright, Chromium), skip it and say so in your final line; ki records `" + BrowserSkipped + "` in the result."

// BrowserSkipped is added to the result of a sandboxed job whose brief asked for a browser check.
const BrowserSkipped = "checks skipped: browser (sandbox)"

// browserCheck finds a brief that asks for a browser check.
var browserCheck = regexp.MustCompile(`(?i)\b(browser|playwright|chromium|puppeteer|cypress|headless chrome)\b`)

// AsksBrowserCheck reports whether a brief asks for a browser check.
func AsksBrowserCheck(task string) bool { return browserCheck.MatchString(task) }

// contract is appended to every brief: the pull request line, then the status lines.
func contract(draft bool) string {
	pr := prReady
	if draft {
		pr = prDraft
	}
	return "\n\n" + pr + "\n\n" + statusLines
}

// WrapTask is the brief the worker receives: the task plus the contract.
func WrapTask(task string, draft bool) string { return strings.TrimSpace(task) + contract(draft) }

// WrapSandboxedTask is the brief for a worker ki runs in a sandbox: the task, the
// rules of the sandbox, then the status lines without a PR, last.
func WrapSandboxedTask(task string) string {
	return strings.TrimSpace(task) + "\n\n" + sandboxedRules + "\n\n" + sandboxedStatusLines
}

// Sandboxed reports whether ki runs the agent in a sandbox that cannot reach git
// remotes: Cursor in auto mode (--sandbox enabled).
func Sandboxed(agent string, m Mode) bool {
	switch filepath.Base(agent) {
	case "cursor-agent", "agent":
		return m == ModeAuto
	}
	return false
}

// WrapEditsTask is the brief for a worker in edits mode: the task, the rules
// of that mode with the commands it may run, then the contract, last.
func WrapEditsTask(task string, allow []string, draft bool) string {
	return strings.TrimSpace(task) + "\n\n" +
		"You run in edits mode. Edit files with your file tools, not the shell. " +
		"The only shell commands you may run are: " + strings.Join(allow, ", ") + " (with any arguments). " +
		"Any other command is refused and ends the job without a result." + contract(draft)
}

// statusMarker finds the last status marker in a line. Cursor's print mode
// joins every message of the turn into one line without separators, so the
// marker may sit mid-line, right after a sentence's full stop.
var statusMarker = regexp.MustCompile(`^(?:.*[\s.!?*\x60])?(PR|DONE|BLOCKED|FAILED):[ \t]*(.*?)[\s*\x60]*$`)

// prURL is the pull request a PR: line points at, cut where Cursor's joined
// text runs straight on after the number.
var prURL = regexp.MustCompile(`https?://\S+?/pull/\d+`)

// ParseStatus reads the status line out of a worker's final message, scanning
// from the end so a trailing line wins. A message without one is FAILED:
// the contract was not met and a planner must not guess.
func ParseStatus(last string) (status, detail string) {
	lines := strings.Split(last, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := statusMarker.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		status, detail = m[1], strings.TrimSpace(m[2])
		if status == JobPR {
			if u := prURL.FindString(detail); u != "" {
				detail = u
			}
		}
		return status, detail
	}
	return JobFailed, noStatusLine
}

// ParseSandboxedStatus is ParseStatus for a worker whose commits ki publishes: a
// final message without a status line is DONE with its first line as the summary,
// because a clean exit means the worker finished and the commits decide the rest.
// A run that failed never reaches it (the exit code decides first).
func ParseSandboxedStatus(last string) (status, detail string) {
	status, detail = ParseStatus(last)
	if status == JobFailed && detail == noStatusLine {
		if summary := firstLine(last); summary != "" {
			return JobDone, clip(summary)
		}
	}
	return status, detail
}

// noStatusLine is the detail of a final message without a status line.
const noStatusLine = "no status line in the final message"

// Headless makes BuildLaunch start the agent non-interactively with Task as
// its prompt and JSON on stdout. LastMessage is the file Codex writes its
// final message to (-o); Claude Code and Cursor return it inside their JSON.
type Headless struct {
	Task        string
	LastMessage string
	// Instructions go in front of the task for Codex and Cursor, which take no
	// system-prompt file; Claude Code gets them as a flag, agy gets none.
	Instructions string
}

// HeadlessSupported reports whether ki knows the agent's non-interactive flags.
func HeadlessSupported(agent string) bool {
	switch filepath.Base(agent) {
	case "claude", "codex", "cursor-agent", "agent", "agy":
		return true
	}
	return false
}

func headlessArgs(agent string, h *Headless) []string {
	switch filepath.Base(agent) {
	case "claude":
		return []string{"--print", "--output-format", "stream-json", "--verbose", h.Task}
	case "codex":
		return []string{"--json", "--output-last-message", h.LastMessage, PromptInstructions(h.Instructions, h.Task)}
	case "cursor-agent", "agent":
		// A fresh worktree is an untrusted directory; without --trust print mode exits.
		return []string{"--print", "--trust", "--output-format", "stream-json", PromptInstructions(h.Instructions, h.Task)}
	case "agy":
		// --print takes the prompt as its value, so it goes last with the task after it.
		return []string{"--output-format", "json", "--print", h.Task}
	}
	return []string{h.Task}
}

// Tokens is what the agents report in common, zero where one does not.
type Tokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	CacheRead int `json:"cache_read"`
}

// Outcome is what ki read back from a worker's output.
type Outcome struct {
	Last    string
	Session string
	// Model is the model the agent says answered, "" when it says nothing.
	Model   string
	Tokens  Tokens
	CostUSD float64
	// Err is the agent's own failure (is_error, turn.failed), "" when it ran to the end.
	Err string
	// QuotaFor is how long the provider reported it will remain over quota.
	QuotaFor time.Duration
}

// ParseOutput reads the agent's stdout in the format headlessArgs asked for.
// lastMessage is the -o file's content for Codex; it wins when present. m is
// the mode the worker ran in, so a refusal can say what to change.
func ParseOutput(agent string, m Mode, stdout []byte, lastMessage string) Outcome {
	var o Outcome
	switch filepath.Base(agent) {
	case "codex":
		o = parseCodex(stdout)
	case "agy":
		o = parseAgy(stdout, m)
	default:
		o = parseResultObject(stdout)
		o.Model = streamModel(stdout)
	}
	if lastMessage = strings.TrimSpace(lastMessage); lastMessage != "" {
		o.Last = lastMessage
	}
	if o.QuotaFor == 0 {
		o.QuotaFor = quotaDuration(limitText(o), time.Now())
	}
	return o
}

// limitText is the part of an outcome that may say the provider is over a
// limit: the agent's own error, else a final message that carries no status
// line (plain text from a CLI that died on the limit). A worker that ran and
// answered with its own status line is never read as a quota hit.
func limitText(o Outcome) string {
	if o.Err != "" {
		return o.Err
	}
	if status, detail := ParseStatus(o.Last); status == JobFailed && strings.HasPrefix(detail, "no status line") {
		return o.Last
	}
	return ""
}

// parseResultObject reads the result event that ends Claude Code's and
// Cursor's stream (or the single object a plain json run prints); the last
// line that parses wins. A stream that ends without one has no final message:
// returning its events as text would let a status marker inside one pass for the worker's.
func parseResultObject(stdout []byte) Outcome {
	lines := bytes.Split(bytes.TrimSpace(stdout), []byte("\n"))
	events := false
	for i := len(lines) - 1; i >= 0; i-- {
		var r struct {
			Type    string         `json:"type"`
			IsError bool           `json:"is_error"`
			Subtype string         `json:"subtype"`
			Result  string         `json:"result"`
			Session string         `json:"session_id"`
			Cost    float64        `json:"total_cost_usd"`
			Usage   map[string]any `json:"usage"`
		}
		if json.Unmarshal(lines[i], &r) != nil {
			continue
		}
		if r.Type != "result" {
			events = events || r.Type != ""
			continue
		}
		o := Outcome{Last: strings.TrimSpace(r.Result), Session: r.Session, CostUSD: r.Cost, Tokens: Tokens{
			Input:     intKey(r.Usage, "input_tokens", "inputTokens"),
			Output:    intKey(r.Usage, "output_tokens", "outputTokens"),
			CacheRead: intKey(r.Usage, "cache_read_input_tokens", "cacheReadTokens"),
		}}
		if r.IsError {
			o.Err = strings.TrimSpace(strings.TrimPrefix(r.Subtype, "error_"))
			if o.Err == "" {
				o.Err = "agent reported an error"
			}
			// A usage limit comes as an error result whose text is the message.
			if quotaDuration(o.Last, time.Now()) > 0 {
				o.Err, o.Last = o.Last, ""
			}
		}
		return o
	}
	if events {
		return Outcome{}
	}
	return Outcome{Last: strings.TrimSpace(string(stdout))}
}

// parseCodex reads `codex exec --json` events: the thread id, the last agent
// message, the turn's usage and any failure.
func parseCodex(stdout []byte) Outcome {
	var o Outcome
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Thread  string `json:"thread_id"`
			Message string `json:"message"`
			Item    struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				Input  int `json:"input_tokens"`
				Cached int `json:"cached_input_tokens"`
				Output int `json:"output_tokens"`
			} `json:"usage"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "thread.started":
			o.Session = ev.Thread
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				o.Last = strings.TrimSpace(ev.Item.Text)
			}
		case "turn.completed":
			o.Tokens = Tokens{Input: ev.Usage.Input, Output: ev.Usage.Output, CacheRead: ev.Usage.Cached}
		case "turn.failed":
			o.Err = ev.Error.Message
		case "error":
			if o.Err == "" {
				o.Err = ev.Message
			}
		}
	}
	return o
}

// parseAgy reads agy's print-mode object: a conversation id, a status word,
// the response and token counts. In edits mode a refused command means the
// allow-list was too short, and the error says where it lives.
func parseAgy(stdout []byte, m Mode) Outcome {
	var r struct {
		Conversation string `json:"conversation_id"`
		Status       string `json:"status"`
		Response     string `json:"response"`
		ShortError   string `json:"short_error"`
		Error        string `json:"error"`
		Usage        struct {
			Input     int `json:"input_tokens"`
			Output    int `json:"output_tokens"`
			CacheRead int `json:"cache_read_tokens"`
		} `json:"usage"`
		Denied []struct {
			Action string `json:"action"`
		} `json:"denied_actions"`
	}
	if json.Unmarshal(bytes.TrimSpace(stdout), &r) != nil {
		return Outcome{Last: strings.TrimSpace(string(stdout))}
	}
	o := Outcome{Last: strings.TrimSpace(r.Response), Session: r.Conversation, Tokens: Tokens{Input: r.Usage.Input, Output: r.Usage.Output, CacheRead: r.Usage.CacheRead}}
	switch {
	case r.Status != "" && r.Status != "SUCCESS":
		o.Err = strings.TrimSpace(r.ShortError)
		if o.Err == "" {
			o.Err = strings.TrimSpace(r.Error)
		}
		if o.Err == "" {
			o.Err = strings.ToLower(r.Status)
		}
		o.QuotaFor = quotaDuration(o.Err, time.Now())
	case len(r.Denied) > 0 && o.Last == "":
		// Headless agy cannot prompt, so a tool it was not allowed is denied and it ends with nothing.
		actions := make([]string, 0, len(r.Denied))
		command := false
		for _, d := range r.Denied {
			actions = append(actions, d.Action)
			command = command || strings.Contains(d.Action, "command")
		}
		if m == ModeEdits && command {
			o.Err = "agy refused a command that is not on the allow-list (denied: " + strings.Join(actions, ", ") +
				"); add it under delegate.allow in machine.yaml, or run with --mode skip"
			break
		}
		o.Err = "tools denied without a prompt: " + strings.Join(actions, ", ") + " (run with --mode skip)"
	}
	return o
}

func intKey(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			return int(f)
		}
	}
	return 0
}

// ResumeArgs are the agent's own flags to reopen the session a job ran,
// so a BLOCKED job can be answered interactively (ki mecca new <job>).
func ResumeArgs(agent, session string) []string {
	if session == "" {
		return nil
	}
	switch filepath.Base(agent) {
	case "claude", "cursor-agent", "agent":
		return []string{"--resume", session}
	case "codex":
		return []string{"resume", session}
	case "agy":
		return []string{"--conversation", session}
	}
	return nil
}

// PullURL is the pull request URL in s, "" when there is none.
func PullURL(s string) string { return prURL.FindString(s) }
