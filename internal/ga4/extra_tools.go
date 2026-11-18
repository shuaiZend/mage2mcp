// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	Rows     []TrafficRow `json:"rows"`
	TimeZone string       `json:"time_zone"`
	Currency string       `json:"currency"`
}

// RegisterExtra adds the capability-probe and realtime read-only tools to reg.
// It never mutates svc and returns the first validation or registration error.
func RegisterExtra(reg *registry.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return errors.New("ga4: RegisterExtra needs a non-nil registry and service")
	}
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
				mcp.AddTool(server, capabilities, func(ctx context.Context, _ *mcp.CallToolRequest, _ CapabilitiesInput) (*mcp.CallToolResult, PropertyCapabilities, error) {
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
				mcp.AddTool(server, realtime, func(ctx context.Context, _ *mcp.CallToolRequest, input RealtimeInput) (*mcp.CallToolResult, RealtimeOutput, error) {
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
