// SPDX-License-Identifier: Apache-2.0

// Package ga4 implements the Google Analytics 4 integration slice: a Data API
// client, the report model the tools expose, a curated compatibility validator,
// and the read-only MCP tool registrations.
package ga4

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
	defaultBaseURL   = "https://analyticsdata.googleapis.com/v1beta"
	defaultTimeout   = 30 * time.Second
	maxResponseBytes = 8 << 20
)

var (
	propertyIDRE  = regexp.MustCompile(`^[0-9]+$`)
	removeFieldRE = regexp.MustCompile(`(?i)please remove ([a-z][a-z0-9_:.]*)`)
)

// TokenSource supplies an OAuth access token for the Data API.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// StaticToken is a fixed bearer token for tests and local runs; production
// deployments must use a service-account token source.
type StaticToken string

// Token returns the static token, or an error when it is empty.
func (t StaticToken) Token(context.Context) (string, error) {
	if t == "" {
		return "", errors.New("ga4: static token is empty")
	}
	return string(t), nil
}

// Config configures a Client. BaseURL and Timeout have defaults; PropertyID and
// TokenSource are required.
type Config struct {
	BaseURL     string
	PropertyID  string
	TokenSource TokenSource
	HTTPClient  *http.Client
	Timeout     time.Duration
}

// Client calls the GA4 Data API for one property. It is safe for concurrent use.
type Client struct {
	baseURL    string
	propertyID string
	tokens     TokenSource
	httpClient *http.Client
	timeout    time.Duration
}

// NewClient validates cfg and returns a Data API client.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = defaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, fmt.Errorf("ga4: invalid base URL: %w", err)
	}
	ip := net.ParseIP(parsed.Hostname())
	loopback := parsed.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !loopback) {
		return nil, errors.New("ga4: base URL must use https; http is allowed only for loopback hosts")
	}
	if !propertyIDRE.MatchString(cfg.PropertyID) {
		return nil, errors.New("ga4: property ID must be digits only")
	}
	if cfg.TokenSource == nil {
		return nil, errors.New("ga4: a token source is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: base, propertyID: cfg.PropertyID, tokens: cfg.TokenSource, httpClient: httpClient, timeout: timeout}, nil
}

func (c *Client) propertyPath(suffix string) string { return "/properties/" + c.propertyID + suffix }

// MarshalJSON encodes the request in the Data API wire format, where dimensions
// and metrics are objects rather than plain names.
func (r ReportRequest) MarshalJSON() ([]byte, error) {
	refs := func(names []string) []map[string]string {
		out := make([]map[string]string, len(names))
		for i, name := range names {
			out[i] = map[string]string{"name": name}
		}
		return out
	}
	wire := map[string]any{"metrics": refs(r.Metrics)}
	if len(r.Dimensions) > 0 {
		wire["dimensions"] = refs(r.Dimensions)
	}
	if len(r.DateRanges) > 0 {
		wire["dateRanges"] = r.DateRanges
	}
	if r.Limit != 0 {
		wire["limit"] = r.Limit
	}
	if r.Offset != 0 {
		wire["offset"] = r.Offset
	}
	if len(r.DimensionFilter) > 0 {
		wire["dimensionFilter"] = r.DimensionFilter
	}
	if len(r.MetricAggregations) > 0 {
		wire["metricAggregations"] = r.MetricAggregations
	}
	orderBys := make([]map[string]any, 0, len(r.OrderBys))
	for _, order := range r.OrderBys {
		entry := map[string]any{"desc": order.Desc}
		switch {
		case order.Dimension != "":
			entry["dimension"] = map[string]string{"dimensionName": order.Dimension}
		case order.Metric != "":
			entry["metric"] = map[string]string{"metricName": order.Metric}
		}
		orderBys = append(orderBys, entry)
	}
	if len(orderBys) > 0 {
		wire["orderBys"] = orderBys
	}
	return json.Marshal(wire)
}

// runReport calls properties.runReport and maps the response into the report
// model. It is unexported: the Service owns the validated query shapes.
func (c *Client) runReport(ctx context.Context, req ReportRequest) (*ReportResponse, error) {
	const op = "ga4.runReport"
	body, err := c.do(ctx, op, http.MethodPost, c.propertyPath(":runReport"), req)
	if err != nil {
		return nil, err
	}
	var response ReportResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "GA4 returned an unreadable response").WithOp(op).WithCause(err)
	}
	return &response, nil
}

// do performs one authenticated Data API call. It caps the response size, maps
// HTTP failures to the shared error taxonomy, and never exposes the access
// token or the upstream payload to callers.
func (c *Client) do(ctx context.Context, op, method, path string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, errs.New(errs.CodeInternal, "failed to encode the GA4 request").WithOp(op).WithCause(err)
		}
		body = bytes.NewReader(encoded)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(callCtx, method, c.baseURL+path, body)
	if err != nil {
		return nil, errs.New(errs.CodeInternal, "failed to build the GA4 request").WithOp(op).WithCause(err)
	}
	token, err := c.tokens.Token(callCtx)
	if err != nil || token == "" {
		return nil, errs.New(errs.CodeUnauthenticated, "GA4 credentials are unavailable").WithOp(op).WithCause(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, transportError(op, err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, transportError(op, err)
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "GA4 response exceeded the 8 MiB limit").WithOp(op)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return data, nil
	}
	return nil, statusError(op, response.StatusCode, data)
}

func transportError(op string, err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return errs.New(errs.CodeUpstreamTimeout, "GA4 did not respond before the timeout").WithOp(op).WithCause(err)
	}
	retryable := errs.New(errs.CodeUpstreamUnavailable, "GA4 is unreachable").WithOp(op).WithCause(err)
	retryable.Retryable = true
	return retryable
}

func statusError(op string, status int, body []byte) error {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	field, incompatible := incompatibility(payload.Error.Message)
	code, text := errs.CodeUpstreamUnavailable, "GA4 returned an unexpected status"
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code, text = errs.CodePermissionDenied, "GA4 denied access to the property"
	case status == http.StatusBadRequest && incompatible:
		text = "GA4 cannot query this dimension and metric combination; split the report or remove one field"
		if field != "" {
			text = fmt.Sprintf("GA4 cannot query this combination; please remove %s to make the request compatible for example", field)
		}
		invalid := errs.New(errs.CodeValidationFailed, text).WithOp(op).
			WithDetail("reason", "incompatible").WithDetail("http_status", strconv.Itoa(status))
		if field != "" {
			_ = invalid.WithDetail("offending_field", field)
		}
		return invalid
	case status == http.StatusBadRequest:
		code, text = errs.CodeInvalidArgument, "GA4 rejected the report request as invalid"
	case status == http.StatusTooManyRequests:
		code, text = errs.CodeRateLimited, "GA4 rate limit exceeded"
	case status >= http.StatusInternalServerError:
		code, text = errs.CodeUpstreamUnavailable, "GA4 is temporarily unavailable"
	}
	failure := errs.New(code, text).WithOp(op).WithDetail("http_status", strconv.Itoa(status))
	failure.Retryable = code == errs.CodeRateLimited || status >= http.StatusInternalServerError
	return failure
}

// incompatibility reports whether an upstream message describes an incompatible
// dimension/metric combination, and names the offending field when GA4 does.
func incompatibility(message string) (string, bool) {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "incompatible") && !strings.Contains(lower, "not compatible") &&
		!strings.Contains(lower, "cannot be queried together") && !strings.Contains(lower, "please remove") {
		return "", false
	}
	if match := removeFieldRE.FindStringSubmatch(message); len(match) == 2 {
		return match[1], true
	}
	return "", true
}
