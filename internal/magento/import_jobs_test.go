// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
	"github.com/shuaiZend/mage2mcp/internal/security/auth"
	"github.com/shuaiZend/mage2mcp/internal/storage"
)

// testSchemaDSN returns the test database DSN scoped to a per-package schema.
//
// Database-backed test packages share one PostgreSQL instance and `go test ./...`
// runs packages in parallel, so an unscoped DSN lets another package's cleanup
// (internal/storage deletes rows from `jobs`) destroy this package's fixtures
// mid-test. A per-package search_path removes that interference without
// serialising the suite.
func testSchemaDSN(t *testing.T, schema string) string {
	t.Helper()
	base := os.Getenv("MAGE2MCP_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("MAGE2MCP_TEST_DATABASE_URL is not set; skipping PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to create test schema: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("create test schema %q: %v", schema, err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("close schema-setup connection: %v", err)
	}
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse MAGE2MCP_TEST_DATABASE_URL: %v", err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type fakeImportQueue struct {
	tasks    []jobs.Task
	canceled string
	record   *jobs.JobRecord
	// ownerInstance and ownerSubject, when set, model the scoped queue: a call
	// for any other instance or subject is not_found, exactly like storage.
	ownerInstance string
	ownerSubject  string
	lastInstance  string
	lastSubject   string
}

func (f *fakeImportQueue) owned(instanceID, subject string) bool {
	if f.ownerInstance == "" && f.ownerSubject == "" {
		return true
	}
	return instanceID == f.ownerInstance && subject == f.ownerSubject
}

func (f *fakeImportQueue) Enqueue(_ context.Context, t jobs.Task) (string, error) {
	f.tasks = append(f.tasks, t)
	return "11111111-1111-1111-1111-111111111111", nil
}

func (f *fakeImportQueue) Cancel(_ context.Context, id, instanceID, subject string) error {
	f.lastInstance, f.lastSubject = instanceID, subject
	if !f.owned(instanceID, subject) {
		return errs.New(errs.CodeNotFound, "job not found")
	}
	f.canceled = id
	return nil
}

func (f *fakeImportQueue) GetJob(_ context.Context, _, instanceID, subject string) (*jobs.JobRecord, error) {
	f.lastInstance, f.lastSubject = instanceID, subject
	if !f.owned(instanceID, subject) {
		return nil, errs.New(errs.CodeNotFound, "job not found")
	}
	return f.record, nil
}

func newImportTestSession(t *testing.T, queue ImportQueue) *mcp.ClientSession {
	t.Helper()
	resolver, err := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New()
	if err := RegisterImportJobs(reg, resolver, queue); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestImportStartEnqueuesJob(t *testing.T) {
	queue := &fakeImportQueue{}
	session := newImportTestSession(t, queue)
	csv := "sku,name,type_id,price,attribute_set_id\nNEW-1,New,simple,9.99,4\n"
	args := map[string]any{
		"instance": "store-us", "format": "csv",
		"data_base64":     base64.StdEncoding.EncodeToString([]byte(csv)),
		"idempotency_key": "batch-42",
	}
	ctx := context.Background()
	result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importStartToolName, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool error: %+v", result.Content)
	}
	if len(queue.tasks) != 1 || queue.tasks[0].Kind != jobs.TypeMagentoImport || queue.tasks[0].Queue != jobs.QueueImport {
		t.Fatalf("tasks = %+v", queue.tasks)
	}
	var payload ImportJobPayload
	if err := json.Unmarshal(queue.tasks[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Instance != "store-us" || payload.Format != "csv" || payload.UpdateExisting {
		t.Fatalf("payload = %+v", payload)
	}
	if queue.tasks[0].InstanceID != "store-us" || queue.tasks[0].IdempotencyKey != "batch-42" {
		t.Fatalf("task identity = %+v", queue.tasks[0])
	}
	if queue.tasks[0].MaxAttempts != 2 {
		t.Fatalf("max attempts = %d, want 2", queue.tasks[0].MaxAttempts)
	}
}

func TestImportStartRejectsBadPayload(t *testing.T) {
	queue := &fakeImportQueue{}
	session := newImportTestSession(t, queue)
	ctx := context.Background()
	for _, args := range []map[string]any{
		{"instance": "store-us", "format": "csv", "data_base64": "not-base64!"},
		{"instance": "store-us", "format": "csv", "data_base64": base64.StdEncoding.EncodeToString([]byte("sku,name\n"))},
		{"instance": "missing", "format": "csv", "data_base64": base64.StdEncoding.EncodeToString([]byte("sku,name\nX,Y\n"))},
	} {
		result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importStartToolName, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("payload %v was accepted", args)
		}
	}
	if len(queue.tasks) != 0 {
		t.Fatalf("bad payloads were enqueued: %+v", queue.tasks)
	}
}

func TestImportStatusAndCancel(t *testing.T) {
	queue := &fakeImportQueue{record: &jobs.JobRecord{
		ID: "11111111-1111-1111-1111-111111111111", Kind: jobs.TypeMagentoImport,
		State: "succeeded", Attempts: 1, MaxAttempts: 1, Result: `{"total":1}`,
	}}
	session := newImportTestSession(t, queue)
	ctx := context.Background()
	result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("status error: %+v", result.Content)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(raw), `"state":"succeeded"`) {
		t.Fatalf("status payload = %s", raw)
	}
	result, err = callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importCancelToolName, Arguments: map[string]any{
		"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("cancel error: %+v", result.Content)
	}
	if queue.canceled != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("canceled = %q", queue.canceled)
	}
}

