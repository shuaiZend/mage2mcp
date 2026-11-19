// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	capabilityProductRead = "magento.product.read"
	toolTimeout           = 30 * time.Second
	getToolName           = "magento.product.get"
	getToolTitle          = "Get Magento product"
	searchToolName        = "magento.product.search"
	searchToolTitle       = "Search Magento products"
	instancePattern       = `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`
	// skuInputPattern omits the 64-character cap; the service rejects over-long SKUs.
	skuInputPattern    = `^[A-Za-z0-9][A-Za-z0-9._:-]*$`
	cursorPattern      = `^(0|[1-9][0-9]{0,8})$`
	getToolDescription = "Reads one product from the configured Magento 2 REST catalog " +
		"(GET /rest/{store_code}/V1/products/{sku}) using the integration credential. " +
		"Read-only: it never creates, updates, or deletes data. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	searchToolDescription = "Searches the configured Magento 2 REST catalog " +
		"(GET /rest/{store_code}/V1/products with searchCriteria filter groups) using the integration credential. " +
		"Read-only: it never creates, updates, or deletes data. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	// Tool input JSON schemas are authoritative for validation.
	productGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; resolved by the server when only one instance is configured."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU, 1-64 characters from [A-Za-z0-9._:-]."}
	}, "required": ["instance", "sku"], "additionalProperties": false}`
	productSearchSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; resolved by the server when only one instance is configured."},
		"query": {"type": "string", "maxLength": 128, "description": "Case-insensitive substring filter on the product name."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Exact SKU filter; 1-64 characters from [A-Za-z0-9._:-]."},
		"type_id": {"type": "string", "enum": ["simple", "configurable", "virtual", "downloadable", "bundle", "grouped"], "description": "Magento product type filter."},
		"status": {"type": "integer", "enum": [1, 2], "description": "Magento status filter: 1 enabled, 2 disabled."},
		"price_from": {"type": "string", "pattern": "^\\d{1,12}(\\.\\d{1,4})?$", "description": "Inclusive lower price bound as a decimal string."},
		"price_to": {"type": "string", "pattern": "^\\d{1,12}(\\.\\d{1,4})?$", "description": "Inclusive upper price bound as a decimal string."},
		"created_from": {"type": "string", "description": "Inclusive creation lower bound, RFC 3339 or YYYY-MM-DD."},
		"created_to": {"type": "string", "description": "Exclusive creation upper bound, RFC 3339 or YYYY-MM-DD."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 50, "description": "Page size, 1-200; defaults to 50."},
		"cursor": {"type": "string", "pattern": "` + cursorPattern + `", "description": "Zero-based row offset; must be a multiple of limit. Omit for the first page."}
	}, "required": ["instance"], "additionalProperties": false}`
)

type productGetInput struct {
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
}

type productGetOutput struct {
	Product Product `json:"product"`
}

type productSearchInput struct {
	Instance    string `json:"instance"`
	Query       string `json:"query"`
	SKU         string `json:"sku"`
	TypeID      string `json:"type_id"`
	Status      int    `json:"status"`
	PriceFrom   string `json:"price_from"`
	PriceTo     string `json:"price_to"`
	CreatedFrom string `json:"created_from"`
	CreatedTo   string `json:"created_to"`
	Limit       int    `json:"limit"`
	Cursor      string `json:"cursor"`
}

type productSearchOutput struct {
	Products []Product `json:"products"`
	Total    int       `json:"total"`
}

// Register adds the read-only Magento product tools to reg.
func Register(reg *registry.Registry, svc *Service) error {
	if svc == nil {
		return errs.New(errs.CodeInternal, "Magento service is required")
	}
	return registerReadTools(reg, singleLookup(svc))
}

// registerReadTools builds the read tools against an instance lookup (ADR-0018).
func registerReadTools(reg *registry.Registry, lookup serviceLookup) error {
	if reg == nil {
		return errs.New(errs.CodeInternal, "tool registry is required")
	}
	for _, tool := range []registry.Tool{
		newGetTool(lookup), newSearchTool(lookup), newImportValidateTool(lookup),
		newOrderGetTool(lookup), newOrderSearchTool(lookup), newSalesSummaryTool(lookup),
		newCategoryGetTool(lookup), newCategoryTreeTool(lookup),
		newAttributeSetsTool(lookup), newAttributesTool(lookup),
		newProductMediaTool(lookup), newStockStatusTool(lookup), newSourceItemsTool(lookup),
		newOrderHistoryTool(lookup), newInvoiceListTool(lookup), newInvoiceGetTool(lookup),
		newShipmentListTool(lookup), newShipmentGetTool(lookup),
		newCreditmemoListTool(lookup), newCreditmemoGetTool(lookup),
		newConfigurableChildrenTool(lookup), newAttributeOptionsTool(lookup), newCategoryProductsTool(lookup),
		newCustomerGetTool(lookup), newCustomerSearchTool(lookup), newProductLinksTool(lookup),
		newProductsBatchTool(lookup),
	} {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}
func toolMetadata(name, title, description string) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: description,
		Capability: capabilityProductRead, Permission: registry.PermissionRead, Timeout: toolTimeout,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}
}
func mcpTool(name, title, description, schema string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(schema),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}
}
func newGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(getToolName, getToolTitle, getToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(getToolName, getToolTitle, getToolDescription, productGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productGetInput) (*mcp.CallToolResult, productGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productGetOutput{}, err
				}
				product, err := svc.Get(ctx, in.SKU)
				if err != nil {
					return nil, productGetOutput{}, err
				}
				return nil, productGetOutput{Product: *product}, nil
			})
		return nil
	}
	return t
}
func newSearchTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(searchToolName, searchToolTitle, searchToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(searchToolName, searchToolTitle, searchToolDescription, productSearchSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productSearchInput) (*mcp.CallToolResult, productSearchOutput, error) {
				offset := 0
				if in.Cursor != "" {
					parsed, err := strconv.Atoi(in.Cursor)
					if err != nil || parsed < 0 {
						return nil, productSearchOutput{}, invalidf("cursor must be a non-negative decimal row offset")
					}
					offset = parsed
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productSearchOutput{}, err
				}
				products, total, err := svc.Search(ctx, ProductQuery{
					SKU: in.SKU, NameContains: in.Query, TypeID: in.TypeID, Status: in.Status,
					PriceFrom: in.PriceFrom, PriceTo: in.PriceTo,
					CreatedFrom: in.CreatedFrom, CreatedTo: in.CreatedTo,
					Limit: in.Limit, CursorOffset: offset,
				})
				if err != nil {
					return nil, productSearchOutput{}, err
				}
				if products == nil {
					products = []Product{}
				}
				return nil, productSearchOutput{Products: products, Total: total}, nil
			})
		return nil
	}
	return t
}

