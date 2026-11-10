package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/kislow/kio/internal/ki"
)

// issueAPI is the GitHub GraphQL shape ki delegate claims through. It has no
// comment operations: a query that touches comments is an unexpected query, so
// any comment write fails the claim.
type issueAPI struct {
	mu         sync.Mutex
	token      string
	repos      map[string]*ghRepo
	calls      int
	ops        []string
	failFinish int
	// failCreateOnce answers the first createLabel with "already been taken"
	// after creating it, so the client re-reads.
	failCreateOnce bool
}

type ghRepo struct {
	ID     string
	Labels map[string]string
	Issues map[int]*ghIssue
	Pulls  map[int]bool
}

type ghIssue struct {
	ID     string
	Title  string
	Body   string
	State  string
	Labels map[string]string
}

func newFakeGH() *issueAPI {
	return &issueAPI{token: "tok", repos: map[string]*ghRepo{}}
}

func (f *issueAPI) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return srv
}

func (f *issueAPI) addIssue(owner, name string, n int, title, body, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repo(owner, name)
	if state == "" {
		state = "OPEN"
	}
	repo.Issues[n] = &ghIssue{
		ID: fmt.Sprintf("issue-%s-%s-%d", owner, name, n), Title: title, Body: body, State: state,
		Labels: map[string]string{},
	}
}

func (f *issueAPI) addPull(owner, name string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.repo(owner, name).Pulls[n] = true
}

func (f *issueAPI) dropLabel(owner, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.repo(owner, name).Labels, ki.AgentWorkingLabel)
}

func (f *issueAPI) putIssueLabel(owner, name string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repo := f.repo(owner, name)
	iss := repo.Issues[n]
	iss.Labels[ki.AgentWorkingLabel] = repo.Labels[ki.AgentWorkingLabel]
}

func (f *issueAPI) repo(owner, name string) *ghRepo {
	key := owner + "/" + name
	if r, ok := f.repos[key]; ok {
		return r
	}
	r := &ghRepo{
		ID: "repo-" + key, Labels: map[string]string{ki.AgentWorkingLabel: "label-" + key},
		Issues: map[int]*ghIssue{}, Pulls: map[int]bool{},
	}
	f.repos[key] = r
	return r
}

func (f *issueAPI) view(owner, name string, n int) (labels []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for name := range f.repo(owner, name).Issues[n].Labels {
		labels = append(labels, name)
	}
	return labels
}

func (f *issueAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	data, op, err := f.handle(req.Query, req.Variables)
	if op != "" {
		f.ops = append(f.ops, op)
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]string{{"message": err.Error()}},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (f *issueAPI) handle(q string, vars map[string]any) (any, string, error) {
	switch {
	case strings.Contains(q, "addLabelsToLabelable"):
		data, err := f.claim(vars)
		return data, "claim", err
	case strings.Contains(q, "removeLabelsFromLabelable"):
		if f.failFinish > 0 {
			f.failFinish--
			return nil, "finish", fmt.Errorf("temporarily unavailable")
		}
		f.finish(vars)
		return map[string]any{"removeLabelsFromLabelable": map[string]any{"clientMutationId": "ok"}}, "finish", nil
	case strings.Contains(q, "createLabel"):
		id, err := f.createLabel(vars)
		if err != nil {
			return nil, "createLabel", err
		}
		return map[string]any{"createLabel": map[string]any{"label": map[string]string{"id": id}}}, "createLabel", nil
	case strings.Contains(q, "issue(number:") && !strings.Contains(q, "omment"):
		return f.read(vars), "read", nil
	default:
		return nil, "unexpected", fmt.Errorf("unexpected query")
	}
}

func (f *issueAPI) read(vars map[string]any) map[string]any {
	repo := f.repos[varString(vars, "owner")+"/"+varString(vars, "name")]
	if repo == nil {
		return map[string]any{"repository": nil}
	}
	var label any
	if id, ok := repo.Labels[ki.AgentWorkingLabel]; ok {
		label = map[string]string{"id": id}
	}
	n := varInt(vars, "number")
	iss := repo.Issues[n]
	if iss == nil {
		var either any
		if repo.Pulls[n] {
			either = map[string]string{"__typename": "PullRequest"}
		}
		return map[string]any{"repository": map[string]any{"id": repo.ID, "label": label, "issue": nil, "issueOrPullRequest": either}}
	}
	nodes := make([]map[string]string, 0, len(iss.Labels))
	for name, id := range iss.Labels {
		nodes = append(nodes, map[string]string{"name": name, "id": id})
	}
	return map[string]any{"repository": map[string]any{
		"id": repo.ID, "label": label,
		"issue": map[string]any{
			"id": iss.ID, "title": iss.Title, "body": iss.Body, "state": iss.State,
			"labels": map[string]any{"nodes": nodes},
		},
	}}
}

func (f *issueAPI) claim(vars map[string]any) (map[string]any, error) {
	iss, repo := f.issueByID(varString(vars, "issueId"))
	if iss == nil {
		return nil, fmt.Errorf("Could not resolve to an Issue")
	}
	labelID := varString(vars, "labelId")
	for name, got := range repo.Labels {
		if got == labelID {
			iss.Labels[name] = labelID
		}
	}
	return map[string]any{"addLabelsToLabelable": map[string]any{"clientMutationId": "ok"}}, nil
}

func (f *issueAPI) createLabel(vars map[string]any) (string, error) {
	repo := f.repoByID(varString(vars, "repoId"))
	if repo == nil {
		return "", fmt.Errorf("repository not found")
	}
	name := varString(vars, "name")
	if _, ok := repo.Labels[name]; ok || f.failCreateOnce {
		f.failCreateOnce = false
		if _, ok := repo.Labels[name]; !ok {
			repo.Labels[name] = "label-created-" + name
		}
		return "", fmt.Errorf("Name has already been taken")
	}
	id := "label-created-" + name
	repo.Labels[name] = id
	return id, nil
}

func (f *issueAPI) finish(vars map[string]any) {
	iss, _ := f.issueByID(varString(vars, "issueId"))
	if iss == nil {
		return
	}
	labelID := varString(vars, "labelId")
	for name, got := range iss.Labels {
		if got == labelID {
			delete(iss.Labels, name)
		}
	}
}

func (f *issueAPI) issueByID(id string) (*ghIssue, *ghRepo) {
	for _, repo := range f.repos {
		for _, iss := range repo.Issues {
			if iss.ID == id {
				return iss, repo
			}
		}
	}
	return nil, nil
}

func (f *issueAPI) repoByID(id string) *ghRepo {
	for _, repo := range f.repos {
		if repo.ID == id {
			return repo
		}
	}
	return nil
}

func varString(vars map[string]any, key string) string {
	s, _ := vars[key].(string)
	return s
}

func varInt(vars map[string]any, key string) int {
	n, _ := vars[key].(float64)
	return int(n)
}

func testRef() ki.IssueRef { return ki.IssueRef{Owner: "work", Repo: "app", Number: 7} }

func (f *issueAPI) client(t *testing.T) *issuesClient {
	t.Helper()
	return newIssuesClient(f.server(t).URL, f.token)
}

func claim(c *issuesClient, steal bool) (issueHold, error) {
	return c.Claim(context.Background(), testRef(), "ki/j-1", steal, nil)
}

func TestClaimAndFinishAreTwoWrites(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "Fix it", "The tests fail.", "OPEN")
	c := f.client(t)
	held, err := claim(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 1 || labels[0] != ki.AgentWorkingLabel {
		t.Fatalf("labels %v", labels)
	}
	if held.branch != "ki/j-1" || held.title != "Fix it" || held.body != "The tests fail." {
		t.Fatalf("hold %+v", held)
	}
	if err := c.Finish(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 0 {
		t.Fatalf("label still on: %v", labels)
	}
	if got := strings.Join(f.ops, ","); got != "read,claim,finish" {
		t.Fatalf("calls %v (n=%d); the only writes are claim and finish", f.ops, f.calls)
	}
}

func TestClaimNamesTheBranchFromTheTitle(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "fix: it", "B", "OPEN")
	held, err := f.client(t).Claim(context.Background(), testRef(), "", false, func(title string) string { return "fix/" + title })
	if err != nil || held.branch != "fix/fix: it" {
		t.Fatalf("got %+v %v", held, err)
	}
}

func TestClaimCreatesTheLabel(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "Fix it", "body", "OPEN")
	f.dropLabel("work", "app")
	if _, err := claim(f.client(t), false); err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 1 || labels[0] != ki.AgentWorkingLabel {
		t.Fatalf("labels %v", labels)
	}
	if strings.Join(f.ops, ",") != "read,createLabel,claim" {
		t.Fatalf("ops %v", f.ops)
	}
}

