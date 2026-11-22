// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
)

var writeToolNames = []string{
	productCreateToolName, productUpdateToolName, configurableCreateToolName,
	configurableAddChildToolName, inventoryAssignToolName, importExecuteToolName,
	productDeleteToolName, removeChildToolName, unassignSourceItemsToolName, mediaUploadToolName,
	invoiceCreateToolName, shipmentCreateToolName, creditmemoCreateToolName,
	orderCommentToolName, orderHoldToolName, orderUnholdToolName,
	categoryCreateToolName, categoryUpdateToolName, categoryAssignToolName,
	productLinkAddToolName, productLinkRemoveToolName, invoiceCaptureToolName, orderEmailToolName,
	mediaDeleteToolName, orderCreateToolName, tierPricesSetToolName, inventoryTransferToolName,
	customerCreateToolName, customerUpdateToolName, mediaUpdateToolName, orderCancelToolName,
	attributeOptionCreateToolName, attributeOptionDeleteToolName, bulkProductUpdateToolName,
	categoryDeleteToolName, customerAddressCreateToolName, customerAddressDeleteToolName,
	customerAddressUpdateToolName, bulkLinkToolName, bulkOptionCreateToolName,
	configurableUpdateToolName, invoiceEmailToolName, shipmentEmailToolName, creditmemoEmailToolName,
	inventorySourceCreateToolName, stockSourceLinkToolName, stockSourceUnlinkToolName,
	createVariationsToolName, setConfigurableOptionsToolName, productOptionCreateToolName,
	productOptionUpdateToolName, productOptionDeleteToolName,
	operationDisposeToolName,
}

func TestRegisterWritesMetadata(t *testing.T) {
	service := testService(t, "https://shop.example.com")
	reg := registry.New()
	if err := RegisterWrites(reg, service); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	if reg.Len() != len(writeToolNames) {
		t.Fatalf("registry has %d tools, want %d", reg.Len(), len(writeToolNames))
	}
	registered := make(map[string]bool, len(writeToolNames))
	for _, tool := range reg.List() {
		registered[tool.Name] = true
	}
	for _, name := range writeToolNames {
		if !registered[name] {
			t.Fatalf("tool %q is not registered", name)
		}
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		wantPermission := registry.PermissionWrite
		if name == productDeleteToolName || name == mediaDeleteToolName || name == inventoryTransferToolName || name == orderCancelToolName || name == attributeOptionDeleteToolName || name == categoryDeleteToolName || name == customerAddressDeleteToolName || name == stockSourceUnlinkToolName || name == productOptionDeleteToolName {
			wantPermission = registry.PermissionDestructive
		}
		if tool.Permission != wantPermission {
			t.Fatalf("tool %q permission = %q, want %q", name, tool.Permission, wantPermission)
		}
		if !tool.RequiresConfirmation {
			t.Fatalf("write tool %q must require confirmation", name)
		}
		wantTimeout := 60 * time.Second
		if name == bulkProductUpdateToolName || name == bulkLinkToolName || name == bulkOptionCreateToolName || name == createVariationsToolName || name == setConfigurableOptionsToolName {
			wantTimeout = 120 * time.Second
		}
		if tool.Timeout != wantTimeout {
			t.Fatalf("tool %q timeout = %s, want %s", name, tool.Timeout, wantTimeout)
		}
		if tool.Annotations.ReadOnlyHint {
			t.Fatalf("write tool %q must not set readOnlyHint", name)
		}
		// idempotentHint is derived from writeDisclosures, not hand-set; the
		// exact per-tool expectation is pinned by
		// TestWriteToolIdempotencyMatchesDisclosures.
		if got, want := tool.Annotations.IdempotentHint, writeIdempotentHint(name); got != want {
			t.Fatalf("write tool %q idempotentHint = %v, want %v (derived from writeDisclosures)", name, got, want)
		}
		destructive := tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		if name == productDeleteToolName || name == mediaDeleteToolName || name == inventoryTransferToolName || name == orderCancelToolName || name == attributeOptionDeleteToolName || name == categoryDeleteToolName || name == customerAddressDeleteToolName || name == stockSourceUnlinkToolName || name == productOptionDeleteToolName {
			if !destructive {
				t.Fatalf("destructive tool %q must set destructiveHint=true", name)
			}
			if tool.Permission != registry.PermissionDestructive {
				t.Fatalf("destructive tool %q permission = %q", name, tool.Permission)
			}
		} else if destructive {
			t.Fatalf("write tool %q must set destructiveHint=false", name)
		}
		if tool.Capability == "" {
			t.Fatalf("tool %q has no capability", name)
		}
		if tool.Title == "" || len(tool.Title) > 60 {
			t.Fatalf("tool %q title is invalid: %q", name, tool.Title)
		}
		if tool.Description == "" || len(tool.Description) > 1024 {
			t.Fatalf("tool %q description is invalid", name)
		}
		if !strings.Contains(tool.Description, "Magento") || !strings.Contains(tool.Description, "write operation") ||
			!strings.Contains(tool.Description, "instance") {
			t.Fatalf("tool %q description must state the data source, the write, and instance resolution: %q",
				name, tool.Description)
		}
	}
	if err := RegisterWrites(nil, service); err == nil {
		t.Fatal("RegisterWrites(nil, service) must fail")
	}
	if err := RegisterWrites(registry.New(), nil); err == nil {
		t.Fatal("RegisterWrites(registry, nil) must fail")
	}
	if err := RegisterWrites(reg, service); err == nil {
		t.Fatal("duplicate registration must fail")
	}
}

