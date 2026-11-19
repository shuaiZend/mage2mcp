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

	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

func testImportService(t *testing.T) *Service {
	t.Helper()
	client, err := NewClient(ClientConfig{BaseURL: "https://shop.example.com", StoreCode: "default", Signer: &BearerSigner{Token: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(client)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestValidateImportCSV(t *testing.T) {
	svc := testImportService(t)
	data := []byte("sku,name,type_id,price,color\nA-1,Alpha,simple,9.99,red\nA-2,Beta,virtual,10,blue\n")
	preview, err := svc.ValidateImport(context.Background(), "csv", data)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Total != 2 || preview.Valid != 2 || preview.Invalid != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	if len(preview.Sample) != 2 || preview.Sample[0].Attributes["color"] != "red" {
		t.Fatalf("sample = %+v", preview.Sample)
	}
}

func TestValidateImportCSVReportsRowErrors(t *testing.T) {
	svc := testImportService(t)
	data := []byte("sku,name,type_id,price\nA-1,Alpha,simple,9.99\nA-1,Duplicate,simple,1\n,Bad,weird,-1\n")
	preview, err := svc.ValidateImport(context.Background(), "csv", data)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid != 1 || preview.Invalid != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	if len(preview.Errors) < 3 {
		t.Fatalf("errors = %+v", preview.Errors)
	}
}

func TestValidateImportJSON(t *testing.T) {
	svc := testImportService(t)
	data := []byte(`[{"sku":"B-1","name":"Beta","type_id":"simple","price":"5.5","attributes":{"size":"m"}}]`)
	preview, err := svc.ValidateImport(context.Background(), "json", data)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid != 1 || preview.Sample[0].Attributes["size"] != "m" {
		t.Fatalf("preview = %+v", preview)
	}
}

func TestValidateImportRejectsUnsupportedFormatAndOversize(t *testing.T) {
	svc := testImportService(t)
	if _, err := svc.ValidateImport(context.Background(), "xlsx", []byte("x")); err == nil {
		t.Fatal("unsupported format accepted")
	}
	if _, err := svc.ValidateImport(context.Background(), "csv", make([]byte, maxImportBytes+1)); err == nil {
		t.Fatal("oversized payload accepted")
	}
	if _, err := svc.ValidateImport(context.Background(), "csv", nil); err == nil {
		t.Fatal("empty payload accepted")
	}
}

func TestDecodeImportPayload(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("sku,name\nA-1,Alpha\n"))
	if _, err := DecodeImportPayload(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeImportPayload("not-base64!!"); err == nil {
		t.Fatal("invalid base64 accepted")
	}
}

func TestImportValidateToolThroughMCP(t *testing.T) {
	svc := testImportService(t)
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	for _, tool := range reg.List() {
		if err := tool.Register(srv); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	csvData := "sku,name,type_id,price\nA-1,Alpha,simple,9.99\n"
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: importValidateToolName, Arguments: map[string]any{
		"instance":    "store-us",
		"format":      "csv",
		"data_base64": base64.StdEncoding.EncodeToString([]byte(csvData)),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	if !strings.Contains(strings.ToLower(mustJSON(t, res.StructuredContent)), "preview") {
		t.Fatal("preview missing from structured content")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestExecuteImportCreatesUpdatesAndReportsPartialFailure(t *testing.T) {
	var created, updated int
	server := newImportFake(t, &created, &updated)
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,name,type_id,price,attribute_set_id\nNEW-1,New product,simple,9.99,4\nOLD-1,Updated name,simple,1.00,4\nFAIL-1,Boom,simple,2.00,0\n")
	result, err := svc.ExecuteImport(context.Background(), "csv", data, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 3 || result.Created != 1 || result.Updated != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v", result)
	}
	if created != 1 || updated != 1 {
		t.Fatalf("upstream calls: created=%d updated=%d", created, updated)
	}
}

func TestExecuteImportSkipsExistingWhenNotUpdating(t *testing.T) {
	var created, updated int
	server := newImportFake(t, &created, &updated)
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)
	result, err := svc.ExecuteImport(context.Background(), "csv", []byte("sku,name\nOLD-1,Name\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Updated != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecuteImportRejectsInvalidRows(t *testing.T) {
	svc := testImportService(t)
	if _, err := svc.ExecuteImport(context.Background(), "csv", []byte("sku,name\n,missing\n"), false); err == nil {
		t.Fatal("invalid rows must be rejected before execution")
	}
}

func TestCompactImportReport(t *testing.T) {
	result := &ImportResult{Total: 3, Created: 1, Failed: 2, Rows: []ImportResultRow{
		{RowNumber: 2, SKU: "A", Action: "create", Status: "succeeded"},
		{RowNumber: 3, SKU: "B", Action: "create", Status: "failed", Error: "boom"},
		{RowNumber: 4, SKU: "C", Action: "update", Status: "failed", Error: "boom"},
	}}
	report := CompactImportReport(result)
	if report.Total != 3 || report.Created != 1 || report.Failed != 2 || len(report.Failures) != 2 {
		t.Fatalf("report = %+v", report)
	}
	if report.Failures[0].SKU != "B" || report.FailuresTruncated {
		t.Fatalf("failures = %+v", report)
	}
	big := &ImportResult{Total: maxReportFailures + 5, Failed: maxReportFailures + 5}
	for i := 0; i < maxReportFailures+5; i++ {
		big.Rows = append(big.Rows, ImportResultRow{RowNumber: i + 2, SKU: "S", Status: "failed", Error: "x"})
	}
	capped := CompactImportReport(big)
	if len(capped.Failures) != maxReportFailures || !capped.FailuresTruncated {
		t.Fatalf("capped failures = %d truncated=%v", len(capped.Failures), capped.FailuresTruncated)
	}
}
