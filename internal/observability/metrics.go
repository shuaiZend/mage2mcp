// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// namespace prefixes every metric owned by the gateway.
const namespace = "mage2mcp"

// toolDurationBuckets covers tool latency from 5ms to 2 minutes.
var toolDurationBuckets = []float64{
	0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120,
}

// Metrics owns the Prometheus registry and the gateway's instruments.
//
// It is safe for concurrent use: every instrument is concurrency-safe and the
// registry is private to this instance.
type Metrics struct {
	// Registry is the private registry exposed by Handler.
	Registry *prometheus.Registry
	// ToolCalls counts tool invocations by module, tool, and outcome.
	ToolCalls *prometheus.CounterVec
	// ToolDuration observes tool call latency in seconds by module and tool.
	ToolDuration *prometheus.HistogramVec
	// UpstreamRequests counts upstream requests by module and status.
	UpstreamRequests *prometheus.CounterVec
	// UpstreamDuration observes upstream request latency in seconds by module.
	UpstreamDuration *prometheus.HistogramVec
	// AuthFailures counts rejected authentication attempts by reason.
	AuthFailures *prometheus.CounterVec
	// Jobs counts background job transitions by queue and state.
	Jobs *prometheus.CounterVec
}

// NewMetrics creates a Metrics with a fresh private registry, the standard Go
// and process collectors, and all gateway instruments registered.
//
// Each call returns an independent registry, so constructing a second Metrics
// never conflicts with the first.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		Registry: registry,
		ToolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "tool_calls_total",
			Help:      "Total number of tool calls by module, tool, and outcome.",
		}, []string{"module", "tool", "outcome"}),
		ToolDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "tool_duration_seconds",
			Help:      "Tool call latency in seconds by module and tool.",
			Buckets:   toolDurationBuckets,
		}, []string{"module", "tool"}),
		UpstreamRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "upstream_requests_total",
			Help:      "Total number of upstream requests by module and status.",
		}, []string{"module", "status"}),
		UpstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: namespace,
			Name:      "upstream_duration_seconds",
			Help:      "Upstream request latency in seconds by module.",
			Buckets:   toolDurationBuckets,
		}, []string{"module"}),
		AuthFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "auth_failures_total",
			Help:      "Total number of authentication failures by reason.",
		}, []string{"reason"}),
		Jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "jobs_total",
			Help:      "Total number of background job transitions by queue and state.",
		}, []string{"queue", "state"}),
	}
	registry.MustRegister(
		m.ToolCalls,
		m.ToolDuration,
		m.UpstreamRequests,
		m.UpstreamDuration,
		m.AuthFailures,
		m.Jobs,
	)
	return m
}

// Handler returns an HTTP handler that exposes m.Registry in the Prometheus
// text exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

// ModuleOf returns the first dot-separated segment of a tool name, or
// "unknown" when the name is empty or its first segment is empty.
func ModuleOf(tool string) string {
	module, _, _ := strings.Cut(strings.TrimSpace(tool), ".")
	if module == "" {
		return "unknown"
	}
	return module
}
