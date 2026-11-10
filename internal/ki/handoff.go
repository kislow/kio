package ki

import (
	"fmt"
	"strings"
)

// HandoffDiffLines caps the diff a handoff note carries; the worker can read
// the rest with git in the worktree it continues in.
const HandoffDiffLines = 300

// WorkDone is how far a stopped job got, read from its worktree.
type WorkDone struct {
	// Base is the ref the branch started from, such as origin/main.
	Base string
	// Commits are the branch's commits since Base, oldest first, `<sha> <subject>`.
	Commits []string
	// DiffStat is `git diff --stat` against Base, uncommitted changes included.
	DiffStat string
	// Untracked are new files git does not track yet.
	Untracked []string
	// Diff is the diff against Base, cut at HandoffDiffLines.
	Diff string
}

// HandoffTask is the brief for a job that continues prev: the original task,
// then a note on what the earlier worker did and where it stopped, so the
// next worker finishes the work instead of starting over.
func HandoffTask(task string, prev Result, p WorkDone) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(task))
	b.WriteString("\n\n## Handoff\n\n")
	who := prev.Provider
	if prev.Model != "" {
		who += " (" + prev.Model + ")"
	}
	fmt.Fprintf(&b, "An earlier worker, %s in job %s, started this task and stopped before it finished (%s", who, prev.ID, prev.Status)
	if prev.Detail != "" {
		fmt.Fprintf(&b, ": %s", prev.Detail)
	}
	b.WriteString("). You continue in the same worktree")
	if prev.Branch != "" {
		fmt.Fprintf(&b, ", on branch %s", prev.Branch)
	}
	b.WriteString(": its commits and edits are already here. Check what it did, keep what is right, finish the task and do not redo finished work.\n")
	base := orDefaultText(p.Base, "the start of the branch")
	if len(p.Commits) > 0 {
		fmt.Fprintf(&b, "\nCommits since %s:\n", base)
		for _, c := range p.Commits {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	} else {
		fmt.Fprintf(&b, "\nNo commits since %s.\n", base)
	}
	if s := strings.TrimSpace(p.DiffStat); s != "" {
		fmt.Fprintf(&b, "\nChanges against %s, uncommitted ones included:\n\n```text\n%s\n```\n", base, s)
	}
	if len(p.Untracked) > 0 {
		fmt.Fprintf(&b, "\nNew files not yet added: %s\n", strings.Join(p.Untracked, ", "))
	}
	if d := strings.TrimSpace(p.Diff); d != "" {
		fmt.Fprintf(&b, "\nThe diff so far:\n\n```diff\n%s\n```\n", d)
	}
	if len(prev.Tail) > 0 {
		b.WriteString("\nIts last progress lines:\n\n```text\n")
		b.WriteString(strings.Join(prev.Tail, "\n"))
		b.WriteString("\n```\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// CapLines keeps the first n lines of s and says how many were cut.
func CapLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n... %d more lines: run git diff in the worktree", len(lines)-n)
}

func orDefaultText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
