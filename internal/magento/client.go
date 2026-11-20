// SPDX-License-Identifier: Apache-2.0

// Package magento implements the Magento 2 REST data source: a bounded HTTP
// client, an OAuth 1.0a signer, a product read service, and the MCP tools that
// expose it. It performs no logging and never exposes credentials.
package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	// DefaultTimeout is the per-request deadline used when ClientConfig.Timeout is zero.
	DefaultTimeout   = 30 * time.Second
	maxResponseBytes = 8 << 20
	maxDrainBytes    = 4 << 10
	// maxResponseOverageBytes bounds how much of an oversized body is read past
	// maxResponseBytes so the error can report the overshoot instead of only
	// "too large". It is a measurement budget, not an accepted size.
	maxResponseOverageBytes = 1 << 20

	// Tuned connection-pool defaults. They replace http.DefaultTransport, whose
	// MaxIdleConnsPerHost of 2 forced a new TCP+TLS handshake for most requests
	// once more than two were in flight against one Magento instance.
	defaultMaxIdleConns        = 100
	defaultMaxIdleConnsPerHost = 16
	defaultMaxConnsPerHost     = 32
	defaultIdleConnTimeout     = 90 * time.Second
	defaultTLSHandshakeTimeout = 10 * time.Second
)

// NewTransport returns the connection pool every Magento client uses.
//
// The pool is shared per client (one per configured instance), keeps up to
// defaultMaxIdleConnsPerHost warm connections per Magento host, and caps total
// connections per host at defaultMaxConnsPerHost so one instance cannot exhaust
// file descriptors. Proxy settings come from the environment, as with
// http.DefaultTransport.
func NewTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          defaultMaxIdleConns,
		MaxIdleConnsPerHost:   defaultMaxIdleConnsPerHost,
		MaxConnsPerHost:       defaultMaxConnsPerHost,
		IdleConnTimeout:       defaultIdleConnTimeout,
		TLSHandshakeTimeout:   defaultTLSHandshakeTimeout,
		ExpectContinueTimeout: time.Second,
	}
}

// ResponseTooLargeError reports that an upstream response body exceeded
// maxResponseBytes. It is the typed cause of the returned *errs.Error, so
// callers can read the exact numbers while an agent sees the same values in the
// error details. Before this existed the body was cut silently and the decode
// failure surfaced as "Magento returned an unreadable product payload".
type ResponseTooLargeError struct {
	// Limit is the largest body the client accepts, in bytes.
	Limit int64
	// Observed is the body size in bytes. It is exact when Exact is true and a
	// lower bound otherwise.
	Observed int64
	// Exact reports whether Observed is the whole body size or a lower bound
	// (the measurement budget was exhausted before the body ended).
	Exact bool
}

func (e *ResponseTooLargeError) Error() string {
	if e == nil {
		return "Magento response exceeded the size limit"
	}
	if e.Exact {
		return fmt.Sprintf("Magento response body is %d bytes, over the %d-byte limit by %d bytes",
			e.Observed, e.Limit, e.Observed-e.Limit)
	}
	return fmt.Sprintf("Magento response body is at least %d bytes, over the %d-byte limit",
		e.Observed, e.Limit)
}

// Signer authenticates one outbound Magento REST request in place.
type Signer interface {
	Sign(req *http.Request) error
}

// ClientConfig configures a Client. BaseURL, StoreCode, and Signer are required.
type ClientConfig struct {
	BaseURL    string
	StoreCode  string
	HTTPClient *http.Client
	Signer     Signer
	Timeout    time.Duration
	// Limits bounds this instance's outbound request rate and concurrency.
	// The zero value means "no budget"; NewResolverFromSettings applies
	// DefaultLimits for an instance that does not configure one.
	Limits Limits
}

// Client is a bounded Magento 2 REST client for {BaseURL}/rest/{StoreCode}/V1.
type Client struct {
	BaseURL    string
	StoreCode  string
	HTTPClient *http.Client
	Signer     Signer
	Timeout    time.Duration

	// limiter is this instance's outbound budget; nil means unlimited.
	limiter *limiter
}

var storeCodeRE = regexp.MustCompile(`^[a-z0-9_]+$`)

