// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// graphqlCall is the wire shape of one recorded GraphQL request.
type graphqlCall struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func testConfig(baseURL string) Config {
	return Config{
		APIBaseURL: baseURL,
		APIToken:   "test-token",
		ZoneID:     "zone-test",
		Timeout:    2 * time.Second,
	}
}

func mustClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"https", Config{APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "t", ZoneID: "z"}, false},
		{"loopback http", Config{APIBaseURL: "http://127.0.0.1:8080/client/v4", APIToken: "t", ZoneID: "z"}, false},
		{"localhost http", Config{APIBaseURL: "http://localhost:8080", APIToken: "t", ZoneID: "z"}, false},
		{"remote http", Config{APIBaseURL: "http://api.example.com", APIToken: "t", ZoneID: "z"}, true},
		{"empty base", Config{APIToken: "t", ZoneID: "z"}, true},
		{"relative base", Config{APIBaseURL: "api.example.com/client/v4", APIToken: "t", ZoneID: "z"}, true},
		{"missing token", Config{APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "  ", ZoneID: "z"}, true},
		{"missing zone", Config{APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "t"}, true},
		{"userinfo", Config{APIBaseURL: "https://user:pass@api.cloudflare.com/client/v4", APIToken: "t", ZoneID: "z"}, true},
		{"query", Config{APIBaseURL: "https://api.cloudflare.com/client/v4?x=1", APIToken: "t", ZoneID: "z"}, true},
	}
	for _, tc := range tests {
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
			if client.zoneID != "z" {
				t.Fatalf("zoneID = %q", client.zoneID)
			}
		})
	}
}

func TestNewClientDefaultsAndTrims(t *testing.T) {
	client := mustClient(t, Config{
		APIBaseURL: "https://api.cloudflare.com/client/v4/",
		APIToken:   " test-token ",
		ZoneID:     " zone-test ",
	})
	if client.baseURL != "https://api.cloudflare.com/client/v4" {
		t.Fatalf("baseURL = %q", client.baseURL)
	}
	if client.token != "test-token" {
		t.Fatalf("token = %q", client.token)
	}
	if client.zoneID != "zone-test" {
		t.Fatalf("zoneID = %q", client.zoneID)
	}
	if client.timeout != defaultTimeout {
		t.Fatalf("timeout = %s, want %s", client.timeout, defaultTimeout)
	}
	custom := mustClient(t, Config{
		APIBaseURL: "https://api.cloudflare.com/client/v4",
		APIToken:   "t",
		ZoneID:     "z",
		Timeout:    5 * time.Second,
	})
	if custom.timeout != 5*time.Second {
		t.Fatalf("timeout = %s", custom.timeout)
	}
}

