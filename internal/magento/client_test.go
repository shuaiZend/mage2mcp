// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// testSigner returns a deterministic OAuth 1.0a signer for tests.
func testSigner(t *testing.T) *OAuth1Signer {
	t.Helper()
	signer, err := NewOAuth1Signer("test-key", "test-secret", "test-token", "test-token-secret")
	if err != nil {
		t.Fatalf("NewOAuth1Signer: %v", err)
	}
	signer.SetNonceSource(fixedNonce("test-nonce", 1700000000))
	return signer
}

// testClient returns a client pointed at baseURL.
func testClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	client, err := NewClient(ClientConfig{
		BaseURL:   baseURL,
		StoreCode: "default",
		Signer:    testSigner(t),
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// testService returns a product service pointed at baseURL.
func testService(t *testing.T, baseURL string) *Service {
	t.Helper()
	service, err := NewService(testClient(t, baseURL))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service
}

func TestNewClientValidation(t *testing.T) {
	tests := []struct {
		name      string
		baseURL   string
		storeCode string
		timeout   time.Duration
		noSigner  bool
		wantErr   bool
	}{
		{name: "https", baseURL: "https://shop.example.com", storeCode: "default"},
		{name: "https with trailing slash", baseURL: "https://shop.example.com/", storeCode: "default"},
		{name: "loopback http", baseURL: "http://127.0.0.1:8080", storeCode: "default"},
		{name: "localhost http", baseURL: "http://localhost:8080", storeCode: "all"},
		{name: "remote http", baseURL: "http://shop.example.com", storeCode: "default", wantErr: true},
		{name: "missing scheme", baseURL: "shop.example.com", storeCode: "default", wantErr: true},
		{name: "userinfo", baseURL: "https://user:pass@shop.example.com", storeCode: "default", wantErr: true},
		{name: "query", baseURL: "https://shop.example.com?x=1", storeCode: "default", wantErr: true},
		{name: "fragment", baseURL: "https://shop.example.com#frag", storeCode: "default", wantErr: true},
		{name: "path", baseURL: "https://shop.example.com/shop", storeCode: "default", wantErr: true},
		{name: "empty store code", baseURL: "https://shop.example.com", storeCode: "", wantErr: true},
		{name: "uppercase store code", baseURL: "https://shop.example.com", storeCode: "Default", wantErr: true},
		{name: "hyphen store code", baseURL: "https://shop.example.com", storeCode: "default-store", wantErr: true},
		{name: "negative timeout", baseURL: "https://shop.example.com", storeCode: "default", timeout: -time.Second, wantErr: true},
		{name: "missing signer", baseURL: "https://shop.example.com", storeCode: "default", noSigner: true, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := ClientConfig{BaseURL: tc.baseURL, StoreCode: tc.storeCode, Timeout: tc.timeout}
			if !tc.noSigner {
				cfg.Signer = testSigner(t)
			}
			client, err := NewClient(cfg)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if tc.timeout == 0 && client.Timeout != DefaultTimeout {
				t.Fatalf("timeout = %s, want %s", client.Timeout, DefaultTimeout)
			}
		})
	}
}

func TestGetProductRequestAndResponse(t *testing.T) {
	var authHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if got, want := r.URL.EscapedPath(), "/rest/default/V1/products/SKU-1"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":7,"sku":"SKU-1","name":"Test Shirt","type_id":"simple",`+
			`"status":1,"visibility":4,"price":19.99,"attribute_set_id":4,`+
			`"created_at":"2026-01-01 00:00:00","updated_at":"2026-01-02 00:00:00"}`)
	}))
	defer ts.Close()

	product, err := testService(t, ts.URL).Get(context.Background(), "SKU-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if product.SKU != "SKU-1" || product.Name != "Test Shirt" || product.Price != 19.99 || product.ID != 7 {
		t.Fatalf("unexpected product: %+v", product)
	}
	if product.AttributeSetID != 4 || product.CreatedAt != "2026-01-01 00:00:00" {
		t.Fatalf("unexpected product: %+v", product)
	}
	fields := parseOAuthHeader(t, authHeader)
	if fields["oauth_consumer_key"] != "test-key" || fields["oauth_token"] != "test-token" {
		t.Fatalf("unexpected OAuth header: %v", fields)
	}
	if fields["oauth_signature"] == "" {
		t.Fatal("OAuth signature is missing")
	}
}