const (
	importValidateToolName        = "magento.import.validate"
	importValidateToolTitle       = "Validate a product import"
	importValidateToolDescription = "Parses and validates a CSV or JSON product import (base64-encoded, at most 8 MiB and 5000 rows) and returns a preview with per-row errors. Read-only: nothing is written to Magento."
	importValidateCapability      = "magento.import.read"
)

type importValidateInput struct {
	Instance string `json:"instance"`
	Format   string `json:"format"`
	Data     string `json:"data_base64"`
}

type importValidateOutput struct {
	Preview *ImportPreview `json:"preview"`
}

const importValidateSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["instance", "format", "data_base64"],
  "properties": {
    "instance": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$"},
    "format": {"type": "string", "enum": ["csv", "json"]},
    "data_base64": {"type": "string", "maxLength": 11184812}
  }
}`

func newImportValidateTool(lookup serviceLookup) registry.Tool {
	return registry.Tool{
		Name: importValidateToolName, Title: importValidateToolTitle,
		Description: importValidateToolDescription, Capability: importValidateCapability,
		Permission: registry.PermissionRead, Timeout: toolTimeout,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		Register: func(srv *mcp.Server) error {
			mcp.AddTool(srv, &mcp.Tool{
				Name: importValidateToolName, Title: importValidateToolTitle,
				Description: importValidateToolDescription,
				InputSchema: json.RawMessage(importValidateSchema),
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
			}, func(ctx context.Context, _ *mcp.CallToolRequest, in importValidateInput) (*mcp.CallToolResult, importValidateOutput, error) {
				data, err := DecodeImportPayload(in.Data)
				if err != nil {
					return nil, importValidateOutput{}, err
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, importValidateOutput{}, err
				}
				preview, err := svc.ValidateImport(ctx, in.Format, data)
				if err != nil {
					return nil, importValidateOutput{}, err
				}
				return nil, importValidateOutput{Preview: preview}, nil
			})
			return nil
		},
	}
}

const (
	orderGetToolName      = "magento.order.get"
	orderGetToolTitle     = "Get Magento order"
	orderSearchToolName   = "magento.order.search"
	orderSearchToolTitle  = "Search Magento orders"
	salesSummaryToolName  = "magento.sales.summary"
	salesSummaryToolTitle = "Magento sales summary"

	orderGetToolDescription = "Reads one order from the configured Magento 2 REST API " +
		"(GET /rest/{store_code}/V1/orders/{id}) including its line items. " +
		"Read-only. Customer email, addresses, and payment data are not returned. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	orderSearchToolDescription = "Searches the configured Magento 2 REST API " +
		"(GET /rest/{store_code}/V1/orders with searchCriteria filter groups) by status, state, increment id, " +
		"creation window, or store view. Read-only; line items are omitted from search results. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	salesSummaryToolDescription = "Aggregates Magento orders per day over a bounded window: order count, " +
		"grand-total revenue, and item quantity. Scans at most 1000 orders and reports truncation. " +
		"Revenue is order grand_total (tax and shipping included) and is not GA4 purchase revenue. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	orderGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id (not the increment id)."}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`

	orderSearchSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"status": {"type": "string", "maxLength": 64, "description": "Order status, e.g. processing or complete."},
		"state": {"type": "string", "maxLength": 64, "description": "Order state, e.g. new or complete."},
		"increment_id": {"type": "string", "maxLength": 32, "description": "Exact increment id, e.g. 000000123."},
		"created_from": {"type": "string", "description": "Inclusive creation lower bound, RFC 3339 or YYYY-MM-DD."},
		"created_to": {"type": "string", "description": "Exclusive creation upper bound, RFC 3339 or YYYY-MM-DD."},
		"store_id": {"type": "integer", "minimum": 0, "description": "Store view id filter."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 50},
		"cursor": {"type": "string", "pattern": "` + cursorPattern + `", "description": "Zero-based row offset; must be a multiple of limit."}
	}, "required": ["instance"], "additionalProperties": false}`

	salesSummarySchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"from": {"type": "string", "description": "Inclusive window start, RFC 3339 or YYYY-MM-DD."},
		"to": {"type": "string", "description": "Exclusive window end, RFC 3339 or YYYY-MM-DD."}
	}, "required": ["instance", "from", "to"], "additionalProperties": false}`
)

type orderGetInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
}

type orderGetOutput struct {
	Order Order `json:"order"`
}

type orderSearchInput struct {
	Instance    string `json:"instance"`
	Status      string `json:"status"`
	State       string `json:"state"`
	IncrementID string `json:"increment_id"`
	CreatedFrom string `json:"created_from"`
	CreatedTo   string `json:"created_to"`
	StoreID     int    `json:"store_id"`
	Limit       int    `json:"limit"`
	Cursor      string `json:"cursor"`
}

type orderSearchOutput struct {
	Orders []Order `json:"orders"`
	Total  int     `json:"total"`
}

type salesSummaryInput struct {
	Instance string `json:"instance"`
	From     string `json:"from"`
	To       string `json:"to"`
}

type salesSummaryOutput struct {
	Summary SalesSummary `json:"summary"`
}

func newOrderGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(orderGetToolName, orderGetToolTitle, orderGetToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(orderGetToolName, orderGetToolTitle, orderGetToolDescription, orderGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderGetInput) (*mcp.CallToolResult, orderGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderGetOutput{}, err
				}
				order, err := svc.GetOrder(ctx, in.OrderID)
				if err != nil {
					return nil, orderGetOutput{}, err
				}
				return nil, orderGetOutput{Order: *order}, nil
			})
		return nil
	}
	return t
}

func newOrderSearchTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(orderSearchToolName, orderSearchToolTitle, orderSearchToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(orderSearchToolName, orderSearchToolTitle, orderSearchToolDescription, orderSearchSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderSearchInput) (*mcp.CallToolResult, orderSearchOutput, error) {
				offset := 0
				if in.Cursor != "" {
					parsed, err := strconv.Atoi(in.Cursor)
					if err != nil || parsed < 0 {
						return nil, orderSearchOutput{}, invalidf("cursor must be a non-negative decimal row offset")
					}
					offset = parsed
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderSearchOutput{}, err
				}
				orders, total, err := svc.SearchOrders(ctx, OrderQuery{
					Status: in.Status, State: in.State, IncrementID: in.IncrementID,
					CreatedFrom: in.CreatedFrom, CreatedTo: in.CreatedTo, StoreID: in.StoreID,
					Limit: in.Limit, CursorOffset: offset,
				})
				if err != nil {
					return nil, orderSearchOutput{}, err
				}
				if orders == nil {
					orders = []Order{}
				}
				return nil, orderSearchOutput{Orders: orders, Total: total}, nil
			})
		return nil
	}
	return t
}

func newSalesSummaryTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(salesSummaryToolName, salesSummaryToolTitle, salesSummaryToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(salesSummaryToolName, salesSummaryToolTitle, salesSummaryToolDescription, salesSummarySchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in salesSummaryInput) (*mcp.CallToolResult, salesSummaryOutput, error) {
				from, err := parseSummaryTime(in.From, "from")
				if err != nil {
					return nil, salesSummaryOutput{}, err
				}
				to, err := parseSummaryTime(in.To, "to")
				if err != nil {
					return nil, salesSummaryOutput{}, err
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, salesSummaryOutput{}, err
				}
				summary, err := svc.SalesSummary(ctx, from, to)
				if err != nil {
					return nil, salesSummaryOutput{}, err
				}
				return nil, salesSummaryOutput{Summary: *summary}, nil
			})
		return nil
	}
	return t
}

// parseSummaryTime accepts RFC 3339 or a date; a date is midnight UTC.
func parseSummaryTime(value, field string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, nil
	}
	return time.Time{}, invalidf("%s must be an RFC 3339 timestamp or YYYY-MM-DD date", field)
}

