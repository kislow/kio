package standup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Item is one pull request or issue and what the user did to it.
type Item struct {
	Kind    string   `json:"kind"` // pr, issue or draft (a board-only note)
	Repo    string   `json:"repo,omitempty"`
	Number  int      `json:"number,omitempty"`
	Title   string   `json:"title"`
	URL     string   `json:"url,omitempty"`
	State   string   `json:"state"` // open, draft, merged or closed
	Actions []string `json:"actions,omitempty"`
	// At is the latest action in the window, or the last update of an open item.
	At time.Time `json:"at"`
}

// activityQuery reads everything through search and the viewer's comments:
// contributionsCollection reports private and enterprise work only as
// restricted counts, so it cannot list what was done.
const activityQuery = `
query($login: String!, $openedPRs: String!, $openedIssues: String!, $reviewed: String!, $merged: String!, $closedPRs: String!, $closedIssues: String!, $openPRs: String!, $openIssues: String!) {
  viewer {
    login
    issueComments(first: 100, orderBy: {field: UPDATED_AT, direction: DESC}) {
      nodes { createdAt issue { ...issue } pullRequest { ...pr } }
    }
  }
  openedPRs: search(query: $openedPRs, type: ISSUE, first: 100) { issueCount nodes { ...pr } }
  openedIssues: search(query: $openedIssues, type: ISSUE, first: 100) { issueCount nodes { ...issue } }
  reviewed: search(query: $reviewed, type: ISSUE, first: 100) {
    issueCount
    nodes { ...pr ... on PullRequest { reviews(last: 20, author: $login) { nodes { state submittedAt } } } }
  }
  merged: search(query: $merged, type: ISSUE, first: 100) { issueCount nodes { ...pr } }
  closedPRs: search(query: $closedPRs, type: ISSUE, first: 100) { issueCount nodes { ...pr } }
  closedIssues: search(query: $closedIssues, type: ISSUE, first: 100) { issueCount nodes { ...issue } }
  openPRs: search(query: $openPRs, type: ISSUE, first: 50) { issueCount nodes { ...pr } }
  openIssues: search(query: $openIssues, type: ISSUE, first: 30) { issueCount nodes { ...issue } }
}
fragment pr on PullRequest { __typename number title url state isDraft createdAt updatedAt mergedAt closedAt repository { nameWithOwner } }
fragment issue on Issue { __typename number title url state createdAt updatedAt closedAt repository { nameWithOwner } }`

