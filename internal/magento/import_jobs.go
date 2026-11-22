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
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Format         string `json:"format" jsonschema:"Payload encoding: csv or json."`
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
		newImportStatusTool(resolver, queue, batches),
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
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Format         string `json:"format" jsonschema:"Payload encoding: csv or json."`
	Data           string `json:"data_base64"`
	UpdateExisting bool   `json:"update_existing"`
	IdempotencyKey string `json:"idempotency_key"`
}

type importStartOutput struct {
	JobID  string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
	Rows   int    `json:"rows" jsonschema:"Number of rows processed."`
	Queued bool   `json:"queued" jsonschema:"True when the work is queued rather than executed inline. False when the idempotency key returned a job that had already finished, so nothing new was queued."`
	// Replayed distinguishes "I queued new work" from "I got the job I asked for
	// before". Reporting queued:true for a job that already finished makes the
	// caller poll for a run that will never start again (U-67).
	Replayed bool   `json:"replayed" jsonschema:"True when the idempotency key matched an existing job that had already left the queue, so this call queued nothing."`
	State    string `json:"state,omitempty" jsonschema:"State of the returned job when it already existed: queued, running, succeeded, partial, failed or canceled."`
}

type importStatusInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	JobID    string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
}

type importStatusOutput struct {
	Job jobs.JobRecord `json:"job" jsonschema:"The import job."`
	// Batch is the persisted business outcome for this job. It is present when
	// the batch store is configured and a batch exists; job.state is derived
	// from it as well as from the job result, so the two tools cannot disagree.
	Batch *jobs.ImportBatch `json:"batch,omitempty" jsonschema:"The persisted import batch for this job, with its counts and failed rows."`
	// Report is the job result already parsed, so the caller does not have to
	// decode job.result a second time (U-68).
	Report *ImportReport `json:"report,omitempty" jsonschema:"The parsed per-row import report: totals, counts and the bounded failure list."`
}

type importCancelInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	JobID    string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
}

type importCancelOutput struct {
	JobID    string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
	Canceled bool   `json:"canceled" jsonschema:"True when the order is now canceled."`
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
				out := importStartOutput{JobID: jobID, Rows: rows, Queued: true}
				// A repeated idempotency key returns the original job id, and the
				// queue reports it the same way as a fresh insert. A job that is
				// already terminal therefore cannot be the one this call queued:
				// say so instead of telling the caller to wait for it.
				if in.IdempotencyKey != "" {
					if record, getErr := queue.GetJob(ctx, jobID, in.Instance, callerSubject(ctx)); getErr == nil && record != nil {
						out.State = record.State
						if isTerminalJobState(record.State) {
							out.Replayed, out.Queued = true, false
						}
					}
				}
				return nil, out, nil
			})
		return nil
	}
	return t
}

// newImportStatusTool builds import.status. The batch reader is optional so the
// read-only registration path (writes disabled, tools.go) keeps compiling; when
// it is present, job state is cross-checked against the persisted batch.
func newImportStatusTool(resolver *Resolver, queue ImportQueue, batchReaders ...ImportBatchReader) registry.Tool {
	var batches ImportBatchReader
	if len(batchReaders) > 0 {
		batches = batchReaders[0]
	}
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
				reported := *record
				var batch *jobs.ImportBatch
				if batches != nil {
					// The batch is the persisted business outcome. The job result is
					// written separately and can be lost (lease lost on a retry, a
					// progress write that never got a final result), which used to
					// leave status reporting "succeeded" for a batch that says
					// "partial" — two tools disagreeing about the same import.
					if got, batchErr := batches.GetImportBatchByJob(ctx, in.JobID, in.Instance, callerSubject(ctx)); batchErr == nil {
						batch = got
					}
				}
				// The queue records execution; a partial import did execute, but
				// reporting "succeeded" next to a "partial" batch tells the caller
				// the import was fully applied when it was not.
				if reported.State == jobs.StateSucceeded &&
					(jobs.ResultIsPartial(reported.Result) || (batch != nil && batch.State == jobs.StatePartial)) {
					reported.State = jobs.StatePartial
				}
				return nil, importStatusOutput{Job: reported, Batch: batch, Report: parseImportReport(reported.Result)}, nil
			})
		return nil
	}
	return t
}

// isTerminalJobState reports whether a job has left the queue for good, so a
// returned job id cannot belong to the call that just enqueued.
func isTerminalJobState(state string) bool {
	switch state {
	case jobs.StateSucceeded, jobs.StatePartial, jobs.StateFailed, jobs.StateCanceled:
		return true
	}
	return false
}

// parseImportReport decodes a stored job result into the report the caller needs,
// so it does not have to parse job.result itself (U-68). A result that carries
// only progress markers, or that is not an import report, yields nil: an
// all-zero report would read as "nothing imported".
func parseImportReport(result string) *ImportReport {
	if result == "" {
		return nil
	}
	var report ImportReport
	if err := json.Unmarshal([]byte(result), &report); err != nil {
		return nil
	}
	if report.Total == 0 && report.Created == 0 && report.Updated == 0 && report.Skipped == 0 && report.Failed == 0 {
		return nil
	}
	return &report
}

