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
	"sort"
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
	// Both branches below assign message; an initial value here was dead (ineffassign).
	var message string
	if exact {
		message = fmt.Sprintf("Magento response exceeded the %d-byte limit by %d bytes", int64(maxResponseBytes), over)
	} else {
		message = fmt.Sprintf("Magento response exceeded the %d-byte limit by at least %d bytes", int64(maxResponseBytes), over)
	}
	// The taxonomy is closed, so an oversized upstream body is reported as
	// upstream_unavailable with explicit details; the typed cause carries the
	// same numbers for Go callers.
	e := errs.New(errs.CodeUpstreamUnavailable, message+"; narrow the query (fewer fields or a smaller page)")
	e = e.WithOp("magento " + method + " " + redactedRoute(path))
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
	e = e.WithOp("magento " + method + " " + redactedRoute(path))
	e = e.WithDetail("upstream_status", strconv.Itoa(status))
	if storeCode != "" {
		e = e.WithDetail("store_code", storeCode)
	}
	// Magento's reason is the only thing that lets a caller correct its own
	// request ("postcode can not be empty", "Can not save link related to Default
	// Source or Default Stock"). The agent-facing Message above stays generic; the
	// scrubbed reason and the field names go into details, and the unscrubbed text
	// stays in Upstream for the server log.
	diag := parseUpstreamDiagnostic(body)
	// A business rule that names a transient cause is retryable, and a rule that
	// names a state conflict is a conflict; the prose alone is not something an
	// integration can branch on (U-26, U-27).
	if reason, conflict, transient := classifyUpstreamReason(diag.Raw); reason != "" {
		if conflict && (code == errs.CodeValidationFailed || code == errs.CodeConflict) {
			e.Code = errs.CodeConflict
			e.Message = "Magento rejected the request because of the entity's state"
		}
		if transient {
			e.Code = errs.CodeConflict
			e.Message = "Magento could not apply the request because of upstream contention; retry it"
			e.Retryable = true
		}
		e = e.WithDetail("reason", reason)
	}
	e = withUpstreamDiagnostic(e, diag)
	if ambiguous {
		e = markAmbiguous(e)
	}
	return e
}

// routeWords are the literal path segments this client uses. Anything else in a
// path is an identifier: a SKU, a document id, an address id. The allowlist is
// fail-safe — a segment it does not know is redacted, so a new route loses detail
// in the operation label rather than leaking the identifier it carries.
var routeWords = map[string]struct{}{
	"products": {}, "orders": {}, "invoices": {}, "creditmemos": {}, "shipments": {},
	"carts": {}, "customers": {}, "addresses": {}, "media": {}, "links": {}, "options": {},
	"tierPrices": {}, "attributes": {}, "attribute-sets": {}, "categories": {}, "configurable": {},
	"inventory": {}, "sources": {}, "stocks": {}, "source-items": {}, "bulk": {}, "async": {},
	"import": {}, "integration": {}, "admin": {}, "token": {}, "search": {}, "comments": {},
	"emails": {}, "hold": {}, "unhold": {}, "cancel": {}, "capture": {}, "items": {},
	"ship":                 {},
	"shipping-information": {}, "totals": {}, "order": {}, "store": {}, "storeConfigs": {},
	"taxClasses": {}, "directory": {}, "countries": {}, "groups": {},
	"stockItems": {}, "configurable-products": {}, "children": {},
	"invoice": {}, "shipment": {}, "creditmemo": {}, "refund": {}, "refunds": {},
	"transactions": {}, "payment": {},
}

// redactedRoute replaces identifier segments with {id} so an operation label can
// be logged and shown without disclosing which SKU or document was addressed.
func redactedRoute(path string) string {
	if path == "" {
		return ""
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		if _, known := routeWords[segment]; known {
			continue
		}
		segments[i] = "{id}"
	}
	return strings.Join(segments, "/")
}

