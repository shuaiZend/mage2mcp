// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

// ImportJobPayload is the job payload for an asynchronous import.
//
// The payload carries the bounded batch; credentials are resolved by the worker
// from configuration and are never part of the job.
type ImportJobPayload struct {
	Instance       string `json:"instance"`
	Format         string `json:"format"`
	DataBase64     string `json:"data_base64"`
	UpdateExisting bool   `json:"update_existing"`
}

// ImportQueue is the queue surface the import tools need.
type ImportQueue interface {
	Enqueue(ctx context.Context, t jobs.Task) (string, error)
	Cancel(ctx context.Context, id string) error
	GetJob(ctx context.Context, id string) (*jobs.JobRecord, error)
}

// RegisterImportJobs registers the asynchronous import tools. It requires a
// durable queue, so callers register these only when the database is configured.
func RegisterImportJobs(reg *registry.Registry, resolver *Resolver, queue ImportQueue) error {
	if reg == nil || resolver == nil || queue == nil {
		return errs.New(errs.CodeInternal, "import tools require a registry, resolver, and queue")
	}
	for _, tool := range []registry.Tool{
		newImportStartTool(resolver, queue),
		newImportStatusTool(queue),
		newImportCancelTool(queue),
	} {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

type importStartInput struct {
	Instance       string `json:"instance"`
	Format         string `json:"format"`
	Data           string `json:"data_base64"`
	UpdateExisting bool   `json:"update_existing"`
}

type importStartOutput struct {
	JobID  string `json:"job_id"`
	Rows   int    `json:"rows"`
	Queued bool   `json:"queued"`
}

type importStatusInput struct {
	JobID string `json:"job_id"`
}

type importStatusOutput struct {
	Job jobs.JobRecord `json:"job"`
}

type importCancelInput struct {
	JobID string `json:"job_id"`
}

type importCancelOutput struct {
	JobID    string `json:"job_id"`
	Canceled bool   `json:"canceled"`
}

func newImportStartTool(resolver *Resolver, queue ImportQueue) registry.Tool {
	t := writeToolMetadata(importStartToolName, importStartToolTitle, importStartToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(importStartToolName, importStartToolTitle, importStartToolDescription, importStartSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importStartInput) (*mcp.CallToolResult, importStartOutput, error) {
				data, err := DecodeImportPayload(in.Data)
				if err != nil {
					return nil, importStartOutput{}, err
				}
				rows, err := ValidateImportShape(in.Format, data)
				if err != nil {
					return nil, importStartOutput{}, err
				}
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importStartOutput{}, err
				}
				payload, err := json.Marshal(ImportJobPayload{
					Instance: in.Instance, Format: in.Format, DataBase64: in.Data, UpdateExisting: in.UpdateExisting,
				})
				if err != nil {
					return nil, importStartOutput{}, errs.New(errs.CodeInternal, "import payload could not be encoded")
				}
				jobID, err := queue.Enqueue(ctx, jobs.Task{
					Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport, Payload: payload, MaxAttempts: 1,
				})
				if err != nil {
					return nil, importStartOutput{}, err
				}
				return nil, importStartOutput{JobID: jobID, Rows: rows, Queued: true}, nil
			})
		return nil
	}
	return t
}

func newImportStatusTool(queue ImportQueue) registry.Tool {
	t := toolMetadata(importStatusToolName, importStatusToolTitle, importStatusToolDescription)
	t.Capability = "magento.import.read"
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpTool(importStatusToolName, importStatusToolTitle, importStatusToolDescription, importStatusSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importStatusInput) (*mcp.CallToolResult, importStatusOutput, error) {
				record, err := queue.GetJob(ctx, in.JobID)
				if err != nil {
					return nil, importStatusOutput{}, err
				}
				return nil, importStatusOutput{Job: *record}, nil
			})
		return nil
	}
	return t
}

func newImportCancelTool(queue ImportQueue) registry.Tool {
	t := writeToolMetadata(importCancelToolName, importCancelToolTitle, importCancelToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(importCancelToolName, importCancelToolTitle, importCancelToolDescription, importCancelSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importCancelInput) (*mcp.CallToolResult, importCancelOutput, error) {
				if err := queue.Cancel(ctx, in.JobID); err != nil {
					return nil, importCancelOutput{}, err
				}
				return nil, importCancelOutput{JobID: in.JobID, Canceled: true}, nil
			})
		return nil
	}
	return t
}

const (
	importStartToolName   = "magento.import.start"
	importStartToolTitle  = "Start an asynchronous import"
	importStatusToolName  = "magento.import.status"
	importStatusToolTitle = "Get asynchronous import status"
	importCancelToolName  = "magento.import.cancel"
	importCancelToolTitle = "Cancel an asynchronous import"

	importStartToolDescription = "Queues a bounded CSV/JSON product import for the worker " +
		"(at most 8 MiB and 5000 rows) after structural validation. Returns a job id; poll import.status for progress and the partial-success report. " +
		"Write operation: requires confirmation and a configured database queue."
	importStatusToolDescription = "Reads the state, attempts, error code, and result summary of an asynchronous import job. Read-only."
	importCancelToolDescription = "Cancels a queued or running asynchronous import job. Requires confirmation."

	jobIDPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

	importStartSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"format": {"type": "string", "enum": ["csv", "json"]},
		"data_base64": {"type": "string", "maxLength": 11184812},
		"update_existing": {"type": "boolean", "default": false},
		"confirmation_id": {"type": "string", "maxLength": 128}
	}, "required": ["instance", "format", "data_base64"], "additionalProperties": false}`

	importStatusSchema = `{"type": "object", "properties": {
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `"}
	}, "required": ["job_id"], "additionalProperties": false}`

	importCancelSchema = `{"type": "object", "properties": {
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `"},
		"confirmation_id": {"type": "string", "maxLength": 128}
	}, "required": ["job_id"], "additionalProperties": false}`
)
