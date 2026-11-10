package ki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBriefTitle(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 30)
	tests := []struct{ name, brief, want string }{
		{"repo clause with remote", "Repo: ~/code/app (remote alice/app). In the Knowledge tab, show tldraw drawings as pictures instead of their raw JSON.\n\nRead first: x. Then y.", "In the Knowledge tab, show tldraw drawings as pictures instead of their raw JSON"},
		{"repo clause with worktree", "Repo: acme/kio (this worktree). Add SVG output to `ki diagram render` so other tools can show drawings.", "Add SVG output to `ki diagram render` so other tools can show drawings"},
		{"repository prefix", "Repository: acme/app. Fix the build. Then more.", "Fix the build"},
		{"paren holds a sentence break", "Repo: acme/app (a. b). Fix it.", "Fix it"},
		{"markdown heading", "## Fix the login redirect. It loops.", "Fix the login redirect"},
		{"markdown bullet and quote", "\n\n> - Fix the login redirect", "Fix the login redirect"},
		{"first non-empty line only", "\n  \nAdd a flag\nSecond line. Third.", "Add a flag"},
		{"no sentence end", "Add the title column", "Add the title column"},
		{"whitespace collapsed", "Add   the\ttitle   column.", "Add the title column"},
		{"single long sentence", long, "word word word word word word word word word word word word word word word word word..."},
		{"empty", "", ""},
		{"blank", " \n\n ", ""},
		{"only a repo line", "Repo: acme/kio (this worktree).", ""},
		{"only a repo line without period", "Repo: acme/kio", ""},
		{"credential is redacted", "Rotate ghp_abcdefghijklmnopqrstuvwxyz0123456789 now. Then deploy.", "Rotate " + "[REDACTED-SECRET]" + " now"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BriefTitle(tc.brief)
			if got != tc.want {
				t.Fatalf("BriefTitle = %q, want %q", got, tc.want)
			}
			if n := len([]rune(got)); n > TitleMax {
				t.Fatalf("%d characters, over %d", n, TitleMax)
			}
		})
	}
}

func TestBriefTitleCapsOnAWordBoundary(t *testing.T) {
	t.Parallel()
	got := BriefTitle(strings.Repeat("abcdefghi ", 20))
	if !strings.HasSuffix(got, "abcdefghi...") || len([]rune(got)) > TitleMax {
		t.Fatalf("got %q (%d)", got, len([]rune(got)))
	}
	got = BriefTitle(strings.Repeat("x", 200))
	if len([]rune(got)) != TitleMax || !strings.HasSuffix(got, "...") {
		t.Fatalf("a word with no break is cut hard: %q", got)
	}
}

func TestResolveTitlePrecedence(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, flag, issue, brief, want string }{
		{"flag wins", "  My   title ", "Issue title", "Brief. More.", "My title"},
		{"issue next", "", "fix(ci): the build fails", "Brief. More.", "fix(ci): the build fails"},
		{"brief last", " ", "", "Brief. More.", "Brief"},
		{"nothing", "", "", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveTitle(tc.flag, tc.issue, tc.brief); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBriefTitleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	id := "j-20261107-053217-b16d"
	if err := os.MkdirAll(filepath.Join(dir, id), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id, "brief.md"), []byte("Do the thing. Then more.\n\nBLOCKED: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BriefTitleFile(dir, id); got != "Do the thing" {
		t.Fatalf("got %q", got)
	}
	for _, bad := range []string{"j-20261107-053217-ffff", "../x", ""} {
		if got := BriefTitleFile(dir, bad); got != "" {
			t.Fatalf("%q gave %q", bad, got)
		}
	}
}
