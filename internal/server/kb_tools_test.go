package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var ingested = time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)

// Stats reports one document per character of the graph's name, so each brain's count differs.
func (f *fakeKB) Stats(_ context.Context, _, graph string) (kb.Stats, error) {
	if graph == f.failGraph {
		return kb.Stats{}, errBackendDown
	}
	return kb.Stats{Documents: len(graph), LastIngest: &ingested}, nil
}

// Recent returns a document committed len(graph) hours before ingested, and an undated one.
func (f *fakeKB) Recent(_ context.Context, _ string, req kb.RecentRequest) ([]kb.Document, error) {
	f.lock.Lock()
	f.recents = append(f.recents, req)
	f.lock.Unlock()
	if req.Graph == f.failGraph {
		return nil, errBackendDown
	}
	at := ingested.Add(-time.Duration(len(req.Graph)) * time.Hour)
	return []kb.Document{
		{Kind: kb.KindDecision, Source: req.Graph + "/decisions/a.md", CommittedAt: &at},
		{Kind: kb.KindPointer, Source: req.Graph + "/README.md"},
	}, nil
}

// Activity reports len(graph) documents on the day of ingested, and none on the day before.
func (f *fakeKB) Activity(_ context.Context, _ string, req kb.ActivityRequest) ([]kb.ActivityDay, error) {
	f.lock.Lock()
	f.activities = append(f.activities, req)
	f.lock.Unlock()
	if req.Graph == f.failGraph {
		return nil, errBackendDown
	}
	return []kb.ActivityDay{{Day: ingested.Format(time.DateOnly), Documents: len(req.Graph)}}, nil
}

// Graph answers every request with one fact between two entities named after the graph.
func (f *fakeKB) Graph(_ context.Context, _ string, req kb.GraphRequest) (kb.Graph, error) {
	f.lock.Lock()
	f.graphs2 = append(f.graphs2, req)
	f.lock.Unlock()
	if req.Graph == f.failGraph {
		return kb.Graph{}, errBackendDown
	}
	return kb.Graph{
		Nodes: []kb.GraphNode{
			{ID: "n1", Name: req.Graph, Labels: []string{"Service"}},
			{ID: "n2", Name: "chi", Labels: []string{}},
		},
		Edges: []kb.GraphEdge{{ID: "e1", From: "n1", To: "n2", Name: "USES", Fact: "routes with chi", Kind: kb.KindDecision}},
	}, nil
}

var errBackendDown = &backendError{}

type backendError struct{}

func (*backendError) Error() string { return "backend down" }

// callStructured calls a tool and decodes its structured result into out.
func callStructured(t *testing.T, ts *httptest.Server, token, brains, tool string, args map[string]any, out any) (text string, isErr bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: headerTransport{token: token, brains: brains}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	if !res.IsError && out != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("structured content %s: %v", raw, err)
		}
	}
	return b.String(), res.IsError
}

func TestBrainsListsTheScopeWithCountsAndLastIngest(t *testing.T) {
	ts, _ := setup(t)
	var out BrainsOutput
	txt, isErr := callStructured(t, ts, "dev", "acme", "brains", nil, &out)
	if isErr {
		t.Fatal(txt)
	}
	want := []BrainInfo{{Name: "acme", Documents: len("brain-acme"), LastIngest: &ingested}, {Name: "cortex", Documents: len("cortex"), LastIngest: &ingested}}
	if len(out.Brains) != 2 || out.Brains[0].Name != want[0].Name || out.Brains[0].Documents != want[0].Documents ||
		out.Brains[1].Name != want[1].Name || out.Brains[1].Documents != want[1].Documents ||
		out.Brains[0].LastIngest == nil || !out.Brains[0].LastIngest.Equal(ingested) {
		t.Fatalf("brains %+v", out.Brains)
	}
	if !strings.Contains(txt, "- acme: 10 documents, last ingest 2026-11-06T10:00:00Z") {
		t.Errorf("text %q", txt)
	}
}

