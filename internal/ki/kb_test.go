package ki

import (
	"testing"
	"time"

	"github.com/kislow/kio/pkg/projects"
)

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		in      string
		want    time.Time
		wantErr bool
	}{
		{"36h", now.Add(-36 * time.Hour), false},
		{"7d", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), false},
		{"2w", time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC), false},
		{"2026-11-01", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), false},
		{"2026-11-01T08:30:00+02:00", time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), false},
		{" 7d ", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), false},
		{"", time.Time{}, true},
		{"d", time.Time{}, true},
		{"0d", time.Time{}, true},
		{"-3d", time.Time{}, true},
		{"last week", time.Time{}, true},
		{"7m", time.Time{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSince(tt.in, now)
			if (err != nil) != tt.wantErr || !got.Equal(tt.want) {
				t.Fatalf("ParseSince(%q) = %v, %v", tt.in, got, err)
			}
		})
	}
}

func TestSourceTitle(t *testing.T) {
	for in, want := range map[string]string{
		"acme/decisions/2026-10-24-routing-with-chi.md":   "2026 10 24 routing with chi",
		"vault/projects/kio/runbooks/New Laptop Setup.md": "New Laptop Setup",
		"acme/README.md":               "README",
		"acme/docs/some_note.markdown": "some note",
		"README":                       "README",
		"":                             "",
	} {
		if got := SourceTitle(in); got != want {
			t.Errorf("SourceTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSourceLink(t *testing.T) {
	cfg := &projects.Config{Projects: []projects.Project{
		{Name: "kio", Repo: "https://github.com/acme/kio", Branch: "main"},
		{Name: "vault", Repo: "https://github.com/alice/vault/", Branch: "release/1"},
	}}
	for in, want := range map[string]string{
		"kio/docs/architecture.md":           "https://github.com/acme/kio/blob/main/docs/architecture.md",
		"vault/projects/New Laptop Setup.md": "https://github.com/alice/vault/blob/release%2F1/projects/New%20Laptop%20Setup.md",
		"unknown/docs/a.md":                  "",
		"kio":                                "",
		"kio/":                               "",
	} {
		if got := SourceLink(cfg, in); got != want {
			t.Errorf("SourceLink(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SourceLink(nil, "kio/a.md"); got != "" {
		t.Errorf("without projects: %q", got)
	}
}

func TestSnippet(t *testing.T) {
	for _, tt := range []struct {
		in   string
		max  int
		want string
	}{
		{"short fact", 40, "short fact"},
		{"two\n\nlines   and   gaps", 40, "two lines and gaps"},
		{"abcdefghij", 5, "abcd…"},
		{"ab cdefghij", 4, "ab…"},
		{"ééééé", 3, "éé…"},
	} {
		if got := Snippet(tt.in, tt.max); got != tt.want {
			t.Errorf("Snippet(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
		}
	}
}
