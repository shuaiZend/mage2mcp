// SPDX-License-Identifier: Apache-2.0

// Package jwt verifies OIDC access tokens against a JWKS document.
//
// The verifier is fail-closed: it bounds token size, checks the algorithm
// allowlist before the signature, selects keys only by `kid` from the fetched
// document, and reports every rejection as errs.CodeUnauthenticated with a
// stable reason in Details["reason"]. The raw token is never logged.
package jwt

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
)

const (
	maxTokenBytes, maxJWKSBytes, maxJWKSKeys                     = 8 << 10, 1 << 20, 100
	clockSkew, defaultCacheTTL, defaultTimeout, minForcedRefresh = 60 * time.Second, 10 * time.Minute, 5 * time.Second, 30 * time.Second
)

// Stable rejection reasons reported in Details["reason"].
const (
	reasonTokenTooLarge, reasonMalformedToken, reasonBadAlg      = "token_too_large", "malformed_token", "bad_alg"
	reasonMissingKid, reasonUnknownKid, reasonExpired            = "missing_kid", "unknown_kid", "expired"
	reasonNotYetValid, reasonIssuedInFuture, reasonMissingExpiry = "not_yet_valid", "issued_in_future", "missing_expiry"
	reasonBadIssuer, reasonBadAudience, reasonBadSignature       = "bad_issuer", "bad_audience", "bad_signature"
	reasonBadRoles, reasonMissingSubject, reasonInvalidToken     = "bad_roles", "missing_subject", "invalid_token"
	reasonUnverifiable, reasonJWKSUnavailable                    = "unverifiable_token", "jwks_unavailable"
)

// allowedAlgs is the complete algorithm allowlist; "none" and HMAC are excluded.
var allowedAlgs = []string{"RS256", "ES256"}

// errRefreshLimited means a refresh was suppressed by the 30-second limiter.
var errRefreshLimited = errors.New("jwt: jwks refresh rate limited")

// Config configures a Verifier. Issuer, Audience, and JWKSURL are required.
type Config struct {
	Issuer   string
	Audience string
	JWKSURL  string
	// CacheTTL is how long a fetched JWKS is trusted. Defaults to 10 minutes.
	CacheTTL time.Duration
	// Timeout bounds one JWKS HTTP request. Defaults to 5 seconds.
	Timeout time.Duration
	// HTTPClient is an optional client, mainly for tests. It is cloned; the
	// clone always disables redirects.
	HTTPClient *http.Client
}

// Verifier verifies tokens against a cached JWKS and is safe for concurrent use.
type Verifier struct {
	issuer, audience, jwksURL string
	cacheTTL, timeout         time.Duration
	client                    *http.Client
	logger                    *slog.Logger

	mu        sync.RWMutex
	keys      map[string]any
	fetchedAt time.Time
	forcedAt  time.Time
	failedAt  time.Time

	fetchMu sync.Mutex
}

