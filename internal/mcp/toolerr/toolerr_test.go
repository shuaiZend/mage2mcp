// SPDX-License-Identifier: Apache-2.0

package toolerr

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
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
	Echo string `json:"echo" jsonschema:"Echoed request value, for tests."`
}

func TestStructuredErrorEnvelope(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.fail", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string"}}}`)},
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
		Add(srv, &mcp.Tool{Name: "test.ok", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string"}}}`)},
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

// TestErrorEnvelopeIsUniform proves handler and denied results use one shape:
// the same code/message/retryable/op/details fields, and a message that does
// not repeat the code.
func TestErrorEnvelopeIsUniform(t *testing.T) {
	typed := errs.New(errs.CodeNotFound, "missing resource").
		WithOp("magento GET /products/X").
		WithDetail("resource", "product")
	typed.Retryable = true

	handler := ErrorResult(typed)
	denied := DeniedResult(typed)
	handlerBody := structured(t, handler)
	deniedBody := structured(t, denied)
	if !reflect.DeepEqual(handlerBody, deniedBody) {
		t.Fatalf("handler envelope = %v, denied envelope = %v", handlerBody, deniedBody)
	}
	for _, key := range []string{"code", "message", "retryable", "op", "details"} {
		if _, ok := handlerBody[key]; !ok {
			t.Errorf("envelope is missing %q: %v", key, handlerBody)
		}
	}
	if handlerBody["code"] != "not_found" {
		t.Fatalf("code = %v, want not_found", handlerBody["code"])
	}
	if handlerBody["message"] != "missing resource" {
		t.Fatalf("message = %q, want the message without the code/op prefix", handlerBody["message"])
	}
	if handlerBody["op"] != "magento GET /products/X" {
		t.Fatalf("op = %v", handlerBody["op"])
	}
	if handlerBody["retryable"] != true {
		t.Fatalf("retryable = %v, want true", handlerBody["retryable"])
	}
	if !strings.Contains(text(t, handler), "not_found: missing resource") {
		t.Fatalf("text = %q", text(t, handler))
	}
}

// TestErrorTextContainsCodeOnce pins the double-prefix defect: the rendered
// text must contain the code exactly once, not "not_found: not_found: ...".
func TestErrorTextContainsCodeOnce(t *testing.T) {
	err := errs.New(errs.CodeNotFound, "unknown Magento instance")
	res := ErrorResult(err)
	rendered := text(t, res)
	if got := strings.Count(rendered, "not_found"); got != 1 {
		t.Fatalf("text %q contains the code %d times, want exactly once", rendered, got)
	}
	if rendered != "not_found: unknown Magento instance" {
		t.Fatalf("text = %q", rendered)
	}
}

// TestUntypedErrorIsSanitized proves a raw storage/pgx error cannot reach the
// agent as SQLSTATE text: it becomes a safe internal envelope.
func TestUntypedErrorIsSanitized(t *testing.T) {
	raw := errors.New(`storage: get job: ERROR: relation "jobs" does not exist (SQLSTATE 42P01)`)
	res := ErrorResult(raw)
	body := structured(t, res)
	if body["code"] != "internal" {
		t.Fatalf("code = %v, want internal", body["code"])
	}
	if body["message"] != "an unexpected error occurred" {
		t.Fatalf("message = %q, want the sanitized text", body["message"])
	}
	rendered := text(t, res)
	for _, leaked := range []string{"SQLSTATE", "storage:", "jobs"} {
		if strings.Contains(rendered, leaked) {
			t.Fatalf("text leaked %q: %q", leaked, rendered)
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SQLSTATE") {
		t.Fatalf("structured content leaked SQLSTATE: %s", encoded)
	}
}

func structured(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	switch body := res.StructuredContent.(type) {
	case map[string]any:
		return body
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode structured content: %v", err)
		}
		return decoded
	case []byte:
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode structured content: %v", err)
		}
		return decoded
	default:
		t.Fatalf("structured content = %T %v", res.StructuredContent, res.StructuredContent)
		return nil
	}
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no text content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content = %T", res.Content[0])
	}
	return tc.Text
}

