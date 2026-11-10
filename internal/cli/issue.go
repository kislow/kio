package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/pkg/projects"
)

// A job that starts reads the issue, adds the agent: working label, and on
// finish removes it. Those are the only GitHub writes: who holds the claim
// lives in the local job ledger, never on the issue. A crash skips finish, so
// the label stays until --steal or someone removes it.

const githubTimeout = 30 * time.Second

const (
	labelColor = "FBCA04"
	labelDesc  = "An agent is working on this issue"
)

var (
	errIssueNotFound = errors.New("issue not found")
	errIsPullRequest = errors.New("number is a pull request")
)

// issuesClient talks to GitHub's GraphQL API. Token is a header only.
type issuesClient struct {
	endpoint string
	token    string
	http     *http.Client
}

func newIssuesClient(endpoint, token string) *issuesClient {
	return &issuesClient{endpoint: endpoint, token: token, http: &http.Client{Timeout: githubTimeout}}
}

func (a *App) issuesClient(ctx context.Context, user string) (*issuesClient, error) {
	tokenFor := a.GitHubToken
	if tokenFor == nil {
		tokenFor = ghToken
	}
	token := tokenFor(ctx, user)
	if token == "" {
		return nil, fail("gh is not logged in as %s; run `gh auth login`", user)
	}
	return newIssuesClient(orDefault(a.Getenv("KI_GITHUB_GRAPHQL_URL"), "https://api.github.com/graphql"), token), nil
}

// issueHold is one claim this process owns. Finish removes its label.
type issueHold struct {
	client  *issuesClient
	ref     ki.IssueRef
	issueID string
	labelID string
	branch  string
	title   string
	body    string
}

type issueSnap struct {
	ref     ki.IssueRef
	repoID  string
	issueID string
	labelID string
	title   string
	body    string
	state   string
	onIssue bool
}

// Claim labels ref. branch, when empty, is name(title) once the issue is read,
// so the worker's branch is named from the title.
func (c *issuesClient) Claim(ctx context.Context, ref ki.IssueRef, branch string, steal bool, name func(title string) string) (issueHold, error) {
	snap, err := c.load(ctx, ref)
	if errors.Is(err, errIsPullRequest) {
		return issueHold{}, fail("%s", ki.PullRequestRefusal(ref.Number))
	}
	if errors.Is(err, errIssueNotFound) {
		return issueHold{}, fail("issue %s was not found", ref)
	}
	if err != nil {
		return issueHold{}, fmt.Errorf("reading %s: %w", ref, err)
	}
	if !strings.EqualFold(snap.state, "OPEN") {
		return issueHold{}, fail("issue %s is closed", ref)
	}
	if snap.onIssue && !steal {
		return issueHold{}, fail("%s", ki.Refusal(ref))
	}
	labelID, err := c.ensureLabel(ctx, snap)
	if err != nil {
		return issueHold{}, fmt.Errorf("adding the %s label on %s: %w", ki.AgentWorkingLabel, ref, err)
	}
	if branch == "" && name != nil {
		branch = name(snap.title)
	}
	vars := map[string]any{"issueId": snap.issueID, "labelId": labelID}
	if err := c.do(ctx, claimMutation, vars, nil); err != nil {
		return issueHold{}, fmt.Errorf("claiming %s: %w", ref, err)
	}
	return issueHold{
		client: c, ref: ref, issueID: snap.issueID, labelID: labelID,
		branch: branch, title: snap.title, body: snap.body,
	}, nil
}

func (c *issuesClient) Finish(ctx context.Context, h issueHold) error {
	vars := map[string]any{"issueId": h.issueID, "labelId": h.labelID}
	if err := c.do(ctx, finishMutation, vars, nil); err != nil {
		return fmt.Errorf("removing the %s label on %s: %w", ki.AgentWorkingLabel, h.ref, err)
	}
	return nil
}

func (c *issuesClient) load(ctx context.Context, ref ki.IssueRef) (issueSnap, error) {
	var resp struct {
		Repository *struct {
			ID    string `json:"id"`
			Label *struct {
				ID string `json:"id"`
			} `json:"label"`
			Issue *struct {
				ID     string `json:"id"`
				Title  string `json:"title"`
				Body   string `json:"body"`
				State  string `json:"state"`
				Labels struct {
					Nodes []struct {
						Name string `json:"name"`
					} `json:"nodes"`
				} `json:"labels"`
			} `json:"issue"`
			// issue(number:) is null for a pull request; this tells the two apart.
			Either *struct {
				Type string `json:"__typename"`
			} `json:"issueOrPullRequest"`
		} `json:"repository"`
	}
	err := c.do(ctx, issueQuery(), map[string]any{"owner": ref.Owner, "name": ref.Repo, "number": ref.Number}, &resp)
	missing := resp.Repository == nil || resp.Repository.Issue == nil
	if missing {
		// A null issue is "not found", including GitHub's "could not resolve" error.
		// A transport failure leaves the body undecoded and must not look like that.
		if err == nil || strings.HasPrefix(err.Error(), "GitHub:") {
			if r := resp.Repository; r != nil && r.Either != nil && r.Either.Type == "PullRequest" {
				return issueSnap{}, errIsPullRequest
			}
			return issueSnap{}, errIssueNotFound
		}
		return issueSnap{}, err
	}
	if err != nil {
		return issueSnap{}, err
	}
	iss := resp.Repository.Issue
	snap := issueSnap{
		ref: ref, repoID: resp.Repository.ID, issueID: iss.ID, title: iss.Title, body: iss.Body,
		state: iss.State,
	}
	if resp.Repository.Label != nil {
		snap.labelID = resp.Repository.Label.ID
	}
	for _, n := range iss.Labels.Nodes {
		if n.Name == ki.AgentWorkingLabel {
			snap.onIssue = true
		}
	}
	return snap, nil
}