// NewClient validates cfg and returns a client.
func NewClient(cfg ClientConfig) (*Client, error) {
	raw := strings.TrimSpace(cfg.BaseURL)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, invalidf("Magento base URL is not a valid absolute URL")
	}
	devHost := strings.EqualFold(u.Hostname(), "127.0.0.1") || strings.EqualFold(u.Hostname(), "localhost")
	if u.Scheme != "https" && (u.Scheme != "http" || !devHost) {
		return nil, invalidf("Magento base URL must use https (http is allowed only for 127.0.0.1 or localhost)")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "" {
		return nil, invalidf("Magento base URL must not carry userinfo, a path, a query, or a fragment")
	}
	if !storeCodeRE.MatchString(cfg.StoreCode) {
		return nil, invalidf("Magento store code must match %s", storeCodeRE.String())
	}
	if cfg.Timeout < 0 {
		return nil, invalidf("Magento timeout must not be negative")
	}
	if cfg.Signer == nil {
		return nil, invalidf("Magento client requires a signer")
	}
	if err := cfg.Limits.Validate(); err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Transport:     NewTransport(),
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // never chase redirects
		}
	} else if httpClient.Transport == nil {
		// A caller-supplied client without a Transport would silently inherit
		// http.DefaultTransport; install the tuned pool instead.
		httpClient.Transport = NewTransport()
	}
	return &Client{
		BaseURL:    strings.TrimRight(raw, "/"),
		StoreCode:  cfg.StoreCode,
		HTTPClient: httpClient,
		Signer:     cfg.Signer,
		Timeout:    timeout,
		limiter:    newLimiter(cfg.Limits),
	}, nil
}

// do executes one signed REST call; errors never carry the upstream payload.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) ([]byte, error) {
	if c == nil || c.BaseURL == "" || c.StoreCode == "" || c.Signer == nil {
		return nil, errs.New(errs.CodeInternal, "Magento client is not configured")
	}
	if ctx == nil {
		return nil, invalidf("Magento request requires a context")
	}
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	// Wait for this instance's outbound budget. The wait is charged against the
	// same deadline as the request, so an exhausted budget surfaces as a typed
	// rate_limited error rather than stalling the tool call.
	release, err := c.limiter.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	target := strings.TrimRight(c.BaseURL, "/") + "/rest/" + c.StoreCode + "/V1" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, errs.New(errs.CodeInternal, "Magento request body could not be encoded").WithCause(err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, errs.New(errs.CodeInternal, "Magento request could not be built").WithCause(err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if err := c.Signer.Sign(req); err != nil {
		var signErr *errs.Error
		if errors.As(err, &signErr) {
			return nil, err
		}
		return nil, errs.New(errs.CodeInternal, "Magento request could not be signed").WithCause(err)
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, transportError(ctx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxDrainBytes))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		return nil, statusError(method, path, resp.StatusCode, body, c.StoreCode)
	}
	// Read at most one byte past the cap so an oversized body is detected
	// explicitly instead of being cut into an undecodable document.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		// The response headers arrived, so the request was processed; a body we
		// cannot read leaves the outcome unknown.
		return nil, markAmbiguous(errs.New(errs.CodeUpstreamUnavailable,
			"Magento response could not be read").WithOp(method + " " + path).WithCause(err))
	}
	if int64(len(raw)) > maxResponseBytes {
		return nil, responseTooLargeError(method, path, resp, int64(len(raw)))
	}
	return raw, nil
}

