package ki

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStreamModelFromRealStreams(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent, file, want string
	}{
		// init and modelUsage both name it; the result event is what decides.
		{"claude", "claude-model.jsonl", "claude-haiku-5-5"},
		// No init event, the result's per-model usage under its older key.
		{"claude", "claude.jsonl", "claude-fable-5-1"},
		// Cursor run without --model: init names its Auto choice and nothing more.
		{"agent", "cursor.jsonl", "Auto Balance"},
		// Codex's stream names no model.
		{"codex", "codex.jsonl", ""},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			if got := ParseOutput(tt.agent, ModeAuto, fixture(t, tt.file), "").Model; got != tt.want {
				t.Fatalf("model %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStreamModel(t *testing.T) {
	t.Parallel()
	const (
		initOpus  = `{"type":"system","subtype":"init","model":"claude-opus-5-5"}`
		subagents = `{"type":"result","modelUsage":{"claude-haiku-5-5":{"outputTokens":900},"claude-opus-5-5":{"outputTokens":300}}}`
	)
	tests := map[string]struct {
		stream, want string
	}{
		"empty stream":                    {"", ""},
		"not json":                        {"Error: not logged in", ""},
		"result wins over init":           {`{"type":"system","subtype":"init","model":"sonnet"}` + "\n" + `{"type":"result","modelUsage":{"claude-sonnet-5-5":{"outputTokens":4}}}`, "claude-sonnet-5-5"},
		"init model among several":        {initOpus + "\n" + subagents, "claude-opus-5-5"},
		"busiest when init names none":    {subagents, "claude-haiku-5-5"},
		"tie breaks by name":              {`{"type":"result","modelUsage":{"b":{"outputTokens":1},"a":{"outputTokens":1}}}`, "a"},
		"init alone":                      {initOpus + "\n" + `{"type":"result","result":"DONE: x"}`, "claude-opus-5-5"},
		"assistant message last resort":   {`{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[]}}`, "claude-sonnet-5-5"},
		"first init wins over a later":    {initOpus + "\n" + `{"type":"system","subtype":"init","model":"other"}`, "claude-opus-5-5"},
		"other system events are ignored": {`{"type":"system","subtype":"hook_started","model":"x"}`, ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := streamModel([]byte(tt.stream)); got != tt.want {
				t.Fatalf("model %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvedModel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent, requested, reported, want string
	}{
		{"claude", "sonnet", "claude-sonnet-5-5", "claude-sonnet-5-5"},
		{"claude", "sonnet", "", "sonnet"},
		{"claude", "", "", ""},
		{"codex", "", "", ""},
		{"agent", "", "Auto Balance", "Auto Balance"},
		{"/usr/local/bin/cursor-agent", "", "", CursorAuto},
		{"agent", "gpt-5", "", "gpt-5"},
	}
	for _, tt := range tests {
		if got := ResolvedModel(tt.agent, tt.requested, tt.reported); got != tt.want {
			t.Errorf("ResolvedModel(%q, %q, %q) = %q, want %q", tt.agent, tt.requested, tt.reported, got, tt.want)
		}
	}
}

func TestLedgerKeepsRequestedAndResolvedModels(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), LedgerFile)
	started := time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	// A record from before the split holds only model, the one ki passed.
	legacy := `{"id":"j-20261106-090000-0001","provider":"claude","model":"sonnet","status":"DONE","detail":"","started":"2026-11-06T09:00:00Z","duration_s":1,"tokens":{"input":0,"output":0,"cache_read":0},"exit":0}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Result{
		{ID: "j-20261106-090100-0002", Provider: "agent", Model: "Auto Balance", Status: JobDone, Started: started},
		{ID: "j-20261106-090200-0003", Provider: "claude", Model: "claude-sonnet-5-5", ModelRequested: "sonnet", Status: JobDone, Started: started},
	} {
		if err := AppendLedger(path, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ReadLedger(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("ledger %+v %v", got, err)
	}
	for i, want := range [][2]string{{"sonnet", "sonnet"}, {"Auto Balance", ""}, {"claude-sonnet-5-5", "sonnet"}} {
		if got[i].Model != want[0] || got[i].ModelRequested != want[1] {
			t.Errorf("job %d: model %q requested %q, want %q %q", i, got[i].Model, got[i].ModelRequested, want[0], want[1])
		}
	}
}
