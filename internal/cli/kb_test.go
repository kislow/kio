package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/kislow/kio/internal/ki"
	"github.com/kislow/kio/internal/server"
	"github.com/kislow/kio/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Secrets distinct enough that finding one in output is a real leak.
const (
	kbToken          = "kbtoken-5f0c1e"
	kbSensitiveToken = "sensitivetoken-9a41d7"
)

const kbProjects = `sensitive_brains: [client, client-*]
identities:
  work: {github_user: alice-work, email: alice@work.example, credential: work, vault: false, ingest: true}
projects:
  - {name: app, repo: https://github.com/work/app, identity: work, brains: [app, voice]}
  - {name: docs, repo: https://github.com/work/docs, identity: work, brains: [other]}
`

var kbIngested = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

// fakeKBQuery stands in for kb-query behind a real ki-mcp: one decision and one
// undated pointer per graph, and a document count per graph name.
type fakeKBQuery struct {
	mu      sync.Mutex
	graphs  []string
	recents []kb.RecentRequest
	actives []kb.ActivityRequest
	graphed []kb.GraphRequest
	failing string
	long    string
}

func (f *fakeKBQuery) Search(_ context.Context, _ string, req kb.SearchRequest) ([]kb.Fact, error) {
	f.mu.Lock()
	f.graphs = append(f.graphs, req.Graph)
	f.mu.Unlock()
	if req.Graph == f.failing {
		return nil, errors.New("backend down")
	}
	fact := "Routing uses chi in " + req.Graph
	if f.long != "" {
		fact = f.long
	}
	old := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	return []kb.Fact{
		{Kind: kb.KindDecision, Fact: fact, Source: "app/decisions/2026-10-24-routing-with-chi.md", Score: 1},
		{Kind: kb.KindPointer, Fact: "older " + req.Graph, Source: "vault/notes/New Laptop Setup.md", Score: 0.4, InvalidAt: &old},
	}, nil
}

func (f *fakeKBQuery) Stats(_ context.Context, _, graph string) (kb.Stats, error) {
	if graph == "cortex" {
		return kb.Stats{Documents: 0}, nil
	}
	return kb.Stats{Documents: len(graph), LastIngest: &kbIngested}, nil
}

func (f *fakeKBQuery) Recent(_ context.Context, _ string, req kb.RecentRequest) ([]kb.Document, error) {
	f.mu.Lock()
	f.recents = append(f.recents, req)
	f.mu.Unlock()
	at := kbIngested.Add(-time.Duration(len(req.Graph)) * time.Hour)
	return []kb.Document{{Kind: kb.KindDecision, Source: "app/decisions/routing.md", CommittedAt: &at}}, nil
}

// Activity reports three documents on the day before kbIngested and len(graph) on its day.
func (f *fakeKBQuery) Activity(_ context.Context, _ string, req kb.ActivityRequest) ([]kb.ActivityDay, error) {
	f.mu.Lock()
	f.actives = append(f.actives, req)
	f.mu.Unlock()
	return []kb.ActivityDay{
		{Day: kbIngested.AddDate(0, 0, -1).Format(time.DateOnly), Documents: 3},
		{Day: kbIngested.Format(time.DateOnly), Documents: len(req.Graph)},
	}, nil
}

// Graph answers with one fact between an entity named after the graph and chi.
func (f *fakeKBQuery) Graph(_ context.Context, _ string, req kb.GraphRequest) (kb.Graph, error) {
	f.mu.Lock()
	f.graphed = append(f.graphed, req)
	f.mu.Unlock()
	at := kbIngested
	return kb.Graph{
		Nodes: []kb.GraphNode{{ID: "n1", Name: req.Graph, Summary: "s", Labels: []string{"Service"}}, {ID: "n2", Name: "chi", Labels: []string{}}},
		Edges: []kb.GraphEdge{
			{ID: "e1", From: "n1", To: "n2", Name: "USES", Fact: "routes with chi", Kind: kb.KindDecision, Source: "app/decisions/routing.md", ValidAt: &at},
			{ID: "e2", From: "n2", To: "n1", Name: "MENTIONS", Fact: "chi is a router", Kind: kb.KindPointer},
		},
	}, nil
}

