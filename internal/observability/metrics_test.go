// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// scrape renders m's registry through its HTTP handler.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want %d", rec.Code, http.StatusOK)
	}
	return rec.Body.String()
}

func TestNewMetricsUsesIndependentRegistries(t *testing.T) {
	first := NewMetrics()
	second := NewMetrics()
	if first.Registry == second.Registry {
		t.Fatal("NewMetrics returned the same registry twice")
	}

	first.ToolCalls.WithLabelValues("magento", "magento.product.get", "success").Inc()
	body := scrape(t, second)
	if strings.Contains(body, "mage2mcp_tool_calls_total") {
		t.Errorf("second registry contains first registry's tool calls:\n%s", body)
	}
}

func TestMetricsHandlerExposesInstruments(t *testing.T) {
	m := NewMetrics()
	m.ToolCalls.WithLabelValues("magento", "magento.product.get", "success").Inc()
	m.ToolDuration.WithLabelValues("magento", "magento.product.get").Observe(0.123)
	m.UpstreamRequests.WithLabelValues("magento", "200").Inc()
	m.UpstreamDuration.WithLabelValues("magento").Observe(0.05)
	m.AuthFailures.WithLabelValues("expired").Inc()
	m.Jobs.WithLabelValues("reindex", "succeeded").Inc()

	body := scrape(t, m)

	wants := []string{
		`mage2mcp_tool_calls_total{module="magento",outcome="success",tool="magento.product.get"} 1`,
		"mage2mcp_tool_duration_seconds_bucket",
		`mage2mcp_tool_duration_seconds_count{module="magento",tool="magento.product.get"} 1`,
		`le="0.005"`,
		`le="120"`,
		"mage2mcp_upstream_requests_total",
		`mage2mcp_upstream_requests_total{module="magento",status="200"} 1`,
		"mage2mcp_upstream_duration_seconds_bucket",
		"mage2mcp_auth_failures_total",
		`mage2mcp_auth_failures_total{reason="expired"} 1`,
		"mage2mcp_jobs_total",
		`mage2mcp_jobs_total{queue="reindex",state="succeeded"} 1`,
		"go_goroutines",
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("metrics body missing %q\n%s", want, body)
		}
	}
}

func TestModuleOf(t *testing.T) {
	tests := []struct {
		tool string
		want string
	}{
		{"magento.product.get", "magento"},
		{"cloudflare.zone.list", "cloudflare"},
		{"ga4.report.run", "ga4"},
		{"platform.info", "platform"},
		{"single", "single"},
		{"", "unknown"},
		{"   ", "unknown"},
		{".leading", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			if got := ModuleOf(tt.tool); got != tt.want {
				t.Errorf("ModuleOf(%q) = %q, want %q", tt.tool, got, tt.want)
			}
		})
	}
}

func TestMetricsConcurrentUse(t *testing.T) {
	m := NewMetrics()
	const (
		goroutines = 8
		iterations = 100
	)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				m.ToolCalls.WithLabelValues("magento", "magento.product.get", "success").Inc()
				m.ToolDuration.WithLabelValues("magento", "magento.product.get").Observe(0.01)
				m.UpstreamRequests.WithLabelValues("magento", "200").Inc()
				m.UpstreamDuration.WithLabelValues("magento").Observe(0.01)
				m.AuthFailures.WithLabelValues("expired").Inc()
				m.Jobs.WithLabelValues("reindex", "succeeded").Inc()
			}
		}()
	}
	wg.Wait()

	if _, err := m.Registry.Gather(); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	body := scrape(t, m)
	if !strings.Contains(body, `mage2mcp_tool_calls_total{module="magento",outcome="success",tool="magento.product.get"} 800`) {
		t.Errorf("unexpected tool call count after concurrent increments:\n%s", body)
	}
}
