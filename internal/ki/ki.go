// Package ki holds the side-effect-free logic of the ki CLI: resolving a repo
// to its scope and identity, choosing a token, and building the agent launch.
package ki

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/kislow/kio/pkg/projects"
	"github.com/kislow/kio/pkg/scope"
)

// MCPServerName is the name agents see the knowledge base under.
const MCPServerName = "ki-kb"

// Session is what a ki session in one repo may see and who it acts as.
type Session struct {
	Projects  []string
	Brains    []string
	Sensitive bool
	Identity  *projects.Identity
	Board     string
	// Unmapped is true for a repo that is not in projects.yaml. It gets cortex
	// only (plus any --brain), no identity check and no GH_TOKEN, so ki refuses
	// to start it without --force.
	Unmapped bool
	// Folder is true for a session started outside a git repo (ResolveFolder,
	// IdentitySession): there is no repo email to check and no repo to refuse.
	Folder bool
}

// ErrInvalidBrain is returned for a --brain value that is not a brain name.
var ErrInvalidBrain = errors.New("invalid brain name")

// ValidateBrains rejects --brain values that are not plain brain names, such
// as a family pattern, before any token is chosen.
func ValidateBrains(brains []string) error {
	for _, b := range brains {
		if !scope.ValidName(b) {
			return fmt.Errorf("%w: %q (use names like acme or client-acme)", ErrInvalidBrain, b)
		}
	}
	return nil
}

// Resolve finds the scope for the repo with this git remote. A repo listed
// under several project names gets the union of their brains. An unknown repo
// is Unmapped and gets cortex only, never "every brain the token grants".
func Resolve(cfg *projects.Config, remote string, extra []string) Session {
	var s Session
	repo, err := projects.NormalizeRepo(remote)
	for _, p := range cfg.Projects {
		if err != nil || p.Repo != repo {
			continue
		}
		s.Projects = append(s.Projects, p.Name)
		s.Brains = appendUnique(s.Brains, p.Brains...)
		if s.Identity == nil {
			id := p.Identity
			s.Identity = &id
		}
		if s.Board == "" {
			s.Board = p.Board
		}
	}
	if len(s.Projects) == 0 {
		s.Unmapped = true
		s.Brains = []string{scope.Cortex}
	}
	s.Brains = appendUnique(s.Brains, extra...)
	for _, b := range s.Brains {
		if cfg.IsSensitive(b) {
			s.Sensitive = true
		}
	}
	return s
}

func appendUnique(list []string, items ...string) []string {
	for _, it := range items {
		if !slices.Contains(list, it) {
			list = append(list, it)
		}
	}
	return list
}

const (
	tokenName          = "token"
	sensitiveTokenName = "token-sensitive"
	// LegacySensitiveTokenName is the old file name of the sensitive token,
	// still read when token-sensitive is absent so existing machines keep working.
	LegacySensitiveTokenName = "token-pci"
)

// TokenFile is the KB token for this session: sessions touching a sensitive
// brain use a separate one. A machine that only has the legacy file keeps
// getting it; callers compare the base name with LegacySensitiveTokenName to
// warn.
func TokenFile(confDir string, s Session) string {
	if !s.Sensitive {
		return filepath.Join(confDir, tokenName)
	}
	path := filepath.Join(confDir, sensitiveTokenName)
	if _, err := os.Stat(path); err != nil {
		legacy := filepath.Join(confDir, LegacySensitiveTokenName)
		if _, lerr := os.Stat(legacy); lerr == nil {
			return legacy
		}
	}
	return path
}

// IdentityProblem explains why git would commit as the wrong person, or returns "".
func IdentityProblem(s Session, actualEmail string) string {
	if s.Identity == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(actualEmail), s.Identity.Email) {
		return ""
	}
	if actualEmail == "" {
		actualEmail = "<unset>"
	}
	return fmt.Sprintf("this repo belongs to identity %q (%s) but git would commit as %s; fix your includeIf config or clone location",
		s.Identity.Name, s.Identity.Email, actualEmail)
}

// VaultServerName is the name agents see the local vault server under.
const VaultServerName = "ki-vault"