func (c *issuesClient) ensureLabel(ctx context.Context, snap issueSnap) (string, error) {
	if snap.labelID != "" {
		return snap.labelID, nil
	}
	var resp struct {
		CreateLabel struct {
			Label struct {
				ID string `json:"id"`
			} `json:"label"`
		} `json:"createLabel"`
	}
	vars := map[string]any{"repoId": snap.repoID, "name": ki.AgentWorkingLabel, "color": labelColor, "description": labelDesc}
	err := c.do(ctx, createLabelMutation, vars, &resp)
	if err == nil && resp.CreateLabel.Label.ID != "" {
		return resp.CreateLabel.Label.ID, nil
	}
	if !alreadyExists(err) {
		if err == nil {
			err = errors.New("empty label id")
		}
		return "", err
	}
	again, loadErr := c.load(ctx, snap.ref)
	if loadErr != nil {
		return "", err
	}
	if again.labelID == "" {
		return "", err
	}
	return again.labelID, nil
}

func alreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already")
}

func issueQuery() string {
	return fmt.Sprintf(`query($owner:String!,$name:String!,$number:Int!){
  repository(owner:$owner,name:$name){
    id
    label(name:%q){id}
    issueOrPullRequest(number:$number){__typename}
    issue(number:$number){
      id title body state
      labels(first:20){nodes{name}}
    }
  }
}`, ki.AgentWorkingLabel)
}

const claimMutation = `mutation($issueId:ID!,$labelId:ID!){
  addLabelsToLabelable(input:{labelableId:$issueId,labelIds:[$labelId]}){clientMutationId}
}`

const createLabelMutation = `mutation($repoId:ID!,$name:String!,$color:String!,$description:String!){
  createLabel(input:{repositoryId:$repoId,name:$name,color:$color,description:$description}){label{id}}
}`

const finishMutation = `mutation($issueId:ID!,$labelId:ID!){
  removeLabelsFromLabelable(input:{labelableId:$issueId,labelIds:[$labelId]}){clientMutationId}
}`

func (c *issuesClient) do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return fmt.Errorf("encoding query: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, githubTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ki-delegate")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling GitHub: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // nothing to recover from a read body
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("reading GitHub response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %s: %s", resp.Status, githubSnippet(raw))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decoding GitHub response: %w", err)
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("decoding GitHub data: %w", err)
		}
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("GitHub: %s", strings.Join(msgs, "; "))
	}
	return nil
}

func githubSnippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// githubRepo splits an origin URL into owner and repo. Empty remote is not an error.
func githubRepo(remote string) (owner, repo string, err error) {
	norm, err := projects.NormalizeRepo(remote)
	if err != nil {
		return "", "", fmt.Errorf("origin %q is not a GitHub repo", remote)
	}
	rest := strings.TrimPrefix(norm, "https://github.com/")
	owner, repo, ok := strings.Cut(rest, "/")
	if !ok || owner == "" || repo == "" {
		return "", "", fmt.Errorf("origin %q is not a GitHub repo", remote)
	}
	return owner, repo, nil
}

// claimIssue claims da.Issue before the worker starts. The brief is the
// issue's title and body when --task was not given.
func (a *App) claimIssue(ctx context.Context, p *prepared, da delegateArgs, id, branch string) (*issueHold, string, error) {
	remote := git(ctx, p.here.root, "remote", "get-url", "origin")
	owner, repo := "", ""
	if remote != "" {
		o, r, err := githubRepo(remote)
		if err != nil && !strings.Contains(da.Issue, "#") {
			return nil, "", fail("%s", err.Error())
		}
		if err == nil {
			owner, repo = o, r
		}
	}
	ref, err := ki.ParseIssueArg(da.Issue, owner, repo)
	if err != nil {
		return nil, "", fail("%s", err.Error())
	}
	if p.session.Identity == nil {
		return nil, "", fail("--issue needs this repo in projects.yaml so ki knows which GitHub account claims it")
	}
	client, err := a.issuesClient(ctx, p.session.Identity.GitHubUser)
	if err != nil {
		return nil, "", err
	}
	name := func(title string) string { return freeBranch(ctx, p.here.root, ki.IssueBranch(ref.Number, title), id) }
	held, err := client.Claim(ctx, ref, branch, da.Steal, name)
	if err != nil {
		var ue UserError
		if errors.As(err, &ue) {
			return nil, "", err
		}
		return nil, "", fmt.Errorf("claiming %s: %w", ref, err)
	}
	return &held, ki.IssueBrief(ref, held.title, held.body), nil
}

// finishIssue removes the label, whatever the outcome. A failure leaves the
// label, the same as a crash.
func (a *App) finishIssue(ctx context.Context, h *issueHold) error {
	if err := h.client.Finish(ctx, *h); err != nil {
		return fail("issue %s: could not remove the %s label: %s", h.ref, ki.AgentWorkingLabel, err.Error())
	}
	return nil
}
