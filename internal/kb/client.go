// Package kb talks to kb-query, the Python service that runs Graphiti
// search against one FalkorDB instance. The MCP server never touches
// FalkorDB directly; it only calls kb-query for the graphs a request may read.
package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Kind classifies a fact for context packing.
type Kind string

const (
	KindDecision   Kind = "decision"
	KindConvention Kind = "convention"
	KindState      Kind = "state"
	KindPointer    Kind = "pointer"
)

// Stats is what one brain's graph holds.
type Stats struct {
	// Documents counts files, however many sections each was split into.
	Documents  int        `json:"documents"`
	LastIngest *time.Time `json:"last_ingest,omitempty"`
}

// RecentRequest is sent to kb-query's /recent endpoint.
type RecentRequest struct {
	Graph string     `json:"graph"`
	Since *time.Time `json:"since,omitempty"`
	Limit int        `json:"limit"`
}

// Document is one ingested file, as listed by /recent.
type Document struct {
	Brain       string     `json:"brain"`
	Kind        Kind       `json:"kind"`
	Source      string     `json:"source"`
	CommittedAt *time.Time `json:"committed_at,omitempty"`
}

// ActivityRequest is sent to kb-query's /activity endpoint.
type ActivityRequest struct {
	Graph string    `json:"graph"`
	Since time.Time `json:"since"`
}

// ActivityDay is how many documents a brain ingested on one UTC day.
type ActivityDay struct {
	Day       string `json:"day"`
	Documents int    `json:"documents"`
}

// GraphRequest is sent to kb-query's /graph endpoint: exactly one of Query and Node.
type GraphRequest struct {
	Graph string `json:"graph"`
	Query string `json:"query,omitempty"`
	Node  string `json:"node,omitempty"`
	Limit int    `json:"limit"`
}

// GraphNode is one entity of a brain, as drawn by /graph.
type GraphNode struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Summary string   `json:"summary"`
	Labels  []string `json:"labels"`
}

// GraphEdge is one fact linking two entities of a brain. From and To are node ids.
type GraphEdge struct {
	ID        string     `json:"id"`
	From      string     `json:"from"`
	To        string     `json:"to"`
	Name      string     `json:"name"`
	Fact      string     `json:"fact"`
	Kind      Kind       `json:"kind"`
	Source    string     `json:"source,omitempty"`
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
}

// Graph is a small subgraph of one brain: every edge has both endpoints in Nodes.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// Fact is one retrieved Graphiti edge, flattened for packing.
type Fact struct {
	Brain     string     `json:"brain"`
	Kind      Kind       `json:"kind"`
	Fact      string     `json:"fact"`
	Source    string     `json:"source,omitempty"`
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
	Score     float64    `json:"score"`
}

// Current reports whether the fact has not been superseded.
func (f Fact) Current() bool { return f.InvalidAt == nil }

// SearchRequest is sent to kb-query's /search endpoint.
type SearchRequest struct {
	Graph string `json:"graph"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
	// Kinds optionally restricts results, e.g. only decisions.
	Kinds []Kind `json:"kinds,omitempty"`
}

// Reader is everything the MCP server asks of kb-query; the HTTP client
// implements it and tests use a fake.
type Reader interface {
	Searcher
	Stats(ctx context.Context, backend, graph string) (Stats, error)
	Recent(ctx context.Context, backend string, req RecentRequest) ([]Document, error)
	Activity(ctx context.Context, backend string, req ActivityRequest) ([]ActivityDay, error)
	Graph(ctx context.Context, backend string, req GraphRequest) (Graph, error)
}

// Searcher is the retrieval interface.
type Searcher interface {
	Search(ctx context.Context, backend string, req SearchRequest) ([]Fact, error)
}

// Client is the HTTP implementation of Reader.
type Client struct {
	HTTP *http.Client
}

// NewClient returns a client with a sane timeout.
func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Search calls POST {backend}/search.
func (c *Client) Search(ctx context.Context, backend string, req SearchRequest) ([]Fact, error) {
	var out struct {
		Facts []Fact `json:"facts"`
	}
	if err := c.post(ctx, backend, "/search", req, &out); err != nil {
		return nil, err
	}
	return out.Facts, nil
}

// Stats calls POST {backend}/stats.
func (c *Client) Stats(ctx context.Context, backend, graph string) (Stats, error) {
	var out Stats
	err := c.post(ctx, backend, "/stats", struct {
		Graph string `json:"graph"`
	}{graph}, &out)
	return out, err
}

// Recent calls POST {backend}/recent.
func (c *Client) Recent(ctx context.Context, backend string, req RecentRequest) ([]Document, error) {
	var out struct {
		Documents []Document `json:"documents"`
	}
	if err := c.post(ctx, backend, "/recent", req, &out); err != nil {
		return nil, err
	}
	return out.Documents, nil
}

// Activity calls POST {backend}/activity.
func (c *Client) Activity(ctx context.Context, backend string, req ActivityRequest) ([]ActivityDay, error) {
	var out struct {
		Days []ActivityDay `json:"days"`
	}
	if err := c.post(ctx, backend, "/activity", req, &out); err != nil {
		return nil, err
	}
	return out.Days, nil
}

// Graph calls POST {backend}/graph.
func (c *Client) Graph(ctx context.Context, backend string, req GraphRequest) (Graph, error) {
	var out Graph
	err := c.post(ctx, backend, "/graph", req, &out)
	return out, err
}

func (c *Client) post(ctx context.Context, backend, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("encoding %s request: %w", path, err)
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, backend+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", path, err)
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return fmt.Errorf("kb-query %s: %w", backend, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kb-query %s: status %d", backend, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("kb-query %s: decode: %w", backend, err)
	}
	return nil
}
