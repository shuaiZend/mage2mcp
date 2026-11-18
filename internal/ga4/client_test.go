// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	testPropertyID = "123"
	testToken      = "test-token"
)

const cannedReport = `{
  "rows": [
    {"dimensionValues": [{"value": "2026-11-12"}], "metricValues": [{"value": "120"}, {"value": "90"}]},
    {"dimensionValues": [{"value": "2026-11-13"}], "metricValues": [{"value": "130"}, {"value": "95"}]}
  ],
  "totals": [{"metricValues": [{"value": "250"}, {"value": "185"}]}],
  "rowCount": 2,
  "metadata": {
    "currencyCode": "USD",
    "timeZone": "America/Los_Angeles",
    "samplingMetadatas": [{"samplesReadCount": "1000", "samplingSpaceSize": "1000"}],
    "dataLossFromOtherRow": true
  }
}`

func testConfig(serverURL string) Config {
	return Config{
		BaseURL:     serverURL + "/v1beta",
		PropertyID:  testPropertyID,
		TokenSource: StaticToken(testToken),
		Timeout:     2 * time.Second,
	}
}

// newTestClient starts a fake Data API and returns a client pointed at it.
func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := testConfig(server.URL)
	cfg.HTTPClient = server.Client()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// newTestService starts a fake Data API and returns a service pointed at it.
func newTestService(t *testing.T, handler http.Handler) *Service {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := testConfig(server.URL)
	cfg.HTTPClient = server.Client()
	service, err := NewService(cfg)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

// reportHandler serves body for the runReport path and fails the test on any
// other request.
// errorDetail returns one taxonomy detail of err, failing the test when absent.
func errorDetail(t *testing.T, err error, key string) string {
	t.Helper()
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is not a taxonomy error", err)
	}
	return typed.Details[key]
}

func TestNewClientConfig(t *testing.T) {
	cases := []struct {
		name        string
		cfg         Config
		wantErr     bool
		wantBase    string
		wantTimeout time.Duration
	}{
		{"defaults", Config{PropertyID: "123", TokenSource: StaticToken("t")}, false, defaultBaseURL, defaultTimeout},
		{"trims trailing slash", Config{BaseURL: "https://example.com/v1beta/", PropertyID: "1", TokenSource: StaticToken("t")}, false, "https://example.com/v1beta", defaultTimeout},
		{"loopback http", Config{BaseURL: "http://127.0.0.1:8080/v1beta", PropertyID: "1", TokenSource: StaticToken("t")}, false, "http://127.0.0.1:8080/v1beta", defaultTimeout},
		{"localhost http", Config{BaseURL: "http://localhost:8080", PropertyID: "1", TokenSource: StaticToken("t")}, false, "http://localhost:8080", defaultTimeout},
		{"remote http", Config{BaseURL: "http://example.com", PropertyID: "1", TokenSource: StaticToken("t")}, true, "", 0},
		{"ftp scheme", Config{BaseURL: "ftp://example.com", PropertyID: "1", TokenSource: StaticToken("t")}, true, "", 0},
		{"non numeric property", Config{PropertyID: "12a", TokenSource: StaticToken("t")}, true, "", 0},
		{"empty property", Config{PropertyID: "", TokenSource: StaticToken("t")}, true, "", 0},
		{"nil token source", Config{PropertyID: "123"}, true, "", 0},
		{"custom timeout", Config{PropertyID: "123", TokenSource: StaticToken("t"), Timeout: 5 * time.Second}, false, defaultBaseURL, 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewClient(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if client.baseURL != tc.wantBase {
				t.Fatalf("baseURL = %q, want %q", client.baseURL, tc.wantBase)
			}
			if client.timeout != tc.wantTimeout {
				t.Fatalf("timeout = %v, want %v", client.timeout, tc.wantTimeout)
			}
			if client.httpClient == nil {
				t.Fatal("http client must not be nil")
			}
		})
	}
}

func TestStaticToken(t *testing.T) {
	token, err := StaticToken(testToken).Token(context.Background())
	if err != nil || token != testToken {
		t.Fatalf("Token() = %q, %v", token, err)
	}
	if _, err := StaticToken("").Token(context.Background()); err == nil {
		t.Fatal("expected an error for an empty static token")
	}
}

