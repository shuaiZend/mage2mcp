// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
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
	// OnlyRows limits execution to the given row numbers (import resume).
	OnlyRows []int `json:"only_rows,omitempty"`
}

// ImportQueue is the queue surface the import tools need.
//
// Every read or cancel is scoped to the caller's instance and subject: a job
// belonging to another tenant or another caller must be reported as not_found,
// never as permission_denied, so the response cannot confirm that a job exists.
type ImportQueue interface {
	Enqueue(ctx context.Context, t jobs.Task) (string, error)
	Cancel(ctx context.Context, id, instanceID, subject string) error
	GetJob(ctx context.Context, id, instanceID, subject string) (*jobs.JobRecord, error)
}

// ImportBatchReader looks up the persisted batch for a job, scoped to the
// caller's instance and subject in the same way as ImportQueue.
type ImportBatchReader interface {
	GetImportBatchByJob(ctx context.Context, jobID, instanceID, subject string) (*jobs.ImportBatch, error)
	GetImportBatch(ctx context.Context, id, instanceID, subject string) (*jobs.ImportBatch, error)
	ListImportBatches(ctx context.Context, instanceID string, limit int) ([]jobs.ImportBatch, error)
}

// callerSubject returns the verified subject the job operations are attributed
// to. In production the auth middleware guarantees an identity; "anonymous" is
// reachable only when authentication is disabled for loopback development.
func callerSubject(ctx context.Context) string {
	if id, ok := auth.IdentityFromContext(ctx); ok && id.Subject != "" {
		return id.Subject
	}
	return "anonymous"
}

