package standup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	since = time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	until = since.Add(30 * time.Hour)
	in    = since.Add(10 * time.Hour)
	early = since.Add(-48 * time.Hour)
)

func pr(repo string, n int, state string, extra map[string]any) map[string]any {
	m := map[string]any{
		"__typename": "PullRequest", "number": n, "title": "pr " + repo, "state": state, "isDraft": false,
		"url": "https://github.com/" + repo + "/pull/" + strconv.Itoa(n), "updatedAt": in,
		"repository": map[string]any{"nameWithOwner": repo},
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func issue(repo string, n int, state string) map[string]any {
	return map[string]any{
		"__typename": "Issue", "number": n, "title": "issue " + repo, "state": state,
		"url": "https://github.com/" + repo + "/issues/" + strconv.Itoa(n), "updatedAt": in,
		"repository": map[string]any{"nameWithOwner": repo},
	}
}

func withUpdated(m map[string]any, at time.Time) map[string]any {
	m["updatedAt"] = at
	return m
}

func nodes(list ...map[string]any) map[string]any { return map[string]any{"nodes": list} }

func searchOf(total int, list ...map[string]any) map[string]any {
	return map[string]any{"issueCount": total, "nodes": list}
}

func activityFixture(login string) map[string]any {
	return map[string]any{
		"viewer": map[string]any{
			"login": login,
			"issueComments": nodes(
				map[string]any{"createdAt": in.Add(time.Hour), "issue": issue("workorg/infra", 1, "CLOSED"), "pullRequest": pr("workorg/infra", 1, "MERGED", nil)},
				map[string]any{"createdAt": in, "issue": issue("workorg/infra", 6, "OPEN"), "pullRequest": nil},
				map[string]any{"createdAt": early, "issue": issue("workorg/infra", 7, "OPEN"), "pullRequest": nil},
			),
		},
		"openedPRs":    searchOf(1, pr("workorg/infra", 1, "MERGED", map[string]any{"createdAt": in})),
		"openedIssues": searchOf(1, map[string]any{"__typename": "Issue", "number": 2, "title": "new", "state": "OPEN", "url": "https://github.com/workorg/infra/issues/2", "createdAt": in, "repository": map[string]any{"nameWithOwner": "workorg/infra"}}),
		"reviewed": searchOf(4,
			pr("workorg/deploy", 3, "OPEN", reviews("COMMENTED", early, "APPROVED", in)),
			pr("workorg/deploy", 4, "OPEN", reviews("CHANGES_REQUESTED", in, "COMMENTED", in)),
			pr("clientorg/api", 5, "OPEN", reviews("COMMENTED", in)),
			pr("workorg/deploy", 12, "OPEN", reviews("APPROVED", early)),
		),
		"merged":       searchOf(1, pr("workorg/infra", 1, "MERGED", map[string]any{"mergedAt": in.Add(2 * time.Hour)})),
		"closedPRs":    searchOf(0),
		"closedIssues": searchOf(1, map[string]any{"__typename": "Issue", "number": 8, "title": "done", "state": "CLOSED", "url": "https://github.com/workorg/infra/issues/8", "closedAt": in, "repository": map[string]any{"nameWithOwner": "workorg/infra"}}),
		"openPRs":      searchOf(2, pr("workorg/deploy", 9, "OPEN", map[string]any{"isDraft": true}), pr("me/kio", 10, "OPEN", nil)),
		"openIssues": searchOf(45, issue("workorg/infra", 2, "OPEN"),
			withUpdated(issue("workorg/infra", 20, "OPEN"), time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC))),
	}
}

// reviews builds a reviews connection from state, time pairs.
func reviews(kv ...any) map[string]any {
	var list []any
	for i := 0; i+1 < len(kv); i += 2 {
		list = append(list, map[string]any{"state": kv[i], "submittedAt": kv[i+1]})
	}
	return map[string]any{"reviews": map[string]any{"nodes": list}}
}

func projectFixture() map[string]any {
	return map[string]any{"owner": map[string]any{"projectsV2": nodes(
		map[string]any{"id": "P_other", "title": "Ops archive", "fields": nodes()},
		map[string]any{"id": "P_ops", "title": "Ops", "fields": nodes(
			map[string]any{"__typename": "ProjectV2Field", "name": "Title"},
			map[string]any{"__typename": "ProjectV2IterationField", "name": "Sprint"},
		)},
	)}}
}

func withLabels(m map[string]any, names ...string) map[string]any {
	list := make([]any, 0, len(names))
	for _, n := range names {
		list = append(list, map[string]any{"name": n})
	}
	m["labels"] = map[string]any{"nodes": list}
	return m
}

func card(content map[string]any, values ...map[string]any) map[string]any {
	return map[string]any{"content": content, "fieldValues": nodes(values...)}
}

func value(typename, field string, kv ...any) map[string]any {
	m := map[string]any{"__typename": typename, "field": map[string]any{"name": field}}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func itemsPage(after any) map[string]any {
	if after == nil {
		return map[string]any{"node": map[string]any{"items": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"},
			"nodes": []any{
				card(withLabels(issue("workorg/infra", 2, "OPEN"), "blocked", "area:eks"),
					value("ProjectV2ItemFieldTextValue", "Title", "text", "ignored"),
					value("ProjectV2ItemFieldSingleSelectValue", "Status", "name", "Blocked", "updatedAt", in),
					value("ProjectV2ItemFieldSingleSelectValue", "Priority", "name", "P1", "updatedAt", early),
					value("ProjectV2ItemFieldTextValue", "Blocker reason", "text", "waiting on vendor"),
					value("ProjectV2ItemFieldIterationValue", "Sprint", "title", "2026-W43"),
				),
				card(map[string]any{}),
			},
		}}}
	}
	return map[string]any{"node": map[string]any{"items": map[string]any{
		"pageInfo": map[string]any{"hasNextPage": false},
		"nodes": []any{
			card(map[string]any{"__typename": "DraftIssue", "title": "a note", "updatedAt": in},
				value("ProjectV2ItemFieldSingleSelectValue", "Status", "name", "Todo", "updatedAt", early)),
			card(issue("clientorg/api", 11, "OPEN"), value("ProjectV2ItemFieldSingleSelectValue", "Status", "name", "Todo")),
		},
	}}}
}

// fakeGitHub answers the standup queries from fixtures and records filters.
type fakeGitHub struct {
	t     *testing.T
	login string
	// orgFails makes the organization lookup fail, so the user lookup runs.
	orgFails bool
	lock     sync.Mutex
	queries  []string
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "bearer tok" {
		http.Error(w, "bad credentials", http.StatusUnauthorized)
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Errorf("decoding request: %v", err)
	}
	var data any
	switch q := req.Query; {
	case strings.Contains(q, "issueComments"):
		f.record(req.Variables["reviewed"], req.Variables["openPRs"])
		data = activityFixture(f.login)
	case strings.Contains(q, "organization(login"):
		if f.orgFails {
			writeJSON(f.t, w, map[string]any{"data": map[string]any{"owner": nil}, "errors": []any{map[string]any{"message": "not an organization"}}})
			return
		}
		data = projectFixture()
	case strings.Contains(q, "user(login"):
		data = projectFixture()
	case strings.Contains(q, "items(first"):
		f.record(req.Variables["q"])
		data = itemsPage(req.Variables["after"])
	default:
		f.t.Errorf("unexpected query %q", q)
	}
	writeJSON(f.t, w, map[string]any{"data": data})
}

func (f *fakeGitHub) record(vals ...any) {
	f.lock.Lock()
	defer f.lock.Unlock()
	for _, v := range vals {
		s, _ := v.(string) //nolint:errcheck // a missing variable records as ""
		f.queries = append(f.queries, s)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding response: %v", err)
	}
}

func newFake(t *testing.T, login string) (*fakeGitHub, *Client) {
	t.Helper()
	f := &fakeGitHub{t: t, login: login}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, NewClient(srv.URL, "tok")
}

func writeNote(t *testing.T, path string, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func refs[T any](list []T, f func(T) string) []string {
	out := make([]string, len(list))
	for i, it := range list {
		out[i] = f(it)
	}
	return out
}

func TestCollectWorkIdentity(t *testing.T) {
	cfg := loadConfig(t)
	fake, c := newFake(t, "me_work")
	vault := t.TempDir()
	writeNote(t, filepath.Join(vault, "projects", "work", "runbooks", "Deploy.md"), in)
	writeNote(t, filepath.Join(vault, "projects", "work", "Old.md"), early)
	writeNote(t, filepath.Join(vault, "projects", "work", ".obsidian", "x.md"), in)
	writeNote(t, filepath.Join(vault, "projects", "kio", "Personal.md"), in)
	writeNote(t, filepath.Join(vault, "daily", "2026-10-23.md"), in)

	r, err := Collect(context.Background(), c, NewScope(cfg, cfg.Identities["work"]), vault, since, until)
	if err != nil {
		t.Fatal(err)
	}

	done := refs(r.Done, func(it Item) string { return ref(it) + " " + it.State + " " + strings.Join(it.Actions, ",") })
	wantDone := []string{
		"workorg/infra#1 merged opened,commented,merged",
		"workorg/infra#2 open opened",
		"workorg/deploy#3 open approved",
		"workorg/deploy#4 open requested changes,reviewed",
		"workorg/infra#6 open commented",
		"workorg/infra#8 closed closed",
	}
	if !slices.Equal(sortedCopy(done), sortedCopy(wantDone)) {
		t.Errorf("done:\n%s\nwant:\n%s", strings.Join(done, "\n"), strings.Join(wantDone, "\n"))
	}
	if r.Done[0].Number != 1 {
		t.Errorf("most recent first: got %s first", ref(r.Done[0]))
	}
	open := refs(r.Open, func(it Item) string { return it.Kind + " " + ref(it) + " " + it.State })
	if want := []string{"pr workorg/deploy#9 draft", "issue workorg/infra#2 open"}; !slices.Equal(open, want) {
		t.Errorf("open %v, want %v", open, want)
	}

	if len(r.Board) != 2 {
		t.Fatalf("board %+v", r.Board)
	}
	b := r.Board[0]
	if b.Board != "Ops" || b.Status != "Blocked" || !b.ChangedInWindow || b.Iteration != "2026-W43" ||
		b.Fields["Blocker reason"] != "waiting on vendor" || !slices.Equal(b.Labels, []string{"blocked", "area:eks"}) || b.Fields["Priority"] != "P1" || b.Fields["Title"] != "" {
		t.Errorf("blocked card %+v", b)
	}
	if d := r.Board[1]; d.Kind != "draft" || d.Status != "Todo" || d.ChangedInWindow || d.Fields != nil {
		t.Errorf("draft card %+v", d)
	}
	// clientorg belongs to another identity: one review, one open PR from me/kio and one card.
	if r.Dropped != 3 {
		t.Errorf("dropped %d, want 3", r.Dropped)
	}
	if want := []string{"projects/work/runbooks/Deploy.md"}; !slices.Equal(refs(r.VaultNotes, func(n Note) string { return n.Path }), want) {
		t.Errorf("vault notes %v, want %v", r.VaultNotes, want)
	}
	if !slices.Contains(r.Warnings, "2 of 45 open issues shown") {
		t.Errorf("warnings %v", r.Warnings)
	}
	wantQueries := []string{
		"is:pr reviewed-by:me_work -author:me_work updated:>=2026-10-23T00:00:00+00:00",
		"is:pr is:open author:me_work archived:false updated:>=2026-10-09 sort:updated-desc",
		"assignee:me_work sprint:@current",
		"assignee:me_work sprint:@current",
	}
	if !slices.Equal(fake.queries, wantQueries) {
		t.Errorf("queries %q", fake.queries)
	}
}

func sortedCopy(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func TestCollectRefusesSomeoneElsesToken(t *testing.T) {
	cfg := loadConfig(t)
	_, c := newFake(t, "me")
	_, err := Collect(context.Background(), c, NewScope(cfg, cfg.Identities["work"]), "", since, until)
	if !errors.Is(err, ErrIdentity) || !strings.Contains(err.Error(), `belongs to "me"`) {
		t.Fatalf("want identity error, got %v", err)
	}
}

func TestCollectDegradesWithoutBoardOrVault(t *testing.T) {
	cfg := loadConfig(t)
	cases := []struct {
		name, vault string
		orgFails    bool
		warn        string
		board       int
	}{
		{name: "vault missing", vault: "/nonexistent/vault", warn: "is not a directory", board: 2},
		{name: "user-owned board", vault: t.TempDir(), orgFails: true, board: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, c := newFake(t, "me_work")
			f.orgFails = tc.orgFails
			r, err := Collect(context.Background(), c, NewScope(cfg, cfg.Identities["work"]), tc.vault, since, until)
			if err != nil {
				t.Fatal(err)
			}
			if tc.warn != "" && !slices.ContainsFunc(r.Warnings, func(w string) bool { return strings.Contains(w, tc.warn) }) {
				t.Errorf("warnings %v, want %q", r.Warnings, tc.warn)
			}
			if len(r.Board) != tc.board {
				t.Errorf("board %d, want %d", len(r.Board), tc.board)
			}
		})
	}
}

func TestCollectWithoutVaultHasNoVaultWarning(t *testing.T) {
	cfg := loadConfig(t)
	f, c := newFake(t, "me_work")
	r, err := Collect(context.Background(), c, NewScope(cfg, cfg.Identities["work"]), "", since, until)
	if err != nil {
		t.Fatal(err)
	}
	_ = f
	if len(r.VaultNotes) != 0 {
		t.Errorf("vault notes %v, want none", r.VaultNotes)
	}
	for _, w := range r.Warnings {
		if strings.Contains(strings.ToLower(w), "vault") {
			t.Errorf("unexpected vault warning %q", w)
		}
	}
	var text strings.Builder
	if err := r.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text.String(), "Vault notes") || strings.Contains(text.String(), "KI_VAULT") {
		t.Errorf("text report mentions the vault:\n%s", text.String())
	}
}

func TestClientErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		errHas  string
	}{
		{"http status", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "rate limited", http.StatusForbidden)
		}, "403 Forbidden: rate limited"},
		{"graphql errors", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"errors":[{"message":"a"},{"message":"b"}]}`)) //nolint:errcheck // test server
		}, "GitHub: a; b"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`<html>`)) //nolint:errcheck // test server
		}, "decoding GitHub response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			var out any
			err := NewClient(srv.URL, "secret-token").query(context.Background(), "{viewer{login}}", nil, &out)
			if err == nil || !strings.Contains(err.Error(), tc.errHas) || strings.Contains(err.Error(), "secret-token") {
				t.Fatalf("got %v, want %q without the token", err, tc.errHas)
			}
		})
	}
}

func TestBoardWithoutIteration(t *testing.T) {
	p := &project{}
	if got := boardFilter(p, "me", since); got != "assignee:me updated:>=2026-10-23" {
		t.Fatalf("filter %q", got)
	}
	p.Fields.Nodes = append(p.Fields.Nodes, struct {
		Typename string `json:"__typename"`
		Name     string `json:"name"`
	}{"ProjectV2IterationField", "Team Sprint"})
	if got := boardFilter(p, "me", since); got != `assignee:me "team sprint":@current` {
		t.Fatalf("filter %q", got)
	}
}

func TestWriteText(t *testing.T) {
	r := &Report{
		Identity: "work", GitHubUser: "me_work", Since: since,
		Done:       []Item{{Kind: "pr", Repo: "workorg/infra", Number: 1, Title: "Fix it", Actions: []string{"opened", "merged"}}},
		Open:       []Item{{Kind: "pr", Repo: "workorg/infra", Number: 2, Title: "Next", State: "draft"}},
		Board:      []BoardItem{{Item: Item{Kind: "draft", Title: "Idea"}, Status: "Todo"}},
		VaultNotes: []Note{{Path: "projects/work/A.md"}},
		Warnings:   []string{"careful"},
	}
	var b bytes.Buffer
	if err := r.WriteText(&b); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"work (me_work) since Fri 2026-10-23", "opened,merged", "workorg/infra#1  Fix it", "Open (1)", "(draft)  Idea", "projects/work/A.md", "warning: careful"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}

func TestCollectCapsVaultNotes(t *testing.T) {
	cfg := loadConfig(t)
	_, c := newFake(t, "me_work")
	vault := t.TempDir()
	for i := range maxNotes + 2 {
		writeNote(t, filepath.Join(vault, "projects", "work-infra", "Note "+strconv.Itoa(i)+".md"), in.Add(time.Duration(i)*time.Minute))
	}
	r, err := Collect(context.Background(), c, NewScope(cfg, cfg.Identities["work"]), vault, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.VaultNotes) != maxNotes || r.VaultNotes[0].Path != "projects/work-infra/Note 21.md" {
		t.Errorf("notes %d, first %v", len(r.VaultNotes), r.VaultNotes[0])
	}
	if !slices.Contains(r.Warnings, "20 of 22 changed vault notes shown") {
		t.Errorf("warnings %v", r.Warnings)
	}
}

func TestNodeKind(t *testing.T) {
	tests := []struct {
		name string
		n    node
		want string
	}{
		{"pull request", node{Typename: "PullRequest", URL: "https://github.com/o/r/pull/1"}, "pr"},
		{"issue", node{Typename: "Issue", URL: "https://github.com/o/r/issues/1"}, "issue"},
		{"pr url wins over typename", node{Typename: "Issue", URL: "https://github.com/o/r/pull/2"}, "pr"},
		{"issue url wins over typename", node{Typename: "PullRequest", URL: "https://github.com/o/r/issues/2"}, "issue"},
		{"no url uses typename", node{Typename: "PullRequest"}, "pr"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nodeKind(tt.n); got != tt.want {
				t.Errorf("nodeKind = %q, want %q", got, tt.want)
			}
		})
	}
}
