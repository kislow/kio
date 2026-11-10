// Package vault holds the side-effect-light logic behind `ki vault mcp`: where
// a note goes, what its frontmatter is, how the folder note is updated, and
// what must never be written. The conventions come from the vault's own
// AGENTS.md and templates/, read at call time, so changing them needs no
// release. Nothing here runs git; the cli package does that around these calls.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// Kinds of note. Dated kinds carry the date in their file name.
const (
	KindNote     = "note"
	KindDecision = "decision"
	KindIncident = "incident"
	KindRunbook  = "runbook"
	KindMeeting  = "meeting"
)

// Areas are the top-level folders an agent may write into. Everything else
// (inbox, daily, people, career, archive, diagrams, attachments) is the
// owner's by hand.
const (
	AreaProjects    = "projects"
	AreaEngineering = "engineering"
	AreaReference   = "reference"
	AreaWriting     = "writing"
	AreaIdeas       = "ideas"
)

// Folders no tool may touch.
var forbidden = []string{".obsidian", ".trash", ".git"}

// kindFolders is the subfolder of a project each kind lives in.
var kindFolders = map[string]string{
	KindDecision: "decisions",
	KindIncident: "incidents",
	KindRunbook:  "runbooks",
	KindMeeting:  "meetings",
}

// datedKinds get a `YYYY-MM-DD ` prefix, like the vault's templates name them.
var datedKinds = map[string]bool{KindDecision: true, KindIncident: true, KindMeeting: true}