// TestUntypedHandlerErrorIsSanitized proves the Add path (not just the
// exported builder) keeps raw storage text out of the agent-facing result.
func TestUntypedHandlerErrorIsSanitized(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.storage", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string"}}}`)},
			func(context.Context, *mcp.CallToolRequest, echoInput) (*mcp.CallToolResult, echoOutput, error) {
				return nil, echoOutput{}, errors.New(`storage: get job: ERROR: relation "jobs" does not exist (SQLSTATE 42P01)`)
			})
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "test.storage", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	body := structured(t, res)
	if body["code"] != "internal" {
		t.Fatalf("code = %v, want internal", body["code"])
	}
	if body["message"] != "an unexpected error occurred" {
		t.Fatalf("message = %q", body["message"])
	}
	encoded, _ := json.Marshal(body)
	for _, leaked := range []string{"SQLSTATE", "storage:", "jobs"} {
		if strings.Contains(string(encoded), leaked) || strings.Contains(text(t, res), leaked) {
			t.Fatalf("untyped storage error leaked %q: %s / %s", leaked, encoded, text(t, res))
		}
	}
}

// TestHandlerErrorTextHasCodeOnce proves the Add path renders the code exactly
// once, not "not_found: not_found: ...".
func TestHandlerErrorTextHasCodeOnce(t *testing.T) {
	session := newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{Name: "test.notfound", InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"value":{"type":"string"}}}`)},
			func(context.Context, *mcp.CallToolRequest, echoInput) (*mcp.CallToolResult, echoOutput, error) {
				return nil, echoOutput{}, errs.New(errs.CodeNotFound, "missing resource")
			})
	})
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "test.notfound", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	rendered := text(t, res)
	if strings.Count(rendered, "not_found") != 1 {
		t.Fatalf("text %q contains the code %d times, want once", rendered, strings.Count(rendered, "not_found"))
	}
}

// GG-01 root cause: a hand-written input schema drifted from the Go input struct,
// so a documented parameter (`shipping_amount`) was rejected by the schema while
// the struct had it. The registration check turns that class of drift into a
// startup failure. Removing validateSchemaMatchesInput from Add makes the
// registration below succeed and this test fail.
func TestAddRejectsSchemaThatDriftedFromTheInputStruct(t *testing.T) {
	type driftedInput struct {
		Instance string   `json:"instance"`
		Shipping *float64 `json:"shipping_amount"`
	}
	tool := &mcp.Tool{
		Name: "test.drifted",
		// The schema omits shipping_amount, exactly like the creditmemo bug.
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"instance":{"type":"string"}},"required":["instance"]}`),
	}
	err := validateSchemaMatchesInput[driftedInput](tool)
	if err == nil {
		t.Fatal("a schema missing a struct field must be rejected at registration")
	}
	if !strings.Contains(err.Error(), "shipping_amount") || !strings.Contains(err.Error(), "not in the schema") {
		t.Fatalf("err = %v, want it to name the drifting field", err)
	}
}

// The reverse drift is equally fatal: a schema property with no struct field is a
// parameter the caller can send and nothing reads.
func TestSchemaInputCheckRejectsUnknownSchemaProperty(t *testing.T) {
	type smallInput struct {
		Instance string `json:"instance"`
	}
	tool := &mcp.Tool{
		Name:        "test.extra",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"instance":{"type":"string"},"ghost":{"type":"string"}}}`),
	}
	err := validateSchemaMatchesInput[smallInput](tool)
	if err == nil {
		t.Fatal("a schema property with no struct field must be rejected")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want it to name the ghost property", err)
	}
}

// A matching schema passes, and the middleware-consumed confirmation_id is not
// mistaken for drift.
func TestSchemaInputCheckAcceptsMatchingSchema(t *testing.T) {
	type goodInput struct {
		Instance string `json:"instance"`
	}
	tool := &mcp.Tool{
		Name:        "test.good",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"instance":{"type":"string"},"confirmation_id":{"type":"string"}},"required":["instance"]}`),
	}
	if err := validateSchemaMatchesInput[goodInput](tool); err != nil {
		t.Fatalf("a matching schema must pass: %v", err)
	}
}

// The registration path itself must refuse the drift, not just the helper: Add is
// where a hand-written schema meets its Go input struct at startup. Removing the
// validateSchemaMatchesInput call from Add makes this test fail (registration
// succeeds and nothing is recovered).
func TestAddRefusesDriftedSchemaAtRegistration(t *testing.T) {
	type driftedInput struct {
		Instance string   `json:"instance"`
		Shipping *float64 `json:"shipping_amount"`
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Add accepted a schema that drifted from its input struct")
		}
	}()
	newSession(t, func(srv *mcp.Server) {
		Add(srv, &mcp.Tool{
			Name:        "test.drifted",
			InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"instance":{"type":"string"}},"required":["instance"]}`),
		}, func(context.Context, *mcp.CallToolRequest, driftedInput) (*mcp.CallToolResult, echoOutput, error) {
			return nil, echoOutput{}, nil
		})
	})
}
