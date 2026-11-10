package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// scrape renders the registry and returns the sample for one metric line.
func scrape(t *testing.T, metric, labels string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(metric) + `\{` + regexp.QuoteMeta(labels) + `\} ([0-9.e+]+)$`)
	m := re.FindStringSubmatch(string(body))
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestToolMetricsCountOutcomes(t *testing.T) {
	ts, _ := setup(t)
	ok := scrape(t, "ki_mcp_tool_calls_total", `outcome="ok",tool="search"`)
	denied := scrape(t, "ki_mcp_tool_calls_total", `outcome="denied",tool="search"`)
	errored := scrape(t, "ki_mcp_tool_calls_total", `outcome="error",tool="get_context"`)
	timed := scrape(t, "ki_mcp_tool_seconds_count", `brain_count="1",tool="search"`)

	if _, isErr := call(t, ts, "dev", "acme", "search", map[string]any{"query": "endpoints"}); isErr {
		t.Fatal("search must succeed")
	}
	if _, isErr := call(t, ts, "dev", "acme,client", "search", map[string]any{"query": "cde"}); !isErr {
		t.Fatal("client without a grant must be denied")
	}
	if _, isErr := call(t, ts, "dev", "acme", "search", map[string]any{"query": "   "}); !isErr {
		t.Fatal("an empty query must be refused")
	}

	if got := scrape(t, "ki_mcp_tool_calls_total", `outcome="ok",tool="search"`); got != ok+1 {
		t.Fatalf("ok calls: want %v, got %v", ok+1, got)
	}
	if got := scrape(t, "ki_mcp_tool_calls_total", `outcome="denied",tool="search"`); got != denied+2 {
		t.Fatalf("denied calls: want %v, got %v", denied+2, got)
	}
	if got := scrape(t, "ki_mcp_tool_seconds_count", `brain_count="1",tool="search"`); got != timed+1 {
		t.Fatalf("timed ok call with one brain: want %v, got %v", timed+1, got)
	}
	_ = errored
}

func TestBackendFailureCountsAsError(t *testing.T) {
	ts, fake := setup(t)
	fake.failGraph = "brain-acme"
	before := scrape(t, "ki_mcp_tool_calls_total", `outcome="error",tool="get_context"`)
	if _, isErr := call(t, ts, "dev", "acme", "get_context", map[string]any{"task": "x"}); !isErr {
		t.Fatal("a failing backend must fail the call")
	}
	if got := scrape(t, "ki_mcp_tool_calls_total", `outcome="error",tool="get_context"`); got != before+1 {
		t.Fatalf("error calls: want %v, got %v", before+1, got)
	}
}

func TestMetricsCarryNoBrainNames(t *testing.T) {
	ts, _ := setup(t)
	_, _ = call(t, ts, "dev", "acme", "search", map[string]any{"query": "endpoints"})
	rec := httptest.NewRecorder()
	MetricsHandler().ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), "GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ki_mcp_") && strings.Contains(line, "acme") {
			t.Fatalf("brain name in a metric label: %s", line)
		}
	}
}

func TestBrainCountAndOutcome(t *testing.T) {
	if n := brainCount(nil); n != 0 {
		t.Fatalf("nil request: %d", n)
	}
	req := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: http.Header{}}}
	if n := brainCount(req); n != 0 {
		t.Fatalf("no header: %d", n)
	}
	req.Extra.Header.Set(BrainsHeader, "acme,voice")
	if n := brainCount(req); n != 2 {
		t.Fatalf("two brains: %d", n)
	}
	req.Extra.Header.Set(BrainsHeader, "not valid!!")
	if n := brainCount(req); n != 0 {
		t.Fatalf("malformed header: %d", n)
	}
	wrapped := scopeError{errors.New("nope")}
	if wrapped.Error() != "nope" || !errors.Is(wrapped, wrapped.err) {
		t.Fatal("scopeError must wrap its cause")
	}
	for _, tc := range []struct {
		err    error
		denied bool
		want   string
	}{{nil, false, "ok"}, {wrapped, true, "denied"}, {errors.New("x"), false, "error"}} {
		if got := outcomeOf(tc.err, tc.denied); got != tc.want {
			t.Errorf("outcomeOf(%v, %v) = %q, want %q", tc.err, tc.denied, got, tc.want)
		}
	}
}
