// SPDX-License-Identifier: Apache-2.0

// Package toolerr registers typed MCP tools whose failures carry a structured
// error envelope ({code, message, details}) instead of text only.
package toolerr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	if err := ValidateSchema(t); err != nil {
		panic(fmt.Sprintf("toolerr: %v", err))
	}
	validator := compile(t.InputSchema)
	s.AddTool(t, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in In
		if req.Params != nil && len(req.Params.Arguments) > 0 {
			if validator != nil {
				if err := validator(req.Params.Arguments); err != nil {
					return errorResult(errs.New(errs.CodeInvalidArgument, "arguments do not match the tool schema").
						WithDetail("validation", BoundedValidationDetail(err.Error()))), nil
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

// BoundedValidationDetail keeps a schema-validation message under the
// errs.WithDetail size cap. Without it a very large offending value makes
// WithDetail drop the whole detail, so the agent gets no reason at all.
func BoundedValidationDetail(msg string) string {
	const max = 900 // 900 + len("validation") < errs' 1024 cap
	if len(msg) <= max {
		return msg
	}
	return msg[:max] + "... (truncated)"
}

// ValidateSchema enforces the registration contract on a tool's input schema:
// it must be an object root, compile/resolve as JSON Schema, and close every
// object that declares properties with additionalProperties:false. Map-shaped
// objects (an additionalProperties schema with no properties) are allowed and
// are how typed key/value maps are represented.
//
// Add calls this during registration; a violation panics, and NewMCP converts
// the panic into a startup error.
func ValidateSchema(t *mcp.Tool) error {
	if t == nil {
		return fmt.Errorf("nil tool")
	}
	if t.InputSchema == nil {
		return fmt.Errorf("tool %q has no input schema", t.Name)
	}
	raw, err := json.Marshal(t.InputSchema)
	if err != nil {
		return fmt.Errorf("tool %q input schema is not JSON: %w", t.Name, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("tool %q input schema is not a JSON object: %w", t.Name, err)
	}
	if doc["type"] != "object" {
		return fmt.Errorf("tool %q input schema root must be type object", t.Name)
	}
	if err := requireClosed(t.Name, doc, "root"); err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("tool %q input schema does not compile: %w", t.Name, err)
	}
	if _, err := schema.Resolve(nil); err != nil {
		return fmt.Errorf("tool %q input schema does not resolve: %w", t.Name, err)
	}
	if err := requireDescribedOutput(t.Name, t.OutputSchema); err != nil {
		return err
	}
	return nil
}

// requireDescribedOutput rejects an output schema whose properties carry no
// description.
//
// The output schema is the only thing an agent sees when it reads a result, so an
// undocumented property is a field it has to guess at — and the guesses that matter
// are flags like `replayed` or `outcome`, where a wrong reading changes what the
// caller does next. This is checked at registration so a new output type cannot
// ship undescribed.
func requireDescribedOutput(name string, output any) error {
	if output == nil {
		return nil
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return fmt.Errorf("tool %q output schema is not JSON: %w", name, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("tool %q output schema is not a JSON object: %w", name, err)
	}
	return requireDescribedNode(name, doc, "root")
}

// requireDescribedNode walks an output schema and fails on the first property
// without a description.
func requireDescribedNode(name string, node map[string]any, path string) error {
	props, _ := node["properties"].(map[string]any)
	for key, value := range props {
		child, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if description, _ := child["description"].(string); strings.TrimSpace(description) == "" {
			return fmt.Errorf("tool %q output schema %s.%s has no description", name, path, key)
		}
		if err := requireDescribedNode(name, child, path+"."+key); err != nil {
			return err
		}
	}
	return nil
}

func requireClosed(name string, node map[string]any, path string) error {
	typ, _ := node["type"].(string)
	props, hasProps := node["properties"].(map[string]any)
	additional, hasAdditional := node["additionalProperties"]
	if path == "root" || hasProps || (typ == "object" && !hasAdditional) {
		closed, ok := additional.(bool)
		if !ok || closed {
			return fmt.Errorf("tool %q schema %s must set additionalProperties:false", name, path)
		}
	}
	for key, value := range props {
		if child, ok := value.(map[string]any); ok {
			if err := requireClosed(name, child, path+"."+key); err != nil {
				return err
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		if err := requireClosed(name, items, path+"[]"); err != nil {
			return err
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		branches, _ := node[key].([]any)
		for i, branch := range branches {
			if child, ok := branch.(map[string]any); ok {
				if err := requireClosed(name, child, fmt.Sprintf("%s.%s[%d]", path, key, i)); err != nil {
					return err
				}
			}
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		defs, _ := node[key].(map[string]any)
		for defName, def := range defs {
			if child, ok := def.(map[string]any); ok {
				if err := requireClosed(name, child, path+"."+key+"."+defName); err != nil {
					return err
				}
			}
		}
	}
	return nil
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

// Envelope is the single error envelope used by every tool boundary. It is
// exported so the server's middleware/denied paths can use the same shape as
// handler errors without duplicating the taxonomy rules.
//
// The message never includes the code or the operation label: the code is the
// machine-readable field, and the text fallback prefixes it exactly once. An
// error that is not an *errs.Error is treated as internal and its raw text is
// never serialized (that is how storage/SQLSTATE text stays out of tool
// results).
func Envelope(err error) map[string]any {
	code := errs.CodeOf(err)
	message := "an unexpected error occurred"
	// ADR-0010 says every error carries Retryable, so the field is always present:
	// a caller must not have to infer "false" from an absent key (U-30).
	envelope := map[string]any{"code": string(code), "message": message, "retryable": false}
	var taxonomy *errs.Error
	if errors.As(err, &taxonomy) {
		envelope["message"] = taxonomy.Message
		envelope["retryable"] = taxonomy.Retryable
		if taxonomy.Op != "" {
			envelope["op"] = taxonomy.Op
		}
		if len(taxonomy.Details) > 0 {
			envelope["details"] = taxonomy.Details
		}
	}
	return envelope
}

// ErrorResult renders err as a tool error using the uniform envelope.
func ErrorResult(err error) *mcp.CallToolResult {
	return resultFor(err)
}

// DeniedResult renders an authorization/confirmation denial with the same
// envelope as a handler error. The server's denied path must use this (or
// Envelope) rather than a reduced {code,message} map.
func DeniedResult(err error) *mcp.CallToolResult {
	return resultFor(err)
}

func resultFor(err error) *mcp.CallToolResult {
	envelope := Envelope(err)
	code, _ := envelope["code"].(string)
	message, _ := envelope["message"].(string)
	raw, marshalErr := json.Marshal(envelope)
	if marshalErr != nil {
		raw = []byte(`{"code":"internal","message":"an unexpected error occurred"}`)
	}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: errorText(code, message, envelope)}},
		StructuredContent: json.RawMessage(raw),
	}
}

// errorText renders the human-readable channel of a tool error.
//
// The text channel is all an agent sees when it prints a result, so it has to
// carry the reason the structured envelope already knows. A schema rejection used
// to read "invalid_argument: arguments do not match the tool schema" and nothing
// else, even though details.validation named the exact property and pattern; a
// denial said "permission denied" without details.reason (U-28). Both details are
// bounded by the caller, so the text stays bounded too.
func errorText(code, message string, envelope map[string]any) string {
	text := code + ": " + message
	for _, key := range []string{"validation", "reason"} {
		detail := detailString(envelope["details"], key)
		if detail == "" {
			continue
		}
		text += " (" + key + ": " + detail + ")"
	}
	return text
}

// detailString reads one detail from either shape an envelope can carry: the
// taxonomy's map[string]string or a decoded JSON object.
func detailString(details any, key string) string {
	switch typed := details.(type) {
	case map[string]string:
		return typed[key]
	case map[string]any:
		value, _ := typed[key].(string)
		return value
	}
	return ""
}

// errorResult is the package-local alias used by Add; it is the same renderer
// as ErrorResult so a handler error cannot drift from the exported contract.
func errorResult(err error) *mcp.CallToolResult {
	return ErrorResult(err)
}
