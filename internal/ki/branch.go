package ki

import (
	"fmt"
	"regexp"
	"strings"
)

// A job's branch is named like the repo's own branches, <type>/<issue>-<slug>,
// so nothing that lands on GitHub reads as the harness's.

// commitType is a Conventional Commits prefix on an issue title.
var commitType = regexp.MustCompile(`(?i)^(feat|fix|chore|docs|refactor|ci|perf|test|build)(\([^)]*\))?!?:\s*`)

// slugMax keeps branch names short enough to read in a PR list.
const slugMax = 40

// IssueBranch is <type>/<number>-<slug> for an issue titled title. The type is
// the title's Conventional Commits prefix, chore without one; the slug is the
// rest of the title.
func IssueBranch(number int, title string) string {
	typ, rest := "chore", strings.TrimSpace(title)
	if m := commitType.FindStringSubmatch(rest); m != nil {
		typ, rest = strings.ToLower(m[1]), rest[len(m[0]):]
	}
	name := fmt.Sprintf("%s/%d", typ, number)
	if s := slug(rest); s != "" {
		name += "-" + s
	}
	return name
}

// TaskBranch is the branch of a job that has no issue: chore/, the slug of its
// title and the job id's random part, so jobs started together on one task get
// different branches. The whole id stays out of it, since a branch name reaches GitHub.
func TaskBranch(title, jobID string) string {
	name := "chore/" + orTask(slug(title))
	return name + "-" + jobID[strings.LastIndex(jobID, "-")+1:]
}

func orTask(s string) string {
	if s == "" {
		return "task"
	}
	return s
}

// UniqueBranch is name, or name with the job id's last part when taken reports
// it exists, so a second job on one issue does not fail git worktree add.
func UniqueBranch(name, jobID string, taken func(string) bool) string {
	if !taken(name) {
		return name
	}
	return name + "-" + jobID[strings.LastIndex(jobID, "-")+1:]
}

// slug lowercases s to letters, digits and single hyphens, cut at a word
// boundary under slugMax.
func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) <= slugMax {
		return out
	}
	out = out[:slugMax]
	if i := strings.LastIndex(out, "-"); i > 0 {
		out = out[:i]
	}
	return strings.Trim(out, "-")
}