func TestRunReportRequestAndResponse(t *testing.T) {
	var received struct {
		Dimensions []struct {
			Name string `json:"name"`
		} `json:"dimensions"`
		Metrics []struct {
			Name string `json:"name"`
		} `json:"metrics"`
		DateRanges []DateRange `json:"dateRanges"`
		Limit      int64       `json:"limit"`
		Offset     int64       `json:"offset"`
		OrderBys   []struct {
			Dimension struct {
				DimensionName string `json:"dimensionName"`
			} `json:"dimension"`
			Desc bool `json:"desc"`
		} `json:"orderBys"`
		MetricAggregations []string `json:"metricAggregations"`
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q", r.Method)
		}
		if r.URL.Path != "/v1beta/properties/123:runReport" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, cannedReport)
	})

	client := newTestClient(t, handler)
	response, err := client.runReport(context.Background(), ReportRequest{
		Dimensions:         []string{"date"},
		Metrics:            []string{"sessions", "totalUsers"},
		DateRanges:         []DateRange{{StartDate: "2026-11-01", EndDate: "2026-11-07"}},
		Limit:              500,
		Offset:             10,
		OrderBys:           []OrderBy{{Dimension: "date"}},
		MetricAggregations: []string{"TOTAL"},
	})
	if err != nil {
		t.Fatalf("runReport: %v", err)
	}
	if len(received.Dimensions) != 1 || received.Dimensions[0].Name != "date" {
		t.Fatalf("dimensions = %+v", received.Dimensions)
	}
	if len(received.Metrics) != 2 || received.Metrics[0].Name != "sessions" || received.Metrics[1].Name != "totalUsers" {
		t.Fatalf("metrics = %+v", received.Metrics)
	}
	if len(received.DateRanges) != 1 || received.DateRanges[0].StartDate != "2026-11-01" || received.DateRanges[0].EndDate != "2026-11-07" {
		t.Fatalf("dateRanges = %+v", received.DateRanges)
	}
	if received.Limit != 500 || received.Offset != 10 {
		t.Fatalf("limit/offset = %d/%d", received.Limit, received.Offset)
	}
	if len(received.OrderBys) != 1 || received.OrderBys[0].Dimension.DimensionName != "date" {
		t.Fatalf("orderBys = %+v", received.OrderBys)
	}
	if len(received.MetricAggregations) != 1 || received.MetricAggregations[0] != "TOTAL" {
		t.Fatalf("metricAggregations = %+v", received.MetricAggregations)
	}

	if len(response.Rows) != 2 {
		t.Fatalf("rows = %d", len(response.Rows))
	}
	first := response.Rows[0]
	if len(first.Dimensions) != 1 || first.Dimensions[0] != "2026-11-12" {
		t.Fatalf("first row dimensions = %+v", first.Dimensions)
	}
	if len(first.Metrics) != 2 || first.Metrics[0] != 120 || first.Metrics[1] != 90 {
		t.Fatalf("first row metrics = %+v", first.Metrics)
	}
	if len(response.Totals) != 1 || response.Totals[0].Metrics[0] != 250 || response.Totals[0].Metrics[1] != 185 {
		t.Fatalf("totals = %+v", response.Totals)
	}
	if response.RowCount != 2 {
		t.Fatalf("rowCount = %d", response.RowCount)
	}
	if response.Metadata.CurrencyCode != "USD" || response.Metadata.TimeZone != "America/Los_Angeles" {
		t.Fatalf("metadata = %+v", response.Metadata)
	}
	if !response.Metadata.Sampled || !response.Metadata.DataLossFromOtherRow {
		t.Fatalf("sampled/dataLoss = %+v", response.Metadata)
	}
}

func TestRunReportErrorMapping(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCode  errs.Code
		retryable bool
		reason    string
		field     string
	}{
		{
			name:     "400 incompatible",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":400,"message":"Please remove eventCount to make the request compatible for example."}}`,
			wantCode: errs.CodeValidationFailed,
			reason:   "incompatible",
			field:    "eventCount",
		},
		{
			name:     "400 invalid",
			status:   http.StatusBadRequest,
			body:     `{"error":{"code":400,"message":"Field metrics[0] is not a valid metric."}}`,
			wantCode: errs.CodeInvalidArgument,
		},
		{
			name:     "401",
			status:   http.StatusUnauthorized,
			body:     `{"error":{"message":"Request had invalid authentication credentials."}}`,
			wantCode: errs.CodePermissionDenied,
		},
		{
			name:     "403",
			status:   http.StatusForbidden,
			body:     `{"error":{"message":"User does not have sufficient permissions for this property."}}`,
			wantCode: errs.CodePermissionDenied,
		},
		{
			name:      "429",
			status:    http.StatusTooManyRequests,
			body:      `{"error":{"message":"Quota exceeded for quota metric 'Tokens per day'."}}`,
			wantCode:  errs.CodeRateLimited,
			retryable: true,
		},
		{
			name:      "500",
			status:    http.StatusInternalServerError,
			body:      `{"error":{"message":"Internal error encountered."}}`,
			wantCode:  errs.CodeUpstreamUnavailable,
			retryable: true,
		},
		{
			name:      "503",
			status:    http.StatusServiceUnavailable,
			body:      `{"error":{"message":"The service is currently unavailable."}}`,
			wantCode:  errs.CodeUpstreamUnavailable,
			retryable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			_, err := client.runReport(context.Background(), ReportRequest{
				Dimensions: []string{"date"},
				Metrics:    []string{"sessions"},
			})
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errs.CodeOf(err); got != tc.wantCode {
				t.Fatalf("code = %s, want %s (%v)", got, tc.wantCode, err)
			}
			if got := errs.RetryableError(err); got != tc.retryable {
				t.Fatalf("retryable = %v, want %v", got, tc.retryable)
			}
			if tc.reason != "" && errorDetail(t, err, "reason") != tc.reason {
				t.Fatalf("reason detail = %q", errorDetail(t, err, "reason"))
			}
			if tc.field != "" && errorDetail(t, err, "offending_field") != tc.field {
				t.Fatalf("offending_field detail = %q", errorDetail(t, err, "offending_field"))
			}
			// The token and the raw upstream body never cross the boundary.
			if strings.Contains(err.Error(), testToken) {
				t.Fatal("error message leaks the access token")
			}
			if strings.Contains(err.Error(), "quota metric") || strings.Contains(err.Error(), "invalid authentication") {
				t.Fatalf("error message leaks the upstream body: %v", err)
			}
		})
	}
}

func TestRunReportTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.HTTPClient = server.Client()
	cfg.Timeout = 30 * time.Millisecond
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.runReport(context.Background(), ReportRequest{Metrics: []string{"sessions"}})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if got := errs.CodeOf(err); got != errs.CodeUpstreamTimeout {
		t.Fatalf("code = %s, want %s (%v)", got, errs.CodeUpstreamTimeout, err)
	}
}
