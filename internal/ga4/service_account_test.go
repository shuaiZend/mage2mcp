// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

const testServiceAccountEmail = "ga4-test@example.iam.gserviceaccount.com"

// testRSAKey generates a fresh RSA key for service-account tests.
func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

// mustJSON marshals value or fails the test.
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// serviceAccountJSON builds a Google service-account key document.
func serviceAccountJSON(t *testing.T, key *rsa.PrivateKey, tokenURI string) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal PKCS#8 key: %v", err)
	}
	payload := map[string]string{
		"type":         "service_account",
		"client_email": testServiceAccountEmail,
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	}
	if tokenURI != "" {
		payload["token_uri"] = tokenURI
	}
	return mustJSON(t, payload)
}

// tokenEndpoint starts a fake OAuth token endpoint that verifies the signed
// assertion and returns a token derived from the request count. It returns the
// server and the number of token exchanges it served.
func tokenEndpoint(t *testing.T, publicKey *rsa.PublicKey) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var requests atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("token method = %q, want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token form: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.PostForm.Get("grant_type"); got != jwtBearerGrantType {
			t.Errorf("grant_type = %q, want %q", got, jwtBearerGrantType)
		}
		checkJWTAssertion(t, r.PostForm.Get("assertion"), publicKey, server.URL)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"access-token-%d","expires_in":3600,"token_type":"Bearer"}`, count)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

// checkJWTAssertion verifies the RS256 signature and the standard claims. It
// uses Errorf because it runs on the httptest handler goroutine.
func checkJWTAssertion(t *testing.T, assertion string, publicKey *rsa.PublicKey, audience string) {
	parts := strings.Split(assertion, ".")
	if len(parts) != 3 {
		t.Errorf("assertion has %d segments, want 3", len(parts))
		return
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Errorf("decode assertion header: %v", err)
		return
	}
	var header map[string]string
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Errorf("unmarshal assertion header: %v", err)
		return
	}
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("assertion header = %+v", header)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Errorf("decode assertion signature: %v", err)
		return
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("assertion signature: %v", err)
		return
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Errorf("decode assertion claims: %v", err)
		return
	}
	var claims struct {
		Issuer string `json:"iss"`
		Scope  string `json:"scope"`
		Aud    string `json:"aud"`
		Issued int64  `json:"iat"`
		Expiry int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Errorf("unmarshal assertion claims: %v", err)
		return
	}
	if claims.Issuer != testServiceAccountEmail {
		t.Errorf("assertion iss = %q", claims.Issuer)
	}
	if claims.Scope != analyticsReadOnlyScope {
		t.Errorf("assertion scope = %q, want %q", claims.Scope, analyticsReadOnlyScope)
	}
	if claims.Aud != audience {
		t.Errorf("assertion aud = %q, want %q", claims.Aud, audience)
	}
	if claims.Expiry-claims.Issued != int64(serviceAccountJWTLife/time.Second) {
		t.Errorf("assertion lifetime = %ds", claims.Expiry-claims.Issued)
	}
}

func TestNewServiceAccountTokenSource(t *testing.T) {
	key := testRSAKey(t)
	t.Run("defaults token URI", func(t *testing.T) {
		source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, ""), nil)
		if err != nil {
			t.Fatalf("NewServiceAccountTokenSource: %v", err)
		}
		if source.tokenURI != defaultTokenURI {
			t.Fatalf("tokenURI = %q, want %q", source.tokenURI, defaultTokenURI)
		}
		if source.clientEmail != testServiceAccountEmail {
			t.Fatalf("clientEmail = %q", source.clientEmail)
		}
		if source.httpClient == nil || source.now == nil {
			t.Fatal("defaults must supply an HTTP client and clock")
		}
	})
	t.Run("honours token URI", func(t *testing.T) {
		source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, "https://oauth.example/token"), nil)
		if err != nil {
			t.Fatalf("NewServiceAccountTokenSource: %v", err)
		}
		if source.tokenURI != "https://oauth.example/token" {
			t.Fatalf("tokenURI = %q", source.tokenURI)
		}
	})
	t.Run("accepts PKCS#1 fallback", func(t *testing.T) {
		credentials := mustJSON(t, map[string]string{
			"client_email": testServiceAccountEmail,
			"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})),
		})
		if _, err := NewServiceAccountTokenSource(credentials, nil); err != nil {
			t.Fatalf("NewServiceAccountTokenSource: %v", err)
		}
	})
	badPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("not-a-key")}))
	cases := []struct {
		name        string
		credentials []byte
	}{
		{"malformed JSON", []byte("{")},
		{"missing client_email", mustJSON(t, map[string]string{"private_key": "x"})},
		{"missing private_key", mustJSON(t, map[string]string{"client_email": testServiceAccountEmail})},
		{"non-PEM key", mustJSON(t, map[string]string{"client_email": testServiceAccountEmail, "private_key": "not-pem"})},
		{"invalid PKCS#8", mustJSON(t, map[string]string{"client_email": testServiceAccountEmail, "private_key": badPEM})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewServiceAccountTokenSource(tc.credentials, nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestServiceAccountTokenCaches(t *testing.T) {
	key := testRSAKey(t)
	server, requests := tokenEndpoint(t, &key.PublicKey)
	source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
	if err != nil {
		t.Fatalf("NewServiceAccountTokenSource: %v", err)
	}
	ctx := context.Background()
	first, err := source.Token(ctx)
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	second, err := source.Token(ctx)
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if first != second {
		t.Fatalf("cached token changed: %q then %q", first, second)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestServiceAccountTokenRefreshesNearExpiry(t *testing.T) {
	key := testRSAKey(t)
	server, requests := tokenEndpoint(t, &key.PublicKey)
	source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
	if err != nil {
		t.Fatalf("NewServiceAccountTokenSource: %v", err)
	}
	current := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	source.now = func() time.Time { return current }
	ctx := context.Background()

	first, err := source.Token(ctx)
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	// One second before the refresh window opens the token is still valid.
	current = current.Add(serviceAccountJWTLife - tokenRefreshWindow - time.Second)
	cached, err := source.Token(ctx)
	if err != nil {
		t.Fatalf("cached Token: %v", err)
	}
	if cached != first || requests.Load() != 1 {
		t.Fatalf("token refreshed too early: %q, requests=%d", cached, requests.Load())
	}
	// Crossing into the 60-second refresh window forces a new exchange.
	current = current.Add(2 * time.Second)
	refreshed, err := source.Token(ctx)
	if err != nil {
		t.Fatalf("refreshed Token: %v", err)
	}
	if refreshed == first {
		t.Fatalf("token was not refreshed: %q", refreshed)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("token requests = %d, want 2", got)
	}
}

func TestServiceAccountTokenSingleFlight(t *testing.T) {
	key := testRSAKey(t)
	server, requests := tokenEndpoint(t, &key.PublicKey)
	source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
	if err != nil {
		t.Fatalf("NewServiceAccountTokenSource: %v", err)
	}
	const callers = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	tokens := make([]string, callers)
	errsByCaller := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			tokens[index], errsByCaller[index] = source.Token(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errsByCaller {
		if err != nil {
			t.Fatalf("caller %d: %v", i, err)
		}
		if tokens[i] != tokens[0] {
			t.Fatalf("caller %d token = %q, want %q", i, tokens[i], tokens[0])
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("token requests = %d, want 1", got)
	}
}

func TestServiceAccountTokenEndpointError(t *testing.T) {
	key := testRSAKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`)
	}))
	t.Cleanup(server.Close)
	source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
	if err != nil {
		t.Fatalf("NewServiceAccountTokenSource: %v", err)
	}
	_, err = source.Token(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := errs.CodeOf(err); got != errs.CodeUnauthenticated {
		t.Fatalf("code = %s, want %s (%v)", got, errs.CodeUnauthenticated, err)
	}
	if got := errorDetail(t, err, "reason"); got != "token_endpoint" {
		t.Fatalf("reason = %q", got)
	}
	for _, forbidden := range []string{"PRIVATE KEY", "assertion", "invalid_grant", "Invalid JWT"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaks %q: %v", forbidden, err)
		}
	}
}

func TestServiceAccountTokenResponseFailures(t *testing.T) {
	key := testRSAKey(t)
	cases := []struct {
		name string
		body string
	}{
		{"oversized response", strings.Repeat("a", tokenResponseLimit+1)},
		{"malformed JSON", `{`},
		{"missing access token", `{"expires_in":3600}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
			if err != nil {
				t.Fatalf("NewServiceAccountTokenSource: %v", err)
			}
			_, err = source.Token(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := errs.CodeOf(err); got != errs.CodeUnauthenticated {
				t.Fatalf("code = %s, want %s (%v)", got, errs.CodeUnauthenticated, err)
			}
			if got := errorDetail(t, err, "reason"); got != "token_endpoint" {
				t.Fatalf("reason = %q", got)
			}
		})
	}
}

func TestServiceAccountTokenContextCancellation(t *testing.T) {
	key := testRSAKey(t)
	server, requests := tokenEndpoint(t, &key.PublicKey)
	source, err := NewServiceAccountTokenSource(serviceAccountJSON(t, key, server.URL), server.Client())
	if err != nil {
		t.Fatalf("NewServiceAccountTokenSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.Token(ctx); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("token requests = %d, want 0", got)
	}
}
