package ki

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/kislow/kio/internal/redact"
)

// TitleMax is the longest job title, ellipsis included.
const TitleMax = 90

// ResolveTitle is a job's title: the --title flag, else the issue title (set
// only when the issue is the brief), else the brief's first sentence. Empty
// when nothing usable is left.
func ResolveTitle(flag, issueTitle, brief string) string {
	for _, t := range []string{flag, issueTitle} {
		if t = CleanTitle(t); t != "" {
			return t
		}
	}
	return BriefTitle(brief)
}

// CleanTitle redacts credentials, collapses whitespace and caps the text at
// TitleMax on a word boundary.
func CleanTitle(s string) string {
	s = strings.Join(strings.Fields(redact.Redact(s)), " ")
	r := []rune(s)
	if len(r) <= TitleMax {
		return s
	}
	cut := r[:TitleMax-len("...")]
	if i := lastSpace(cut); i > 0 && r[len(cut)] != ' ' {
		cut = cut[:i]
	}
	return strings.TrimRight(string(cut), " ,;:-") + "..."
}

func lastSpace(r []rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == ' ' {
			return i
		}
	}
	return -1
}

// BriefTitle is the first sentence of a brief's first non-empty line, without
// a leading "Repo: ... ." clause or markdown marks.
func BriefTitle(brief string) string {
	line := ""
	for _, l := range strings.Split(brief, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = redact.Redact(l)
			break
		}
	}
	line = trimMarkdown(line)
	if hasFoldPrefix(line, "Repo:") || hasFoldPrefix(line, "Repository:") {
		_, line = cutSentence(line)
		line = trimMarkdown(line)
	}
	first, _ := cutSentence(line)
	return CleanTitle(strings.TrimRight(first, ". "))
}

// BriefTitleFile is BriefTitle of a job's brief.md, "" when it cannot be read.
func BriefTitleFile(jobsDir, id string) string {
	if !ValidJobID(id) {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(jobsDir, id, "brief.md")) // #nosec G304 -- id is a valid job id under the jobs folder
	if err != nil {
		return ""
	}
	return BriefTitle(string(data))
}

func hasFoldPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func trimMarkdown(s string) string {
	return strings.TrimLeft(s, "#-*> \t")
}

// cutSentence splits s at the first ". " (or a final ".") outside parentheses,
// so a clause like "(this worktree). " ends after the closing bracket.
func cutSentence(s string) (first, rest string) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '.':
			if depth == 0 && (i+1 == len(s) || s[i+1] == ' ') {
				return s[:i], strings.TrimSpace(s[i+1:])
			}
		}
	}
	return s, ""
}
