package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/kislow/kio/pkg/config"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeKB records which graphs were queried and returns one current and one
// superseded fact per graph. failGraph makes that graph's search fail.
// honorLimit pads the result to req.Limit. scores overrides the current-fact
// score per graph so one brain can outrank another.
type fakeKB struct {
	lock       sync.Mutex
	graphs     []string
	limits     []int
	failGraph  string
	honorLimit bool
	scores     map[string]float64
	fixed      []kb.Fact
	recents    []kb.RecentRequest
	activities []kb.ActivityRequest
	graphs2    []kb.GraphRequest
}

func (f *fakeKB) Search(_ context.Context, backend string, req kb.SearchRequest) ([]kb.Fact, error) {
	f.lock.Lock()
	f.graphs = append(f.graphs, req.Graph)
	f.limits = append(f.limits, req.Limit)
	fail := req.Graph == f.failGraph
	honor := f.honorLimit
	score := 1.0
	if s, ok := f.scores[req.Graph]; ok {
		score = s
	}
	ranked := f.scores != nil
	fixed := f.fixed
	f.lock.Unlock()
	if fixed != nil {
		return append([]kb.Fact(nil), fixed...), nil
	}
	if fail {
		return nil, errors.New("backend down")
	}
	second := 0.5
	if ranked {
		second = score - 0.1
	}
	old := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	facts := []kb.Fact{
		{Kind: kb.KindDecision, Fact: "fact from " + req.Graph + " via " + backend, Score: score},
		{Kind: kb.KindDecision, Fact: "old fact from " + req.Graph, Score: second, InvalidAt: &old},
	}
	n := len(facts)
	if honor {
		n = req.Limit
	}
	if n < 1 {
		n = 1
	}
	if n <= len(facts) {
		return facts[:n], nil
	}
	for i := len(facts); i < n; i++ {
		facts = append(facts, kb.Fact{
			Kind:  kb.KindDecision,
			Fact:  fmt.Sprintf("extra %d from %s", i, req.Graph),
			Score: score - float64(i),
		})
	}
	return facts, nil
}

func (f *fakeKB) snapshot() (graphs []string, limits []int) {
	f.lock.Lock()
	defer f.lock.Unlock()
	return append([]string(nil), f.graphs...), append([]int(nil), f.limits...)
}

func (f *fakeKB) reset() {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.graphs = nil
	f.limits = nil
}

// headerTransport adds the bearer token and brains header, as the harness does.
type headerTransport struct {
	token, brains string
}

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+h.token)
	if h.brains != "" {
		r.Header.Set(BrainsHeader, h.brains)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (*httptest.Server, *fakeKB) {
	t.Helper()
	ts, fake, _ := setupLogged(t)
	return ts, fake
}

func setupLogged(t *testing.T) (*httptest.Server, *fakeKB, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	sum := func(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
	exp := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	tokens := filepath.Join(dir, "tokens.json")
	mustWrite(t, tokens, `[
	  {"id":"laptop","sha256":"`+sum("dev")+`","brains":["cortex","acme"],"expires":"`+exp+`"},
	  {"id":"client-session","sha256":"`+sum("client")+`","brains":["cortex","acme","client","client-*"],"expires":"`+exp+`"},
	  {"id":"one-client","sha256":"`+sum("acme")+`","brains":["cortex","client-acme"],"expires":"`+exp+`"}
	]`)
	cfgPath := filepath.Join(dir, "config.json")
	mustWrite(t, cfgPath, `{
	  "listen": "127.0.0.1:0", "tokens_file": "`+tokens+`",
	  "brains": {
	    "cortex":   {"graph":"cortex","backend":"http://main"},
	    "acme": {"graph":"brain-acme","backend":"http://main"},
	    "client":      {"graph":"brain-client","backend":"http://client","sensitive":true},
	    "client-*":    {"graph":"brain-client-*","backend":"http://client","sensitive":true}
	  }
	}`)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	toks, err := config.LoadTokens(tokens, cfg)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeKB{}
	logs := &bytes.Buffer{}
	srv := New(cfg, fake, slog.New(slog.NewJSONHandler(logs, nil)), "test")
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(auth.RequireBearerToken(toks.Verifier(), nil)(h))
	t.Cleanup(ts.Close)
	return ts, fake, logs
}

func call(t *testing.T, ts *httptest.Server, token, brains, tool string, args map[string]any) (string, bool) {
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
	return b.String(), res.IsError
}

func TestGetContextScopedToRepo(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "dev", "acme", "get_context", map[string]any{"task": "add an endpoint"})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if !strings.Contains(out, "## Active decisions") || !strings.Contains(out, "brain-acme") {
		t.Fatalf("pack missing content:\n%s", out)
	}
	for _, g := range fake.graphs {
		if g == "brain-client" {
			t.Fatal("client graph must not be queried")
		}
	}
}

func TestSensitiveFailsClosedWithoutGrant(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "dev", "acme,client", "search", map[string]any{"query": "cde segmentation"})
	if !isErr || !strings.Contains(out, "not permitted") {
		t.Fatalf("want permission error, got isErr=%v %q", isErr, out)
	}
	if len(fake.graphs) != 0 {
		t.Fatalf("no backend may be called on a denied request, got %v", fake.graphs)
	}
}