// writeToolResultText concatenates the text content of a tool result.
func writeToolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var builder strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

// structuredOutput decodes a tool result's structured content into T.
func structuredOutput[T any](t *testing.T, res *mcp.CallToolResult) T {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("structured content cannot be encoded: %v", err)
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("structured content does not decode into %T: %v (%s)", out, err, raw)
	}
	return out
}

// callWriteTool calls one write tool and fails on a protocol error.
// callWriteTool drives a write tool the way a real client must: the first call
// returns confirmation_required, a different human approves it, and the second
// call carries the confirmation id. Tests that assert on the unapproved error
// should call session.CallTool directly.
func callWriteTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult { //nolint:revive // test helper keeps t first by convention
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !res.IsError {
		return res
	}
	id := confirmationIDFromResult(t, res)
	if id == "" {
		return res
	}
	if testConfirmService == nil {
		t.Fatalf("call %s requires approval but the server was built without a confirmation service", name)
	}
	if err := testConfirmService.Approve(ctx, id, "qa-approver"); err != nil {
		t.Fatalf("approve %s (%s): %v", name, id, err)
	}
	approved := make(map[string]any, len(args)+1)
	for k, v := range args {
		approved[k] = v
	}
	approved["confirmation_id"] = id
	second, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: approved})
	if err != nil {
		t.Fatalf("call %s with confirmation: %v", name, err)
	}
	return second
}

