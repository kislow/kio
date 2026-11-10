package ki

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Managed blocks let ki own a section of a file the user also edits (Codex's
// config.toml and AGENTS.md) without touching anything outside it. Running
// setup again replaces the block in place.
const (
	blockBegin = ">>> ki managed block: do not edit, run `ki setup` instead >>>"
	blockEnd   = "<<< ki managed block <<<"
)

// ErrUnmanagedConflict means the user already defined what ki wants to manage.
var ErrUnmanagedConflict = errors.New("conflicting definition outside the ki managed block")

// Comment styles for the files ki manages.
type Comment struct{ Open, Close string }

// TOMLComment wraps markers for config.toml.
func TOMLComment() Comment { return Comment{Open: "# ", Close: ""} }

// MarkdownComment wraps markers for AGENTS.md.
func MarkdownComment() Comment { return Comment{Open: "<!-- ", Close: " -->"} }

func (c Comment) begin() string { return c.Open + blockBegin + c.Close }
func (c Comment) end() string   { return c.Open + blockEnd + c.Close }

// UpsertBlock returns content with ki's managed block set to body: replaced if
// present, appended otherwise. conflict, if non-empty, is a string that must
// not appear outside the block (e.g. the TOML table header ki defines).
func UpsertBlock(content, body string, c Comment, conflict string) (string, error) {
	// Keep a CRLF file CRLF: work in LF and convert back at the end.
	crlf := strings.Contains(content, "\r\n")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	out, err := upsertLF(content, body, c, conflict)
	if err != nil || !crlf {
		return out, err
	}
	return strings.ReplaceAll(out, "\n", "\r\n"), nil
}

func upsertLF(content, body string, c Comment, conflict string) (string, error) {
	block := c.begin() + "\n" + strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n") + "\n" + c.end() + "\n"

	start := strings.Index(content, c.begin())
	if start >= 0 {
		rel := strings.Index(content[start:], c.end())
		if rel < 0 {
			return "", fmt.Errorf("ki managed block is missing its end marker; fix the file by hand")
		}
		stop := start + rel + len(c.end())
		if stop < len(content) && content[stop] == '\n' {
			stop++
		}
		outside := content[:start] + content[stop:]
		if conflict != "" && strings.Contains(outside, conflict) {
			return "", fmt.Errorf("%w: %s", ErrUnmanagedConflict, conflict)
		}
		return content[:start] + block + content[stop:], nil
	}

	if conflict != "" && strings.Contains(content, conflict) {
		return "", fmt.Errorf("%w: %s", ErrUnmanagedConflict, conflict)
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if content != "" {
		content += "\n"
	}
	return content + block, nil
}

// HasBlock reports whether content contains ki's managed block.
func HasBlock(content string, c Comment) bool {
	return strings.Contains(content, c.begin()) && strings.Contains(content, c.end())
}

// CodexTable is the config.toml table ki manages for the knowledge base.
const CodexTable = "[mcp_servers." + MCPServerName + "]"

// CodexVaultTable is the config.toml table for the local vault server.
const CodexVaultTable = "[mcp_servers." + VaultServerName + "]"

// CodexMCPBlock is the Codex MCP server entry. Token and scope are read from
// the environment that `ki --agent codex` sets, so one entry serves every repo.
// With a vault, the block also starts `ki vault mcp` over stdio; the vault
// path is baked in because config.toml is per machine anyway. With no url the
// ki-kb table is left out, and with no vault either the block is empty.
func CodexMCPBlock(url string, vault MCPServer) string {
	block := ""
	if url != "" {
		block = fmt.Sprintf(`%s
url = %q
bearer_token_env_var = "KI_TOKEN"
env_http_headers = { "X-Ki-Brains" = "KI_BRAINS" }
`, CodexTable, strings.TrimRight(url, "/")+"/mcp")
	}
	if vault.Command == "" {
		return block
	}
	if block != "" {
		block += "\n"
	}
	args := make([]string, 0, len(vault.Args))
	for _, a := range vault.Args {
		args = append(args, fmt.Sprintf("%q", a))
	}
	return block + fmt.Sprintf(`%s
command = %q
args = [%s]
env = { KI_VAULT = %q }
`, CodexVaultTable, vault.Command, strings.Join(args, ", "), vault.Env["KI_VAULT"])
}

// ErrBadSettings means a settings file is not a JSON object ki can update.
var ErrBadSettings = errors.New("settings file is not a JSON object")

// ClaudeAttributionOff returns Claude Code user settings with attribution.commit
// and attribution.pr set to empty strings, so no Co-Authored-By trailer or
// "Generated with" line is added anywhere Claude Code runs, with or without ki.
// Every other key is kept. changed is false when nothing needed to change.
func ClaudeAttributionOff(content []byte) (out []byte, changed bool, err error) {
	settings := map[string]any{}
	if len(bytes.TrimSpace(content)) > 0 {
		if err := json.Unmarshal(content, &settings); err != nil {
			return nil, false, fmt.Errorf("%w: %w", ErrBadSettings, err)
		}
	}
	attribution := map[string]any{}
	if raw, ok := settings["attribution"]; ok {
		obj, ok := raw.(map[string]any)
		if !ok {
			return nil, false, fmt.Errorf("%w: attribution must be an object", ErrBadSettings)
		}
		attribution = obj
	}
	for _, key := range []string{"commit", "pr"} {
		if v, ok := attribution[key].(string); !ok || v != "" {
			attribution[key] = ""
			changed = true
		}
	}
	if !changed {
		return content, false, nil
	}
	settings["attribution"] = attribution
	out, err = json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, false, fmt.Errorf("encoding settings: %w", err)
	}
	return append(out, '\n'), true, nil
}

// ClaudeAttributionIsOff reports whether settings already hide both attributions.
func ClaudeAttributionIsOff(content []byte) bool {
	_, changed, err := ClaudeAttributionOff(content)
	return err == nil && !changed
}

// CursorRule wraps the agent instructions as an always-applied Cursor project rule.
func CursorRule(instructions string) string {
	return "---\ndescription: Knowledge base usage and no-trace rules\nalwaysApply: true\n---\n\n" +
		strings.TrimSpace(instructions) + "\n"
}
