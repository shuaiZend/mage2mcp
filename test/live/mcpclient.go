// SPDX-License-Identifier: Apache-2.0

//go:build live

// Package main is the live functional-test harness for mage2mcp.
//
// It drives a deployed gateway over MCP against a real Magento store and records
// per-tool evidence. It is deliberately outside the normal test run: it needs a
// live endpoint, real credentials and a store that accepts writes, so it carries
// the `live` build tag and refuses to run without MCP_LIVE=1.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// endpoint is the MCP Streamable HTTP endpoint of the gateway under test.
func endpoint() string {
	if v := os.Getenv("MCP_ENDPOINT"); v != "" {
		return v
	}
	return "http://127.0.0.1:18080/mcp"
}

// instance is the configured Magento instance id every call routes to.
func instance() string {
	if v := os.Getenv("MCP_INSTANCE"); v != "" {
		return v
	}
	return "demo"
}

// approveCmd is a shell command that approves one confirmation. It receives the
// confirmation id as {id}. Empty means writes cannot be exercised.
func approveCmd() string { return os.Getenv("MCP_APPROVE_CMD") }

// Client is a minimal MCP client: initialize once, then tools/call.
type Client struct {
	mu      sync.Mutex
	session string
	nextID  int
	http    *http.Client
}

// NewClient returns a client with a bounded HTTP timeout.
func NewClient() *Client {
	return &Client{nextID: 1, http: &http.Client{Timeout: 180 * time.Second}}
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

// ToolResult is the part of a tools/call result the harness needs.
type ToolResult struct {
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	Content           []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// Code returns the taxonomy code from the structured error envelope, or "".
func (r *ToolResult) Code() string {
	if r == nil || !r.IsError {
		return ""
	}
	var envelope struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return ""
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Code
}

// Detail returns one detail field from the structured error envelope.
func (r *ToolResult) Detail(key string) string {
	var envelope struct {
		Details map[string]string `json:"details"`
	}
	raw, err := json.Marshal(r.StructuredContent)
	if err != nil {
		return ""
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return envelope.Details[key]
}

// Text returns the text content of the result.
func (r *ToolResult) Text() string {
	if r == nil {
		return ""
	}
	parts := make([]string, 0, len(r.Content))
	for _, c := range r.Content {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, " | ")
}

// post sends one JSON-RPC message and returns the decoded result.
func (c *Client) post(body map[string]any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		c.session = id
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	reader := bufio.NewReader(resp.Body)
	var payload []byte
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			payload = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		}
		if err != nil {
			break
		}
	}
	if len(payload) == 0 {
		body, _ := io.ReadAll(reader)
		if len(body) > 0 {
			payload = body
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(payload))
	}
	var decoded rpcResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode rpc: %w (%s)", err, string(payload))
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, fmt.Errorf("rpc error: %s", string(decoded.Error))
	}
	return decoded.Result, nil
}

// Initialize performs the MCP handshake.
func (c *Client) Initialize() error {
	_, err := c.post(map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "mage2mcp-live", "version": "1"},
		},
	})
	if err != nil {
		return err
	}
	c.nextID++
	_, err = c.post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return err
}

// Tool describes one registered tool.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListTools returns every registered tool.
func (c *Client) ListTools() ([]Tool, error) {
	c.nextID++
	raw, err := c.post(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": "tools/list"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

// Call invokes one tool. A confirmation-requiring write is approved and retried
// exactly as a real client must, when an approval command is configured.
func (c *Client) Call(name string, args map[string]any) (*ToolResult, error) {
	res, err := c.callOnce(name, args)
	if err != nil || res == nil || !res.IsError {
		return res, err
	}
	if res.Code() != "confirmation_required" {
		return res, nil
	}
	id := res.Detail("confirmation_id")
	if id == "" {
		return res, nil
	}
	if os.Getenv("MCP_NO_APPROVE") == "1" {
		// Probing mode: surface the pending confirmation instead of approving it.
		return res, nil
	}
	if approveCmd() == "" {
		return res, fmt.Errorf("write requires approval but MCP_APPROVE_CMD is not set")
	}
	if err := runApprove(id); err != nil {
		return res, fmt.Errorf("approve %s: %w", id, err)
	}
	approved := make(map[string]any, len(args)+1)
	for k, v := range args {
		approved[k] = v
	}
	approved["confirmation_id"] = id
	return c.callOnce(name, approved)
}

func (c *Client) callOnce(name string, args map[string]any) (*ToolResult, error) {
	c.nextID++
	raw, err := c.post(map[string]any{
		"jsonrpc": "2.0", "id": c.nextID, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		return nil, err
	}
	var res ToolResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode tools/call: %w", err)
	}
	return &res, nil
}

// runApprove executes the approval command for one confirmation id.
func runApprove(id string) error {
	cmdline := strings.ReplaceAll(approveCmd(), "{id}", id)
	cmd := exec.Command("sh", "-c", cmdline)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