func TestSensitiveOnlyWhenNamed(t *testing.T) {
	ts, fake := setup(t)
	call(t, ts, "client", "", "search", map[string]any{"query": "anything"})
	for _, g := range fake.graphs {
		if g == "brain-client" {
			t.Fatal("client must not be included by default even when granted")
		}
	}
	out, isErr := call(t, ts, "client", "client", "search", map[string]any{"query": "anything"})
	if isErr || !strings.Contains(out, "brain-client via http://client") {
		t.Fatalf("client brain should be served by its own backend: %v %s", isErr, out)
	}
}

func TestClientFamily(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "client", "client-acme", "search", map[string]any{"query": "eks"})
	if isErr || !strings.Contains(out, "brain-client-acme via http://client") {
		t.Fatalf("family member should be served from its own graph on the sensitive backend: %v %s", isErr, out)
	}
	fake.graphs = nil
	// A machine granted one client cannot read another.
	out, isErr = call(t, ts, "acme", "client-globex", "search", map[string]any{"query": "eks"})
	if !isErr || !strings.Contains(out, "not permitted") || len(fake.graphs) != 0 {
		t.Fatalf("other client must be denied without a backend call: %v %s %v", isErr, out, fake.graphs)
	}
	out, isErr = call(t, ts, "acme", "client-acme", "search", map[string]any{"query": "eks"})
	if isErr || !strings.Contains(out, "brain-client-acme") {
		t.Fatalf("single-client grant should work: %v %s", isErr, out)
	}
}

func TestRejectsMissingToken(t *testing.T) {
	ts, _ := setup(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
}

func TestDecisionsFor(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "dev", "acme", "decisions_for", map[string]any{"component": "auth"})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	for _, want := range []string{"## Active", "fact from brain-acme", "fact from cortex", "## Superseded", "(superseded 2026-01-02)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	graphs, limits := fake.snapshot()
	if !sameSet(graphs, "brain-acme", "cortex") {
		t.Fatalf("omitted brains searches the resolved scope, got %v", graphs)
	}
	for _, n := range limits {
		if n != decisionsLimit {
			t.Fatalf("limit %d, want %d", n, decisionsLimit)
		}
	}
	if out, isErr := call(t, ts, "dev", "acme", "decisions_for", map[string]any{"component": " "}); !isErr {
		t.Fatalf("empty component must fail: %s", out)
	}
}

func TestDecisionsForLimit(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "dev", "acme", "decisions_for", map[string]any{
		"component": "auth",
		"limit":     5,
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	_, limits := fake.snapshot()
	for _, n := range limits {
		if n != 5 {
			t.Fatalf("limit %d, want 5", n)
		}
	}
}

func TestOneFailingBackendFailsTheCall(t *testing.T) {
	ts, fake := setup(t)
	fake.failGraph = "brain-acme"
	out, isErr := call(t, ts, "dev", "acme", "get_context", map[string]any{"task": "x"})
	if !isErr || !strings.Contains(out, "brain acme") {
		t.Fatalf("a partial pack must not be returned: %v %s", isErr, out)
	}
}

func TestRejectsMalformedBrainsHeader(t *testing.T) {
	ts, fake := setup(t)
	out, isErr := call(t, ts, "dev", "brain:client", "search", map[string]any{"query": "x"})
	if !isErr || len(fake.graphs) != 0 {
		t.Fatalf("malformed header must fail before any search: %v %s", isErr, out)
	}
}

func TestRequiredInputs(t *testing.T) {
	ts, _ := setup(t)
	for tool, args := range map[string]map[string]any{
		"get_context": {"task": ""},
		"search":      {"query": ""},
	} {
		if out, isErr := call(t, ts, "dev", "acme", tool, args); !isErr {
			t.Errorf("%s with empty input must fail: %s", tool, out)
		}
	}
}

func TestNarrow(t *testing.T) {
	resolved := []string{"acme", "cortex"}
	tests := []struct {
		name    string
		want    []string
		wantOut []string
		err     string
	}{
		{name: "omitted", wantOut: resolved},
		{name: "empty", want: []string{}, wantOut: resolved},
		{name: "intersection drops cortex", want: []string{"acme"}, wantOut: []string{"acme"}},
		{name: "intersection keeps both", want: []string{"cortex", "acme"}, wantOut: []string{"acme", "cortex"}},
		{name: "duplicate", want: []string{"acme", " acme "}, wantOut: []string{"acme"}},
		{name: "outside", want: []string{"voice"}, err: "outside the resolved scope"},
		{name: "mixed outside is not a partial intersection", want: []string{"acme", "voice"}, err: "outside the resolved scope"},
		{name: "invalid", want: []string{"CLIENT"}, err: "invalid brain name"},
		{name: "blank", want: []string{" "}, err: "invalid brain name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := narrow(resolved, tt.want)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want %q, got %v (%v)", tt.err, err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, ",") != strings.Join(tt.wantOut, ",") {
				t.Fatalf("got %v, want %v", got, tt.wantOut)
			}
		})
	}
}

