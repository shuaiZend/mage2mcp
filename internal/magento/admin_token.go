// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	// DefaultAdminTokenTTL mirrors Magento's default 4-hour admin token
	// lifetime. The token endpoint returns no expiry, so the gateway assumes
	// this lifetime and refreshes ahead of it.
	DefaultAdminTokenTTL = 4 * time.Hour
	// DefaultAdminTokenTimeout bounds one admin token request, independent of
	// the REST client's per-request deadline.
	DefaultAdminTokenTimeout = 15 * time.Second

	adminTokenPath        = "/V1/integration/admin/token"
	adminTokenRefreshSkew = 5 * time.Minute
	maxAdminCredentialLen = 1024
	maxAdminTokenLen      = 4096
	// maxAdminTokenResponseBytes bounds the token response; a token is a few
	// hundred bytes, so 64 KiB leaves room for an error envelope and no more.
	maxAdminTokenResponseBytes = 64 << 10
)

// AdminTokenSigner mints a Magento admin bearer token from an admin username
// and password, caches it, refreshes it before the assumed expiry, and mints a
// new one once when Magento answers 401.
//
// The password and the token are never logged, persisted, or echoed in errors:
// the signer renders itself redacted, token requests have a bounded deadline,
// and upstream response bodies are never copied into an error.
type AdminTokenSigner struct {
	username string
	password string

	mu        sync.Mutex
	baseURL   string
	storeCode string
	token     string
	expiresAt time.Time
	// refreshing is closed when an in-flight refresh finishes. Waiters block on it
	// instead of on the mutex, so one slow token fetch does not queue every request
	// of the instance behind it.
	refreshing chan struct{}
	ttl        time.Duration
	skew       time.Duration
	now        func() time.Time
	timeout    time.Duration
	httpClient *http.Client
}

var _ Signer = (*AdminTokenSigner)(nil)

// NewAdminTokenSigner validates an admin username and password and returns an
// unbound signer. NewResolverFromSettings binds it to the configured endpoint
// and store code before the first request.
func NewAdminTokenSigner(username, password string) (*AdminTokenSigner, error) {
	username = strings.TrimSpace(username)
	if err := validateAdminUsername(username); err != nil {
		return nil, err
	}
	if err := validateAdminPassword(password); err != nil {
		return nil, err
	}
	return &AdminTokenSigner{
		username: username,
		password: password,
		ttl:      DefaultAdminTokenTTL,
		skew:     adminTokenRefreshSkew,
		now:      time.Now,
		timeout:  DefaultAdminTokenTimeout,
	}, nil
}

func validateAdminUsername(username string) error {
	if username == "" {
		return invalidf("Magento admin credential requires a username")
	}
	if len(username) > maxAdminCredentialLen {
		return invalidf("Magento admin username is too long")
	}
	if hasControlChars(username) {
		return invalidf("Magento admin username must not contain control characters")
	}
	return nil
}

func validateAdminPassword(password string) error {
	if password == "" {
		return invalidf("Magento admin credential requires a password")
	}
	if len(password) > maxAdminCredentialLen {
		return invalidf("Magento admin password is too long")
	}
	if hasControlChars(password) {
		return invalidf("Magento admin password must not contain control characters")
	}
	return nil
}

func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// Bind attaches the signer to one Magento instance. NewResolverFromSettings
// calls it before the first request; rebinding to another instance drops any
// cached token. The endpoint rules match NewClient, which validates again when
// the REST client is built.
func (s *AdminTokenSigner) Bind(baseURL, storeCode string) error {
	if s == nil {
		return errs.New(errs.CodeInternal, "Magento admin token signer is not configured")
	}
	normalized, err := normalizeAdminBaseURL(baseURL)
	if err != nil {
		return err
	}
	if !storeCodeRE.MatchString(storeCode) {
		return invalidf("Magento store code must match %s", storeCodeRE.String())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.baseURL != normalized || s.storeCode != storeCode {
		s.token, s.expiresAt = "", time.Time{}
	}
	s.baseURL, s.storeCode = normalized, storeCode
	return nil
}

func normalizeAdminBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", invalidf("Magento base URL is not a valid absolute URL")
	}
	devHost := strings.EqualFold(u.Hostname(), "127.0.0.1") || strings.EqualFold(u.Hostname(), "localhost")
	if u.Scheme != "https" && (u.Scheme != "http" || !devHost) {
		return "", invalidf("Magento base URL must use https (http is allowed only for 127.0.0.1 or localhost)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "" {
		return "", invalidf("Magento base URL must not carry userinfo, a path, a query, or a fragment")
	}
	return strings.TrimRight(raw, "/"), nil
}

