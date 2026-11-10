// Package sessions defines the versioned wire format used to mirror agent sessions.
package sessions

import (
	"fmt"
	"time"
)

// SchemaVersion is the current wire schema version.
const SchemaVersion = 1

// Limits of one batch. The receiver refuses a request body over MaxBatchBytes, so a
// reporter splits its work to stay under all three.
const (
	MaxBatchSessions = 1000
	MaxBatchTurns    = 10000
	MaxBatchBytes    = 1 << 20
)

// Session describes one native agent session.
type Session struct {
	ID               string    `json:"id"`
	Tool             string    `json:"tool"`
	ToolVersion      string    `json:"tool_version,omitempty"`
	ParentID         string    `json:"parent_id,omitempty"`
	JobID            string    `json:"job_id,omitempty"`
	WindowID         string    `json:"window_id,omitempty"`
	Workspace        string    `json:"workspace,omitempty"`
	Project          string    `json:"project,omitempty"`
	Repo             string    `json:"repo,omitempty"`
	Branch           string    `json:"branch,omitempty"`
	CWD              string    `json:"cwd,omitempty"`
	Title            string    `json:"title,omitempty"`
	Models           []string  `json:"models,omitempty"`
	Started          time.Time `json:"started,omitempty"`
	LastActivity     time.Time `json:"last_activity,omitempty"`
	Ended            time.Time `json:"ended,omitempty"`
	Status           string    `json:"status"`
	InputTokens      int64     `json:"input_tokens,omitempty"`
	OutputTokens     int64     `json:"output_tokens,omitempty"`
	CacheReadTokens  int64     `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64     `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64     `json:"reasoning_tokens,omitempty"`
	CostUSD          *float64  `json:"cost_usd,omitempty"`
	Level            string    `json:"level"`
	Schema           int       `json:"schema"`
}

// ToolCall describes a tool invocation without its output body.
type ToolCall struct {
	Name       string `json:"name"`
	Target     string `json:"target,omitempty"`
	Status     string `json:"status,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	OutputSize int64  `json:"output_size,omitempty"`
	Diff       string `json:"diff,omitempty"`
}

// Turn is one ordered, idempotent record in a session.
type Turn struct {
	SessionID        string     `json:"session_id"`
	Key              string     `json:"key"`
	Seq              int64      `json:"seq"`
	Role             string     `json:"role"`
	Time             time.Time  `json:"time,omitempty"`
	ObservedAt       time.Time  `json:"observed_at,omitempty"`
	Model            string     `json:"model,omitempty"`
	Text             string     `json:"text,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	InputTokens      int64      `json:"input_tokens,omitempty"`
	OutputTokens     int64      `json:"output_tokens,omitempty"`
	CacheReadTokens  int64      `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64      `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int64      `json:"reasoning_tokens,omitempty"`
	Event            string     `json:"event,omitempty"`
}

// Batch contains sessions and turns sent together to the receiver.
type Batch struct {
	Schema   int       `json:"schema"`
	Sessions []Session `json:"sessions"`
	Turns    []Turn    `json:"turns"`
	Gap      bool      `json:"gap,omitempty"`
}

// Validate checks schema, required fields, enums, and conservative wire limits.
func (b Batch) Validate() error {
	if b.Schema != SchemaVersion {
		return fmt.Errorf("schema must be %d", SchemaVersion)
	}
	if len(b.Sessions) > MaxBatchSessions || len(b.Turns) > MaxBatchTurns {
		return fmt.Errorf("batch exceeds item limit")
	}
	for i, s := range b.Sessions {
		if s.ID == "" || s.Tool == "" {
			return fmt.Errorf("sessions[%d]: id and tool are required", i)
		}
		if s.Tool != "claude" && s.Tool != "codex" && s.Tool != "agy" && s.Tool != "cursor" {
			return fmt.Errorf("sessions[%d]: invalid tool", i)
		}
		if s.Schema != SchemaVersion {
			return fmt.Errorf("sessions[%d]: schema must be %d", i, SchemaVersion)
		}
		if s.Status != "running" && s.Status != "idle" && s.Status != "ended" {
			return fmt.Errorf("sessions[%d]: invalid status", i)
		}
		if s.Level != "full" && s.Level != "metadata" {
			return fmt.Errorf("sessions[%d]: invalid level", i)
		}
		if len(s.Title) > 4096 || len(s.CWD) > 4096 || len(s.Repo) > 2048 || len(s.Branch) > 1024 || len(s.Models) > 64 || s.InputTokens < 0 || s.OutputTokens < 0 || s.CacheReadTokens < 0 || s.CacheWriteTokens < 0 || s.ReasoningTokens < 0 || (s.CostUSD != nil && *s.CostUSD < 0) {
			return fmt.Errorf("sessions[%d]: field exceeds size limit", i)
		}
		for _, at := range []time.Time{s.Started, s.LastActivity, s.Ended} {
			if !at.IsZero() && at.After(time.Now().Add(time.Minute)) {
				return fmt.Errorf("sessions[%d]: timestamp is in the future", i)
			}
		}
	}
	for i, t := range b.Turns {
		if t.SessionID == "" || t.Key == "" || t.Seq < 0 {
			return fmt.Errorf("turns[%d]: session_id, key and non-negative seq are required", i)
		}
		if t.Role != "user" && t.Role != "assistant" && t.Role != "tool" && t.Role != "event" {
			return fmt.Errorf("turns[%d]: invalid role", i)
		}
		if t.Time.IsZero() && t.ObservedAt.IsZero() {
			return fmt.Errorf("turns[%d]: time or observed_at is required", i)
		}
		for _, at := range []time.Time{t.Time, t.ObservedAt} {
			if !at.IsZero() && at.After(time.Now().Add(time.Minute)) {
				return fmt.Errorf("turns[%d]: timestamp is in the future", i)
			}
		}
		if len(t.Key) > 512 || len(t.Text) > 32768 || len(t.ToolCalls) > 256 {
			return fmt.Errorf("turns[%d]: field exceeds size limit", i)
		}
		if t.Role == "event" && !validEvent(t.Event) {
			return fmt.Errorf("turns[%d]: invalid or missing event", i)
		}
		for j, c := range t.ToolCalls {
			if c.Name == "" || len(c.Name) > 256 || len(c.Target) > 4096 || len(c.Diff) > 65536 {
				return fmt.Errorf("turns[%d].tool_calls[%d]: invalid field", i, j)
			}
		}
	}
	return nil
}

func validEvent(event string) bool {
	switch event {
	case "compaction", "error", "interrupt", "turn_end":
		return true
	}
	return false
}