// RegisterImportJobs registers the asynchronous import tools. It requires a
// durable queue, so callers register these only when the database is configured.
// A batch reader is optional; without one, resume falls back to the job's
// bounded failure report.
func RegisterImportJobs(reg *registry.Registry, resolver *Resolver, queue ImportQueue, batchReaders ...ImportBatchReader) error {
	if reg == nil || resolver == nil || queue == nil {
		return errs.New(errs.CodeInternal, "import tools require a registry, resolver, and queue")
	}
	var batches ImportBatchReader
	if len(batchReaders) > 0 {
		batches = batchReaders[0]
	}
	for _, tool := range []registry.Tool{
		newImportStartTool(resolver, queue),
		newImportStatusTool(resolver, queue),
		newImportCancelTool(resolver, queue),
		newImportResumeTool(resolver, queue, batches),
		newImportBatchListTool(resolver, batches),
		newImportBatchGetTool(resolver, batches),
	} {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	for name, schema := range map[string]string{
		importStartToolName:     importStartSchema,
		importStatusToolName:    importStatusSchema,
		importCancelToolName:    importCancelSchema,
		importResumeToolName:    importResumeSchema,
		importBatchListToolName: importBatchListSchema,
		importBatchGetToolName:  importBatchGetSchema,
	} {
		if err := reg.SetInputValidator(name, schema); err != nil {
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
	IdempotencyKey string `json:"idempotency_key"`
}

type importStartOutput struct {
	JobID  string `json:"job_id"`
	Rows   int    `json:"rows"`
	Queued bool   `json:"queued"`
}

type importStatusInput struct {
	Instance string `json:"instance"`
	JobID    string `json:"job_id"`
}

type importStatusOutput struct {
	Job jobs.JobRecord `json:"job"`
}

type importCancelInput struct {
	Instance string `json:"instance"`
	JobID    string `json:"job_id"`
}

type importCancelOutput struct {
	JobID    string `json:"job_id"`
	Canceled bool   `json:"canceled"`
}

func newImportStartTool(resolver *Resolver, queue ImportQueue) registry.Tool {
	t := writeToolMetadata(importStartToolName, importStartToolTitle, importStartToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(importStartToolName, importStartToolTitle, importStartToolDescription, importStartSchema),
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
					Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport,
					InstanceID: in.Instance, Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
					// Retries only cover failures before or during execution (for
					// example an upstream outage); per-row failures are reported,
					// not retried. Re-running a batch is safe because creates skip
					// existing SKUs and updates are set operations.
					Payload: payload, MaxAttempts: 2,
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

func newImportStatusTool(resolver *Resolver, queue ImportQueue) registry.Tool {
	t := toolMetadata(importStatusToolName, importStatusToolTitle, importStatusToolDescription)
	t.Capability = "magento.import.read"
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpTool(importStatusToolName, importStatusToolTitle, importStatusToolDescription, importStatusSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importStatusInput) (*mcp.CallToolResult, importStatusOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importStatusOutput{}, err
				}
				record, err := queue.GetJob(ctx, in.JobID, in.Instance, callerSubject(ctx))
				if err != nil {
					return nil, importStatusOutput{}, err
				}
				return nil, importStatusOutput{Job: *record}, nil
			})
		return nil
	}
	return t
}

func newImportCancelTool(resolver *Resolver, queue ImportQueue) registry.Tool {
	t := writeToolMetadata(importCancelToolName, importCancelToolTitle, importCancelToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(importCancelToolName, importCancelToolTitle, importCancelToolDescription, importCancelSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importCancelInput) (*mcp.CallToolResult, importCancelOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importCancelOutput{}, err
				}
				if err := queue.Cancel(ctx, in.JobID, in.Instance, callerSubject(ctx)); err != nil {
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
		"(at most 8 MiB decoded and 5000 rows, and small enough that its base64 form fits the server body limit, 16 MiB by default) after structural validation. Returns a job id; poll import.status for progress and the partial-success report. " +
		"Write operation: requires confirmation and a configured database queue."
	importStatusToolDescription = "Reads the state, attempts, error code, and result summary of an asynchronous import job on the named instance. Only the caller that started the job can read it. Read-only."
	importCancelToolDescription = "Cancels a queued or running asynchronous import job on the named instance. Only the caller that started the job can cancel it. Requires confirmation."

	jobIDPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

	// importStartSchema bounds data_base64 to base64.EncodedLen(maxImportBytes)
	// (11184812 = 4*ceil(8 MiB/3)) so the schema, the decoder, and the HTTP body
	// cap agree; TestImportLimitsAgreeAcrossPackages fails if they drift.
	importStartSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id the import runs against."},
		"format": {"type": "string", "enum": ["csv", "json"], "description": "Payload encoding: csv with a header row, or a json array of objects."},
		"data_base64": {"type": "string", "maxLength": 11184812, "description": "The import payload, base64 encoded. At most 8 MiB decoded."},
		"update_existing": {"type": "boolean", "default": false},
		"idempotency_key": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$", "description": "Optional deduplication key; a repeated key returns the original job id."},
		"confirmation_id": {"type": "string", "maxLength": 128}
	}, "required": ["instance", "format", "data_base64"], "additionalProperties": false}`

	importStatusSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `"}
	}, "required": ["instance", "job_id"], "additionalProperties": false}`

	importCancelSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `"},
		"confirmation_id": {"type": "string", "maxLength": 128}
	}, "required": ["instance", "job_id"], "additionalProperties": false}`
)

const (
	importResumeToolName  = "magento.import.resume"
	importResumeToolTitle = "Resume failed import rows"

	importResumeToolDescription = "Queues a new asynchronous import on the named instance containing only the failed rows of a previous job " +
		"(from its bounded report). Only the caller that started the original job can resume it, and the new job runs on the same instance. Requires confirmation and a configured database queue; repeated resumes of the same job return the same new job."
	importResumeSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `"},
		"confirmation_id": {"type": "string", "maxLength": 128}
	}, "required": ["instance", "job_id"], "additionalProperties": false}`
)

type importResumeInput struct {
	Instance string `json:"instance"`
	JobID    string `json:"job_id"`
}

type importResumeOutput struct {
	JobID string `json:"job_id"`
	Rows  int    `json:"rows"`
}

func newImportResumeTool(resolver *Resolver, queue ImportQueue, batches ImportBatchReader) registry.Tool {
	t := writeToolMetadata(importResumeToolName, importResumeToolTitle, importResumeToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(importResumeToolName, importResumeToolTitle, importResumeToolDescription, importResumeSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importResumeInput) (*mcp.CallToolResult, importResumeOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importResumeOutput{}, err
				}
				subject := callerSubject(ctx)
				record, err := queue.GetJob(ctx, in.JobID, in.Instance, subject)
				if err != nil {
					return nil, importResumeOutput{}, err
				}
				var payload ImportJobPayload
				if err := json.Unmarshal(record.Payload, &payload); err != nil {
					return nil, importResumeOutput{}, errs.New(errs.CodeInvalidArgument, "job is not an import job")
				}
				// The job row's instance already matched the request, so a payload
				// naming a different instance is corrupt; resuming it would write to
				// a tenant the caller never named.
				if payload.Instance != "" && payload.Instance != in.Instance {
					return nil, importResumeOutput{}, errs.New(errs.CodeNotFound, "job not found")
				}
				payload.Instance = in.Instance
				rows := make([]int, 0)
				if batches != nil {
					if batch, err := batches.GetImportBatchByJob(ctx, in.JobID, in.Instance, subject); err == nil && batch != nil {
						for _, failure := range batch.Failures {
							rows = append(rows, failure.RowNumber)
						}
					}
				}
				if len(rows) == 0 {
					var report ImportReport
					if record.Result != "" {
						if err := json.Unmarshal([]byte(record.Result), &report); err != nil {
							return nil, importResumeOutput{}, errs.New(errs.CodeInvalidArgument, "job has no resumable report")
						}
					}
					for _, failure := range report.Failures {
						rows = append(rows, failure.RowNumber)
					}
				}
				if len(rows) == 0 {
					return nil, importResumeOutput{}, errs.New(errs.CodeInvalidArgument, "job has no failed rows to resume")
				}
				payload.OnlyRows = rows
				raw, err := json.Marshal(payload)
				if err != nil {
					return nil, importResumeOutput{}, errs.New(errs.CodeInternal, "resume payload could not be encoded")
				}
				jobID, err := queue.Enqueue(ctx, jobs.Task{
					Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport,
					InstanceID: in.Instance, Subject: subject,
					IdempotencyKey: "resume:" + in.JobID,
					Payload:        raw, MaxAttempts: 2,
				})
				if err != nil {
					return nil, importResumeOutput{}, err
				}
				return nil, importResumeOutput{JobID: jobID, Rows: len(rows)}, nil
			})
		return nil
	}
	return t
}

const (
	importBatchListToolName        = "magento.import.batches"
	importBatchListToolTitle       = "List import batches"
	importBatchListToolDescription = "Lists recent asynchronous import batches for the configured instance " +
		"(newest first, at most 100) with state, counts, and error code. Read-only."
	importBatchGetToolName        = "magento.import.batch"
	importBatchGetToolTitle       = "Get an import batch"
	importBatchGetToolDescription = "Returns one asynchronous import batch by id on the named instance with its counts and every failed row. Only the caller that started the owning job can read it. Read-only."

	importBatchListSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20}
	}, "required": ["instance"], "additionalProperties": false}`

	importBatchGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `"},
		"batch_id": {"type": "string", "pattern": "` + jobIDPattern + `"}
	}, "required": ["instance", "batch_id"], "additionalProperties": false}`
)