const (
	categoryGetToolName    = "magento.category.get"
	categoryGetToolTitle   = "Get Magento category"
	categoryTreeToolName   = "magento.category.tree"
	categoryTreeToolTitle  = "Magento category tree"
	attributeSetsToolName  = "magento.attribute.sets"
	attributeSetsToolTitle = "List Magento attribute sets"
	attributesToolName     = "magento.attributes.list"
	attributesToolTitle    = "List Magento attribute-set attributes"

	categoryGetToolDescription = "Reads one category from the configured Magento 2 REST API " +
		"(GET /rest/{store_code}/V1/categories/{id}). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	categoryTreeToolDescription = "Reads the category subtree under a root category " +
		"(GET /rest/{store_code}/V1/categories?rootCategoryId&depth), nested up to depth 1-5. " +
		"Read-only. The instance input is resolved by the server when only one Magento instance is configured."
	attributeSetsToolDescription = "Lists the product attribute sets " +
		"(GET /rest/{store_code}/V1/products/attribute-sets/sets/list), at most 200. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	attributesToolDescription = "Lists the attributes of one product attribute set " +
		"(GET /rest/{store_code}/V1/products/attribute-sets/{setId}/attributes). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	categoryGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"category_id": {"type": "integer", "minimum": 1, "description": "Magento category id."}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`

	categoryTreeSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"root_id": {"type": "integer", "minimum": 1, "default": 2, "description": "Root category id; Magento's default root is 2."},
		"depth": {"type": "integer", "minimum": 1, "maximum": 5, "default": 2, "description": "Tree depth, 1-5."}
	}, "required": ["instance"], "additionalProperties": false}`

	attributeSetsSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."}
	}, "required": ["instance"], "additionalProperties": false}`

	attributesSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"attribute_set_id": {"type": "integer", "minimum": 1, "description": "Attribute set id, e.g. 4 for Default."}
	}, "required": ["instance", "attribute_set_id"], "additionalProperties": false}`
)

type categoryGetInput struct {
	Instance   string `json:"instance"`
	CategoryID int    `json:"category_id"`
}

type categoryGetOutput struct {
	Category Category `json:"category"`
}

type categoryTreeInput struct {
	Instance string `json:"instance"`
	RootID   int    `json:"root_id"`
	Depth    int    `json:"depth"`
}

type categoryTreeOutput struct {
	Roots []Category `json:"roots"`
}

type attributeSetsInput struct {
	Instance string `json:"instance"`
}

type attributeSetsOutput struct {
	AttributeSets []AttributeSet `json:"attribute_sets"`
}

type attributesInput struct {
	Instance       string `json:"instance"`
	AttributeSetID int    `json:"attribute_set_id"`
}

type attributesOutput struct {
	Attributes []Attribute `json:"attributes"`
}

func newCategoryGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(categoryGetToolName, categoryGetToolTitle, categoryGetToolDescription)
	t.Capability = "magento.category.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(categoryGetToolName, categoryGetToolTitle, categoryGetToolDescription, categoryGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryGetInput) (*mcp.CallToolResult, categoryGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryGetOutput{}, err
				}
				category, err := svc.GetCategory(ctx, in.CategoryID)
				if err != nil {
					return nil, categoryGetOutput{}, err
				}
				return nil, categoryGetOutput{Category: *category}, nil
			})
		return nil
	}
	return t
}

func newCategoryTreeTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(categoryTreeToolName, categoryTreeToolTitle, categoryTreeToolDescription)
	t.Capability = "magento.category.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(categoryTreeToolName, categoryTreeToolTitle, categoryTreeToolDescription, categoryTreeSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryTreeInput) (*mcp.CallToolResult, categoryTreeOutput, error) {
				rootID := in.RootID
				if rootID == 0 {
					rootID = 2
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryTreeOutput{}, err
				}
				roots, err := svc.CategoryTree(ctx, rootID, in.Depth)
				if err != nil {
					return nil, categoryTreeOutput{}, err
				}
				return nil, categoryTreeOutput{Roots: roots}, nil
			})
		return nil
	}
	return t
}

func newAttributeSetsTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(attributeSetsToolName, attributeSetsToolTitle, attributeSetsToolDescription)
	t.Capability = "magento.attribute.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(attributeSetsToolName, attributeSetsToolTitle, attributeSetsToolDescription, attributeSetsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in attributeSetsInput) (*mcp.CallToolResult, attributeSetsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, attributeSetsOutput{}, err
				}
				sets, err := svc.AttributeSets(ctx)
				if err != nil {
					return nil, attributeSetsOutput{}, err
				}
				return nil, attributeSetsOutput{AttributeSets: sets}, nil
			})
		return nil
	}
	return t
}

func newAttributesTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(attributesToolName, attributesToolTitle, attributesToolDescription)
	t.Capability = "magento.attribute.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(attributesToolName, attributesToolTitle, attributesToolDescription, attributesSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in attributesInput) (*mcp.CallToolResult, attributesOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, attributesOutput{}, err
				}
				attributes, err := svc.Attributes(ctx, in.AttributeSetID)
				if err != nil {
					return nil, attributesOutput{}, err
				}
				return nil, attributesOutput{Attributes: attributes}, nil
			})
		return nil
	}
	return t
}

const (
	productMediaToolName  = "magento.product.media"
	productMediaToolTitle = "Get Magento product media"
	stockStatusToolName   = "magento.inventory.status"
	stockStatusToolTitle  = "Get Magento stock status"
	sourceItemsToolName   = "magento.inventory.source_items"
	sourceItemsToolTitle  = "List Magento MSI source items"

	productMediaToolDescription = "Lists the media gallery of one product " +
		"(GET /rest/{store_code}/V1/products/{sku}/media). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	stockStatusToolDescription = "Reads the catalog inventory status of one SKU " +
		"(GET /rest/{store_code}/V1/stockItems/{sku}): quantity, in-stock flag, and backorders. " +
		"On multi-source installations this is the aggregated catalog value; use source_items for per-source quantities. Read-only."
	sourceItemsToolDescription = "Lists MSI source quantities for up to 20 SKUs " +
		"(GET /rest/{store_code}/V1/inventory/source-items with a sku in-filter). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	productMediaSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU, 1-64 characters from [A-Za-z0-9._:-]."}
	}, "required": ["instance", "sku"], "additionalProperties": false}`

	stockStatusSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU, 1-64 characters from [A-Za-z0-9._:-]."}
	}, "required": ["instance", "sku"], "additionalProperties": false}`

	sourceItemsSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"skus": {"type": "array", "minItems": 1, "maxItems": 20, "items": {"type": "string", "pattern": "` + skuInputPattern + `"}, "description": "SKUs to look up, 1-20."}
	}, "required": ["instance", "skus"], "additionalProperties": false}`
)

