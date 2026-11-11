package ki

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// KindAsk marks a ledger record of `ki ask`: one read-only question.
const KindAsk = "ask"

// Where an answer may come from.
const (
	SourceKB     = "kb"
	SourceVault  = "vault"
	SourceGitHub = "gh"
)

// AskSources is every source, in the order the brief names them.
var AskSources = []string{SourceKB, SourceVault, SourceGitHub}

// ParseSources reads --source: a comma-separated subset of AskSources.
func ParseSources(s string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !slices.Contains(AskSources, part) {
			return nil, fmt.Errorf("unknown source %q (use %s)", part, strings.Join(AskSources, ", "))
		}
		if !slices.Contains(out, part) {
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--source needs one or more of %s", strings.Join(AskSources, ", "))
	}
	return out, nil
}

// askFamily is the agent as ki asks it: claude, codex, cursor, or "" for an
// agent ki cannot hold to read-only.
func askFamily(agent string) string {
	switch filepath.Base(agent) {
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	case "cursor-agent", "agent":
		return "cursor"
	}
	return ""
}

// AskSupported reports whether ki can start the agent unable to write.
func AskSupported(agent string) bool { return askFamily(agent) != "" }

// AskGitHub reports whether the agent can read GitHub during an ask. Only
// Claude Code can be limited to gh's read subcommands; Codex's read-only
// sandbox and Cursor's sandbox have no network, which is what keeps gh from
// writing there.
func AskGitHub(agent string) bool { return askFamily(agent) == "claude" }

// askGitHubCommands are the gh subcommands an ask may run, all read-only.
var askGitHubCommands = []string{"gh search", "gh pr view", "gh issue view", "gh run view", "gh project item-list"}

// askKBTools are the knowledge base tools; the server has no other.
var askKBTools = []string{"get_context", "decisions_for", "search"}

// askVaultTools are the vault server's read tools; the read-only server has no other.
var askVaultTools = []string{"vault_read", "vault_list", "vault_search"}

// ReadOnlyVaultServer is `ki vault mcp --read-only`: the vault's read tools and
// no vault_write. With paths it reads only those vault folders, one --path each.
func ReadOnlyVaultServer(kiBinary, vaultDir string, paths ...string) MCPServer {
	s := VaultServer(kiBinary, vaultDir)
	s.Args = append(s.Args, "--read-only")
	for _, p := range paths {
		s.Args = append(s.Args, "--path", p)
	}
	return s
}

// AskSetup is what one ask needs to start an agent read-only.
type AskSetup struct {
	Sources []string
	// MCPURL is the knowledge base's base URL; Brains its scope header.
	MCPURL string
	Brains []string
	// Vault is the read-only vault server, zero when the machine has no vault.
	Vault MCPServer
	// VaultPaths are the vault folders the read-only server is limited to;
	// empty means the whole vault.
	VaultPaths []string
	// CodexVault is whether Codex's own config defines the vault server, which
	// is then pointed at the read-only one even when no vault is known here.
	CodexVault bool
}

// AskArgs are the flags that start the agent for a question, unable to
// change anything: Claude Code may call only the source tools listed, with
// no MCP server but ki's and every other tool denied without a prompt;
// Codex runs in its read-only sandbox with ki's servers defined in -c
// (the token stays in KI_TOKEN); Cursor runs in ask mode, which refuses
// edits and shell commands, inside its sandbox, with the MCP tools its
// workspace allows (AskCursorPermissions).
func AskArgs(agent string, s AskSetup) []string {
	switch askFamily(agent) {
	case "claude":
		tools := askClaudeTools(s.Sources)
		args := make([]string, 0, 4+len(tools))
		args = append(args,
			"--strict-mcp-config", "--permission-mode", "dontAsk",
			"--disallowedTools=Edit,Write,NotebookEdit,WebFetch,WebSearch,mcp__"+VaultServerName+"__vault_write",
		)
		for _, tool := range tools {
			args = append(args, "--allowedTools="+tool)
		}
		return args
	case "codex":
		return append([]string{"--sandbox", "read-only", "--skip-git-repo-check"}, codexAskConfig(s)...)
	case "cursor":
		return []string{"--mode", "ask", "--sandbox", "enabled", "--approve-mcps"}
	}
	return nil
}