// confirmationIDFromResult returns the confirmation id a tool error advertises,
// or "" when the error is not a pending-approval response.
func confirmationIDFromResult(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("encode tool envelope: %v", err)
	}
	var envelope struct {
		Code    string            `json:"code"`
		Details map[string]string `json:"details"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	if envelope.Code != string(errs.CodeConfirmationRequired) {
		return ""
	}
	return envelope.Details["confirmation_id"]
}

func TestWriteToolsThroughInMemoryTransport(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/products":
			body, _ := io.ReadAll(r.Body)
			sku := productSKUFromBody(t, body)
			if sku == "PHONE-01" {
				_, _ = io.WriteString(w, `{"id":100,"sku":"PHONE-01","name":"Phone 01","type_id":"configurable",`+
					`"status":1,"visibility":4,"price":699,"attribute_set_id":4}`)
				return
			}
			name, price := "Test Shirt", 19.99
			if sku == "PHONE-01-BLK" {
				name, price = "Child", 699
			}
			_, _ = fmt.Fprintf(w, `{"id":1,"sku":%q,"name":%q,"type_id":"simple",`+
				`"status":1,"visibility":1,"price":%g,"attribute_set_id":4}`, sku, name, price)
		case r.Method == http.MethodPut && r.URL.Path == "/rest/default/V1/products/SKU-1":
			_, _ = io.WriteString(w, `{"id":1,"sku":"SKU-1","name":"Test Shirt","type_id":"simple",`+
				`"status":1,"visibility":4,"price":21.99,"attribute_set_id":4}`)
		case r.Method == http.MethodPost && r.URL.Path == "/rest/default/V1/inventory/source-items":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/child"):
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/children"):
			// The staged create verifies the child links by reading them back.
			_, _ = io.WriteString(w, `[{"id":1,"sku":"PHONE-01-BLK","type_id":"simple"}]`)
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/rest/default/V1/products/"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)

	reg := registry.New()
	if err := RegisterWrites(reg, testService(t, ts.URL)); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	found := make(map[string]bool, len(writeToolNames))
	for _, tool := range listed.Tools {
		found[tool.Name] = true
		if tool.Name == "platform.info" {
			continue
		}
		if tool.Annotations == nil || tool.Annotations.ReadOnlyHint {
			t.Fatalf("write tool %q must not be read-only", tool.Name)
		}
		destructive := tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		if tool.Name == productDeleteToolName || tool.Name == mediaDeleteToolName || tool.Name == inventoryTransferToolName || tool.Name == orderCancelToolName || tool.Name == attributeOptionDeleteToolName || tool.Name == categoryDeleteToolName || tool.Name == customerAddressDeleteToolName || tool.Name == stockSourceUnlinkToolName || tool.Name == productOptionDeleteToolName {
			if !destructive {
				t.Fatalf("destructive tool %q must set destructiveHint=true", tool.Name)
			}
		} else if destructive {
			t.Fatalf("write tool %q must set destructiveHint=false", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("tool %q has no input schema", tool.Name)
		}
	}
	for _, name := range writeToolNames {
		if !found[name] {
			t.Fatalf("tool %q is missing from tools/list", name)
		}
	}

	// product.create also assigns inventory because quantity is supplied.
	res := callWriteTool(t, ctx, session, productCreateToolName, map[string]any{
		"instance": "default", "sku": "SKU-1", "name": "Test Shirt", "attribute_set_id": 4,
		"type_id": "simple", "price": "19.99", "status": 1, "visibility": 4,
		"website_ids": []int{1}, "attributes": map[string]string{"color": "12"}, "quantity": 2,
	})
	if res.IsError {
		t.Fatalf("product.create returned a tool error: %s", writeToolResultText(t, res))
	}
	created := structuredOutput[productWriteOutput](t, res)
	if created.Product.SKU != "SKU-1" || created.Product.Name != "Test Shirt" {
		t.Fatalf("unexpected product.create output: %+v", created)
	}
	if calls.Load() != 2 {
		t.Fatalf("product.create made %d upstream calls, want 2", calls.Load())
	}

	// product.update sends only the supplied field.
	res = callWriteTool(t, ctx, session, productUpdateToolName, map[string]any{
		"instance": "default", "sku": "SKU-1", "price": "21.99",
	})
	if res.IsError {
		t.Fatalf("product.update returned a tool error: %s", writeToolResultText(t, res))
	}
	updated := structuredOutput[productWriteOutput](t, res)
	if updated.Product.Price != 21.99 {
		t.Fatalf("unexpected product.update output: %+v", updated)
	}

	// configurable.create creates the child, the parent, then the link.
	res = callWriteTool(t, ctx, session, configurableCreateToolName, map[string]any{
		"instance": "default",
		"parent": map[string]any{
			"sku": "PHONE-01", "name": "Phone 01", "attribute_set_id": 4,
			"price": "699", "website_ids": []int{1},
		},
		"variant_attributes": []any{map[string]any{
			"attribute_id": 93, "code": "color",
			"values": []any{map[string]any{"value_index": 12}},
		}},
		"children": []any{map[string]any{
			"sku": "PHONE-01-BLK", "attributes": map[string]string{"color": "12"},
		}},
	})
	if res.IsError {
		t.Fatalf("configurable.create returned a tool error: %s", writeToolResultText(t, res))
	}
	configurable := structuredOutput[configurableCreateOutput](t, res)
	if configurable.Parent.SKU != "PHONE-01" || len(configurable.Children) != 1 {
		t.Fatalf("unexpected configurable.create output: %+v", configurable)
	}

	res = callWriteTool(t, ctx, session, configurableAddChildToolName, map[string]any{
		"instance": "default", "parent_sku": "PHONE-01", "child_sku": "PHONE-01-BLK",
	})
	if res.IsError {
		t.Fatalf("configurable.add_child returned a tool error: %s", writeToolResultText(t, res))
	}
	linked := structuredOutput[configurableAddChildOutput](t, res)
	if !linked.Linked || linked.ChildSKU != "PHONE-01-BLK" {
		t.Fatalf("unexpected configurable.add_child output: %+v", linked)
	}

	res = callWriteTool(t, ctx, session, inventoryAssignToolName, map[string]any{
		"instance": "default",
		"items": []any{map[string]any{
			"sku": "SKU-1", "source_code": "default", "quantity": 5, "status": 1,
		}},
	})
	if res.IsError {
		t.Fatalf("inventory.assign returned a tool error: %s", writeToolResultText(t, res))
	}
	assigned := structuredOutput[inventoryAssignOutput](t, res)
	if assigned.Assigned != 1 {
		t.Fatalf("unexpected inventory.assign output: %+v", assigned)
	}

	// A schema-valid but service-invalid SKU must be a tool error and must not
	// reach the upstream.
	before := calls.Load()
	res = callWriteTool(t, ctx, session, productCreateToolName, map[string]any{
		"instance": "default", "sku": strings.Repeat("A", 65), "name": "Test Shirt",
		"attribute_set_id": 4, "type_id": "simple", "price": "19.99",
	})
	if !res.IsError {
		t.Fatal("an over-long SKU must be rejected as a tool error")
	}
	if text := writeToolResultText(t, res); !strings.Contains(text, "invalid_argument") {
		t.Fatalf("tool error does not carry invalid_argument: %q", text)
	}
	if calls.Load() != before {
		t.Fatal("invalid SKU must not reach the upstream")
	}

	// Schema validation rejects a missing required field.
	res = callWriteTool(t, ctx, session, productCreateToolName, map[string]any{
		"instance": "default", "sku": "SKU-2", "name": "Test Shirt", "attribute_set_id": 4, "type_id": "simple",
	})
	if !res.IsError {
		t.Fatal("a missing required price must be a tool error")
	}

	// Schema validation rejects a malformed price before the handler runs.
	before = calls.Load()
	res = callWriteTool(t, ctx, session, productCreateToolName, map[string]any{
		"instance": "default", "sku": "SKU-2", "name": "Test Shirt",
		"attribute_set_id": 4, "type_id": "simple", "price": "free",
	})
	if !res.IsError {
		t.Fatal("a malformed price must be a tool error")
	}
	if calls.Load() != before {
		t.Fatal("schema-invalid price must not reach the upstream")
	}
}

func TestWriteSchemasAcceptConfirmationID(t *testing.T) {
	// The confirmation middleware consumes confirmation_id from the raw
	// arguments, but the JSON Schema must also allow it or the approval retry
	// fails validation before the handler runs.
	schemas := map[string]string{
		"product.create":                 productCreateSchema,
		"product.update":                 productUpdateSchema,
		"configurable.create":            configurableCreateSchema,
		"configurable.child":             configurableAddChildSchema,
		"inventory.assign":               inventoryAssignSchema,
		"import.execute":                 importExecuteSchema,
		"product.delete":                 productDeleteSchema,
		"configurable.remove_child":      removeChildSchema,
		"inventory.unassign":             unassignSourceItemsSchema,
		"product.media.upload":           mediaUploadSchema,
		"invoice.create":                 invoiceCreateSchema,
		"shipment.create":                shipmentCreateSchema,
		"creditmemo.create":              creditmemoCreateSchema,
		"order.comment":                  orderCommentSchema,
		"order.hold":                     orderHoldSchema,
		"order.unhold":                   orderHoldSchema,
		"category.create":                categoryCreateSchema,
		"category.update":                categoryUpdateSchema,
		"category.assign_product":        categoryAssignSchema,
		"product.links.add":              productLinkAddSchema,
		"product.links.remove":           productLinkRemoveSchema,
		"invoice.capture":                invoiceCaptureSchema,
		"order.email":                    orderEmailSchema,
		"product.media.delete":           mediaDeleteSchema,
		"order.create":                   orderCreateSchema,
		"product.tier_prices.set":        tierPricesSetSchema,
		"inventory.transfer":             inventoryTransferSchema,
		"customer.create":                customerCreateSchema,
		"customer.update":                customerUpdateSchema,
		"product.media.update":           mediaUpdateSchema,
		"order.cancel":                   orderCancelSchema,
		"attributes.options.create":      attributeOptionCreateSchema,
		"attributes.options.delete":      attributeOptionDeleteSchema,
		"products.bulk_update":           bulkProductUpdateSchema,
		"category.delete":                categoryDeleteSchema,
		"customer.addresses.create":      customerAddressCreateSchema,
		"customer.addresses.delete":      customerAddressDeleteSchema,
		"customer.addresses.update":      customerAddressUpdateSchema,
		"product.links.bulk":             bulkLinkSchema,
		"attributes.options.bulk_create": bulkOptionCreateSchema,
		"configurable.update":            productUpdateSchema,
		"invoice.email":                  documentEmailSchema,
		"shipment.email":                 documentEmailSchema,
		"creditmemo.email":               documentEmailSchema,
		"inventory.source_create":        inventorySourceCreateSchema,
		"inventory.stock_source_link":    stockSourceLinkSchema,
		"inventory.stock_source_unlink":  stockSourceUnlinkSchema,
		"configurable.create_variations": createVariationsSchema,
		"configurable.set_options":       setConfigurableOptionsSchema,
		"product.options.create":         productOptionCreateSchema,
		"product.options.update":         productOptionUpdateSchema,
		"product.options.delete":         productOptionDeleteSchema,
	}
	for name, schema := range schemas {
		var doc struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal([]byte(schema), &doc); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", name, err)
		}
		if _, ok := doc.Properties["confirmation_id"]; !ok {
			t.Fatalf("%s schema does not accept confirmation_id", name)
		}
	}
}

// configurableResumeFake is a stateful catalog for the staged configurable
// create: products, child links, and the source-item read the stock stage uses.
type configurableResumeFake struct {
	mu           sync.Mutex
	products     map[string]string // sku -> type_id
	variants     map[string]string // child sku -> color value
	linked       map[string]bool
	linkFail     map[string]bool
	requests     []string
	productPOSTs int
	linkPOSTs    int
}

func newConfigurableResumeFake() *configurableResumeFake {
	return &configurableResumeFake{
		products: map[string]string{}, variants: map[string]string{},
		linked: map[string]bool{}, linkFail: map[string]bool{},
	}
}

func (f *configurableResumeFake) productJSON(sku string) string {
	custom := ""
	if value, ok := f.variants[sku]; ok {
		custom = fmt.Sprintf(`,"custom_attributes":[{"attribute_code":"color","value":%q}]`, value)
	}
	return fmt.Sprintf(`{"id":100,"sku":%q,"type_id":%q,"name":%q,"price":699,"attribute_set_id":4%s}`,
		sku, f.products[sku], "Name "+sku, custom)
}

func (f *configurableResumeFake) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/rest/default/V1/products/attribute-sets/4/attributes":
			_, _ = io.WriteString(w, `[{"attribute_id":93,"attribute_code":"color","frontend_input":"select"}]`)
		case r.Method == http.MethodGet && path == "/rest/default/V1/inventory/source-items":
			f.mu.Lock()
			rows := make([]string, 0, len(f.products))
			for sku, typeID := range f.products {
				if typeID == "simple" {
					rows = append(rows, fmt.Sprintf(`{"sku":%q,"source_code":"default","quantity":5,"status":1}`, sku))
				}
			}
			f.mu.Unlock()
			_, _ = io.WriteString(w, `{"items":[`+strings.Join(rows, ",")+`],"total_count":`+fmt.Sprint(len(rows))+`}`)
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/children"):
			f.mu.Lock()
			items := make([]string, 0, len(f.linked))
			for sku := range f.linked {
				items = append(items, fmt.Sprintf(`{"sku":%q,"type_id":"simple"}`, sku))
			}
			f.mu.Unlock()
			_, _ = io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/rest/default/V1/products/"):
			sku := strings.TrimPrefix(path, "/rest/default/V1/products/")
			f.mu.Lock()
			_, ok := f.products[sku]
			body := ""
			if ok {
				body = f.productJSON(sku)
			}
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, body)
		case r.Method == http.MethodPost && path == "/rest/default/V1/products":
			raw, _ := io.ReadAll(r.Body)
			var body struct {
				Product struct {
					SKU              string `json:"sku"`
					TypeID           string `json:"type_id"`
					CustomAttributes []struct {
						Code  string `json:"attribute_code"`
						Value string `json:"value"`
					} `json:"custom_attributes"`
				} `json:"product"`
			}
			_ = json.Unmarshal(raw, &body)
			f.mu.Lock()
			f.products[body.Product.SKU] = body.Product.TypeID
			for _, attribute := range body.Product.CustomAttributes {
				if attribute.Code == "color" {
					f.variants[body.Product.SKU] = attribute.Value
				}
			}
			f.productPOSTs++
			rendered := f.productJSON(body.Product.SKU)
			f.mu.Unlock()
			_, _ = io.WriteString(w, rendered)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/child"):
			raw, _ := io.ReadAll(r.Body)
			child := linkChildSKU(t, raw)
			f.mu.Lock()
			f.linkPOSTs++
			fail := f.linkFail[child]
			if !fail {
				f.linked[child] = true
			}
			f.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"message":"The child could not be linked."}`)
				return
			}
			_, _ = io.WriteString(w, `true`)
		case r.Method == http.MethodPut && strings.Contains(path, "/stockItems/"):
			_, _ = io.WriteString(w, `true`)
		default:
			http.NotFound(w, r)
		}
	}
}

