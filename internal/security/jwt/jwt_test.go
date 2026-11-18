// SPDX-License-Identifier: Apache-2.0

package jwt

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

const (
	testIssuer   = "https://issuer.example"
	testAudience = "mage2mcp"
)

type fixture struct {
	t        *testing.T
	server   *httptest.Server
	rsaKey   *rsa.PrivateKey
	ecKey    *ecdsa.PrivateKey
	rsaKid   string
	ecKid    string
	requests atomic.Int64
	status   atomic.Int64
	keys     atomic.Value // []map[string]any
	logs     *bytes.Buffer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate EC key: %v", err)
	}
	f := &fixture{t: t, rsaKey: rsaKey, ecKey: ecKey, rsaKid: "rsa-1", ecKid: "ec-1", logs: &bytes.Buffer{}}
	f.keys.Store([]map[string]any{f.rsaJWK(), f.ecJWK()})
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.requests.Add(1)
		if status := f.status.Load(); status != 0 {
			w.WriteHeader(int(status))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": f.keys.Load()})
	}))
	t.Cleanup(f.server.Close)
	return f
}

func b64url(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func pad32(v *big.Int) []byte {
	out := make([]byte, 32)
	v.FillBytes(out)
	return out
}

func (f *fixture) rsaJWK() map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": f.rsaKid, "use": "sig", "alg": "RS256",
		"n": b64url(f.rsaKey.N.Bytes()),
		"e": b64url(big.NewInt(int64(f.rsaKey.E)).Bytes()),
	}
}

func (f *fixture) ecJWK() map[string]any {
	return map[string]any{
		"kty": "EC", "kid": f.ecKid, "use": "sig", "alg": "ES256", "crv": "P-256",
		"x": b64url(pad32(f.ecKey.X)), "y": b64url(pad32(f.ecKey.Y)),
	}
}

func (f *fixture) verifier(t *testing.T) *Verifier {
	t.Helper()
	return f.verifierWithTTL(t, time.Minute)
}

func (f *fixture) verifierWithTTL(t *testing.T, ttl time.Duration) *Verifier {
	t.Helper()
	v, err := NewVerifier(Config{
		Issuer:     testIssuer,
		Audience:   testAudience,
		JWKSURL:    f.server.URL,
		CacheTTL:   ttl,
		Timeout:    2 * time.Second,
		HTTPClient: f.server.Client(),
	}, slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

func (f *fixture) claims() jwtlib.MapClaims {
	now := time.Now()
	return jwtlib.MapClaims{
		"iss":   testIssuer,
		"aud":   testAudience,
		"sub":   "user-123",
		"exp":   float64(now.Add(5 * time.Minute).Unix()),
		"iat":   float64(now.Add(-time.Minute).Unix()),
		"nbf":   float64(now.Add(-time.Minute).Unix()),
		"roles": []string{"viewer", "analyst"},
	}
}

func (f *fixture) sign(t *testing.T, claims jwtlib.MapClaims) string {
	t.Helper()
	return f.signWith(t, jwtlib.SigningMethodRS256, f.rsaKey, f.rsaKid, claims)
}

func (f *fixture) signWith(t *testing.T, method jwtlib.SigningMethod, key any, kid string, claims jwtlib.MapClaims) string {
	t.Helper()
	tok := jwtlib.NewWithClaims(method, claims)
	if kid != "" {
		tok.Header["kid"] = kid
	}
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return raw
}

func requireReason(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected rejection %q, got nil", want)
	}
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *errs.Error, got %T: %v", err, err)
	}
	if e.Code != errs.CodeUnauthenticated {
		t.Fatalf("expected code %q, got %q", errs.CodeUnauthenticated, e.Code)
	}
	if got := e.Details["reason"]; got != want {
		t.Fatalf("expected reason %q, got %q (%v)", want, got, err)
	}
}

func TestVerifyValidRS256(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	raw := f.sign(t, f.claims())

	id, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "user-123" || id.Class != auth.ClassHuman {
		t.Fatalf("unexpected identity: %+v", id)
	}
	if len(id.Roles) != 2 || id.Roles[0] != "viewer" || id.Roles[1] != "analyst" {
		t.Fatalf("unexpected roles: %v", id.Roles)
	}
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("expected 1 JWKS request, got %d", got)
	}
}

func TestVerifyValidES256(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	raw := f.signWith(t, jwtlib.SigningMethodES256, f.ecKey, f.ecKid, f.claims())
	id, err := v.Verify(context.Background(), raw)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.Subject != "user-123" {
		t.Fatalf("unexpected subject %q", id.Subject)
	}
}

func TestVerifyServiceIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(jwtlib.MapClaims)
	}{
		{"common_name", func(c jwtlib.MapClaims) { c["common_name"] = "svc-account" }},
		{"type service", func(c jwtlib.MapClaims) { c["type"] = "service" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			v := f.verifier(t)
			claims := f.claims()
			claims["roles"] = []any{"operator"}
			tt.mutate(claims)
			id, err := v.Verify(context.Background(), f.sign(t, claims))
			if err != nil {
				t.Fatalf("Verify: %v", err)
			}
			if id.Class != auth.ClassService {
				t.Fatalf("expected service class, got %q", id.Class)
			}
			if len(id.Roles) != 1 || id.Roles[0] != "operator" {
				t.Fatalf("unexpected roles: %v", id.Roles)
			}
		})
	}
}

func TestVerifyClaimRejections(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(jwtlib.MapClaims)
		want   string
	}{
		{"expired", func(c jwtlib.MapClaims) { c["exp"] = float64(time.Now().Add(-2 * time.Minute).Unix()) }, reasonExpired},
		{"not yet valid", func(c jwtlib.MapClaims) { c["nbf"] = float64(time.Now().Add(10 * time.Minute).Unix()) }, reasonNotYetValid},
		{"issued in future", func(c jwtlib.MapClaims) { c["iat"] = float64(time.Now().Add(10 * time.Minute).Unix()) }, reasonIssuedInFuture},
		{"wrong issuer", func(c jwtlib.MapClaims) { c["iss"] = "https://evil.example" }, reasonBadIssuer},
		{"wrong audience", func(c jwtlib.MapClaims) { c["aud"] = "someone-else" }, reasonBadAudience},
		{"missing expiry", func(c jwtlib.MapClaims) { delete(c, "exp") }, reasonMissingExpiry},
		{"missing subject", func(c jwtlib.MapClaims) { delete(c, "sub") }, reasonMissingSubject},
		{"malformed roles", func(c jwtlib.MapClaims) { c["roles"] = []any{"viewer", 42} }, reasonBadRoles},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			v := f.verifier(t)
			claims := f.claims()
			tt.mutate(claims)
			_, err := v.Verify(context.Background(), f.sign(t, claims))
			requireReason(t, err, tt.want)
		})
	}
}

func TestVerifyAlgorithmRejections(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	publicDER, err := x509.MarshalPKIXPublicKey(&f.rsaKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	// A token signed by an attacker key with a known kid must fail the signature.
	attackerKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate attacker key: %v", err)
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "alg none",
			raw:  f.signWith(t, jwtlib.SigningMethodNone, jwtlib.UnsafeAllowNoneSignatureType, f.rsaKid, f.claims()),
			want: reasonBadAlg,
		},
		{
			name: "hs256 with public key bytes",
			raw:  f.signWith(t, jwtlib.SigningMethodHS256, publicDER, f.rsaKid, f.claims()),
			want: reasonBadAlg,
		},
		{
			name: "header alg does not match key type",
			raw:  f.signWith(t, jwtlib.SigningMethodRS256, f.rsaKey, f.ecKid, f.claims()),
			want: reasonBadAlg,
		},
		{
			name: "bad signature",
			raw:  f.signWith(t, jwtlib.SigningMethodRS256, attackerKey, f.rsaKid, f.claims()),
			want: reasonBadSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := v.Verify(context.Background(), tt.raw)
			requireReason(t, err, tt.want)
		})
	}
}

func TestVerifyKidRejections(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	if _, err := v.Verify(context.Background(), f.sign(t, f.claims())); err != nil {
		t.Fatalf("prime cache: %v", err)
	}
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("expected 1 JWKS request, got %d", got)
	}

	unknown := f.signWith(t, jwtlib.SigningMethodRS256, f.rsaKey, "does-not-exist", f.claims())
	_, err := v.Verify(context.Background(), unknown)
	requireReason(t, err, reasonUnknownKid)
	if got := f.requests.Load(); got != 2 {
		t.Fatalf("expected an unknown kid to force one refresh, got %d requests", got)
	}
	// A second unknown kid inside the 30s window must not refetch.
	_, err = v.Verify(context.Background(), unknown)
	requireReason(t, err, reasonUnknownKid)
	if got := f.requests.Load(); got != 2 {
		t.Fatalf("expected the forced refresh to be rate limited, got %d requests", got)
	}

	// A missing kid never reaches the JWKS.
	before := f.requests.Load()
	_, err = v.Verify(context.Background(), f.signWith(t, jwtlib.SigningMethodRS256, f.rsaKey, "", f.claims()))
	requireReason(t, err, reasonMissingKid)
	if got := f.requests.Load(); got != before {
		t.Fatalf("missing kid fetched the JWKS: %d -> %d", before, got)
	}
}

func TestVerifyRejectsOversizedToken(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	_, err := v.Verify(context.Background(), strings.Repeat("A", maxTokenBytes+1))
	requireReason(t, err, reasonTokenTooLarge)
	if got := f.requests.Load(); got != 0 {
		t.Fatalf("oversized token fetched the JWKS: %d", got)
	}
}

func TestVerifyRejectsMalformedTokens(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	header := b64url([]byte(`{"alg":"RS256","kid":"rsa-1"}`))
	tests := []string{
		"",
		"not-a-token",
		"a.b.c",
		header + ".e30.!!!",
	}
	for _, raw := range tests {
		_, err := v.Verify(context.Background(), raw)
		requireReason(t, err, reasonMalformedToken)
	}
}

