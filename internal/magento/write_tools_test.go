// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
		if name == productDeleteToolName || name == mediaDeleteToolName || name == inventoryTransferToolName || name == orderCancelToolName || name == attributeOptionDeleteToolName || name == categoryDeleteToolName || name == customerAddressDeleteToolName || name == stockSourceUnlinkToolName {
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
		if tool.Annotations.IdempotentHint {
			t.Fatalf("write tool %q must not set idempotentHint", name)
		}
		destructive := tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint
		if name == productDeleteToolName || name == mediaDeleteToolName || name == inventoryTransferToolName || name == orderCancelToolName || name == attributeOptionDeleteToolName || name == categoryDeleteToolName || name == customerAddressDeleteToolName || name == stockSourceUnlinkToolName {
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
func callWriteTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult { //nolint:revive // test helper keeps t first by convention
	t.Helper()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
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
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg})
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
		if tool.Name == productDeleteToolName || tool.Name == mediaDeleteToolName || tool.Name == inventoryTransferToolName || tool.Name == orderCancelToolName || tool.Name == attributeOptionDeleteToolName || tool.Name == categoryDeleteToolName || tool.Name == customerAddressDeleteToolName || tool.Name == stockSourceUnlinkToolName {
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
