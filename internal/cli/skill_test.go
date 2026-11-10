package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

const readOnlySkill = "---\nname: summary\ndescription: Summarize.\n---\n\nSummarize $ARGUMENTS.\n"

const writingSkill = `---
name: ptal
description: Post a PTAL.
ki-role: writing
ki-agents: [claude]
ki-tools:
  - gh pr view
  - mcp__claude_ai_Slack__slack_send_message
---

Post a PTAL for $ARGUMENTS.
`

func (d *delegateEnv) addSkill(t *testing.T, name, body string) {
	t.Helper()
	write(t, filepath.Join(d.harness, "skills", name, ki.SkillFile), body)
}

func (d *delegateEnv) skillRun(args ...string) error {
	d.out.Reset()
	return d.app.Run(context.Background(), append([]string{"skill", "run"}, args...))
}

func (d *delegateEnv) skillResult(t *testing.T) skillOutput {
	t.Helper()
	var r skillOutput
	if err := json.Unmarshal(d.out.Bytes(), &r); err != nil {
		t.Fatalf("not a skill result: %v\n%s", err, d.out.String())
	}
	return r
}

func TestSkillRunWithoutToolsIsReadOnlyInAScratchFolder(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nmodels:\n  execution: {claude: sonnet}\n")
	d.addSkill(t, "summary", readOnlySkill)
	d.worker["RESULT"] = claudeAnswer("Three changes.\n\nDONE: summarized main")
	if err := d.skillRun("summary", "main", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.skillResult(t)
	if r.Status != ki.JobDone || r.Skill != "summary" || r.Agent != "claude" || r.Model != "sonnet" || r.Detail != "summarized main" ||
		r.Output != "Three changes.\n\nDONE: summarized main" || r.Tokens.Output != 9 || r.CostUSD != 0.01 {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	for _, want := range []string{"--permission-mode", "dontAsk", "--allowedTools=mcp__ki-kb__search", "--add-dir", "--model", "sonnet", "--append-system-prompt-file"} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--allowedTools=Bash") || a == "auto" || a == "--strict-mcp-config" {
			t.Errorf("a read-only skill got %q: %v", a, args)
		}
		if strings.HasPrefix(a, "--disallowedTools=") && (!strings.Contains(a, "Write") || !strings.Contains(a, "Bash(git push:*)")) {
			t.Errorf("writes not denied: %s", a)
		}
	}
	if task := args[len(args)-1]; !strings.HasPrefix(task, "Summarize main.") || !strings.Contains(task, "You are read-only") {
		t.Errorf("brief %q", task)
	}
	jobDir := filepath.Join(d.jobsDir(), r.ID)
	if cwd := d.cwd(t); filepath.Base(cwd) != "scratch" || !strings.HasSuffix(filepath.Dir(cwd), r.ID) {
		t.Errorf("ran in %s, want the job's scratch folder", cwd)
	}
	for _, gone := range []string{"scratch", "mcp.json"} {
		if _, err := os.Stat(filepath.Join(jobDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s left behind: %v", gone, err)
		}
	}
	jobs := d.ledger(t)
	if len(jobs) != 1 || jobs[0].Kind != ki.KindSkill || jobs[0].Title != "summary main" || jobs[0].Detail != "summarized main" || jobs[0].Status != ki.JobDone {
		t.Fatalf("ledger %+v", jobs)
	}

	d.out.Reset()
	if err := d.app.Run(context.Background(), []string{"delegate", "ls", "--json"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(d.out.String(), r.ID) {
		t.Errorf("ki delegate ls shows a skill run without --all: %s", d.out.String())
	}
	d.out.Reset()
	if err := d.app.Run(context.Background(), []string{"delegate", "ls", "--all", "--json"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.out.String(), r.ID) {
		t.Errorf("ki delegate ls --all leaves the skill run out: %s", d.out.String())
	}
}

func TestSkillRunTakesItsRoleAndToolsAndSkipsAgentsThatCannotHoldThem(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\nroles:\n  writing:\n    providers: [codex, claude:opus]\n  execution:\n    providers: [codex]\n")
	d.addSkill(t, "ptal", writingSkill)
	d.worker["RESULT"] = claudeAnswer("DONE: posted")
	if err := d.skillRun("ptal", "#12", "--allow", "git push --force-with-lease", "--json"); err != nil {
		t.Fatal(err)
	}
	r := d.skillResult(t)
	if r.Agent != "claude" || r.Model != "opus" || r.Role != ki.ClassWriting || r.Status != ki.JobDone {
		t.Fatalf("result %+v", r)
	}
	args := d.args(t)
	for _, want := range []string{
		"--allowedTools=Bash(gh pr view:*)", "--allowedTools=mcp__claude_ai_Slack__slack_send_message",
		"--allowedTools=Bash(git push --force-with-lease:*)",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("argument %q missing from %v", want, args)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--disallowedTools=") && strings.Contains(a, "git push") {
			t.Errorf("git push denied though the caller allowed it: %s", a)
		}
	}
	i := slices.Index(args, "--settings")
	if i < 0 || !strings.Contains(args[i+1], "skill guard || exit 2") || !strings.Contains(args[i+1], `"attribution"`) {
		t.Errorf("the run does not register the guard hook next to ki's own settings: %v", args)
	}
	if !strings.Contains(args[i+1], "session guard --worker --allow 'Explore' || exit 2") || !strings.Contains(args[i+1], `"matcher":"Agent|Task"`) {
		t.Errorf("the run does not register the subagent guard in the same settings: %v", args)
	}
	if slices.Contains(args[i+1:], "--settings") {
		t.Errorf("a second --settings replaces the first: %v", args)
	}
	if stderr := d.app.Stderr.(interface{ String() string }).String(); !strings.Contains(stderr, "codex skipped for ptal") {
		t.Errorf("no note about codex: %s", stderr)
	}
	if !strings.Contains(args[len(args)-1], "only these tools: gh pr view; mcp__claude_ai_Slack__slack_send_message; git push --force-with-lease") {
		t.Errorf("brief does not name the tools: %s", args[len(args)-1])
	}
}

func TestSkillRunReadOnlyOnCodex(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	d.worker["LAST"] = "Fine.\nDONE: read it"
	d.worker["RESULT"] = codexEvents
	if err := d.skillRun("summary", "--agent", "codex", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	args := d.args(t)
	if !slices.Contains(args, "read-only") || !slices.Contains(args, "--skip-git-repo-check") {
		t.Errorf("codex not read-only: %v", args)
	}
	if strings.Contains(strings.Join(args, " "), askToken) {
		t.Errorf("the token is in codex's arguments")
	}
}

func TestSkillRunRefusals(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "ptal", writingSkill)
	d.addSkill(t, "pusher", "---\nki-tools: [git push]\n---\nPush.\n")
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"no skill":         {nil, "usage: ki skill run"},
		"unknown flag":     {[]string{"ptal", "--post"}, "arguments for the skill go after --"},
		"missing":          {[]string{"nope"}, `no skill "nope"`},
		"role and agent":   {[]string{"ptal", "--role", "writing", "--agent", "claude"}, "exclusive"},
		"model alone":      {[]string{"ptal", "--model", "opus"}, "--model goes with --agent"},
		"allow every":      {[]string{"ptal", "--allow", "Bash(*)"}, "grants every shell command"},
		"agent not listed": {[]string{"ptal", "--agent", "codex"}, "ki-agents does not list it"},
		"skill pushes":     {[]string{"pusher"}, "only the caller grants"},
		"checkout no repo": {[]string{"ptal", "--checkout", "main", "--repo", "/"}, "not a git checkout"},
		"bad timeout":      {[]string{"ptal", "--timeout", "soon"}, "--timeout"},
		"value missing":    {[]string{"ptal", "--allow"}, "--allow needs a value"},
	} {
		t.Run(name, func(t *testing.T) {
			err := d.skillRun(tc.args...)
			if !isUserError(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := os.Stat(d.argsFile); !os.IsNotExist(err) {
		t.Error("an agent ran for a refused skill")
	}
}

func TestSkillRunPassesFlagsAfterDashDashAndFailsWithoutAnEnd(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	d.worker["RESULT"] = claudeAnswer("FAILED: nothing to read")
	err := d.skillRun("summary", "--quiet", "--", "12", "--post")
	if !isUserError(err) || !strings.Contains(err.Error(), "summary FAILED: nothing to read") {
		t.Fatalf("got %v", err)
	}
	if task := d.args(t)[len(d.args(t))-1]; !strings.HasPrefix(task, "Summarize 12 --post.") {
		t.Errorf("brief %q", task)
	}
	if d.out.String() != "FAILED: nothing to read\n" {
		t.Errorf("stdout %q", d.out.String())
	}
}

func TestSkillRunChecksOutABranchInAThrowawayWorktree(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, d.repo, "init", "-q", "--bare", origin)
	gitIn(t, d.repo, "remote", "add", "origin", origin)
	gitIn(t, d.repo, "push", "-q", "origin", "HEAD:refs/heads/feature")
	head := git(context.Background(), d.repo, "rev-parse", "HEAD")
	d.worker["RESULT"] = claudeAnswer("DONE: read the branch")
	if err := d.skillRun("summary", "--checkout", "feature", "--repo", d.repo, "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	r := d.skillResult(t)
	want := filepath.Join(filepath.Dir(d.repo), filepath.Base(d.repo)+"-worktrees", r.ID)
	if cwd := d.cwd(t); mustEval(t, filepath.Dir(cwd)) != mustEval(t, filepath.Dir(want)) || filepath.Base(cwd) != r.ID {
		t.Errorf("ran in %s, want %s", cwd, want)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Errorf("worktree left behind: %v", err)
	}
	if refs := git(context.Background(), d.repo, "for-each-ref", "refs/ki-skill/"); refs != "" {
		t.Errorf("fetch ref left behind: %s", refs)
	}
	if task := d.args(t)[len(d.args(t))-1]; !strings.Contains(task, "throwaway worktree of branch feature, detached at "+head) {
		t.Errorf("brief %q", task)
	}
	if jobs := d.ledger(t); jobs[0].Branch != "feature" || mustEval(t, jobs[0].Repo) != mustEval(t, d.repo) {
		t.Errorf("ledger %+v", jobs[0])
	}

	err := d.skillRun("summary", "--checkout", "nope", "--repo", d.repo, "--quiet")
	if !isUserError(err) || !strings.Contains(err.Error(), "fetching branch nope") {
		t.Fatalf("got %v", err)
	}
	if err := d.skillRun("summary", "--checkout", "a..b", "--repo", d.repo); !isUserError(err) || !strings.Contains(err.Error(), "pull request number or a branch") {
		t.Fatalf("got %v", err)
	}
}

func TestSkillHelpAndUnknownCommand(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	if err := d.app.Run(context.Background(), []string{"skill", "help"}); err != nil || !strings.Contains(d.out.String(), "ki skill run <skill>") {
		t.Fatalf("help: %v %s", err, d.out.String())
	}
	if err := d.app.Run(context.Background(), []string{"skill", "ls"}); !isUserError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestPullNumber(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]int{"12": 12, "#7": 7, "main": 0, "0": 0, "-3": 0} {
		if n, ok := pullNumber(in); n != want && ok {
			t.Errorf("pullNumber(%q) = %d", in, n)
		} else if ok != (want > 0) {
			t.Errorf("pullNumber(%q) ok = %v", in, ok)
		}
	}
}

func TestSkillRunChecksOutAPullRequestHead(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, d.repo, "init", "-q", "--bare", origin)
	gitIn(t, d.repo, "remote", "add", "origin", origin)
	gitIn(t, d.repo, "push", "-q", "origin", "HEAD:refs/pull/5/head")
	d.worker["RESULT"] = claudeAnswer("DONE: reviewed")
	if err := d.skillRun("summary", "--checkout", "#5", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if task := d.args(t)[len(d.args(t))-1]; !strings.Contains(task, "throwaway worktree of pull request #5, detached at") || strings.Contains(task, "its branch on origin") {
		t.Errorf("brief %q", task)
	}
}

func TestSkillRunReadOnlyOnCursorWritesItsWorkspaceConfig(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	d.addSkill(t, "summary", readOnlySkill)
	cursorCopy := filepath.Join(t.TempDir(), "cursor.json")
	d.worker["COPY_CURSOR_MCP"] = cursorCopy
	d.worker["RESULT"] = cursorAnswer("DONE: read")
	if err := d.skillRun("summary", "--agent", "agent", "--json", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if args := d.args(t); !slices.Contains(args, "ask") || !slices.Contains(args, "--sandbox") {
		t.Errorf("cursor not in ask mode: %v", args)
	}
	raw, err := os.ReadFile(cursorCopy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), askToken) || !strings.Contains(string(raw), "${env:KI_TOKEN}") {
		t.Errorf("cursor's MCP config: %s", raw)
	}
}

func TestSkillGuardReadsTheHookOnStdin(t *testing.T) {
	d := newAskEnv(t, "identities: [work]\n")
	for _, tc := range []struct{ in, want string }{
		{`{"tool_name":"Bash","tool_input":{"command":"git rebase origin/main"}}`, ""},
		{`{"tool_name":"Bash","tool_input":{"command":"git rebase --exec make origin/main"}}`, `"permissionDecision":"deny"`},
	} {
		d.out.Reset()
		d.app.Stdin = strings.NewReader(tc.in)
		if err := d.app.Run(context.Background(), []string{"skill", "guard"}); err != nil {
			t.Fatal(err)
		}
		if got := d.out.String(); (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%s: printed %q, want %q", tc.in, got, tc.want)
		}
	}
}
