package ki

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Agents `ki mcp ls` reports on, in output order.
var MCPAgents = []string{"claude", "cursor", "codex"}

// Where an MCP server comes from.
const (
	MCPSourceKi        = "ki"
	MCPSourceClaudeAI  = "claude.ai"
	MCPSourcePlugin    = "plugin"
	MCPSourceLocal     = "local"
	MCPSourceProject   = "project"
	MCPStatusConnected = "connected"
	MCPStatusNeedsAuth = "needs-auth"
	MCPStatusFailed    = "failed"
	MCPStatusUnknown   = "unknown"
)

// MCPEntry is one MCP server one agent can reach. It holds only what is safe to
// print: the target is a URL's host or a command's name, never a path, query,
// argument, header or environment value, so no token can reach the output.
type MCPEntry struct {
	Agent     string   `json:"agent"`
	Name      string   `json:"name"`
	Source    string   `json:"source"`
	Transport string   `json:"transport"`
	Target    string   `json:"target"`
	Status    string   `json:"status"`
	Brains    []string `json:"brains,omitempty"`
}

// RedactTarget reduces a server's URL or command line to its host or command name.
func RedactTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return u.Host
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

var claudeTransportSuffix = regexp.MustCompile(`(?i)\s+\((http|sse|stdio)\)$`)

// ParseClaudeList reads `claude mcp list`: one `name: target [(HTTP)] - status`
// line per server after a health-check header. project names the servers the
// repo's .mcp.json defines, which the list does not distinguish from user ones.
func ParseClaudeList(out string, project []string) []MCPEntry {
	inProject := map[string]bool{}
	for _, n := range project {
		inProject[n] = true
	}
	var entries []MCPEntry
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		name, rest, ok := strings.Cut(line, ": ")
		dash := strings.LastIndex(rest, " - ")
		if !ok || name == "" || dash < 0 {
			continue
		}
		target, transport := strings.TrimSpace(rest[:dash]), ""
		if m := claudeTransportSuffix.FindStringSubmatch(target); m != nil {
			transport = strings.ToLower(m[1])
			target = strings.TrimSpace(strings.TrimSuffix(target, m[0]))
		}
		if transport == "" {
			transport = transportOf(strings.Contains(target, "://"), "")
		}
		entries = append(entries, MCPEntry{
			Agent: "claude", Name: name, Source: claudeSource(name, inProject[name]),
			Transport: transport, Target: RedactTarget(target), Status: claudeStatus(rest[dash+3:]),
		})
	}
	return entries
}

func claudeSource(name string, project bool) string {
	switch {
	case isKiServer(name):
		return MCPSourceKi
	case strings.HasPrefix(name, "claude.ai "):
		return MCPSourceClaudeAI
	case strings.HasPrefix(name, "plugin:"):
		return MCPSourcePlugin
	case project:
		return MCPSourceProject
	}
	return MCPSourceLocal
}

func claudeStatus(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "needs auth"):
		return MCPStatusNeedsAuth
	case strings.Contains(s, "fail"):
		return MCPStatusFailed
	case strings.Contains(s, "connected") && !strings.Contains(s, "not connected"):
		return MCPStatusConnected
	}
	return MCPStatusUnknown
}

func isKiServer(name string) bool { return name == MCPServerName || name == VaultServerName }

// transportOf names the transport of a server from what its config declares.
func transportOf(hasURL bool, declared string) string {
	switch strings.ToLower(declared) {
	case "sse":
		return "sse"
	case "stdio":
		return "stdio"
	case "http", "streamable-http", "streamablehttp":
		return "http"
	}
	if hasURL {
		return "http"
	}
	return "stdio"
}

// entryFor builds a config-file entry. Config files carry no health, so its
// status is unknown.
func entryFor(agent, source, name, rawURL, command, declared string) MCPEntry {
	if isKiServer(name) {
		source = MCPSourceKi
	}
	target := command
	if rawURL != "" {
		target = rawURL
	}
	transport := transportOf(rawURL != "", declared)
	if declared == "" && rawURL != "" && strings.HasSuffix(strings.TrimRight(strings.SplitN(rawURL, "?", 2)[0], "/"), "/sse") {
		transport = "sse"
	}
	return MCPEntry{Agent: agent, Name: name, Source: source, Transport: transport, Target: RedactTarget(target), Status: MCPStatusUnknown}
}

