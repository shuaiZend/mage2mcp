// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// rfc5849BaseString is the signature base string from RFC 5849 section 3.4.1.1.
const rfc5849BaseString = "POST&http%3A%2F%2Fexample.com%2Frequest&a2%3Dr%2520b%26a3%3D2%2520q%26a3%3Da%26b5%3D%253D%25253D%26c%2540%3D%26c2%3D%26oauth_consumer_key%3D9djdj82h48djs9d2%26oauth_nonce%3D7d8f3e4a%26oauth_signature_method%3DHMAC-SHA1%26oauth_timestamp%3D137131201%26oauth_token%3Dkkk9d7dh3k39sjv7%26oauth_version%3D1.0"

// rfc5849Signature is HMAC-SHA1 over rfc5849BaseString with the RFC example
// secrets, base64-encoded. It is a cross-check against an independent
// implementation, not a value produced by this package.
const rfc5849Signature = "sxjJA7Zf0VWsDtw601IrVwGUjR8="

func fixedNonce(nonce string, timestamp int64) func() (string, int64) {
	return func() (string, int64) { return nonce, timestamp }
}

// parseOAuthHeader decodes an OAuth Authorization header into its fields.
func parseOAuthHeader(t *testing.T, header string) map[string]string {
	t.Helper()
	const prefix = "OAuth "
	if !strings.HasPrefix(header, prefix) {
		t.Fatalf("Authorization header is not OAuth: %q", header)
	}
	fields := make(map[string]string)
	for _, part := range strings.Split(strings.TrimPrefix(header, prefix), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			t.Fatalf("malformed OAuth header field %q", part)
		}
		decoded, err := url.PathUnescape(strings.Trim(value, `"`))
		if err != nil {
			t.Fatalf("OAuth header field %q is not percent-encoded: %v", part, err)
		}
		fields[key] = decoded
	}
	return fields
}

func TestOAuth1RFC5849Vector(t *testing.T) {
	signer, err := NewOAuth1Signer("9djdj82h48djs9d2", "kd94hf93k423kf44", "kkk9d7dh3k39sjv7", "pfkkdhi9sl3r4s00")
	if err != nil {
		t.Fatalf("NewOAuth1Signer: %v", err)
	}
	signer.SetNonceSource(fixedNonce("7d8f3e4a", 137131201))

	const body = "c2&a3=2+q"
	req, err := http.NewRequest(http.MethodPost, "http://example.com/request?b5=%3D%253D&a3=a&c%40=&a2=r%20b", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	base, params, err := signer.prepare(req)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if base != rfc5849BaseString {
		t.Fatalf("signature base string mismatch:\n got %s\nwant %s", base, rfc5849BaseString)
	}
	if params["oauth_nonce"] != "7d8f3e4a" || params["oauth_timestamp"] != "137131201" {
		t.Fatalf("unexpected nonce/timestamp: %v", params)
	}
	if _, ok := params["oauth_signature"]; ok {
		t.Fatal("prepare must not include the signature")
	}

	if err := signer.Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	fields := parseOAuthHeader(t, req.Header.Get("Authorization"))
	want := map[string]string{
		"oauth_consumer_key":     "9djdj82h48djs9d2",
		"oauth_nonce":            "7d8f3e4a",
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        "137131201",
		"oauth_token":            "kkk9d7dh3k39sjv7",
		"oauth_version":          "1.0",
		"oauth_signature":        rfc5849Signature,
	}
	for key, value := range want {
		if fields[key] != value {
			t.Errorf("header field %s = %q, want %q", key, fields[key], value)
		}
	}
	if !strings.Contains(req.Header.Get("Authorization"), "sxjJA7Zf0VWsDtw601IrVwGUjR8%3D") {
		t.Errorf("signature must be percent-encoded in the header: %q", req.Header.Get("Authorization"))
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != body {
		t.Fatalf("request body was not restored: %q", restored)
	}
}

func TestOAuth1TwoLeggedOmitsToken(t *testing.T) {
	signer, err := NewOAuth1Signer("key", "secret", "", "")
	if err != nil {
		t.Fatal(err)
	}
	signer.SetNonceSource(fixedNonce("n0nce", 1700000000))
	req, err := http.NewRequest(http.MethodGet, "https://shop.example.com/rest/default/V1/products/SKU-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	fields := parseOAuthHeader(t, req.Header.Get("Authorization"))
	if _, ok := fields["oauth_token"]; ok {
		t.Fatal("two-legged requests must not send oauth_token")
	}
	if fields["oauth_signature"] == "" {
		t.Fatal("signature is missing")
	}
	if fields["oauth_consumer_key"] != "key" {
		t.Fatalf("oauth_consumer_key = %q", fields["oauth_consumer_key"])
	}
}

func TestNewOAuth1SignerValidation(t *testing.T) {
	tests := []struct {
		name                                       string
		consumerKey, consumerSecret, token, secret string
		wantErr                                    bool
	}{
		{"four credentials", "ck", "cs", "tok", "ts", false},
		{"two-legged", "ck", "cs", "", "", false},
		{"missing consumer key", "", "cs", "", "", true},
		{"missing consumer secret", "ck", "", "", "", true},
		{"token without secret", "ck", "cs", "tok", "", true},
		{"secret without token", "ck", "cs", "", "ts", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewOAuth1Signer(tc.consumerKey, tc.consumerSecret, tc.token, tc.secret)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPercentEncode(t *testing.T) {
	tests := map[string]string{
		"abc":   "abc",
		"a b":   "a%20b",
		"a+b":   "a%2Bb",
		"=%3D":  "%3D%253D",
		"c@":    "c%40",
		"~-._":  "~-._",
		"/":     "%2F",
		"héllo": "h%C3%A9llo",
	}
	for input, want := range tests {
		if got := percentEncode(input); got != want {
			t.Errorf("percentEncode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBearerSigner(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://shop.example.com/rest/default/V1/products", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := (BearerSigner{Token: "integration-token"}).Sign(req); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer integration-token" {
		t.Fatalf("Authorization = %q", got)
	}

	err = (BearerSigner{}).Sign(req)
	if err == nil {
		t.Fatal("empty bearer token must fail")
	}
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeUnauthenticated {
		t.Fatalf("code = %v, want unauthenticated", err)
	}
}
