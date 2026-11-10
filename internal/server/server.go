// Package server exposes the knowledge base as narrow, typed MCP tools.
// Agents never send Cypher and never name a graph; scope comes from the
// bearer token plus the X-Ki-Brains header ki sets from projects.yaml.
// decisions_for and search may narrow that scope with a brains argument;
// the argument cannot widen it.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kislow/kio/internal/kb"
	"github.com/kislow/kio/internal/pack"
	"github.com/kislow/kio/pkg/config"
	"github.com/kislow/kio/pkg/scope"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BrainsHeader carries the session's brains, set by ki from projects.yaml.
const BrainsHeader = "X-Ki-Brains"

// Server holds what the tool handlers need.
type Server struct {
	cfg *config.Config
	kb  kb.Reader
	log *slog.Logger
}

// New builds the MCP server with all tools registered.
func New(cfg *config.Config, reader kb.Reader, log *slog.Logger, version string) *mcp.Server {
	s := &Server{cfg: cfg, kb: reader, log: log}
	srv := mcp.NewServer(&mcp.Implementation{Name: "kio-kb", Version: version}, &mcp.ServerOptions{
		Instructions: "Call get_context before planning any change. Scope is set by the session. decisions_for, search, recent and activity accept an optional brains list that narrows the resolved scope and never widens it.",
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_context",
		Description: "Context pack for a task: conventions, active decisions, current state and pointers, trimmed to a token budget. Call this first.",
	}, timed("get_context", s.getContext))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search",
		Description: "Ranked facts for a query within the session's brains. Pass brains to search only that subset of the resolved scope.",
	}, timed("search", s.search))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "brains",
		Description: "The brains in the session's scope, each with its document count and last ingest time.",
	}, timed("brains", s.brains))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "recent",
		Description: "The newest documents in the session's brains, by commit time. Pass since to skip older ones and brains to look at a subset of the resolved scope.",
	}, timed("recent", s.recent))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "activity",
		Description: "How many documents each brain ingested per UTC day since a time, by ingest time rather than commit time. Days without documents are left out. Pass brains to look at a subset of the resolved scope.",
	}, timed("activity", s.activity))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "graph",
		Description: "A small subgraph of one brain: entities as nodes and facts as edges, around a query or around one entity (a node id from an earlier graph result). Exactly one of query and node; brain is required because ids belong to one graph.",
	}, timed("graph", s.graph))
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "decisions_for",
		Description: "Decisions about a component: active ones with rationale, plus superseded history. Pass brains to search only that subset of the resolved scope, or limit to return fewer than 30 facts from each brain.",
	}, timed("decisions_for", s.decisionsFor))
	return srv
}

// scopeError marks a refusal before any backend was called: bad input or a
// scope the token does not grant. The metrics count it apart from errors.
type scopeError struct{ err error }

func (e scopeError) Error() string { return e.err.Error() }
func (e scopeError) Unwrap() error { return e.err }

// timed wraps a tool handler with the duration histogram and the outcome
// counter. The brain count comes from the request's brains header once the
// scope resolved; a denied call records 0.
func timed[In, Out any](tool string, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		started := time.Now()
		res, extra, err := h(ctx, req, in)
		var denied scopeError
		isDenied := errors.As(err, &denied)
		brains := 0
		if err == nil || !isDenied {
			brains = brainCount(req)
		}
		observe(tool, brains, started, outcomeOf(err, isDenied))
		if isDenied {
			err = denied.err
		}
		return res, extra, err
	}
}

// brainCount is the number of brains the session asked for, from the header
// ki sets; 0 when the request carries none.
func brainCount(req *mcp.CallToolRequest) int {
	if req == nil || req.Extra == nil || req.Extra.Header == nil {
		return 0
	}
	requested, err := scope.ParseHeader(req.Extra.Header.Get(BrainsHeader))
	if err != nil {
		return 0
	}
	return len(requested)
}

// GetContextInput is the get_context tool input.
type GetContextInput struct {
	Task   string `json:"task" jsonschema:"what the agent is about to do"`
	Budget int    `json:"budget,omitempty" jsonschema:"token budget for the pack, default 3000"`
}

// SearchInput is the search tool input.
type SearchInput struct {
	Query  string   `json:"query"`
	Limit  int      `json:"limit,omitempty" jsonschema:"max facts, default 15"`
	Brains []string `json:"brains,omitempty" jsonschema:"optional subset of the resolved scope; omit to search every resolved brain; a name outside that scope is an error"`
}

