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
	trafficToolName    = "ga4.analytics.traffic"
	metadataToolName   = "ga4.property.metadata"
	trafficCapability  = "ga4.analytics.read"
	metadataCapability = "ga4.property.read"
)

// TrafficInput is the input of ga4.analytics.traffic.
type TrafficInput struct {
	Instance  string   `json:"instance" jsonschema:"configured GA4 instance id"`
	StartDate string   `json:"start_date,omitempty" jsonschema:"inclusive start date as YYYY-MM-DD or a GA4 relative form"`
	EndDate   string   `json:"end_date,omitempty" jsonschema:"inclusive end date as YYYY-MM-DD or a GA4 relative form"`
	Metrics   []string `json:"metrics" jsonschema:"traffic metrics to aggregate"`
}

// MetadataInput is the input of ga4.property.metadata.
type MetadataInput struct {
	Instance string `json:"instance" jsonschema:"configured GA4 instance id"`
}

// Register adds the read-only GA4 tools to reg for one service.
func Register(reg *registry.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return errors.New("ga4: Register needs a non-nil registry and service")
	}
	return registerTools(reg, singleLookup(svc))
}

// registerTools builds the report tools against an instance lookup (ADR-0018).
func registerTools(reg *registry.Registry, lookup serviceLookup) error {
	traffic := &mcp.Tool{
		Name:  trafficToolName,
		Title: "GA4 traffic report",
		Description: "Reads aggregate traffic from the Google Analytics 4 Data API for the configured property. " +
			"Rows are grouped by date and carry the requested metrics; period totals come from GA4 and are never summed from rows. " +
			"Read-only and aggregate-only: no user-level data is returned. Ecommerce metrics require a capability probe and are not exposed by this tool.",
		InputSchema: trafficInputSchema(),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	metadata := &mcp.Tool{
		Name:  metadataToolName,
		Title: "GA4 property metadata",
		Description: "Lists the dimension and metric API names available on the configured Google Analytics 4 property, including custom fields. " +
			"Use it to confirm a field exists before reporting. Read-only and aggregate-only; ecommerce fields still require a capability probe before their numbers are trusted.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)},
	}
	tools := []registry.Tool{
		{
			Name: traffic.Name, Title: traffic.Title, Description: traffic.Description,
			Capability: trafficCapability, Permission: registry.PermissionRead, Timeout: 60 * time.Second,
			Annotations: *traffic.Annotations,
			Register: func(server *mcp.Server) error {
				mcp.AddTool(server, traffic, func(ctx context.Context, _ *mcp.CallToolRequest, input TrafficInput) (*mcp.CallToolResult, ReportResult, error) {
					svc, err := lookup(input.Instance)
					if err != nil {
						return nil, ReportResult{}, err
					}
					result, err := svc.Traffic(ctx, TrafficQuery{StartDate: input.StartDate, EndDate: input.EndDate, Metrics: input.Metrics})
					if err != nil {
						return nil, ReportResult{}, err
					}
					return nil, *result, nil
				})
				return nil
			},
		},
		{
			Name: metadata.Name, Title: metadata.Title, Description: metadata.Description,
			Capability: metadataCapability, Permission: registry.PermissionRead, Timeout: 30 * time.Second,
			Annotations: *metadata.Annotations,
			Register: func(server *mcp.Server) error {
				mcp.AddTool(server, metadata, func(ctx context.Context, _ *mcp.CallToolRequest, input MetadataInput) (*mcp.CallToolResult, PropertyMetadata, error) {
					svc, err := lookup(input.Instance)
					if err != nil {
						return nil, PropertyMetadata{}, err
					}
					result, err := svc.Metadata(ctx)
					if err != nil {
						return nil, PropertyMetadata{}, err
					}
					return nil, *result, nil
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

// trafficInputSchema advertises the metric allowlist as an enum. The enum lives
// in an anyOf branch next to a plain string branch so an unknown name still
// reaches the handler: the handler answers with a coded validation_failed error
// instead of an opaque schema-validation error, which the tool contract needs.
func trafficInputSchema() map[string]any {
	allowed := make([]any, len(trafficMetricAllowlist))
	for i, name := range trafficMetricAllowlist {
		allowed[i] = name
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"instance": map[string]any{
				"type":        "string",
				"description": "Configured GA4 instance id, for example giztop.",
			},
			"start_date": map[string]any{
				"type":        "string",
				"description": "Inclusive start date as YYYY-MM-DD or a GA4 relative form such as 7daysAgo. Defaults to 7daysAgo.",
			},
			"end_date": map[string]any{
				"type":        "string",
				"description": "Inclusive end date as YYYY-MM-DD or a GA4 relative form such as yesterday. Defaults to yesterday.",
			},
			"metrics": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "Traffic metrics to aggregate, one value per date row. Allowed: " + strings.Join(trafficMetricAllowlist, ", ") + ".",
				"anyOf": []any{
					map[string]any{"items": map[string]any{"type": "string", "enum": allowed}},
					map[string]any{"items": map[string]any{"type": "string"}},
				},
			},
		},
		"required": []any{"instance", "metrics"},
	}
}

func boolPtr(value bool) *bool {
	return &value
}
