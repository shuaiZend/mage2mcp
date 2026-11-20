// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	capabilitiesToolName   = "ga4.property.capabilities"
	realtimeToolName       = "ga4.analytics.realtime"
	capabilitiesCapability = "ga4.property.read"
	realtimeCapability     = "ga4.analytics.read"
)

// CapabilitiesInput is the input of ga4.property.capabilities.
type CapabilitiesInput struct {
	Instance string `json:"instance" jsonschema:"configured GA4 instance id"`
}

// RealtimeInput is the input of ga4.analytics.realtime.
type RealtimeInput struct {
	Instance   string   `json:"instance" jsonschema:"configured GA4 instance id"`
	Dimensions []string `json:"dimensions,omitempty" jsonschema:"realtime dimensions to group by"`
	Metrics    []string `json:"metrics" jsonschema:"realtime metrics to aggregate"`
	Limit      int64    `json:"limit,omitempty" jsonschema:"maximum rows, 1-100"`
}

// RealtimeOutput is the structured result of ga4.analytics.realtime: rows plus
// the property time zone and currency.
type RealtimeOutput struct {
	Rows     []TrafficRow `json:"rows" jsonschema:"Per-row results, in input order."`
	TimeZone string       `json:"time_zone" jsonschema:"IANA time zone the report was computed in."`
	Currency string       `json:"currency" jsonschema:"ISO 4217 code the amounts are expressed in."`
}

// RegisterExtra adds the capability-probe and realtime read-only tools to reg.
// It never mutates svc and returns the first validation or registration error.
func RegisterExtra(reg *registry.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return errors.New("ga4: RegisterExtra needs a non-nil registry and service")
	}
	return registerExtraTools(reg, singleLookup(svc))
}

// registerExtraTools builds the capability, realtime, and ecommerce tools
// against an instance lookup (ADR-0018).
func registerExtraTools(reg *registry.Registry, lookup serviceLookup) error {
	capabilities := &mcp.Tool{
		Name:  capabilitiesToolName,
		Title: "GA4 property capabilities",
		Description: "Reads the ecommerce capability profile of the configured Google Analytics 4 property: whether any of the eleven standard ecommerce events fired in the last seven complete days, plus the property currency and time zone. " +
			"The probe is read-only and aggregate-only: it returns event counts, never user-level data. An absent result is a fact about the property, not proof that zero sales occurred.",
		InputSchema: instanceInputSchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	realtime := &mcp.Tool{
		Name:  realtimeToolName,
		Title: "GA4 realtime report",
		Description: "Reads the last 30 minutes of activity from the Google Analytics 4 Data API for the configured property. " +
			"Dimensions and metrics are allowlisted, rows are capped at 100, and results are read-only and aggregate-only: no user-level data is returned. " +
			"Realtime counts are volatile, cover a fixed 30-minute window, and are not comparable to daily report totals.",
		InputSchema: realtimeInputSchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	tools := []registry.Tool{
		{
			Name: capabilities.Name, Title: capabilities.Title, Description: capabilities.Description,
			Capability: capabilitiesCapability, Permission: registry.PermissionRead, Timeout: 60 * time.Second,
			Annotations: *capabilities.Annotations,
			Register: func(server *mcp.Server) error {
				toolerr.Add(server, capabilities, func(ctx context.Context, _ *mcp.CallToolRequest, input CapabilitiesInput) (*mcp.CallToolResult, PropertyCapabilities, error) {
					svc, err := lookup(input.Instance)
					if err != nil {
						return nil, PropertyCapabilities{}, err
					}
					result, err := svc.Capabilities(ctx)
					if err != nil {
						return nil, PropertyCapabilities{}, err
					}
					return nil, *result, nil
				})
				return nil
			},
		},
		{
			Name: realtime.Name, Title: realtime.Title, Description: realtime.Description,
			Capability: realtimeCapability, Permission: registry.PermissionRead, Timeout: 60 * time.Second,
			Annotations: *realtime.Annotations,
			Register: func(server *mcp.Server) error {
				toolerr.Add(server, realtime, func(ctx context.Context, _ *mcp.CallToolRequest, input RealtimeInput) (*mcp.CallToolResult, RealtimeOutput, error) {
					svc, err := lookup(input.Instance)
					if err != nil {
						return nil, RealtimeOutput{}, err
					}
					result, err := svc.Realtime(ctx, RealtimeQuery{
						Dimensions: input.Dimensions,
						Metrics:    input.Metrics,
						Limit:      input.Limit,
					})
					if err != nil {
						return nil, RealtimeOutput{}, err
					}
					return nil, RealtimeOutput{Rows: result.Rows, TimeZone: result.TimeZone, Currency: result.Currency}, nil
				})
				return nil
			},
		},
	}
	tools = append(tools, newEcommerceTool(lookup), newProductsTool(lookup))
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

// instanceInputSchema is the strict schema shared by tools whose only input is
// the instance id.
func instanceInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"instance": map[string]any{
				"type":        "string",
				"description": "Configured GA4 instance id, for example giztop.",
			},
		},
		"required": []any{"instance"},
	}
}

