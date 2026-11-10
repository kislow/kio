package kb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// backend answers each kb-query endpoint with a canned body and records what it was sent.
func backend(t *testing.T, status int, bodies map[string]string) (*httptest.Server, map[string]map[string]any) {
	t.Helper()
	sent := map[string]map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		sent[r.URL.Path] = in
		w.WriteHeader(status)
		_, _ = w.Write([]byte(bodies[r.URL.Path]))
	}))
	t.Cleanup(ts.Close)
	return ts, sent
}

func TestClientStatsAndRecent(t *testing.T) {
	ts, sent := backend(t, http.StatusOK, map[string]string{
		"/stats":  `{"documents": 12, "last_ingest": "2026-11-06T10:00:00Z"}`,
		"/recent": `{"documents": [{"source": "acme/decisions/a.md", "kind": "decision", "committed_at": "2026-11-05T08:00:00Z"}, {"source": "acme/README.md", "kind": "pointer"}]}`,
	})
	c := NewClient()

	st, err := c.Stats(context.Background(), ts.URL, "brain-acme")
	if err != nil || st.Documents != 12 || st.LastIngest == nil || st.LastIngest.Year() != 2026 {
		t.Fatalf("stats %+v, %v", st, err)
	}
	if sent["/stats"]["graph"] != "brain-acme" {
		t.Errorf("stats request %v", sent["/stats"])
	}

	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	docs, err := c.Recent(context.Background(), ts.URL, RecentRequest{Graph: "brain-acme", Since: &since, Limit: 5})
	if err != nil || len(docs) != 2 || docs[0].Kind != KindDecision || docs[0].CommittedAt == nil || docs[1].CommittedAt != nil {
		t.Fatalf("recent %+v, %v", docs, err)
	}
	if sent["/recent"]["since"] != "2026-11-01T00:00:00Z" || sent["/recent"]["limit"] != float64(5) {
		t.Errorf("recent request %v", sent["/recent"])
	}
}

func TestClientActivity(t *testing.T) {
	ts, sent := backend(t, http.StatusOK, map[string]string{
		"/activity": `{"days": [{"day": "2026-11-07", "documents": 3}, {"day": "2026-11-08", "documents": 12}]}`,
	})
	since := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	days, err := NewClient().Activity(context.Background(), ts.URL, ActivityRequest{Graph: "brain-acme", Since: since})
	if err != nil || len(days) != 2 || days[1] != (ActivityDay{Day: "2026-11-08", Documents: 12}) {
		t.Fatalf("activity %+v, %v", days, err)
	}
	if sent["/activity"]["graph"] != "brain-acme" || sent["/activity"]["since"] != "2026-10-09T00:00:00Z" {
		t.Errorf("activity request %v", sent["/activity"])
	}
}

func TestClientGraph(t *testing.T) {
	ts, sent := backend(t, http.StatusOK, map[string]string{
		"/graph": `{"nodes": [{"id": "n1", "name": "Acme", "summary": "s", "labels": ["Service"]}, {"id": "n2", "name": "chi", "summary": "", "labels": []}],
		  "edges": [{"id": "e1", "from": "n1", "to": "n2", "name": "USES", "fact": "routes with chi", "kind": "decision", "source": "acme/routing.md", "valid_at": "2026-11-05T08:00:00Z", "invalid_at": null}]}`,
	})
	g, err := NewClient().Graph(context.Background(), ts.URL, GraphRequest{Graph: "brain-acme", Query: "routing", Limit: 10})
	if err != nil || len(g.Nodes) != 2 || len(g.Edges) != 1 {
		t.Fatalf("graph %+v, %v", g, err)
	}
	if e := g.Edges[0]; e.From != "n1" || e.To != "n2" || e.Kind != KindDecision || e.Source != "acme/routing.md" || e.ValidAt == nil || e.InvalidAt != nil {
		t.Errorf("edge %+v", e)
	}
	if sent["/graph"]["graph"] != "brain-acme" || sent["/graph"]["query"] != "routing" || sent["/graph"]["limit"] != float64(10) {
		t.Errorf("graph request %v", sent["/graph"])
	}
	if _, ok := sent["/graph"]["node"]; ok {
		t.Errorf("an empty node must not be sent: %v", sent["/graph"])
	}
	nf, _ := backend(t, http.StatusNotFound, nil)
	if _, err := NewClient().Graph(context.Background(), nf.URL, GraphRequest{Graph: "g", Node: "n"}); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Errorf("graph on a missing node: %v", err)
	}
}

func TestClientReportsBackendFailures(t *testing.T) {
	down, _ := backend(t, http.StatusBadGateway, nil)
	garbled, _ := backend(t, http.StatusOK, map[string]string{"/stats": "not json", "/search": "{", "/recent": "[", "/activity": "["})
	c := NewClient()
	ctx := context.Background()

	if _, err := c.Stats(ctx, down.URL, "g"); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Errorf("stats on a failing backend: %v", err)
	}
	if _, err := c.Recent(ctx, down.URL, RecentRequest{Graph: "g"}); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Errorf("recent on a failing backend: %v", err)
	}
	if _, err := c.Activity(ctx, down.URL, ActivityRequest{Graph: "g"}); err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Errorf("activity on a failing backend: %v", err)
	}
	if _, err := c.Search(ctx, down.URL, SearchRequest{Graph: "g", Query: "q"}); err == nil {
		t.Error("search on a failing backend succeeded")
	}
	for name, err := range map[string]error{
		"stats":  func() error { _, err := c.Stats(ctx, garbled.URL, "g"); return err }(),
		"search": func() error { _, err := c.Search(ctx, garbled.URL, SearchRequest{Graph: "g"}); return err }(),
		"recent": func() error { _, err := c.Recent(ctx, garbled.URL, RecentRequest{Graph: "g"}); return err }(),
		"activity": func() error {
			_, err := c.Activity(ctx, garbled.URL, ActivityRequest{Graph: "g"})
			return err
		}(),
	} {
		if err == nil || !strings.Contains(err.Error(), "decode") {
			t.Errorf("%s on a garbled reply: %v", name, err)
		}
	}
	if _, err := c.Stats(ctx, "http://127.0.0.1:1", "g"); err == nil {
		t.Error("an unreachable backend succeeded")
	}
}