// ParseMCPJSON reads an `mcpServers` JSON file (Cursor's mcp.json, Claude
// Code's .mcp.json). Headers and env are never read.
func ParseMCPJSON(agent, source string, data []byte) ([]MCPEntry, error) {
	var f struct {
		Servers map[string]struct {
			Type    string `json:"type"`
			URL     string `json:"url"`
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("not valid MCP JSON: %w", err)
	}
	names := make([]string, 0, len(f.Servers))
	for n := range f.Servers {
		names = append(names, n)
	}
	sort.Strings(names)
	entries := make([]MCPEntry, 0, len(names))
	for _, n := range names {
		s := f.Servers[n]
		entries = append(entries, entryFor(agent, source, n, s.URL, s.Command, s.Type))
	}
	return entries, nil
}

var codexTable = regexp.MustCompile(`^\[mcp_servers\.(?:"([^"]+)"|([A-Za-z0-9_-]+))(\.[^\]]*)?\]\s*(?:#.*)?$`)

// ParseCodexTOML reads the [mcp_servers.NAME] tables of Codex's config.toml:
// only each server's url and command. Sub-tables (env, headers) are skipped.
func ParseCodexTOML(source string, data []byte) []MCPEntry {
	type server struct{ url, command string }
	servers := map[string]*server{}
	var order []string
	var cur *server
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "["):
			cur = nil
			m := codexTable.FindStringSubmatch(line)
			if m == nil || m[3] != "" {
				continue
			}
			name := m[1] + m[2]
			if servers[name] == nil {
				servers[name] = &server{}
				order = append(order, name)
			}
			cur = servers[name]
		case cur != nil:
			key, val, ok := strings.Cut(line, "=")
			switch strings.TrimSpace(key) {
			case "url":
				cur.url = tomlString(val, ok)
			case "command":
				cur.command = tomlString(val, ok)
			}
		}
	}
	sort.Strings(order)
	entries := make([]MCPEntry, 0, len(order))
	for _, n := range order {
		entries = append(entries, entryFor("codex", source, n, servers[n].url, servers[n].command, ""))
	}
	return entries
}

// tomlString reads a single-line quoted TOML string value, dropping any trailing comment.
func tomlString(val string, ok bool) string {
	val = strings.TrimSpace(val)
	if !ok || len(val) < 2 {
		return ""
	}
	switch val[0] {
	case '"':
		for i := 1; i < len(val); i++ {
			switch val[i] {
			case '\\':
				i++
			case '"':
				s, err := strconv.Unquote(val[:i+1])
				if err != nil {
					return ""
				}
				return s
			}
		}
	case '\'':
		if end := strings.IndexByte(val[1:], '\''); end >= 0 {
			return val[1 : end+1]
		}
	}
	return ""
}

// WithKiServers makes sure agent's entries include ki's own servers: the KB
// server with the brains it would get here, and the vault server when the
// session would start one. A server the agent's own list already shows keeps
// its status and transport and gains the source and brains.
func WithKiServers(entries []MCPEntry, agent, kbURL string, brains []string, vault bool) []MCPEntry {
	ours := []MCPEntry{{
		Agent: agent, Name: MCPServerName, Source: MCPSourceKi, Transport: "http",
		Target: RedactTarget(kbURL), Status: MCPStatusUnknown, Brains: brains,
	}}
	if vault {
		ours = append(ours, MCPEntry{
			Agent: agent, Name: VaultServerName, Source: MCPSourceKi, Transport: "stdio",
			Target: "ki", Status: MCPStatusUnknown,
		})
	}
	for _, o := range ours {
		found := false
		for i := range entries {
			if entries[i].Name == o.Name {
				found = true
				entries[i].Source = MCPSourceKi
				if o.Name == MCPServerName {
					entries[i].Brains = o.Brains
				}
			}
		}
		if !found {
			entries = append(entries, o)
		}
	}
	return entries
}

// SortMCP orders entries by agent (claude, cursor, codex), then name.
func SortMCP(entries []MCPEntry) {
	rank := func(a string) int {
		for i, n := range MCPAgents {
			if n == a {
				return i
			}
		}
		return len(MCPAgents)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if ri, rj := rank(entries[i].Agent), rank(entries[j].Agent); ri != rj {
			return ri < rj
		}
		return entries[i].Name < entries[j].Name
	})
}