func TestVerifyJWKSFailureIsRateLimited(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	f.status.Store(http.StatusInternalServerError)
	raw := f.sign(t, f.claims())

	_, err := v.Verify(context.Background(), raw)
	requireReason(t, err, reasonJWKSUnavailable)
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("expected 1 JWKS request, got %d", got)
	}
	_, err = v.Verify(context.Background(), raw)
	requireReason(t, err, reasonJWKSUnavailable)
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("expected the failed fetch to be rate limited, got %d requests", got)
	}
}

func TestVerifyJWKSRedirectRejected(t *testing.T) {
	var targetHits atomic.Int64
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	v, err := NewVerifier(Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: redirector.URL,
		Timeout: 2 * time.Second, HTTPClient: redirector.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	raw := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, jwtlib.MapClaims{
		"iss": testIssuer, "aud": testAudience, "sub": "u",
		"exp": float64(time.Now().Add(time.Minute).Unix()),
	})
	raw.Header["kid"] = "rsa-1"
	signed, err := raw.SignedString(fixtureKey(t))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = v.Verify(context.Background(), signed)
	requireReason(t, err, reasonJWKSUnavailable)
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect was followed: %d target hits", got)
	}
}

func fixtureKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestVerifyJWKSResponseTooLarge(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("a"), maxJWKSBytes+1))
	}))
	defer server.Close()
	v, err := NewVerifier(Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: server.URL,
		Timeout: 2 * time.Second, HTTPClient: server.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	key := fixtureKey(t)
	tok := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, jwtlib.MapClaims{
		"iss": testIssuer, "aud": testAudience, "sub": "u",
		"exp": float64(time.Now().Add(time.Minute).Unix()),
	})
	tok.Header["kid"] = "rsa-1"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, err = v.Verify(context.Background(), raw)
	requireReason(t, err, reasonJWKSUnavailable)
}

func TestVerifyContextCancellationAbortsFetch(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	finished := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer server.Close()
	v, err := NewVerifier(Config{
		Issuer: testIssuer, Audience: testAudience, JWKSURL: server.URL,
		Timeout: 5 * time.Second, HTTPClient: server.Client(),
	}, nil)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-started
		cancel()
	}()
	_, err = v.Verify(ctx, f.sign(t, f.claims()))
	requireReason(t, err, reasonJWKSUnavailable)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("JWKS server did not observe context cancellation")
	}
}

func TestVerifyRefetchesAfterCacheTTL(t *testing.T) {
	f := newFixture(t)
	v := f.verifierWithTTL(t, 50*time.Millisecond)
	raw := f.sign(t, f.claims())
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("first Verify: %v", err)
	}
	if got := f.requests.Load(); got != 1 {
		t.Fatalf("expected 1 JWKS request, got %d", got)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := v.Verify(context.Background(), raw); err != nil {
		t.Fatalf("second Verify: %v", err)
	}
	if got := f.requests.Load(); got != 2 {
		t.Fatalf("expected a refetch after cache expiry, got %d requests", got)
	}
}

func TestNewVerifierValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"missing issuer", Config{Audience: "a", JWKSURL: "https://x/jwks"}, true},
		{"missing audience", Config{Issuer: "https://i", JWKSURL: "https://x/jwks"}, true},
		{"http jwks", Config{Issuer: "https://i", Audience: "a", JWKSURL: "http://x/jwks"}, true},
		{"relative jwks", Config{Issuer: "https://i", Audience: "a", JWKSURL: "/jwks"}, true},
		{"malformed jwks", Config{Issuer: "https://i", Audience: "a", JWKSURL: "://bad"}, true},
		{"valid", Config{Issuer: "https://i", Audience: "a", JWKSURL: "https://x/jwks"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := NewVerifier(tt.cfg, nil)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got verifier %+v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewVerifier: %v", err)
			}
			if v.cacheTTL != defaultCacheTTL {
				t.Fatalf("expected default cache TTL %s, got %s", defaultCacheTTL, v.cacheTTL)
			}
			if v.timeout != defaultTimeout {
				t.Fatalf("expected default timeout %s, got %s", defaultTimeout, v.timeout)
			}
			if v.client.CheckRedirect == nil {
				t.Fatal("redirects must be disabled")
			}
		})
	}
}

func TestVerifyDoesNotLogToken(t *testing.T) {
	f := newFixture(t)
	v := f.verifier(t)
	raw := f.sign(t, f.claims())
	f.status.Store(http.StatusInternalServerError)
	if _, err := v.Verify(context.Background(), raw); err == nil {
		t.Fatal("expected failure")
	}
	logged := f.logs.String()
	if logged == "" {
		t.Fatal("expected the JWKS failure to be logged")
	}
	if strings.Contains(logged, raw) || strings.Contains(logged, raw[:32]) {
		t.Fatal("raw token leaked into logs")
	}
}