// realtimeInputSchema advertises the realtime allowlists as enums and bounds
// the row limit at 100.
func realtimeInputSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"instance": map[string]any{
				"type":        "string",
				"description": "Configured GA4 instance id, for example giztop.",
			},
			"dimensions": map[string]any{
				"type":        "array",
				"maxItems":    len(realtimeDimensionAllowlist),
				"description": "Realtime dimensions to group by. Allowed: " + strings.Join(realtimeDimensionAllowlist, ", ") + ".",
				"items":       map[string]any{"type": "string", "enum": stringEnums(realtimeDimensionAllowlist)},
			},
			"metrics": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "Realtime metrics to aggregate. Allowed: " + strings.Join(realtimeMetricAllowlist, ", ") + ".",
				"items":       map[string]any{"type": "string", "enum": stringEnums(realtimeMetricAllowlist)},
			},
			"limit": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"maximum":     maxRealtimeLimit,
				"default":     defaultRealtimeLimit,
				"description": "Maximum rows to return, from 1 to 100. Defaults to 100.",
			},
		},
		"required": []any{"instance", "metrics"},
	}
}

// stringEnums converts an allowlist into JSON schema enum values.
func stringEnums(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

const (
	ecommerceToolName   = "ga4.ecommerce.overview"
	ecommerceToolTitle  = "GA4 ecommerce overview"
	ecommerceToolDesc   = "Read-only aggregate report of GA4 purchases and revenue per day for the configured property. The property capability probe runs first; a property without ecommerce tracking returns ecommerce_not_tracked instead of zeros. Revenue is GA4-attributed revenue, not Magento order revenue."
	ecommerceCapability = "ga4.ecommerce.read"
)

// EcommerceInput is the input of ga4.ecommerce.overview.
type EcommerceInput struct {
	Instance  string `json:"instance" jsonschema:"configured GA4 instance id"`
	StartDate string `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD or relative value; defaults to 7daysAgo"`
	EndDate   string `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD or relative value; defaults to yesterday"`
}

// EcommerceOutput is the structured result of ga4.ecommerce.overview.
type EcommerceOutput struct {
	Overview *EcommerceOverview `json:"overview" jsonschema:"Headline figures for the window."`
}

func newEcommerceTool(lookup serviceLookup) registry.Tool {
	definition := &mcp.Tool{
		Name:        ecommerceToolName,
		Title:       ecommerceToolTitle,
		Description: ecommerceToolDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	return registry.Tool{
		Name: definition.Name, Title: definition.Title, Description: definition.Description,
		Capability: ecommerceCapability, Permission: registry.PermissionRead, Timeout: 60 * time.Second,
		Annotations: *definition.Annotations,
		Register: func(server *mcp.Server) error {
			toolerr.Add(server, definition, func(ctx context.Context, _ *mcp.CallToolRequest, in EcommerceInput) (*mcp.CallToolResult, EcommerceOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, EcommerceOutput{}, err
				}
				overview, err := svc.EcommerceOverview(ctx, in.StartDate, in.EndDate)
				if err != nil {
					return nil, EcommerceOutput{}, err
				}
				return nil, EcommerceOutput{Overview: overview}, nil
			})
			return nil
		},
	}
}

const (
	productsToolName   = "ga4.ecommerce.products"
	productsToolTitle  = "GA4 product performance"
	productsToolDesc   = "Read-only item-level GA4 report: views, add-to-carts, purchases, and item revenue per itemId for the configured property. The capability probe runs first; a property without ecommerce tracking returns ecommerce_not_tracked instead of zeros. Item revenue is GA4-attributed revenue, not Magento order revenue."
	productsCapability = "ga4.ecommerce.read"
)

// ProductsInput is the input of ga4.ecommerce.products.
type ProductsInput struct {
	Instance  string `json:"instance" jsonschema:"configured GA4 instance id"`
	StartDate string `json:"start_date,omitempty" jsonschema:"YYYY-MM-DD or relative value; defaults to 7daysAgo"`
	EndDate   string `json:"end_date,omitempty" jsonschema:"YYYY-MM-DD or relative value; defaults to yesterday"`
	Limit     int64  `json:"limit,omitempty" jsonschema:"maximum rows, 1-200"`
}

// ProductsOutput is the structured result of ga4.ecommerce.products.
type ProductsOutput struct {
	Performance *ProductPerformance `json:"performance" jsonschema:"Performance figures for the window."`
}

func newProductsTool(lookup serviceLookup) registry.Tool {
	definition := &mcp.Tool{
		Name:        productsToolName,
		Title:       productsToolTitle,
		Description: productsToolDesc,
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	return registry.Tool{
		Name: definition.Name, Title: definition.Title, Description: definition.Description,
		Capability: productsCapability, Permission: registry.PermissionRead, Timeout: 60 * time.Second,
		Annotations: *definition.Annotations,
		Register: func(server *mcp.Server) error {
			toolerr.Add(server, definition, func(ctx context.Context, _ *mcp.CallToolRequest, in ProductsInput) (*mcp.CallToolResult, ProductsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, ProductsOutput{}, err
				}
				performance, err := svc.TopProducts(ctx, in.StartDate, in.EndDate, in.Limit)
				if err != nil {
					return nil, ProductsOutput{}, err
				}
				return nil, ProductsOutput{Performance: performance}, nil
			})
			return nil
		},
	}
}