func TestDoSendsBearerAndDecodes(t *testing.T) {
	var (
		mu     sync.Mutex
		method string
		path   string
		auth   string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method, path, auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()

	client := mustClient(t, testConfig(server.URL))
	var out struct {
		OK bool `json:"ok" jsonschema:"True when the call succeeded."`
	}
	if err := client.do(context.Background(), http.MethodGet, "/zones", nil, &out); err != nil {
		t.Fatalf("do: %v", err)
	}
	if !out.OK {
		t.Fatal("response not decoded")
	}
	mu.Lock()
	defer mu.Unlock()
	if method != http.MethodGet || path != "/zones" {
		t.Fatalf("request = %s %s", method, path)
	}
	if auth != "Bearer test-token" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestDoStatusErrors(t *testing.T) {
	tests := []struct {
		status    int
		code      errs.Code
		retryable bool
	}{
		{http.StatusUnauthorized, errs.CodePermissionDenied, false},
		{http.StatusForbidden, errs.CodePermissionDenied, false},
		{http.StatusNotFound, errs.CodeNotFound, false},
		{http.StatusTooManyRequests, errs.CodeRateLimited, true},
		{http.StatusInternalServerError, errs.CodeUpstreamUnavailable, true},
		{http.StatusServiceUnavailable, errs.CodeUpstreamUnavailable, true},
		{http.StatusBadRequest, errs.CodeUpstreamUnavailable, false},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"secret":"test-token","body":"raw-body"}`)
			}))
			defer server.Close()

			client := mustClient(t, testConfig(server.URL))
			err := client.do(context.Background(), http.MethodGet, "/zones", nil, nil)
			if err == nil {
				t.Fatal("expected an error")
			}
			var typed *errs.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error is not *errs.Error: %T", err)
			}
			if typed.Code != tc.code {
				t.Fatalf("code = %s, want %s", typed.Code, tc.code)
			}
			if typed.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", typed.Retryable, tc.retryable)
			}
			if typed.Details["upstream_status"] == "" {
				t.Fatal("missing upstream_status detail")
			}
			if strings.Contains(err.Error(), "test-token") || strings.Contains(err.Error(), "raw-body") {
				t.Fatalf("error leaked sensitive data: %v", err)
			}
		})
	}
}

func TestDoTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	cfg := testConfig(server.URL)
	cfg.Timeout = 20 * time.Millisecond
	client := mustClient(t, cfg)
	err := client.do(context.Background(), http.MethodGet, "/zones", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeUpstreamTimeout {
		t.Fatalf("code = %s, want %s", got, errs.CodeUpstreamTimeout)
	}
}

func TestDoResponseCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxResponseBytes+1))
	}))
	defer server.Close()

	client := mustClient(t, testConfig(server.URL))
	err := client.do(context.Background(), http.MethodGet, "/zones", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeUpstreamUnavailable {
		t.Fatalf("code = %s, want %s", got, errs.CodeUpstreamUnavailable)
	}
	if !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("unexpected message: %v", err)
	}
}

func TestDoMalformedAndEmpty(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"malformed", `not-json`},
		{"empty", ``},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client := mustClient(t, testConfig(server.URL))
			var out struct{}
			err := client.do(context.Background(), http.MethodGet, "/zones", nil, &out)
			if got := errs.CodeOf(err); got != errs.CodeUpstreamUnavailable {
				t.Fatalf("code = %s, want %s", got, errs.CodeUpstreamUnavailable)
			}
		})
	}
}

func TestDoRejectsRelativePath(t *testing.T) {
	client := mustClient(t, testConfig("http://127.0.0.1:1"))
	err := client.do(context.Background(), http.MethodGet, "zones", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeInternal {
		t.Fatalf("code = %s, want %s", got, errs.CodeInternal)
	}
}

func TestGraphQLSuccess(t *testing.T) {
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":{"answer":42}}`
	})
	client := mustClient(t, testConfig(server.URL))
	var out struct {
		Answer int `json:"answer"`
	}
	query := `query Test($limit: uint64) { viewer { zones { httpRequests1hGroups(limit: $limit) { sum { requests } } } } }`
	if err := client.graphql(context.Background(), query, map[string]any{"limit": 5}, &out); err != nil {
		t.Fatalf("graphql: %v", err)
	}
	if out.Answer != 42 {
		t.Fatalf("answer = %d", out.Answer)
	}
	call := recorder.last(t)
	if call.Query != query {
		t.Fatalf("query = %q", call.Query)
	}
	if got, ok := call.Variables["limit"].(float64); !ok || got != 5 {
		t.Fatalf("limit variable = %#v", call.Variables["limit"])
	}
}

func TestGraphQLErrors(t *testing.T) {
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"errors":[{"message":"cannot request data older than 2678400s","extensions":{"secret":"leak"}}],"data":null}`
	})
	client := mustClient(t, testConfig(server.URL))
	err := client.graphql(context.Background(), "query Test { viewer { zones { date } } }", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeUpstreamUnavailable {
		t.Fatalf("code = %s, want %s", got, errs.CodeUpstreamUnavailable)
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not *errs.Error: %T", err)
	}
	// SEC-10: the upstream reason is kept for the server log, not the agent. It must
	// reach Upstream and must not appear in the serialised details.
	if typed.Upstream != "cannot request data older than 2678400s" {
		t.Fatalf("upstream = %q, want the reason kept server-side", typed.Upstream)
	}
	if _, leaked := typed.Details["upstream_message"]; leaked {
		t.Fatal("upstream_message leaked into the agent-visible details")
	}
	if strings.Contains(err.Error(), "leak") {
		t.Fatalf("error leaked the raw payload: %v", err)
	}
}

func TestGraphQLErrorMessageIsBounded(t *testing.T) {
	long := strings.Repeat("x", 300)
	server, _ := newFakeGraphQL(t, func(graphqlCall) string {
		payload, err := json.Marshal(map[string]any{"errors": []map[string]string{{"message": long}}})
		if err != nil {
			return `{"errors":[{"message":"encode failure"}]}`
		}
		return string(payload)
	})
	client := mustClient(t, testConfig(server.URL))
	err := client.graphql(context.Background(), "query Test { viewer { zones { date } } }", nil, nil)
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not *errs.Error: %T", err)
	}
	if got := len([]rune(typed.Upstream)); got != maxGraphQLErrorDetail {
		t.Fatalf("upstream length = %d, want %d", got, maxGraphQLErrorDetail)
	}
}

func TestGraphQLNoDataAndEmptyQuery(t *testing.T) {
	server, recorder := newFakeGraphQL(t, func(graphqlCall) string {
		return `{"data":null}`
	})
	client := mustClient(t, testConfig(server.URL))
	if got := errs.CodeOf(client.graphql(context.Background(), "query Test { viewer { zones { date } } }", nil, nil)); got != errs.CodeUpstreamUnavailable {
		t.Fatalf("null data code = %s", got)
	}
	if got := errs.CodeOf(client.graphql(context.Background(), "  ", nil, nil)); got != errs.CodeInternal {
		t.Fatalf("empty query code = %s", got)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d, want 1", recorder.count())
	}
}