type importBatchListInput struct {
	Instance string `json:"instance"`
	Limit    int    `json:"limit"`
}

type importBatchListOutput struct {
	Batches []jobs.ImportBatch `json:"batches"`
}

type importBatchGetInput struct {
	Instance string `json:"instance"`
	BatchID  string `json:"batch_id"`
}

type importBatchGetOutput struct {
	Batch *jobs.ImportBatch `json:"batch"`
}

func newImportBatchListTool(resolver *Resolver, batches ImportBatchReader) registry.Tool {
	t := toolMetadata(importBatchListToolName, importBatchListToolTitle, importBatchListToolDescription)
	t.Capability = "magento.import.read"
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpTool(importBatchListToolName, importBatchListToolTitle, importBatchListToolDescription, importBatchListSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importBatchListInput) (*mcp.CallToolResult, importBatchListOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importBatchListOutput{}, err
				}
				if batches == nil {
					return nil, importBatchListOutput{}, errs.New(errs.CodeInternal, "import batch store is not configured")
				}
				list, err := batches.ListImportBatches(ctx, in.Instance, in.Limit)
				if err != nil {
					return nil, importBatchListOutput{}, err
				}
				return nil, importBatchListOutput{Batches: list}, nil
			})
		return nil
	}
	return t
}

func newImportBatchGetTool(resolver *Resolver, batches ImportBatchReader) registry.Tool {
	t := toolMetadata(importBatchGetToolName, importBatchGetToolTitle, importBatchGetToolDescription)
	t.Capability = "magento.import.read"
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpTool(importBatchGetToolName, importBatchGetToolTitle, importBatchGetToolDescription, importBatchGetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importBatchGetInput) (*mcp.CallToolResult, importBatchGetOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importBatchGetOutput{}, err
				}
				if batches == nil {
					return nil, importBatchGetOutput{}, errs.New(errs.CodeInternal, "import batch store is not configured")
				}
				batch, err := batches.GetImportBatch(ctx, in.BatchID, in.Instance, callerSubject(ctx))
				if err != nil {
					return nil, importBatchGetOutput{}, err
				}
				if batch == nil {
					return nil, importBatchGetOutput{}, errs.New(errs.CodeNotFound, "import batch not found")
				}
				return nil, importBatchGetOutput{Batch: batch}, nil
			})
		return nil
	}
	return t
}
