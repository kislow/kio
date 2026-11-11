package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var day = time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)

func TestPlace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		req     Request
		rel     string
		folder  string
		refused bool
	}{
		{"project note", Request{Area: "projects", Project: "acme", Title: "Postgres Failover"}, "projects/acme/Postgres Failover.md", "projects/acme", false},
		{"decision is dated", Request{Area: "projects", Project: "acme", Kind: "decision", Title: "Use Postgres"}, "projects/acme/decisions/2026-10-18 Use Postgres.md", "projects/acme", false},
		{"incident is dated", Request{Area: "projects", Project: "kio", Kind: "incident", Title: "Empty Graph"}, "projects/kio/incidents/2026-10-18 Empty Graph.md", "projects/kio", false},
		{"meeting is dated", Request{Area: "projects", Project: "beacon", Kind: "meeting", Title: "Alpaca"}, "projects/beacon/meetings/2026-10-18 Alpaca.md", "projects/beacon", false},
		{"runbook is not dated", Request{Area: "projects", Project: "acme", Kind: "runbook", Title: "Restore"}, "projects/acme/runbooks/Restore.md", "projects/acme", false},
		{"engineering note", Request{Area: "engineering", Title: "Secrets Handling"}, "engineering/Secrets Handling.md", "", false},
		{"title loses .md", Request{Area: "ideas", Title: "Thing.md"}, "ideas/Thing.md", "", false},
		{"unknown area", Request{Area: "daily", Title: "x"}, "", "", true},
		{"unknown kind", Request{Area: "projects", Project: "p", Kind: "poem", Title: "x"}, "", "", true},
		{"kind outside projects", Request{Area: "writing", Kind: "decision", Title: "x"}, "", "", true},
		{"project outside projects", Request{Area: "reference", Project: "p", Title: "x"}, "", "", true},
		{"bad project name", Request{Area: "projects", Project: "Acme App", Title: "x"}, "", "", true},
		{"empty title", Request{Area: "projects", Project: "p", Title: "  "}, "", "", true},
		{"title with slash", Request{Area: "projects", Project: "p", Title: "a/b"}, "", "", true},
		{"title with hash", Request{Area: "projects", Project: "p", Title: "a#b"}, "", "", true},
		{"dot title", Request{Area: "projects", Project: "p", Title: ".hidden"}, "", "", true},
		{"long title", Request{Area: "projects", Project: "p", Title: strings.Repeat("a", 121)}, "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p, err := Place(c.req, day)
			if c.refused {
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("want refusal, got %v (%+v)", err, p)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p.Rel != c.rel || p.Folder != c.folder || p.Stem != strings.TrimSuffix(filepath.Base(c.rel), ".md") {
				t.Fatalf("got %+v", p)
			}
		})
	}
}

const decisionTemplate = `---
title: {{date:YYYY-MM-DD}} {{title}}
area: projects
project:
type: decision
status: active
created: {{date:YYYY-MM-DD}}
supersedes:
---

## Context

What was the situation.
`

func TestRenderUsesTemplateFrontmatter(t *testing.T) {
	t.Parallel()
	req := Request{Area: "projects", Project: "acme", Kind: "decision", Title: "Use Postgres", Body: "## Decision\n\nPostgres.\n"}
	p, _ := Place(req, day)
	out, err := Render(decisionTemplate, req, p, day)
	if err != nil {
		t.Fatal(err)
	}
	want := "---\ntitle: 2026-10-18 Use Postgres\narea: projects\nproject: acme\ntype: decision\nstatus: active\ncreated: 2026-10-18\nsupersedes:\n---\n\n## Decision\n\nPostgres.\n"
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestRenderMinimalFrontmatterAndBodyRules(t *testing.T) {
	t.Parallel()
	req := Request{Area: "engineering", Title: "Secrets Handling", Body: "# Secrets Handling\n\nUse a secrets manager.\n"}
	p, _ := Place(req, day)
	out, err := Render("", req, p, day)
	if err != nil {
		t.Fatal(err)
	}
	if out != "---\ntitle: Secrets Handling\narea: engineering\ncreated: 2026-10-18\n---\n\nUse a secrets manager.\n" {
		t.Fatalf("got:\n%s", out)
	}
	runbook := Request{Area: "projects", Project: "p", Kind: "runbook", Title: "R", Body: "steps"}
	rp, _ := Place(runbook, day)
	out, _ = Render("", runbook, rp, day)
	if !strings.Contains(out, "type: runbook\n") || !strings.Contains(out, "project: p\n") {
		t.Fatalf("got:\n%s", out)
	}
	for _, body := range []string{"", "   ", "---\ntitle: x\n---\nbody", "# only a heading"} {
		if _, err := Render("", Request{Area: "ideas", Title: "x", Body: body}, Placement{Stem: "x"}, day); !errors.Is(err, ErrRefused) {
			t.Errorf("body %q: want refusal, got %v", body, err)
		}
	}
}

func TestSecretLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text string
		line int
	}{
		{"plain prose about a password policy\n", 0},
		{"token: see secrets manager: mcp-tokens\n", 0},
		{"password: ${DB_PASSWORD}\n", 0},
		{"the api_key is required\n", 0},
		{"ok\n-----BEGIN RSA PRIVATE KEY-----\n", 2},
		{"key AKIAIOSFODNN7EXAMPLE here", 1},
		{"ghp_" + strings.Repeat("a", 36), 1},
		{"xoxb-12345678901-abc", 1},
		{"password = \"hunter2hunter2\"", 1},
		{"\n\nDB_PASSWORD=p4ssw0rdp4ss", 3},
		{"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r", 1},
	}
	for _, c := range cases {
		if got := SecretLine(c.text); got != c.line {
			t.Errorf("%q: got line %d, want %d", c.text, got, c.line)
		}
	}
}