func TestImportResumeEnqueuesFailedRows(t *testing.T) {
	csv := "sku,name,type_id,price,attribute_set_id\nNEW-1,New,simple,9.99,4\n"
	payload, _ := json.Marshal(ImportJobPayload{
		Instance: "store-us", Format: "csv",
		DataBase64: base64.StdEncoding.EncodeToString([]byte(csv)),
	})
	queue := &fakeImportQueue{record: &jobs.JobRecord{
		ID: "11111111-1111-1111-1111-111111111111", Kind: jobs.TypeMagentoImport,
		State: "succeeded", Payload: payload,
		Result: `{"total":1,"created":0,"failed":1,"failures":[{"row_number":2,"sku":"NEW-1","action":"create","error":"boom"}]}`,
	}}
	session := newImportTestSession(t, queue)
	result, err := callToolApproved(t, context.Background(), session, &mcp.CallToolParams{Name: importResumeToolName,
		Arguments: map[string]any{"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("resume error: %+v", result.Content)
	}
	if len(queue.tasks) != 1 {
		t.Fatalf("tasks = %+v", queue.tasks)
	}
	task := queue.tasks[0]
	if task.IdempotencyKey != "resume:11111111-1111-1111-1111-111111111111" || task.InstanceID != "store-us" {
		t.Fatalf("task = %+v", task)
	}
	var resumed ImportJobPayload
	if err := json.Unmarshal(task.Payload, &resumed); err != nil {
		t.Fatal(err)
	}
	if len(resumed.OnlyRows) != 1 || resumed.OnlyRows[0] != 2 {
		t.Fatalf("payload = %+v", resumed)
	}
}

func TestImportResumeWithoutFailures(t *testing.T) {
	queue := &fakeImportQueue{record: &jobs.JobRecord{
		ID: "11111111-1111-1111-1111-111111111111", Kind: jobs.TypeMagentoImport,
		State: "succeeded", Result: `{"total":1,"created":1,"failed":0}`,
	}}
	session := newImportTestSession(t, queue)
	result, err := callToolApproved(t, context.Background(), session, &mcp.CallToolParams{Name: importResumeToolName,
		Arguments: map[string]any{"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("resume without failures must fail")
	}
}

func TestRegisterImportJobsValidation(t *testing.T) {
	resolver, _ := NewResolver(map[string]*Service{"a": testService(t, "https://shop.example.com")})
	if err := RegisterImportJobs(nil, resolver, &fakeImportQueue{}); err == nil {
		t.Fatal("nil registry accepted")
	}
	if err := RegisterImportJobs(registry.New(), nil, &fakeImportQueue{}); err == nil {
		t.Fatal("nil resolver accepted")
	}
	if err := RegisterImportJobs(registry.New(), resolver, nil); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("nil queue err = %v", err)
	}
}

type fakeBatchReader struct {
	batch *jobs.ImportBatch
}

func (f *fakeBatchReader) GetImportBatchByJob(context.Context, string, string, string) (*jobs.ImportBatch, error) {
	return f.batch, nil
}

func (f *fakeBatchReader) GetImportBatch(context.Context, string, string, string) (*jobs.ImportBatch, error) {
	return f.batch, nil
}

func (f *fakeBatchReader) ListImportBatches(context.Context, string, int) ([]jobs.ImportBatch, error) {
	if f.batch == nil {
		return nil, nil
	}
	return []jobs.ImportBatch{*f.batch}, nil
}

func TestImportResumeUsesPersistedFailures(t *testing.T) {
	payload, _ := json.Marshal(ImportJobPayload{Instance: "store-us", Format: "csv", DataBase64: base64.StdEncoding.EncodeToString([]byte("sku,name\nA,B\n"))})
	queue := &fakeImportQueue{record: &jobs.JobRecord{ID: "11111111-1111-1111-1111-111111111111", Kind: jobs.TypeMagentoImport, Payload: payload}}
	batches := &fakeBatchReader{batch: &jobs.ImportBatch{Failures: []jobs.ImportBatchFailure{
		{RowNumber: 5, SKU: "A"}, {RowNumber: 9, SKU: "B"},
	}}}
	resolver, _ := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	reg := registry.New()
	if err := RegisterImportJobs(reg, resolver, queue, batches); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importResumeToolName,
		Arguments: map[string]any{"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("resume error: %+v", result.Content)
	}
	var resumed ImportJobPayload
	if err := json.Unmarshal(queue.tasks[0].Payload, &resumed); err != nil {
		t.Fatal(err)
	}
	if len(resumed.OnlyRows) != 2 || resumed.OnlyRows[0] != 5 || resumed.OnlyRows[1] != 9 {
		t.Fatalf("payload = %+v", resumed)
	}
}

func TestImportBatchTools(t *testing.T) {
	batch := &jobs.ImportBatch{
		ID: "22222222-2222-2222-2222-222222222222", InstanceID: "store-us", State: "partial",
		Total: 3, Created: 2, Failed: 1,
		Failures: []jobs.ImportBatchFailure{{RowNumber: 4, SKU: "X", Action: "create", Error: "boom"}},
	}
	queue := &fakeImportQueue{}
	batches := &fakeBatchReader{batch: batch}
	resolver, _ := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	reg := registry.New()
	if err := RegisterImportJobs(reg, resolver, queue, batches); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	list, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importBatchListToolName,
		Arguments: map[string]any{"instance": "store-us"}})
	if err != nil || list.IsError {
		t.Fatalf("list = %+v %v", list, err)
	}
	raw, _ := json.Marshal(list.StructuredContent)
	if !strings.Contains(string(raw), `"state":"partial"`) {
		t.Fatalf("list payload = %s", raw)
	}

	get, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importBatchGetToolName,
		Arguments: map[string]any{"instance": "store-us", "batch_id": batch.ID}})
	if err != nil || get.IsError {
		t.Fatalf("get = %+v %v", get, err)
	}
	raw, _ = json.Marshal(get.StructuredContent)
	if !strings.Contains(string(raw), `"sku":"X"`) {
		t.Fatalf("get payload = %s", raw)
	}
}

