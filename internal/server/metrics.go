package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Tool metrics: how long each MCP tool takes and how often it fails. Labels
// carry the tool name and the number of brains in scope, never a brain name,
// a token or a query.
var (
	toolSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "ki_mcp_tool_seconds",
		Help:    "Wall time of one MCP tool call, including the fan-out to kb-query.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
	}, []string{"tool", "brain_count"})
	toolCalls = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ki_mcp_tool_calls_total",
		Help: "MCP tool calls by outcome: ok, denied (scope or input), error (backend).",
	}, []string{"tool", "outcome"})
)

// MetricsHandler serves the registry. Mount it on a path the ingress does
// not expose; it carries no secrets but is for the cluster's Prometheus.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// observe records one tool call. brains is the resolved scope size, 0 when
// the call was denied before resolution.
func observe(tool string, brains int, started time.Time, outcome string) {
	toolSeconds.WithLabelValues(tool, strconv.Itoa(brains)).Observe(time.Since(started).Seconds())
	toolCalls.WithLabelValues(tool, outcome).Inc()
}

// outcomeOf classifies a handler's result for the counter.
func outcomeOf(err error, denied bool) string {
	switch {
	case err == nil:
		return "ok"
	case denied:
		return "denied"
	default:
		return "error"
	}
}
