package ki

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// The guard runs as a Claude Code PreToolUse hook for Bash in a skill run. A
// prefix rule such as Bash(git rebase:*) checks where a command starts, not
// the options after it, so the guard reads the whole command and refuses git
// invocations that run another program, name an external helper or write a
// file, and anything it cannot read with confidence.

// skillGuardCommand is the ki subcommand the hook runs.
const skillGuardCommand = "skill guard"

// skillGuardHook is the hook's reply for a refused command.
type skillGuardHook struct {
	Specific struct {
		Event    string `json:"hookEventName"`
		Decision string `json:"permissionDecision"`
		Reason   string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// SkillGuardSettings is the --settings JSON of a skill run: ClaudeSettings
// plus the hook that registers the guard and, unless sub is nil, the subagent
// guard, in one object because Claude Code keeps only the last --settings it
// is given. The hook command fails closed: a guard that cannot start blocks
// the command, where Claude Code would otherwise let it through.
func SkillGuardSettings(kiBinary string, sub *SubagentGuard) string {
	bash := settingsHook{Matcher: "Bash", Command: shellQuote(kiBinary) + " " + skillGuardCommand + " || exit 2"}
	return claudeSettingsWith(append([]settingsHook{bash}, sub.hooks()...)...)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RunSkillGuard answers one PreToolUse hook call: it reads the hook's JSON from
// in and writes a deny reply to out for a command the skill run may not run,
// nothing for one the allow-list alone decides. Input it cannot read is denied.
func RunSkillGuard(in io.Reader, out io.Writer) error {
	reason := guardHookInput(in)
	if reason == "" {
		return nil
	}
	return writeDeny(out, "ki skill run", reason)
}

// writeDeny writes the hook reply that refuses a tool call, the reason
// prefixed with the command that refused it.
func writeDeny(out io.Writer, source, reason string) error {
	var h skillGuardHook
	h.Specific.Event, h.Specific.Decision, h.Specific.Reason = "PreToolUse", "deny", source+": "+reason
	if err := json.NewEncoder(out).Encode(h); err != nil {
		return fmt.Errorf("writing the hook reply: %w", err)
	}
	return nil
}

func guardHookInput(in io.Reader) string {
	var call struct {
		Tool  string `json:"tool_name"`
		Input struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(io.LimitReader(in, 1<<20)).Decode(&call); err != nil {
		return "the hook input is not readable, so the command cannot be checked"
	}
	if call.Tool != "Bash" {
		return ""
	}
	return GuardBash(call.Input.Command)
}

// GuardBash says why a shell command may not run in a skill run, "" when it
// may. Every part of a compound command is checked.
func GuardBash(command string) string {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return "the command does not parse as shell, so it cannot be checked"
	}
	reason := ""
	syntax.Walk(file, func(n syntax.Node) bool {
		if reason != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.CmdSubst:
			reason = "command substitution cannot be checked; write the command out"
		case *syntax.ProcSubst:
			reason = "process substitution cannot be checked; write the command out"
		case *syntax.FuncDecl:
			reason = "a shell function cannot be checked; write the commands out"
		case *syntax.Stmt:
			// Redirects sit on the statement, so a group or subshell is checked as well as a plain command.
			reason = guardRedirects(n.Redirs)
			if call, ok := n.Cmd.(*syntax.CallExpr); ok && reason == "" {
				reason = guardCall(call, n.Redirs)
			}
		}
		return reason == ""
	})
	return reason
}

// launchers run another command from their arguments or change what a name
// means, so what they start is out of the guard's sight.
var launchers = []string{"eval", "exec", "source", ".", "env", "command", "builtin", "xargs", "nohup", "nice", "timeout", "sudo", "doas", "alias"}

var shells = []string{"sh", "bash", "zsh", "dash", "ksh", "ash", "fish"}

func guardCall(call *syntax.CallExpr, redirs []*syntax.Redirect) string {
	if len(call.Args) == 0 {
		return ""
	}
	name, ok := literalWord(call.Args[0])
	if !ok {
		return "a command name built at run time cannot be checked"
	}
	base := path.Base(name)
	switch {
	case slices.Contains(launchers, base):
		return base + " starts commands the guard cannot check"
	case slices.Contains(shells, base):
		return guardShell(call, redirs)
	case base == "git":
		return guardGitCall(call)
	case strings.HasPrefix(base, "git-"):
		return "run " + base + " as git " + strings.TrimPrefix(base, "git-")
	}
	return ""
}

// guardShell refuses a shell that would run a script given inline or on
// stdin; a shell given a file (bash -n script.sh) stays allowed.
func guardShell(call *syntax.CallExpr, redirs []*syntax.Redirect) string {
	for _, r := range redirs {
		if r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc || r.Op == syntax.WordHdoc {
			return "a script fed to a shell on stdin cannot be checked"
		}
	}
	files := 0
	for _, w := range call.Args[1:] {
		a, ok := literalWord(w)
		switch {
		case !ok:
			return "a shell argument built at run time cannot be checked"
		case strings.HasPrefix(a, "--"), a == "-":
		case strings.HasPrefix(a, "-"):
			if strings.ContainsAny(a[1:], "cs") {
				return "a shell script given inline or on stdin cannot be checked"
			}
		default:
			files++
		}
	}
	if files == 0 {
		return "a shell with no script file reads commands from stdin, which cannot be checked"
	}
	return ""
}

func guardGitCall(call *syntax.CallExpr) string {
	if len(call.Assigns) > 0 {
		return "variables set in front of git cannot be checked"
	}
	args := make([]string, 0, len(call.Args)-1)
	for _, w := range call.Args[1:] {
		a, ok := literalWord(w)
		if !ok {
			return "a git argument built at run time cannot be checked"
		}
		if expandsAtRunTime(w) {
			return "an unquoted wildcard or brace list in a git argument cannot be checked; quote it"
		}
		args = append(args, a)
	}
	return gitDenied(args)
}

// guardRedirects refuses output sent to a file by any command: an allowed
// read-only command would otherwise write wherever the run can reach, past the
// Edit and Write tools the skill was denied. /dev/null and descriptor
// duplication such as 2>&1 are fine.
func guardRedirects(redirs []*syntax.Redirect) string {
	for _, r := range redirs {
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.RdrClob, syntax.RdrInOut, syntax.DplOut:
		default:
			continue
		}
		target, ok := literalWord(r.Word)
		if ok && (target == "/dev/null" || (r.Op == syntax.DplOut && isFD(target))) {
			continue
		}
		return "output redirected to a file is not allowed"
	}
	return ""
}

func isFD(s string) bool {
	s = strings.TrimSuffix(s, "-")
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// literalWord is a word's value when every part of it is fixed text.
func literalWord(w *syntax.Word) (string, bool) {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if _, ok := q.(*syntax.Lit); !ok {
					return "", false
				}
			}
		default:
			return "", false
		}
	}
	s, err := expand.Literal(&expand.Config{}, w)
	return s, err == nil
}

