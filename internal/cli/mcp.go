package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/kislow/kio/internal/ki"
)

const mcpUsage = `usage: ki mcp ls [--json] [--agent claude|cursor|codex]

Every MCP server each agent on this machine can reach, with where it comes from (ki, claude.ai,
plugin, local, project), its transport, a redacted target (a URL's host or a command's name; never
a path, token, header or environment value) and its status (connected, needs-auth, failed, unknown).

Claude Code's status comes from "claude mcp list". Cursor and Codex have no cheap health probe, so
their servers are read from their config files with status unknown. ki's own servers (ki-kb, ki-vault)
are listed for every agent, ki-kb with the brains it would get in the current repo. An agent that
cannot be read is reported in the errors and does not fail the list.`

// mcpAgentTimeout bounds reading one agent's servers; claude mcp list probes each server.
const mcpAgentTimeout = 30 * time.Second

// mcpOutput is what `ki mcp ls --json` prints.
type mcpOutput struct {
	Servers []ki.MCPEntry `json:"servers"`
	Errors  []mcpProblem  `json:"errors"`
}

// mcpProblem is one agent whose servers could not be read, wholly or in part.
type mcpProblem struct {
	Agent string `json:"agent"`
	Error string `json:"error"`
}

func (a *App) mcp(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintln(a.Stdout, mcpUsage)
		return nil
	}
	if args[0] != "ls" {
		return fail("unknown command ki mcp %s\n\n%s", args[0], mcpUsage)
	}
	asJSON, agents, err := parseMCPArgs(args[1:])
	if err != nil {
		return err
	}
	out := a.listMCP(ctx, agents)
	if asJSON {
		enc := json.NewEncoder(a.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			return fmt.Errorf("writing MCP servers: %w", err)
		}
		return nil
	}
	return a.printMCP(out)
}

func parseMCPArgs(args []string) (asJSON bool, agents []string, err error) {
	agents = ki.MCPAgents
	wantAgent := false
	for _, arg := range args {
		switch {
		case wantAgent:
			if !slices.Contains(ki.MCPAgents, arg) {
				return false, nil, fail("--agent %q: use one of %s", arg, strings.Join(ki.MCPAgents, ", "))
			}
			agents, wantAgent = []string{arg}, false
		case arg == "--json":
			asJSON = true
		case arg == "--agent":
			wantAgent = true
		default:
			return false, nil, fail("unknown argument %s\n\n%s", arg, mcpUsage)
		}
	}
	if wantAgent {
		return false, nil, fail("--agent needs a value")
	}
	return asJSON, agents, nil
}

// listMCP reads each agent's servers on its own timeout; a failing agent
// leaves a problem behind and the others still list.
func (a *App) listMCP(ctx context.Context, agents []string) mcpOutput {
	out := mcpOutput{Servers: []ki.MCPEntry{}, Errors: []mcpProblem{}}
	root := git(ctx, a.Dir, "rev-parse", "--show-toplevel")
	kbURL, brains, problem := a.kiMCPScope(ctx)
	if problem != nil {
		out.Errors = append(out.Errors, mcpProblem{Agent: "ki", Error: problem.Error()})
	}
	vault := a.Getenv("KI_VAULT") != "" && a.binary() != ""

	for _, agent := range agents {
		actx, cancel := context.WithTimeout(ctx, mcpAgentTimeout)
		var entries []ki.MCPEntry
		var err error
		switch agent {
		case "claude":
			entries, err = a.claudeMCP(actx, root)
		case "cursor":
			entries, err = a.cursorMCP(root)
		case "codex":
			entries, err = a.codexMCP(root)
		}
		cancel()
		if err != nil {
			out.Errors = append(out.Errors, mcpProblem{Agent: agent, Error: err.Error()})
		}
		out.Servers = append(out.Servers, ki.WithKiServers(entries, agent, kbURL, brains, vault)...)
	}
	ki.SortMCP(out.Servers)
	return out
}

// kiMCPScope is the KB URL and the brains ki-kb would get in the current repo.
// With no usable configuration the URL may still come from KI_MCP_URL.
func (a *App) kiMCPScope(ctx context.Context) (url string, brains []string, err error) {
	h, err := a.current(ctx, nil)
	if err != nil {
		return a.mcpURL(nil), nil, err
	}
	return a.mcpURL(h), h.session.Brains, nil
}

