package ki

import (
	"os"
	"regexp"
	"slices"
	"strings"
)

// Text a worker or ki sends to GitHub (branch names, commit messages, pull
// request titles and bodies) must carry no agent attribution and no detail of
// the machine or job that produced it. The patterns live here only: the hooks
// ki gives a worker call `ki delegate guard`, which asks these functions.

// minHostLen keeps a short hostname from matching ordinary words.
const minHostLen = 4

var (
	// jobDetail finds what names a job or its worktree: ki's old branch prefix,
	// the job id shape (any prefix of its hex suffix) and the worktrees folder.
	jobDetail = regexp.MustCompile(`ki/j-|j-20\d{6}-\d{6}-[0-9a-f]{4}|-worktrees/`)

	// attributionTrailer is a co-author trailer that names an agent or a vendor address.
	attributionTrailer = regexp.MustCompile(`(?i)^\s*co-authored-by:.*(\b(claude|cursor|codex|gemini|agy|copilot)\b|@[\w.-]*\b(anthropic|cursor|openai)\.com)`)

	// creditLine opens a "Generated with" or "Made with" line, robot emoji or not.
	creditLine = regexp.MustCompile(`(?i)^\s*(🤖\s*)?(generated|made) with\b`)

	// agentName is an agent or a vendor address; a credit line counts as attribution
	// only with one, so "Generated with mockery" stays an ordinary line.
	agentName = regexp.MustCompile(`(?i)\b(claude|cursor|codex|gemini|agy|copilot)\b|\b(anthropic|cursor|openai)\.com\b`)

	// shortLetters is a name of letters only, too short to tell from a word.
	shortLetters = regexp.MustCompile(`^[a-z]{1,5}$`)
)

// genericHosts are the default names of Linux machines, containers and laptops;
// text about "localhost" or "ubuntu" is not a leak of this machine.
var genericHosts = map[string]bool{
	"localhost": true, "localhost.localdomain": true, "ubuntu": true, "debian": true,
	"runner": true, "server": true, "host": true, "linux": true, "macbook": true, "mac": true,
}

// AttributionLine returns line when it credits an agent, "" otherwise. A human
// co-author trailer is not attribution.
func AttributionLine(line string) string {
	line = strings.TrimRight(line, "\r")
	if attributionTrailer.MatchString(line) || creditLine.MatchString(line) && agentName.MatchString(line) {
		return strings.TrimSpace(line)
	}
	return ""
}

// StripAttribution removes the lines of a commit message that credit an agent
// and the blank lines they leave at its end.
func StripAttribution(msg string) string {
	lines := strings.Split(msg, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if AttributionLine(l) == "" {
			kept = append(kept, l)
		}
	}
	return strings.TrimRight(strings.Join(kept, "\n"), "\r\n\t ") + "\n"
}

// LeakMatch returns the first thing in text that must not reach GitHub, "" for
// ordinary text.
func LeakMatch(text string) string {
	host, _ := os.Hostname()
	return leakMatch(text, host)
}

func leakMatch(text, host string) string {
	for _, l := range strings.Split(text, "\n") {
		if m := AttributionLine(l); m != "" {
			return m
		}
	}
	if m := jobDetail.FindString(text); m != "" {
		return m
	}
	lower := strings.ToLower(text)
	for _, h := range hostnames(host) {
		if wholeToken(h).MatchString(lower) {
			return h
		}
	}
	return ""
}

// wholeToken matches name only when no letter, digit or hyphen touches it, so
// "acme-build-01" is not found in "acme-build-012"; a dot is a boundary.
func wholeToken(name string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^a-z0-9-])` + regexp.QuoteMeta(name) + `(?:$|[^a-z0-9-])`)
}

// hostnames are the full and the short form of host, lowercase, without the
// ones too short or too generic to tell from a word.
func hostnames(host string) []string {
	host = strings.ToLower(strings.TrimSpace(host))
	short, _, _ := strings.Cut(host, ".")
	var out []string
	for _, h := range []string{host, short} {
		if len(h) >= minHostLen && !genericHosts[h] && !shortLetters.MatchString(h) && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}