// ErrRefused is wrapped by every refusal: a bad request, not a broken vault.
var ErrRefused = errors.New("refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// Request is one vault_write call.
type Request struct {
	Area      string
	Project   string
	Kind      string
	Title     string
	Body      string
	Overwrite bool
}

// Placement is where a note goes and what it is called.
type Placement struct {
	// Rel is the vault-relative path, e.g. "projects/acme/decisions/2026-10-18 Use Postgres.md".
	Rel string
	// Stem is the note name without ".md": what wikilinks use.
	Stem string
	// Folder is the vault-relative folder whose folder note lists the note; "" when none applies.
	Folder string
}

var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Place decides the path for a request on a given day.
func Place(r Request, now time.Time) (Placement, error) {
	title, err := cleanTitle(r.Title)
	if err != nil {
		return Placement{}, err
	}
	kind := r.Kind
	if kind == "" {
		kind = KindNote
	}
	if kind != KindNote {
		if _, ok := kindFolders[kind]; !ok {
			return Placement{}, refuse("unknown kind %q (note, decision, incident, runbook, meeting)", kind)
		}
	}
	stem := title
	if datedKinds[kind] {
		stem = now.Format("2006-01-02") + " " + title
	}
	switch r.Area {
	case AreaProjects:
		if !projectName.MatchString(r.Project) {
			return Placement{}, refuse("project must be a lowercase folder name like acme or kio, got %q", r.Project)
		}
		folder := path.Join(AreaProjects, r.Project)
		dir := folder
		if sub := kindFolders[kind]; sub != "" {
			dir = path.Join(folder, sub)
		}
		return Placement{Rel: path.Join(dir, stem+".md"), Stem: stem, Folder: folder}, nil
	case AreaEngineering, AreaReference, AreaWriting, AreaIdeas:
		if kind != KindNote {
			return Placement{}, refuse("kind %q only exists under projects; %s takes plain notes", kind, r.Area)
		}
		if r.Project != "" {
			return Placement{}, refuse("project is only used with area projects")
		}
		return Placement{Rel: path.Join(r.Area, stem+".md"), Stem: stem}, nil
	default:
		return Placement{}, refuse("unknown area %q (projects, engineering, reference, writing, ideas)", r.Area)
	}
}

// cleanTitle keeps a title that is safe as a file name and readable in Obsidian.
func cleanTitle(t string) (string, error) {
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(t, ".md")
	if t == "" {
		return "", refuse("title is required")
	}
	if len(t) > 120 {
		return "", refuse("title is longer than 120 characters")
	}
	if strings.HasPrefix(t, ".") {
		return "", refuse("title must not start with a dot")
	}
	for _, r := range t {
		if strings.ContainsRune(`/\:*?"<>|#^[]`, r) || unicode.IsControl(r) {
			return "", refuse("title must not contain %q", string(r))
		}
	}
	return t, nil
}

// Render builds the note: frontmatter from the kind's template when the vault
// has one, else the minimal block, followed by the body. A leading H1 in the
// body is dropped: the file name is the title.
func Render(tmpl string, r Request, p Placement, now time.Time) (string, error) {
	body := strings.TrimSpace(r.Body)
	if body == "" {
		return "", refuse("body is required")
	}
	if strings.HasPrefix(body, "---\n") || body == "---" {
		return "", refuse("body must not start with frontmatter; the tool adds it")
	}
	if strings.HasPrefix(body, "# ") {
		_, rest, _ := strings.Cut(body, "\n")
		body = strings.TrimSpace(rest)
		if body == "" {
			return "", refuse("body holds only a heading")
		}
	}
	fm := frontmatter(tmpl, r, p, now)
	return fm + "\n" + body + "\n", nil
}

// frontmatter is the template's block with its placeholders filled, or the minimal block.
func frontmatter(tmpl string, r Request, p Placement, now time.Time) string {
	date := now.Format("2006-01-02")
	if block, ok := templateFrontmatter(tmpl); ok {
		block = strings.ReplaceAll(block, "{{date:YYYY-MM-DD}}", date)
		block = strings.ReplaceAll(block, "{{title}}", strings.TrimPrefix(p.Stem, date+" "))
		lines := strings.Split(block, "\n")
		out := make([]string, 0, len(lines))
		for _, line := range lines {
			switch {
			case line == "project:" && r.Project != "":
				line = "project: " + r.Project
			case strings.HasPrefix(line, "area:"):
				line = "area: " + r.Area
			}
			out = append(out, line)
		}
		return "---\n" + strings.Join(out, "\n") + "\n---\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\ntitle: %s\narea: %s\n", p.Stem, r.Area)
	if r.Project != "" {
		fmt.Fprintf(&b, "project: %s\n", r.Project)
	}
	if kind := r.Kind; kind != "" && kind != KindNote {
		fmt.Fprintf(&b, "type: %s\n", kind)
	}
	fmt.Fprintf(&b, "created: %s\n---\n", date)
	return b.String()
}

// templateFrontmatter returns the lines between the first two "---" fences.
func templateFrontmatter(tmpl string) (string, bool) {
	tmpl = strings.ReplaceAll(tmpl, "\r\n", "\n")
	if !strings.HasPrefix(tmpl, "---\n") {
		return "", false
	}
	rest := tmpl[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// Secret-shaped values, the same families the kb scanner rejects before
// anything reaches a model. A note that trips one of these is refused whole.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |PGP )?PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b|\bgithub_pat_[A-Za-z0-9_]{40,}\b`),
	regexp.MustCompile(`\bxox[abpors]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{32,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|secret|api[_-]?key|token)\s*[:=]\s*['"][^'"\s]{8,}['"]`),
	regexp.MustCompile(`(?i)\b\w*(?:password|passwd|secret|api[_-]?key|token)\w*\s*[:=]\s*(?:[A-Za-z0-9_\-./+=]*\d)[A-Za-z0-9_\-./+=]{7,}`),
}

// unquotedExcluded are the values the unquoted rule must let through:
// placeholders, references and prose.
var unquotedExcluded = regexp.MustCompile(`(?i)\b\w*(?:password|passwd|secret|api[_-]?key|token)\w*\s*[:=]\s*[<$'"{]`)

// SecretLine returns the 1-based line of the first credential-shaped value, or 0.
func SecretLine(text string) int {
	for i, line := range strings.Split(text, "\n") {
		for _, re := range secretPatterns {
			if !re.MatchString(line) {
				continue
			}
			if re == secretPatterns[len(secretPatterns)-1] && unquotedExcluded.MatchString(line) {
				continue
			}
			return i + 1
		}
	}
	return 0
}

// SafeRel reports whether rel stays inside the vault and outside the folders
// no tool may touch.
func SafeRel(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || path.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return false
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != rel {
		return false
	}
	for _, part := range strings.Split(clean, "/") {
		if slices.Contains(forbidden, part) {
			return false
		}
	}
	return true
}

// FolderNote finds the folder note of a project folder: the one note named
// like the folder (projects/beacon/Beacon.md, projects/kio/kio.md).
// It returns "" when the folder has none.
func FolderNote(root, folder string) string {
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(folder)))
	if err != nil {
		return ""
	}
	want := path.Base(folder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if folderKey(strings.TrimSuffix(e.Name(), ".md")) == want {
			return path.Join(folder, e.Name())
		}
	}
	return ""
}