// Sign implements Signer.
func (s *AdminTokenSigner) Sign(req *http.Request) error {
	if req == nil {
		return invalidf("Magento admin token signer needs an HTTP request")
	}
	token, err := s.tokenFor(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// tokenFor returns a cached token or mints a new one.
//
// A burst of calls mints exactly one token: the first caller starts the refresh and
// the rest wait on it. They wait on a channel rather than on the mutex, because
// holding the mutex across the token request serialized every request of the
// instance behind one fetch.
func (s *AdminTokenSigner) tokenFor(ctx context.Context) (string, error) {
	if s == nil {
		return "", errs.New(errs.CodeInternal, "Magento admin token signer is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.baseURL == "" || s.storeCode == "" {
		return "", errs.New(errs.CodeInternal, "Magento admin token signer is not bound to an instance")
	}
	now := s.now
	if now == nil {
		now = time.Now
	}
	// One refresh at a time, and waiters block on the in-flight refresh rather than on
	// the mutex. Holding the mutex across the HTTP call serialized every request of the
	// instance behind one token fetch, so a slow token endpoint queued a whole worker's
	// traffic behind it.
	for {
		s.mu.Lock()
		if s.token != "" && now().Before(s.expiresAt.Add(-s.skew)) {
			cached := s.token
			s.mu.Unlock()
			return cached, nil
		}
		if wait := s.refreshing; wait != nil {
			// A refresh is in flight: wait for it, then look at the cache again. The
			// recheck is inside the loop because the refresh may have failed, in which
			// case this caller starts the next one.
			s.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return "", errs.New(errs.CodeUpstreamTimeout,
					"the admin token refresh did not finish before the deadline")
			}
			continue
		}
		done := make(chan struct{})
		s.refreshing = done
		s.mu.Unlock()

		token, err := s.fetchToken(ctx)

		s.mu.Lock()
		s.refreshing = nil
		if err == nil {
			ttl := s.ttl
			if ttl <= 0 {
				ttl = DefaultAdminTokenTTL
			}
			s.token = token
			s.expiresAt = now().Add(ttl)
		}
		s.mu.Unlock()
		close(done)
		if err != nil {
			return "", err
		}
		return token, nil
	}
}

func (s *AdminTokenSigner) fetchToken(ctx context.Context) (string, error) {
	endpoint := s.baseURL + "/rest/" + s.storeCode + adminTokenPath
	payload, err := json.Marshal(struct { // #nosec G117 -- the Magento token endpoint requires the password in the request body; it is never logged, persisted, or echoed
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: s.username, Password: s.password})
	if err != nil {
		return "", errs.New(errs.CodeInternal, "Magento admin credential could not be encoded").WithCause(err)
	}
	if s.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", errs.New(errs.CodeInternal, "Magento admin token request could not be built").WithCause(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	client := s.httpClient
	if client == nil {
		// Never chase a redirect: a POST that carries the admin password must
		// not be replayed to another host.
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", adminTokenTransportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAdminTokenResponseBytes))
	if err != nil {
		return "", errs.New(errs.CodeUpstreamUnavailable, "Magento admin token response could not be read").WithCause(err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", adminTokenStatusError(resp.StatusCode, s.storeCode)
	}
	token := parseAdminToken(raw)
	if token == "" {
		return "", errs.New(errs.CodeUpstreamUnavailable, "Magento returned no usable admin token")
	}
	return token, nil
}

func adminTokenTransportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return errs.New(errs.CodeUpstreamTimeout, "Magento did not issue an admin token before the deadline").WithCause(err)
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return errs.New(errs.CodeUpstreamTimeout, "Magento admin token request was canceled before a response arrived").WithCause(err)
	}
	e := errs.New(errs.CodeUpstreamUnavailable, "Magento is unreachable while issuing an admin token")
	e.Retryable = true
	return e.WithCause(err)
}

func adminTokenStatusError(status int, storeCode string) error {
	code, message, retryable := errs.CodeUpstreamUnavailable, "Magento returned an unexpected HTTP status while issuing an admin token", false
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		code, message = errs.CodeUnauthenticated, "Magento rejected the admin username or password"
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		code, message = errs.CodeValidationFailed, "Magento rejected the admin credential request as invalid; verify the username and password"
	case status == http.StatusNotFound:
		code, message = errs.CodeNotFound, "Magento has no admin token route; verify the store code"
	case status == http.StatusTooManyRequests:
		code, message, retryable = errs.CodeRateLimited, "Magento is rate limiting admin token requests", true
	case status >= 500:
		code, message, retryable = errs.CodeUpstreamUnavailable, "Magento is unavailable while issuing an admin token", true
	}
	e := errs.New(code, message)
	e.Retryable = retryable
	e = e.WithOp("magento POST "+adminTokenPath).WithDetail("upstream_status", strconv.Itoa(status))
	if storeCode != "" {
		e = e.WithDetail("store_code", storeCode)
	}
	return e
}

// parseAdminToken accepts Magento's bare JSON string and the object envelope
// some builds and proxies return.
func parseAdminToken(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return ""
	}
	var token string
	if err := json.Unmarshal(trimmed, &token); err == nil {
		return usableAdminToken(token)
	}
	var envelope struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(trimmed, &envelope); err == nil {
		return usableAdminToken(envelope.Token)
	}
	return ""
}