func TestSafeRel(t *testing.T) {
	t.Parallel()
	for rel, ok := range map[string]bool{
		"projects/x/Note.md": true, "engineering/A.md": true,
		"": false, "/abs/x.md": false, "../x.md": false, "projects/../../x.md": false,
		".obsidian/app.json": false, "projects/.trash/x.md": false, ".git/config": false, "projects//x.md": false,
	} {
		if SafeRel(rel) != ok {
			t.Errorf("SafeRel(%q) = %v, want %v", rel, !ok, ok)
		}
	}
}

func TestFolderNoteAndUpdate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("projects/beacon/Beacon.md", "---\ntitle: Beacon\n---\n\nAbout.\n\n## Notes\n\n- [[Backend]]\n\n## Decisions\n\n- \n")
	mk("projects/beacon/Backend.md", "x")
	mk("projects/kio/kio.md", "---\ntitle: kio\n---\n")
	mk("projects/empty/Readme.md", "x")

	if fn := FolderNote(root, "projects/beacon"); fn != "projects/beacon/Beacon.md" {
		t.Fatalf("beacon folder note: %q", fn)
	}
	if fn := FolderNote(root, "projects/kio"); fn != "projects/kio/kio.md" {
		t.Fatalf("kio folder note: %q", fn)
	}
	if fn := FolderNote(root, "projects/empty"); fn != "" {
		t.Fatalf("no folder note expected, got %q", fn)
	}
	if fn := FolderNote(root, "projects/missing"); fn != "" {
		t.Fatalf("missing folder: %q", fn)
	}

	content, _ := os.ReadFile(filepath.Join(root, "projects/beacon/Beacon.md"))
	out, changed := AddToFolderNote(string(content), "Database", KindNote)
	if !changed || !strings.Contains(out, "## Notes\n\n- [[Backend]]\n- [[Database]]\n\n## Decisions") {
		t.Fatalf("note append:\n%s", out)
	}
	out, changed = AddToFolderNote(out, "2026-10-18 Use Postgres", KindDecision)
	if !changed || !strings.HasSuffix(out, "## Decisions\n\n- [[2026-10-18 Use Postgres]]\n") {
		t.Fatalf("decision replaces the empty item:\n%s", out)
	}
	if _, changed := AddToFolderNote(out, "Database", KindNote); changed {
		t.Fatal("linking twice must be a no-op")
	}
	out, changed = AddToFolderNote("---\ntitle: X\n---\n", "First", KindNote)
	if !changed || !strings.HasSuffix(out, "---\n\n## Notes\n\n- [[First]]\n") {
		t.Fatalf("missing section is created:\n%s", out)
	}
	out, _ = AddToFolderNote("", "Only", KindDecision)
	if out != "## Decisions\n\n- [[Only]]\n" {
		t.Fatalf("empty note:\n%q", out)
	}
	out, _ = AddToFolderNote("## Notes\r\n\r\n- [[A]]\r\n\r\n## Other\r\n\r\ntext\r\n", "B", KindNote)
	if !strings.Contains(out, "- [[A]]\n- [[B]]\n\n## Other") {
		t.Fatalf("crlf and following section:\n%s", out)
	}
}

