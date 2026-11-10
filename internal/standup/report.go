package standup

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// Report is everything one identity's standup is drafted from.
type Report struct {
	Identity   string      `json:"identity"`
	GitHubUser string      `json:"github_user"`
	Since      time.Time   `json:"since"`
	Until      time.Time   `json:"until"`
	Done       []Item      `json:"done"`
	Open       []Item      `json:"open"`
	Board      []BoardItem `json:"board"`
	VaultNotes []Note      `json:"vault_notes"`
	// Dropped counts items from repos that belong to other identities.
	Dropped  int      `json:"dropped"`
	Warnings []string `json:"warnings"`
}

// Collect reads the activity, boards and vault notes of the scope's identity.
// Only the activity is required; a board or vault that cannot be read becomes
// a warning.
func Collect(ctx context.Context, c *Client, s Scope, vault string, since, until time.Time) (*Report, error) {
	login := s.Identity.GitHubUser
	act, err := fetchActivity(ctx, c, login, since, until)
	if err != nil {
		return nil, err
	}
	r := &Report{
		Identity: s.Identity.Name, GitHubUser: login, Since: since, Until: until,
		Done: []Item{}, Open: []Item{}, Board: []BoardItem{}, VaultNotes: []Note{},
		Warnings: append([]string{}, act.warnings...),
	}
	r.Done = r.keep(s, act.done)
	r.Open = r.keep(s, act.open)

	for _, b := range s.Boards {
		items, err := fetchBoard(ctx, c, b, login, since)
		if err != nil {
			r.Warnings = append(r.Warnings, fmt.Sprintf("board %q skipped: %v", b.Title, err))
			continue
		}
		for _, it := range items {
			if it.Repo != "" && !s.Allows(it.Repo) {
				r.Dropped++
				continue
			}
			r.Board = append(r.Board, it)
		}
	}

	switch {
	case vault == "": // no vault is a configuration, not a problem
	case !vaultUsable(vault):
		r.Warnings = append(r.Warnings, fmt.Sprintf("KI_VAULT %s is not a directory; no vault notes", vault))
	default:
		notes, total, err := recentNotes(vault, s.VaultFolders, since)
		switch {
		case err != nil:
			r.Warnings = append(r.Warnings, fmt.Sprintf("vault notes skipped: %v", err))
		case total > len(notes):
			r.Warnings = append(r.Warnings, fmt.Sprintf("%d of %d changed vault notes shown", len(notes), total))
			fallthrough
		case notes != nil:
			r.VaultNotes = notes
		}
	}
	return r, nil
}

// keep drops items from repos that belong to other identities.
func (r *Report) keep(s Scope, items []Item) []Item {
	out := []Item{}
	for _, it := range items {
		if !s.Allows(it.Repo) {
			r.Dropped++
			continue
		}
		out = append(out, it)
	}
	return out
}

// WriteText prints the report for a person; skills use the JSON form.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s) since %s\n", r.Identity, r.GitHubUser, r.Since.Format("Mon 2006-01-02 15:04"))
	section(&b, "Done", len(r.Done))
	for _, it := range r.Done {
		fmt.Fprintf(&b, "  %-5s %-24s %s  %s\n", it.Kind, strings.Join(it.Actions, ","), ref(it), it.Title)
	}
	section(&b, "Open", len(r.Open))
	for _, it := range r.Open {
		fmt.Fprintf(&b, "  %-5s %-24s %s  %s\n", it.Kind, it.State, ref(it), it.Title)
	}
	section(&b, "Board", len(r.Board))
	for _, it := range r.Board {
		fmt.Fprintf(&b, "  %-5s %-24s %s  %s\n", it.Kind, it.Status, ref(it.Item), it.Title)
	}
	if len(r.VaultNotes) > 0 {
		section(&b, "Vault notes", len(r.VaultNotes))
		for _, n := range r.VaultNotes {
			fmt.Fprintf(&b, "  %s\n", n.Path)
		}
	}
	for _, msg := range r.Warnings {
		fmt.Fprintf(&b, "warning: %s\n", msg)
	}
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}
	return nil
}

func section(b *strings.Builder, name string, n int) {
	fmt.Fprintf(b, "\n%s (%d)\n", name, n)
}

func ref(it Item) string {
	if it.Repo == "" {
		return "(draft)"
	}
	return fmt.Sprintf("%s#%d", it.Repo, it.Number)
}