type productMediaInput struct {
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
}

type productMediaOutput struct {
	Media []MediaEntry `json:"media"`
}

type stockStatusInput struct {
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
}

type stockStatusOutput struct {
	Status StockStatus `json:"status"`
}

type sourceItemsInput struct {
	Instance string   `json:"instance"`
	SKUs     []string `json:"skus"`
}

type sourceItemsOutput struct {
	Items []SourceItemRead `json:"items"`
}

func newProductMediaTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(productMediaToolName, productMediaToolTitle, productMediaToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(productMediaToolName, productMediaToolTitle, productMediaToolDescription, productMediaSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productMediaInput) (*mcp.CallToolResult, productMediaOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productMediaOutput{}, err
				}
				media, err := svc.ProductMedia(ctx, in.SKU)
				if err != nil {
					return nil, productMediaOutput{}, err
				}
				return nil, productMediaOutput{Media: media}, nil
			})
		return nil
	}
	return t
}

func newStockStatusTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(stockStatusToolName, stockStatusToolTitle, stockStatusToolDescription)
	t.Capability = "magento.inventory.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(stockStatusToolName, stockStatusToolTitle, stockStatusToolDescription, stockStatusSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in stockStatusInput) (*mcp.CallToolResult, stockStatusOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, stockStatusOutput{}, err
				}
				status, err := svc.StockStatus(ctx, in.SKU)
				if err != nil {
					return nil, stockStatusOutput{}, err
				}
				return nil, stockStatusOutput{Status: *status}, nil
			})
		return nil
	}
	return t
}

func newSourceItemsTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(sourceItemsToolName, sourceItemsToolTitle, sourceItemsToolDescription)
	t.Capability = "magento.inventory.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(sourceItemsToolName, sourceItemsToolTitle, sourceItemsToolDescription, sourceItemsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in sourceItemsInput) (*mcp.CallToolResult, sourceItemsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, sourceItemsOutput{}, err
				}
				items, err := svc.SourceItems(ctx, in.SKUs)
				if err != nil {
					return nil, sourceItemsOutput{}, err
				}
				return nil, sourceItemsOutput{Items: items}, nil
			})
		return nil
	}
	return t
}