// NewVerifier validates cfg and returns a verifier that fetches the JWKS
// lazily. A JWKS URL that is not absolute HTTPS is rejected.
func NewVerifier(cfg Config, logger *slog.Logger) (*Verifier, error) {
	if strings.TrimSpace(cfg.Issuer) == "" {
		return nil, errors.New("jwt: issuer is required")
	}
	if strings.TrimSpace(cfg.Audience) == "" {
		return nil, errors.New("jwt: audience is required")
	}
	u, err := url.Parse(cfg.JWKSURL)
	if err != nil {
		return nil, fmt.Errorf("jwt: parse jwks_url: %w", err)
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("jwt: jwks_url %q must be an absolute https URL", cfg.JWKSURL)
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	ttl, timeout := cfg.CacheTTL, cfg.Timeout
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := &http.Client{}
	if cfg.HTTPClient != nil {
		cloned := *cfg.HTTPClient
		client = &cloned
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout == 0 {
		client.Timeout = timeout
	}
	return &Verifier{
		issuer: cfg.Issuer, audience: cfg.Audience, jwksURL: cfg.JWKSURL,
		cacheTTL: ttl, timeout: timeout, client: client, logger: logger,
	}, nil
}

// Verify checks the token's algorithm, signature, and claims and returns the
// caller identity. Every failure is an *errs.Error with CodeUnauthenticated.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (*auth.Identity, error) {
	if rawToken == "" {
		return nil, reject(reasonMalformedToken)
	}
	if len(rawToken) > maxTokenBytes {
		return nil, reject(reasonTokenTooLarge)
	}
	alg, kid, err := decodeHeader(rawToken)
	if err != nil {
		return nil, reject(reasonMalformedToken)
	}
	if !slices.Contains(allowedAlgs, alg) {
		return nil, reject(reasonBadAlg)
	}
	if kid == "" {
		return nil, reject(reasonMissingKid)
	}
	var c tokenClaims
	parser := jwtlib.NewParser(
		jwtlib.WithValidMethods(allowedAlgs),
		jwtlib.WithExpirationRequired(),
		jwtlib.WithIssuedAt(),
		jwtlib.WithLeeway(clockSkew),
	)
	tok, err := parser.ParseWithClaims(rawToken, &c, func(t *jwtlib.Token) (any, error) { return v.key(ctx, t) })
	if err != nil {
		return nil, classify(err)
	}
	if !tok.Valid {
		return nil, reject(reasonUnverifiable)
	}
	if c.Issuer != v.issuer {
		return nil, reject(reasonBadIssuer)
	}
	if !slices.Contains(c.Audience, v.audience) {
		return nil, reject(reasonBadAudience)
	}
	if c.Subject == "" {
		return nil, reject(reasonMissingSubject)
	}
	roles, err := parseRoles(c.Roles)
	if err != nil {
		return nil, reject(reasonBadRoles)
	}
	class := auth.ClassHuman
	if c.CommonName != "" || c.Type == "service" {
		class = auth.ClassService
	}
	return &auth.Identity{Subject: c.Subject, Class: class, Roles: roles}, nil
}

// tokenClaims is the verified claim set; unknown claims are ignored.
type tokenClaims struct {
	jwtlib.RegisteredClaims
	Roles      json.RawMessage `json:"roles"`
	CommonName string          `json:"common_name"`
	Type       string          `json:"type"`
}

// decodeHeader reads alg and kid without verifying anything.
func decodeHeader(token string) (alg, kid string, err error) {
	first, _, ok := strings.Cut(token, ".")
	if !ok || first == "" {
		return "", "", errors.New("jwt: token has no header segment")
	}
	raw, err := base64.RawURLEncoding.DecodeString(first)
	if err != nil {
		return "", "", fmt.Errorf("jwt: decode header: %w", err)
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", "", fmt.Errorf("jwt: parse header: %w", err)
	}
	return h.Alg, h.Kid, nil
}

// parseRoles accepts a []string or a JSON array of strings and nothing else.
func parseRoles(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("jwt: roles claim is not an array: %w", err)
	}
	roles := make([]string, 0, len(values))
	for _, value := range values {
		role, ok := value.(string)
		if !ok {
			return nil, errors.New("jwt: roles claim contains a non-string entry")
		}
		roles = append(roles, role)
	}
	return roles, nil
}

// classify maps a parser error to a stable, non-leaking rejection.
func classify(err error) error {
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, jwtlib.ErrTokenExpired):
		return reject(reasonExpired)
	case errors.Is(err, jwtlib.ErrTokenNotValidYet):
		return reject(reasonNotYetValid)
	case errors.Is(err, jwtlib.ErrTokenUsedBeforeIssued):
		return reject(reasonIssuedInFuture)
	case errors.Is(err, jwtlib.ErrTokenRequiredClaimMissing):
		return reject(reasonMissingExpiry)
	case errors.Is(err, jwtlib.ErrTokenMalformed):
		return reject(reasonMalformedToken)
	case errors.Is(err, jwtlib.ErrTokenSignatureInvalid):
		return reject(reasonBadSignature)
	case errors.Is(err, jwtlib.ErrTokenUnverifiable):
		return reject(reasonUnverifiable)
	default:
		return reject(reasonInvalidToken)
	}
}

// key selects the JWKS key for the token and checks it against the header alg.
func (v *Verifier) key(ctx context.Context, tok *jwtlib.Token) (any, error) {
	alg, _ := tok.Header["alg"].(string)
	if !slices.Contains(allowedAlgs, alg) {
		return nil, reject(reasonBadAlg)
	}
	kid, _ := tok.Header["kid"].(string)
	if kid == "" {
		return nil, reject(reasonMissingKid)
	}
	k, found, fresh := v.lookup(kid)
	if !found {
		refreshErr := v.refresh(ctx)
		if k, found, _ = v.lookup(kid); !found {
			switch {
			case errors.Is(refreshErr, errRefreshLimited) && fresh:
				return nil, reject(reasonUnknownKid)
			case refreshErr != nil:
				return nil, reject(reasonJWKSUnavailable).WithCause(refreshErr)
			default:
				return nil, reject(reasonUnknownKid)
			}
		}
	}
	switch alg {
	case "RS256":
		if _, ok := k.(*rsa.PublicKey); ok {
			return k, nil
		}
	case "ES256":
		if _, ok := k.(*ecdsa.PublicKey); ok {
			return k, nil
		}
	}
	return nil, reject(reasonBadAlg)
}

