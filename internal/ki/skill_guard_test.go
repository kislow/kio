package ki

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestGuardBashAllowed(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		"",
		"git status",
		"git fetch origin main feat/x",
		"git rebase origin/main",
		"git -c core.editor=true rebase --continue",
		"git rebase --continue --no-verify",
		"git rebase -s ort origin/main",
		"git rebase --strategy=recursive -X theirs origin/main",
		"git rebase -Xtheirs origin/main",
		"git add src/a.go && git -c core.editor=true rebase --continue",
		"git rm -- src/old.go",
		"git log --oneline HEAD..origin/main",
		"git log -p -n 3 origin/main -- src/a.go",
		"git log --output-indicator-new=+ origin/main",
		"git diff origin/main...HEAD --stat",
		"git diff --text --no-ext-diff --no-textconv HEAD",
		"git show REBASE_HEAD",
		"git cherry origin/main origin/feat",
		"git rev-parse --short HEAD",
		"git grep -n -e output -- '*.go'",
		"git grep -eOther",
		"git -C sub log -1",
		"git --no-pager log -1",
		"git log -1 2>&1",
		"git log -1 > /dev/null",
		"git log --grep='--output=x' -1",
		"git log -1 -- --output",
		"git log HEAD@{1} @{upstream}..HEAD",
		"git diff HEAD -- 'a::b'",
		"/usr/bin/git status",
		"git status | head -5",
		"git fetch origin && git rebase origin/main || git rebase --abort",
		"gh pr view 12 --json title",
		"gh pr view 12 2>&1 | head -5",
		"gh pr diff 12 > /dev/null",
		"gh pr view 12 < /dev/null",
		"bash -n scripts/a.sh",
	} {
		if why := GuardBash(cmd); why != "" {
			t.Errorf("GuardBash(%q) refused: %s", cmd, why)
		}
	}
}

func TestGuardBashDenied(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ cmd, want string }{
		// -c and --config-env
		{"git -c core.pager=less log", "-c sets git configuration"},
		{"git -c core.editor=true -c alias.x=y rebase --continue", "-c sets git configuration"},
		{"git -c core.editor=false rebase --continue", "-c sets git configuration"},
		{"git -c", "-c sets git configuration"},
		{"git -ccore.editor=true log", "sets git configuration"},
		{"git --config-env=core.editor=ED log", "--config-env"},
		{"git --config-env core.editor=ED log", "--config-env"},
		{"git --exec-path=/tmp log", "--exec-path"},
		{"git -p log", "pager"},
		{"git --paginate log", "pager"},
		{"git --unknown log", "not one the guard knows"},
		// programs and helpers
		{"git rebase --exec 'make test' origin/main", "--exec runs another program"},
		{"git rebase origin/main --exec=make", "--exec runs another program"},
		{"git rebase --exe=make origin/main", "--exe runs another program"},
		{"git rebase -x make origin/main", "-x runs another program"},
		{"git rebase -ix make origin/main", "-x runs another program"},
		{"git rebase origin/main -x make", "-x runs another program"},
		{"git rebase -s custom origin/main", "-s \"custom\""},
		{"git rebase -scustom origin/main", "-s \"custom\""},
		{"git rebase --strategy=custom origin/main", "--strategy"},
		{"git rebase --strategy custom origin/main", "--strategy"},
		{"git pull -s custom", "-s"},
		{"git fetch --upload-pack=prog origin", "--upload-pack"},
		{"git fetch --upload-pack prog origin", "--upload-pack"},
		{"git fetch origin --upload=prog", "--upload"},
		{"git pull --upload-pack=prog", "--upload-pack"},
		{"git push --receive-pack=prog origin", "--receive-pack"},
		{"git fetch 'ext::sh -c id' main", "remote helper"},
		{"git fetch origin transport::x", "remote helper"},
		{"git diff --ext-diff", "--ext-diff"},
		{"git log --ext-diff -p", "--ext-diff"},
		{"git show --textconv HEAD:f", "--textconv"},
		{"git diff HEAD --textconv -- f", "--textconv"},
		{"git grep -O pattern", "-O runs a pager"},
		{"git grep -nOless pattern", "-O runs a pager"},
		{"git grep --open-files-in-pager=less x", "--open-files-in-pager"},
		{"git clone -u prog url", "-u"},
		{"git clone -c a=b url", "-c"},
		// files
		{"git log --output=out.txt", "--output writes a file"},
		{"git log --output out.txt", "--output writes a file"},
		{"git diff HEAD --output=out.txt", "--output writes a file"},
		{"git show --out=f HEAD", "--out"},
		{"git archive -o x.tar HEAD", "-o writes a file"},
		{"git format-patch -o dir HEAD~1", "-o writes files"},
		{"git format-patch --output-directory dir HEAD~1", "--output-directory"},
		{"git log > out.txt", "redirected"},
		{"git log >> out.txt", "redirected"},
		{"git log &> out.txt", "redirected"},
		{"git log 1>out.txt", "redirected"},
		{"git log >&out.txt", "redirected"},
		{"git log --oneline | tee out.txt && git log > out.txt", "redirected"},
		{"gh pr view 12 > out.txt", "redirected"},
		{"gh pr diff 12 >> ~/.zshrc", "redirected"},
		{"gh pr view 12 &> out.txt", "redirected"},
		{"gh pr view 12 2> err.txt", "redirected"},
		{"{ gh pr view 12; } > out.txt", "redirected"},
		{"(gh pr view 12) >> out.txt", "redirected"},
		{"gh pr view 12 && gh pr diff 12 > out.txt", "redirected"},
		{"if true; then gh pr view 12; fi > out.txt", "redirected"},
		// compound commands, checked part by part
		{"git status && git log --output=x", "--output"},
		{"git status; git rebase --exec=x main", "--exec"},
		{"git status || git diff --ext-diff", "--ext-diff"},
		{"git log | git -c x=y diff", "-c"},
		{"(git log --output=x)", "--output"},
		{"if true; then git log --output=x; fi", "--output"},
		{"for b in a b; do git rebase -x make; done", "-x"},
		{"git status\ngit log --output=x", "--output"},
		{"git log -1 &\ngit log --output=x", "--output"},
		// cannot be read with confidence
		{"git log $(echo --output=x)", "run time"},
		{"git log `echo --output=x`", "run time"},
		{"echo $(git log --output=x)", "command substitution"},
		{"cat <(git log)", "process substitution"},
		{"git log $OPT", "built at run time"},
		{"git log \"$OPT\"", "built at run time"},
		{"git log --${OPT}=x", "built at run time"},
		{"git log --{output,exec}=x", "brace list"},
		{"git log *", "wildcard"},
		{"git log -- ?.go", "wildcard"},
		{"$G log", "command name"},
		{"FOO=bar git log", "variables set in front of git"},
		{"GIT_SSH_COMMAND=prog git fetch", "variables set in front of git"},
		{"eval 'git log'", "eval"},
		{"exec git log", "exec"},
		{"env git log", "env"},
		{"xargs git log", "xargs"},
		{"command git log", "command"},
		{"sh -c 'git log'", "inline"},
		{"bash -lc 'git log'", "inline"},
		{"bash <<EOF\ngit log\nEOF", "stdin"},
		{"bash <<< 'git log'", "stdin"},
		{"echo git log | bash", "no script file"},
		{"bash -s", "inline or on stdin"},
		{"git-rebase --exec x", "git rebase"},
		{"f() { git log; }; f", "function"},
		{"git log 'unterminated", "does not parse"},
		{"git log &&", "does not parse"},
	} {
		why := GuardBash(tc.cmd)
		if why == "" || !strings.Contains(why, tc.want) {
			t.Errorf("GuardBash(%q) = %q, want a reason with %q", tc.cmd, why, tc.want)
		}
	}
}

