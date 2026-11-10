package sessions

import (
	"testing"
	"time"
)

func TestBatchValidate(t *testing.T) {
	t.Parallel()
	base := Batch{Schema: SchemaVersion, Sessions: []Session{{ID: "codex:1", Tool: "codex", Status: "running", Level: "full", Schema: SchemaVersion}}, Turns: []Turn{{SessionID: "codex:1", Key: "k", Role: "user", ObservedAt: time.Now()}}}
	for _, tc := range []struct {
		name string
		edit func(*Batch)
		want string
	}{
		{"valid", func(*Batch) {}, ""},
		{"schema", func(b *Batch) { b.Schema = 2 }, "schema must"},
		{"missing session id", func(b *Batch) { b.Sessions[0].ID = "" }, "id and tool"},
		{"bad session status", func(b *Batch) { b.Sessions[0].Status = "waiting" }, "invalid status"},
		{"bad session tool", func(b *Batch) { b.Sessions[0].Tool = "unknown" }, "invalid tool"},
		{"bad level", func(b *Batch) { b.Sessions[0].Level = "none" }, "invalid level"},
		{"bad session schema", func(b *Batch) { b.Sessions[0].Schema = 0 }, "schema must"},
		{"bad turn role", func(b *Batch) { b.Turns[0].Role = "system" }, "invalid role"},
		{"required turn key", func(b *Batch) { b.Turns[0].Key = "" }, "required"},
		{"negative sequence", func(b *Batch) { b.Turns[0].Seq = -1 }, "required"},
		{"oversized text", func(b *Batch) { b.Turns[0].Text = string(make([]byte, 32769)) }, "size limit"},
		{"bad event", func(b *Batch) { b.Turns[0].Role = "event"; b.Turns[0].Event = "wat" }, "invalid or missing event"},
		{"missing event", func(b *Batch) { b.Turns[0].Role = "event" }, "invalid or missing event"},
		{"missing timestamp", func(b *Batch) { b.Turns[0].ObservedAt = time.Time{} }, "time or observed_at"},
		{"bad tool call", func(b *Batch) { b.Turns[0].ToolCalls = []ToolCall{{}} }, "invalid field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := base
			b.Sessions = append([]Session(nil), base.Sessions...)
			b.Turns = append([]Turn(nil), base.Turns...)
			tc.edit(&b)
			err := b.Validate()
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !contains(err.Error(), tc.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func contains(s, part string) bool {
	for i := 0; i+len(part) <= len(s); i++ {
		if s[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