func askClaudeTools(sources []string) []string {
	var tools []string
	if slices.Contains(sources, SourceKB) {
		for _, t := range askKBTools {
			tools = append(tools, "mcp__"+MCPServerName+"__"+t)
		}
	}
	if slices.Contains(sources, SourceVault) {
		for _, t := range askVaultTools {
			tools = append(tools, "mcp__"+VaultServerName+"__"+t)
		}
	}
	if slices.Contains(sources, SourceGitHub) {
		for _, c := range askGitHubCommands {
			tools = append(tools, "Bash("+c+":*)")
		}
	}
	return tools
}

// codexAskConfig defines ki's servers for Codex on the command line. The
// vault server is always the read-only one: Codex's sandbox does not cover
// an MCP server, and `ki setup codex` registers the writing one.
func codexAskConfig(s AskSetup) []string {
	var c []string
	if slices.Contains(s.Sources, SourceKB) {
		c = append(c,
			"-c", fmt.Sprintf("mcp_servers.%s.url=%q", MCPServerName, strings.TrimRight(s.MCPURL, "/")+"/mcp"),
			"-c", fmt.Sprintf("mcp_servers.%s.bearer_token_env_var=%q", MCPServerName, "KI_TOKEN"),
			"-c", fmt.Sprintf("mcp_servers.%s.env_http_headers={ %q = %q }", MCPServerName, "X-Ki-Brains", "KI_BRAINS"),
		)
	}
	readOnly := tomlStrings(ReadOnlyVaultServer("", "", s.VaultPaths...).Args)
	switch {
	case s.Vault.Command != "":
		c = append(c,
			"-c", fmt.Sprintf("mcp_servers.%s.command=%q", VaultServerName, s.Vault.Command),
			"-c", fmt.Sprintf("mcp_servers.%s.args=%s", VaultServerName, tomlStrings(s.Vault.Args)),
			"-c", fmt.Sprintf("mcp_servers.%s.env={ KI_VAULT = %q }", VaultServerName, s.Vault.Env["KI_VAULT"]),
		)
	case s.CodexVault:
		c = append(c, "-c", fmt.Sprintf("mcp_servers.%s.args=%s", VaultServerName, readOnly))
	}
	return c
}