func TestBrainsLeavesOutWhatTheTokenDoesNotGrantAndFailsClosed(t *testing.T) {
	ts, _ := setup(t)
	var out BrainsOutput
	if txt, isErr := callStructured(t, ts, "dev", "", "brains", nil, &out); isErr || len(out.Brains) != 2 {
		t.Fatalf("default scope %v %+v", txt, out)
	}
	if _, isErr := callStructured(t, ts, "dev", "client", "brains", nil, nil); !isErr {
		t.Fatal("a brain the token does not grant must be refused")
	}
}

func TestOneFailingBackendFailsBrainsAndRecent(t *testing.T) {
	ts, fake := setup(t)
	fake.failGraph = "brain-acme"
	for _, tool := range []string{"brains", "recent"} {
		if txt, isErr := callStructured(t, ts, "dev", "acme", tool, nil, nil); !isErr || !strings.Contains(txt, "brain acme") {
			t.Errorf("%s: isErr %v, %q", tool, isErr, txt)
		}
	}
}

func TestRecentMergesBrainsNewestFirstAndKeepsUndatedLast(t *testing.T) {
	ts, fake := setup(t)
	var out RecentOutput
	txt, isErr := callStructured(t, ts, "dev", "acme", "recent", map[string]any{"limit": 3, "since": "2026-11-01T00:00:00Z"}, &out)
	if isErr {
		t.Fatal(txt)
	}
	got := make([]string, 0, len(out.Documents))
	for _, d := range out.Documents {
		got = append(got, d.Brain+":"+d.Source)
	}
	// cortex (6 chars) was committed 6h ago, brain-acme (10 chars) 10h ago; the undated READMEs follow in brain order
	// (acme, cortex), whichever brain answers first.
	want := []string{"cortex:cortex/decisions/a.md", "acme:brain-acme/decisions/a.md", "acme:brain-acme/README.md"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("documents %v, want %v", got, want)
	}
	if !strings.Contains(txt, "- [cortex/decision] cortex/decisions/a.md (2026-11-06T04:00:00Z)") || !strings.Contains(txt, "unknown date") {
		t.Errorf("text %q", txt)
	}
	fake.lock.Lock()
	defer fake.lock.Unlock()
	if len(fake.recents) != 2 || fake.recents[0].Limit != 3 || fake.recents[0].Since == nil || fake.recents[0].Since.Year() != 2026 {
		t.Errorf("backend requests %+v", fake.recents)
	}
}

func TestRecentNarrowsAndValidatesItsInput(t *testing.T) {
	ts, fake := setup(t)
	var out RecentOutput
	if txt, isErr := callStructured(t, ts, "dev", "acme", "recent", map[string]any{"brains": []string{"acme"}}, &out); isErr || len(out.Documents) != 2 {
		t.Fatalf("narrowed: %v %q %+v", isErr, txt, out)
	}
	fake.lock.Lock()
	if len(fake.recents) != 1 || fake.recents[0].Graph != "brain-acme" || fake.recents[0].Limit != recentDefault || fake.recents[0].Since != nil {
		t.Errorf("backend requests %+v", fake.recents)
	}
	fake.lock.Unlock()
	if _, isErr := callStructured(t, ts, "dev", "acme", "recent", map[string]any{"brains": []string{"client"}}, nil); !isErr {
		t.Error("a brain outside the scope must be refused")
	}
	if txt, isErr := callStructured(t, ts, "dev", "acme", "recent", map[string]any{"since": "last week"}, nil); !isErr || !strings.Contains(txt, "RFC 3339") {
		t.Errorf("bad since: %v %q", isErr, txt)
	}
}

