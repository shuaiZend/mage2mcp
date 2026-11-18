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
	if reg == nil {
		return errs.New(errs.CodeInternal, "tool registry is required")
	}
	if svc == nil {
		return errs.New(errs.CodeInternal, "Magento service is required")
	}
	for _, tool := range []registry.Tool{newGetTool(svc), newSearchTool(svc)} {
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
func newGetTool(svc *Service) registry.Tool {
	t := toolMetadata(getToolName, getToolTitle, getToolDescription)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(getToolName, getToolTitle, getToolDescription, productGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productGetInput) (*mcp.CallToolResult, productGetOutput, error) {
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
func newSearchTool(svc *Service) registry.Tool {
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
