package ki

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"
)

// KindSkill marks a ledger record of `ki skill run`: one skill, run once.
const KindSkill = "skill"

// SkillFile is the file that holds a skill, in its own folder.
const SkillFile = "SKILL.md"

// Skill is a SKILL.md as `ki skill run` reads it: the frontmatter fields ki
// acts on and the procedure after them.
type Skill struct {
	Name string
	// Dir is the folder the skill's own files are in.
	Dir string
	// Role is ki-role: the policy role that picks the agent, "" for the default.
	Role string
	// Tools is ki-tools: what the skill may do beyond reading. Empty is read-only.
	Tools []string
	// Agents is ki-agents: the only agents that can run it, empty for any.
	Agents []string
	Body   string
}

type skillFrontmatter struct {
	Name   string   `yaml:"name"`
	Role   string   `yaml:"ki-role"`
	Tools  []string `yaml:"ki-tools"`
	Agents []string `yaml:"ki-agents"`
}

var skillName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// FindSkill locates a skill: a path to a SKILL.md or its folder, else the
// first <dir>/<name>/SKILL.md among dirs.
func FindSkill(name string, dirs []string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) || strings.HasSuffix(name, ".md") {
		path := name
		if st, err := os.Stat(path); err == nil && st.IsDir() {
			path = filepath.Join(path, SkillFile)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("no skill at %s", path)
		}
		return path, nil
	}
	if !skillName.MatchString(name) {
		return "", fmt.Errorf("skill name %q: use the folder name, like ki-review, or a path", name)
	}
	for _, d := range dirs {
		path := filepath.Join(d, name, SkillFile)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no skill %q in %s", name, strings.Join(dirs, ", "))
}

