package ki

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Streams reports whether the agent prints one JSON event per line while it
// works. agy prints a single object at the end, so it has nothing to follow.
func Streams(agent string) bool {
	switch filepath.Base(agent) {
	case "claude", "codex", "cursor-agent", "agent":
		return true
	}
	return false
}

// Clock renders a job's elapsed time: 02:14, or 1:02:14 past an hour.
func Clock(d time.Duration) string {
	s := int(d.Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// Progress reads one line of a worker's stream and returns what it did, one
// entry per tool call or assistant message; thinking, tool results and
// bookkeeping events give nothing.
func Progress(line []byte) []string {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message struct {
			Content []block `json:"content"`
		} `json:"message"`
		ToolCall map[string]json.RawMessage `json:"tool_call"`
		Item     struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Command string `json:"command"`
			Changes []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return nil
	}
	switch ev.Type {
	case "assistant":
		return blockLines(ev.Message.Content)
	case "tool_call":
		if ev.Subtype != "started" {
			return nil
		}
		// The call sits beside bookkeeping keys of other shapes: the one named <tool>ToolCall.
		for name, raw := range ev.ToolCall {
			kind, ok := strings.CutSuffix(name, "ToolCall")
			if !ok {
				continue
			}
			var c struct {
				Args map[string]any `json:"args"`
			}
			_ = json.Unmarshal(raw, &c) // a call without readable args still names the tool
			return []string{toolLine(kind, c.Args)}
		}
	case "item.started":
		if ev.Item.Type == "command_execution" {
			return []string{"run " + clip(shellInner(ev.Item.Command))}
		}
	case "item.completed":
		switch ev.Item.Type {
		case "agent_message":
			return []string{"message: " + clip(firstLine(ev.Item.Text))}
		case "file_change":
			paths := make([]string, 0, len(ev.Item.Changes))
			for _, c := range ev.Item.Changes {
				paths = append(paths, c.Path)
			}
			return []string{"edit " + clip(strings.Join(paths, ", "))}
		}
	}
	return nil
}

type block struct {
	Type  string         `json:"type"`
	Text  string         `json:"text"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

func blockLines(blocks []block) []string {
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := firstLine(b.Text); t != "" {
				out = append(out, "message: "+clip(t))
			}
		case "tool_use":
			out = append(out, toolLine(b.Name, b.Input))
		}
	}
	return out
}

// targetKeys are the tool arguments that say what a call acts on, best first.
var targetKeys = []string{"file_path", "path", "command", "globPattern", "pattern", "query", "url", "description"}

func toolLine(name string, args map[string]any) string {
	line := strings.ToLower(name)
	for _, k := range targetKeys {
		if s, ok := args[k].(string); ok && s != "" {
			return line + " " + clip(firstLine(s))
		}
	}
	return line
}

// shellWrapper is how Codex shows a command: the login shell it ran it with.
var shellWrapper = regexp.MustCompile(`^\S*sh -lc '(.*)'$`)

func shellInner(cmd string) string {
	if m := shellWrapper.FindStringSubmatch(cmd); m != nil {
		return m[1]
	}
	return cmd
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func clip(s string) string {
	if r := []rune(s); len(r) > 100 {
		return string(r[:99]) + "…"
	}
	return s
}