const (
	orderHistoryToolName  = "magento.order.status_history"
	orderHistoryToolTitle = "Magento order status history"
	invoiceListToolName   = "magento.invoice.list"
	invoiceListToolTitle  = "List Magento order invoices"
	invoiceGetToolName    = "magento.invoice.get"
	invoiceGetToolTitle   = "Get Magento invoice"
	shipmentListToolName  = "magento.shipment.list"
	shipmentListToolTitle = "List Magento order shipments"
	shipmentGetToolName   = "magento.shipment.get"
	shipmentGetToolTitle  = "Get Magento shipment"

	orderHistoryToolDescription = "Reads the status history of one order " +
		"(GET /rest/{store_code}/V1/orders/{id}/comments), including status, comment, and notification flags. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	invoiceListToolDescription = "Lists the invoices of one order " +
		"(GET /rest/{store_code}/V1/orders/{id}/invoices) with totals and quantities. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	invoiceGetToolDescription = "Reads one invoice by id " +
		"(GET /rest/{store_code}/V1/invoices/{id}) with totals and quantities. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	shipmentListToolDescription = "Lists the shipments of one order " +
		"(GET /rest/{store_code}/V1/orders/{id}/shipments) with total quantity and tracking numbers. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	shipmentGetToolDescription = "Reads one shipment by id " +
		"(GET /rest/{store_code}/V1/shipments/{id}) with total quantity and tracking numbers. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	orderIDOnlySchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id."}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`

	invoiceIDSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"invoice_id": {"type": "integer", "minimum": 1, "description": "Magento invoice entity id."}
	}, "required": ["instance", "invoice_id"], "additionalProperties": false}`

	shipmentIDSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"shipment_id": {"type": "integer", "minimum": 1, "description": "Magento shipment entity id."}
	}, "required": ["instance", "shipment_id"], "additionalProperties": false}`
)

type orderIDInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
}

type orderHistoryOutput struct {
	History []OrderStatusHistory `json:"history"`
}

type invoiceListOutput struct {
	Invoices []Invoice `json:"invoices"`
}

type invoiceGetInput struct {
	Instance  string `json:"instance"`
	InvoiceID int    `json:"invoice_id"`
}

type invoiceGetOutput struct {
	Invoice Invoice `json:"invoice"`
}

type shipmentListOutput struct {
	Shipments []Shipment `json:"shipments"`
}

type shipmentGetInput struct {
	Instance   string `json:"instance"`
	ShipmentID int    `json:"shipment_id"`
}

type shipmentGetOutput struct {
	Shipment Shipment `json:"shipment"`
}

func newOrderHistoryTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(orderHistoryToolName, orderHistoryToolTitle, orderHistoryToolDescription)
	t.Capability = "magento.order.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(orderHistoryToolName, orderHistoryToolTitle, orderHistoryToolDescription, orderIDOnlySchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderIDInput) (*mcp.CallToolResult, orderHistoryOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderHistoryOutput{}, err
				}
				history, err := svc.OrderStatusHistory(ctx, in.OrderID)
				if err != nil {
					return nil, orderHistoryOutput{}, err
				}
				return nil, orderHistoryOutput{History: history}, nil
			})
		return nil
	}
	return t
}

func newInvoiceListTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(invoiceListToolName, invoiceListToolTitle, invoiceListToolDescription)
	t.Capability = "magento.invoice.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(invoiceListToolName, invoiceListToolTitle, invoiceListToolDescription, orderIDOnlySchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderIDInput) (*mcp.CallToolResult, invoiceListOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceListOutput{}, err
				}
				invoices, err := svc.OrderInvoices(ctx, in.OrderID)
				if err != nil {
					return nil, invoiceListOutput{}, err
				}
				return nil, invoiceListOutput{Invoices: invoices}, nil
			})
		return nil
	}
	return t
}

func newInvoiceGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(invoiceGetToolName, invoiceGetToolTitle, invoiceGetToolDescription)
	t.Capability = "magento.invoice.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(invoiceGetToolName, invoiceGetToolTitle, invoiceGetToolDescription, invoiceIDSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in invoiceGetInput) (*mcp.CallToolResult, invoiceGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceGetOutput{}, err
				}
				invoice, err := svc.GetInvoice(ctx, in.InvoiceID)
				if err != nil {
					return nil, invoiceGetOutput{}, err
				}
				return nil, invoiceGetOutput{Invoice: *invoice}, nil
			})
		return nil
	}
	return t
}

func newShipmentListTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(shipmentListToolName, shipmentListToolTitle, shipmentListToolDescription)
	t.Capability = "magento.shipment.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(shipmentListToolName, shipmentListToolTitle, shipmentListToolDescription, orderIDOnlySchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderIDInput) (*mcp.CallToolResult, shipmentListOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, shipmentListOutput{}, err
				}
				shipments, err := svc.OrderShipments(ctx, in.OrderID)
				if err != nil {
					return nil, shipmentListOutput{}, err
				}
				return nil, shipmentListOutput{Shipments: shipments}, nil
			})
		return nil
	}
	return t
}

func newShipmentGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(shipmentGetToolName, shipmentGetToolTitle, shipmentGetToolDescription)
	t.Capability = "magento.shipment.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(shipmentGetToolName, shipmentGetToolTitle, shipmentGetToolDescription, shipmentIDSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in shipmentGetInput) (*mcp.CallToolResult, shipmentGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, shipmentGetOutput{}, err
				}
				shipment, err := svc.GetShipment(ctx, in.ShipmentID)
				if err != nil {
					return nil, shipmentGetOutput{}, err
				}
				return nil, shipmentGetOutput{Shipment: *shipment}, nil
			})
		return nil
	}
	return t
}

const (
	creditmemoListToolName  = "magento.creditmemo.list"         // #nosec G101 -- Magento credit memo, not a credential
	creditmemoListToolTitle = "List Magento order credit memos" // #nosec G101 -- Magento credit memo
	creditmemoGetToolName   = "magento.creditmemo.get"          // #nosec G101 -- Magento credit memo
	creditmemoGetToolTitle  = "Get Magento credit memo"         // #nosec G101 -- Magento credit memo

	creditmemoListToolDescription = "Lists the credit memos of one order " +
		"(GET /rest/{store_code}/V1/orders/{id}/creditmemos) with totals and quantities. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	creditmemoGetToolDescription = "Reads one credit memo by id " +
		"(GET /rest/{store_code}/V1/creditmemos/{id}) with totals and quantities. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	creditmemoIDSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"creditmemo_id": {"type": "integer", "minimum": 1, "description": "Magento credit memo entity id."}
	}, "required": ["instance", "creditmemo_id"], "additionalProperties": false}`
)

type creditmemoListOutput struct {
	Creditmemos []Creditmemo `json:"creditmemos"`
}

type creditmemoGetInput struct {
	Instance     string `json:"instance"`
	CreditmemoID int    `json:"creditmemo_id"`
}

type creditmemoGetOutput struct {
	Creditmemo Creditmemo `json:"creditmemo"`
}

func newCreditmemoListTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(creditmemoListToolName, creditmemoListToolTitle, creditmemoListToolDescription)
	t.Capability = "magento.creditmemo.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(creditmemoListToolName, creditmemoListToolTitle, creditmemoListToolDescription, orderIDOnlySchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderIDInput) (*mcp.CallToolResult, creditmemoListOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, creditmemoListOutput{}, err
				}
				creditmemos, err := svc.OrderCreditmemos(ctx, in.OrderID)
				if err != nil {
					return nil, creditmemoListOutput{}, err
				}
				return nil, creditmemoListOutput{Creditmemos: creditmemos}, nil
			})
		return nil
	}
	return t
}

func newCreditmemoGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(creditmemoGetToolName, creditmemoGetToolTitle, creditmemoGetToolDescription)
	t.Capability = "magento.creditmemo.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(creditmemoGetToolName, creditmemoGetToolTitle, creditmemoGetToolDescription, creditmemoIDSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in creditmemoGetInput) (*mcp.CallToolResult, creditmemoGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, creditmemoGetOutput{}, err
				}
				creditmemo, err := svc.GetCreditmemo(ctx, in.CreditmemoID)
				if err != nil {
					return nil, creditmemoGetOutput{}, err
				}
				return nil, creditmemoGetOutput{Creditmemo: *creditmemo}, nil
			})
		return nil
	}
	return t
}

