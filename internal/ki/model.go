package ki

import (
	"bufio"
	"bytes"
	"encoding/json"
	"path/filepath"
	"sort"
)

// CursorAuto is the model recorded for a Cursor job that named none and whose
// stream named none either: Cursor then chose for itself in its Auto mode.
const CursorAuto = "auto"

// ResolvedModel is the model a job ran on: what the agent reported, else what
// ki asked for, else Cursor's own Auto choice. It is "" only when nothing was
// asked and the agent reported nothing.
func ResolvedModel(agent, requested, reported string) string {
	switch {
	case reported != "":
		return reported
	case requested != "":
		return requested
	case isCursor(agent):
		return CursorAuto
	}
	return ""
}

func isCursor(agent string) bool {
	switch filepath.Base(agent) {
	case "agent", "cursor-agent":
		return true
	}
	return false
}

// usage is one model's share of a Claude Code result event.
type usage struct {
	OutputTokens int `json:"outputTokens"`
}

// streamModel reads the model out of a Claude Code or Cursor stream. Claude
// Code's result event lists every model that answered under modelUsage; the
// session's own model is the one its init event names, and when the init
// event names none of them the one that wrote the most wins (subagents may run
// a cheaper one). Cursor reports only the init event's model ("Auto Balance"
// when it chose for itself). An assistant message's model is the last resort.
func streamModel(stdout []byte) string {
	var initModel, msgModel string
	var used map[string]usage
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev struct {
			Type       string           `json:"type"`
			Subtype    string           `json:"subtype"`
			Model      string           `json:"model"`
			ModelUsage map[string]usage `json:"modelUsage"`
			SinceLast  map[string]usage `json:"model_usage_since_last_result"`
			Message    struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && initModel == "" {
				initModel = ev.Model
			}
		case "assistant":
			if ev.Message.Model != "" {
				msgModel = ev.Message.Model
			}
		case "result":
			used = ev.ModelUsage
			if len(used) == 0 {
				used = ev.SinceLast
			}
		}
	}
	if m := busiest(used, initModel); m != "" {
		return m
	}
	if initModel != "" {
		return initModel
	}
	return msgModel
}

// busiest is prefer when it is among the models used, else the one that
// wrote the most output, else "".
func busiest(used map[string]usage, prefer string) string {
	if _, ok := used[prefer]; ok && prefer != "" {
		return prefer
	}
	names := make([]string, 0, len(used))
	for n := range used {
		names = append(names, n)
	}
	sort.Strings(names)
	best := ""
	for _, n := range names {
		if best == "" || used[n].OutputTokens > used[best].OutputTokens {
			best = n
		}
	}
	return best
}