// DecisionsInput is the decisions_for tool input.
type DecisionsInput struct {
	Component string   `json:"component" jsonschema:"service, package or component name"`
	Brains    []string `json:"brains,omitempty" jsonschema:"optional subset of the resolved scope; omit to search every resolved brain; a name outside that scope is an error"`
	Limit     int      `json:"limit,omitempty" jsonschema:"maximum facts from each brain, default 30"`
}

// recent returns recentDefault documents unless asked for more, up to recentMax; each brain is fetched up to that limit.
const (
	recentDefault = 20
	recentMax     = 100
)

// decisionsLimit is the most decisions fetched from each brain after narrowing.
const decisionsLimit = 30

func (s *Server) getContext(ctx context.Context, req *mcp.CallToolRequest, in GetContextInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Task) == "" {
		return nil, nil, scopeError{errors.New("task is required")}
	}
	brains, err := s.readable(req, "get_context", nil)
	if err != nil {
		return nil, nil, scopeError{err}
	}
	budget := in.Budget
	if budget <= 0 || budget > 20000 {
		budget = s.cfg.DefaultBudget
	}
	facts, err := s.fanOut(ctx, brains, kb.SearchRequest{Query: in.Task, Limit: 40})
	if err != nil {
		return nil, nil, err
	}
	return text(pack.Build(in.Task, brains, kb.Resolve(facts), budget)), nil, nil
}

// SearchHit is one ranked fact in search's structured result.
type SearchHit struct {
	Brain      string  `json:"brain"`
	Kind       string  `json:"kind"`
	Fact       string  `json:"fact"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score"`
	Superseded bool    `json:"superseded"`
}

// SearchOutput is search's structured result; the text content carries the same facts.
type SearchOutput struct {
	Hits []SearchHit `json:"hits"`
}

func (s *Server) search(ctx context.Context, req *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	none := SearchOutput{Hits: []SearchHit{}}
	if strings.TrimSpace(in.Query) == "" {
		return nil, none, scopeError{errors.New("query is required")}
	}
	brains, err := s.readable(req, "search", in.Brains)
	if err != nil {
		return nil, none, scopeError{err}
	}
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 15
	}
	facts, err := s.fanOut(ctx, brains, kb.SearchRequest{Query: in.Query, Limit: limit})
	if err != nil {
		return nil, none, err
	}
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].Score > facts[j].Score })
	if len(facts) > limit {
		facts = facts[:limit]
	}
	var b strings.Builder
	out := none
	for _, f := range facts {
		status := ""
		if !f.Current() {
			status = " (superseded)"
		}
		fmt.Fprintf(&b, "- [%s/%s] %s%s\n", f.Brain, f.Kind, f.Fact, status)
		out.Hits = append(out.Hits, SearchHit{Brain: f.Brain, Kind: string(f.Kind), Fact: f.Fact, Source: f.Source, Score: f.Score, Superseded: !f.Current()})
	}
	if b.Len() == 0 {
		b.WriteString("No results.\n")
	}
	return text(b.String()), out, nil
}

// BrainInfo is one brain in brains' structured result.
type BrainInfo struct {
	Name       string     `json:"name"`
	Documents  int        `json:"documents"`
	LastIngest *time.Time `json:"last_ingest,omitempty"`
}

// BrainsOutput is brains' structured result, sorted by name.
type BrainsOutput struct {
	Brains []BrainInfo `json:"brains"`
}