func (f *fakeKBQuery) searched() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.graphs)
	slices.Sort(out)
	return slices.Compact(out)
}

// kbEnv is a mapped repo, two tokens and a ki-mcp serving them, with the
// tokens' secrets on disk where ki reads them.
type kbEnv struct {
	*env
	fake *fakeKBQuery
	logs *bytes.Buffer
	errs *bytes.Buffer
}

func newKBEnv(t *testing.T, remote string) *kbEnv {
	t.Helper()
	e := newEnv(t, remote)
	write(t, filepath.Join(e.harness, "deploy", "config", "projects.yaml"), kbProjects)
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "token"), kbToken+"\n")
	write(t, filepath.Join(e.vars["KI_CONFIG_DIR"], "token-sensitive"), kbSensitiveToken+"\n")

	dir := t.TempDir()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	tokens := filepath.Join(dir, "tokens.json")
	write(t, tokens, `[
	  {"id":"laptop","sha256":"`+sum(kbToken)+`","brains":["cortex","app","voice","other"],"expires":"`+exp+`"},
	  {"id":"client-session","sha256":"`+sum(kbSensitiveToken)+`","brains":["cortex","app","voice","other","client"],"expires":"`+exp+`"}
	]`)
	cfgPath := filepath.Join(dir, "config.json")
	write(t, cfgPath, `{"listen":"127.0.0.1:0","tokens_file":"`+tokens+`","brains":{
	  "cortex":{"graph":"cortex","backend":"http://main"},
	  "app":{"graph":"brain-app","backend":"http://main"},
	  "voice":{"graph":"brain-voice","backend":"http://main"},
	  "other":{"graph":"brain-other","backend":"http://main"},
	  "client":{"graph":"brain-client","backend":"http://client","sensitive":true}}}`)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	toks, err := config.LoadTokens(tokens, cfg)
	if err != nil {
		t.Fatal(err)
	}
	k := &kbEnv{env: e, fake: &fakeKBQuery{}, logs: &bytes.Buffer{}, errs: &bytes.Buffer{}}
	srv := server.New(cfg, k.fake, slog.New(slog.NewJSONHandler(k.logs, nil)), "test")
	mux := http.NewServeMux()
	mux.Handle("/mcp", auth.RequireBearerToken(toks.Verifier(), nil)(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	e.vars["KI_MCP_URL"] = ts.URL + "/"
	e.app.Stderr = k.errs
	return k
}

func (k *kbEnv) run(args ...string) error {
	k.out.Reset()
	k.errs.Reset()
	return k.app.Run(context.Background(), append([]string{"kb"}, args...))
}

func (k *kbEnv) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(k.out.Bytes(), v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, k.out.String())
	}
}

func (k *kbEnv) noLeak(t *testing.T, err error) {
	t.Helper()
	all := k.out.String() + k.errs.String() + k.logs.String()
	if err != nil {
		all += err.Error()
	}
	for _, secret := range []string{kbToken, kbSensitiveToken} {
		if strings.Contains(all, secret) {
			t.Fatalf("token %s appears in output, logs or errors:\n%s", secret, all)
		}
	}
}

const mappedRepo = "https://github.com/work/app"

