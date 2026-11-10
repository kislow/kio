package ki

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// AgentWorkingLabel is the claim: a delegated job puts it on an issue while it
// runs, and nothing else about the job is written to GitHub. A crashed job
// leaves it; finish takes it off.
const AgentWorkingLabel = "agent: working"

// IssueRef is a GitHub issue, as --issue prints it: owner/repo#N.
type IssueRef struct {
	Owner  string
	Repo   string
	Number int
}

func (r IssueRef) String() string {
	return fmt.Sprintf("%s/%s#%d", r.Owner, r.Repo, r.Number)
}

// issueArg matches "217" or "owner/repo#217".
var issueArg = regexp.MustCompile(`^(?:([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+)#)?([1-9][0-9]*)$`)

// pullArg matches a pull request URL, which --issue cannot claim.
var pullArg = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/pull/([1-9][0-9]*)(?:[/?#].*)?$`)

// PullRequestRefusal is what --issue says for a number that is a pull request.
func PullRequestRefusal(n int) string {
	return fmt.Sprintf("#%d is a pull request; use --task with a brief for it", n)
}

// ParseIssueArg reads --issue. A bare number uses owner and repo from the
// checkout's origin; owner/repo#N stands alone.
func ParseIssueArg(arg, owner, repo string) (IssueRef, error) {
	arg = strings.TrimSpace(arg)
	if m := pullArg.FindStringSubmatch(arg); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return IssueRef{}, errors.New(PullRequestRefusal(n))
		}
	}
	m := issueArg.FindStringSubmatch(arg)
	if m == nil {
		return IssueRef{}, fmt.Errorf("invalid issue %q: use a number or owner/repo#number", arg)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return IssueRef{}, fmt.Errorf("invalid issue %q: use a number or owner/repo#number", arg)
	}
	if m[1] != "" {
		return IssueRef{Owner: m[1], Repo: m[2], Number: n}, nil
	}
	if owner == "" || repo == "" {
		return IssueRef{}, fmt.Errorf("issue %s: this checkout has no GitHub origin; pass --issue owner/repo#%s", arg, arg)
	}
	return IssueRef{Owner: owner, Repo: repo, Number: n}, nil
}

// Refusal is the error a second machine sees. Who holds the claim is not on
// the issue; `ki delegate ls` on that machine says.
func Refusal(ref IssueRef) string {
	return fmt.Sprintf("issue %s is claimed (%s label); pass --steal to take it over", ref, AgentWorkingLabel)
}

// IssueBrief is the worker's task when --task is not given: the issue itself.
func IssueBrief(ref IssueRef, title, body string) string {
	return fmt.Sprintf("Issue %s: %s\n\n%s", ref, strings.TrimSpace(title), strings.TrimSpace(body))
}
