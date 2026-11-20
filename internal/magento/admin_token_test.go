// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	testAdminUser     = "api-admin"
	testAdminPassword = "s3cr3t-pa:ss"
)

// adminTokenTestServer fakes the admin token endpoint and a product route.
// tokenFn/productFn may be nil to accept the default behaviour.
type adminTokenTestServer struct {
	*httptest.Server
	tokenCalls   atomic.Int64
	productCalls atomic.Int64
	tokenFn      func(call int64, r *http.Request) (status int, body string)
	productFn    func(call int64, r *http.Request) (status int, body string)
}

func newAdminTokenTestServer(t *testing.T, tokenFn, productFn func(int64, *http.Request) (int, string)) *adminTokenTestServer {
	t.Helper()
	srv := &adminTokenTestServer{tokenFn: tokenFn, productFn: productFn}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/rest/default/V1/integration/admin/token":
			call := srv.tokenCalls.Add(1)
			status, body := http.StatusOK, fmt.Sprintf("%q", fmt.Sprintf("tok-%d", call))
			if srv.tokenFn != nil {
				status, body = srv.tokenFn(call, r)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		case "/rest/default/V1/products":
			call := srv.productCalls.Add(1)
			status, body := http.StatusOK, `{"items":[]}`
			if srv.productFn != nil {
				status, body = srv.productFn(call, r)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			t.Errorf("unexpected request path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAdminTestSigner(t *testing.T, baseURL string) *AdminTokenSigner {
	t.Helper()
	signer, err := NewAdminTokenSigner(testAdminUser, testAdminPassword)
	if err != nil {
		t.Fatalf("NewAdminTokenSigner: %v", err)
	}
	if err := signer.Bind(baseURL, "default"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	return signer
}

func newAdminTestClient(t *testing.T, baseURL string) (*Client, *AdminTokenSigner) {
	t.Helper()
	signer := newAdminTestSigner(t, baseURL)
	client, err := NewClient(ClientConfig{
		BaseURL:    baseURL,
		StoreCode:  "default",
		Signer:     signer,
		HTTPClient: signer.HTTPClient(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, signer
}

func TestParseCredentialAdminForm(t *testing.T) {
	signer, err := ParseCredential("admin:" + testAdminUser + ":" + testAdminPassword)
	if err != nil {
		t.Fatalf("ParseCredential: %v", err)
	}
	admin, ok := signer.(*AdminTokenSigner)
	if !ok {
		t.Fatalf("ParseCredential returned %T, want *AdminTokenSigner", signer)
	}
	if admin.username != testAdminUser {
		t.Errorf("username = %q, want %q", admin.username, testAdminUser)
	}
	if admin.password != testAdminPassword {
		t.Errorf("password was not preserved")
	}
	for _, credential := range []string{
		"admin:",
		"admin:" + testAdminUser,
		"admin::" + testAdminPassword,
		"admin:" + testAdminUser + ":",
		"admin:bad\x00user:pw",
	} {
		if _, err := ParseCredential(credential); err == nil {
			t.Errorf("ParseCredential(%q) = nil error, want validation error", credential)
		}
	}
}

func TestParseCredentialBearerAndOAuth1Unchanged(t *testing.T) {
	signer, err := ParseCredential("bearer:integration-token")
	if err != nil {
		t.Fatalf("ParseCredential(bearer): %v", err)
	}
	bearer, ok := signer.(*BearerSigner)
	if !ok || bearer.Token != "integration-token" {
		t.Fatalf("bearer form changed: %T %+v", signer, signer)
	}
	if _, err := ParseCredential("oauth1:key:secret:token:token-secret"); err != nil {
		t.Fatalf("ParseCredential(oauth1): %v", err)
	}
	if _, err := ParseCredential("oauth1:too-few"); err == nil {
		t.Error("short oauth1 credential was accepted")
	}
	if _, err := ParseCredential("plain-token"); err == nil {
		t.Error("unknown credential form was accepted")
	}
}

func TestAdminTokenSignerRequiresInstanceBinding(t *testing.T) {
	signer, err := NewAdminTokenSigner(testAdminUser, testAdminPassword)
	if err != nil {
		t.Fatalf("NewAdminTokenSigner: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://shop.example.com/rest/default/V1/products", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	err = signer.Sign(req)
	if got := errs.CodeOf(err); got != errs.CodeInternal {
		t.Fatalf("Sign code = %s, want %s (err: %v)", got, errs.CodeInternal, err)
	}
	if !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("Sign error = %q, want a binding hint", err.Error())
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("unbound signer set an Authorization header")
	}
}

func TestAdminTokenSignerBindValidatesInstance(t *testing.T) {
	signer, err := NewAdminTokenSigner(testAdminUser, testAdminPassword)
	if err != nil {
		t.Fatalf("NewAdminTokenSigner: %v", err)
	}
	for _, baseURL := range []string{
		"",
		"ftp://shop.example.com",
		"https://shop.example.com/store",
		"https://shop.example.com?query=1",
		"http://shop.example.com",
	} {
		if err := signer.Bind(baseURL, "default"); err == nil {
			t.Errorf("Bind(%q) = nil error, want validation error", baseURL)
		}
	}
	for _, storeCode := range []string{"", "Bad Store", "default/extra"} {
		if err := signer.Bind("https://shop.example.com", storeCode); err == nil {
			t.Errorf("Bind(store_code=%q) = nil error, want validation error", storeCode)
		}
	}
	if err := signer.Bind("https://shop.example.com", "default"); err != nil {
		t.Fatalf("Bind(valid): %v", err)
	}
}

func TestAdminTokenSignerRebindDropsCachedToken(t *testing.T) {
	first := newAdminTokenTestServer(t, nil, nil)
	second := newAdminTokenTestServer(t, nil, nil)
	signer := newAdminTestSigner(t, first.URL)
	if _, err := signer.tokenFor(context.Background()); err != nil {
		t.Fatalf("tokenFor: %v", err)
	}
	if first.tokenCalls.Load() != 1 {
		t.Fatalf("first instance token calls = %d, want 1", first.tokenCalls.Load())
	}
	if err := signer.Bind(second.URL, "default"); err != nil {
		t.Fatalf("Bind(second): %v", err)
	}
	if _, err := signer.tokenFor(context.Background()); err != nil {
		t.Fatalf("tokenFor after rebind: %v", err)
	}
	if second.tokenCalls.Load() != 1 {
		t.Fatalf("second instance token calls = %d, want 1", second.tokenCalls.Load())
	}
	if first.tokenCalls.Load() != 1 {
		t.Fatalf("first instance was called again: %d", first.tokenCalls.Load())
	}
}

func TestAdminTokenSignerAcquiresAndCachesToken(t *testing.T) {
	srv := newAdminTokenTestServer(t, func(_ int64, r *http.Request) (int, string) {
		if r.Method != http.MethodPost {
			t.Errorf("token method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("token Content-Type = %q, want application/json", got)
		}
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("token body is not JSON: %v", err)
			return http.StatusBadRequest, `{}`
		}
		if payload.Username != testAdminUser || payload.Password != testAdminPassword {
			t.Errorf("token body carried the wrong credential")
		}
		return http.StatusOK, `"tok-1"`
	}, func(_ int64, r *http.Request) (int, string) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization = %q, want Bearer tok-1", got)
		}
		return http.StatusOK, `{"items":[]}`
	})
	client, signer := newAdminTestClient(t, srv.URL)
	for i := 0; i < 3; i++ {
		if _, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if got := srv.tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1 (token must be cached)", got)
	}
	if got := srv.productCalls.Load(); got != 3 {
		t.Fatalf("product calls = %d, want 3", got)
	}
	for _, rendered := range []string{
		signer.String(),
		fmt.Sprintf("%v", signer),
		fmt.Sprintf("%+v", signer),
		fmt.Sprintf("%#v", signer),
	} {
		if strings.Contains(rendered, testAdminPassword) || strings.Contains(rendered, "tok-1") {
			t.Fatalf("signer formatting leaked a credential: %q", rendered)
		}
	}
}

func TestAdminTokenSignerRefreshesBeforeExpiry(t *testing.T) {
	srv := newAdminTokenTestServer(t, nil, nil)
	signer := newAdminTestSigner(t, srv.URL)
	signer.ttl = time.Minute
	signer.skew = 10 * time.Second
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	signer.now = func() time.Time { return clock }

	sign := func() string {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if err := signer.Sign(req); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		return req.Header.Get("Authorization")
	}

	if got := sign(); got != "Bearer tok-1" {
		t.Fatalf("first token = %q, want Bearer tok-1", got)
	}
	clock = clock.Add(30 * time.Second)
	if got := sign(); got != "Bearer tok-1" {
		t.Fatalf("cached token = %q, want Bearer tok-1", got)
	}
	if got := srv.tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1 while the token is fresh", got)
	}
	clock = clock.Add(25 * time.Second) // 55s after mint: past the 50s refresh threshold
	if got := sign(); got != "Bearer tok-2" {
		t.Fatalf("refreshed token = %q, want Bearer tok-2", got)
	}
	if got := srv.tokenCalls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2 after the refresh window", got)
	}
}

func TestAdminTokenSignerRetriesOnceOn401(t *testing.T) {
	t.Run("expired token is replaced", func(t *testing.T) {
		srv := newAdminTokenTestServer(t, nil, func(_ int64, r *http.Request) (int, string) {
			switch r.Header.Get("Authorization") {
			case "Bearer tok-1":
				return http.StatusUnauthorized, `{"message":"The access token has expired"}`
			case "Bearer tok-2":
				return http.StatusOK, `{"items":[]}`
			default:
				t.Errorf("unexpected Authorization header %q", r.Header.Get("Authorization"))
				return http.StatusUnauthorized, `{}`
			}
		})
		client, _ := newAdminTestClient(t, srv.URL)
		if _, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil); err != nil {
			t.Fatalf("request after retry: %v", err)
		}
		if got := srv.productCalls.Load(); got != 2 {
			t.Fatalf("product calls = %d, want 2 (original + one retry)", got)
		}
		if got := srv.tokenCalls.Load(); got != 2 {
			t.Fatalf("token calls = %d, want 2", got)
		}
	})

	t.Run("write body is replayed on retry", func(t *testing.T) {
		srv := newAdminTokenTestServer(t, nil, func(_ int64, r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("retried body could not be decoded: %v", err)
				return http.StatusBadRequest, `{}`
			}
			if payload["sku"] != "SKU-1" {
				t.Errorf("retried body = %v, want sku SKU-1", payload)
			}
			if r.Header.Get("Authorization") == "Bearer tok-1" {
				return http.StatusUnauthorized, `{"message":"The access token has expired"}`
			}
			return http.StatusOK, `{"sku":"SKU-1"}`
		})
		client, _ := newAdminTestClient(t, srv.URL)
		if _, err := client.do(context.Background(), http.MethodPost, "/products", nil, map[string]string{"sku": "SKU-1"}); err != nil {
			t.Fatalf("POST after retry: %v", err)
		}
		if got := srv.productCalls.Load(); got != 2 {
			t.Fatalf("product calls = %d, want 2", got)
		}
	})

	t.Run("second 401 is returned, not retried again", func(t *testing.T) {
		srv := newAdminTokenTestServer(t, nil, func(_ int64, _ *http.Request) (int, string) {
			return http.StatusUnauthorized, `{"message":"unauthorized"}`
		})
		client, _ := newAdminTestClient(t, srv.URL)
		_, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil)
		if got := errs.CodeOf(err); got != errs.CodeUnauthenticated {
			t.Fatalf("error code = %s, want %s (err: %v)", got, errs.CodeUnauthenticated, err)
		}
		if got := srv.productCalls.Load(); got != 2 {
			t.Fatalf("product calls = %d, want exactly 2", got)
		}
		if got := srv.tokenCalls.Load(); got != 2 {
			t.Fatalf("token calls = %d, want exactly 2", got)
		}
	})
}

func TestAdminTokenSignerRejectsInvalidCredentials(t *testing.T) {
	const upstream = `{"message":"The account sign-in was incorrect or your account is disabled temporarily."}`
	srv := newAdminTokenTestServer(t, func(_ int64, _ *http.Request) (int, string) {
		return http.StatusUnauthorized, upstream
	}, nil)
	client, _ := newAdminTestClient(t, srv.URL)
	_, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeUnauthenticated {
		t.Fatalf("error code = %s, want %s (err: %v)", got, errs.CodeUnauthenticated, err)
	}
	if strings.Contains(err.Error(), testAdminPassword) {
		t.Fatalf("error leaked the password: %v", err)
	}
	if strings.Contains(err.Error(), "sign-in") {
		t.Fatalf("error echoed the upstream body: %v", err)
	}
	if got := srv.tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}
	if got := srv.productCalls.Load(); got != 0 {
		t.Fatalf("product calls = %d, want 0", got)
	}
}

func TestAdminTokenSignerBoundsTokenRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(`"tok-late"`))
	}))
	defer srv.Close()
	signer := newAdminTestSigner(t, srv.URL)
	signer.timeout = 25 * time.Millisecond
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	err = signer.Sign(req)
	if got := errs.CodeOf(err); got != errs.CodeUpstreamTimeout {
		t.Fatalf("error code = %s, want %s (err: %v)", got, errs.CodeUpstreamTimeout, err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("timed-out token request still signed the request")
	}
}

func TestAdminTokenSignerRejectsUnusableToken(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty body", ""},
		{"empty string", `""`},
		{"empty envelope", `{"token":""}`},
		{"unexpected envelope", `{"access_token":"tok"}`},
		{"not json", `not-json`},
		{"header injection", `"tok\nInjected: yes"`},
		{"oversized", `"` + strings.Repeat("a", maxAdminTokenLen+1) + `"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAdminTokenTestServer(t, func(_ int64, _ *http.Request) (int, string) {
				return http.StatusOK, tc.body
			}, nil)
			signer := newAdminTestSigner(t, srv.URL)
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			err = signer.Sign(req)
			if got := errs.CodeOf(err); got != errs.CodeUpstreamUnavailable {
				t.Fatalf("error code = %s, want %s (err: %v)", got, errs.CodeUpstreamUnavailable, err)
			}
			if req.Header.Get("Authorization") != "" {
				t.Fatal("unusable token was still applied")
			}
		})
	}

	t.Run("object envelope is accepted", func(t *testing.T) {
		srv := newAdminTokenTestServer(t, func(_ int64, _ *http.Request) (int, string) {
			return http.StatusOK, `{"token":"tok-object"}`
		}, nil)
		signer := newAdminTestSigner(t, srv.URL)
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if err := signer.Sign(req); err != nil {
			t.Fatalf("Sign: %v", err)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer tok-object" {
			t.Fatalf("Authorization = %q, want Bearer tok-object", got)
		}
	})
}

func TestAdminTokenSignerDoesNotFollowRedirect(t *testing.T) {
	var leaked atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		leaked.Add(1)
		_, _ = w.Write([]byte(`"leaked-token"`))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	signer := newAdminTestSigner(t, srv.URL)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if err := signer.Sign(req); err == nil {
		t.Fatal("Sign followed a redirect instead of failing")
	}
	if leaked.Load() != 0 {
		t.Fatal("the admin credential was replayed to the redirect target")
	}
}

func TestAdminTokenSignerMintsOneTokenForConcurrentSigns(t *testing.T) {
	srv := newAdminTokenTestServer(t, func(_ int64, _ *http.Request) (int, string) {
		time.Sleep(10 * time.Millisecond)
		return http.StatusOK, `"tok-shared"`
	}, nil)
	signer := newAdminTestSigner(t, srv.URL)
	const workers = 8
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
			if err != nil {
				t.Errorf("NewRequest: %v", err)
				return
			}
			if err := signer.Sign(req); err != nil {
				t.Errorf("Sign: %v", err)
				return
			}
			if got := req.Header.Get("Authorization"); got != "Bearer tok-shared" {
				t.Errorf("Authorization = %q, want Bearer tok-shared", got)
			}
		}()
	}
	wg.Wait()
	if got := srv.tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1 for concurrent signers", got)
	}
}

func TestNewResolverFromSettingsAdminCredential(t *testing.T) {
	srv := newAdminTokenTestServer(t, nil, func(_ int64, r *http.Request) (int, string) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok-1" {
			t.Errorf("Authorization = %q, want Bearer tok-1", got)
		}
		return http.StatusOK, `{"items":[]}`
	})
	resolver, err := NewResolverFromSettings([]InstanceSettings{{
		ID:         "store-us",
		Endpoint:   srv.URL,
		StoreCode:  "default",
		Credential: "admin:" + testAdminUser + ":" + testAdminPassword,
	}})
	if err != nil {
		t.Fatalf("NewResolverFromSettings: %v", err)
	}
	svc, err := resolver.Resolve("store-us")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := svc.client.do(context.Background(), http.MethodGet, "/products", nil, nil); err != nil {
		t.Fatalf("request through resolver: %v", err)
	}
	if got := srv.tokenCalls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1", got)
	}
	if got := srv.productCalls.Load(); got != 1 {
		t.Fatalf("product calls = %d, want 1", got)
	}

	_, err = NewResolverFromSettings([]InstanceSettings{{
		ID:         "store-eu",
		Endpoint:   srv.URL,
		StoreCode:  "default",
		Credential: "admin::" + testAdminPassword,
	}})
	if err == nil || !strings.Contains(err.Error(), "store-eu") {
		t.Fatalf("invalid admin credential error = %v, want an instance-scoped error", err)
	}
}

func TestAdminTokenSignerTokenEndpointStatusErrors(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		want      errs.Code
		retryable bool
	}{
		{"bad request", http.StatusBadRequest, errs.CodeValidationFailed, false},
		{"forbidden", http.StatusForbidden, errs.CodeUnauthenticated, false},
		{"not found", http.StatusNotFound, errs.CodeNotFound, false},
		{"rate limited", http.StatusTooManyRequests, errs.CodeRateLimited, true},
		{"server error", http.StatusServiceUnavailable, errs.CodeUpstreamUnavailable, true},
		{"redirect", http.StatusFound, errs.CodeUpstreamUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newAdminTokenTestServer(t, func(_ int64, _ *http.Request) (int, string) {
				return tc.status, `{"message":"upstream detail must not leak"}`
			}, nil)
			signer := newAdminTestSigner(t, srv.URL)
			req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/default/V1/products", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			err = signer.Sign(req)
			if got := errs.CodeOf(err); got != tc.want {
				t.Fatalf("error code = %s, want %s (err: %v)", got, tc.want, err)
			}
			if got := errs.RetryableError(err); got != tc.retryable {
				t.Fatalf("retryable = %v, want %v (err: %v)", got, tc.retryable, err)
			}
			if strings.Contains(err.Error(), "upstream detail") {
				t.Fatalf("error echoed the upstream body: %v", err)
			}
			if strings.Contains(err.Error(), testAdminPassword) {
				t.Fatalf("error leaked the password: %v", err)
			}
		})
	}
}

func TestAdminTokenSignerKeepsOriginal401WhenRefreshFails(t *testing.T) {
	srv := newAdminTokenTestServer(t, func(call int64, _ *http.Request) (int, string) {
		if call == 1 {
			return http.StatusOK, `"tok-1"`
		}
		return http.StatusUnauthorized, `{"message":"The account sign-in was incorrect."}`
	}, func(_ int64, _ *http.Request) (int, string) {
		return http.StatusUnauthorized, `{"message":"The access token has expired"}`
	})
	client, _ := newAdminTestClient(t, srv.URL)
	_, err := client.do(context.Background(), http.MethodGet, "/products", nil, nil)
	if got := errs.CodeOf(err); got != errs.CodeUnauthenticated {
		t.Fatalf("error code = %s, want %s (err: %v)", got, errs.CodeUnauthenticated, err)
	}
	if got := srv.tokenCalls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2", got)
	}
	if got := srv.productCalls.Load(); got != 1 {
		t.Fatalf("product calls = %d, want 1", got)
	}
}

func TestAdminTokenRetryTransportDoesNotReplayUnrewindableBody(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	signer := newAdminTestSigner(t, srv.URL)
	transport := &adminTokenRetryTransport{signer: signer}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/rest/default/V1/products", io.NopCloser(strings.NewReader(`{"sku":"SKU-1"}`)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (a body without GetBody must not be replayed)", got)
	}
}
