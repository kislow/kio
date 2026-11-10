package ki

import (
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/kislow/kio/pkg/projects"
)

// VoiceBrain is left out of `ki kb --all` unless it is named: it holds how the
// owner writes, which no UI browsing the knowledge base should surface by default.
const VoiceBrain = "voice"

// ParseSince reads --since: a count of hours, days or weeks ("36h", "7d",
// "2w"), a date ("2026-11-01") or an RFC 3339 time. Counts are relative to now.
func ParseSince(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if n := len(v) - 1; n > 0 {
		if unit, ok := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[v[n]]; ok {
			if count, err := strconv.Atoi(v[:n]); err == nil && count > 0 {
				return now.Add(-time.Duration(count) * unit).UTC(), nil
			}
		}
	}
	for _, layout := range []string{time.RFC3339, time.DateOnly} {
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("--since %q: use a count like 36h, 7d or 2w, a date like 2026-11-01 or an RFC 3339 time", v)
}

// SourceTitle names a document by its file: the base name without extension,
// with dashes and underscores read as spaces.
func SourceTitle(source string) string {
	base := path.Base(source)
	base = strings.TrimSuffix(base, path.Ext(base))
	if base == "." || base == "/" || base == "" {
		return source
	}
	return strings.NewReplacer("-", " ", "_", " ").Replace(base)
}

// SourceLink is the GitHub page of a source "<project>/<path>", or "" when the
// project is not in the projects file.
func SourceLink(cfg *projects.Config, source string) string {
	name, rel, ok := strings.Cut(source, "/")
	if !ok || rel == "" || cfg == nil {
		return ""
	}
	for _, p := range cfg.Projects {
		if p.Name != name {
			continue
		}
		segs := strings.Split(rel, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		return strings.TrimRight(p.Repo, "/") + "/blob/" + url.PathEscape(p.Branch) + "/" + strings.Join(segs, "/")
	}
	return ""
}

// Snippet is a fact on one line, cut to max characters with an ellipsis.
func Snippet(fact string, max int) string {
	s := strings.Join(strings.Fields(fact), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRight(string(r[:max-1]), " ") + "…"
}
