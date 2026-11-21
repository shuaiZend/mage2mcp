// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// TestNewClientInstallsTunedTransport proves the client no longer inherits
// http.DefaultTransport (MaxIdleConnsPerHost = 2), which forced a new TCP+TLS
// handshake for most concurrent requests.
func TestNewClientInstallsTunedTransport(t *testing.T) {
	client, err := NewClient(ClientConfig{BaseURL: "https://shop.example.com", StoreCode: "default", Signer: testSigner(t)})
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.HTTPClient.Transport)
	}
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"MaxIdleConns", transport.MaxIdleConns, defaultMaxIdleConns},
		{"MaxIdleConnsPerHost", transport.MaxIdleConnsPerHost, defaultMaxIdleConnsPerHost},
		{"MaxConnsPerHost", transport.MaxConnsPerHost, defaultMaxConnsPerHost},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
	if transport.IdleConnTimeout != defaultIdleConnTimeout {
		t.Fatalf("IdleConnTimeout = %s, want %s", transport.IdleConnTimeout, defaultIdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != defaultTLSHandshakeTimeout {
		t.Fatalf("TLSHandshakeTimeout = %s, want %s", transport.TLSHandshakeTimeout, defaultTLSHandshakeTimeout)
	}
	if transport.MaxIdleConnsPerHost <= http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost {
		t.Fatalf("MaxIdleConnsPerHost = %d, must exceed the default transport's %d",
			transport.MaxIdleConnsPerHost, http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost)
	}

	t.Run("supplied client without a transport is tuned", func(t *testing.T) {
		supplied := &http.Client{}
		got, err := NewClient(ClientConfig{
			BaseURL: "https://shop.example.com", StoreCode: "default", Signer: testSigner(t), HTTPClient: supplied,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.HTTPClient.Transport.(*http.Transport); !ok {
			t.Fatalf("transport = %T, want the tuned *http.Transport", got.HTTPClient.Transport)
		}
	})

	t.Run("supplied transport is preserved", func(t *testing.T) {
		custom := &http.Transport{MaxIdleConnsPerHost: 3}
		got, err := NewClient(ClientConfig{
			BaseURL: "https://shop.example.com", StoreCode: "default", Signer: testSigner(t),
			HTTPClient: &http.Client{Transport: custom},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.HTTPClient.Transport != custom {
			t.Fatal("a caller-supplied transport must not be replaced")
		}
	})
}

// TestClientReusesConnections measures the pool behaviour the audit flagged:
// sequential requests must reuse one connection instead of paying a handshake
// each time.
func TestClientReusesConnections(t *testing.T) {
	var newConns int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(productJSONForTest("SKU-1"))
	}))
	ts.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	ts.Start()
	defer ts.Close()

	svc := testService(t, ts.URL)
	for i := 0; i < 40; i++ {
		if _, err := svc.Get(context.Background(), "SKU-1"); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&newConns); got != 1 {
		t.Fatalf("connections opened = %d for 40 sequential requests, want 1", got)
	}

	t.Run("concurrent burst stays inside the pool", func(t *testing.T) {
		var burstConns int32
		burst := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(productJSONForTest("SKU-1"))
		}))
		burst.Config.ConnState = func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				atomic.AddInt32(&burstConns, 1)
			}
		}
		burst.Start()
		defer burst.Close()

		burstSvc := testService(t, burst.URL)
		const workers, perWorker = 8, 20
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					if _, err := burstSvc.Get(context.Background(), "SKU-1"); err != nil {
						t.Errorf("burst request: %v", err)
						return
					}
					time.Sleep(time.Millisecond)
				}
			}()
		}
		wg.Wait()
		// The audit measured 41-64 connections for this shape with
		// http.DefaultTransport (MaxIdleConnsPerHost = 2). The tuned pool keeps
		// one connection per worker.
		got := atomic.LoadInt32(&burstConns)
		t.Logf("connections for %d concurrent workers x %d requests = %d", workers, perWorker, got)
		if got > int32(workers*2) {
			t.Fatalf("connections opened = %d for %d concurrent workers, want <= %d",
				got, workers, workers*2)
		}
	})
}

func productJSONForTest(sku string) []byte {
	return []byte(`{"id":1,"sku":"` + sku + `","name":"P","type_id":"simple","price":9.5,` +
		`"status":1,"visibility":4,"attribute_set_id":4}`)
}