// upstreamMessage extracts Magento's validation message without echoing request
// parameters, strips control characters, and bounds the length.
// upstreamDiagnostic is Magento's own account of why it rejected a request,
// reduced to what a caller can act on: the most specific message (with Magento's
// "%field" placeholders resolved) and the field names Magento named.
type upstreamDiagnostic struct {
	// Message is the most specific reason, scrubbed and bounded.
	Message string
	// Fields are the parameter values that name the offending field, for example
	// postcode or regionId. Identifiers only, never free text.
	Fields []string
	// Raw is the unscrubbed text, kept for the server log only.
	Raw string
}

const (
	// maxUpstreamDetailRunes bounds what reaches a caller: enough to diagnose,
	// too short to become a data channel.
	maxUpstreamDetailRunes = 300
	maxUpstreamFields      = 3
)

var (
	// upstreamFieldRE accepts a field name and nothing that looks like data.
	upstreamFieldRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.\-]{0,63}$`)
	// upstreamEmailRE and upstreamTokenRE redact PII and credential-shaped values
	// before any upstream text is serialised.
	upstreamEmailRE = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	upstreamTokenRE = regexp.MustCompile(`[A-Za-z0-9_\-]{32,}`)
	// upstreamPlaceholderRE matches Magento's "%field"-style placeholders.
	// Magento writes placeholders as "%field"; the quotes are part of the template,
	// so they are consumed with it and the field name stands on its own.
	upstreamPlaceholderRE = regexp.MustCompile(`"?%([A-Za-z][A-Za-z0-9_]*)"?`)
	// upstreamNumericPlaceholderRE matches Magento's numbered "%1" templates,
	// which arrive with `parameters` as an array.
	upstreamNumericPlaceholderRE = regexp.MustCompile(`%[0-9]+`)
	// The next three detect the identifiers SEC-10 keeps out of the agent-visible
	// surface: ACL resource names, quoted attribute codes, and SKU-shaped tokens.
	upstreamACLRE    = regexp.MustCompile(`[A-Za-z_]+::[A-Za-z_]+`)
	upstreamQuotedRE = regexp.MustCompile(`"[^"]*"`)
	// upstreamSKURE matches an identifier that mixes letters and digits. A bare
	// number is not a SKU: matching it dropped whole reasons such as
	// "Większość pieniędzy do zwrotu to 16." (F5), which is exactly the
	// money-failure text a caller needs.
	upstreamSKURE = regexp.MustCompile(`[A-Z0-9_-]*([A-Z][A-Z0-9_-]*[0-9]|[0-9][A-Z0-9_-]*[A-Z])[A-Z0-9_-]*`)
)

// upstreamTextIsSafe reports whether Magento's reason can be carried in a detail.
// SEC-10 keeps upstream text server-side because it echoes SKUs, attribute codes
// and ACL resource names; a reason that names none of those is just a rule
// statement ("Can not save link related to Default Source or Default Stock") and
// is exactly what a caller needs to correct its request. Anything carrying an
// identifier stays in Upstream, the server log.
func upstreamTextIsSafe(text string) bool {
	if text == "" {
		return false
	}
	return !upstreamACLRE.MatchString(text) &&
		!upstreamQuotedRE.MatchString(text) &&
		!upstreamSKURE.MatchString(text)
}

// redactUpstreamText keeps Magento's reason readable while removing the
// identifiers SEC-10 protects. A message that only failed upstreamTextIsSafe
// because it quoted a field name or echoed a SKU still tells the caller what
// went wrong; dropping it entirely (U-24) left `details` with nothing but
// upstream_status, so an operator could not tell a bad payload from a bad state.
func redactUpstreamText(text string) string {
	if text == "" {
		return ""
	}
	redacted := upstreamQuotedRE.ReplaceAllString(text, `"<value>"`)
	redacted = upstreamSKURE.ReplaceAllString(redacted, "<sku>")
	redacted = upstreamACLRE.ReplaceAllString(redacted, "<acl>")
	return redacted
}