func TestImportBatchToolErrors(t *testing.T) {
	queue := &fakeImportQueue{}
	resolver, _ := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	reg := registry.New()
	// No batch reader: the read tools must report a configuration error.
	if err := RegisterImportJobs(reg, resolver, queue); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{importBatchListToolName, map[string]any{"instance": "store-us"}},
		{importBatchGetToolName, map[string]any{"instance": "store-us", "batch_id": "22222222-2222-2222-2222-222222222222"}},
	} {
		result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError {
			t.Fatalf("%s without a store must fail", call.name)
		}
	}
}

func TestImportBatchNotFound(t *testing.T) {
	queue := &fakeImportQueue{}
	batches := &fakeBatchReader{}
	resolver, _ := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	reg := registry.New()
	if err := RegisterImportJobs(reg, resolver, queue, batches); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = serverSession.Close() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importBatchGetToolName,
		Arguments: map[string]any{"instance": "store-us", "batch_id": "22222222-2222-2222-2222-222222222222"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("missing batch must be an error")
	}
}

// newImportTestSessionAs builds a session whose tool calls carry the given
// verified identity, so ownership can be exercised end to end.
func newImportTestSessionAs(t *testing.T, resolver *Resolver, queue ImportQueue, batches ImportBatchReader, subject string) *mcp.ClientSession {
	t.Helper()
	reg := registry.New()
	var err error
	if batches == nil {
		err = RegisterImportJobs(reg, resolver, queue)
	} else {
		err = RegisterImportJobs(reg, resolver, queue, batches)
	}
	if err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: subject, Class: auth.ClassHuman})
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancel)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolErrorBody(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	return string(raw)
}