func TestDecisionsForBrainsIsIntersection(t *testing.T) {
	ts, fake := setup(t)
	fake.honorLimit = true
	out, isErr := call(t, ts, "dev", "acme", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"acme"},
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	graphs, limits := fake.snapshot()
	if !sameSet(graphs, "brain-acme") {
		t.Fatalf("narrowed fetch must not include cortex, got %v", graphs)
	}
	if len(limits) != 1 || limits[0] != decisionsLimit {
		t.Fatalf("limit applies to the narrowed brain, got %v", limits)
	}
	if got := strings.Count(out, "[acme]"); got != decisionsLimit {
		t.Fatalf("want %d acme decisions, got %d in:\n%s", decisionsLimit, got, out)
	}
	if strings.Contains(out, "[cortex]") || strings.Contains(out, "from cortex") {
		t.Fatalf("cortex decision in narrowed result:\n%s", out)
	}

	fake.reset()
	out, isErr = call(t, ts, "dev", "acme", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"acme", "cortex"},
	})
	if isErr {
		t.Fatalf("naming the resolved set must succeed: %s", out)
	}
	graphs, limits = fake.snapshot()
	if !sameSet(graphs, "brain-acme", "cortex") {
		t.Fatalf("intersection of the full resolved set searches both, got %v", graphs)
	}
	for _, n := range limits {
		if n != decisionsLimit {
			t.Fatalf("limit %d, want %d", n, decisionsLimit)
		}
	}
}

func TestBrainsArgumentDoesNotWiden(t *testing.T) {
	ts, fake := setup(t)
	// voice is not in the resolved scope (header acme → acme + cortex).
	out, isErr := call(t, ts, "dev", "acme", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"voice"},
	})
	if !isErr || !strings.Contains(out, "outside the resolved scope") || !strings.Contains(out, "voice") {
		t.Fatalf("want an outside-scope error, got isErr=%v %q", isErr, out)
	}
	if graphs, _ := fake.snapshot(); len(graphs) != 0 {
		t.Fatalf("no backend may be called, got %v", graphs)
	}

	fake.reset()
	// client is granted by this token, but the header did not resolve it.
	// Naming it must not add it to the search.
	out, isErr = call(t, ts, "client", "acme", "search", map[string]any{
		"query":  "cde",
		"brains": []any{"client"},
	})
	if !isErr || !strings.Contains(out, "outside the resolved scope") || !strings.Contains(out, "client") {
		t.Fatalf("granted-but-unresolved brain must not widen scope: isErr=%v %q", isErr, out)
	}
	if graphs, _ := fake.snapshot(); len(graphs) != 0 {
		t.Fatalf("widening must not query, got %v", graphs)
	}

	fake.reset()
	out, isErr = call(t, ts, "client", "acme", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"acme", "client"},
	})
	if !isErr || !strings.Contains(out, "client") {
		t.Fatalf("a mixed list must fail closed: isErr=%v %q", isErr, out)
	}
	if graphs, _ := fake.snapshot(); len(graphs) != 0 {
		t.Fatalf("mixed list must not query the in-scope brain, got %v", graphs)
	}
}