const (
	configurableChildrenToolName  = "magento.configurable.children"
	configurableChildrenToolTitle = "List configurable children"
	attributeOptionsToolName      = "magento.attributes.options"
	attributeOptionsToolTitle     = "List Magento attribute options"
	categoryProductsToolName      = "magento.category.products"
	categoryProductsToolTitle     = "List products in a Magento category"

	configurableChildrenToolDescription = "Lists the child products linked to a configurable product " +
		"(GET /rest/{store_code}/V1/configurable-products/{sku}/children). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	attributeOptionsToolDescription = "Lists the options of a select or multiselect attribute " +
		"(GET /rest/{store_code}/V1/products/attributes/{code}/options); configurable variants reference options by value_index. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	categoryProductsToolDescription = "Lists the products assigned to a category " +
		"(GET /rest/{store_code}/V1/categories/{id}/products) with pagination. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	configurableChildrenSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Configurable parent SKU."}
	}, "required": ["instance", "parent_sku"], "additionalProperties": false}`

	attributeOptionsSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `", "description": "Attribute code, e.g. color."}
	}, "required": ["instance", "attribute_code"], "additionalProperties": false}`

	categoryProductsSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"category_id": {"type": "integer", "minimum": 1, "description": "Magento category id."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 200, "default": 50, "description": "Page size, 1-200."},
		"cursor": {"type": "string", "pattern": "` + cursorPattern + `", "description": "Zero-based row offset; must be a multiple of limit."}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`
)

type configurableChildrenInput struct {
	Instance  string `json:"instance"`
	ParentSKU string `json:"parent_sku"`
}

type configurableChildrenOutput struct {
	Children []Product `json:"children"`
}

type attributeOptionsInput struct {
	Instance      string `json:"instance"`
	AttributeCode string `json:"attribute_code"`
}

type attributeOptionsOutput struct {
	Options []AttributeOption `json:"options"`
}

type categoryProductsInput struct {
	Instance   string `json:"instance"`
	CategoryID int    `json:"category_id"`
	Limit      int    `json:"limit"`
	Cursor     string `json:"cursor"`
}

type categoryProductsOutput struct {
	Products []Product `json:"products"`
	Total    int       `json:"total"`
}

func newConfigurableChildrenTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(configurableChildrenToolName, configurableChildrenToolTitle, configurableChildrenToolDescription)
	t.Capability = "magento.configurable.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(configurableChildrenToolName, configurableChildrenToolTitle, configurableChildrenToolDescription, configurableChildrenSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in configurableChildrenInput) (*mcp.CallToolResult, configurableChildrenOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, configurableChildrenOutput{}, err
				}
				children, err := svc.Children(ctx, in.ParentSKU)
				if err != nil {
					return nil, configurableChildrenOutput{}, err
				}
				return nil, configurableChildrenOutput{Children: children}, nil
			})
		return nil
	}
	return t
}

func newAttributeOptionsTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(attributeOptionsToolName, attributeOptionsToolTitle, attributeOptionsToolDescription)
	t.Capability = "magento.attribute.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(attributeOptionsToolName, attributeOptionsToolTitle, attributeOptionsToolDescription, attributeOptionsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in attributeOptionsInput) (*mcp.CallToolResult, attributeOptionsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, attributeOptionsOutput{}, err
				}
				options, err := svc.AttributesOptions(ctx, in.AttributeCode)
				if err != nil {
					return nil, attributeOptionsOutput{}, err
				}
				return nil, attributeOptionsOutput{Options: options}, nil
			})
		return nil
	}
	return t
}

func newCategoryProductsTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(categoryProductsToolName, categoryProductsToolTitle, categoryProductsToolDescription)
	t.Capability = "magento.category.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(categoryProductsToolName, categoryProductsToolTitle, categoryProductsToolDescription, categoryProductsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryProductsInput) (*mcp.CallToolResult, categoryProductsOutput, error) {
				offset := 0
				if in.Cursor != "" {
					parsed, err := strconv.Atoi(in.Cursor)
					if err != nil || parsed < 0 {
						return nil, categoryProductsOutput{}, invalidf("cursor must be a non-negative decimal row offset")
					}
					offset = parsed
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryProductsOutput{}, err
				}
				products, total, err := svc.CategoryProducts(ctx, in.CategoryID, in.Limit, offset)
				if err != nil {
					return nil, categoryProductsOutput{}, err
				}
				if products == nil {
					products = []Product{}
				}
				return nil, categoryProductsOutput{Products: products, Total: total}, nil
			})
		return nil
	}
	return t
}