func (a *App) claudeMCP(ctx context.Context, root string) ([]ki.MCPEntry, error) {
	list := a.ClaudeMCPList
	if list == nil {
		list = a.runClaudeMCPList
	}
	text, err := list(ctx)
	if err != nil {
		return nil, fmt.Errorf("claude mcp list: %w", err)
	}
	var project []string
	if root != "" {
		entries, err := readMCPFile(filepath.Join(root, ".mcp.json"), func(b []byte) ([]ki.MCPEntry, error) {
			return ki.ParseMCPJSON("claude", ki.MCPSourceProject, b)
		})
		if err != nil {
			return ki.ParseClaudeList(text, nil), err
		}
		for _, e := range entries {
			project = append(project, e.Name)
		}
	}
	return ki.ParseClaudeList(text, project), nil
}

func (a *App) runClaudeMCPList(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", errors.New("claude is not on PATH")
	}
	cmd := exec.CommandContext(ctx, "claude", "mcp", "list") // #nosec G204 -- fixed binary and arguments
	cmd.Dir = a.Dir
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("timed out after %s", mcpAgentTimeout)
	}
	if err != nil && len(out) == 0 {
		return "", fmt.Errorf("running claude: %w", err)
	}
	return string(out), nil
}

func (a *App) cursorMCP(root string) ([]ki.MCPEntry, error) {
	parse := func(source string) func([]byte) ([]ki.MCPEntry, error) {
		return func(b []byte) ([]ki.MCPEntry, error) { return ki.ParseMCPJSON("cursor", source, b) }
	}
	return readMCPFiles(
		mcpFile{filepath.Join(a.home(), ".cursor", "mcp.json"), parse(ki.MCPSourceLocal)},
		mcpFile{projectPath(root, ".cursor", "mcp.json"), parse(ki.MCPSourceProject)},
	)
}

func (a *App) codexMCP(root string) ([]ki.MCPEntry, error) {
	home := orDefault(a.Getenv("CODEX_HOME"), filepath.Join(a.home(), ".codex"))
	parse := func(source string) func([]byte) ([]ki.MCPEntry, error) {
		return func(b []byte) ([]ki.MCPEntry, error) { return ki.ParseCodexTOML(source, b), nil }
	}
	return readMCPFiles(
		mcpFile{filepath.Join(home, "config.toml"), parse(ki.MCPSourceLocal)},
		mcpFile{projectPath(root, ".codex", "config.toml"), parse(ki.MCPSourceProject)},
	)
}

// projectPath is a file inside the repo, "" outside one.
func projectPath(root string, elem ...string) string {
	if root == "" {
		return ""
	}
	return filepath.Join(append([]string{root}, elem...)...)
}

type mcpFile struct {
	path  string
	parse func([]byte) ([]ki.MCPEntry, error)
}

// readMCPFiles reads every file that exists; one that cannot be parsed is
// reported and the others still count.
func readMCPFiles(files ...mcpFile) ([]ki.MCPEntry, error) {
	var entries []ki.MCPEntry
	var errs []error
	for _, f := range files {
		if f.path == "" {
			continue
		}
		got, err := readMCPFile(f.path, f.parse)
		entries = append(entries, got...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return entries, errors.Join(errs...)
}

// readMCPFile parses one config file; a missing file means the agent has no servers there.
func readMCPFile(path string, parse func([]byte) ([]ki.MCPEntry, error)) ([]ki.MCPEntry, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the agents' own config locations
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	entries, err := parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return entries, nil
}

func (a *App) printMCP(out mcpOutput) error {
	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tNAME\tSOURCE\tTRANSPORT\tSTATUS\tTARGET")
	for _, s := range out.Servers {
		target := s.Target
		if len(s.Brains) > 0 {
			target += " (brains: " + strings.Join(s.Brains, ",") + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Agent, s.Name, s.Source, s.Transport, s.Status, target)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("writing MCP servers: %w", err)
	}
	for _, p := range out.Errors {
		fmt.Fprintf(a.Stderr, "ki: warning: %s: %s\n", p.Agent, p.Error)
	}
	return nil
}