func TestActivityCountsPerBrainAndNarrows(t *testing.T) {
	ts, fake := setup(t)
	var out ActivityOutput
	txt, isErr := callStructured(t, ts, "dev", "acme", "activity", map[string]any{"since": "2026-10-09T00:00:00Z"}, &out)
	if isErr {
		t.Fatal(txt)
	}
	want := map[string][]kb.ActivityDay{
		"acme":   {{Day: "2026-11-06", Documents: 10}},
		"cortex": {{Day: "2026-11-06", Documents: 6}},
	}
	if len(out.Brains) != 2 || out.Brains["acme"][0] != want["acme"][0] || out.Brains["cortex"][0] != want["cortex"][0] {
		t.Errorf("brains %+v, want %+v", out.Brains, want)
	}
	if !strings.Contains(txt, "- acme: 10 documents on 1 days") {
		t.Errorf("text %q", txt)
	}
	fake.lock.Lock()
	if len(fake.activities) != 2 || fake.activities[0].Since.Year() != 2026 || fake.activities[0].Since.Month() != 10 {
		t.Errorf("backend requests %+v", fake.activities)
	}
	fake.activities = nil
	fake.lock.Unlock()

	out = ActivityOutput{}
	if txt, isErr := callStructured(t, ts, "dev", "acme", "activity", map[string]any{"since": "2026-10-09T00:00:00Z", "brains": []string{"acme"}}, &out); isErr || len(out.Brains) != 1 {
		t.Fatalf("narrowed: %v %q %+v", isErr, txt, out)
	}
	fake.lock.Lock()
	if len(fake.activities) != 1 || fake.activities[0].Graph != "brain-acme" {
		t.Errorf("backend requests %+v", fake.activities)
	}
	fake.lock.Unlock()
}

func TestActivityValidatesItsInputAndFailsWithAnyBackend(t *testing.T) {
	ts, fake := setup(t)
	if _, isErr := callStructured(t, ts, "dev", "acme", "activity", map[string]any{"since": "2026-10-09T00:00:00Z", "brains": []string{"client"}}, nil); !isErr {
		t.Error("a brain outside the scope must be refused")
	}
	for _, args := range []map[string]any{{"since": "last week"}, {}} {
		if txt, isErr := callStructured(t, ts, "dev", "acme", "activity", args, nil); !isErr {
			t.Errorf("args %v: want an error, got %q", args, txt)
		}
	}
	fake.failGraph = "brain-acme"
	if txt, isErr := callStructured(t, ts, "dev", "acme", "activity", map[string]any{"since": "2026-10-09T00:00:00Z"}, nil); !isErr || !strings.Contains(txt, "brain acme") {
		t.Errorf("failing backend: %v %q", isErr, txt)
	}
}

func TestSearchReturnsStructuredHitsBesideTheText(t *testing.T) {
	ts, fake := setup(t)
	old := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	fake.fixed = []kb.Fact{
		{Kind: kb.KindDecision, Fact: "Acme routes with chi", Source: "acme/decisions/routing.md", Score: 1},
		{Kind: kb.KindPointer, Fact: "Acme used mux", Score: 0.5, InvalidAt: &old},
	}
	var out SearchOutput
	txt, isErr := callStructured(t, ts, "dev", "acme", "search", map[string]any{"query": "routing", "brains": []string{"acme"}}, &out)
	if isErr {
		t.Fatal(txt)
	}
	if len(out.Hits) != 2 || out.Hits[0].Source != "acme/decisions/routing.md" || out.Hits[0].Brain != "acme" || out.Hits[0].Superseded || !out.Hits[1].Superseded {
		t.Fatalf("hits %+v", out.Hits)
	}
	if !strings.Contains(txt, "- [acme/decision] Acme routes with chi\n") || !strings.Contains(txt, "(superseded)") {
		t.Errorf("text %q", txt)
	}
	var none SearchOutput
	fake.fixed = []kb.Fact{}
	if txt, _ := callStructured(t, ts, "dev", "acme", "search", map[string]any{"query": "nothing", "brains": []string{"acme"}}, &none); txt != "No results.\n" || none.Hits == nil {
		t.Errorf("empty search: %q %+v", txt, none)
	}
}

const nodeID = "6f1d2c1e-8a4b-4c55-9e0a-3b7f0a9d1c42"