const (
	customerGetToolName     = "magento.customer.get"
	customerGetToolTitle    = "Get Magento customer"
	customerSearchToolName  = "magento.customer.search"
	customerSearchToolTitle = "Search Magento customers by email"

	customerGetToolDescription = "Reads one customer from the configured Magento 2 REST API " +
		"(GET /rest/{store_code}/V1/customers/{id}). Read-only and PII-safe: the projection omits email, name, and addresses. " +
		"The instance input is resolved by the server when only one Magento instance is configured."
	customerSearchToolDescription = "Finds customers by exact email " +
		"(GET /rest/{store_code}/V1/customers/search with an email eq filter). The response omits the email, name, and addresses: " +
		"it returns id, group, website, store, and timestamps only. Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	customerGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"customer_id": {"type": "integer", "minimum": 1, "description": "Magento customer id."}
	}, "required": ["instance", "customer_id"], "additionalProperties": false}`

	customerSearchSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"email": {"type": "string", "format": "email", "maxLength": 254, "description": "Exact customer email to match; never echoed back."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20, "description": "Page size, 1-100."},
		"cursor": {"type": "string", "pattern": "` + cursorPattern + `", "description": "Zero-based row offset; must be a multiple of limit."}
	}, "required": ["instance", "email"], "additionalProperties": false}`
)

type customerGetInput struct {
	Instance   string `json:"instance"`
	CustomerID int    `json:"customer_id"`
}

type customerGetOutput struct {
	Customer Customer `json:"customer"`
}

type customerSearchInput struct {
	Instance string `json:"instance"`
	Email    string `json:"email"`
	Limit    int    `json:"limit"`
	Cursor   string `json:"cursor"`
}

type customerSearchOutput struct {
	Customers []Customer `json:"customers"`
	Total     int        `json:"total"`
}

func newCustomerGetTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(customerGetToolName, customerGetToolTitle, customerGetToolDescription)
	t.Capability = "magento.customer.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(customerGetToolName, customerGetToolTitle, customerGetToolDescription, customerGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerGetInput) (*mcp.CallToolResult, customerGetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerGetOutput{}, err
				}
				customer, err := svc.GetCustomer(ctx, in.CustomerID)
				if err != nil {
					return nil, customerGetOutput{}, err
				}
				return nil, customerGetOutput{Customer: *customer}, nil
			})
		return nil
	}
	return t
}

func newCustomerSearchTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(customerSearchToolName, customerSearchToolTitle, customerSearchToolDescription)
	t.Capability = "magento.customer.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(customerSearchToolName, customerSearchToolTitle, customerSearchToolDescription, customerSearchSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerSearchInput) (*mcp.CallToolResult, customerSearchOutput, error) {
				offset := 0
				if in.Cursor != "" {
					parsed, err := strconv.Atoi(in.Cursor)
					if err != nil || parsed < 0 {
						return nil, customerSearchOutput{}, invalidf("cursor must be a non-negative decimal row offset")
					}
					offset = parsed
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerSearchOutput{}, err
				}
				customers, total, err := svc.SearchCustomers(ctx, in.Email, in.Limit, offset)
				if err != nil {
					return nil, customerSearchOutput{}, err
				}
				return nil, customerSearchOutput{Customers: customers, Total: total}, nil
			})
		return nil
	}
	return t
}

const (
	productLinksToolName  = "magento.product.links"
	productLinksToolTitle = "List Magento product links"

	productLinksToolDescription = "Lists related, upsell, or cross-sell links of a product " +
		"(GET /rest/{store_code}/V1/products/{sku}/links?linkType=...). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	productLinksSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU."},
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"], "description": "Link relation to list."}
	}, "required": ["instance", "sku", "link_type"], "additionalProperties": false}`
)

type productLinksInput struct {
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
	LinkType string `json:"link_type"`
}

type productLinksOutput struct {
	Links []ProductLink `json:"links"`
}

func newProductLinksTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(productLinksToolName, productLinksToolTitle, productLinksToolDescription)
	t.Capability = "magento.product.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(productLinksToolName, productLinksToolTitle, productLinksToolDescription, productLinksSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productLinksInput) (*mcp.CallToolResult, productLinksOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productLinksOutput{}, err
				}
				links, err := svc.ProductLinks(ctx, in.SKU, in.LinkType)
				if err != nil {
					return nil, productLinksOutput{}, err
				}
				return nil, productLinksOutput{Links: links}, nil
			})
		return nil
	}
	return t
}

const (
	productsBatchToolName  = "magento.products.get"
	productsBatchToolTitle = "Get several Magento products"

	productsBatchToolDescription = "Reads up to 50 products by SKU in one search " +
		"(GET /rest/{store_code}/V1/products with a sku in-filter). Read-only. " +
		"The instance input is resolved by the server when only one Magento instance is configured."

	productsBatchSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id."},
		"skus": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string", "pattern": "` + skuInputPattern + `"}, "description": "SKUs to fetch, 1-50; duplicates are ignored."}
	}, "required": ["instance", "skus"], "additionalProperties": false}`
)

type productsBatchInput struct {
	Instance string   `json:"instance"`
	SKUs     []string `json:"skus"`
}

type productsBatchOutput struct {
	Products []Product `json:"products"`
}

func newProductsBatchTool(lookup serviceLookup) registry.Tool {
	t := toolMetadata(productsBatchToolName, productsBatchToolTitle, productsBatchToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(productsBatchToolName, productsBatchToolTitle, productsBatchToolDescription, productsBatchSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productsBatchInput) (*mcp.CallToolResult, productsBatchOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productsBatchOutput{}, err
				}
				products, err := svc.BatchGet(ctx, in.SKUs)
				if err != nil {
					return nil, productsBatchOutput{}, err
				}
				return nil, productsBatchOutput{Products: products}, nil
			})
		return nil
	}
	return t
}
