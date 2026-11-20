// SPDX-License-Identifier: Apache-2.0

package toolerr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func newSession(t *testing.T, register func(*mcp.Server)) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	register(srv)
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

type echoInput struct {
	Value string `json:"value"`
}

type echoOutput struct {
	Echo string `json:"echo"`
}

func TestStructuredErrorEnvelope(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.fail", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(context.Context, *mcp.CallToolRequest, echoInput) (*mcp.CallToolResult, echoOutput, error) {
				return nil, echoOutput{}, errs.New(errs.CodeNotFound, "missing resource").WithDetail("resource", "product")
			})
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "test.fail", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected a tool error")
	}
	envelope, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured content = %T %v", res.StructuredContent, res.StructuredContent)
	}
	if envelope["code"] != "not_found" {
		t.Fatalf("code = %v", envelope["code"])
	}
	details, _ := envelope["details"].(map[string]any)
	if details["resource"] != "product" {
		t.Fatalf("details = %v", envelope["details"])
	}
}

func TestStructuredSuccessContent(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.ok", InputSchema: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`)},
			func(_ context.Context, _ *mcp.CallToolRequest, in echoInput) (*mcp.CallToolResult, echoOutput, error) {
				return nil, echoOutput{Echo: in.Value}, nil
			})
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "test.ok", Arguments: map[string]any{"value": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var out echoOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Echo != "hi" {
		t.Fatalf("out = %+v", out)
	}
}

func TestInvalidArgumentsEnvelope(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.schema",
			InputSchema: json.RawMessage(`{"type":"object","required":["value"],"additionalProperties":false,"properties":{"value":{"type":"string"}}}`)},
			func(context.Context, *mcp.CallToolRequest, echoInput) (*mcp.CallToolResult, echoOutput, error) {
				return nil, echoOutput{}, nil
			})
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "test.schema", Arguments: map[string]any{"bogus": true}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected schema validation error")
	}
	envelope, _ := res.StructuredContent.(map[string]any)
	if envelope["code"] != "invalid_argument" {
		t.Fatalf("envelope = %v", res.StructuredContent)
	}
}
