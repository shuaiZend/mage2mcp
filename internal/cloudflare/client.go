// SPDX-License-Identifier: Apache-2.0

// Package cloudflare implements the read-only Cloudflare module: a bounded
// REST/GraphQL client and the traffic and security analytics tools built on it.
package cloudflare

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
	"strconv"
	"strings"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultTimeout        = 30 * time.Second
	maxResponseBytes      = 8 << 20
	maxGraphQLErrorDetail = 200
)

// Config configures a Cloudflare client; the module reads no environment.
type Config struct {
	// APIBaseURL is the Cloudflare API root; plain HTTP is allowed only for
	// loopback hosts, which keeps local and httptest servers usable.
	APIBaseURL string
	// APIToken is a scoped Cloudflare API token; it never appears in an error.
	APIToken string
	// ZoneID is the single zone this client is bound to.
	ZoneID string
	// AccountID is reserved for account-scoped endpoints.
	AccountID string
	// HTTPClient overrides the HTTP transport; nil means a default client.
	HTTPClient *http.Client
	// Timeout bounds each upstream request; zero means 30 seconds.
	Timeout time.Duration
}

// Client talks to one Cloudflare zone with a bearer token; it is safe for
// concurrent use.
type Client struct {
	baseURL string
	token   string
	zoneID  string
	http    *http.Client
	timeout time.Duration
}

// NewClient validates cfg and returns a client bound to cfg.ZoneID.
func NewClient(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(cfg.APIBaseURL))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: invalid API base URL: %w", err)
	}
	if base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("cloudflare: API base URL must be absolute and free of credentials, query, and fragment")
	}
	if base.Scheme != "https" && (base.Scheme != "http" || !isLoopbackHost(base.Hostname())) {
		return nil, errors.New("cloudflare: API base URL must use https (http is allowed only for 127.0.0.1 or localhost)")
	}
	token := strings.TrimSpace(cfg.APIToken)
	if token == "" {
		return nil, errors.New("cloudflare: API token is required")
	}
	zoneID := strings.TrimSpace(cfg.ZoneID)
	if zoneID == "" {
		return nil, errors.New("cloudflare: zone ID is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{
		baseURL: strings.TrimRight(base.String(), "/"),
		token:   token,
		zoneID:  zoneID,
		http:    httpClient,
		timeout: timeout,
	}, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Service is the Cloudflare module bound to a single client, and therefore to a
// single zone.
type Service struct {
	client *Client
}

// NewService returns a service that reads Cloudflare through client.
func NewService(client *Client) *Service { return &Service{client: client} }

func (s *Service) clientOrError(op string) (*Client, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "cloudflare service is not configured").WithOp(op)
	}
	return s.client, nil
}

// do performs one REST call, attaches the bearer token, and decodes JSON into out.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if !strings.HasPrefix(path, "/") {
		return errs.New(errs.CodeInternal, "cloudflare request path must be absolute")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return errs.New(errs.CodeInternal, "cloudflare: encode request body").WithCause(err)
		}
		payload = bytes.NewReader(encoded)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, payload) //nolint:gosec // G107: the base URL is validated in NewClient and the path is internal.
	if err != nil {
		return errs.New(errs.CodeInternal, "cloudflare: build request").WithCause(err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return transportError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode > 299 {
		return statusError(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return transportError(err)
	}
	if len(data) > maxResponseBytes {
		return errs.New(errs.CodeUpstreamUnavailable, "Cloudflare response exceeded the 8 MiB limit")
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errs.New(errs.CodeUpstreamUnavailable, "Cloudflare returned an empty response")
	}
	if err := json.Unmarshal(data, out); err != nil {
		return errs.New(errs.CodeUpstreamUnavailable, "Cloudflare returned malformed JSON").WithCause(err)
	}
	return nil
}

// statusError maps an HTTP status onto the closed error taxonomy.
func statusError(status int) error {
	code, message := errs.CodeUpstreamUnavailable, "Cloudflare rejected the request"
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code, message = errs.CodePermissionDenied, "Cloudflare denied the request; check the API token permissions"
	case status == http.StatusNotFound:
		code, message = errs.CodeNotFound, "Cloudflare resource not found"
	case status == http.StatusTooManyRequests:
		code, message = errs.CodeRateLimited, "Cloudflare rate limit exceeded; retry after the budget resets"
	case status >= http.StatusInternalServerError:
		message = "Cloudflare is temporarily unavailable"
	}
	e := errs.New(code, message).WithDetail("upstream_status", strconv.Itoa(status))
	e.Retryable = status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
	return e
}

// transportError maps a transport failure without leaking its raw text.
func transportError(err error) error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return errs.New(errs.CodeUpstreamTimeout, "Cloudflare request timed out").WithCause(err)
	}
	e := errs.New(errs.CodeUpstreamUnavailable, "Cloudflare request failed").WithCause(err)
	e.Retryable = !errors.Is(err, context.Canceled)
	return e
}

// graphql executes one fixed document; Cloudflare reports query-level failures
// as HTTP 200 with a non-empty "errors" array whose first message is bounded.
func (c *Client) graphql(ctx context.Context, query string, variables map[string]any, out any) error {
	if strings.TrimSpace(query) == "" {
		return errs.New(errs.CodeInternal, "cloudflare GraphQL query is empty")
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	body := struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{Query: query, Variables: variables}
	if err := c.do(ctx, http.MethodPost, "/graphql", body, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		e := errs.New(errs.CodeUpstreamUnavailable, "Cloudflare GraphQL returned an error")
		if msg := truncate(strings.TrimSpace(envelope.Errors[0].Message), maxGraphQLErrorDetail); msg != "" {
			e = e.WithDetail("upstream_message", msg)
		}
		return e
	}
	trimmed := bytes.TrimSpace(envelope.Data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return errs.New(errs.CodeUpstreamUnavailable, "Cloudflare GraphQL returned no data")
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(trimmed, out); err != nil {
		return errs.New(errs.CodeUpstreamUnavailable, "Cloudflare GraphQL returned malformed data").WithCause(err)
	}
	return nil
}

// truncate cuts s to at most limit runes without splitting a UTF-8 sequence.
func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
