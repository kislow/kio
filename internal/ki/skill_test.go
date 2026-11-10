package ki

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const ptalSkill = `---
name: slack-ptal
description: Post a PTAL.
ki-role: writing
ki-agents: [claude]
ki-tools:
  - gh pr view
  - mcp__claude_ai_Slack__slack_send_message
  - Read(./docs/**)
---

Post a PTAL for $ARGUMENTS in the channel.
`

func TestParseSkillReadsTheKiFields(t *testing.T) {
	t.Parallel()
	s, err := ParseSkill([]byte(ptalSkill))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "slack-ptal" || s.Role != ClassWriting || !slices.Equal(s.Agents, []string{"claude"}) ||
		!slices.Equal(s.Tools, []string{"gh pr view", "mcp__claude_ai_Slack__slack_send_message", "Read(./docs/**)"}) ||
		s.Body != "Post a PTAL for $ARGUMENTS in the channel." {
		t.Fatalf("skill %+v", s)
	}
}

func TestParseSkillCollectsEveryProblem(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ src, want string }{
		"no closing":  {"---\nname: x\n", "no closing ---"},
		"tools text":  {"---\nki-tools: gh pr view\n---\nbody", "ki-tools and ki-agents are lists"},
		"no body":     {"---\nname: x\n---\n", "no procedure"},
		"bad role":    {"---\nki-role: boss\n---\nbody", `ki-role "boss"`},
		"bad agent":   {"---\nki-agents: [vim]\n---\nbody", `unknown agent "vim"`},
		"push":        {"---\nki-tools: [git push]\n---\nbody", "only the caller grants"},
		"all of git":  {"---\nki-tools: [git]\n---\nbody", "only the caller grants"},
		"bash star":   {"---\nki-tools: [\"Bash(git *)\"]\n---\nbody", "only the caller grants"},
		"every shell": {"---\nki-tools: [Bash]\n---\nbody", "grants every shell command"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseSkill([]byte(tc.src)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	_, err := ParseSkill([]byte("---\nki-role: boss\nki-agents: [vim]\n---\nbody"))
	if err == nil || !strings.Contains(err.Error(), "ki-role") || !strings.Contains(err.Error(), "ki-agents") {
		t.Fatalf("want both problems, got %v", err)
	}
}

func TestParseSkillWithoutFrontmatterIsReadOnly(t *testing.T) {
	t.Parallel()
	s, err := ParseSkill([]byte("\ufeffJust do it.\n"))
	if err != nil || s.Body != "Just do it." || len(s.Tools) != 0 || s.Role != "" {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestCheckSkillTool(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		entry  string
		caller bool
		ok     bool
	}{
		{"gh pr view", false, true},
		{"git rebase", false, true},
		{"git fetch origin", false, true},
		{"Bash(git log:*)", false, true},
		{"mcp__claude_ai_Slack__slack_send_message", false, true},
		{"Read", false, true},
		{"git push --force-with-lease", false, false},
		{"git push --force-with-lease", true, true},
		{"Bash(git push:*)", false, false},
		{"git", false, false},
		{"git", true, true},
		{"Bash(*)", true, false},
		{"Bash", true, false},
		{"*", true, false},
	} {
		if err := CheckSkillTool(tc.entry, tc.caller); (err == nil) != tc.ok {
			t.Errorf("CheckSkillTool(%q, caller=%v) = %v, want ok=%v", tc.entry, tc.caller, err, tc.ok)
		}
	}
}

func TestClaudeToolRule(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"gh pr view":                 "Bash(gh pr view:*)",
		"Bash(terraform -chdir=* x)": "Bash(terraform -chdir=* x)",
		"mcp__ki-kb__search":         "mcp__ki-kb__search",
		"Read(./docs/**)":            "Read(./docs/**)",
		"WebFetch":                   "WebFetch",
	} {
		if got := ClaudeToolRule(in); got != want {
			t.Errorf("ClaudeToolRule(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkillClaudeArgs(t *testing.T) {
	t.Parallel()
	ro := SkillClaudeArgs(nil, []string{"/skills/x"}, "/bin/ki", nil)
	joined := strings.Join(ro, " ")
	for _, want := range []string{"--permission-mode dontAsk", "--add-dir /skills/x", "--allowedTools=mcp__ki-kb__search", "--allowedTools=mcp__ki-vault__vault_read"} {
		if !strings.Contains(joined, want) {
			t.Errorf("%q missing from %v", want, ro)
		}
	}
	if !strings.Contains(joined, "--disallowedTools=Edit,MultiEdit,Write,NotebookEdit,WebFetch,WebSearch,mcp__ki-vault__vault_write,Bash(git push:*)") {
		t.Errorf("writes not denied: %v", ro)
	}
	if strings.Contains(joined, "Bash(gh") {
		t.Errorf("a read-only run allows a shell command: %v", ro)
	}

	rw := strings.Join(SkillClaudeArgs([]string{"git rebase", "git push --force-with-lease", "Edit"}, nil, "/bin/ki", nil), " ")
	for _, want := range []string{"--allowedTools=Bash(git rebase:*)", "--allowedTools=Bash(git push --force-with-lease:*)", "--allowedTools=Edit"} {
		if !strings.Contains(rw, want) {
			t.Errorf("%q missing from %s", want, rw)
		}
	}
	if strings.Contains(rw, "Bash(git push:*)") || strings.Contains(rw, "disallowedTools=Edit,") {
		t.Errorf("an allowed tool is still denied: %s", rw)
	}
}

func TestSkillUnsupported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		agent  string
		tools  []string
		agents []string
		ok     bool
	}{
		{"claude", []string{"gh pr view"}, nil, true},
		{"/usr/local/bin/claude", nil, []string{"claude"}, true},
		{"codex", nil, nil, true},
		{"cursor-agent", nil, []string{"cursor"}, true},
		{"codex", []string{"gh pr view"}, nil, false},
		{"agent", []string{"gh pr view"}, nil, false},
		{"codex", nil, []string{"claude"}, false},
		{"agy", nil, nil, false},
	} {
		if why := SkillUnsupported(tc.agent, tc.tools, tc.agents); (why == "") != tc.ok {
			t.Errorf("SkillUnsupported(%s, %v, %v) = %q, want ok=%v", tc.agent, tc.tools, tc.agents, why, tc.ok)
		}
	}
}

func TestSkillBrief(t *testing.T) {
	t.Parallel()
	s, err := ParseSkill([]byte(ptalSkill))
	if err != nil {
		t.Fatal(err)
	}
	s.Dir = "/skills/ptal"
	b := SkillBrief(s, []string{"#12", "#13"}, SkillWorkspace{Repo: "acme/app"})
	for _, want := range []string{
		"Post a PTAL for #12 #13 in the channel.", "only these tools: gh pr view; mcp__claude_ai_Slack__slack_send_message",
		"empty scratch folder", "The repository is acme/app", "/skills/ptal", "BLOCKED: <the one question",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("%q missing from the brief:\n%s", want, b)
		}
	}

	ro := &Skill{Name: "x", Body: "Summarize.", Dir: "/s"}
	b = SkillBrief(ro, []string{"main"}, SkillWorkspace{Checkout: "branch main", SHA: "abc", Branch: "main"})
	for _, want := range []string{"Arguments: main\n\nSummarize.", "read-only", "throwaway worktree of branch main, detached at abc; its branch on origin is main"} {
		if !strings.Contains(b, want) {
			t.Errorf("%q missing from the brief:\n%s", want, b)
		}
	}
}

func TestParseSkillStatus(t *testing.T) {
	t.Parallel()
	for last, want := range map[string][2]string{
		"":                                 {JobFailed, "no final message"},
		"Posted.\n\nDONE: posted the PTAL": {JobDone, "posted the PTAL"},
		"BLOCKED: which channel?":          {JobBlocked, "which channel?"},
		"FAILED: gh is not logged in":      {JobFailed, "gh is not logged in"},
		"## Review\n\nLGTM, ready.":        {JobDone, "LGTM, ready."},
	} {
		status, detail := ParseSkillStatus(last)
		if status != want[0] || detail != want[1] {
			t.Errorf("ParseSkillStatus(%q) = %s %q, want %v", last, status, detail, want)
		}
	}
}

func TestFindAndLoadSkill(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	for _, d := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(d, "demo"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(second, "demo", SkillFile), []byte("Do it."), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := FindSkill("demo", []string{first, second})
	if err != nil || path != filepath.Join(second, "demo", SkillFile) {
		t.Fatalf("got %s, %v", path, err)
	}
	s, err := LoadSkill(path)
	if err != nil || s.Name != "demo" || s.Dir != filepath.Join(second, "demo") {
		t.Fatalf("got %+v, %v", s, err)
	}
	if byDir, err := FindSkill(filepath.Join(second, "demo"), nil); err != nil || byDir != path {
		t.Fatalf("by folder: %s, %v", byDir, err)
	}
	for _, bad := range []string{"missing", "../etc", "Demo"} {
		if _, err := FindSkill(bad, []string{first, second}); err == nil {
			t.Errorf("FindSkill(%q) found something", bad)
		}
	}
	if _, err := FindSkill(filepath.Join(first, "nope.md"), nil); err == nil {
		t.Error("a missing path was found")
	}
	if _, err := LoadSkill(filepath.Join(first, "nope", SkillFile)); err == nil {
		t.Error("a missing file loaded")
	}
}

func TestShippedRebaseSkillRunsHeadlessWithoutPushing(t *testing.T) {
	t.Parallel()
	s, err := LoadSkill(filepath.Join("..", "..", "skills", "ki-rebase", SkillFile))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "ki-rebase" || s.Role != ClassExecution || !slices.Equal(s.Agents, []string{"claude"}) {
		t.Fatalf("skill %+v", s)
	}
	args := strings.Join(SkillClaudeArgs(s.Tools, []string{s.Dir}, "/bin/ki", nil), " ")
	for _, want := range []string{
		"--allowedTools=Edit", "--allowedTools=Bash(git rebase:*)", "--allowedTools=Bash(git -c core.editor=true rebase --continue:*)",
		"--allowedTools=Bash(git cherry:*)", "Bash(git push:*)",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if strings.Contains(args, "--disallowedTools=Edit") || strings.Contains(args, ",Edit,") {
		t.Errorf("Edit is denied though the skill lists it: %s", args)
	}
	if err := CheckSkillTool("git push --force-with-lease", true); err != nil {
		t.Errorf("the caller's push rule: %v", err)
	}
}

func TestShippedAddressSkillReadsAndEditsButNeverPushesOrQueriesThreads(t *testing.T) {
	t.Parallel()
	s, err := LoadSkill(filepath.Join("..", "..", "skills", "ki-address", SkillFile))
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "ki-address" || s.Role != ClassExecution || !slices.Equal(s.Agents, []string{"claude"}) {
		t.Fatalf("skill %+v", s)
	}
	for _, want := range []string{"Edit", "git add", "git commit", "git reset --hard", "go test", "golangci-lint", "make"} {
		if !slices.Contains(s.Tools, want) {
			t.Errorf("tools lack %q: %v", want, s.Tools)
		}
	}
	for _, tool := range s.Tools {
		if strings.HasPrefix(tool, "gh api") || strings.HasPrefix(tool, "git push") {
			t.Errorf("tools grant %q, which only the caller may", tool)
		}
	}
	args := strings.Join(SkillClaudeArgs(s.Tools, []string{s.Dir}, "/bin/ki", nil), " ")
	for _, want := range []string{"--allowedTools=Edit", "--allowedTools=Bash(git commit:*)", "Bash(git push:*)"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	for _, rule := range []string{"git push --force-with-lease", "gh api graphql"} {
		if err := CheckSkillTool(rule, true); err != nil {
			t.Errorf("the caller's %q rule: %v", rule, err)
		}
	}
}

func TestShippedReviewSkillEndsWithAVerdictLine(t *testing.T) {
	t.Parallel()
	s, err := LoadSkill(filepath.Join("..", "..", "skills", "ki-review", SkillFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"`VERDICT: APPROVE`", "`VERDICT: COMMENT`", "`VERDICT: CHANGES`",
		"^VERDICT: (APPROVE|COMMENT|CHANGES)[ \\t\\r]*$", "machine-read contract", "nothing but the run's status line may follow",
	} {
		if !strings.Contains(s.Body, want) {
			t.Errorf("skill body lacks %q", want)
		}
	}
}

func TestShippedReviewSkillPrintsReviewJSONBeforeTheVerdict(t *testing.T) {
	t.Parallel()
	s, err := LoadSkill(filepath.Join("..", "..", "skills", "ki-review", SkillFile))
	if err != nil {
		t.Fatal(err)
	}
	review := strings.Index(s.Body, "`REVIEW-JSON: {")
	verdict := strings.Index(s.Body, "- **Verdict line**")
	if review < 0 || verdict < 0 || review > verdict {
		t.Fatalf("REVIEW-JSON at %d, verdict line at %d: want the first before the second", review, verdict)
	}
	for _, want := range []string{"RIGHT-side line", "JSON-escaped on one line", "no other line starts with `REVIEW-JSON:`"} {
		if !strings.Contains(s.Body, want) {
			t.Errorf("skill body lacks %q", want)
		}
	}
}
