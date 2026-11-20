// SPDX-License-Identifier: Apache-2.0

// Package toolerr registers typed MCP tools whose failures carry a structured
// error envelope ({code, message, details}) instead of text only.
package toolerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Add registers a typed tool. On handler error the result is a tool error whose
// structuredContent contains the taxonomy code, the message, and any details.
func Add[In, Out any](s *mcp.Server, t *mcp.Tool, h func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, Out, error)) {
	if t.InputSchema == nil {
		if inferred, err := jsonschema.For[In](nil); err == nil {
			t.InputSchema = inferred
		}
	}
	if t.OutputSchema == nil {
		if inferred, err := jsonschema.For[Out](nil); err == nil {
			t.OutputSchema = inferred
		}
	}
	validator := compile(t.InputSchema)
	s.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in In
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			if validator != nil {
				if err := validator(req.Params.Arguments); err != nil {
					return errorResult(errs.New(errs.CodeInvalidArgument, "arguments do not match the tool schema").
						WithDetail("validation", err.Error())), nil
				}
			}
			if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
				return errorResult(fmt.Errorf("decoding arguments: %w", err)), nil
			}
		}
		res, out, err := h(ctx, req, in)
		if err != nil {
			return errorResult(err), nil
		}
		if res == nil {
			res = &mcp.CallToolResult{}
		}
		if res.IsError {
			return res, nil
		}
		raw, err := json.Marshal(out)
		if err != nil {
			return errorResult(fmt.Errorf("marshaling output: %w", err)), nil
		}
		res.StructuredContent = json.RawMessage(raw)
		if res.Content == nil {
			res.Content = []mcp.Content{&mcp.TextContent{Text: string(raw)}}
		}
		return res, nil
	})
}

func compile(input any) func([]byte) error {
	if input == nil {
		return nil
	}
	var raw []byte
	switch v := input.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	case *jsonschema.Schema:
		if v == nil {
			return nil
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = encoded
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		raw = encoded
	}
	var parsed jsonschema.Schema
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	resolved, err := parsed.Resolve(nil)
	if err != nil {
		return nil
	}
	return func(args []byte) error {
		var instance any
		if err := json.Unmarshal(args, &instance); err != nil {
			return err
		}
		return resolved.Validate(instance)
	}
}

// errorResult builds a tool error with a structured envelope.
func errorResult(err error) *mcp.CallToolResult {
	code := errs.CodeOf(err)
	message := err.Error()
	envelope := map[string]any{"code": string(code), "message": message}
	var taxonomy *errs.Error
	if errors.As(err, &taxonomy) {
		if taxonomy.Retryable {
			envelope["retryable"] = true
		}
		if taxonomy.Op != "" {
			envelope["op"] = taxonomy.Op
		}
		if len(taxonomy.Details) > 0 {
			envelope["details"] = taxonomy.Details
		}
	}
	raw, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		raw = []byte(`{"code":"internal","message":"tool error could not be encoded"}`)
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(code) + ": " + message}},
		StructuredContent: json.RawMessage(raw),
	}
}
