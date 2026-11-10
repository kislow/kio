package ki

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The fixtures are real runs of each agent, trimmed of bulk and local paths.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "stream", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func progressOf(stream []byte) []string {
	lines := bytes.Split(stream, []byte("\n"))
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, Progress(line)...)
	}
	return out
}

func TestProgressFromRealStreams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent, file string
		want        []string
	}{
		{"claude", "claude.jsonl", []string{
			"message: Reading a.txt now.",
			"read /work/repo/a.txt",
			"bash ls -la /work/repo /work/repo 2>&1; find /work/repo -name 'a.txt' 2>/dev/null",
			"message: The file a.txt does not exist in /work/repo or /work/repo. The directory only contains claude.err, …",
		}},
		{"cursor-agent", "cursor.jsonl", []string{"message: I'll read `a.txt` and then reply with the exact text you asked for.", "read /work/repo/a.txt", "glob **/a.txt", "message: DONE: ok"}},
		{"codex", "codex.jsonl", []string{"run cat a.txt", "message: DONE: ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			t.Parallel()
			got := progressOf(fixture(t, tt.file))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestProgressLines(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		agent, line string
		want        []string
	}{
		"edit has its path":          {"claude", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"docs/x.md"}}]}}`, []string{"edit docs/x.md"}},
		"two blocks, two lines":      {"claude", `{"type":"assistant","message":{"content":[{"type":"text","text":"on it\nsecond"},{"type":"tool_use","name":"Grep","input":{"pattern":"foo"}}]}}`, []string{"message: on it", "grep foo"}},
		"tool without a target":      {"claude", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"ExitPlanMode","input":{}}]}}`, []string{"exitplanmode"}},
		"thinking is skipped":        {"claude", `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hm"}]}}`, nil},
		"tool results are skipped":   {"claude", `{"type":"user","message":{"content":[{"type":"tool_result","content":"x"}]}}`, nil},
		"not json":                   {"claude", `plain stderr`, nil},
		"cursor finished call":       {"cursor-agent", `{"type":"tool_call","subtype":"completed","tool_call":{"readToolCall":{"args":{"path":"a"}}}}`, nil},
		"cursor shell":               {"cursor-agent", `{"type":"tool_call","subtype":"started","tool_call":{"shellToolCall":{"args":{"command":"go test ./..."}}}}`, []string{"shell go test ./..."}},
		"codex file change":          {"codex", `{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go"},{"path":"b.go"}]}}`, []string{"edit a.go, b.go"}},
		"codex started message":      {"codex", `{"type":"item.started","item":{"type":"agent_message"}}`, nil},
		"long text is clipped":       {"claude", `{"type":"assistant","message":{"content":[{"type":"text","text":"` + string(bytes.Repeat([]byte("x"), 300)) + `"}]}}`, []string{"message: " + string(bytes.Repeat([]byte("x"), 99)) + "…"}},
		"codex command without wrap": {"codex", `{"type":"item.started","item":{"type":"command_execution","command":"ls"}}`, []string{"run ls"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := Progress([]byte(tt.line)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseOutputReadsTheFinalEventOfRealStreams(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		agent, file, status, session string
		tokens                       Tokens
	}{
		"claude": {"claude", "claude.jsonl", JobFailed, "301056f4-b19a-4ac7-b88a-e8601b94f396", Tokens{Input: 66, Output: 386, CacheRead: 123047}},
		"cursor": {"cursor-agent", "cursor.jsonl", JobDone, "14b24b60-25a3-4658-ac60-c38f920591ab", Tokens{Input: 8811, Output: 308, CacheRead: 35072}},
		"codex":  {"codex", "codex.jsonl", JobDone, "01a1770c-28d9-7361-b170-8d984a5e7307", Tokens{Input: 36302, Output: 105, CacheRead: 28160}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := ParseOutput(tt.agent, ModeAuto, fixture(t, tt.file), "")
			if status, _ := ParseStatus(got.Last); status != tt.status || got.Session != tt.session || got.Tokens != tt.tokens || got.Err != "" {
				t.Fatalf("got %+v, status %s", got, status)
			}
		})
	}
}

func TestCursorResultEventEndsTheStream(t *testing.T) {
	t.Parallel()
	stream := `{"type":"system","subtype":"init","session_id":"488f"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"DONE: hi"}]}}
{"type":"result","subtype":"success","is_error":false,"result":"DONE: hi","session_id":"488f","usage":{"inputTokens":8519,"outputTokens":454,"cacheReadTokens":6016}}`
	got := ParseOutput("cursor-agent", ModeAuto, []byte(stream), "")
	want := Outcome{Last: "DONE: hi", Session: "488f", Tokens: Tokens{Input: 8519, Output: 454, CacheRead: 6016}}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if lines := progressOf([]byte(stream)); !reflect.DeepEqual(lines, []string{"message: DONE: hi"}) {
		t.Fatalf("progress %q", lines)
	}
}

func TestStreamWithoutResultHasNoMessage(t *testing.T) {
	t.Parallel()
	// A status marker inside an event must not pass for the worker's final line.
	stream := `{"type":"assistant","message":{"content":[{"type":"text","text":"I will say DONE: soon"}]}}`
	if got := ParseOutput("claude", ModeAuto, []byte(stream), ""); got.Last != "" {
		t.Fatalf("got %q", got.Last)
	}
}

func TestStreamsAndClock(t *testing.T) {
	t.Parallel()
	for agent, want := range map[string]bool{"claude": true, "/bin/codex": true, "cursor-agent": true, "agent": true, "agy": false} {
		if Streams(agent) != want {
			t.Errorf("Streams(%s) = %v", agent, !want)
		}
	}
	for d, want := range map[time.Duration]string{0: "00:00", 134 * time.Second: "02:14", 59*time.Minute + 59*time.Second: "59:59", time.Hour + 2*time.Minute + 14*time.Second: "1:02:14"} {
		if got := Clock(d); got != want {
			t.Errorf("Clock(%s) = %s, want %s", d, got, want)
		}
	}
}

func TestReadRunning(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(id, status string, pid int, started time.Time) {
		t.Helper()
		if err := WritePrivate(filepath.Join(dir, id, "result.json"), Result{ID: id, Status: status, Pid: pid, Started: started}); err != nil {
			t.Fatal(err)
		}
	}
	write("j-20261026-070000-aaaa", JobRunning, os.Getpid(), now.Add(-time.Minute))
	write("j-20261026-071000-bbbb", JobRunning, os.Getpid(), now)
	write("j-20261026-072000-cccc", JobRunning, 0, now)
	write("j-20261026-073000-dddd", JobDone, 0, now)
	if err := os.MkdirAll(filepath.Join(dir, "not-a-job"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "j-20261026-074000-eeee"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRunning(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids, statuses := make([]string, 0, len(got)), make([]string, 0, len(got))
	for _, r := range got {
		ids, statuses = append(ids, r.ID), append(statuses, r.Status)
	}
	if !reflect.DeepEqual(ids, []string{"j-20261026-071000-bbbb", "j-20261026-072000-cccc", "j-20261026-070000-aaaa"}) && !reflect.DeepEqual(ids, []string{"j-20261026-072000-cccc", "j-20261026-071000-bbbb", "j-20261026-070000-aaaa"}) {
		t.Fatalf("ids %v", ids)
	}
	if lost := got[0].Status + got[1].Status; lost != JobRunning+JobLost && lost != JobLost+JobRunning {
		t.Fatalf("statuses %v: the dead worker is LOST, the live one RUNNING", statuses)
	}
	if missing, err := ReadRunning(filepath.Join(dir, "none")); err != nil || missing != nil {
		t.Fatalf("no jobs folder is no jobs: %v %v", missing, err)
	}
	if _, err := ReadResult(dir, "j-20261026-074000-eeee"); err == nil {
		t.Fatal("a job folder without result.json must error")
	}
}