// lookup reports the key, whether it was found, and whether the cache is fresh.
func (v *Verifier) lookup(kid string) (any, bool, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	fresh := len(v.keys) > 0 && time.Now().Before(v.fetchedAt.Add(v.cacheTTL))
	if !fresh {
		return nil, false, false
	}
	k, ok := v.keys[kid]
	return k, ok, true
}

// refresh fetches the JWKS at most once per cache window. A forced refresh (an
// unknown kid against a fresh cache) and a retry after a failed fetch are both
// limited to one attempt per minForcedRefresh.
func (v *Verifier) refresh(ctx context.Context) error {
	v.fetchMu.Lock()
	defer v.fetchMu.Unlock()
	now := time.Now()
	v.mu.RLock()
	fresh := len(v.keys) > 0 && now.Before(v.fetchedAt.Add(v.cacheTTL))
	lastForced, failedAt := v.forcedAt, v.failedAt
	v.mu.RUnlock()
	if fresh && !lastForced.IsZero() && now.Sub(lastForced) < minForcedRefresh {
		return errRefreshLimited
	}
	if !fresh && !failedAt.IsZero() && now.Sub(failedAt) < minForcedRefresh {
		return errRefreshLimited
	}
	if fresh {
		v.mu.Lock()
		v.forcedAt = now
		v.mu.Unlock()
	}
	keys, err := v.fetch(ctx)
	if err != nil {
		v.mu.Lock()
		v.failedAt = time.Now()
		v.mu.Unlock()
		v.logger.WarnContext(ctx, "jwks refresh failed", "jwks_url", v.jwksURL, "error", err)
		return err
	}
	v.mu.Lock()
	v.keys, v.fetchedAt, v.failedAt = keys, time.Now(), time.Time{}
	v.mu.Unlock()
	return nil
}

// fetch retrieves and parses the JWKS under a bounded timeout and size.
func (v *Verifier) fetch(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, fmt.Errorf("jwt: build jwks request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwt: fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("jwt: jwks endpoint returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes+1))
	if err != nil {
		return nil, fmt.Errorf("jwt: read jwks: %w", err)
	}
	if len(body) > maxJWKSBytes {
		return nil, fmt.Errorf("jwt: jwks response exceeds %d bytes", maxJWKSBytes)
	}
	return parseJWKS(body)
}

type jsonWebKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKS keeps only usable signature keys. A bad entry is skipped so it
// cannot shadow a good one; a document with no usable key is an error.
func parseJWKS(body []byte) (map[string]any, error) {
	var doc struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("jwt: parse jwks: %w", err)
	}
	if len(doc.Keys) == 0 {
		return nil, errors.New("jwt: jwks contains no keys")
	}
	if len(doc.Keys) > maxJWKSKeys {
		return nil, fmt.Errorf("jwt: jwks contains %d keys, limit is %d", len(doc.Keys), maxJWKSKeys)
	}
	keys := make(map[string]any, len(doc.Keys))
	for _, jwk := range doc.Keys {
		if jwk.Kid == "" || jwk.Use == "enc" {
			continue
		}
		if key, err := jwk.publicKey(); err == nil {
			keys[jwk.Kid] = key
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("jwt: jwks contains no usable signature keys")
	}
	return keys, nil
}

func (k jsonWebKey) publicKey() (any, error) {
	switch k.Kty {
	case "RSA":
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(n) == 0 || len(e) == 0 || len(e) > 4 {
			return nil, errors.New("jwt: invalid RSA key")
		}
		exp := 0
		for _, b := range e {
			exp = exp<<8 | int(b)
		}
		if exp < 3 {
			return nil, errors.New("jwt: invalid RSA exponent")
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: exp}, nil
	case "EC":
		if k.Crv != "P-256" {
			return nil, fmt.Errorf("jwt: unsupported curve %q", k.Crv)
		}
		x, errX := base64.RawURLEncoding.DecodeString(k.X)
		y, errY := base64.RawURLEncoding.DecodeString(k.Y)
		if errX != nil || errY != nil || len(x) == 0 || len(x) > 32 || len(y) == 0 || len(y) > 32 {
			return nil, errors.New("jwt: invalid EC point")
		}
		raw := make([]byte, 0, 1+len(x)+len(y))
		raw = append(raw, 0x04) // uncompressed point marker
		raw = append(raw, x...)
		raw = append(raw, y...)
		pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
		if err != nil {
			return nil, errors.New("jwt: EC point is not on P-256")
		}
		return pub, nil
	}
	return nil, fmt.Errorf("jwt: unsupported key type %q", k.Kty)
}

// reject builds the only error shape this package returns.
func reject(reason string) *errs.Error {
	return errs.New(errs.CodeUnauthenticated, "authentication failed").
		WithOp("jwt.verify").
		WithDetail("reason", reason)
}

var _ auth.Verifier = (*Verifier)(nil)