func (s *Server) brains(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, BrainsOutput, error) {
	none := BrainsOutput{Brains: []BrainInfo{}}
	brains, err := s.readable(req, "brains", nil)
	if err != nil {
		return nil, none, scopeError{err}
	}
	var mu sync.Mutex
	out := none
	err = s.eachBrain(brains, func(name string, b config.Brain) error {
		st, err := s.kb.Stats(ctx, b.Backend, b.Graph)
		if err != nil {
			return fmt.Errorf("stats: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		out.Brains = append(out.Brains, BrainInfo{Name: name, Documents: st.Documents, LastIngest: st.LastIngest})
		return nil
	})
	if err != nil {
		return nil, none, err
	}
	sort.Slice(out.Brains, func(i, j int) bool { return out.Brains[i].Name < out.Brains[j].Name })
	var b strings.Builder
	for _, br := range out.Brains {
		fmt.Fprintf(&b, "- %s: %d documents", br.Name, br.Documents)
		if br.LastIngest != nil {
			fmt.Fprintf(&b, ", last ingest %s", br.LastIngest.Format(time.RFC3339))
		}
		b.WriteString("\n")
	}
	return text(b.String()), out, nil
}

// RecentInput is the recent tool input.
type RecentInput struct {
	Since  string   `json:"since,omitempty" jsonschema:"RFC 3339 time; documents committed before it are left out"`
	Limit  int      `json:"limit,omitempty" jsonschema:"max documents, default 20"`
	Brains []string `json:"brains,omitempty" jsonschema:"optional subset of the resolved scope; omit to look at every resolved brain; a name outside that scope is an error"`
}

// RecentOutput is recent's structured result, newest first.
type RecentOutput struct {
	Documents []kb.Document `json:"documents"`
}

func (s *Server) recent(ctx context.Context, req *mcp.CallToolRequest, in RecentInput) (*mcp.CallToolResult, RecentOutput, error) {
	none := RecentOutput{Documents: []kb.Document{}}
	var since *time.Time
	if in.Since != "" {
		t, err := time.Parse(time.RFC3339, in.Since)
		if err != nil {
			return nil, none, scopeError{fmt.Errorf("since must be an RFC 3339 time: %w", err)}
		}
		since = &t
	}
	brains, err := s.readable(req, "recent", in.Brains)
	if err != nil {
		return nil, none, scopeError{err}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = recentDefault
	}
	limit = min(limit, recentMax)
	var mu sync.Mutex
	out := none
	err = s.eachBrain(brains, func(name string, b config.Brain) error {
		docs, err := s.kb.Recent(ctx, b.Backend, kb.RecentRequest{Graph: b.Graph, Since: since, Limit: limit})
		if err != nil {
			return fmt.Errorf("recent: %w", err)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, d := range docs {
			d.Brain = name
			out.Documents = append(out.Documents, d)
		}
		return nil
	})
	if err != nil {
		return nil, none, err
	}
	// The brains answer concurrently, so ties (and undated documents) need a fixed order: the order the brains were
	// asked for, then the path.
	rank := make(map[string]int, len(brains))
	for i, name := range brains {
		rank[name] = i
	}
	sort.SliceStable(out.Documents, func(i, j int) bool {
		a, b := out.Documents[i], out.Documents[j]
		if newer(a, b) || newer(b, a) {
			return newer(a, b)
		}
		if rank[a.Brain] != rank[b.Brain] {
			return rank[a.Brain] < rank[b.Brain]
		}
		return a.Source < b.Source
	})
	if len(out.Documents) > limit {
		out.Documents = out.Documents[:limit]
	}
	var b strings.Builder
	for _, d := range out.Documents {
		at := "unknown date"
		if d.CommittedAt != nil {
			at = d.CommittedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "- [%s/%s] %s (%s)\n", d.Brain, d.Kind, d.Source, at)
	}
	if b.Len() == 0 {
		b.WriteString("No documents.\n")
	}
	return text(b.String()), out, nil
}

// ActivityInput is the activity tool input.
type ActivityInput struct {
	Since  string   `json:"since" jsonschema:"RFC 3339 time; documents ingested before it are left out"`
	Brains []string `json:"brains,omitempty" jsonschema:"optional subset of the resolved scope; omit to look at every resolved brain; a name outside that scope is an error"`
}

// ActivityOutput is activity's structured result: per brain, one row per day that ingested documents.
type ActivityOutput struct {
	Brains map[string][]kb.ActivityDay `json:"brains"`
}

func (s *Server) activity(ctx context.Context, req *mcp.CallToolRequest, in ActivityInput) (*mcp.CallToolResult, ActivityOutput, error) {
	none := ActivityOutput{Brains: map[string][]kb.ActivityDay{}}
	since, err := time.Parse(time.RFC3339, in.Since)
	if err != nil {
		return nil, none, scopeError{fmt.Errorf("since must be an RFC 3339 time: %w", err)}
	}
	// Scope and audit follow recent: the same documents are being read, only counted.
	brains, err := s.readable(req, "recent", in.Brains)
	if err != nil {
		return nil, none, scopeError{err}
	}
	var mu sync.Mutex
	out := none
	err = s.eachBrain(brains, func(name string, b config.Brain) error {
		days, err := s.kb.Activity(ctx, b.Backend, kb.ActivityRequest{Graph: b.Graph, Since: since})
		if err != nil {
			return fmt.Errorf("activity: %w", err)
		}
		if days == nil {
			days = []kb.ActivityDay{}
		}
		mu.Lock()
		defer mu.Unlock()
		out.Brains[name] = days
		return nil
	})
	if err != nil {
		return nil, none, err
	}
	var b strings.Builder
	for _, name := range brains {
		total := 0
		for _, d := range out.Brains[name] {
			total += d.Documents
		}
		fmt.Fprintf(&b, "- %s: %d documents on %d days\n", name, total, len(out.Brains[name]))
	}
	return text(b.String()), out, nil
}

// GraphInput is the graph tool input.
type GraphInput struct {
	Brain string `json:"brain" jsonschema:"the one brain to read; it must be in the resolved scope"`
	Query string `json:"query,omitempty" jsonschema:"find the facts that match this text and the entities they link; give this or node"`
	Node  string `json:"node,omitempty" jsonschema:"uuid of an entity of the brain: its facts and the entities they link; give this or query"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum edges, 1 to 60; default 30"`
}

// GraphOutput is graph's structured result.
type GraphOutput = kb.Graph

const (
	graphDefault = 30
	graphMax     = 60
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) graph(ctx context.Context, req *mcp.CallToolRequest, in GraphInput) (*mcp.CallToolResult, GraphOutput, error) {
	none := GraphOutput{Nodes: []kb.GraphNode{}, Edges: []kb.GraphEdge{}}
	query, node := strings.TrimSpace(in.Query), strings.TrimSpace(in.Node)
	switch {
	case strings.TrimSpace(in.Brain) == "":
		return nil, none, scopeError{errors.New("brain is required")}
	case (query == "") == (node == ""):
		return nil, none, scopeError{errors.New("give exactly one of query and node")}
	case node != "" && !uuidPattern.MatchString(node):
		return nil, none, scopeError{errors.New("node must be a uuid")}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = graphDefault
	}
	limit = min(limit, graphMax)
	// Scope follows recent: the same documents are being read, shown as entities and facts.
	// The audit line names graph, because it returns fact text and summaries, not only names.
	brains, err := s.readable(req, "graph", []string{in.Brain})
	if err != nil {
		return nil, none, scopeError{err}
	}
	b, ok := s.cfg.Lookup(brains[0])
	if !ok {
		return nil, none, fmt.Errorf("brain %s is not configured", brains[0])
	}
	g, err := s.kb.Graph(ctx, b.Backend, kb.GraphRequest{Graph: b.Graph, Query: query, Node: node, Limit: limit})
	if err != nil {
		return nil, none, fmt.Errorf("graph: brain %s: %w", brains[0], err)
	}
	if g.Nodes == nil {
		g.Nodes = none.Nodes
	}
	if g.Edges == nil {
		g.Edges = none.Edges
	}
	names := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		names[n.ID] = n.Name
	}
	var out strings.Builder
	for _, e := range g.Edges {
		fmt.Fprintf(&out, "- %s -[%s]-> %s: %s\n", names[e.From], e.Name, names[e.To], e.Fact)
	}
	if out.Len() == 0 {
		out.WriteString("No facts.\n")
	}
	return text(out.String()), g, nil
}

// newer orders documents newest first; one without a date sorts last.
func newer(a, b kb.Document) bool {
	switch {
	case a.CommittedAt == nil:
		return false
	case b.CommittedAt == nil:
		return true
	default:
		return a.CommittedAt.After(*b.CommittedAt)
	}
}

func (s *Server) decisionsFor(ctx context.Context, req *mcp.CallToolRequest, in DecisionsInput) (*mcp.CallToolResult, any, error) {
	if strings.TrimSpace(in.Component) == "" {
		return nil, nil, scopeError{errors.New("component is required")}
	}
	brains, err := s.readable(req, "decisions_for", in.Brains)
	if err != nil {
		return nil, nil, scopeError{err}
	}
	limit := in.Limit
	if limit <= 0 || limit > decisionsLimit {
		limit = decisionsLimit
	}
	facts, err := s.fanOut(ctx, brains, kb.SearchRequest{Query: in.Component, Limit: limit, Kinds: []kb.Kind{kb.KindDecision}})
	if err != nil {
		return nil, nil, err
	}
	var active, history strings.Builder
	for _, f := range kb.Resolve(facts) {
		line := fmt.Sprintf("- %s [%s]\n", f.Fact, f.Brain)
		if f.Current() {
			active.WriteString(line)
		} else {
			history.WriteString(strings.TrimSuffix(line, "\n") + fmt.Sprintf(" (superseded %s)\n", f.InvalidAt.Format("2006-01-02")))
		}
	}
	out := "## Active\n\n" + orNone(active.String()) + "\n## Superseded\n\n" + orNone(history.String())
	return text(out), nil, nil
}

// scope resolves the brains this request may read. It does not narrow.
func (s *Server) scope(req *mcp.CallToolRequest, tool string) ([]string, error) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil, errors.New("unauthenticated")
	}
	requested, err := scope.ParseHeader(req.Extra.Header.Get(BrainsHeader))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", BrainsHeader, err)
	}
	brains, err := scope.Resolve(req.Extra.TokenInfo.Scopes, requested, s.cfg.Sensitive)
	if err != nil {
		s.log.Warn("scope denied", "token", req.Extra.TokenInfo.UserID, "tool", tool, "requested", requested, "err", err)
		return nil, fmt.Errorf("resolving scope: %w", err)
	}
	return brains, nil
}