func (f *configurableResumeFake) counts() (productPOSTs, linkPOSTs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.productPOSTs, f.linkPOSTs
}

// TestConfigurableCreateResumesThroughMCP is the t29 regression: a partial create
// exposes its execution record at the MCP boundary, and the returned
// operation_id resumes it without re-POSTing an existing SKU.
func TestConfigurableCreateResumesThroughMCP(t *testing.T) {
	fake := newConfigurableResumeFake()
	fake.linkFail["PHONE-01-WHT"] = true
	ts := httptest.NewServer(fake.handler(t))
	defer ts.Close()
	session := startToolSession(t, testService(t, ts.URL))
	ctx := context.Background()

	args := map[string]any{
		"instance": "default",
		"parent": map[string]any{
			"sku": "PHONE-01", "name": "Phone 01", "attribute_set_id": 4, "price": "699",
		},
		"variant_attributes": []any{map[string]any{
			"attribute_id": 93, "code": "color",
			"values": []any{map[string]any{"value_index": 12}, map[string]any{"value_index": 13}},
		}},
		"children": []any{
			map[string]any{"sku": "PHONE-01-BLK", "attributes": map[string]string{"color": "12"}},
			map[string]any{"sku": "PHONE-01-WHT", "attributes": map[string]string{"color": "13"}},
		},
	}

	// First attempt: the second link fails, so the create is partial.
	first := callWriteTool(t, ctx, session, configurableCreateToolName, args)
	if !first.IsError {
		t.Fatalf("partial create must be a tool error: %s", writeToolResultText(t, first))
	}
	var envelope struct {
		Code   string                   `json:"code"`
		Result configurableCreateOutput `json:"result"`
	}
	rawEnvelope, err := json.Marshal(first.StructuredContent)
	if err != nil {
		t.Fatalf("partial envelope is not JSON: %v", err)
	}
	if err := json.Unmarshal(rawEnvelope, &envelope); err != nil {
		t.Fatalf("partial envelope is not JSON: %v (%s)", err, rawEnvelope)
	}
	if envelope.Code != string(errs.CodeConflict) {
		t.Fatalf("code = %q, want conflict", envelope.Code)
	}
	partial := envelope.Result
	if partial.Outcome != ConfigurablePartial || partial.FailedStage != stageLinks {
		t.Fatalf("partial record = %+v, want outcome=partial failed_stage=links", partial)
	}
	if got := strings.Join(partial.OrphanSKUs, ","); got != "PHONE-01-WHT" {
		t.Fatalf("orphan_skus = %q, want the unlinked child", got)
	}
	if partial.OperationID == "" {
		t.Fatal("operation_id is empty, so the caller cannot resume")
	}
	if !contains(partial.CompletedStages, stageChildren) || !contains(partial.CompletedStages, stageParent) {
		t.Fatalf("completed_stages = %v, want children and parent", partial.CompletedStages)
	}
	if len(partial.Stages) != len(configurableStages) {
		t.Fatalf("stages = %v, want %d entries", partial.Stages, len(configurableStages))
	}
	productPOSTs, linkPOSTs := fake.counts()
	if productPOSTs != 3 {
		t.Fatalf("product POSTs = %d, want 3", productPOSTs)
	}
	if linkPOSTs != 2 {
		t.Fatalf("link POSTs = %d, want 2 (one succeeded, one failed)", linkPOSTs)
	}

	// Resume with the operation id after the link is repaired upstream.
	fake.mu.Lock()
	delete(fake.linkFail, "PHONE-01-WHT")
	fake.mu.Unlock()
	args["operation_id"] = partial.OperationID

	resumed := callWriteTool(t, ctx, session, configurableCreateToolName, args)
	if resumed.IsError {
		t.Fatalf("resume failed: %s", writeToolResultText(t, resumed))
	}
	out := structuredOutput[configurableCreateOutput](t, resumed)
	if out.Outcome != ConfigurableCompleted || out.FailedStage != "" || len(out.OrphanSKUs) != 0 {
		t.Fatalf("resume record = %+v, want a completed create", out)
	}
	if out.OperationID != partial.OperationID {
		t.Fatalf("operation_id = %q, want the resumed id %q", out.OperationID, partial.OperationID)
	}
	for _, name := range []string{stagePrecheck, stageChildren, stageParent, stageLinks, stageStock} {
		if !contains(out.CompletedStages, name) {
			t.Fatalf("completed_stages = %v, want %q", out.CompletedStages, name)
		}
	}
	if len(out.Stages) != len(configurableStages) {
		t.Fatalf("stages = %v, want %d entries", out.Stages, len(configurableStages))
	}
	for i, name := range configurableStages {
		if out.Stages[i].Name != name || out.Stages[i].Detail == "" {
			t.Fatalf("stage %d = %+v, want %q with a detail", i, out.Stages[i], name)
		}
	}
	// The resume reused the children and the parent: no new product POST, and
	// exactly one new link call for the orphan.
	gotProducts, gotLinks := fake.counts()
	if gotProducts != productPOSTs {
		t.Fatalf("product POSTs after the resume = %d, want %d (no SKU re-created)", gotProducts, productPOSTs)
	}
	if gotLinks != linkPOSTs+1 {
		t.Fatalf("link POSTs after the resume = %d, want %d (only the orphan is relinked)", gotLinks, linkPOSTs+1)
	}
	if !fake.linked["PHONE-01-WHT"] {
		t.Fatal("the orphan was not linked on resume")
	}
}

