// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const testToken = "header.payload.signature"

type stubVerifier struct {
	identity *Identity
	err      error
	gotToken string
	calls    int
}

func (s *stubVerifier) Verify(_ context.Context, rawToken string) (*Identity, error) {
	s.calls++
	s.gotToken = rawToken
	return s.identity, s.err
}

func newLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func doRequest(t *testing.T, h http.Handler, headers map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestIdentityContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	if _, ok := IdentityFromContext(ctx); ok {
		t.Fatal("empty context must not carry an identity")
	}
	roles := []string{"viewer"}
	ctx = WithIdentity(ctx, &Identity{Subject: "user-1", Class: ClassHuman, Roles: roles})
	roles[0] = "mutated"

	id, ok := IdentityFromContext(ctx)
	if !ok {
		t.Fatal("identity not found")
	}
	if id.Subject != "user-1" || id.Class != ClassHuman {
		t.Fatalf("unexpected identity: %+v", id)
	}
	if len(id.Roles) != 1 || id.Roles[0] != "viewer" {
		t.Fatalf("identity aliases the caller's roles slice: %v", id.Roles)
	}
}

func TestWithIdentityIgnoresNil(t *testing.T) {
	ctx := context.Background()
	if got := WithIdentity(ctx, nil); got != ctx {
		t.Fatal("nil identity must not change the context")
	}
	if _, ok := IdentityFromContext(context.TODO()); ok {
		t.Fatal("nil context must not carry an identity")
	}
}

func TestMiddlewareRejectsMissingAndMalformedHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string][]string
		reason  string
	}{
		{"missing", nil, reasonMissingToken},
		{"wrong scheme", map[string][]string{"Authorization": {"Basic abc"}}, reasonMalformedAuthorization},
		{"empty bearer", map[string][]string{"Authorization": {"Bearer"}}, reasonMalformedAuthorization},
		{"extra fields", map[string][]string{"Authorization": {"Bearer a b"}}, reasonMalformedAuthorization},
		{"duplicate", map[string][]string{"Authorization": {"Bearer a", "Bearer b"}}, reasonDuplicateAuthorization},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier := &stubVerifier{identity: &Identity{Subject: "user-1"}}
			called := false
			h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			rec := doRequest(t, h, tt.headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			body := decodeBody(t, rec)
			if body["error"] != "unauthenticated" || body["reason"] != tt.reason {
				t.Fatalf("unexpected body: %v", body)
			}
			if called {
				t.Fatal("handler ran for a rejected request")
			}
			if verifier.calls != 0 {
				t.Fatalf("verifier ran for a malformed request: %d calls", verifier.calls)
			}
		})
	}
}

func TestMiddlewareAcceptsValidToken(t *testing.T) {
	want := &Identity{Subject: "user-1", Class: ClassHuman, Roles: []string{"viewer"}}
	verifier := &stubVerifier{identity: want}
	var got *Identity
	h := Middleware(verifier, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFromContext(r.Context())
	}))
	rec := doRequest(t, h, map[string][]string{"Authorization": {"Bearer " + testToken}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if verifier.gotToken != testToken {
		t.Fatalf("verifier received %q", verifier.gotToken)
	}
	if got == nil || got.Subject != want.Subject || got.Class != want.Class {
		t.Fatalf("identity missing from request context: %+v", got)
	}
}

func TestMiddlewareMapsVerifierFailureReason(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason string
	}{
		{
			name:   "explicit reason",
			err:    errs.New(errs.CodeUnauthenticated, "authentication failed").WithDetail("reason", "expired"),
			reason: "expired",
		},
		{
			name:   "unexpected error",
			err:    errors.New("boom"),
			reason: reasonInvalidToken,
		},
		{
			name:   "errs without reason",
			err:    errs.New(errs.CodeUnauthenticated, "authentication failed"),
			reason: reasonInvalidToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Middleware(&stubVerifier{err: tt.err}, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler ran for a rejected request")
			}))
			rec := doRequest(t, h, map[string][]string{"Authorization": {"Bearer " + testToken}})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			body := decodeBody(t, rec)
			if body["error"] != "unauthenticated" || body["reason"] != tt.reason {
				t.Fatalf("unexpected body: %v", body)
			}
		})
	}
}

