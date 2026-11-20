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
)

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
}

// Client is a bounded Magento 2 REST client for {BaseURL}/rest/{StoreCode}/V1.
type Client struct {
	BaseURL    string
	StoreCode  string
	HTTPClient *http.Client
	Signer     Signer
	Timeout    time.Duration
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
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }} // never chase redirects
	}
	return &Client{BaseURL: strings.TrimRight(raw, "/"), StoreCode: cfg.StoreCode, HTTPClient: httpClient, Signer: cfg.Signer, Timeout: timeout}, nil
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento response could not be read").WithOp(method + " " + path).WithCause(err)
	}
	return raw, nil
}
func transportError(ctx context.Context, err error) error {
	op := "magento request"
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return errs.New(errs.CodeUpstreamTimeout, "Magento did not answer before the deadline; the call may have completed").WithOp(op).WithCause(err)
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return errs.New(errs.CodeUpstreamTimeout, "Magento request was canceled before a response arrived").WithOp(op).WithCause(err)
	}
	e := errs.New(errs.CodeUpstreamUnavailable, "Magento is unreachable")
	e.Retryable = true
	return e.WithOp(op).WithCause(err)
}
func statusError(method, path string, status int, body []byte, storeCode string) error {
	code, message, retryable := errs.CodeUpstreamUnavailable, "Magento returned an unexpected HTTP status", false
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
		code, message, retryable = errs.CodeUpstreamUnavailable, "Magento is unavailable", true
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