// responseTooLargeError builds the explicit over-cap error. It reports the
// exact body size when Content-Length is known or when the bounded overage
// probe reaches the end of the body, and a lower bound otherwise.
func responseTooLargeError(method, path string, resp *http.Response, read int64) error {
	observed, exact := read, false
	if resp != nil && resp.ContentLength > maxResponseBytes {
		observed, exact = resp.ContentLength, true
	} else if resp != nil && resp.Body != nil {
		if extra, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseOverageBytes)); err == nil {
			observed += extra
			exact = extra < maxResponseOverageBytes
		}
	}
	over := observed - maxResponseBytes
	if over < 0 {
		over = 0
	}
	message := fmt.Sprintf("Magento response exceeded the %d-byte limit", int64(maxResponseBytes))
	if exact {
		message = fmt.Sprintf("Magento response exceeded the %d-byte limit by %d bytes", int64(maxResponseBytes), over)
	} else {
		message = fmt.Sprintf("Magento response exceeded the %d-byte limit by at least %d bytes", int64(maxResponseBytes), over)
	}
	// The taxonomy is closed, so an oversized upstream body is reported as
	// upstream_unavailable with explicit details; the typed cause carries the
	// same numbers for Go callers.
	e := errs.New(errs.CodeUpstreamUnavailable, message+"; narrow the query (fewer fields or a smaller page)")
	e = e.WithOp("magento " + method + " " + path)
	e = e.WithDetail("limit_bytes", strconv.FormatInt(int64(maxResponseBytes), 10))
	e = e.WithDetail("observed_bytes", strconv.FormatInt(observed, 10))
	e = e.WithDetail("overage_bytes", strconv.FormatInt(over, 10))
	if !exact {
		e = e.WithDetail("observed_bytes_exact", "false")
	}
	return e.WithCause(&ResponseTooLargeError{Limit: int64(maxResponseBytes), Observed: observed, Exact: exact})
}
func transportError(ctx context.Context, err error) error {
	op := "magento request"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		// The deadline can only fire after the request was written, so the write
		// may have been applied: the outcome is unknown, never "failed".
		return markAmbiguous(errs.New(errs.CodeUpstreamTimeout,
			"Magento did not answer before the deadline; the call may have completed").WithOp(op).WithCause(err))
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return markAmbiguous(errs.New(errs.CodeUpstreamTimeout,
			"Magento request was canceled before a response arrived").WithOp(op).WithCause(err))
	}
	e := errs.New(errs.CodeUpstreamUnavailable, "Magento is unreachable")
	e.Retryable = true
	if requestMayHaveBeenSent(err) {
		// The connection existed, so the request may have been delivered.
		e = markAmbiguous(e)
	}
	return e.WithOp(op).WithCause(err)
}
func statusError(method, path string, status int, body []byte, storeCode string) error {
	code, message, retryable := errs.CodeUpstreamUnavailable, "Magento returned an unexpected HTTP status", false
	ambiguous := false
	switch {
	case status == http.StatusUnauthorized:
		code, message = errs.CodeUnauthenticated, "Magento rejected the credential; re-authentication is required"
	case status == http.StatusForbidden:
		code, message = errs.CodePermissionDenied, "Magento denied the request for this credential"
	case status == http.StatusNotFound:
		code, message = errs.CodeNotFound, "Magento has no such resource or route"
	case status == http.StatusConflict:
		code, message = errs.CodeConflict, "Magento reported a conflict"
	case status == http.StatusRequestTimeout:
		code, message = errs.CodeUpstreamTimeout, "Magento timed out while processing the request"
	case status == http.StatusTooManyRequests:
		code, message, retryable = errs.CodeRateLimited, "Magento is rate limiting requests", true
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		code, message = errs.CodeValidationFailed, "Magento rejected the request as invalid; verify store_code and field values"
	case status >= 500:
		// Magento answered, but a 5xx may be returned after the mutation was
		// applied. The outcome is unknown, so the caller must reconcile.
		code, message, retryable = errs.CodeUpstreamUnavailable, "Magento is unavailable", true
		ambiguous = true
	}
	e := errs.New(code, message)
	e.Retryable = retryable
	e = e.WithOp("magento " + method + " " + path)
	e = e.WithDetail("upstream_status", strconv.Itoa(status))
	if storeCode != "" {
		e = e.WithDetail("store_code", storeCode)
	}
	if upstream := upstreamMessage(body); upstream != "" {
		e = e.WithDetail("upstream_message", upstream)
	}
	if ambiguous {
		e = markAmbiguous(e)
	}
	return e
}

// upstreamMessage extracts Magento's validation message without echoing request
// parameters, strips control characters, and bounds the length.
func upstreamMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Message == "" {
		return ""
	}
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, payload.Message)
	clean = strings.TrimSpace(clean)
	if runes := []rune(clean); len(runes) > 300 {
		clean = string(runes[:300])
	}
	return clean
}
func invalidf(format string, args ...any) error {
	return errs.New(errs.CodeInvalidArgument, fmt.Sprintf(format, args...))
}

// OutcomeDetail and OutcomeUnknown are the error-detail contract the MCP
// middleware reads to decide whether a failed write may have been applied
// upstream. internal/mcp/server declares the same two constants; they must stay
// identical (a server test asserts it).
const (
	OutcomeDetail  = "outcome"
	OutcomeUnknown = "unknown"
)

// markAmbiguous records that the request may have reached Magento and the
// outcome is therefore unknown. A caller must reconcile, never retry.
func markAmbiguous(e *errs.Error) *errs.Error {
	return e.WithDetail(OutcomeDetail, OutcomeUnknown)
}