// expandsAtRunTime reports whether the shell would turn an unquoted wildcard
// or brace list in the word into other words.
func expandsAtRunTime(w *syntax.Word) bool {
	var unquoted strings.Builder
	for _, p := range w.Parts {
		if l, ok := p.(*syntax.Lit); ok {
			unquoted.WriteString(l.Value)
		}
	}
	u := unquoted.String()
	return strings.ContainsAny(u, "*?[") || braceList.MatchString(u)
}

// braceList is {a,b} or {1..3}; @{upstream} and HEAD@{1} are not.
var braceList = regexp.MustCompile(`\{[^{}]*(?:,|\.\.)[^{}]*\}`)

// gitSafeOptions are git options that are safe though git would also read them
// as abbreviations of a refused one.
var gitSafeOptions = []string{"text"}

// gitRefusedLong are long options, of any subcommand, that run a program, name
// an external helper or write a file. git accepts any unambiguous prefix of a
// long option, so a name that starts one of these is refused too.
var gitRefusedLong = []struct{ name, why string }{
	{"exec", "runs another program"},
	{"exec-path", "runs programs from another folder"},
	{"config", "sets git configuration"},
	{"config-env", "sets git configuration"},
	{"upload-pack", "names another program for the remote side"},
	{"receive-pack", "names another program for the remote side"},
	{"ext-diff", "runs an external diff program"},
	{"textconv", "runs a text conversion program"},
	{"open-files-in-pager", "runs a pager program"},
	{"output", "writes a file"},
	{"output-directory", "writes files"},
}

// gitShort describes the short options of a subcommand that matter here:
// refused ones, and ones that take the rest of the cluster as their value.
type gitShort struct {
	refused, value string
	why            map[byte]string
	strategy       bool
}

var gitShorts = map[string]gitShort{
	"rebase":       {refused: "x", value: "XCS", why: map[byte]string{'x': "runs another program"}, strategy: true},
	"merge":        {value: "XS", strategy: true},
	"pull":         {value: "XS", strategy: true},
	"grep":         {refused: "O", value: "efABCm", why: map[byte]string{'O': "runs a pager program"}},
	"clone":        {refused: "uc", why: map[byte]string{'u': "names another program for the remote side", 'c': "sets git configuration"}},
	"archive":      {refused: "o", why: map[byte]string{'o': "writes a file"}},
	"format-patch": {refused: "o", why: map[byte]string{'o': "writes files"}},
}