func tomlStrings(list []string) string {
	q := make([]string, len(list))
	for i, s := range list {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// AskMCPConfig is the MCP config of an ask for Claude Code (--mcp-config)
// or, with cursor set, for Cursor's workspace. Cursor's file sits in the
// directory the agent reads, so it names the token through ${env:KI_TOKEN}
// instead of holding it; Claude Code's sits outside, in the job folder.
func AskMCPConfig(s AskSetup, token string, cursor bool) MCPConfig {
	cfg := MCPConfig{MCPServers: map[string]MCPServer{}}
	if slices.Contains(s.Sources, SourceKB) {
		if cursor {
			token = "${env:KI_TOKEN}" // #nosec G101 -- a reference Cursor resolves from the environment, not a credential
		}
		cfg.MCPServers[MCPServerName] = NewMCPConfig(s.MCPURL, token, s.Brains).MCPServers[MCPServerName]
	}
	if slices.Contains(s.Sources, SourceVault) && s.Vault.Command != "" {
		cfg.MCPServers[VaultServerName] = s.Vault
	}
	if cursor {
		for name, srv := range cfg.MCPServers {
			srv.Type = "" // Cursor infers the transport from url or command
			cfg.MCPServers[name] = srv
		}
	}
	return cfg
}

// Where Cursor reads a workspace's MCP servers and its permissions.
const (
	CursorMCPFile = ".cursor/mcp.json"
	CursorCLIFile = ".cursor/cli.json"
)

// CursorPermissions is a workspace's .cursor/cli.json.
type CursorPermissions struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// AskCursorPermissions allows the source tools of ki's MCP servers and
// nothing else. Cursor's ask mode refuses every edit and shell command, and
// an MCP call it is not told to allow; this lets the sources through.
func AskCursorPermissions(sources []string) CursorPermissions {
	var p CursorPermissions
	p.Permissions.Allow, p.Permissions.Deny = []string{}, []string{}
	if slices.Contains(sources, SourceKB) {
		for _, t := range askKBTools {
			p.Permissions.Allow = append(p.Permissions.Allow, "Mcp("+MCPServerName+":"+t+")")
		}
	}
	if slices.Contains(sources, SourceVault) {
		for _, t := range askVaultTools {
			p.Permissions.Allow = append(p.Permissions.Allow, "Mcp("+VaultServerName+":"+t+")")
		}
	}
	return p
}

// askSourceText says, per source, which tools reach it.
var askSourceText = map[string]string{
	SourceKB:     "the knowledge base (" + MCPServerName + " tools get_context, search, decisions_for): cite the document it names",
	SourceVault:  "the vault of notes (" + VaultServerName + " tools vault_search, vault_list, vault_read): cite the note's path",
	SourceGitHub: "GitHub, read-only through gh (`gh search`, `gh pr view`, `gh issue view`, `gh run view`, `gh project item-list`): cite the URL",
}

// AskBrief is the prompt of an ask: the question, the sources it may use
// and how to answer. There is no status line: the answer is the result.
func AskBrief(question string, sources []string) string { return AskBriefAfter(question, sources, "") }

// AskBriefAfter is AskBrief for a follow-up that starts a fresh run: the
// earlier turns (AskHistory) come first, then the question.
func AskBriefAfter(question string, sources []string, history string) string {
	var b strings.Builder
	if history == "" {
		b.WriteString("Answer this question:\n\n")
	} else {
		b.WriteString("This continues an earlier conversation. The turns so far, oldest first:\n\n" + history + "\n\nAnswer this follow-up question, using those turns as context:\n\n")
	}
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nLook it up in these sources, and only these:\n")
	for _, s := range AskSources {
		if slices.Contains(sources, s) {
			b.WriteString("- " + askSourceText[s] + "\n")
		}
	}
	b.WriteString(`
Rules:
- You are read-only. Do not create, edit or delete anything: no files, notes, issues, comments, pull requests or labels. If the question asks you to change something, say that you only answer questions.
- Answer short, in markdown. Back every fact with its source; say what you could not confirm.
- If nothing in the sources answers it, say so in one sentence and do not guess.
- Never print a secret, token, password or key, and never a full cloud account id: write the last four digits only.
- You cannot ask questions.

End your final message with a line ` + "`Sources:`" + ` and one ` + "`- `" + ` line per source you cited (the knowledge base document, the note path or the URL), nothing after them. With nothing cited, end with ` + "`Sources:`" + ` and ` + "`- none`" + `.`)
	return b.String()
}

var sourcesHeading = regexp.MustCompile(`(?i)^\s*(?:#+\s*)?(?:\*\*)?sources:?(?:\*\*)?:?\s*$`)

// SplitAnswer separates an answer from its trailing Sources list. An answer
// without one is all answer and no sources.
func SplitAnswer(last string) (answer string, sources []string) {
	lines := strings.Split(strings.TrimSpace(last), "\n")
	at := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if sourcesHeading.MatchString(lines[i]) {
			at = i
			break
		}
	}
	if at < 0 {
		return strings.TrimSpace(last), []string{}
	}
	sources = []string{}
	for _, l := range lines[at+1:] {
		l = strings.TrimSpace(l)
		l = strings.TrimSpace(strings.TrimLeft(l, "-*•"))
		if l == "" || strings.EqualFold(l, "none") {
			continue
		}
		sources = append(sources, l)
	}
	return strings.TrimSpace(strings.Join(lines[:at], "\n")), sources
}

// ParseAskStatus reads an ask's final message: any answer is DONE, an empty one FAILED.
func ParseAskStatus(last string) (status, detail string) {
	if strings.TrimSpace(last) == "" {
		return JobFailed, "no answer in the final message"
	}
	_, sources := SplitAnswer(last)
	switch n := len(sources); n {
	case 0:
		return JobDone, "answered, no source cited"
	case 1:
		return JobDone, "answered, 1 source"
	default:
		return JobDone, fmt.Sprintf("answered, %d sources", n)
	}
}
