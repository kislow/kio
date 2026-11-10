// Package pack turns retrieved facts into a context pack: markdown with
// fixed headings in a fixed order, trimmed to a token budget. Every model
// sees the same structure, which is what keeps quality similar across LLMs.
package pack

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kislow/kio/internal/kb"
)

// Section order is part of the contract with agents. Do not reorder.
var sections = []struct {
	Title string
	Kind  kb.Kind
}{
	{"Conventions", kb.KindConvention},
	{"Active decisions", kb.KindDecision},
	{"Current state", kb.KindState},
	{"Pointers", kb.KindPointer},
}

// Fill priority when the budget is tight: decisions first, pointers last.
var priority = []kb.Kind{kb.KindDecision, kb.KindConvention, kb.KindState, kb.KindPointer}

// EstimateTokens is a deliberately simple, model-independent estimate.
func EstimateTokens(s string) int { return (len(s) + 3) / 4 }

// Build renders the pack. Superseded facts are dropped; history is served by
// decisions_for, not by get_context.
func Build(task string, brains []string, facts []kb.Fact, budget int) string {
	header := fmt.Sprintf("# Context pack\n\nTask: %s\nBrains: %s\n", oneLine(task), strings.Join(brains, ", "))
	used := EstimateTokens(header)

	byKind := map[kb.Kind][]kb.Fact{}
	seen := map[string]bool{}
	for _, f := range facts {
		key := string(f.Kind) + "|" + f.Fact
		if !f.Current() || seen[key] {
			continue
		}
		seen[key] = true
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	for k := range byKind {
		fs := byKind[k]
		sort.SliceStable(fs, func(i, j int) bool { return fs[i].Score > fs[j].Score })
	}

	chosen := map[kb.Kind][]string{}
	// Reserve room for every section heading so the structure is always intact.
	for _, s := range sections {
		used += EstimateTokens("\n## " + s.Title + "\n\n_none_\n")
	}
	// Round-robin across kinds in priority order so one kind cannot starve the rest.
	idx := map[kb.Kind]int{}
	for progress := true; progress; {
		progress = false
		for _, k := range priority {
			i := idx[k]
			if i >= len(byKind[k]) {
				continue
			}
			line := bullet(byKind[k][i])
			cost := EstimateTokens(line)
			idx[k] = i + 1
			if used+cost > budget {
				continue
			}
			chosen[k] = append(chosen[k], line)
			used += cost
			progress = true
		}
	}

	var b strings.Builder
	b.WriteString(header)
	for _, s := range sections {
		b.WriteString("\n## " + s.Title + "\n\n")
		if len(chosen[s.Kind]) == 0 {
			b.WriteString("_none_\n")
			continue
		}
		for _, line := range chosen[s.Kind] {
			b.WriteString(line)
		}
	}
	return b.String()
}

func bullet(f kb.Fact) string {
	line := "- " + oneLine(f.Fact)
	if f.Source != "" {
		line += " (" + f.Source + ")"
	}
	if f.Brain != "" {
		line += " [" + f.Brain + "]"
	}
	return line + "\n"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