func TestFindListSearch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mk := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("projects/acme/Acme.md", "overview\n")
	mk("projects/acme/runbooks/Restore.md", "## When\n\nAfter a Postgres failover.\n")
	mk("engineering/Restore.md", "duplicate name\n")
	mk(".obsidian/plugins/x/Restore.md", "never\n")
	mk(".trash/Old.md", "postgres\n")
	mk("attachments/img.png", "binary")
	if err := os.Symlink("/etc/hosts", filepath.Join(root, "engineering", "Link.md")); err != nil {
		t.Skip("symlinks unsupported")
	}

	found, err := Find(root, "Restore")
	if err != nil || len(found) != 2 {
		t.Fatalf("find: %v %+v", err, found)
	}
	if found, _ := Find(root, "Link"); len(found) != 0 {
		t.Fatal("symlinks must not be found")
	}

	entries, err := List(root, "")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "attachments,engineering,projects" {
		t.Fatalf("root list: %v", names)
	}
	entries, _ = List(root, "projects/acme")
	if len(entries) != 2 || entries[0].Name != "Acme" || !entries[1].IsDir || entries[1].Name != "runbooks" {
		t.Fatalf("acme list: %+v", entries)
	}
	for _, bad := range []string{".obsidian", "../x", "nope"} {
		if _, err := List(root, bad); !errors.Is(err, ErrRefused) {
			t.Errorf("List(%q): want refusal, got %v", bad, err)
		}
	}

	hits, err := Search(root, "postgres", 20)
	if err != nil || len(hits) != 1 || hits[0].Rel != "projects/acme/runbooks/Restore.md" || hits[0].Line != 3 {
		t.Fatalf("search text: %v %+v", err, hits)
	}
	hits, _ = Search(root, "restore", 20)
	if len(hits) != 2 || hits[0].Line != 0 {
		t.Fatalf("search by name: %+v", hits)
	}
	if hits, _ := Search(root, "restore", 1); len(hits) != 1 {
		t.Fatalf("limit: %+v", hits)
	}
	if _, err := Search(root, " ", 5); !errors.Is(err, ErrRefused) {
		t.Fatal("empty query must be refused")
	}
}

func seedReader(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"projects/acme/Backend.md": "Go and Postgres.\n",
		"projects/other/Plan.md":   "Postgres elsewhere.\n",
		"engineering/Style.md":     "Postgres naming.\n",
		"Home.md":                  "Postgres home.\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReaderConfinesFindListAndSearch(t *testing.T) {
	t.Parallel()
	root := seedReader(t)
	r, err := NewReader(root, []string{"projects/acme", "engineering/"})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := r.Find("Backend"); err != nil || len(n) != 1 {
		t.Fatalf("find inside: %v %v", n, err)
	}
	for _, stem := range []string{"Plan", "Home"} {
		if n, err := r.Find(stem); err != nil || len(n) != 0 {
			t.Errorf("find %q outside: %v %v", stem, n, err)
		}
	}
	hits, err := r.Search("postgres", 10)
	if err != nil || len(hits) != 2 || hits[0].Rel != "engineering/Style.md" || hits[1].Rel != "projects/acme/Backend.md" {
		t.Fatalf("search: %v %v", hits, err)
	}
	if _, err := r.Search(" ", 10); !errors.Is(err, ErrRefused) {
		t.Fatalf("empty query: %v", err)
	}
	root2, err := r.List("")
	if err != nil || len(root2) != 2 || !root2[0].IsDir || !root2[1].IsDir {
		t.Fatalf("root: %v %v", root2, err)
	}
	for _, folder := range []string{"projects/other", "..", "projects/acme/../other"} {
		if _, err := r.List(folder); !errors.Is(err, ErrRefused) {
			t.Errorf("list %q: %v", folder, err)
		}
	}
	if _, err := r.List("engineering/missing"); !errors.Is(err, ErrRefused) {
		t.Errorf("missing folder: %v", err)
	}
}

func TestReaderSkipsAListedFolderThatDoesNotExist(t *testing.T) {
	t.Parallel()
	r, err := NewReader(seedReader(t), []string{"reference", "engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, err := r.Search("postgres", 10); err != nil || len(hits) != 1 {
		t.Fatalf("search: %v %v", hits, err)
	}
}

func TestReaderRefusesALinkedFolderAboveTheListedOnes(t *testing.T) {
	t.Parallel()
	root := seedReader(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	if err := os.Symlink(filepath.Join(root, "projects"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(root, []string{"linked/sub", "alias/acme"})
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"linked", "alias"} {
		if _, err := r.List(folder); !errors.Is(err, ErrRefused) {
			t.Errorf("list %q: %v", folder, err)
		}
	}
}

func TestReaderWithoutAListIsTheWholeVault(t *testing.T) {
	t.Parallel()
	root := seedReader(t)
	for _, allow := range [][]string{nil, {}} {
		r, err := NewReader(root, allow)
		if err != nil {
			t.Fatal(err)
		}
		if hits, err := r.Search("postgres", 10); err != nil || len(hits) != 4 {
			t.Fatalf("search: %v %v", hits, err)
		}
		if es, err := r.List(""); err != nil || len(es) != 3 {
			t.Fatalf("root: %v %v", es, err)
		}
	}
	if hits, err := Search(root, "postgres", 10); err != nil || len(hits) != 4 {
		t.Fatalf("package Search: %v %v", hits, err)
	}
}

func TestNewReaderRefusesPathsOutsideTheVault(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "/etc", "../x", "a/../../x", ".git", "projects/.obsidian", "a//b", "./a"} {
		if _, err := NewReader(t.TempDir(), []string{bad}); !errors.Is(err, ErrRefused) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