// withUpstreamDiagnostic attaches a parsed diagnostic to an error using one
// policy for every upstream path: the generic message stays agent-facing, the
// scrubbed reason goes to upstream_message, and a reason that only carries
// identifiers goes to upstream_reason in redacted form.
func withUpstreamDiagnostic(e *errs.Error, diag upstreamDiagnostic) *errs.Error {
	if diag.Message != "" {
		switch {
		case upstreamTextIsSafe(diag.Message):
			e = e.WithDetail("upstream_message", diag.Message)
		case upstreamACLRE.MatchString(diag.Message) || upstreamSKURE.MatchString(diag.Message):
			// An identifier-bearing reason stays server-side: redacting it still
			// leaves Magento's internals (ACL names, SKU assignments) in the
			// agent-visible surface, which SEC-10 forbids.
		default:
			// The message failed only because it quoted a field name or an
			// attribute code. Redacting those keeps the sentence a caller can act
			// on without leaking the identifier (U-24).
			if redacted := redactUpstreamText(diag.Message); strings.TrimSpace(redacted) != "" {
				e = e.WithDetail("upstream_reason", redacted)
			}
		}
	}
	if len(diag.Fields) > 0 {
		e = e.WithDetail("upstream_field", strings.Join(diag.Fields, ","))
	}
	if diag.Raw != "" {
		e = e.WithUpstream(diag.Raw)
	}
	return e
}

// upstreamTransientRE matches the upstream answers that are safe to retry: the
// write did not commit, but the cause is contention or a transient lock, not the
// caller's payload. Reporting them as validation_failed (U-26) told an
// integration never to retry a request that would have succeeded.
var upstreamTransientRE = regexp.MustCompile(`(?i)(deadlock|lock wait timeout|sqlstate\[40001\]|the linked products data is invalid|unable to be saved|try again later|please try again)`)

// upstreamStateConflictRE matches business-rule rejections that are really state
// conflicts, so a caller gets `conflict` instead of `validation_failed` (U-27).
var upstreamStateConflictRE = regexp.MustCompile(`(?i)(has status closed|status closed|already been canceled|already canceled|already invoiced|already paid|can not be canceled|can't be canceled)`)

// classifyUpstreamReason returns a stable reason code for the reasons a caller
// has to branch on. Prose in a Polish message is not something an integration
// can program against.
func classifyUpstreamReason(text string) (reason string, conflict bool, retryable bool) {
	if text == "" {
		return "", false, false
	}
	if upstreamTransientRE.MatchString(text) {
		return "transient_upstream_error", true, true
	}
	if upstreamStateConflictRE.MatchString(text) {
		return "document_state_conflict", true, false
	}
	if strings.Contains(strings.ToLower(text), "najmniej, ile możesz zakupić") {
		return "min_qty_violation", false, false
	}
	if strings.Contains(strings.ToLower(text), "without products") {
		return "no_refundable_items", false, false
	}
	return "", false, false
}