// LoadSkill reads and checks a SKILL.md.
func LoadSkill(path string) (*Skill, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a skill the caller named
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	s, err := ParseSkill(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Dir = filepath.Dir(path)
	if s.Name == "" {
		s.Name = filepath.Base(s.Dir)
	}
	return s, nil
}

// ParseSkill splits a SKILL.md into its frontmatter and procedure and checks
// the ki fields, collecting every problem.
func ParseSkill(data []byte) (*Skill, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, err
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal(front, &fm); err != nil {
		return nil, fmt.Errorf("frontmatter: %w (ki-tools and ki-agents are lists)", err)
	}
	s := &Skill{Name: fm.Name, Role: fm.Role, Agents: fm.Agents, Body: strings.TrimSpace(body)}
	var errs []error
	if s.Role != "" && !slices.Contains(skillRoles, s.Role) {
		errs = append(errs, fmt.Errorf("ki-role %q: use one of %s", s.Role, strings.Join(skillRoles, ", ")))
	}
	for _, a := range s.Agents {
		if AgentFamily(a) == "" {
			errs = append(errs, fmt.Errorf("ki-agents: unknown agent %q (use claude, codex, cursor or agy)", a))
		}
	}
	for _, t := range fm.Tools {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if err := CheckSkillTool(t, false); err != nil {
			errs = append(errs, fmt.Errorf("ki-tools: %w", err))
			continue
		}
		s.Tools = append(s.Tools, t)
	}
	if s.Body == "" {
		errs = append(errs, errors.New("no procedure after the frontmatter"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return s, nil
}

var skillRoles = []string{ClassWriting, ClassExecution, ClassJudgement, ClassSearch}

func splitFrontmatter(data []byte) (front []byte, body string, err error) {
	data = bytes.TrimPrefix(data, []byte("\ufeff"))
	if !bytes.HasPrefix(data, []byte("---\n")) && !bytes.HasPrefix(data, []byte("---\r\n")) {
		return nil, string(data), nil
	}
	rest := data[bytes.IndexByte(data, '\n')+1:]
	for off := 0; off < len(rest); {
		end := bytes.IndexByte(rest[off:], '\n')
		line := rest[off:]
		if end >= 0 {
			line = rest[off : off+end]
		}
		if strings.TrimSpace(string(line)) == "---" {
			if end < 0 {
				return rest[:off], "", nil
			}
			return rest[:off], string(rest[off+end+1:]), nil
		}
		if end < 0 {
			break
		}
		off += end + 1
	}
	return nil, "", errors.New("frontmatter has no closing ---")
}

// AgentFamily is the agent as a skill's ki-agents names it: claude, codex,
// cursor or agy, "" for one ki does not know.
func AgentFamily(agent string) string {
	switch filepath.Base(agent) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "cursor", "cursor-agent", "agent":
		return "cursor"
	case "agy":
		return "agy"
	}
	return ""
}

// SkillUnsupported says why ki cannot run the skill on agent, "" when it can.
// Only Claude Code takes a tool allow-list ki can hold it to; Codex and
// Cursor run a read-only skill through the read-only setup of `ki ask`.
func SkillUnsupported(agent string, tools, agents []string) string {
	fam := AgentFamily(agent)
	if len(agents) > 0 && !slices.ContainsFunc(agents, func(a string) bool { return AgentFamily(a) == fam }) {
		return "the skill's ki-agents does not list it"
	}
	switch fam {
	case "claude":
		return ""
	case "codex", "cursor":
		if len(tools) > 0 {
			return "ki cannot hold it to a tool allow-list, only to read-only"
		}
		return ""
	}
	return "ki knows no way to limit its tools"
}

// toolRule is a Claude Code tool name, optionally with its specifier: Read,
// WebFetch(domain:example.com), Bash(gh pr view:*).
var toolRule = regexp.MustCompile(`^[A-Z][A-Za-z]*(?:\((.*)\))?$`)

// CheckSkillTool refuses an entry that would grant every shell command, one
// that contains a git option a skill run refuses (see GuardBash), and one that
// would let the agent push unless the caller passed it: a skill's own list
// never pushes.
func CheckSkillTool(entry string, fromCaller bool) error {
	cmd, shell := shellCommand(entry)
	if !shell {
		return nil
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 || strings.HasPrefix(fields[0], "*") {
		return fmt.Errorf("%q grants every shell command; name the commands", entry)
	}
	if fields[0] != "git" {
		return nil
	}
	if why := gitDenied(fields[1:]); why != "" {
		return fmt.Errorf("%q: %s; a skill run refuses it", entry, why)
	}
	if fromCaller {
		return nil
	}
	if len(fields) == 1 || strings.HasPrefix(fields[1], "*") || strings.HasPrefix(fields[1], "push") {
		return fmt.Errorf("%q can run git push, which only the caller grants: --allow \"git push --force-with-lease\"", entry)
	}
	return nil
}

// shellCommand is the command an entry allows, without a trailing wildcard,
// and whether the entry is a shell command at all.
func shellCommand(entry string) (string, bool) {
	if strings.HasPrefix(entry, "mcp__") {
		return "", false
	}
	if m := toolRule.FindStringSubmatch(entry); m != nil {
		if !strings.HasPrefix(entry, "Bash") {
			return "", false
		}
		entry = m[1]
	}
	entry = strings.TrimSpace(entry)
	for _, suffix := range []string{":*", " *", "*"} {
		entry = strings.TrimSuffix(entry, suffix)
	}
	return strings.TrimSpace(entry), true
}

// ClaudeToolRule is an entry as Claude Code's --allowedTools takes it: a
// tool name or MCP tool as written, a plain command as Bash(<command>:*).
func ClaudeToolRule(entry string) string {
	if strings.HasPrefix(entry, "mcp__") || toolRule.MatchString(entry) {
		return entry
	}
	return "Bash(" + entry + ":*)"
}

// skillDenied are the tools a Claude Code skill run is refused unless its
// list names them: every write and every way out besides its own tools.
var skillDenied = []string{"Edit", "MultiEdit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "mcp__" + VaultServerName + "__vault_write"}

// SkillClaudeArgs are Claude Code's flags for a skill run: anything not
// allowed is denied without a prompt, the knowledge base and vault reads
// are allowed, the writes are denied unless tools names them, and git push
// is denied unless the caller allowed it. dirs are folders the agent may
// read besides its own, such as the skill's. kiBinary is the ki the run's
// PreToolUse hook starts to check every shell command (GuardBash); sub, unless
// nil, adds the subagent guard to the same settings.
func SkillClaudeArgs(tools, dirs []string, kiBinary string, sub *SubagentGuard) []string {
	rules := make([]string, 0, len(askKBTools)+len(askVaultTools)+len(tools))
	for _, t := range askKBTools {
		rules = append(rules, "mcp__"+MCPServerName+"__"+t)
	}
	for _, t := range askVaultTools {
		rules = append(rules, "mcp__"+VaultServerName+"__"+t)
	}
	push := false
	for _, t := range tools {
		r := ClaudeToolRule(t)
		if !slices.Contains(rules, r) {
			rules = append(rules, r)
		}
		if cmd, ok := shellCommand(t); ok && (cmd == "git" || strings.HasPrefix(cmd, "git push")) {
			push = true
		}
	}
	var denied []string
	for _, d := range skillDenied {
		if !slices.Contains(rules, d) {
			denied = append(denied, d)
		}
	}
	if !push {
		denied = append(denied, "Bash(git push:*)")
	}
	args := make([]string, 0, 5+2*len(dirs)+len(rules))
	args = append(args, "--permission-mode", "dontAsk", "--disallowedTools="+strings.Join(denied, ","),
		"--settings", SkillGuardSettings(kiBinary, sub))
	for _, d := range dirs {
		args = append(args, "--add-dir", d)
	}
	for _, r := range rules {
		args = append(args, "--allowedTools="+r)
	}
	return args
}

// SkillVaultWrites reports whether the tools let the skill write notes.
func SkillVaultWrites(tools []string) bool {
	return slices.Contains(tools, "mcp__"+VaultServerName+"__vault_write")
}

// SkillWorkspace says where a skill run happens, for its brief.
type SkillWorkspace struct {
	// Checkout is what the worktree holds ("pull request #12", "branch main"), "" for a scratch folder.
	Checkout string
	// SHA is the commit the worktree is detached at; Branch the branch on origin, "" when unknown.
	SHA, Branch string
	// Repo is owner/repo on GitHub, "" when the run has no GitHub repo.
	Repo string
}

// SkillBrief is the prompt of a skill run: the procedure with its arguments
// in place of $ARGUMENTS, then the rules of a headless run and the status
// line to end with.
func SkillBrief(s *Skill, args []string, ws SkillWorkspace) string {
	joined := strings.Join(args, " ")
	var b strings.Builder
	switch {
	case strings.Contains(s.Body, "$ARGUMENTS"):
		b.WriteString(strings.ReplaceAll(s.Body, "$ARGUMENTS", joined))
	case joined != "":
		b.WriteString("Arguments: " + joined + "\n\n" + s.Body)
	default:
		b.WriteString(s.Body)
	}
	b.WriteString("\n\nRules for this run of the " + s.Name + " skill:\n")
	b.WriteString("- You run headless: nobody can answer a question or approve a prompt. You cannot ask questions.\n")
	if len(s.Tools) == 0 {
		b.WriteString("- You are read-only: read files, the knowledge base and the vault; no shell command, no edit, nothing posted.\n")
	} else {
		b.WriteString("- Besides reading, you may use only these tools: " + strings.Join(s.Tools, "; ") + ". Anything else is denied; when the skill needs it, stop and say so.\n")
	}
	if ws.Checkout == "" {
		b.WriteString("- You are in an empty scratch folder that is removed after the run.\n")
	} else {
		b.WriteString("- You are in a throwaway worktree of " + ws.Checkout + ", detached at " + ws.SHA)
		if ws.Branch != "" {
			b.WriteString("; its branch on origin is " + ws.Branch)
		}
		b.WriteString(". It is removed after the run: anything not pushed is lost.\n")
	}
	if ws.Repo != "" {
		b.WriteString("- The repository is " + ws.Repo + " on GitHub; pass --repo " + ws.Repo + " to gh outside a checkout of it.\n")
	}
	b.WriteString("- The skill's own files are in " + s.Dir + "; read any file it names from there.\n")
	b.WriteString("- Never print a secret, token, password or key.\n")
	b.WriteString(`
End your final message with one status line, nothing after it:
- DONE: <one-line summary> when the skill finished
- BLOCKED: <the one question you need answered> when it cannot go on without a person
- FAILED: <reason> when something broke`)
	return b.String()
}

// ParseSkillStatus reads a skill run's final message: its status line when
// it ends with one, else DONE with the last line as the detail. An empty
// message is FAILED.
func ParseSkillStatus(last string) (status, detail string) {
	if strings.TrimSpace(last) == "" {
		return JobFailed, "no final message"
	}
	if status, detail = ParseStatus(last); status != JobFailed || detail != noStatusLine {
		return status, detail
	}
	lines := strings.Split(strings.TrimSpace(last), "\n")
	return JobDone, CleanTitle(lines[len(lines)-1])
}