func TestGraphReadsOneBrainAndRendersEdges(t *testing.T) {
	ts, fake := setup(t)
	var out GraphOutput
	txt, isErr := callStructured(t, ts, "dev", "acme", "graph", map[string]any{"brain": "acme", "query": "routing", "limit": 500}, &out)
	if isErr {
		t.Fatal(txt)
	}
	if len(out.Nodes) != 2 || len(out.Edges) != 1 || out.Edges[0].From != "n1" || out.Edges[0].Kind != kb.KindDecision {
		t.Errorf("graph %+v", out)
	}
	if txt != "- brain-acme -[USES]-> chi: routes with chi\n" {
		t.Errorf("text %q", txt)
	}
	fake.lock.Lock()
	defer fake.lock.Unlock()
	if len(fake.graphs2) != 1 || fake.graphs2[0] != (kb.GraphRequest{Graph: "brain-acme", Query: "routing", Limit: graphMax}) {
		t.Errorf("backend requests %+v", fake.graphs2)
	}
}

func TestGraphAroundANodeDefaultsTheLimit(t *testing.T) {
	ts, fake := setup(t)
	if txt, isErr := callStructured(t, ts, "dev", "acme", "graph", map[string]any{"brain": "acme", "node": nodeID}, nil); isErr {
		t.Fatal(txt)
	}
	fake.lock.Lock()
	defer fake.lock.Unlock()
	if len(fake.graphs2) != 1 || fake.graphs2[0] != (kb.GraphRequest{Graph: "brain-acme", Node: nodeID, Limit: graphDefault}) {
		t.Errorf("backend requests %+v", fake.graphs2)
	}
}

func TestGraphRefusesABrainOutsideTheScopeAndNeverWidensIt(t *testing.T) {
	ts, fake := setup(t)
	// The token grants cortex and acme, not client.
	for _, brain := range []string{"client", "unknown"} {
		args := map[string]any{"brain": brain, "query": "x"}
		if txt, isErr := callStructured(t, ts, "dev", "acme", "graph", args, nil); !isErr || !strings.Contains(txt, "outside the resolved scope") && !strings.Contains(txt, "invalid") {
			t.Errorf("brain %s: want a refusal, got %v %q", brain, isErr, txt)
		}
	}
	if _, isErr := callStructured(t, ts, "dev", "acme,client", "graph", map[string]any{"brain": "client", "query": "x"}, nil); !isErr {
		t.Error("a session asking for a brain its token does not grant must be refused")
	}
	fake.lock.Lock()
	defer fake.lock.Unlock()
	if len(fake.graphs2) != 0 {
		t.Errorf("no backend may be called for a refused brain: %+v", fake.graphs2)
	}
}

func TestGraphValidatesItsInput(t *testing.T) {
	ts, fake := setup(t)
	for name, args := range map[string]map[string]any{
		"no brain":          {"query": "x"},
		"no anchor":         {"brain": "acme"},
		"both anchors":      {"brain": "acme", "query": "x", "node": nodeID},
		"blank query":       {"brain": "acme", "query": "  "},
		"node not a uuid":   {"brain": "acme", "node": "n1"},
		"node with a quote": {"brain": "acme", "node": nodeID + "' OR 1=1"},
	} {
		if txt, isErr := callStructured(t, ts, "dev", "acme", "graph", args, nil); !isErr {
			t.Errorf("%s: want an error, got %q", name, txt)
		}
	}
	fake.lock.Lock()
	defer fake.lock.Unlock()
	if len(fake.graphs2) != 0 {
		t.Errorf("invalid input reached the backend: %+v", fake.graphs2)
	}
}

func TestGraphFailsWithTheBackend(t *testing.T) {
	ts, fake := setup(t)
	fake.failGraph = "brain-acme"
	if txt, isErr := callStructured(t, ts, "dev", "acme", "graph", map[string]any{"brain": "acme", "query": "x"}, nil); !isErr || !strings.Contains(txt, "brain acme") {
		t.Errorf("failing backend: %v %q", isErr, txt)
	}
}