// MCPServer is one entry in an MCP client config: an HTTP server (URL and
// headers) or a stdio one (command, args and env).
type MCPServer struct {
	Type    string            `json:"type,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// VaultServer is the stdio entry for `ki vault mcp`: the ki binary itself,
// pointed at the vault. Added only when the machine has a vault.
func VaultServer(kiBinary, vaultDir string) MCPServer {
	return MCPServer{
		Type:    "stdio",
		Command: kiBinary,
		Args:    []string{"vault", "mcp"},
		Env:     map[string]string{"KI_VAULT": vaultDir},
	}
}

// MCPConfig is the file passed to `claude --mcp-config`.
type MCPConfig struct {
	MCPServers map[string]MCPServer `json:"mcpServers"`
}

// NewMCPConfig builds the client config for the KB server.
func NewMCPConfig(url, token string, brains []string) MCPConfig {
	return MCPConfig{MCPServers: map[string]MCPServer{
		MCPServerName: {
			Type: "http",
			URL:  strings.TrimRight(url, "/") + "/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer " + token,
				"X-Ki-Brains":   strings.Join(brains, ","),
			},
		},
	}}
}

// Launch is the agent process to exec.
type Launch struct {
	Argv []string
	Env  map[string]string
}

// LaunchInput gathers what BuildLaunch needs.
type LaunchInput struct {
	Agent            string
	AgentArgs        []string
	Session          Session
	Token            string
	MCPURL           string
	MCPConfigPath    string
	InstructionsPath string
	GHToken          string
	// Model, when set, is passed to the agent (claude and codex take --model);
	// every agent also sees it as KI_MODEL. Empty leaves the agent's default.
	Model string
	// ModeArgs are the agent's permission flags, from ModeArgs. They go before
	// AgentArgs so the user's own flags come last.
	ModeArgs []string
	// Effort is passed to Claude Code and agy as --effort; other agents have no equivalent.
	Effort string
	// Headless, when set, starts the agent non-interactively on one task (ki delegate).
	Headless *Headless
	// Resume, when set, reopens that agent session instead of starting a new one.
	Resume string
	// Settings is Claude Code's --settings JSON (SessionSettings); empty means ClaudeSettings.
	Settings string
}

// ClaudeSettings is passed to Claude Code with --settings for every ki session:
// no Co-Authored-By trailer and no attribution line in PR descriptions.
const ClaudeSettings = `{"attribution":{"commit":"","pr":""}}`

// BuildLaunch returns the command and extra environment for the agent.
// Claude Code gets the MCP config, instructions and settings as flags; other
// agents read KI_* from the environment through their own configuration.
// With no knowledge base (MCPURL empty) none of the KB variables are set.
// With Headless the agent's non-interactive flags and the task come last.
func BuildLaunch(in LaunchInput) Launch {
	env := map[string]string{}
	if in.MCPURL != "" {
		env["KI_TOKEN"] = in.Token
		env["KI_BRAINS"] = strings.Join(in.Session.Brains, ",")
		env["KI_MCP_URL"] = in.MCPURL
	}
	if in.GHToken != "" {
		env["GH_TOKEN"] = in.GHToken
	}
	if in.Model != "" {
		env["KI_MODEL"] = in.Model
	}
	argv := []string{in.Agent}
	switch filepath.Base(in.Agent) {
	case "claude":
		argv = append(argv, "--mcp-config", in.MCPConfigPath)
		// An ask carries its rules in the prompt and has no session instructions.
		if in.InstructionsPath != "" {
			argv = append(argv, "--append-system-prompt-file", in.InstructionsPath)
		}
		if !slices.Contains(in.ModeArgs, "--settings") { // Claude Code keeps only the last one
			settings := in.Settings
			if settings == "" {
				settings = ClaudeSettings
			}
			argv = append(argv, "--settings", settings)
		}
		if in.Resume != "" {
			argv = append(argv, "--resume", in.Resume)
		}
		if in.Model != "" {
			argv = append(argv, "--model", in.Model)
		}
		if in.Effort != "" {
			argv = append(argv, "--effort", in.Effort)
		}
	case "codex":
		if in.Headless != nil {
			argv = append(argv, "exec") // the subcommand owns the model and permission flags
			if in.Resume != "" {
				argv = append(argv, "resume", in.Resume)
				in.ModeArgs = codexResumeFlags(in.ModeArgs)
			}
		}
		if in.Model != "" {
			argv = append(argv, "--model", in.Model)
		}
	case "cursor-agent", "agent":
		if in.Resume != "" {
			argv = append(argv, "--resume", in.Resume)
		}
	case "agy":
		if in.Model != "" {
			argv = append(argv, "--model", in.Model)
		}
		if in.Effort != "" {
			argv = append(argv, "--effort", in.Effort)
		}
	}
	argv = append(argv, in.ModeArgs...)
	argv = append(argv, in.AgentArgs...)
	if in.Headless != nil {
		argv = append(argv, headlessArgs(in.Agent, in.Headless)...)
	}
	return Launch{Argv: argv, Env: env}
}

// codexResumeFlags rewrites --sandbox X as -c sandbox_mode="X": `codex exec resume`
// rejects --sandbox (codex-cli 0.167.0) but takes -c, so a resumed ask stays read-only.
func codexResumeFlags(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if (args[i] == "--sandbox" || args[i] == "-s") && i+1 < len(args) {
			out = append(out, "-c", fmt.Sprintf("sandbox_mode=%q", args[i+1]))
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// RunArgs are the parsed arguments of `ki [run]`.
type RunArgs struct {
	Brains []string
	Agent  string
	Model  string
	Mode   string
	Force  bool
	// Identity is --identity: outside a repo it picks the session's identity;
	// inside one it must be the repo's.
	Identity  string
	AgentArgs []string
	// AgentGiven is true when --agent named the agent, as opposed to defaultAgent.
	AgentGiven bool
}

// ParseRunArgs parses `ki [--brain B]... [--agent A] [--model M] [--mode M] [--identity NAME] [--force] [-- agent args]`.
// Unknown arguments are passed through to the agent.
func ParseRunArgs(args []string, defaultAgent string) RunArgs {
	r := RunArgs{Agent: defaultAgent}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--brain" && i+1 < len(args):
			r.Brains = append(r.Brains, args[i+1])
			i++
		case a == "--agent" && i+1 < len(args):
			r.Agent, r.AgentGiven = args[i+1], true
			i++
		case a == "--model" && i+1 < len(args):
			r.Model = args[i+1]
			i++
		case a == "--mode" && i+1 < len(args):
			r.Mode = args[i+1]
			i++
		case a == "--identity" && i+1 < len(args):
			r.Identity = args[i+1]
			i++
		case a == "--force":
			r.Force = true
		case a == "--":
			r.AgentArgs = append(r.AgentArgs, args[i+1:]...)
			return r
		default:
			r.AgentArgs = append(r.AgentArgs, a)
		}
	}
	return r
}

// MergeEnv returns base with every key in extra set exactly once. Existing
// entries for those keys are dropped first: with duplicates, some programs read
// the first copy and others the last, so a stale exported GH_TOKEN could win.
func MergeEnv(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := extra[key]; !replaced {
			out = append(out, kv)
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

// PruneRunFiles deletes generated MCP configs (they hold a token) and
// instructions files older than maxAge from dir, and returns how many it
// removed. A session's files are only read when its agent starts, so old ones
// are never needed again.
func PruneRunFiles(dir string, maxAge time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", dir, err)
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !isRunFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return removed, fmt.Errorf("removing %s: %w", e.Name(), err)
		}
		removed++
	}
	return removed, nil
}

func isRunFile(name string) bool {
	return strings.HasPrefix(name, "mcp-") && strings.HasSuffix(name, ".json") ||
		strings.HasPrefix(name, "instructions-") && strings.HasSuffix(name, ".md")
}

// WritePrivate writes a file only the current user can read; it may hold a token.
func WritePrivate(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	return WritePrivateBytes(path, append(data, '\n'))
}

// WritePrivateBytes writes data to a file only the current user can read.
func WritePrivateBytes(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	// Replaced by rename, so a reader (ki delegate ls) never sees a half-written file.
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, werr := tmp.Write(data)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name()) // the write already failed; a leftover temp file is the only thing left to clean
		return fmt.Errorf("writing %s: %w", path, werr)
	}
	return nil
}