// callToolApproved is callTool plus the approval dance: when the call comes back
// confirmation_required it approves as a different human and retries with the
// confirmation id. Read tools and already-approved calls pass straight through.
func callToolApproved(t *testing.T, ctx context.Context, session *mcp.ClientSession, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	t.Helper()
	res, err := session.CallTool(ctx, params)
	if err != nil || res == nil || !res.IsError {
		return res, err
	}
	id := confirmationIDFromResult(t, res)
	if id == "" || testConfirmService == nil {
		return res, nil
	}
	if aerr := testConfirmService.Approve(ctx, id, "qa-approver"); aerr != nil {
		t.Fatalf("approve %s (%s): %v", params.Name, id, aerr)
	}
	original, ok := params.Arguments.(map[string]any)
	if !ok {
		return res, nil
	}
	approved := make(map[string]any, len(original)+1)
	for k, v := range original {
		approved[k] = v
	}
	approved["confirmation_id"] = id
	return session.CallTool(ctx, &mcp.CallToolParams{Name: params.Name, Arguments: approved})
}

// TestWriteToolIdempotencyMatchesDisclosures is the SEM-DOC-10 regression: the
// protocol annotation and the prose disclosure must tell an agent the same thing.
//
// It enumerates every registered write tool instead of asserting per tool, so a
// write tool added later is covered the moment it is registered: the tool must
// have a writeDisclosures entry, and its idempotentHint must equal what that
// entry declares. Hard-coding IdempotentHint anywhere makes this fail.
func TestWriteToolIdempotencyMatchesDisclosures(t *testing.T) {
	reg := registry.New()
	if err := RegisterWrites(reg, testService(t, "https://shop.example.com")); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	// The import write tools live behind the queue, so the write surface is
	// RegisterWrites plus RegisterImportJobs.
	resolver, err := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	if err != nil {
		t.Fatalf("NewResolver: %v", err)
	}
	if err := RegisterImportJobs(reg, resolver, &fakeImportQueue{}); err != nil {
		t.Fatalf("RegisterImportJobs: %v", err)
	}

	// Every registered write tool must be described by the single table, and its
	// hint must be what the table declares.
	registered := make(map[string]bool, reg.Len())
	for _, tool := range reg.List() {
		if tool.Permission == registry.PermissionRead {
			continue
		}
		registered[tool.Name] = true
		if _, ok := writeDisclosures[tool.Name]; !ok {
			t.Fatalf("write tool %q has no writeDisclosures entry; the table is the single source of truth", tool.Name)
		}
		if got, want := tool.Annotations.IdempotentHint, writeIdempotentHint(tool.Name); got != want {
			t.Fatalf("registry %q idempotentHint = %v, want %v", tool.Name, got, want)
		}
	}
	if len(registered) == 0 {
		t.Fatal("no write tools registered")
	}

	// The table must not describe anything that is not a registered write tool.
	for name := range writeDisclosures {
		if !registered[name] {
			t.Fatalf("writeDisclosures describes %q, which is not a registered write tool", name)
		}
	}

	// Every idempotency statement must fall into a classification this package
	// understands; a novel phrasing fails here instead of silently becoming
	// "not idempotent".
	for name, disclosure := range writeDisclosures {
		statement := strings.ToLower(strings.TrimSpace(disclosure.idempotency))
		switch {
		case strings.HasPrefix(statement, "idempotent"),
			strings.HasPrefix(statement, "not idempotent"),
			strings.HasPrefix(statement, "not fully idempotent"),
			strings.HasPrefix(statement, "resumable"):
		default:
			t.Fatalf("%s idempotency statement is unclassified: %q", name, disclosure.idempotency)
		}
	}

	// The declared expectation for the tools this task names explicitly.
	idempotent := []string{
		"magento.category.update",
		"magento.category.assign_product",
		"magento.customer.update",
		"magento.customer.addresses.update",
		"magento.product.media.update",
		"magento.product.options.update",
		"magento.product.tier_prices.set",
		"magento.product.update",
		"magento.configurable.update",
	}
	for _, name := range idempotent {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%q is not registered", name)
		}
		if !tool.Annotations.IdempotentHint {
			t.Fatalf("%q repeats the same fields without changing the record, so idempotentHint must be true", name)
		}
		if !strings.HasPrefix(strings.ToLower(writeDisclosures[name].idempotency), "idempotent") {
			t.Fatalf("%q disclosure does not declare idempotency: %q", name, writeDisclosures[name].idempotency)
		}
	}
	notIdempotent := []string{
		"magento.product.create",
		"magento.order.create",
		"magento.order.cancel",
		"magento.invoice.capture",
		"magento.creditmemo.create",
		"magento.import.start",
		"magento.product.delete",
		"magento.inventory.transfer",
	}
	for _, name := range notIdempotent {
		tool, ok := reg.Lookup(name)
		if !ok {
			t.Fatalf("%q is not registered", name)
		}
		if tool.Annotations.IdempotentHint {
			t.Fatalf("%q must not claim idempotency (a retry can duplicate or needs an idempotency_key)", name)
		}
	}

	// The hint a client actually reads comes from the mcp.Tool registered on the
	// server, not from the registry copy: check that channel too.
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != reg.Len() {
		t.Fatalf("client sees %d tools, registry has %d", len(listed.Tools), reg.Len())
	}
	checked := 0
	for _, tool := range listed.Tools {
		if !registered[tool.Name] {
			continue // read tools carry no disclosure
		}
		if tool.Annotations == nil {
			t.Fatalf("client-visible tool %q has no annotations", tool.Name)
		}
		if got, want := tool.Annotations.IdempotentHint, writeIdempotentHint(tool.Name); got != want {
			t.Fatalf("client-visible %q idempotentHint = %v, want %v", tool.Name, got, want)
		}
		checked++
	}
	if checked != len(registered) {
		t.Fatalf("checked %d client-visible write tools, want %d", checked, len(registered))
	}
}