// usableAdminToken rejects empty, oversized, and control-character tokens so a
// hostile or broken upstream cannot inject header content.
func usableAdminToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > maxAdminTokenLen || hasControlChars(token) {
		return ""
	}
	return token
}

// Invalidate drops the cached token; the next Sign mints a new one. The 401
// retry transport calls it before re-signing.
func (s *AdminTokenSigner) Invalidate() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.token, s.expiresAt = "", time.Time{}
	s.mu.Unlock()
}

// String redacts the credential so accidental formatting cannot leak it.
func (s *AdminTokenSigner) String() string { return "magento.AdminTokenSigner{credential:<redacted>}" }

// GoString redacts the credential for %#v formatting.
func (s *AdminTokenSigner) GoString() string { return s.String() }

// HTTPClient returns a client whose transport re-signs a request once after a
// 401 so an expired or revoked token is replaced transparently. A 401 is
// rejected before Magento executes the request, so retrying a write once is
// safe. NewResolverFromSettings installs this client automatically.
func (s *AdminTokenSigner) HTTPClient() *http.Client {
	return &http.Client{
		Transport:     &adminTokenRetryTransport{base: NewTransport(), signer: s},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// adminTokenRetryTransport retries one request with a freshly minted token when
// the upstream answers 401.
type adminTokenRetryTransport struct {
	base   http.RoundTripper
	signer *AdminTokenSigner
}

func (t *adminTokenRetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	retry, ok := cloneForRetry(req)
	if !ok {
		return resp, nil
	}
	t.signer.Invalidate()
	if signErr := t.signer.Sign(retry); signErr != nil {
		// Keep the original 401 so the client reports an authentication
		// failure instead of masking it as a transport error.
		return resp, nil
	}
	if resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}
	return base.RoundTrip(retry)
}

// cloneForRetry copies a request and rewinds its body so it can be signed and
// sent a second time. Requests without a replayable body are not retried.
func cloneForRetry(req *http.Request) (*http.Request, bool) {
	if req == nil {
		return nil, false
	}
	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		return nil, false
	}
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, false
		}
		retry.Body = body
	}
	return retry, true
}