func TestMiddlewareRejectsInvalidIdentity(t *testing.T) {
	for name, verifier := range map[string]*stubVerifier{
		"nil identity":  {},
		"empty subject": {identity: &Identity{Class: ClassHuman}},
	} {
		t.Run(name, func(t *testing.T) {
			h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("handler ran for a rejected request")
			}))
			rec := doRequest(t, h, map[string][]string{"Authorization": {"Bearer " + testToken}})
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			if got := decodeBody(t, rec)["reason"]; got != reasonInvalidIdentity {
				t.Fatalf("expected reason %q, got %q", reasonInvalidIdentity, got)
			}
		})
	}
}

func TestMiddlewareWithoutVerifierFailsClosed(t *testing.T) {
	h := Middleware(nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran without a verifier")
	}))
	rec := doRequest(t, h, map[string][]string{"Authorization": {"Bearer " + testToken}})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if got := decodeBody(t, rec)["reason"]; got != reasonNoVerifier {
		t.Fatalf("expected reason %q, got %q", reasonNoVerifier, got)
	}
}

func TestMiddlewareNeverLeaksToken(t *testing.T) {
	var logs bytes.Buffer
	verifier := &stubVerifier{err: errs.New(errs.CodeUnauthenticated, "authentication failed").WithDetail("reason", "bad_signature")}
	h := Middleware(verifier, newLogger(&logs))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran for a rejected request")
	}))
	rec := doRequest(t, h, map[string][]string{"Authorization": {"Bearer " + testToken}})
	if body := rec.Body.String(); bytes.Contains([]byte(body), []byte(testToken)) {
		t.Fatalf("response leaked the token: %s", body)
	}
	if bytes.Contains(logs.Bytes(), []byte(testToken)) {
		t.Fatalf("logs leaked the token: %s", logs.String())
	}
	if logs.Len() == 0 {
		t.Fatal("expected the rejection to be logged at debug level")
	}
}

// perTokenVerifier accepts a fixed set of tokens and rejects everything else, so
// a test can prove which credential the middleware actually handed to the
// verifier.
type perTokenVerifier struct {
	accept map[string]*Identity
	got    []string
}

func (v *perTokenVerifier) Verify(_ context.Context, raw string) (*Identity, error) {
	v.got = append(v.got, raw)
	if id, ok := v.accept[raw]; ok {
		return id, nil
	}
	return nil, errs.New(errs.CodeUnauthenticated, "authentication failed").
		WithDetail("reason", "bad_signature")
}

func (v *perTokenVerifier) onlyToken(t *testing.T, want string) {
	t.Helper()
	if len(v.got) != 1 || v.got[0] != want {
		t.Fatalf("verifier saw %q, want exactly [%q]", v.got, want)
	}
}

// TestMiddlewareAcceptsAssertionHeader proves the documented Cloudflare Access
// ingress works: the assertion is extracted and handed to the verifier.
func TestMiddlewareAcceptsAssertionHeader(t *testing.T) {
	verifier := &perTokenVerifier{accept: map[string]*Identity{
		testToken: {Subject: "user-1", Class: ClassHuman, Roles: []string{"viewer"}},
	}}
	var got *Identity
	h := Middleware(verifier, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = IdentityFromContext(r.Context())
	}))
	rec := doRequest(t, h, map[string][]string{AssertionHeader: {testToken}})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	verifier.onlyToken(t, testToken)
	if got == nil || got.Subject != "user-1" || len(got.Roles) != 1 || got.Roles[0] != "viewer" {
		t.Fatalf("identity missing from request context: %+v", got)
	}
}

// TestMiddlewareRejectsBadAssertionHeaders mirrors the Authorization handling
// for duplicates and malformed values.
func TestMiddlewareRejectsBadAssertionHeaders(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string][]string
		reason  string
	}{
		{"duplicate assertion", map[string][]string{AssertionHeader: {"a", "b"}}, reasonDuplicateAssertion},
		{"empty assertion", map[string][]string{AssertionHeader: {""}}, reasonMalformedAssertion},
		{"blank assertion", map[string][]string{AssertionHeader: {"   "}}, reasonMalformedAssertion},
		{"whitespace inside assertion", map[string][]string{AssertionHeader: {"a b"}}, reasonMalformedAssertion},
		{"neither header", map[string][]string{"X-Forwarded-For": {"203.0.113.7"}}, reasonMissingToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verifier := &perTokenVerifier{accept: map[string]*Identity{}}
			called := false
			h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				called = true
			}))
			rec := doRequest(t, h, tt.headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d", rec.Code)
			}
			if body := decodeBody(t, rec); body["reason"] != tt.reason {
				t.Fatalf("reason = %q, want %q", body["reason"], tt.reason)
			}
			if called {
				t.Fatal("handler ran for a rejected request")
			}
			if len(verifier.got) != 0 {
				t.Fatalf("verifier ran for a malformed request: %v", verifier.got)
			}
		})
	}
}