// node is a pull request or issue as the queries return it.
type node struct {
	Typename   string     `json:"__typename"`
	Number     int        `json:"number"`
	Title      string     `json:"title"`
	URL        string     `json:"url"`
	State      string     `json:"state"`
	IsDraft    bool       `json:"isDraft"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	MergedAt   *time.Time `json:"mergedAt"`
	ClosedAt   *time.Time `json:"closedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Reviews struct {
		Nodes []struct {
			State       string    `json:"state"`
			SubmittedAt time.Time `json:"submittedAt"`
		} `json:"nodes"`
	} `json:"reviews"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

type search struct {
	IssueCount int    `json:"issueCount"`
	Nodes      []node `json:"nodes"`
}

type activityData struct {
	Viewer struct {
		Login    string `json:"login"`
		Comments struct {
			Nodes []struct {
				CreatedAt   time.Time `json:"createdAt"`
				Issue       *node     `json:"issue"`
				PullRequest *node     `json:"pullRequest"`
			} `json:"nodes"`
		} `json:"issueComments"`
	} `json:"viewer"`
	OpenedPRs    search `json:"openedPRs"`
	OpenedIssues search `json:"openedIssues"`
	Reviewed     search `json:"reviewed"`
	Merged       search `json:"merged"`
	ClosedPRs    search `json:"closedPRs"`
	ClosedIssues search `json:"closedIssues"`
	OpenPRs      search `json:"openPRs"`
	OpenIssues   search `json:"openIssues"`
}

// openLookback limits open items to those updated within this long before the
// window starts: an open PR or assigned issue nobody updated for weeks is not
// today's work. The search qualifier asks for it and fetchActivity enforces it
// again on updatedAt, so a result the search let through still cannot list.
const openLookback = 14 * 24 * time.Hour

// activity is what the activity query found, before scoping.
type activity struct {
	done     []Item
	open     []Item
	warnings []string
}

// fetchActivity reads the user's GitHub activity between since and until. It
// fails when the token belongs to anyone but login.
func fetchActivity(ctx context.Context, c *Client, login string, since, until time.Time) (*activity, error) {
	at := since.UTC().Format("2006-01-02T15:04:05+00:00")
	touched := since.Add(-openLookback).Format(time.DateOnly)
	vars := map[string]any{
		"login":        login,
		"openedPRs":    fmt.Sprintf("is:pr author:%s created:>=%s", login, at),
		"openedIssues": fmt.Sprintf("is:issue author:%s created:>=%s", login, at),
		"reviewed":     fmt.Sprintf("is:pr reviewed-by:%s -author:%s updated:>=%s", login, login, at),
		"merged":       fmt.Sprintf("is:pr author:%s merged:>=%s", login, at),
		"closedPRs":    fmt.Sprintf("is:pr author:%s is:unmerged closed:>=%s", login, at),
		"closedIssues": fmt.Sprintf("is:issue assignee:%s closed:>=%s", login, at),
		"openPRs":      fmt.Sprintf("is:pr is:open author:%s archived:false updated:>=%s sort:updated-desc", login, touched),
		"openIssues":   fmt.Sprintf("is:issue is:open assignee:%s archived:false updated:>=%s sort:updated-desc", login, touched),
	}
	var d activityData
	if err := c.query(ctx, activityQuery, vars, &d); err != nil {
		return nil, fmt.Errorf("reading activity: %w", err)
	}
	if !strings.EqualFold(d.Viewer.Login, login) {
		return nil, fmt.Errorf("%w: the token belongs to %q, not %q", ErrIdentity, d.Viewer.Login, login)
	}

	done := newLedger(since, until)
	for _, n := range slices.Concat(d.OpenedPRs.Nodes, d.OpenedIssues.Nodes) {
		done.add(n, "opened", n.CreatedAt)
	}
	for _, n := range d.Reviewed.Nodes {
		for _, r := range n.Reviews.Nodes {
			done.add(n, reviewAction(r.State), r.SubmittedAt)
		}
	}
	for _, n := range d.Viewer.Comments.Nodes {
		if target := orNode(n.PullRequest, n.Issue); target != nil {
			done.add(*target, "commented", n.CreatedAt)
		}
	}
	for _, n := range d.Merged.Nodes {
		done.add(n, "merged", deref(n.MergedAt))
	}
	for _, n := range slices.Concat(d.ClosedPRs.Nodes, d.ClosedIssues.Nodes) {
		done.add(n, "closed", deref(n.ClosedAt))
	}

	a := &activity{done: done.items()}
	cutoff := since.Add(-openLookback)
	for _, n := range slices.Concat(d.OpenPRs.Nodes, d.OpenIssues.Nodes) {
		if n.UpdatedAt.Before(cutoff) {
			continue
		}
		a.open = append(a.open, toItem(n, n.UpdatedAt))
	}
	for name, s := range map[string]search{
		"opened PRs": d.OpenedPRs, "opened issues": d.OpenedIssues, "reviewed PRs": d.Reviewed,
		"merged PRs": d.Merged, "closed PRs": d.ClosedPRs, "closed issues": d.ClosedIssues,
		"open PRs": d.OpenPRs, "open issues": d.OpenIssues,
	} {
		if s.IssueCount > len(s.Nodes) {
			a.warnings = append(a.warnings, fmt.Sprintf("%d of %d %s shown", len(s.Nodes), s.IssueCount, name))
		}
	}
	slices.Sort(a.warnings)
	return a, nil
}

func reviewAction(state string) string {
	switch state {
	case "APPROVED":
		return "approved"
	case "CHANGES_REQUESTED":
		return "requested changes"
	default:
		return "reviewed"
	}
}

func orNode(a, b *node) *node {
	if a != nil {
		return a
	}
	return b
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// nodeKind trusts the URL over __typename: the URL is what a reader follows,
// and a node whose fragment did not match carries no typename at all.
func nodeKind(n node) string {
	switch {
	case strings.Contains(n.URL, "/pull/"):
		return "pr"
	case strings.Contains(n.URL, "/issues/"):
		return "issue"
	case n.Typename == "PullRequest":
		return "pr"
	}
	return "issue"
}

func toItem(n node, at time.Time) Item {
	kind, state := nodeKind(n), strings.ToLower(n.State)
	if kind == "pr" && n.IsDraft && state == "open" {
		state = "draft"
	}
	return Item{Kind: kind, Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title, URL: n.URL, State: state, At: at}
}

// ledger merges actions on the same item that happened inside the window.
type ledger struct {
	since, until time.Time
	byURL        map[string]*Item
	order        []string
}

func newLedger(since, until time.Time) *ledger {
	return &ledger{since: since, until: until, byURL: map[string]*Item{}}
}

func (l *ledger) add(n node, action string, at time.Time) {
	if n.URL == "" || at.Before(l.since) || at.After(l.until) {
		return
	}
	it, ok := l.byURL[n.URL]
	if !ok {
		item := toItem(n, at)
		it = &item
		l.byURL[n.URL] = it
		l.order = append(l.order, n.URL)
	}
	if !slices.Contains(it.Actions, action) {
		it.Actions = append(it.Actions, action)
	}
	if at.After(it.At) {
		it.At = at
	}
}

// items returns the merged items, most recent first.
func (l *ledger) items() []Item {
	out := make([]Item, 0, len(l.order))
	for _, u := range l.order {
		out = append(out, *l.byURL[u])
	}
	slices.SortStableFunc(out, func(a, b Item) int { return b.At.Compare(a.At) })
	return out
}