func TestClaimReloadsWhenTheLabelAlreadyExists(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "Fix it", "body", "OPEN")
	f.dropLabel("work", "app")
	f.failCreateOnce = true
	if _, err := claim(f.client(t), false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.ops, ",") != "read,createLabel,read,claim" {
		t.Fatalf("ops %v", f.ops)
	}
}

func TestClaimRefusesMissingClosedAndHeld(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state string
		pull  bool
		label bool
		want  string
	}{
		{name: "missing", want: "was not found"},
		{name: "pull request", pull: true, want: "#7 is a pull request; use --task with a brief for it"},
		{name: "closed", state: "CLOSED", want: "is closed"},
		{name: "labelled", state: "OPEN", label: true, want: "is claimed (agent: working label); pass --steal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeGH()
			if tt.state != "" {
				f.addIssue("work", "app", 7, "T", "B", tt.state)
			}
			if tt.pull {
				f.addPull("work", "app", 7)
			}
			if tt.label {
				f.putIssueLabel("work", "app", 7)
			}
			_, err := claim(f.client(t), false)
			if !isUserError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v", err)
			}
			for _, op := range f.ops {
				if op == "claim" || op == "finish" {
					t.Fatalf("a refused claim wrote: %v", f.ops)
				}
			}
		})
	}
}

func TestCrashedClaimStaysUntilSteal(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "T", "B", "OPEN")
	c := f.client(t)
	if _, err := claim(c, false); err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 1 {
		t.Fatalf("a claim with no finish leaves the label: %v", labels)
	}
	if _, err := claim(c, false); !isUserError(err) {
		t.Fatalf("second claim: %v", err)
	}
	held, err := claim(c, true)
	if err != nil {
		t.Fatalf("steal: %v", err)
	}
	if err := c.Finish(context.Background(), held); err != nil {
		t.Fatal(err)
	}
	if labels := f.view("work", "app", 7); len(labels) != 0 {
		t.Fatalf("label after steal finished: %v", labels)
	}
}

func TestFinishFailureKeepsTheLabel(t *testing.T) {
	t.Parallel()
	f := newFakeGH()
	f.addIssue("work", "app", 7, "T", "B", "OPEN")
	f.failFinish = 1
	c := f.client(t)
	held, err := claim(c, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Finish(context.Background(), held); err == nil {
		t.Fatal("want an error")
	}
	if labels := f.view("work", "app", 7); len(labels) != 1 {
		t.Fatalf("labels %v", labels)
	}
}

func TestGitHubErrorDoesNotEchoTheToken(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	c := newIssuesClient(srv.URL, "super-secret-token")
	_, err := claim(c, false)
	if err == nil || strings.Contains(err.Error(), "super-secret-token") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
}