// TestResponseTooLargeIsExplicit covers the audit's silent-truncation finding:
// a body over the cap must produce a typed error that names the limit and the
// overshoot, not "Magento returned an unreadable product payload".
func TestResponseTooLargeIsExplicit(t *testing.T) {
	overCap := func(extra int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":1,"sku":"BIG","name":"`))
			_, _ = io.CopyN(w, strings.NewReader(strings.Repeat("a", maxResponseBytes+extra)), int64(maxResponseBytes+extra))
			_, _ = w.Write([]byte(`"}`))
		}))
	}

	t.Run("one byte over the cap", func(t *testing.T) {
		ts := overCap(0)
		defer ts.Close()
		_, err := testService(t, ts.URL).Get(context.Background(), "BIG")
		if err == nil {
			t.Fatal("expected an error for an oversized body")
		}
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("error %v does not wrap *ResponseTooLargeError", err)
		}
		if tooLarge.Limit != int64(maxResponseBytes) {
			t.Fatalf("Limit = %d, want %d", tooLarge.Limit, maxResponseBytes)
		}
		if !tooLarge.Exact || tooLarge.Observed <= int64(maxResponseBytes) {
			t.Fatalf("Observed = %d exact=%v, want an exact size above the limit", tooLarge.Observed, tooLarge.Exact)
		}
		var typed *errs.Error
		if !errors.As(err, &typed) {
			t.Fatalf("error is not *errs.Error: %v", err)
		}
		if typed.Code != errs.CodeUpstreamUnavailable {
			t.Fatalf("code = %s, want upstream_unavailable", typed.Code)
		}
		if typed.Details["limit_bytes"] != strconv.Itoa(maxResponseBytes) {
			t.Fatalf("limit_bytes = %q, want %d", typed.Details["limit_bytes"], maxResponseBytes)
		}
		if typed.Details["observed_bytes"] != strconv.FormatInt(tooLarge.Observed, 10) {
			t.Fatalf("observed_bytes = %q, want %d", typed.Details["observed_bytes"], tooLarge.Observed)
		}
		if typed.Details["overage_bytes"] == "" || typed.Details["overage_bytes"] == "0" {
			t.Fatalf("overage_bytes = %q, want a positive overshoot", typed.Details["overage_bytes"])
		}
		if strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("error still blames the payload: %v", err)
		}
		if !strings.Contains(err.Error(), "exceeded") || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("error does not name the limit: %v", err)
		}
	})

	t.Run("far over the cap reports the overshoot", func(t *testing.T) {
		ts := overCap(64 << 10)
		defer ts.Close()
		_, err := testService(t, ts.URL).Get(context.Background(), "BIG")
		var tooLarge *ResponseTooLargeError
		if !errors.As(err, &tooLarge) {
			t.Fatalf("error %v does not wrap *ResponseTooLargeError", err)
		}
		if want := int64(maxResponseBytes + 64<<10 + len(`{"id":1,"sku":"BIG","name":"`+"\"}")); tooLarge.Observed != want {
			t.Fatalf("Observed = %d, want %d", tooLarge.Observed, want)
		}
	})

	t.Run("exactly at the cap is accepted", func(t *testing.T) {
		body := []byte(`{"id":1,"sku":"BIG","name":"`)
		body = append(body, []byte(strings.Repeat("a", maxResponseBytes-len(body)-2))...)
		body = append(body, []byte(`"}`)...)
		if len(body) != maxResponseBytes {
			t.Fatalf("fixture is %d bytes, want %d", len(body), maxResponseBytes)
		}
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		defer ts.Close()
		product, err := testService(t, ts.URL).Get(context.Background(), "BIG")
		if err != nil {
			t.Fatalf("a body of exactly the limit must be accepted: %v", err)
		}
		if product.SKU != "BIG" {
			t.Fatalf("sku = %q", product.SKU)
		}
	})
}