// decodeUpstreamBusinessError reports a business failure that Magento encoded in
// an HTTP 200 body. Several write routes answer 200 with a JSON array of
// {"message": ...} objects when the operation did not happen; treating the status
// code as the verdict made tier-price writes report set:true with zero writes
// (U-07). A body that is not an error array is not an error.
//
//nolint:unparam // method feeds the error's op label; today every 200-with-error route is POST, but the helper is shared and a future one may be PUT (the refund route already is).
func decodeUpstreamBusinessError(method, path string, raw []byte, storeCode string) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil
	}
	var items []struct {
		Message    string          `json:"message"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil
	}
	messages := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Message) != "" {
			messages = append(messages, item.Message)
		}
	}
	if len(messages) == 0 {
		return nil
	}
	body, _ := json.Marshal(map[string]any{"message": strings.Join(messages, " | ")})
	diag := parseUpstreamDiagnostic(body)
	reason, conflict, retryable := classifyUpstreamReason(diag.Raw)
	code, message := errs.CodeValidationFailed, "Magento reported a business failure; the request did not apply"
	if conflict {
		code, message = errs.CodeConflict, "Magento rejected the request because of the entity's state"
	}
	e := errs.New(code, message)
	e.Retryable = retryable
	e = e.WithOp("magento " + method + " " + redactedRoute(path))
	e = e.WithDetail("upstream_status", "200")
	e = e.WithDetail("upstream_business_error", "true")
	if storeCode != "" {
		e = e.WithDetail("store_code", storeCode)
	}
	if reason != "" {
		e = e.WithDetail("reason", reason)
	}
	return withUpstreamDiagnostic(e, diag)
}

// parseUpstreamDiagnostic extracts Magento's reason from an error body. Magento
// has two shapes: a bare {"message": ...}, and {"message":"Validation Failed",
// "errors":[{"message":"\"%field\" can not be empty.","parameters":{"field":
// "postcode"}}]}. The bare top-level "Validation Failed" carries no information,
// so the errors array is preferred.
func parseUpstreamDiagnostic(body []byte) upstreamDiagnostic {
	if len(body) == 0 {
		return upstreamDiagnostic{}
	}
	// parameters is RawMessage because Magento sends [] (an array) when it has no
	// parameters; unmarshalling straight into a map would discard the whole body,
	// which is exactly how the business-rule reasons were being lost.
	var payload struct {
		Message    string          `json:"message"`
		Parameters json.RawMessage `json:"parameters"`
		Errors     []struct {
			Message    string          `json:"message"`
			Parameters json.RawMessage `json:"parameters"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return upstreamDiagnostic{}
	}

	rawParts := make([]string, 0, len(payload.Errors)+1)
	if payload.Message != "" {
		rawParts = append(rawParts, payload.Message)
	}

	var diag upstreamDiagnostic
	// Prefer the first error that says something more specific than the generic
	// envelope, and resolve its placeholders against its own parameters.
	for _, item := range payload.Errors {
		if item.Message != "" {
			rawParts = append(rawParts, item.Message)
		}
		if diag.Message != "" || isGenericUpstreamMessage(item.Message) {
			continue
		}
		params := upstreamParamMap(item.Parameters)
		diag.Message = resolveUpstreamPlaceholders(item.Message, params)
		diag.Fields = upstreamFields(params)
	}
	if diag.Message == "" && !isGenericUpstreamMessage(payload.Message) {
		params := upstreamParamMap(payload.Parameters)
		diag.Message = resolveUpstreamPlaceholders(payload.Message, params)
		diag.Fields = upstreamFields(params)
	}
	// Field names are useful even when every message was generic.
	if len(diag.Fields) == 0 {
		for _, item := range payload.Errors {
			if fields := upstreamFields(upstreamParamMap(item.Parameters)); len(fields) > 0 {
				diag.Fields = fields
				break
			}
		}
	}
	if len(diag.Fields) == 0 {
		diag.Fields = upstreamFields(upstreamParamMap(payload.Parameters))
	}

	diag.Message = scrubUpstreamText(diag.Message)
	diag.Raw = strings.Join(rawParts, " | ")
	if runes := []rune(diag.Raw); len(runes) > maxUpstreamMessageBytes {
		diag.Raw = string(runes[:maxUpstreamMessageBytes])
	}
	return diag
}

// upstreamParamMap tolerates Magento's shapes: an object, an array, or absent.
// Magento sends `parameters` as an array for `%1`-style templates, and an array
// used to yield no parameters at all, which is why those placeholders reached
// callers unreplaced (U-25). Array entries are keyed "1", "2", ... to match the
// template numbering.
func upstreamParamMap(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var params map[string]any
	if err := json.Unmarshal(raw, &params); err == nil {
		return params
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil
	}
	if len(list) == 0 {
		return nil
	}
	params = make(map[string]any, len(list))
	for i, value := range list {
		params[strconv.Itoa(i+1)] = value
	}
	return params
}

func isGenericUpstreamMessage(message string) bool {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "validation failed", "bad request", "error", "invalid request":
		return true
	}
	return false
}

