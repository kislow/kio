package standup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// BoardItem is one card assigned to the user on a project board.
type BoardItem struct {
	Item
	Board  string   `json:"board"`
	Labels []string `json:"labels,omitempty"`
	Status string   `json:"status,omitempty"`
	// StatusChangedAt is when the Status field was last set.
	StatusChangedAt *time.Time `json:"status_changed_at,omitempty"`
	Iteration       string     `json:"iteration,omitempty"`
	// Fields holds the card's other single-select and text values by field
	// name, such as Priority or a blocker reason.
	Fields          map[string]string `json:"fields,omitempty"`
	ChangedInWindow bool              `json:"changed_in_window"`
}

// maxBoardPages bounds how many pages of 100 cards one board may cost.
const maxBoardPages = 5

const projectFields = `projectsV2(query: $title, first: 20) { nodes { id title fields(first: 50) { nodes { __typename ... on ProjectV2FieldCommon { name } } } } }`

var projectQueries = []string{
	`query($owner: String!, $title: String!) { owner: organization(login: $owner) { ` + projectFields + ` } }`,
	`query($owner: String!, $title: String!) { owner: user(login: $owner) { ` + projectFields + ` } }`,
}

const itemsQuery = `
query($id: ID!, $q: String!, $after: String) {
  node(id: $id) {
    ... on ProjectV2 {
      items(first: 100, after: $after, query: $q) {
        pageInfo { hasNextPage endCursor }
        nodes {
          content {
            __typename
            ... on Issue { number title url state updatedAt repository { nameWithOwner } labels(first: 20) { nodes { name } } }
            ... on PullRequest { number title url state isDraft updatedAt repository { nameWithOwner } labels(first: 20) { nodes { name } } }
            ... on DraftIssue { title updatedAt }
          }
          fieldValues(first: 30) {
            nodes {
              __typename
              ... on ProjectV2ItemFieldSingleSelectValue { name updatedAt field { ... on ProjectV2FieldCommon { name } } }
              ... on ProjectV2ItemFieldIterationValue { title field { ... on ProjectV2FieldCommon { name } } }
              ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
            }
          }
        }
      }
    }
  }
}`

type project struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Fields struct {
		Nodes []struct {
			Typename string `json:"__typename"`
			Name     string `json:"name"`
		} `json:"nodes"`
	} `json:"fields"`
}

type fieldValue struct {
	Typename  string     `json:"__typename"`
	Name      string     `json:"name"`
	Title     string     `json:"title"`
	Text      string     `json:"text"`
	UpdatedAt *time.Time `json:"updatedAt"`
	Field     struct {
		Name string `json:"name"`
	} `json:"field"`
}

type itemsData struct {
	Node struct {
		Items struct {
			PageInfo struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []struct {
				Content     node `json:"content"`
				FieldValues struct {
					Nodes []fieldValue `json:"nodes"`
				} `json:"fieldValues"`
			} `json:"nodes"`
		} `json:"items"`
	} `json:"node"`
}

// findProject looks the board up by title under an organization, then a user.
func findProject(ctx context.Context, c *Client, b Board) (*project, error) {
	var lastErr error
	for _, q := range projectQueries {
		var d struct {
			Owner *struct {
				Projects struct {
					Nodes []project `json:"nodes"`
				} `json:"projectsV2"`
			} `json:"owner"`
		}
		if err := c.query(ctx, q, map[string]any{"owner": b.Owner, "title": b.Title}, &d); err != nil {
			lastErr = err
			continue
		}
		if d.Owner == nil {
			continue
		}
		for _, p := range d.Owner.Projects.Nodes {
			if strings.EqualFold(p.Title, b.Title) {
				return &p, nil
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("finding board %q of %s: %w", b.Title, b.Owner, lastErr)
	}
	return nil, fmt.Errorf("board %q not found under %s", b.Title, b.Owner)
}

// boardFilter selects the user's cards in the current iteration, or the cards
// updated in the window when the board has no iteration field.
func boardFilter(p *project, login string, since time.Time) string {
	for _, f := range p.Fields.Nodes {
		if f.Typename == "ProjectV2IterationField" {
			name := f.Name
			if strings.ContainsAny(name, " \t") {
				name = `"` + name + `"`
			}
			return fmt.Sprintf("assignee:%s %s:@current", login, strings.ToLower(name))
		}
	}
	return fmt.Sprintf("assignee:%s updated:>=%s", login, since.Format(time.DateOnly))
}

// fetchBoard returns the user's cards on one board.
func fetchBoard(ctx context.Context, c *Client, b Board, login string, since time.Time) ([]BoardItem, error) {
	p, err := findProject(ctx, c, b)
	if err != nil {
		return nil, err
	}
	vars := map[string]any{"id": p.ID, "q": boardFilter(p, login, since), "after": nil}
	var out []BoardItem
	for range maxBoardPages {
		var d itemsData
		if err := c.query(ctx, itemsQuery, vars, &d); err != nil {
			return nil, fmt.Errorf("reading board %q: %w", b.Title, err)
		}
		for _, n := range d.Node.Items.Nodes {
			if n.Content.Typename == "" { // a card the token cannot see
				continue
			}
			out = append(out, toBoardItem(p.Title, n.Content, n.FieldValues.Nodes, since))
		}
		if !d.Node.Items.PageInfo.HasNextPage {
			return out, nil
		}
		vars["after"] = d.Node.Items.PageInfo.EndCursor
	}
	return out, nil
}

func toBoardItem(board string, content node, values []fieldValue, since time.Time) BoardItem {
	it := toItem(content, content.UpdatedAt)
	if content.Typename == "DraftIssue" {
		it.Kind, it.State = "draft", "open"
	}
	bi := BoardItem{Item: it, Board: board, Fields: map[string]string{}}
	for _, l := range content.Labels.Nodes {
		bi.Labels = append(bi.Labels, l.Name)
	}
	for _, v := range values {
		switch {
		case v.Field.Name == "" || v.Field.Name == "Title":
		case v.Typename == "ProjectV2ItemFieldIterationValue":
			bi.Iteration = v.Title
		case v.Typename == "ProjectV2ItemFieldSingleSelectValue" && strings.EqualFold(v.Field.Name, "Status"):
			bi.Status, bi.StatusChangedAt = v.Name, v.UpdatedAt
			bi.ChangedInWindow = v.UpdatedAt != nil && !v.UpdatedAt.Before(since)
		case v.Typename == "ProjectV2ItemFieldSingleSelectValue":
			bi.Fields[v.Field.Name] = v.Name
		case v.Typename == "ProjectV2ItemFieldTextValue" && v.Text != "":
			bi.Fields[v.Field.Name] = v.Text
		}
	}
	if len(bi.Fields) == 0 {
		bi.Fields = nil
	}
	return bi
}