func TestKBBrainsListsEveryReachableBrain(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	err := k.run("brains", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Brains []struct {
			Name       string     `json:"name"`
			Documents  int        `json:"documents"`
			LastIngest *time.Time `json:"last_ingest"`
			Projects   []string   `json:"projects"`
		} `json:"brains"`
	}
	k.json(t, &out)
	names := make([]string, 0, len(out.Brains))
	for _, b := range out.Brains {
		names = append(names, b.Name)
	}
	if !slices.Equal(names, []string{"app", "cortex", "other", "voice"}) {
		t.Fatalf("brains %v: sensitive ones belong to named requests only", names)
	}
	app := out.Brains[0]
	if app.Documents != len("brain-app") || app.LastIngest == nil || !app.LastIngest.Equal(kbIngested) || !slices.Equal(app.Projects, []string{"app"}) {
		t.Errorf("app %+v", app)
	}
	if cortex := out.Brains[1]; cortex.LastIngest != nil || cortex.Projects == nil || len(cortex.Projects) != 0 {
		t.Errorf("an empty brain has no last ingest and an empty (not null) project list: %+v", cortex)
	}
	k.noLeak(t, err)

	err = k.run("brains")
	if err != nil {
		t.Fatal(err)
	}
	text := k.out.String()
	if !strings.Contains(text, "app") || !strings.Contains(text, "9 documents") || !strings.Contains(text, "2026-11-06 10:00Z") || !strings.Contains(text, "never") {
		t.Errorf("text output:\n%s", text)
	}
	k.noLeak(t, err)
}

func TestKBBrainsNamingASensitiveBrainUsesTheSensitiveToken(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	err := k.run("brains", "--brain", "client", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Brains []struct{ Name string } `json:"brains"`
	}
	k.json(t, &out)
	if len(out.Brains) != 2 || out.Brains[0].Name != "client" || out.Brains[1].Name != "cortex" {
		t.Fatalf("brains %+v", out.Brains)
	}
	if !strings.Contains(k.logs.String(), `"brain":"client"`) {
		t.Error("a sensitive read must reach the server's audit log")
	}
	k.noLeak(t, err)
}

type kbSearchOut struct {
	Query  string   `json:"query"`
	Brains []string `json:"brains"`
	Hits   []struct {
		Title      string  `json:"title"`
		Source     string  `json:"source"`
		Brain      string  `json:"brain"`
		Kind       string  `json:"kind"`
		Snippet    string  `json:"snippet"`
		Link       string  `json:"link"`
		Score      float64 `json:"score"`
		Superseded bool    `json:"superseded"`
	} `json:"hits"`
}

func TestKBSearchInARepoReadsTheSessionsBrains(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	k.fake.long = strings.Repeat("chi ", 100)
	err := k.run("search", "how", "is", "routing", "done", "--json", "--limit", "5")
	if err != nil {
		t.Fatal(err)
	}
	var out kbSearchOut
	k.json(t, &out)
	if out.Query != "how is routing done" || !slices.Equal(out.Brains, []string{"cortex", "app", "voice"}) {
		t.Fatalf("query %q brains %v", out.Query, out.Brains)
	}
	if got := k.fake.searched(); !slices.Equal(got, []string{"brain-app", "brain-voice", "cortex"}) {
		t.Fatalf("graphs %v", got)
	}
	top := out.Hits[0]
	if top.Source != "app/decisions/2026-10-24-routing-with-chi.md" || top.Title != "2026 10 24 routing with chi" || top.Kind != "decision" || top.Superseded ||
		top.Link != "https://github.com/work/app/blob/main/decisions/2026-10-24-routing-with-chi.md" || top.Brain == "" || top.Score != 1 {
		t.Errorf("top hit %+v", top)
	}
	if n := len([]rune(top.Snippet)); n != kbSnippetRunes || !strings.HasSuffix(top.Snippet, "…") {
		t.Errorf("snippet is %d characters, want %d with an ellipsis: %q", n, kbSnippetRunes, top.Snippet)
	}
	old := out.Hits[len(out.Hits)-1]
	if !old.Superseded || old.Link != "" {
		t.Errorf("a source outside projects.yaml has no link, and a superseded hit says so: %+v", old)
	}
	k.noLeak(t, err)
}

func TestKBSearchBrainFlagReplacesTheDefault(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := k.run("search", "routing", "--brain", "other", "--json"); err != nil {
		t.Fatal(err)
	}
	if got := k.fake.searched(); !slices.Equal(got, []string{"brain-other", "cortex"}) {
		t.Fatalf("graphs %v", got)
	}
}