// TestImportJobToolsRequireInstance proves every job tool rejects a call that
// does not name the instance, so tenancy can never be inferred from the job id.
func TestImportJobToolsRequireInstance(t *testing.T) {
	const jobID = "11111111-1111-1111-1111-111111111111"
	resolver, err := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	session := newImportTestSessionAs(t, resolver, &fakeImportQueue{}, &fakeBatchReader{}, "agent-1")
	ctx := context.Background()
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{importStatusToolName, map[string]any{"job_id": jobID}},
		{importCancelToolName, map[string]any{"job_id": jobID}},
		{importResumeToolName, map[string]any{"job_id": jobID}},
		{importBatchGetToolName, map[string]any{"batch_id": jobID}},
	} {
		result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if !result.IsError {
			t.Fatalf("%s accepted a call without instance", call.name)
		}
		if body := toolErrorBody(t, result); !strings.Contains(body, "invalid_argument") {
			t.Fatalf("%s missing-instance body = %s", call.name, body)
		}
	}
	// An unknown instance is rejected before any queue access.
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{importStatusToolName, map[string]any{"instance": "store-zz", "job_id": jobID}},
		{importCancelToolName, map[string]any{"instance": "store-zz", "job_id": jobID}},
		{importResumeToolName, map[string]any{"instance": "store-zz", "job_id": jobID}},
		{importBatchGetToolName, map[string]any{"instance": "store-zz", "batch_id": jobID}},
	} {
		result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if !result.IsError {
			t.Fatalf("%s accepted an unknown instance", call.name)
		}
		if body := toolErrorBody(t, result); !strings.Contains(body, "not_found") {
			t.Fatalf("%s unknown-instance body = %s", call.name, body)
		}
	}
}

