// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	trafficToolName     = "cloudflare.analytics.traffic"
	securityToolName    = "cloudflare.security.summary"
	eventsToolName      = "cloudflare.security.events"
	trafficTitle        = "Cloudflare traffic analytics"
	securityTitle       = "Cloudflare security summary"
	eventsTitle         = "Cloudflare security events"
	toolTimeout         = 60 * time.Second
	defaultTrafficLimit = 100

	trafficDescription  = "Read-only. Queries the Cloudflare Analytics GraphQL dataset httpRequests1hGroups for the configured zone and aggregates hourly requests, bytes, and cached bytes by hostname, country, or status. The dataset is pre-aggregated and not adaptively sampled, and the current hour can be incomplete; treat values as estimates. The module never writes to Cloudflare."
	eventsDescription   = "Read-only. Returns individual Cloudflare Security Events from firewallEventsAdaptive for the configured zone over a window of at most 30 days, newest first, bounded by limit. Results are adaptive-sampled with 31-day retention; client IPs are never returned. The module never writes to Cloudflare."
	securityDescription = "Read-only. Summarizes Cloudflare Security Events from the GraphQL dataset firewallEventsAdaptiveGroups by action over a window of at most 30 days. Counts are adaptive-sampled event counts, not request counts, with 31-day retention; disclose the sampling when reporting. The module never writes to Cloudflare."

	trafficInputSchemaJSON = `{
  "type": "object", "additionalProperties": false, "required": ["instance", "since", "until", "group_by"],
  "properties": {
    "instance": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$", "description": "Configured Cloudflare instance alias; the service is bound to one zone, so it does not select credentials."},
    "since": {"type": "string", "description": "Inclusive window start as an RFC 3339 timestamp in UTC."},
    "until": {"type": "string", "description": "Exclusive window end as an RFC 3339 timestamp in UTC."},
    "group_by": {"type": "string", "enum": ["hostname", "country", "status"], "description": "Dimension to aggregate hourly traffic by."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 1000, "default": 100, "description": "Maximum number of aggregate rows to return."}
  }
}`
	eventsInputSchemaJSON = `{
  "type": "object", "additionalProperties": false, "required": ["instance", "since", "until"],
  "properties": {
    "instance": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$", "description": "Configured Cloudflare instance alias; the service is bound to one zone."},
    "since": {"type": "string", "description": "Inclusive window start as an RFC 3339 timestamp in UTC."},
    "until": {"type": "string", "description": "Exclusive window end as an RFC 3339 timestamp in UTC."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 1000, "default": 100}
  }
}`
	securityInputSchemaJSON = `{
  "type": "object", "additionalProperties": false, "required": ["instance", "since", "until"],
  "properties": {
    "instance": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$", "description": "Configured Cloudflare instance alias; the service is bound to one zone, so it does not select credentials."},
    "since": {"type": "string", "description": "Inclusive window start as an RFC 3339 timestamp in UTC."},
    "until": {"type": "string", "description": "Exclusive window end as an RFC 3339 timestamp in UTC."}
  }
}`
)

// TrafficInput is the input of cloudflare.analytics.traffic.
type TrafficInput struct {
	// Instance is the configured instance alias; it does not select credentials.
	Instance string `json:"instance"`
	Since    string `json:"since"`
	Until    string `json:"until"`
	// GroupBy is one of "hostname", "country", or "status".
	GroupBy string `json:"group_by"`
	// Limit caps the aggregate rows; zero means the schema default of 100.
	Limit int `json:"limit,omitempty"`
}

// SecurityInput is the input of cloudflare.security.summary.
type SecurityInput struct {
	// Instance is the configured instance alias; it does not select credentials.
	Instance string `json:"instance"`
	Since    string `json:"since"`
	Until    string `json:"until"`
}

// SecurityEventsInput is the input of cloudflare.security.events.
type SecurityEventsInput struct {
	Instance string `json:"instance"`
	Since    string `json:"since"`
	Until    string `json:"until"`
	Limit    int    `json:"limit,omitempty"`
}

// SecurityEventsOutput is a bounded page of sampled security events.
type SecurityEventsOutput struct {
	Events        []SecurityEvent `json:"events"`
	Sampled       bool            `json:"sampled"`
	RetentionDays int             `json:"retention_days"`
	Truncated     bool            `json:"truncated"`
}

// Quality carries data-quality caveats attached to an analytics result.
type Quality struct {
	// Sampled reports whether the upstream dataset is adaptively sampled.
	Sampled bool `json:"sampled"`
	// Notes are short, agent-facing caveats about how to read the result.
	Notes []string `json:"notes,omitempty"`
}

// TrafficOutput is the structured result of cloudflare.analytics.traffic.
type TrafficOutput struct {
	Rows []TrafficRow `json:"rows"`
	// Sampled is false: httpRequests1hGroups is pre-aggregated, not adaptive.
	Sampled bool `json:"sampled"`
	// Quality repeats the sampling flag and adds phrasing caveats.
	Quality Quality `json:"quality"`
}