func TestKBAllLeavesOutVoiceUnlessNamed(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := k.run("search", "routing", "--all", "--json"); err != nil {
		t.Fatal(err)
	}
	var out kbSearchOut
	k.json(t, &out)
	if slices.Contains(out.Brains, "voice") || !slices.Contains(out.Brains, "other") || !slices.Contains(out.Brains, "cortex") {
		t.Fatalf("brains %v", out.Brains)
	}
	if slices.Contains(k.fake.searched(), "brain-voice") {
		t.Fatal("voice was searched")
	}

	if err := k.run("search", "routing", "--all", "--brain", "voice", "--json"); err != nil {
		t.Fatal(err)
	}
	k.json(t, &out)
	if !slices.Contains(out.Brains, "voice") || !slices.Contains(out.Brains, "other") {
		t.Fatalf("brains %v", out.Brains)
	}

	err := k.run("search", "routing", "--all", "--brain", "client", "--json")
	if err != nil {
		t.Fatal(err)
	}
	k.json(t, &out)
	if !slices.Contains(out.Brains, "client") || !slices.Contains(k.fake.searched(), "brain-client") {
		t.Fatalf("brains %v", out.Brains)
	}
	k.noLeak(t, err)
}

func TestKBSearchTextOutput(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := k.run("search", "routing", "--brain", "app"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"[app/decision] 2026 10 24 routing with chi\n  Routing uses chi in brain-app\n  https://github.com/work/app/blob/main/decisions/2026-10-24-routing-with-chi.md\n",
		"(superseded)", "\n  vault/notes/New Laptop Setup.md\n",
	} {
		if !strings.Contains(k.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, k.out.String())
		}
	}
}

func TestKBRecent(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	before := time.Now()
	err := k.run("recent", "--since", "7d", "--limit", "5", "--brain", "app", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Since     *time.Time `json:"since"`
		Brains    []string   `json:"brains"`
		Documents []struct {
			Title       string     `json:"title"`
			Source      string     `json:"source"`
			Brain       string     `json:"brain"`
			Kind        string     `json:"kind"`
			Link        string     `json:"link"`
			CommittedAt *time.Time `json:"committed_at"`
		} `json:"documents"`
	}
	k.json(t, &out)
	if out.Since == nil || out.Since.Before(before.Add(-7*24*time.Hour-time.Minute)) || out.Since.After(before.Add(-7*24*time.Hour+time.Minute)) {
		t.Errorf("since %v", out.Since)
	}
	if len(out.Documents) != 2 || out.Documents[0].Title != "routing" || out.Documents[0].Kind != "decision" ||
		out.Documents[0].Link != "https://github.com/work/app/blob/main/decisions/routing.md" || out.Documents[0].CommittedAt == nil {
		t.Fatalf("documents %+v", out.Documents)
	}
	k.fake.mu.Lock()
	if len(k.fake.recents) != 2 || k.fake.recents[0].Limit != 5 || k.fake.recents[0].Since == nil {
		t.Errorf("backend requests %+v", k.fake.recents)
	}
	k.fake.mu.Unlock()
	k.noLeak(t, err)

	err = k.run("recent", "--brain", "app")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(k.out.String(), "2026-11-06  [app/decision] app/decisions/routing.md") {
		t.Errorf("text output:\n%s", k.out.String())
	}
	if err := k.run("recent", "--brain", "app", "--json"); err != nil {
		t.Fatal(err)
	}
	k.json(t, &out)
	if out.Since != nil {
		t.Errorf("without --since the field is null: %v", out.Since)
	}
}

