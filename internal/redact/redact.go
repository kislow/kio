// Package redact masks credential-shaped values in text. Its matching follows
// Python's re semantics, so every caller redacts exactly what the earlier
// Python tooling did.
package redact

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Redacted replaces every credential-shaped value.
const Redacted = "[REDACTED-SECRET]"

// Python's \s and \w are Unicode-aware; RE2's are ASCII. These classes are
// Python's sets, so a pattern matches exactly where the Python one did.
const (
	Space = `\x{09}-\x{0d}\x{1c}-\x{20}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	Word  = `\pL\pN_`
)

// FoldI makes (?i) match i as Python does, which also folds İ and ı.
func FoldI(s string) string { return strings.ReplaceAll(s, "i", "[iİı]") }

// guarded is a pattern whose leading \b or lookbehind RE2 cannot express:
// ok decides from the character before a match (-1 at the start) whether
// Python would have matched there.
type guarded struct {
	re *regexp.Regexp
	ok func(prev rune) bool
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// afterNonWord is a leading \b before a word character.
func afterNonWord(prev rune) bool { return prev < 0 || !isWord(prev) }

func always(rune) bool { return true }

// replace is re.sub: matches are tried left to right on the original text,
// and a rejected start moves on by one character, not past the match.
func (g guarded) replace(s string, repl func(m []string) string) string {
	var b strings.Builder
	last, pos := 0, 0
	for pos < len(s) {
		loc := g.re.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		prev := rune(-1)
		if start > 0 {
			prev, _ = utf8.DecodeLastRuneInString(s[:start])
		}
		if !g.ok(prev) {
			_, size := utf8.DecodeRuneInString(s[start:])
			pos = start + size
			continue
		}
		groups := make([]string, len(loc)/2)
		for i := range groups {
			if loc[2*i] >= 0 {
				groups[i] = s[pos+loc[2*i] : pos+loc[2*i+1]]
			}
		}
		b.WriteString(s[last:start])
		b.WriteString(repl(groups))
		last, pos = end, end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

func mustGuard(pattern string, ok func(rune) bool) guarded {
	return guarded{regexp.MustCompile(pattern), ok}
}

var (
	secrets = []guarded{
		mustGuard(`AKIA[0-9A-Z]{16}`, always),
		mustGuard(`gh[pousr]_[A-Za-z0-9]{30,}`, afterNonWord),
		mustGuard(`github_pat_[A-Za-z0-9_]{40,}`, afterNonWord),
		mustGuard(`xox[abprs]-[A-Za-z0-9-]{10,}`, afterNonWord),
		mustGuard(`sk-(?:ant-)?[A-Za-z0-9_-]{20,}`, afterNonWord),
		mustGuard(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`, always),
	}
	// Not after ":", "-" or "/": that is an ARN (secretsmanager:...:secret:name) or a key such as id-token: write.
	assigned = mustGuard(`(?i)(`+FoldI(`password|passwd|secret|api[_-]?key|token`)+`)([`+Space+`]*[:=][`+Space+`]*)(['"]?)([^`+Space+"'\"`]{8,})",
		func(prev rune) bool { return prev < 0 || !isWord(prev) && prev != ':' && prev != '/' && prev != '-' })
	urlPassword = mustGuard(`([a-z][a-z0-9+.-]*://[^:/`+Space+`@]+:)([^@`+Space+`]+)(@)`, afterNonWord)
)

// Redact masks credential-shaped values: provider tokens, private keys,
// password-like assignments and passwords in URLs.
func Redact(text string) string {
	toRedacted := func([]string) string { return Redacted }
	for _, p := range secrets {
		text = p.replace(text, toRedacted)
	}
	text = assigned.replace(text, func(m []string) string { return m[1] + m[2] + m[3] + Redacted })
	return urlPassword.replace(text, func(m []string) string { return m[1] + Redacted + m[3] })
}