// TestWriteToolSourceHasNoHardcodedIdempotentHint keeps the derivation honest at
// the source level: a new tool that pastes "IdempotentHint: false" into its
// literal would pass the registry test only until someone noticed the two
// channels disagreeing again.
func TestWriteToolSourceHasNoHardcodedIdempotentHint(t *testing.T) {
	source, err := os.ReadFile("write_tools.go")
	if err != nil {
		t.Fatalf("read write_tools.go: %v", err)
	}
	if strings.Contains(string(source), "IdempotentHint: false") {
		t.Fatal("write_tools.go hard-codes IdempotentHint; derive it with writeIdempotentHint(name)")
	}
	if strings.Contains(string(source), "IdempotentHint: true") {
		t.Fatal("write_tools.go hard-codes IdempotentHint; derive it with writeIdempotentHint(name)")
	}
}

// newWriteToolSession builds an MCP session over the write registry for tests
// that must exercise a tool through validation, confirmation and the handler
// rather than calling the service directly.
func newWriteToolSession(t *testing.T, baseURL string) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	if err := RegisterWrites(reg, testService(t, baseURL)); err != nil {
		t.Fatalf("RegisterWrites: %v", err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatalf("NewMCP: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// shippingRefundOrder is an order whose two units were invoiced and not refunded,
// with 10 of shipping still refundable.
const shippingRefundOrder = `{"entity_id":42,"order_currency_code":"USD","grand_total":60,` +
	`"shipping_amount":10,"shipping_refunded":0,` +
	`"items":[{"item_id":1,"sku":"SKU-1","price":25,"qty_invoiced":2,"qty_refunded":0}]}`

// GG-01: the credit memo tool must accept shipping_amount. The Go input struct and
// the service both knew the field, but creditmemoCreateSchema is hand-written and
// declares additionalProperties:false, so every caller that used the U-11 shipping
// control was rejected before the handler ran. Removing the schema property, or
// dropping ShippingAmount from the tool input struct, makes this test fail.
func TestCreditmemoCreateAcceptsShippingAmount(t *testing.T) {
	var doc struct {
		Properties map[string]struct {
			Type        string   `json:"type"`
			Minimum     *float64 `json:"minimum"`
			Description string   `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(creditmemoCreateSchema), &doc); err != nil {
		t.Fatalf("creditmemo.create schema is not valid JSON: %v", err)
	}
	prop, ok := doc.Properties["shipping_amount"]
	if !ok {
		t.Fatal("creditmemo.create schema does not accept shipping_amount; MCP callers cannot reach the shipping control")
	}
	if prop.Type != "number" || prop.Minimum == nil || *prop.Minimum != 0 {
		t.Fatalf("shipping_amount schema = %+v, want type number with minimum 0", prop)
	}
	if !strings.Contains(prop.Description, "remaining shipping") {
		t.Fatalf("shipping_amount description does not explain the default: %q", prop.Description)
	}

	// Schema acceptance alone is not enough: a missing struct field would drop the
	// value silently, which is the worse failure (the refund looks successful but
	// no shipping comes back). Drive the tool end to end and read the upstream body.
	explicit := &creditmemoFake{orderJSON: shippingRefundOrder, invoices: singleInvoice}
	ts := httptest.NewServer(explicit.handler(t))
	defer ts.Close()
	useLedger(t, newFakeLedger())
	res := callWriteTool(t, context.Background(), newWriteToolSession(t, ts.URL), creditmemoCreateToolName, map[string]any{
		"instance": "default", "order_id": 42,
		"items":           []any{map[string]any{"order_item_id": 1, "qty": 1}},
		"shipping_amount": 4,
	})
	if res.IsError {
		t.Fatalf("creditmemo.create rejected shipping_amount: %+v", res.Content)
	}
	var body refundOrderBody
	if err := json.Unmarshal(explicit.postBody, &body); err != nil {
		t.Fatalf("refund body: %v", err)
	}
	if body.Arguments == nil || body.Arguments.ShippingAmount == nil || *body.Arguments.ShippingAmount != 4 {
		t.Fatalf("upstream arguments = %+v, want an explicit shipping_amount of 4", body.Arguments)
	}

	// Omitting the field must stay distinguishable from 0: a partial refund sends
	// an explicit 0 so Magento does not sweep the whole remaining shipping in.
	omitted := &creditmemoFake{orderJSON: shippingRefundOrder, invoices: singleInvoice}
	tsOmitted := httptest.NewServer(omitted.handler(t))
	defer tsOmitted.Close()
	res = callWriteTool(t, context.Background(), newWriteToolSession(t, tsOmitted.URL), creditmemoCreateToolName, map[string]any{
		"instance": "default", "order_id": 42,
		"items": []any{map[string]any{"order_item_id": 1, "qty": 1}},
	})
	if res.IsError {
		t.Fatalf("creditmemo.create without shipping_amount failed: %+v", res.Content)
	}
	if err := json.Unmarshal(omitted.postBody, &body); err != nil {
		t.Fatalf("refund body: %v", err)
	}
	if body.Arguments == nil || body.Arguments.ShippingAmount == nil || *body.Arguments.ShippingAmount != 0 {
		t.Fatalf("upstream arguments = %+v, want shipping_amount 0 when the caller omitted it", body.Arguments)
	}
}

// GG-03: an agent cannot use a default it cannot see. The credit memo description
// must say what omitting shipping_amount does and what 0 means.
func TestCreditmemoCreateDescriptionStatesShippingDefault(t *testing.T) {
	for _, want := range []string{"shipping_amount", "partial refund returns no shipping", "remaining shipping", "0 states explicitly"} {
		if !strings.Contains(creditmemoCreateDescription, want) {
			t.Fatalf("creditmemo.create description does not mention %q: %s", want, creditmemoCreateDescription)
		}
	}
}

// GG-04: with an offline payment method Magento marks the invoice paid whether or
// not capture was requested, so the invoice state cannot prove a capture. A
// description that says it does sends an ERP to the wrong field (U-14).
func TestInvoiceCreateDescriptionDoesNotClaimStateProvesCapture(t *testing.T) {
	lower := strings.ToLower(invoiceCreateDescription)
	if strings.Contains(lower, "state proves") {
		t.Fatalf("invoice.create still claims the invoice state proves the capture: %s", invoiceCreateDescription)
	}
	for _, want := range []string{"captured", "checkmo", "never proves a capture"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("invoice.create description does not mention %q: %s", want, invoiceCreateDescription)
		}
	}
}