func TestKBActivity(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	before := time.Now()
	if err := k.run("activity", "--since", "7d", "--brain", "app", "--json"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Since    time.Time `json:"since"`
		Brains   []string  `json:"brains"`
		Activity map[string][]struct {
			Day       string `json:"day"`
			Documents int    `json:"documents"`
		} `json:"activity"`
	}
	k.json(t, &out)
	if out.Since.Before(before.Add(-7*24*time.Hour-time.Minute)) || out.Since.After(before.Add(-7*24*time.Hour+time.Minute)) {
		t.Errorf("since %v", out.Since)
	}
	days := out.Activity["app"]
	if len(out.Activity) != 2 || len(days) != 2 || days[0].Day != "2026-11-05" || days[0].Documents != 3 || days[1].Day != "2026-11-06" || days[1].Documents != len("brain-app") {
		t.Fatalf("activity %+v", out.Activity)
	}
	k.fake.mu.Lock()
	if len(k.fake.actives) != 2 || k.fake.actives[0].Since.IsZero() {
		t.Errorf("backend requests %+v", k.fake.actives)
	}
	k.fake.actives = nil
	k.fake.mu.Unlock()

	if err := k.run("activity", "--brain", "app"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(k.out.String(), "app") || !strings.Contains(k.out.String(), "12 documents") {
		t.Errorf("text output:\n%s", k.out.String())
	}
	k.fake.mu.Lock()
	got := time.Since(k.fake.actives[0].Since)
	k.fake.mu.Unlock()
	if got < 30*24*time.Hour-time.Minute || got > 30*24*time.Hour+time.Minute {
		t.Errorf("default window %v, want 30d", got)
	}
}

func TestSparklineFillsTheGapsAndScalesToThePeak(t *testing.T) {
	now := time.Date(2026, 11, 8, 15, 0, 0, 0, time.UTC)
	days := []kbActivityDay{{Day: "2026-11-05", Documents: 8}, {Day: "2026-11-07", Documents: 1}}
	if got, want := sparkline(days, now.AddDate(0, 0, -4), now), " █ ▁ "; got != want {
		t.Errorf("sparkline %q, want %q", got, want)
	}
	if got := sparkline(nil, now.AddDate(0, 0, -2), now); got != "   " {
		t.Errorf("empty sparkline %q", got)
	}
}

func TestKBGraphJSONAroundAQueryAndANode(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	const node = "6f1d2c1e-8a4b-4c55-9e0a-3b7f0a9d1c42"
	var out struct {
		Brain string `json:"brain"`
		Nodes []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Labels []string `json:"labels"`
		} `json:"nodes"`
		Edges []struct {
			ID, From, To, Name, Kind, Source string
			ValidAt                          *time.Time `json:"valid_at"`
		} `json:"edges"`
	}
	if err := k.run("graph", "--brain", "app", "--query", "routing", "--json"); err != nil {
		t.Fatal(err)
	}
	k.json(t, &out)
	if out.Brain != "app" || len(out.Nodes) != 2 || out.Nodes[0].Name != "brain-app" || len(out.Edges) != 2 || out.Edges[0].From != "n1" || out.Edges[0].Source != "app/decisions/routing.md" || out.Edges[0].ValidAt == nil {
		t.Fatalf("graph %+v", out)
	}
	if err := k.run("graph", "--brain", "app", "--node", node, "--limit", "7", "--json"); err != nil {
		t.Fatal(err)
	}
	k.fake.mu.Lock()
	defer k.fake.mu.Unlock()
	want := []kb.GraphRequest{{Graph: "brain-app", Query: "routing", Limit: 30}, {Graph: "brain-app", Node: node, Limit: 7}}
	if !slices.Equal(k.fake.graphed, want) {
		t.Errorf("backend requests %+v, want %+v", k.fake.graphed, want)
	}
}

func TestKBGraphJSONAlwaysCarriesBothTimes(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := k.run("graph", "--brain", "app", "--query", "routing", "--json"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Edges []map[string]json.RawMessage `json:"edges"`
	}
	k.json(t, &out)
	if len(out.Edges) != 2 {
		t.Fatalf("edges %+v", out.Edges)
	}
	for _, e := range out.Edges {
		if string(e["invalid_at"]) != "null" {
			t.Errorf("current fact invalid_at = %s, want null", e["invalid_at"])
		}
	}
	if string(out.Edges[1]["valid_at"]) != "null" || string(out.Edges[0]["valid_at"]) == "null" {
		t.Errorf("valid_at %s and %s", out.Edges[0]["valid_at"], out.Edges[1]["valid_at"])
	}
}