// TestImportToolsThreadInstanceAndSubject proves the tools pass the verified
// caller and the named instance to the queue rather than a constant.
func TestImportToolsThreadInstanceAndSubject(t *testing.T) {
	queue := &fakeImportQueue{
		record:        &jobs.JobRecord{ID: "11111111-1111-1111-1111-111111111111", State: "succeeded"},
		ownerInstance: "store-us",
		ownerSubject:  "agent-1",
	}
	resolver, err := NewResolver(map[string]*Service{"store-us": testService(t, "https://shop.example.com")})
	if err != nil {
		t.Fatal(err)
	}
	session := newImportTestSessionAs(t, resolver, queue, &fakeBatchReader{}, "agent-1")
	ctx := context.Background()
	if result, err := callToolApproved(t, ctx, session, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111",
	}}); err != nil || result.IsError {
		t.Fatalf("status = %+v %v", result, err)
	}
	if queue.lastInstance != "store-us" || queue.lastSubject != "agent-1" {
		t.Fatalf("queue saw instance=%q subject=%q, want store-us/agent-1", queue.lastInstance, queue.lastSubject)
	}
	// A different caller is rejected by the queue's ownership check and the tool
	// surfaces not_found, not permission_denied.
	other := newImportTestSessionAs(t, resolver, queue, &fakeBatchReader{}, "agent-2")
	result, err := callToolApproved(t, ctx, other, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-us", "job_id": "11111111-1111-1111-1111-111111111111",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("another caller read a job it does not own")
	}
	if body := toolErrorBody(t, result); !strings.Contains(body, "not_found") {
		t.Fatalf("foreign-job body = %s, want not_found", body)
	}
}