// requestMayHaveBeenSent reports whether a transport failure happened after the
// request left this process. Only a failure to establish the connection (DNS,
// dial, connection refused) proves the request was never sent.
func requestMayHaveBeenSent(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		// A dial error means no connection was established; anything else is a
		// read/write failure on an established connection, i.e. after send.
		return opErr.Op != "dial"
	}
	// Unknown failure mode: assume the request may have been applied. Marking a
	// never-sent call ambiguous costs one reconciliation read; marking a sent
	// call "definitely failed" costs a duplicate charge.
	return true
}

// Observation is what an upstream read proves about an operation.
type Observation string

const (
	// ObservationApplied: the upstream system proves the mutation happened.
	ObservationApplied Observation = "applied"
	// ObservationNotApplied: it proves the mutation did not happen.
	ObservationNotApplied Observation = "not_applied"
	// ObservationInconclusive: the read could not decide (or the entity is in a
	// state that neither confirms nor denies the specific mutation).
	ObservationInconclusive Observation = "inconclusive"
)

// ObserveOperation applies the per-capability reconciliation read from
// docs/audit/reliability-review.md §3.3: it asks Magento what state the entity is
// in and reports whether that proves the operation applied. It never mutates.
//
// A read that fails for a transport reason returns ObservationInconclusive (the
// reconciler will back off), never ObservationNotApplied — an unreachable
// upstream must not be mistaken for "it did not happen".
func (s *Service) ObserveOperation(ctx context.Context, operation, entityType, entityID string) (Observation, error) {
	if s == nil || s.client == nil {
		return ObservationInconclusive, errs.New(errs.CodeInternal, "Magento service is not configured")
	}
	if entityID == "" {
		return ObservationInconclusive, nil
	}
	switch entityType {
	case "invoice":
		raw, err := s.client.do(ctx, http.MethodGet, "/invoices/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		var payload struct {
			State string `json:"state"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			return ObservationInconclusive, nil
		}
		switch strings.ToLower(payload.State) {
		case "paid", "complete":
			return ObservationApplied, nil
		case "open", "pending":
			return ObservationNotApplied, nil
		}
		return ObservationInconclusive, nil
	case "order":
		raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		var payload struct {
			Status string `json:"status"`
			State  string `json:"state"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			return ObservationInconclusive, nil
		}
		status := strings.ToLower(payload.Status)
		switch {
		case strings.HasSuffix(operation, ".cancel"):
			if status == "canceled" {
				return ObservationApplied, nil
			}
			return ObservationNotApplied, nil
		case strings.HasSuffix(operation, ".hold"):
			if status == "holded" {
				return ObservationApplied, nil
			}
			return ObservationNotApplied, nil
		case strings.HasSuffix(operation, ".unhold"):
			if status == "holded" {
				return ObservationNotApplied, nil
			}
			return ObservationApplied, nil
		}
		return ObservationInconclusive, nil
	case "creditmemo", "shipment":
		collection := "/creditmemos/"
		if entityType == "shipment" {
			collection = "/shipments/"
		}
		_, err := s.client.do(ctx, http.MethodGet, collection+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		return ObservationApplied, nil
	case "product":
		_, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		return ObservationApplied, nil
	case "category":
		_, err := s.client.do(ctx, http.MethodGet, "/categories/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		return ObservationApplied, nil
	case "customer":
		_, err := s.client.do(ctx, http.MethodGet, "/customers/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		return ObservationApplied, nil
	case "stock":
		query := url.Values{}
		query.Set("searchCriteria[filter_groups][0][filters][0][field]", "sku")
		query.Set("searchCriteria[filter_groups][0][filters][0][value]", entityID)
		raw, err := s.client.do(ctx, http.MethodGet, "/inventory/source-items", query, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		var payload struct {
			TotalCount int `json:"total_count"`
		}
		if json.Unmarshal(raw, &payload) != nil {
			return ObservationInconclusive, nil
		}
		if payload.TotalCount > 0 {
			return ObservationApplied, nil
		}
		return ObservationInconclusive, nil
	}
	return ObservationInconclusive, nil
}

// observeError turns a read error into an observation. A 404 proves the entity
// does not exist (not applied); any other error is inconclusive, because an
// unreachable upstream must never be read as "it did not happen".
func observeError(err error) (Observation, bool) {
	if err == nil {
		return "", false
	}
	if errs.CodeOf(err) == errs.CodeNotFound {
		return ObservationNotApplied, true
	}
	return ObservationInconclusive, true
}