func folderKey(stem string) string {
	return strings.ReplaceAll(strings.ToLower(stem), " ", "-")
}

// AddToFolderNote returns the folder note with a wikilink to stem appended to
// its section (## Decisions for decisions, ## Notes otherwise), creating the
// section at the end when missing. Already-linked notes leave it unchanged.
func AddToFolderNote(content, stem, kind string) (string, bool) {
	link := "[[" + stem + "]]"
	if strings.Contains(content, link) {
		return content, false
	}
	heading := "## Notes"
	if kind == KindDecision {
		heading = "## Decisions"
	}
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == heading {
			start = i
			break
		}
	}
	item := "- " + link
	if start < 0 {
		if len(lines) == 1 && lines[0] == "" {
			return heading + "\n\n" + item + "\n", true
		}
		lines = append(lines, "", heading, "", item)
		return strings.Join(lines, "\n") + "\n", true
	}
	// Insert after the last list item of the section, before the next heading.
	insert := start + 1
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "#") {
			break
		}
		if item := strings.TrimSpace(lines[i]); item == "-" || strings.HasPrefix(item, "- ") {
			insert = i + 1
		} else if item == "" && insert == start+1 {
			insert = i + 1
		}
	}
	// An empty placeholder item ("- ") is replaced rather than kept.
	if insert > start+1 && strings.TrimSpace(lines[insert-1]) == "-" {
		lines[insert-1] = item
	} else {
		lines = slices.Insert(lines, insert, item)
	}
	return strings.Join(lines, "\n") + "\n", true
}

// Note is a vault file found by name.
type Note struct {
	Rel  string
	Stem string
}

// Find returns every note whose name is stem, anywhere in the vault. Names are
// unique by convention, so more than one result is itself a finding.
func Find(root, stem string) ([]Note, error) {
	var found []Note
	err := walk(root, func(rel string, d fs.DirEntry) {
		if !d.IsDir() && strings.TrimSuffix(d.Name(), ".md") == stem && strings.HasSuffix(d.Name(), ".md") {
			found = append(found, Note{Rel: rel, Stem: stem})
		}
	})
	return found, err
}

// Entry is one line of vault_list.
type Entry struct {
	Name  string
	IsDir bool
}

// List returns the folders and notes directly under folder ("" for the root).
func List(root, folder string) ([]Entry, error) {
	if folder != "" && !SafeRel(folder) {
		return nil, refuse("folder %q is outside the vault or not for tools", folder)
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(folder)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("no folder %q in the vault", folder)
	}
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", folder, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if slices.Contains(forbidden, e.Name()) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.IsDir() {
			out = append(out, Entry{Name: e.Name(), IsDir: true})
		} else if strings.HasSuffix(e.Name(), ".md") {
			out = append(out, Entry{Name: strings.TrimSuffix(e.Name(), ".md")})
		}
	}
	return out, nil
}

// Hit is one vault_search result.
type Hit struct {
	Rel  string
	Line int
	Text string
}

// Search returns the notes whose name or text contains query, case-insensitive,
// with the first matching line, at most limit of them.
func Search(root, query string, limit int) ([]Hit, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, refuse("query is required")
	}
	var hits []Hit
	err := walk(root, func(rel string, d fs.DirEntry) {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") || len(hits) >= limit {
			return
		}
		if strings.Contains(strings.ToLower(strings.TrimSuffix(d.Name(), ".md")), q) {
			hits = append(hits, Hit{Rel: rel, Line: 0, Text: "(name)"})
			return
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))) // #nosec G304 -- walked from the vault root
		if err != nil {
			return
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), q) {
				hits = append(hits, Hit{Rel: rel, Line: i + 1, Text: strings.TrimSpace(line)})
				return
			}
		}
	})
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Rel < hits[j].Rel })
	return hits, err
}

// walk visits every entry under root except the forbidden folders, never
// following symlinks.
func walk(root string, visit func(rel string, d fs.DirEntry)) error {
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return fmt.Errorf("relative path of %s: %w", p, rerr)
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() && slices.Contains(forbidden, d.Name()) {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		visit(rel, d)
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking the vault: %w", err)
	}
	return nil
}
