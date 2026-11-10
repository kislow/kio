package ki

import (
	"strings"
	"testing"
)

func TestHandoffTaskCarriesTheWorkSoFar(t *testing.T) {
	t.Parallel()
	prev := Result{
		ID: "j-20261107-090000-abcd", Provider: "claude", Model: "sonnet", Status: JobQuota, Detail: "You've hit your limit", Branch: "feat/1-x",
		Tail: []string{"ki: 00:10 claude  edit a.go", "ki: 00:20 claude  bash go test ./..."},
	}
	got := HandoffTask("  Add the thing.  ", prev, WorkDone{
		Base: "origin/main", Commits: []string{"abc123 wip: half"}, DiffStat: " a.go | 2 +-", Untracked: []string{"b.go"}, Diff: "+x",
	})
	for _, want := range []string{
		"Add the thing.\n\n## Handoff",
		"claude (sonnet) in job j-20261107-090000-abcd",
		"(QUOTA: You've hit your limit)",
		"on branch feat/1-x",
		"Commits since origin/main:\n- abc123 wip: half",
		"a.go | 2 +-",
		"New files not yet added: b.go",
		"```diff\n+x\n```",
		"ki: 00:20 claude  bash go test ./...",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	bare := HandoffTask("t", Result{ID: "j", Provider: "codex", Status: JobStalled}, WorkDone{})
	if !strings.Contains(bare, "No commits since the start of the branch.") || strings.Contains(bare, "```") || strings.Contains(bare, "on branch") {
		t.Fatalf("bare handoff:\n%s", bare)
	}
}

func TestCapLines(t *testing.T) {
	t.Parallel()
	if got := CapLines("a\nb\n", 2); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
	if got := CapLines("a\nb\nc\nd", 2); got != "a\nb\n... 2 more lines: run git diff in the worktree" {
		t.Fatalf("got %q", got)
	}
}