func newImportCancelTool(resolver *Resolver, queue ImportQueue) registry.Tool {
	t := writeToolMetadata(importCancelToolName, importCancelToolTitle, importCancelToolDescription, "magento.import.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(importCancelToolName, importCancelToolTitle, importCancelToolDescription, importCancelSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importCancelInput) (*mcp.CallToolResult, importCancelOutput, error) {
				if _, err := resolver.Resolve(in.Instance); err != nil {
					return nil, importCancelOutput{}, err
				}
				// A job that is already canceled has nothing to cancel: returning
				// success makes the caller believe it just stopped work that had
				// already stopped. The queue rejects the other terminal states
				// itself; only "canceled" used to fall through as a success.
				if record, err := queue.GetJob(ctx, in.JobID, in.Instance, callerSubject(ctx)); err == nil &&
					record != nil && record.State == jobs.StateCanceled {
					return nil, importCancelOutput{}, errs.New(errs.CodeIllegalTransition,
						"job is already canceled").
						WithDetail("job_id", in.JobID).WithDetail("state", record.State).
						WithCause(jobs.ErrNotCancelable)
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

	// GG-10: an agent that does not know which columns are required, or what one bad
	// row does, sends a payload it cannot repair. Both facts belong here because they
	// decide the shape of the file, not the semantics of a single field.
	importStartToolDescription = "Queues a bounded CSV/JSON product import (8 MiB, 5000 rows, base64 within the 16 MiB body limit) after validation. " +
		"Returns a job id; poll import.status. " +
		"REQUIRED COLUMNS: sku on every row; a row that creates a product also needs name, attribute_set_id and type_id (simple|virtual). An update row may omit them: a sku,price file is a valid bulk reprice. Other columns become attributes. " +
		"ONLY sku IS CHECKED BEFORE THE JOB EXISTS: a bad sku rejects the whole payload (validation_failed, details.rows naming every bad row). A create row missing one of them still returns a job id and fails per row at execution: reconcile with partial:true and failures[]. Execution failures are per-row, not retried. " +
		"Write operation: requires confirmation and a configured database queue. " +
		"COPYING: the payload is flat, so it only expresses simple products; see the product_copy prompt. "
	importStatusToolDescription = "Reads the state, attempts, error code, and result summary of an asynchronous import job on the named instance. Only the caller that started the job can read it. Read-only."
	importCancelToolDescription = "Cancels a queued or running asynchronous import job on the named instance. Only the caller that started the job can cancel it. Requires confirmation."

	jobIDPattern = `^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`

	// importStartSchema bounds data_base64 to base64.EncodedLen(maxImportBytes)
	// (11184812 = 4*ceil(8 MiB/3)) so the schema, the decoder, and the HTTP body
	// cap agree; TestImportLimitsAgreeAcrossPackages fails if they drift.
	// instance, idempotency_key and confirmation_id reuse the shared write-tool
	// properties: a second wording for the same field is how "resolved by the
	// server" survived in one corner of the surface (GG-12/15). update_existing
	// carries the rule that decides whether an existing SKU is written or skipped.
	importStartSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + idempotencyKeyProp + `, ` + confirmationProp + `,
		"format": {"type": "string", "enum": ["csv", "json"], "description": "Payload encoding: csv with a header row, or a json array of objects."},
		"data_base64": {"type": "string", "maxLength": 11184812, "description": "The import payload, base64 encoded. At most 8 MiB decoded."},
		"update_existing": {"type": "boolean", "default": false, "description": "Update the products whose SKU already exists instead of skipping them. Default false: an existing SKU is skipped and reported as skipped, and only new SKUs are created."}
	}, "required": ["instance", "format", "data_base64"], "additionalProperties": false}`

	importStatusSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `", "description": "Import job id returned by import.start or import.execute."}
	}, "required": ["instance", "job_id"], "additionalProperties": false}`

	importCancelSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `", "description": "Import job id returned by import.start or import.execute."},
		` + confirmationProp + `
	}, "required": ["instance", "job_id"], "additionalProperties": false}`
)

const (
	importResumeToolName  = "magento.import.resume"
	importResumeToolTitle = "Resume failed import rows"

	importResumeToolDescription = "Queues a new asynchronous import on the named instance containing only the failed rows of a previous job " +
		"(from its bounded report). Only the caller that started the original job can resume it, and the new job runs on the same instance. Requires confirmation and a configured database queue; repeated resumes of the same job return the same new job."
	importResumeSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."},
		"job_id": {"type": "string", "pattern": "` + jobIDPattern + `", "description": "Import job id returned by import.start or import.execute."},
		` + confirmationProp + `
	}, "required": ["instance", "job_id"], "additionalProperties": false}`
)

type importResumeInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	JobID    string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
}

type importResumeOutput struct {
	JobID string `json:"job_id" jsonschema:"Import job id, used by import.status and import.batch."`
	Rows  int    `json:"rows" jsonschema:"Number of rows processed."`
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
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."},
		"limit": {"type": "integer", "minimum": 1, "maximum": 100, "default": 20}
	}, "required": ["instance"], "additionalProperties": false}`

	importBatchGetSchema = `{"type": "object", "properties": {
		"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."},
		"batch_id": {"type": "string", "pattern": "` + jobIDPattern + `", "description": "Import batch id, as returned in import.batches[].id. It is not the job id; use the job id with import.status."}
	}, "required": ["instance", "batch_id"], "additionalProperties": false}`
)

type importBatchListInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Limit    int    `json:"limit"`
}

type importBatchListOutput struct {
	Batches []jobs.ImportBatch `json:"batches" jsonschema:"Import batches, newest first."`
}

type importBatchGetInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	BatchID  string `json:"batch_id"`
}

type importBatchGetOutput struct {
	Batch *jobs.ImportBatch `json:"batch" jsonschema:"The import batch."`
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