func TestRunSkillGuard(t *testing.T) {
	t.Parallel()
	hook := func(tool, cmd string) string {
		in, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": map[string]string{"command": cmd}})
		return string(in)
	}
	for _, tc := range []struct{ name, in, want string }{
		{"allowed", hook("Bash", "git rebase origin/main"), ""},
		{"other tool", hook("Read", "git log --output=x"), ""},
		{"denied", hook("Bash", "git log --output=x"), "--output writes a file"},
		{"bad json", "not json", "not readable"},
		{"empty", "", "not readable"},
	} {
		var out bytes.Buffer
		if err := RunSkillGuard(strings.NewReader(tc.in), &out); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.want == "" {
			if out.Len() != 0 {
				t.Errorf("%s: printed %s, want nothing", tc.name, out.String())
			}
			continue
		}
		var h skillGuardHook
		if err := json.Unmarshal(out.Bytes(), &h); err != nil {
			t.Fatalf("%s: %v: %s", tc.name, err, out.String())
		}
		if h.Specific.Event != "PreToolUse" || h.Specific.Decision != "deny" || !strings.Contains(h.Specific.Reason, tc.want) {
			t.Errorf("%s: reply %+v, want a deny with %q", tc.name, h, tc.want)
		}
	}
}

func TestSkillGuardSettings(t *testing.T) {
	t.Parallel()
	var s struct {
		Attribution map[string]string
		Hooks       struct {
			PreToolUse []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct{ Type, Command string }
			}
		}
	}
	if err := json.Unmarshal([]byte(SkillGuardSettings("/opt/my ki/ki'x", nil)), &s); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Attribution["commit"]; !ok {
		t.Errorf("ki's own settings are lost: %+v", s)
	}
	pre := s.Hooks.PreToolUse
	if len(pre) != 1 || pre[0].Matcher != "Bash" || len(pre[0].Hooks) != 1 {
		t.Fatalf("settings %+v", s)
	}
	h := pre[0].Hooks[0]
	if h.Type != "command" || h.Command != `'/opt/my ki/ki'\''x' skill guard || exit 2` {
		t.Errorf("hook %+v", h)
	}
}

func TestCheckSkillToolRefusesGitOptionsThatRunPrograms(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		entry  string
		caller bool
		ok     bool
	}{
		{"git -c core.editor=true rebase --continue", false, true},
		{"Bash(git -c core.editor=true rebase --continue:*)", false, true},
		{"git log --oneline", false, true},
		{"git -c core.pager=x log", false, false},
		{"git rebase --exec make", false, false},
		{"Bash(git log --output=x:*)", false, false},
		{"git fetch --upload-pack=prog", false, false},
		{"git diff --ext-diff", false, false},
		{"git -c alias.x=y status", true, false},
		{"git push --force-with-lease", true, true},
		{"gh pr view", false, true},
	} {
		if err := CheckSkillTool(tc.entry, tc.caller); (err == nil) != tc.ok {
			t.Errorf("CheckSkillTool(%q, caller=%v) = %v, want ok=%v", tc.entry, tc.caller, err, tc.ok)
		}
	}
}

func TestSkillClaudeArgsRegistersTheGuard(t *testing.T) {
	t.Parallel()
	for _, tools := range [][]string{nil, {"git rebase"}} {
		args := SkillClaudeArgs(tools, nil, "/bin/ki", nil)
		i := slices.Index(args, "--settings")
		if i < 0 || i+1 >= len(args) || args[i+1] != SkillGuardSettings("/bin/ki", nil) {
			t.Errorf("tools %v: no guard settings in %v", tools, args)
		}
	}
}