// TestMiddlewareAssertionTakesPrecedence pins the documented rule: when the
// Access assertion is present it is the only credential consulted, so a second
// credential can never widen access and a bad assertion is never downgraded to a
// bearer token.
func TestMiddlewareAssertionTakesPrecedence(t *testing.T) {
	const assertionToken = "assertion.header.sig"
	const bearerToken = "bearer.header.sig"

	t.Run("assertion wins over a valid bearer", func(t *testing.T) {
		verifier := &perTokenVerifier{accept: map[string]*Identity{
			assertionToken: {Subject: "access-user", Class: ClassHuman, Roles: []string{"viewer"}},
			bearerToken:    {Subject: "bearer-user", Class: ClassHuman, Roles: []string{"admin"}},
		}}
		var got *Identity
		h := Middleware(verifier, nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = IdentityFromContext(r.Context())
		}))
		rec := doRequest(t, h, map[string][]string{
			AssertionHeader: {assertionToken},
			"Authorization": {"Bearer " + bearerToken},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", rec.Code, rec.Body.String())
		}
		verifier.onlyToken(t, assertionToken)
		if got == nil || got.Subject != "access-user" {
			t.Fatalf("identity = %+v, want the assertion's subject", got)
		}
	})

	t.Run("a bad assertion is not downgraded to a valid bearer", func(t *testing.T) {
		verifier := &perTokenVerifier{accept: map[string]*Identity{
			bearerToken: {Subject: "bearer-user", Class: ClassHuman, Roles: []string{"admin"}},
		}}
		h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler ran on a rejected request")
		}))
		rec := doRequest(t, h, map[string][]string{
			AssertionHeader: {"forged.header.sig"},
			"Authorization": {"Bearer " + bearerToken},
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if body := decodeBody(t, rec); body["reason"] != "bad_signature" {
			t.Fatalf("reason = %q, want the verifier's reason", body["reason"])
		}
		verifier.onlyToken(t, "forged.header.sig")
	})

	t.Run("authorization is not parsed when the assertion is present", func(t *testing.T) {
		verifier := &perTokenVerifier{accept: map[string]*Identity{
			assertionToken: {Subject: "access-user", Class: ClassHuman},
		}}
		h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		rec := doRequest(t, h, map[string][]string{
			AssertionHeader: {assertionToken},
			"Authorization": {"Basic not-a-bearer-token"},
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("a malformed Authorization must be ignored when the assertion is present, got %d (%s)",
				rec.Code, rec.Body.String())
		}
		verifier.onlyToken(t, assertionToken)
	})

	t.Run("duplicate authorization is still rejected", func(t *testing.T) {
		verifier := &perTokenVerifier{accept: map[string]*Identity{
			assertionToken: {Subject: "access-user", Class: ClassHuman},
		}}
		h := Middleware(verifier, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler ran for an ambiguous request")
		}))
		rec := doRequest(t, h, map[string][]string{
			AssertionHeader: {assertionToken},
			"Authorization": {"Bearer a", "Bearer b"},
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		if body := decodeBody(t, rec); body["reason"] != reasonDuplicateAuthorization {
			t.Fatalf("reason = %q, want %q", body["reason"], reasonDuplicateAuthorization)
		}
		if len(verifier.got) != 0 {
			t.Fatalf("verifier ran for an ambiguous request: %v", verifier.got)
		}
	})
}

// TestMiddlewareNeverLeaksAssertion is the assertion-header counterpart of the
// bearer-token leak test.
func TestMiddlewareNeverLeaksAssertion(t *testing.T) {
	var logs bytes.Buffer
	verifier := &stubVerifier{err: errs.New(errs.CodeUnauthenticated, "authentication failed").WithDetail("reason", "expired")}
	h := Middleware(verifier, newLogger(&logs))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler ran for a rejected request")
	}))
	rec := doRequest(t, h, map[string][]string{AssertionHeader: {testToken}})
	if body := rec.Body.String(); bytes.Contains([]byte(body), []byte(testToken)) {
		t.Fatalf("response leaked the assertion: %s", body)
	}
	if bytes.Contains(logs.Bytes(), []byte(testToken)) {
		t.Fatalf("logs leaked the assertion: %s", logs.String())
	}
}