// gitStrategies are the merge strategies built into git; any other name makes
// git run a git-merge-<name> program.
const strategyOption = "strategy"

var gitStrategies = []string{"ort", "recursive", "resolve", "octopus", "ours", "subtree"}

var gitRemoteCommands = []string{"fetch", "pull", "push", "clone", "ls-remote", "remote", "submodule"}

var (
	gitGlobalFlags  = []string{"--no-pager", "-P", "--bare", "--no-replace-objects", "--literal-pathspecs", "--glob-pathspecs", "--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks", "--no-lazy-fetch", "--version", "--help", "-h", "-v", "--html-path", "--man-path", "--info-path"}
	gitGlobalValued = []string{"-C", "--git-dir", "--work-tree", "--namespace", "--attr-source", "--super-prefix"}
)

// gitConfigAllowed is the one -c override a skill may use: the editor of a
// rebase that continues after a conflict, set to a no-op so nothing opens.
const gitConfigAllowed = "core.editor=true"

// gitDenied says why one git invocation, given as the words after "git", may
// not run in a skill run, "" when it may.
func gitDenied(args []string) string {
	sub, rest, why := gitSubcommand(args)
	if why != "" {
		return why
	}
	if slices.Contains(gitRemoteCommands, sub) {
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") && strings.Contains(a, "::") {
				return fmt.Sprintf("git %s with %q names a remote helper program", sub, a)
			}
		}
	}
	short, hasShort := gitShorts[sub]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--":
			return ""
		case strings.HasPrefix(a, "--"):
			name, val, hasVal := strings.Cut(a[2:], "=")
			if why := refusedLong(sub, name); why != "" {
				return why
			}
			if hasShort && short.strategy && name != "" && strings.HasPrefix(strategyOption, name) {
				if !hasVal && i+1 < len(rest) {
					val = rest[i+1]
				}
				if why := strategyDenied(sub, "--strategy", val); why != "" {
					return why
				}
			}
		case len(a) > 1 && a[0] == '-' && hasShort:
			if why := shortDenied(sub, short, a[1:], rest[i+1:]); why != "" {
				return why
			}
		}
	}
	return ""
}

// gitSubcommand reads the options before the subcommand and returns the
// subcommand with the words after it.
func gitSubcommand(args []string) (sub string, rest []string, why string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, hasVal := strings.Cut(a, "=")
		switch {
		case !strings.HasPrefix(a, "-") || a == "-":
			return a, args[i+1:], ""
		case a == "-c":
			if i+1 >= len(args) || args[i+1] != gitConfigAllowed {
				return "", nil, "git -c sets git configuration; only -c " + gitConfigAllowed + " is allowed"
			}
			i++
		case strings.HasPrefix(a, "-c") && !strings.HasPrefix(a, "--"):
			return "", nil, "git " + a + " sets git configuration; only -c " + gitConfigAllowed + " is allowed"
		case name == "--config-env":
			return "", nil, "git --config-env sets git configuration"
		case name == "--exec-path":
			return "", nil, "git --exec-path runs programs from another folder"
		case a == "-p" || a == "--paginate":
			return "", nil, "git " + a + " runs a pager program"
		case slices.Contains(gitGlobalFlags, a):
		case slices.Contains(gitGlobalValued, a):
			i++
		case hasVal && slices.Contains(gitGlobalValued, name):
		default:
			return "", nil, "git option " + a + " before the subcommand is not one the guard knows"
		}
	}
	return "", nil, ""
}

func refusedLong(sub, name string) string {
	if name == "" || slices.Contains(gitSafeOptions, name) {
		return ""
	}
	for _, r := range gitRefusedLong {
		if strings.HasPrefix(r.name, name) {
			return fmt.Sprintf("git %s --%s %s", sub, name, r.why)
		}
	}
	return ""
}

// shortDenied checks one cluster of short options (the text after the dash)
// and the words after it, which hold the value of a trailing -s.
func shortDenied(sub string, s gitShort, cluster string, next []string) string {
	for i := 0; i < len(cluster); i++ {
		c := cluster[i]
		switch {
		case strings.IndexByte(s.refused, c) >= 0:
			return fmt.Sprintf("git %s -%c %s", sub, c, s.why[c])
		case c == 's' && s.strategy:
			val := cluster[i+1:]
			if val == "" && len(next) > 0 {
				val = next[0]
			}
			return strategyDenied(sub, "-s", val)
		case strings.IndexByte(s.value, c) >= 0:
			return ""
		}
	}
	return ""
}

func strategyDenied(sub, opt, val string) string {
	if slices.Contains(gitStrategies, val) {
		return ""
	}
	return fmt.Sprintf("git %s %s %q runs a program of that name; use one of %s", sub, opt, val, strings.Join(gitStrategies, ", "))
}