// A 400 that carries Magento's errors[] must reach the caller with the reason and
// the field name, not just "verify store_code and field values". Reverting the
// extraction in statusError makes every assertion here fail.
func TestStatusErrorPreservesUpstreamMessageAndField(t *testing.T) {
	body := []byte(`{"message":"Validation Failed","errors":[` +
		`{"message":"\"%field\" can not be empty.","parameters":{"field":"postcode"}}]}`)
	err := statusError(http.MethodPost, "/inventory/sources", http.StatusBadRequest, body, "pl")
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("statusError returned %T, want a taxonomy error", err)
	}
	if got := errs.CodeOf(err); got != errs.CodeValidationFailed {
		t.Fatalf("code = %s, want validation_failed", got)
	}
	// The agent-facing message stays generic: upstream text must not land here.
	if strings.Contains(e.Message, "postcode") || strings.Contains(e.Message, "Validation Failed") {
		t.Fatalf("message = %q, want the generic sentence (upstream text belongs in details)", e.Message)
	}
	if got := e.Details["upstream_field"]; got != "postcode" {
		t.Fatalf("upstream_field = %q, want postcode", got)
	}
	message := e.Details["upstream_message"]
	if !strings.Contains(message, "postcode") || !strings.Contains(message, "can not be empty") {
		t.Fatalf("upstream_message = %q, want the placeholder resolved to the real field", message)
	}
	if e.Upstream == "" {
		t.Fatal("the unscrubbed text must still reach the server log via Upstream")
	}
}

// A business-rule rejection has no parameters but is still the whole point of the
// diagnostic: the operator must see the rule, not a field hint.
func TestStatusErrorPreservesBusinessRuleMessage(t *testing.T) {
	body := []byte(`{"message":"Validation Failed","errors":[` +
		`{"message":"Can not save link related to Default Source or Default Stock","parameters":[]}]}`)
	err := statusError(http.MethodPost, "/inventory/stock-source-links", http.StatusBadRequest, body, "pl")
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatal("want a taxonomy error")
	}
	if got := e.Details["upstream_message"]; !strings.Contains(got, "Default Source or Default Stock") {
		t.Fatalf("upstream_message = %q, want Magento's business rule", got)
	}
	if _, ok := e.Details["upstream_field"]; ok {
		t.Fatal("a message without parameters must not invent a field")
	}
}

// The reason is carried in details, so it must not carry PII or a credential-shaped
// value even when Magento echoes one.
func TestStatusErrorScrubsSensitiveUpstreamText(t *testing.T) {
	body := []byte(`{"message":"Validation Failed","errors":[` +
		`{"message":"Customer demo.admin+tag@example.com failed; token 0123456789abcdef0123456789abcdef","parameters":[]}]}`)
	err := statusError(http.MethodPost, "/customers", http.StatusBadRequest, body, "pl")
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatal("want a taxonomy error")
	}
	message := e.Details["upstream_message"]
	if strings.Contains(message, "demo.admin+tag@example.com") {
		t.Fatalf("upstream_message leaked an email address: %q", message)
	}
	if strings.Contains(message, "0123456789abcdef0123456789abcdef") {
		t.Fatalf("upstream_message leaked a credential-shaped value: %q", message)
	}
	if !strings.Contains(message, "<email>") || !strings.Contains(message, "<redacted>") {
		t.Fatalf("upstream_message = %q, want the redaction markers", message)
	}
}

// A bare top-level message still reaches details, and an unparseable body must not
// panic or invent one.
func TestStatusErrorHandlesPlainAndOpaqueBodies(t *testing.T) {
	plain := statusError(http.MethodPost, "/orders", http.StatusBadRequest,
		[]byte(`{"message":"We can't create a shipment without products."}`), "pl")
	var e *errs.Error
	if !errors.As(plain, &e) {
		t.Fatal("want a taxonomy error")
	}
	if got := e.Details["upstream_message"]; !strings.Contains(got, "without products") {
		t.Fatalf("upstream_message = %q, want the plain message", got)
	}

	opaque := statusError(http.MethodPost, "/orders", http.StatusBadRequest, []byte(`<html>bad gateway</html>`), "pl")
	var o *errs.Error
	if !errors.As(opaque, &o) {
		t.Fatal("want a taxonomy error")
	}
	if _, ok := o.Details["upstream_message"]; ok {
		t.Fatal("an unparseable body must not produce an upstream_message")
	}
	if got := errs.CodeOf(opaque); got != errs.CodeValidationFailed {
		t.Fatalf("code = %s, want validation_failed", got)
	}
}

// SEM-17: route redaction must keep action words and redact only identifiers.
func TestRedactedRouteKeepsActionWords(t *testing.T) {
	cases := map[string]string{
		"/stockItems/WJ01":                     "/stockItems/{id}",
		"/configurable-products/WJ01/children": "/configurable-products/{id}/children",
		"/invoices/123/capture":                "/invoices/{id}/capture",
		"/order/1/refund":                      "/order/{id}/refund",
		"/shipments/1":                         "/shipments/{id}",
	}
	for in, want := range cases {
		if got := redactedRoute(in); got != want {
			t.Errorf("redactedRoute(%q) = %q, want %q", in, got, want)
		}
	}
}