func TestSearchLimitAppliesWithinNarrowedBrains(t *testing.T) {
	ts, fake := setup(t)
	fake.scores = map[string]float64{"cortex": 10, "brain-acme": 1}
	out, isErr := call(t, ts, "dev", "acme", "search", map[string]any{"query": "auth", "limit": 2})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	graphs, _ := fake.snapshot()
	if !sameSet(graphs, "brain-acme", "cortex") {
		t.Fatalf("un-narrowed search queries the resolved scope, got %v", graphs)
	}
	if strings.Contains(out, "[acme/") || !strings.Contains(out, "[cortex/") {
		t.Fatalf("cortex should fill the limit before narrowing:\n%s", out)
	}

	fake.reset()
	out, isErr = call(t, ts, "dev", "acme", "search", map[string]any{
		"query":  "auth",
		"limit":  2,
		"brains": []any{"acme"},
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	graphs, limits := fake.snapshot()
	if !sameSet(graphs, "brain-acme") {
		t.Fatalf("narrowed search must fetch only acme, got %v", graphs)
	}
	if len(limits) != 1 || limits[0] != 2 {
		t.Fatalf("limit applies to the narrowed fetch, got %v", limits)
	}
	if !strings.Contains(out, "[acme/") || strings.Contains(out, "[cortex/") {
		t.Fatalf("narrowed results should be acme only:\n%s", out)
	}
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("want 2 facts, got:\n%s", out)
	}
}

func TestNarrowingDoesNotAuditUnreadSensitiveBrains(t *testing.T) {
	ts, fake, logs := setupLogged(t)
	out, isErr := call(t, ts, "client", "acme,client", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"acme"},
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	graphs, _ := fake.snapshot()
	if !sameSet(graphs, "brain-acme") {
		t.Fatalf("got %v", graphs)
	}
	if strings.Contains(logs.String(), `"brain":"client"`) {
		t.Fatalf("client was not read:\n%s", logs)
	}

	logs.Reset()
	fake.reset()
	out, isErr = call(t, ts, "client", "acme,client", "decisions_for", map[string]any{
		"component": "auth",
		"brains":    []any{"client"},
	})
	if isErr {
		t.Fatalf("unexpected error: %s", out)
	}
	if !strings.Contains(logs.String(), `"brain":"client"`) {
		t.Fatalf("reading client must be audited:\n%s", logs)
	}
}

func TestBrainsInToolSchema(t *testing.T) {
	ts, _ := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: headerTransport{token: "dev", brains: "acme"}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"decisions_for", "search"} {
		tool := byName[name]
		if tool == nil {
			t.Fatalf("missing tool %s", name)
		}
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		brains, ok := schema.Properties["brains"]
		desc, _ := brains["description"].(string)
		if !ok || schemaType(brains["type"]) != "array" || !strings.Contains(desc, "resolved scope") {
			t.Fatalf("%s brains schema: %+v\n%s", name, brains, raw)
		}
		for _, req := range schema.Required {
			if req == "brains" {
				t.Fatalf("%s brains must be optional", name)
			}
		}
	}
	raw, err := json.Marshal(byName["get_context"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "brains") {
		t.Fatalf("get_context must not take brains: %s", raw)
	}
}

func schemaType(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s != "null" {
				return s
			}
		}
	}
	return ""
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	n := map[string]int{}
	for _, g := range got {
		n[g]++
	}
	for _, g := range want {
		n[g]--
	}
	for _, c := range n {
		if c != 0 {
			return false
		}
	}
	return true
}

func TestDuplicateDecisionsAppearOnce(t *testing.T) {
	const old = "ki delegate ls read the ledger, which is appended when a job ends, so it listed finished jobs only."
	const dup = "models.execution and effort.execution now apply through ki delegate only."
	ts, fake := setup(t)
	fake.fixed = []kb.Fact{
		{Kind: kb.KindDecision, Fact: old, Source: "kio/decisions/2026-10-25-ki-delegate.md", Score: 1},
		{Kind: kb.KindDecision, Fact: "ki delegate ls lists RUNNING jobs from result.json, where the ledger listed finished jobs only.", Source: "kio/decisions/2026-10-26-delegate-streaming.md", Score: 0.9},
		{Kind: kb.KindDecision, Fact: dup, Source: "kio/decisions/2026-10-25-ki-delegate.md", Score: 0.8},
	}
	// The same fact text also comes back from the second brain in scope.
	for name, args := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"decisions_for": {"decisions_for", map[string]any{"component": "ki delegate"}},
		"get_context":   {"get_context", map[string]any{"task": "change ki delegate ls"}},
	} {
		t.Run(name, func(t *testing.T) {
			out, isErr := call(t, ts, "dev", "acme", args.tool, args.in)
			if isErr {
				t.Fatalf("unexpected error: %s", out)
			}
			if n := strings.Count(out, "models.execution"); n != 1 {
				t.Fatalf("duplicate fact appears %d times in:\n%s", n, out)
			}
		})
	}
}