func TestKBGraphTextIsOneLinePerEdge(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := k.run("graph", "--brain", "app", "--query", "routing"); err != nil {
		t.Fatal(err)
	}
	want := "brain-app -[USES]-> chi: routes with chi (app/decisions/routing.md)\nchi -[MENTIONS]-> brain-app: chi is a router (pointer)\n"
	if k.out.String() != want {
		t.Errorf("output %q, want %q", k.out.String(), want)
	}
}

func TestKBGraphOfASensitiveBrainUsesTheSensitiveToken(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	err := k.run("graph", "--brain", "client", "--query", "cde", "--json")
	if err != nil {
		t.Fatal(err)
	}
	k.noLeak(t, err)
	if !strings.Contains(k.logs.String(), `"brain":"client"`) {
		t.Errorf("the read of a sensitive brain is not audited:\n%s", k.logs.String())
	}
}

func TestKBContextIsTheAgentsPack(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	err := k.run("context", "add", "an", "endpoint", "--budget", "500", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Query   string   `json:"query"`
		Brains  []string `json:"brains"`
		Context string   `json:"context"`
	}
	k.json(t, &out)
	if out.Query != "add an endpoint" || !strings.Contains(out.Context, "# Context pack") || !strings.Contains(out.Context, "Routing uses chi") || len(out.Brains) != 3 {
		t.Fatalf("context %+v", out)
	}
	k.noLeak(t, err)

	err = k.run("context", "add an endpoint")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(k.out.String(), "# Context pack") || !strings.HasSuffix(k.out.String(), "\n") || strings.HasSuffix(k.out.String(), "\n\n") {
		t.Errorf("text output:\n%q", k.out.String())
	}
}

func TestKBOutsideARepoNeedsAnIdentityAndThenReadsItsBrains(t *testing.T) {
	k := newKBEnv(t, "https://github.com/someone/else")
	err := k.run("search", "routing")
	if !isUserError(err) || !strings.Contains(err.Error(), "--identity") {
		t.Fatalf("want a request for --identity, got %v", err)
	}
	if err := k.run("brains"); err != nil {
		t.Fatalf("listing the reachable brains needs no identity: %v", err)
	}
	if err := k.run("search", "routing", "--identity", "work", "--json"); err != nil {
		t.Fatal(err)
	}
	var out kbSearchOut
	k.json(t, &out)
	if !slices.Equal(out.Brains, []string{"cortex", "app", "voice", "other"}) {
		t.Fatalf("brains %v", out.Brains)
	}
	if err := k.run("search", "routing", "--identity", "nobody"); !isUserError(err) || !strings.Contains(err.Error(), "unknown identity") {
		t.Fatalf("got %v", err)
	}
}

func TestKBRefusalsNeverCarryTheToken(t *testing.T) {
	k := newKBEnv(t, mappedRepo)

	err := k.run("search", "routing", "--brain", "nosuch")
	if !isUserError(err) || !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("a brain the token does not grant: %v", err)
	}
	k.noLeak(t, err)

	k.fake.failing = "brain-app"
	err = k.run("search", "routing", "--brain", "app")
	if !isUserError(err) || !strings.Contains(err.Error(), "backend down") {
		t.Fatalf("a failing backend: %v", err)
	}
	k.noLeak(t, err)

	write(t, filepath.Join(k.vars["KI_CONFIG_DIR"], "token"), "wrong-"+kbToken+"\n")
	err = k.run("brains")
	if !isUserError(err) {
		t.Fatalf("a rejected token: %v", err)
	}
	k.noLeak(t, err)
	if strings.Contains(err.Error(), "wrong-") {
		t.Errorf("error carries the rejected token: %v", err)
	}

	k.vars["KI_MCP_URL"] = "http://127.0.0.1:1"
	err = k.run("brains")
	if !isUserError(err) || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("an unreachable server: %v", err)
	}
	k.noLeak(t, err)
}

