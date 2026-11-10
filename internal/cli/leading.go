package cli

import (
	"slices"
	"strings"

	"github.com/kislow/kio/internal/ki"
)

// coreCommands are the first words Run dispatches itself, apart from the registered extensions.
var coreCommands = []string{
	"help", "version", "status", "recap", "standup", "update", "doctor", "cursor", "setup", "vault",
	"diagram", "validate", "mecca", "delegate", "approve", "ask", "skill", "session", "mcp", "kb", "queue", "run",
}

// maxSuggestDistance is how many edits away a word may be and still draw a suggestion.
const maxSuggestDistance = 2

// checkLeadingWord refuses a first word that starts a session by accident: not a flag, not a
// command, not an extension and not an agent. It only sees the first argument, so a flag's
// value and everything after the flags or `--` still go to the agent unchanged.
func checkLeadingWord(word string) error {
	if strings.HasPrefix(word, "-") || slices.Contains(ki.ShorthandAgents(), word) {
		return nil
	}
	msg := "unknown command or agent " + quote(word) + "\nagents: " + strings.Join(ki.ShorthandAgents(), ", ")
	if near := closest(word); near != "" {
		msg += "\ndid you mean `ki " + near + "`?"
	}
	msg += "\nagent arguments go after `--`: ki -- " + quote(word)
	return fail("%s", msg)
}

func quote(s string) string { return `"` + s + `"` }

// closest is the command or agent within maxSuggestDistance edits of word, "" when none is.
func closest(word string) string {
	candidates := append(slices.Clone(coreCommands), ki.ShorthandAgents()...)
	for _, e := range extensions {
		candidates = append(candidates, e.name)
	}
	best, bestDist := "", maxSuggestDistance+1
	for _, c := range candidates {
		if d := editDistance(word, c); d < bestDist && d < len(word) {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