// readable resolves the session scope, optionally narrows it to want, and
// audits the sensitive brains the call will read. An empty want leaves the
// resolved scope unchanged. scope.Resolve is not modified.
func (s *Server) readable(req *mcp.CallToolRequest, tool string, want []string) ([]string, error) {
	brains, err := s.scope(req, tool)
	if err != nil {
		return nil, err
	}
	brains, err = narrow(brains, want)
	if err != nil {
		return nil, fmt.Errorf("narrowing brains: %w", err)
	}
	s.audit(req, tool, brains)
	return brains, nil
}

// narrow returns the subset of resolved named by want, sorted. An empty
// want leaves resolved unchanged. A name that is not in resolved is an
// error, so want cannot widen the scope.
func narrow(resolved, want []string) ([]string, error) {
	if len(want) == 0 {
		return resolved, nil
	}
	allowed := make(map[string]bool, len(resolved))
	for _, b := range resolved {
		allowed[b] = true
	}
	seen := make(map[string]bool, len(want))
	out := make([]string, 0, len(want))
	for _, raw := range want {
		b := strings.TrimSpace(raw)
		if !scope.ValidName(b) {
			return nil, fmt.Errorf("invalid brain name %q", b)
		}
		if !allowed[b] {
			return nil, fmt.Errorf("brain %q is outside the resolved scope", b)
		}
		if seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	sort.Strings(out)
	return out, nil
}

// audit records sensitive brains this call will read. The query and the
// results stay out of the log: the task text can describe client design itself.
func (s *Server) audit(req *mcp.CallToolRequest, tool string, brains []string) {
	for _, b := range brains {
		if s.cfg.Sensitive(b) {
			s.log.Info("sensitive access", "brain", b, "token", req.Extra.TokenInfo.UserID, "tool", tool)
		}
	}
}

// eachBrain runs fn for every brain concurrently. Any failure fails the
// whole call: a partial answer would silently hide a brain, which is worse
// than an error.
func (s *Server) eachBrain(brains []string, fn func(name string, b config.Brain) error) error {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []error
	)
	for _, name := range brains {
		b, ok := s.cfg.Lookup(name)
		if !ok {
			return fmt.Errorf("brain %s is not configured", name)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(name, b); err != nil {
				mu.Lock()
				defer mu.Unlock()
				errs = append(errs, fmt.Errorf("brain %s: %w", name, err))
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// fanOut searches each brain's graph concurrently and merges the facts.
func (s *Server) fanOut(ctx context.Context, brains []string, base kb.SearchRequest) ([]kb.Fact, error) {
	var (
		mu    sync.Mutex
		facts []kb.Fact
	)
	err := s.eachBrain(brains, func(name string, b config.Brain) error {
		req := base
		req.Graph = b.Graph
		got, err := s.kb.Search(ctx, b.Backend, req)
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}
		for i := range got {
			got[i].Brain = name
		}
		mu.Lock()
		defer mu.Unlock()
		facts = append(facts, got...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return facts, nil
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func orNone(s string) string {
	if s == "" {
		return "_none_\n"
	}
	return s
}