func TestKBMissingTokenFileIsAUserError(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	if err := os.Remove(filepath.Join(k.vars["KI_CONFIG_DIR"], "token-sensitive")); err != nil {
		t.Fatal(err)
	}
	if err := k.run("brains", "--brain", "client"); !isUserError(err) || !strings.Contains(err.Error(), "token-sensitive") {
		t.Fatalf("got %v", err)
	}
}

func TestKBLegacySensitiveTokenFileStillWorksWithANotice(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	dir := k.vars["KI_CONFIG_DIR"]
	if err := os.Rename(filepath.Join(dir, "token-sensitive"), filepath.Join(dir, ki.LegacySensitiveTokenName)); err != nil {
		t.Fatal(err)
	}
	err := k.run("brains", "--brain", "client", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Brains []struct{ Name string } `json:"brains"`
	}
	k.json(t, &out)
	if len(out.Brains) != 2 || out.Brains[0].Name != "client" {
		t.Fatalf("brains %+v", out.Brains)
	}
	if !strings.Contains(k.errs.String(), "deprecated; rename it to token-sensitive") {
		t.Fatalf("no deprecation notice: %q", k.errs.String())
	}
	k.noLeak(t, nil)
}

func TestKBArgumentErrors(t *testing.T) {
	k := newKBEnv(t, mappedRepo)
	for name, tt := range map[string]struct {
		args []string
		want string
	}{
		"unknown command":      {[]string{"nosuch"}, "unknown ki kb command"},
		"search without query": {[]string{"search"}, "needs a query"},
		"context without task": {[]string{"context", "--json"}, "needs a query"},
		"brains with a query":  {[]string{"brains", "extra"}, "takes no query"},
		"unknown flag":         {[]string{"search", "x", "--nope"}, "unknown flag"},
		"flag without value":   {[]string{"search", "x", "--limit"}, "needs a value"},
		"bad limit":            {[]string{"search", "x", "--limit", "many"}, "positive number"},
		"zero budget":          {[]string{"context", "x", "--budget", "0"}, "positive number"},
		"bad since":            {[]string{"recent", "--since", "lately"}, "--since"},
		"bad brain name":       {[]string{"search", "x", "--brain", "Bad Name"}, "invalid brain"},
		"search limit too big": {[]string{"search", "x", "--limit", "51"}, "at most 50"},
		"recent limit too big": {[]string{"recent", "--limit", "101"}, "at most 100"},
		"graph no brain":       {[]string{"graph", "--query", "x"}, "exactly one brain"},
		"graph two brains":     {[]string{"graph", "--brain", "app", "--brain", "other", "--query", "x"}, "exactly one brain"},
		"graph with --all":     {[]string{"graph", "--brain", "app", "--all", "--query", "x"}, "exactly one brain"},
		"graph no anchor":      {[]string{"graph", "--brain", "app"}, "exactly one of --query and --node"},
		"graph both anchors":   {[]string{"graph", "--brain", "app", "--query", "x", "--node", "y"}, "exactly one of --query and --node"},
		"graph limit too big":  {[]string{"graph", "--brain", "app", "--query", "x", "--limit", "61"}, "at most 60"},
		"query on search":      {[]string{"search", "x", "--query", "y"}, "belong to ki kb graph"},
	} {
		t.Run(name, func(t *testing.T) {
			err := k.run(tt.args...)
			if !isUserError(err) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("got %v, want %q", err, tt.want)
			}
		})
	}
	for _, args := range [][]string{{}, {"help"}, {"--help"}} {
		if err := k.run(args...); err != nil || !strings.HasPrefix(k.out.String(), "usage: ki kb brains") {
			t.Errorf("ki kb %v: %v\n%s", args, err, k.out.String())
		}
	}
}

func TestKBAppearsInTheTopLevelUsage(t *testing.T) {
	if !strings.Contains(usage(), "ki kb brains|search|recent|activity|graph|context") {
		t.Error("ki help does not list ki kb")
	}
}
