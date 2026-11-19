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
	Instance string `json:"instance"`
	Query    string `json:"query"`
	SKU      string `json:"sku"`
	TypeID   string `json:"type_id"`
	Status   int    `json:"status"`
	Limit    int    `json:"limit"`
	Cursor   string `json:"cursor"`
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
					SKU: in.SKU, NameContains: in.Query, TypeID: in.TypeID,
					Status: in.Status, Limit: in.Limit, CursorOffset: offset,
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