// resolveUpstreamPlaceholders turns Magento's "%field" and "%1" templates into
// the text a caller can act on. A placeholder with no matching parameter becomes
// an explicit marker rather than a raw "%1" the caller has to guess at (U-25).
// Only field-name-shaped values are substituted: a numeric placeholder whose
// parameter is free text (a SKU, a message) must not become an identifier leak.
func resolveUpstreamPlaceholders(message string, parameters map[string]any) string {
	if !strings.Contains(message, "%") {
		return message
	}
	if len(parameters) == 0 {
		// Both template shapes become an explicit marker: a raw "%customerGroup"
		// or "%1" is a template the caller cannot act on (U-25, F4).
		resolved := upstreamPlaceholderRE.ReplaceAllString(message, "(omitted)")
		return upstreamNumericPlaceholderRE.ReplaceAllString(resolved, "(omitted)")
	}
	lower := make(map[string]string, len(parameters))
	for key, value := range parameters {
		text, ok := value.(string)
		if !ok {
			continue
		}
		lower[strings.ToLower(key)] = text
	}
	resolved := upstreamPlaceholderRE.ReplaceAllStringFunc(message, func(token string) string {
		name := strings.ToLower(strings.Trim(token, `"`)[1:])
		if value, ok := lower[name]; ok {
			return value
		}
		// Magento sends `parameters` as an array for some templates, so a named
		// placeholder such as %customerGroup has no matching key and used to be
		// echoed verbatim (F4). An explicit omission marker is actionable; a
		// template token is not.
		return "(omitted)"
	})
	return upstreamNumericPlaceholderRE.ReplaceAllStringFunc(resolved, func(token string) string {
		name := strings.TrimPrefix(token, "%")
		value, ok := lower[name]
		if !ok || !upstreamFieldRE.MatchString(value) {
			return "(omitted)"
		}
		return value
	})
}

// upstreamFields keeps only parameter values that look like field names, so a
// parameter carrying a value or a message can never become the reported field.
func upstreamFields(parameters map[string]any) []string {
	if len(parameters) == 0 {
		return nil
	}
	keys := make([]string, 0, len(parameters))
	for key := range parameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]string, 0, maxUpstreamFields)
	seen := make(map[string]bool, maxUpstreamFields)
	for _, key := range keys {
		value, ok := parameters[key].(string)
		if !ok || !upstreamFieldRE.MatchString(value) || seen[value] {
			continue
		}
		seen[value] = true
		fields = append(fields, value)
		if len(fields) == maxUpstreamFields {
			break
		}
	}
	return fields
}

// scrubUpstreamText removes control characters, redacts PII and credential-shaped
// values, and bounds the length. This is what makes it safe to carry Magento's
// text in a detail instead of only in the server log.
func scrubUpstreamText(text string) string {
	if text == "" {
		return ""
	}
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	clean = upstreamEmailRE.ReplaceAllString(clean, "<email>")
	clean = upstreamTokenRE.ReplaceAllString(clean, "<redacted>")
	clean = strings.TrimSpace(clean)
	if runes := []rune(clean); len(runes) > maxUpstreamDetailRunes {
		clean = string(runes[:maxUpstreamDetailRunes]) + "…"
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
	// ObservationApplied means the upstream system proves the mutation happened.
	ObservationApplied Observation = "applied"
	// ObservationNotApplied means it proves the mutation did not happen.
	ObservationNotApplied Observation = "not_applied"
	// ObservationInconclusive means the read could not decide (or the entity is in
	// a state that neither confirms nor denies the specific mutation).
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
			State string `json:"state" jsonschema:"Current state."`
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
		// magento.order.create no longer files itself under this entity type: it
		// records the customer it places the order for (U-70), so a *new*
		// order-typed row always names a real order. The guard stays because rows
		// written before that fix still carry entity_type=order with a customer id
		// in entity_id; reading /orders/{that id} would fetch someone else's order
		// and could report a placement as applied that never happened. Such rows
		// are inconclusive by construction and belong to a human.
		if operation == "magento.order.create" {
			return ObservationInconclusive, nil
		}
		raw, err := s.client.do(ctx, http.MethodGet, "/orders/"+url.PathEscape(entityID), nil, nil)
		if observation, done := observeError(err); done {
			return observation, nil
		}
		var payload struct {
			Status string `json:"status" jsonschema:"Current status."`
			State  string `json:"state" jsonschema:"Current state."`
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
		// magento.order.create records the customer, not the order, because the
		// order does not exist yet when the operation begins (U-70). A customer
		// that exists proves nothing about whether an order was placed for it:
		// reading /customers/{id} would report an ambiguous placement as applied,
		// so this operation is inconclusive by construction and is left to the
		// placement path and to a human. Every other customer-typed operation
		// (customer.update, ...) is still decided by the read below.
		if operation == "magento.order.create" {
			return ObservationInconclusive, nil
		}
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
