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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultTokenURI           = "https://oauth2.googleapis.com/token"                // #nosec G101 -- public endpoint, not a credential
	analyticsReadOnlyScope    = "https://www.googleapis.com/auth/analytics.readonly" //nolint:gosec // #nosec G101 -- OAuth scope
	jwtBearerGrantType        = "urn:ietf:params:oauth:grant-type:jwt-bearer"        // #nosec G101 -- grant type name, not a credential
	tokenResponseLimit        = 1 << 20                                              //nolint:gosec // #nosec G101 -- size limit
	tokenRefreshWindow        = 60 * time.Second
	serviceAccountJWTLife     = time.Hour
	defaultTokenClientTimeout = 30 * time.Second
	serviceAccountTokenOp     = "ga4.serviceAccount.token" // #nosec G101 -- operation label, not a credential
)

// ServiceAccountTokenSource exchanges a Google service-account JSON key for
// OAuth access tokens scoped to analytics.readonly. Tokens are cached until 60
// seconds before expiry, and concurrent refreshes share one token request. It
// is safe for concurrent use and implements TokenSource.
type ServiceAccountTokenSource struct {
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURI    string
	httpClient  *http.Client
	// now is the clock seam tests use to advance past the cache window.
	now func() time.Time

	mu       sync.Mutex
	token    string
	expiry   time.Time
	inflight *tokenExchange
}

type tokenExchange struct {
	done  chan struct{}
	token string
	err   error
}

// serviceAccountKey is the subset of a Google service-account JSON key this package needs.
type serviceAccountKey struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
}

// NewServiceAccountTokenSource parses a Google service-account JSON key. The
// token_uri defaults to the Google OAuth endpoint when the key omits it, and
// httpClient defaults to a client with a 30-second timeout.
func NewServiceAccountTokenSource(credentialsJSON []byte, httpClient *http.Client) (*ServiceAccountTokenSource, error) {
	var key serviceAccountKey
	if err := json.Unmarshal(credentialsJSON, &key); err != nil {
		return nil, fmt.Errorf("ga4: service-account credentials are not valid JSON: %w", err)
	}
	if strings.TrimSpace(key.ClientEmail) == "" {
		return nil, errors.New("ga4: service-account credentials are missing client_email")
	}
	if strings.TrimSpace(key.PrivateKey) == "" {
		return nil, errors.New("ga4: service-account credentials are missing private_key")
	}
	tokenURI := strings.TrimSpace(key.TokenURI)
	if tokenURI == "" {
		tokenURI = defaultTokenURI
	}
	parsed, err := url.Parse(tokenURI)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, errors.New("ga4: service-account token_uri must be an absolute http(s) URL")
	}
	privateKey, err := parseRSAPrivateKey(key.PrivateKey)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTokenClientTimeout}
	}
	return &ServiceAccountTokenSource{
		clientEmail: key.ClientEmail,
		privateKey:  privateKey,
		tokenURI:    tokenURI,
		httpClient:  httpClient,
		now:         time.Now,
	}, nil
}

// parseRSAPrivateKey decodes a PEM PKCS#8 RSA key, accepting PKCS#1 as a
// fallback. Errors never echo the key material.
func parseRSAPrivateKey(pemData string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("ga4: service-account private_key is not PEM encoded")
	}
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("ga4: service-account private_key is not an RSA key")
		}
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("ga4: service-account private_key is not a valid PKCS#8 or PKCS#1 RSA key")
}

// Token returns a cached access token, refreshing it when it is within 60
// seconds of expiry. Concurrent refreshes issue exactly one token request.
func (s *ServiceAccountTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.validLocked() {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	if inflight := s.inflight; inflight != nil {
		s.mu.Unlock()
		select {
		case <-inflight.done:
			return inflight.token, inflight.err
		case <-ctx.Done():
			return "", tokenEndpointError(ctx.Err())
		}
	}
	inflight := &tokenExchange{done: make(chan struct{})}
	s.inflight = inflight
	s.mu.Unlock()

	token, expiresIn, err := s.exchange(ctx)

	s.mu.Lock()
	if err == nil {
		s.token = token
		s.expiry = s.now().Add(expiresIn)
	}
	inflight.token, inflight.err = token, err
	s.inflight = nil
	close(inflight.done)
	s.mu.Unlock()
	return token, err
}

func (s *ServiceAccountTokenSource) validLocked() bool {
	return s.token != "" && s.now().Before(s.expiry.Add(-tokenRefreshWindow))
}

func (s *ServiceAccountTokenSource) exchange(ctx context.Context) (string, time.Duration, error) {
	assertion, err := s.assertion()
	if err != nil {
		return "", 0, tokenEndpointError(err)
	}
	form := url.Values{"grant_type": {jwtBearerGrantType}, "assertion": {assertion}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, tokenEndpointError(fmt.Errorf("build token request: %w", err))
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return "", 0, tokenEndpointError(fmt.Errorf("token request: %w", err))
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, tokenResponseLimit+1))
	if err != nil {
		return "", 0, tokenEndpointError(fmt.Errorf("read token response: %w", err))
	}
	if len(body) > tokenResponseLimit {
		return "", 0, tokenEndpointError(errors.New("token response exceeded the 1 MiB limit"))
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", 0, tokenEndpointError(fmt.Errorf("token endpoint returned status %d", response.StatusCode))
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", 0, tokenEndpointError(fmt.Errorf("decode token response: %w", err))
	}
	if payload.AccessToken == "" {
		return "", 0, tokenEndpointError(errors.New("token response has no access_token"))
	}
	expiresIn := time.Duration(payload.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = serviceAccountJWTLife
	}
	return payload.AccessToken, expiresIn, nil
}

func (s *ServiceAccountTokenSource) assertion() (string, error) {
	now := s.now().UTC()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss":   s.clientEmail,
		"scope": analyticsReadOnlyScope,
		"aud":   s.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(serviceAccountJWTLife).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, s.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// tokenEndpointError maps any token exchange failure to the unauthenticated
// taxonomy without exposing the assertion, the private key, or the response
// body.
func tokenEndpointError(cause error) *errs.Error {
	return errs.New(errs.CodeUnauthenticated, "GA4 service-account token request failed").
		WithOp(serviceAccountTokenOp).
		WithDetail("reason", "token_endpoint").
		WithCause(cause)
}

var _ TokenSource = (*ServiceAccountTokenSource)(nil)
