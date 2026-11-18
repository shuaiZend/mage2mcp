// SPDX-License-Identifier: Apache-2.0

package unified

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	compareToolName        = "unified.traffic.compare"
	compareToolTitle       = "Compare Cloudflare traffic with GA4 sessions"
	compareToolDescription = "Compares Cloudflare HTTP requests with GA4 sessions for a declared binding and UTC window. Reports each source separately, the requests-per-session ratio when sessions exist, and quality flags. Requests are not sessions: the ratio reflects asset and bot mix, not audience size."
	compareCapability      = "unified.traffic.read"
	compareTimeout         = 60 * time.Second
)

type compareInput struct {
	Binding string `json:"binding"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

type compareOutput struct {
	Comparison *Comparison `json:"comparison"`
}

// Register adds the unified analytics tools to reg.
func Register(reg *registry.Registry, svc *Service) error {
	if reg == nil || svc == nil {
		return errs.New(errs.CodeInternal, "unified service and registry are required")
	}
	tool := registry.Tool{
		Name: compareToolName, Title: compareToolTitle, Description: compareToolDescription,
		Capability: compareCapability, Permission: registry.PermissionRead, Timeout: compareTimeout,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}
	tool.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{
			Name: compareToolName, Title: compareToolTitle, Description: compareToolDescription,
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolPtr(false)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in compareInput) (*mcp.CallToolResult, compareOutput, error) {
			start, err := time.Parse(time.RFC3339, in.Start)
			if err != nil {
				return nil, compareOutput{}, errs.New(errs.CodeInvalidArgument, "start must be RFC3339").WithDetail("field", "start")
			}
			end, err := time.Parse(time.RFC3339, in.End)
			if err != nil {
				return nil, compareOutput{}, errs.New(errs.CodeInvalidArgument, "end must be RFC3339").WithDetail("field", "end")
			}
			comparison, err := svc.Compare(ctx, in.Binding, start, end)
			if err != nil {
				return nil, compareOutput{}, err
			}
			return nil, compareOutput{Comparison: comparison}, nil
		})
		return nil
	}
	return reg.Register(tool)
}

func boolPtr(v bool) *bool { return &v }