// TestImportJobsCrossTenantIsolation is the SEC-02 regression test against the
// real queue: a caller on instance A cannot read, cancel, or resume a job owned
// by another tenant or another subject, and a resume is attributed to the
// resuming caller on the verified instance.
func TestImportJobsCrossTenantIsolation(t *testing.T) {
	url := testSchemaDSN(t, "test_magento")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := storage.Open(ctx, storage.Config{URL: url, MaxConns: 4})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	queue := storage.NewJobQueue(store)
	resolver, err := NewResolver(map[string]*Service{
		"store-a": testService(t, "https://a.example.com"),
		"store-b": testService(t, "https://b.example.com"),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := newImportTestSessionAs(t, resolver, queue, store, "agent-a")
	other := newImportTestSessionAs(t, resolver, queue, store, "agent-b")

	csv := "sku,name,type_id,price,attribute_set_id\nSEC-1,Secret,simple,9.99,4\n"
	start, err := callToolApproved(t, ctx, owner, &mcp.CallToolParams{Name: importStartToolName, Arguments: map[string]any{
		"instance": "store-a", "format": "csv", "data_base64": base64.StdEncoding.EncodeToString([]byte(csv)),
	}})
	if err != nil || start.IsError {
		t.Fatalf("start = %+v %v", start, err)
	}
	var started importStartOutput
	if err := json.Unmarshal([]byte(toolErrorBody(t, start)), &started); err != nil {
		t.Fatalf("decode start: %v", err)
	}
	if started.JobID == "" {
		t.Fatalf("no job id in %s", toolErrorBody(t, start))
	}

	assertNotFound := func(t *testing.T, label string, result *mcp.CallToolResult) {
		t.Helper()
		if !result.IsError {
			t.Fatalf("%s was allowed", label)
		}
		if body := toolErrorBody(t, result); !strings.Contains(body, "not_found") {
			t.Fatalf("%s body = %s, want not_found", label, body)
		}
	}

	// Ownership is a hash of the real subject, not a constant: a second job on the
	// same instance enqueued for another subject is invisible to the first.
	otherJobID, err := queue.Enqueue(ctx, jobs.Task{
		Kind: jobs.TypeMagentoImport, Queue: jobs.QueueImport, InstanceID: "store-a",
		Subject: "agent-b", Payload: []byte(`{"instance":"store-a","format":"csv"}`), MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("enqueue for agent-b: %v", err)
	}
	if _, err := queue.GetJob(ctx, otherJobID, "store-a", "agent-b"); err != nil {
		t.Fatalf("agent-b cannot read its own job: %v", err)
	}
	if _, err := queue.GetJob(ctx, otherJobID, "store-a", "agent-a"); err == nil {
		t.Fatal("jobs for different subjects share one subject_hash")
	}

	// The owner can read its own job.
	read, err := callToolApproved(t, ctx, owner, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-a", "job_id": started.JobID,
	}})
	if err != nil || read.IsError {
		t.Fatalf("owner status = %+v %v", read, err)
	}

	// Same caller, wrong instance.
	read, err = callToolApproved(t, ctx, owner, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-b", "job_id": started.JobID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound(t, "status across instances", read)

	// Different caller, same instance.
	read, err = callToolApproved(t, ctx, other, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"instance": "store-a", "job_id": started.JobID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound(t, "status across subjects", read)

	// A foreign caller cannot cancel, and the job is untouched.
	cancelRes, err := callToolApproved(t, ctx, other, &mcp.CallToolParams{Name: importCancelToolName, Arguments: map[string]any{
		"instance": "store-a", "job_id": started.JobID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound(t, "cancel across subjects", cancelRes)
	record, err := queue.GetJob(ctx, started.JobID, "store-a", "agent-a")
	if err != nil {
		t.Fatalf("owner GetJob: %v", err)
	}
	if record.State != "queued" {
		t.Fatalf("cross-tenant cancel changed the job state to %q", record.State)
	}

	// A foreign caller cannot resume.
	resumeRes, err := callToolApproved(t, ctx, other, &mcp.CallToolParams{Name: importResumeToolName, Arguments: map[string]any{
		"instance": "store-a", "job_id": started.JobID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound(t, "resume across subjects", resumeRes)

	// Give the job a failed row so the owner's resume has work to do.
	batchID, err := store.CreateImportBatch(ctx, "store-a", started.JobID, "csv", []byte(csv))
	if err != nil {
		t.Fatalf("CreateImportBatch: %v", err)
	}
	if err := store.FinishImportBatch(ctx, batchID, &jobs.ImportBatchResult{
		Total: 1, Processed: 1, Failed: 1,
		Failures: []jobs.ImportBatchFailure{{RowNumber: 2, SKU: "SEC-1", Action: "create", Error: "boom"}},
	}, "partial", ""); err != nil {
		t.Fatalf("FinishImportBatch: %v", err)
	}

	// A foreign caller cannot read the batch either.
	batchRes, err := callToolApproved(t, ctx, other, &mcp.CallToolParams{Name: importBatchGetToolName, Arguments: map[string]any{
		"instance": "store-a", "batch_id": batchID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertNotFound(t, "batch across subjects", batchRes)

	// The owner can read the batch.
	batchRes, err = callToolApproved(t, ctx, owner, &mcp.CallToolParams{Name: importBatchGetToolName, Arguments: map[string]any{
		"instance": "store-a", "batch_id": batchID,
	}})
	if err != nil || batchRes.IsError {
		t.Fatalf("owner batch = %+v %v", batchRes, err)
	}

	// The owner resumes: the new job must be attributed to the resuming caller
	// on the verified instance.
	resumeRes, err = callToolApproved(t, ctx, owner, &mcp.CallToolParams{Name: importResumeToolName, Arguments: map[string]any{
		"instance": "store-a", "job_id": started.JobID,
	}})
	if err != nil || resumeRes.IsError {
		t.Fatalf("owner resume = %+v %v", resumeRes, err)
	}
	var resumed importResumeOutput
	if err := json.Unmarshal([]byte(toolErrorBody(t, resumeRes)), &resumed); err != nil {
		t.Fatalf("decode resume: %v", err)
	}
	if resumed.JobID == "" || resumed.JobID == started.JobID {
		t.Fatalf("resume did not create a new job: %+v", resumed)
	}
	resumedRecord, err := queue.GetJob(ctx, resumed.JobID, "store-a", "agent-a")
	if err != nil {
		t.Fatalf("resumed job is not attributed to the resuming caller on store-a: %v", err)
	}
	if resumedRecord.InstanceID != "store-a" {
		t.Fatalf("resumed job instance = %q, want store-a", resumedRecord.InstanceID)
	}
	if _, err := queue.GetJob(ctx, resumed.JobID, "store-a", "agent-b"); err == nil {
		t.Fatal("resumed job is visible to another subject")
	}
}