func TestGetRejectsInvalidSKUWithoutRequest(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer ts.Close()

	for _, sku := range []string{"", "-leading", "has space", strings.Repeat("A", 65)} {
		_, err := testService(t, ts.URL).Get(context.Background(), sku)
		if err == nil {
			t.Fatalf("Get(%q) must fail", sku)
		}
		var e *errs.Error
		if !errors.As(err, &e) || e.Code != errs.CodeInvalidArgument {
			t.Fatalf("Get(%q) code = %v, want invalid_argument", sku, err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid SKUs reached the upstream %d times", requests)
	}
}

func TestSearchProductQuery(t *testing.T) {
	var query map[string][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/rest/default/V1/products"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		query = r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[{"id":1,"sku":"SKU-1","name":"Test Shirt"}],"total_count":42}`)
	}))
	defer ts.Close()

	products, total, err := testService(t, ts.URL).Search(context.Background(), ProductQuery{
		SKU: "SKU-1", NameContains: "shirt", TypeID: "simple", Status: 1, Limit: 25, CursorOffset: 25,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 42 || len(products) != 1 || products[0].SKU != "SKU-1" {
		t.Fatalf("unexpected result: total=%d products=%+v", total, products)
	}
	want := map[string]string{
		"searchCriteria[pageSize]":                                     "25",
		"searchCriteria[currentPage]":                                  "2",
		"searchCriteria[filter_groups][0][filters][0][field]":          "sku",
		"searchCriteria[filter_groups][0][filters][0][value]":          "SKU-1",
		"searchCriteria[filter_groups][0][filters][0][condition_type]": "eq",
		"searchCriteria[filter_groups][1][filters][0][field]":          "name",
		"searchCriteria[filter_groups][1][filters][0][value]":          "%shirt%",
		"searchCriteria[filter_groups][1][filters][0][condition_type]": "like",
		"searchCriteria[filter_groups][2][filters][0][field]":          "type_id",
		"searchCriteria[filter_groups][2][filters][0][value]":          "simple",
		"searchCriteria[filter_groups][2][filters][0][condition_type]": "eq",
		"searchCriteria[filter_groups][3][filters][0][field]":          "status",
		"searchCriteria[filter_groups][3][filters][0][value]":          "1",
		"searchCriteria[filter_groups][3][filters][0][condition_type]": "eq",
	}
	for key, value := range want {
		if got := query[key]; len(got) != 1 || got[0] != value {
			t.Errorf("query %s = %v, want [%s]", key, got, value)
		}
	}
}

func TestSearchDefaultsPageSize(t *testing.T) {
	var query map[string][]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = io.WriteString(w, `{"items":[],"total_count":0}`)
	}))
	defer ts.Close()

	products, total, err := testService(t, ts.URL).Search(context.Background(), ProductQuery{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if total != 0 || len(products) != 0 {
		t.Fatalf("unexpected result: total=%d products=%+v", total, products)
	}
	if got := query["searchCriteria[pageSize]"]; len(got) != 1 || got[0] != "50" {
		t.Fatalf("pageSize = %v, want [50]", got)
	}
	if got := query["searchCriteria[currentPage]"]; len(got) != 1 || got[0] != "1" {
		t.Fatalf("currentPage = %v, want [1]", got)
	}
	if len(query) != 2 {
		t.Fatalf("unexpected query parameters: %v", query)
	}
}

func TestSearchValidation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("invalid query reached the upstream")
	}))
	defer ts.Close()

	queries := []ProductQuery{
		{Limit: -1},
		{Limit: 201},
		{CursorOffset: 5, Limit: 50},
		{SKU: "bad sku"},
		{TypeID: "Simple"},
		{Status: 3},
		{NameContains: strings.Repeat("x", 129)},
	}
	for _, q := range queries {
		if _, _, err := testService(t, ts.URL).Search(context.Background(), q); err == nil {
			t.Fatalf("Search(%+v) must fail", q)
		} else if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
			t.Fatalf("Search(%+v) code = %s, want invalid_argument", q, code)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		status    int
		code      errs.Code
		retryable bool
	}{
		{status: http.StatusUnauthorized, code: errs.CodeUnauthenticated},
		{status: http.StatusForbidden, code: errs.CodePermissionDenied},
		{status: http.StatusNotFound, code: errs.CodeNotFound},
		{status: http.StatusConflict, code: errs.CodeConflict},
		{status: http.StatusTooManyRequests, code: errs.CodeRateLimited, retryable: true},
		{status: http.StatusInternalServerError, code: errs.CodeUpstreamUnavailable, retryable: true},
		{status: http.StatusBadGateway, code: errs.CodeUpstreamUnavailable, retryable: true},
		{status: http.StatusBadRequest, code: errs.CodeValidationFailed},
		{status: http.StatusUnprocessableEntity, code: errs.CodeValidationFailed},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"SECRET-UPSTREAM-BODY","trace":"SECRET-TRACE"}`)
			}))
			defer ts.Close()

			_, err := testService(t, ts.URL).Get(context.Background(), "SKU-1")
			if err == nil {
				t.Fatal("expected an error")
			}
			var e *errs.Error
			if !errors.As(err, &e) {
				t.Fatalf("error is not *errs.Error: %v", err)
			}
			if e.Code != tc.code {
				t.Fatalf("code = %s, want %s", e.Code, tc.code)
			}
			if e.Retryable != tc.retryable {
				t.Fatalf("retryable = %v, want %v", e.Retryable, tc.retryable)
			}
			if e.Details["upstream_status"] != strconv.Itoa(tc.status) {
				t.Fatalf("upstream_status = %q, want %d", e.Details["upstream_status"], tc.status)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked the upstream body: %v", err)
			}
		})
	}
}

func TestTimeoutMapping(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	t.Run("client timeout", func(t *testing.T) {
		client, err := NewClient(ClientConfig{
			BaseURL: ts.URL, StoreCode: "default", Signer: testSigner(t), Timeout: 25 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewService(client)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Get(context.Background(), "SKU-1")
		if code := errs.CodeOf(err); code != errs.CodeUpstreamTimeout {
			t.Fatalf("code = %s, want upstream_timeout (%v)", code, err)
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		service := testService(t, ts.URL)
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_, err := service.Get(ctx, "SKU-1")
		if code := errs.CodeOf(err); code != errs.CodeUpstreamTimeout {
			t.Fatalf("code = %s, want upstream_timeout (%v)", code, err)
		}
	})
}
