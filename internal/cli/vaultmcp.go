package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kislow/kio/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxNoteRead caps what vault_read hands back; a note is prose, not a dump.
const maxNoteRead = 200_000

// vaultMCP serves $KI_VAULT to the agent that started it, over stdio, until
// the agent closes the pipe. Stdout is the protocol channel, so nothing else
// in this process may write to it; progress goes to stderr. readOnly leaves
// vault_write out, for agents that may only read (ki ask).
func (a *App) vaultMCP(ctx context.Context, readOnly bool) error {
	srv, err := a.vaultServer(ctx, readOnly)
	if err != nil {
		return err
	}
	// The agent ends the session by closing the pipe. The SDK reports that as
	// its internal "server is closing" error (jsonrpc2.ErrServerClosing, not
	// importable) wrapping io.EOF; both are the normal end, not a failure.
	err = srv.Run(ctx, &mcp.StdioTransport{})
	if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "server is closing") {
		return fmt.Errorf("vault server: %w", err)
	}
	return nil
}

// vaultTools is the state behind the vault_* tools.
type vaultTools struct {
	app *App
	dir string
	now func() time.Time
	log *slog.Logger
}

// vaultServer builds the MCP server with the vault tools registered. The
// vault must exist as a clone; the per-call checks (identity, origin, rebase)
// run in vaultPreflight at write time, so a session that opens in a repo whose
// identity may not reach the vault gets a clear refusal instead of a dead server.
func (a *App) vaultServer(ctx context.Context, readOnly bool) (*mcp.Server, error) {
	dir := a.Getenv("KI_VAULT")
	if dir == "" {
		return nil, fail("KI_VAULT is not set; see docs/laptop-setup.md")
	}
	if git(ctx, dir, "rev-parse", "--is-inside-work-tree") != "true" {
		return nil, fail("KI_VAULT=%s is not a git repo; clone the vault repo there (docs/laptop-setup.md)", dir)
	}
	t := &vaultTools{app: a, dir: dir, now: time.Now, log: slog.New(slog.NewTextHandler(a.Stderr, nil))}
	instructions := "The user's Obsidian vault. Save documentation, decisions, runbooks, incident and meeting notes with vault_write; " +
		"it places the note, adds frontmatter, updates the folder note and pushes. Never write vault files by hand. " +
		"Notes are named in Title Case and names are unique across the vault."
	if readOnly {
		instructions = "The user's Obsidian vault, read-only: find notes with vault_search and vault_list, read them with vault_read. " +
			"Notes are named in Title Case and names are unique across the vault."
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "ki-vault", Version: a.Version}, &mcp.ServerOptions{Instructions: instructions})
	if !readOnly {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "vault_write",
			Description: "Write a note into the vault where it belongs and push it. area projects needs project; kind is note (default), " +
				"decision, incident, runbook or meeting. Refuses secrets and existing names unless overwrite is set.",
		}, t.write)
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "vault_read",
		Description: "Read a note by its name (the file name without .md).",
	}, t.read)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "vault_list",
		Description: "List the folders and notes directly under a vault folder; empty folder lists the root.",
	}, t.list)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "vault_search",
		Description: "Find notes whose name or text contains a phrase (plain text, case-insensitive). For meaning, use the knowledge base instead.",
	}, t.search)
	return srv, nil
}

// VaultWriteInput is the vault_write tool input.
type VaultWriteInput struct {
	Area      string `json:"area" jsonschema:"projects, engineering, reference, writing or ideas"`
	Project   string `json:"project,omitempty" jsonschema:"folder under projects/, e.g. acme; required with area projects"`
	Kind      string `json:"kind,omitempty" jsonschema:"note (default), decision, incident, runbook or meeting; kinds other than note exist only under projects"`
	Title     string `json:"title" jsonschema:"note name in Title Case, without a date prefix or .md"`
	Body      string `json:"body" jsonschema:"markdown body without frontmatter or an H1; wikilinks [[Note Name]] are fine"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"replace an existing note of this name at the same place"`
}

// VaultReadInput is the vault_read tool input.
type VaultReadInput struct {
	Title string `json:"title" jsonschema:"note name without .md"`
}

// VaultListInput is the vault_list tool input.
type VaultListInput struct {
	Folder string `json:"folder,omitempty" jsonschema:"vault-relative folder, e.g. projects/acme; empty for the root"`
}

// VaultSearchInput is the vault_search tool input.
type VaultSearchInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty" jsonschema:"max notes, default 20"`
}