// SecurityOutput is the structured result of cloudflare.security.summary.
type SecurityOutput struct {
	// Actions maps a Cloudflare action value to its event count.
	Actions map[string]int `json:"actions"`
	// Total is the sum of Actions; events, not requests.
	Total int `json:"total"`
	// Sampled reports that the adaptive dataset is sampled.
	Sampled bool `json:"sampled"`
	// RetentionDays is the documented Security Events retention.
	RetentionDays int `json:"retention_days"`
}

// Register adds the read-only Cloudflare analytics and security tools to reg.
// A nil registry or service fails fast rather than registering a partial set.
func Register(reg *registry.Registry, svc *Service) error {
	if reg == nil || svc == nil || svc.client == nil {
		return errors.New("cloudflare: registry and service are required")
	}
	tools := []registry.Tool{
		newReadTool(trafficToolName, trafficTitle, trafficDescription, "cloudflare.analytics.read", func(srv *mcp.Server) error {
			addReadTool(srv, trafficToolName, trafficTitle, trafficDescription, trafficInputSchemaJSON,
				func(ctx context.Context, _ *mcp.CallToolRequest, in TrafficInput) (*mcp.CallToolResult, TrafficOutput, error) {
					return handleTraffic(ctx, svc, in)
				})
			return nil
		}),
		newReadTool(eventsToolName, eventsTitle, eventsDescription, "cloudflare.security.read", func(srv *mcp.Server) error {
			addReadTool(srv, eventsToolName, eventsTitle, eventsDescription, eventsInputSchemaJSON,
				func(ctx context.Context, _ *mcp.CallToolRequest, in SecurityEventsInput) (*mcp.CallToolResult, SecurityEventsOutput, error) {
					return handleSecurityEvents(ctx, svc, in)
				})
			return nil
		}),
		newReadTool(securityToolName, securityTitle, securityDescription, "cloudflare.security.read", func(srv *mcp.Server) error {
			addReadTool(srv, securityToolName, securityTitle, securityDescription, securityInputSchemaJSON,
				func(ctx context.Context, _ *mcp.CallToolRequest, in SecurityInput) (*mcp.CallToolResult, SecurityOutput, error) {
					return handleSecurity(ctx, svc, in)
				})
			return nil
		}),
	}
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func newReadTool(name, title, description, capability string, register registry.RegisterFunc) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: description, Capability: capability,
		Permission: registry.PermissionRead, Timeout: toolTimeout,
		Annotations: *readAnnotations(), Register: register,
	}
}

func addReadTool[In, Out any](srv *mcp.Server, name, title, description, schema string, handler mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(schema), Annotations: readAnnotations(),
	}, handler)
}

func readAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)}
}

func handleTraffic(ctx context.Context, svc *Service, in TrafficInput) (*mcp.CallToolResult, TrafficOutput, error) {
	since, until, err := parseWindow(trafficToolName, in.Since, in.Until)
	if err != nil {
		return nil, TrafficOutput{}, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultTrafficLimit
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	rows, err := svc.Traffic(ctx, TrafficQuery{Since: since, Until: until, GroupBy: in.GroupBy, Limit: limit})
	if err != nil {
		return nil, TrafficOutput{}, err
	}
	if rows == nil {
		rows = []TrafficRow{}
	}
	return nil, TrafficOutput{
		Rows: rows,
		Quality: Quality{
			Notes: []string{"httpRequests1hGroups is an hourly aggregate; the current hour may be incomplete."},
		},
	}, nil
}

func handleSecurity(ctx context.Context, svc *Service, in SecurityInput) (*mcp.CallToolResult, SecurityOutput, error) {
	since, until, err := parseWindow(securityToolName, in.Since, in.Until)
	if err != nil {
		return nil, SecurityOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	summary, err := svc.Summary(ctx, since, until)
	if err != nil {
		return nil, SecurityOutput{}, err
	}
	return nil, SecurityOutput{
		Actions:       summary.Actions,
		Total:         summary.Total,
		Sampled:       summary.Sampled,
		RetentionDays: summary.RetentionDays,
	}, nil
}

func parseWindow(op, since, until string) (time.Time, time.Time, error) {
	start, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return time.Time{}, time.Time{}, errs.New(errs.CodeInvalidArgument, "since must be an RFC 3339 timestamp").WithOp(op)
	}
	end, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return time.Time{}, time.Time{}, errs.New(errs.CodeInvalidArgument, "until must be an RFC 3339 timestamp").WithOp(op)
	}
	return start, end, nil
}

func boolPtr(v bool) *bool { return &v }

func handleSecurityEvents(ctx context.Context, svc *Service, in SecurityEventsInput) (*mcp.CallToolResult, SecurityEventsOutput, error) {
	since, until, err := parseWindow(eventsToolName, in.Since, in.Until)
	if err != nil {
		return nil, SecurityEventsOutput{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()
	page, err := svc.Events(ctx, since, until, in.Limit)
	if err != nil {
		return nil, SecurityEventsOutput{}, err
	}
	return nil, SecurityEventsOutput{
		Events: page.Events, Sampled: page.Sampled,
		RetentionDays: page.RetentionDays, Truncated: page.Truncated,
	}, nil
}
