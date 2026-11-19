// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/jobs"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
)

type fakeImportQueue struct {
	tasks    []jobs.Task
	canceled string
	record   *jobs.JobRecord
}

func (f *fakeImportQueue) Enqueue(_ context.Context, t jobs.Task) (string, error) {
	f.tasks = append(f.tasks, t)
	return "11111111-1111-1111-1111-111111111111", nil
}

func (f *fakeImportQueue) Cancel(_ context.Context, id string) error {
	f.canceled = id
	return nil
}

func (f *fakeImportQueue) GetJob(_ context.Context, _ string) (*jobs.JobRecord, error) {
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
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg})
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
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: importStartToolName, Arguments: args})
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
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: importStartToolName, Arguments: args})
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
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: importStatusToolName, Arguments: map[string]any{
		"job_id": "11111111-1111-1111-1111-111111111111",
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
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: importCancelToolName, Arguments: map[string]any{
		"job_id": "11111111-1111-1111-1111-111111111111",
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
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: importResumeToolName,
		Arguments: map[string]any{"job_id": "11111111-1111-1111-1111-111111111111"}})
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
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: importResumeToolName,
		Arguments: map[string]any{"job_id": "11111111-1111-1111-1111-111111111111"}})
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