func (t *vaultTools) write(ctx context.Context, _ *mcp.CallToolRequest, in VaultWriteInput) (*mcp.CallToolResult, any, error) {
	clone, err := t.app.vaultPreflight(ctx)
	if err != nil {
		return nil, nil, err
	}
	req := vault.Request{Area: in.Area, Project: in.Project, Kind: in.Kind, Title: in.Title, Body: in.Body, Overwrite: in.Overwrite}
	now := t.now()
	p, err := vault.Place(req, now)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_write: %w", err)
	}
	if line := vault.SecretLine(in.Title + "\n" + in.Body); line > 0 {
		return nil, nil, fmt.Errorf("vault_write: line %d of the note looks like a credential; secrets live in your secrets manager, write `see secrets manager: <item>` instead", line-1)
	}
	if !vault.SafeRel(p.Rel) {
		return nil, nil, fmt.Errorf("vault_write: %q is not a path a tool may write", p.Rel)
	}
	existing, err := vault.Find(t.dir, p.Stem)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_write: %w", err)
	}
	for _, n := range existing {
		if n.Rel != p.Rel {
			return nil, nil, fmt.Errorf("vault_write: a note named %q already exists at %s; note names are unique, pick another title", p.Stem, n.Rel)
		}
		if !in.Overwrite {
			return nil, nil, fmt.Errorf("vault_write: %s exists; pass overwrite to replace it", n.Rel)
		}
	}
	kind := orDefault(in.Kind, vault.KindNote)
	tmpl, err := readOptional(filepath.Join(t.dir, "templates", kind+".md"))
	if err != nil {
		return nil, nil, fmt.Errorf("vault_write: %w", err)
	}
	content, err := vault.Render(tmpl, req, p, now)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_write: %w", err)
	}
	full := filepath.Join(t.dir, filepath.FromSlash(p.Rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return nil, nil, fmt.Errorf("vault_write: creating %s: %w", filepath.Dir(p.Rel), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil { // #nosec G306 -- a note, shared with Obsidian and git like every other
		return nil, nil, fmt.Errorf("vault_write: writing %s: %w", p.Rel, err)
	}
	lines := []string{"wrote " + p.Rel}
	if p.Folder != "" {
		if fn := vault.FolderNote(t.dir, p.Folder); fn != "" {
			fnPath := filepath.Join(t.dir, filepath.FromSlash(fn))
			current, err := readOptional(fnPath)
			if err != nil {
				return nil, nil, fmt.Errorf("vault_write: %w", err)
			}
			if updated, changed := vault.AddToFolderNote(current, p.Stem, kind); changed {
				if err := writeFile(fnPath, updated, 0o644); err != nil {
					return nil, nil, fmt.Errorf("vault_write: %w", err)
				}
				lines = append(lines, "listed in "+fn)
			}
		}
	}
	synced, err := t.app.vaultSync(ctx, clone, kind+": "+strings.TrimSpace(in.Title))
	lines = append(lines, synced...)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_write: the note is written but not pushed: %w", err)
	}
	t.log.Info("vault write", "path", p.Rel)
	return textResult(strings.Join(lines, "\n")), nil, nil
}

func (t *vaultTools) read(_ context.Context, _ *mcp.CallToolRequest, in VaultReadInput) (*mcp.CallToolResult, any, error) {
	stem := strings.TrimSuffix(strings.TrimSpace(in.Title), ".md")
	if stem == "" {
		return nil, nil, errors.New("vault_read: title is required")
	}
	found, err := vault.Find(t.dir, stem)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_read: %w", err)
	}
	switch len(found) {
	case 0:
		return nil, nil, fmt.Errorf("vault_read: no note named %q", stem)
	case 1:
	default:
		paths := make([]string, 0, len(found))
		for _, n := range found {
			paths = append(paths, n.Rel)
		}
		return nil, nil, fmt.Errorf("vault_read: %q exists more than once (%s); names should be unique, ask the user which", stem, strings.Join(paths, ", "))
	}
	data, err := os.ReadFile(filepath.Join(t.dir, filepath.FromSlash(found[0].Rel))) // #nosec G304 -- found by walking the vault
	if err != nil {
		return nil, nil, fmt.Errorf("vault_read: %w", err)
	}
	text := string(data)
	if len(text) > maxNoteRead {
		text = text[:maxNoteRead] + "\n[truncated]\n"
	}
	return textResult("# " + found[0].Rel + "\n\n" + text), nil, nil
}

func (t *vaultTools) list(_ context.Context, _ *mcp.CallToolRequest, in VaultListInput) (*mcp.CallToolResult, any, error) {
	folder := strings.Trim(strings.TrimSpace(in.Folder), "/")
	entries, err := vault.List(t.dir, folder)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_list: %w", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintf(&b, "%s/\n", e.Name)
		} else {
			fmt.Fprintf(&b, "%s\n", e.Name)
		}
	}
	if b.Len() == 0 {
		b.WriteString("(empty)\n")
	}
	return textResult(b.String()), nil, nil
}

func (t *vaultTools) search(_ context.Context, _ *mcp.CallToolRequest, in VaultSearchInput) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	hits, err := vault.Search(t.dir, in.Query, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("vault_search: %w", err)
	}
	var b strings.Builder
	for _, h := range hits {
		if h.Line == 0 {
			fmt.Fprintf(&b, "- %s\n", h.Rel)
		} else {
			fmt.Fprintf(&b, "- %s:%d %s\n", h.Rel, h.Line, h.Text)
		}
	}
	if b.Len() == 0 {
		b.WriteString("No notes match.\n")
	}
	return textResult(b.String()), nil, nil
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}
