// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
	mcpserver "github.com/shuaiZend/mage2mcp/internal/mcp/server"
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

func TestExecuteImportProgress(t *testing.T) {
	var created, updated int
	server := newImportFake(t, &created, &updated)
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,name,type_id,price,attribute_set_id\nNEW-1,New,simple,9.99,4\nNEW-2,New2,simple,8.99,4\nOLD-1,Updated,simple,1.00,4\n")
	var progress [][2]int
	_, err := svc.ExecuteImportWithProgress(context.Background(), "csv", data, true, func(done, total int) {
		progress = append(progress, [2]int{done, total})
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0] != [2]int{0, 3} || progress[1] != [2]int{3, 3} {
		t.Fatalf("progress = %v", progress)
	}
}

func TestValidateImportSEOWarnings(t *testing.T) {
	svc := testImportService(t)
	preview, err := svc.ValidateImport(context.Background(), "csv",
		[]byte("sku,name\nSKU-1,Widget\n"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid != 1 || len(preview.Warnings) == 0 {
		t.Fatalf("preview = %+v", preview)
	}
	fields := map[string]bool{}
	for _, warning := range preview.Warnings {
		fields[warning.Field] = true
	}
	for _, want := range []string{"description", "url_key", "meta_title", "meta_description"} {
		if !fields[want] {
			t.Fatalf("missing %q warning: %+v", want, preview.Warnings)
		}
	}
	if preview.WarningsTruncated {
		t.Fatal("single row must not truncate")
	}
}

func TestValidateImportSEOWarningsComplete(t *testing.T) {
	svc := testImportService(t)
	description := strings.Repeat("copy ", 100)
	title := "Blue Widget | Example Store Official"
	metaDescription := strings.Repeat("a", 140)
	csv := "sku,name,description,url_key,meta_title,meta_description\n" +
		"SKU-1,Widget," + description + ",blue-widget," + title + "," + metaDescription + "\n"
	preview, err := svc.ValidateImport(context.Background(), "csv", []byte(csv))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Warnings) != 0 {
		t.Fatalf("warnings = %+v", preview.Warnings)
	}
}

func TestValidateImportWarningsTruncated(t *testing.T) {
	svc := testImportService(t)
	var builder strings.Builder
	builder.WriteString("sku,name\n")
	for i := 0; i < 60; i++ {
		builder.WriteString("SKU-" + strconv.Itoa(i) + ",Widget\n")
	}
	preview, err := svc.ValidateImport(context.Background(), "csv", []byte(builder.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Warnings) != maxImportWarnings || !preview.WarningsTruncated {
		t.Fatalf("warnings=%d truncated=%v", len(preview.Warnings), preview.WarningsTruncated)
	}
}

func TestExecuteImportRoutesReservedAttributes(t *testing.T) {
	var created, updated int
	server := newImportFake(t, &created, &updated)
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,name,type_id,price,attribute_set_id,description,url_key,meta_title,weight\n" +
		"NEW-1,New,simple,9.99,4," + strings.Repeat("copy ", 80) + ",new-widget,New Widget | Store,1.25\n")
	result, err := svc.ExecuteImport(context.Background(), "csv", data, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestImportReservedAttributeValidation(t *testing.T) {
	rows, err := parseImportRows("csv", []byte("sku,name,weight,special_from_date,tax_class_id\nSKU-1,Widget,heavy,01/11/2026,abc\n"))
	if err != nil {
		t.Fatal(err)
	}
	problems := validateImportRow(rows[0], map[string]int{})
	if len(problems) < 3 {
		t.Fatalf("problems = %+v", problems)
	}
}

func TestExecuteImportRowsFilters(t *testing.T) {
	var created, updated int
	server := newImportFake(t, &created, &updated)
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,name,type_id,price,attribute_set_id\nNEW-1,New,simple,9.99,4\nOLD-1,Updated,simple,1.00,4\n")
	result, err := svc.ExecuteImportRows(context.Background(), "csv", data, true, []int{3})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Updated != 1 || result.Created != 0 {
		t.Fatalf("result = %+v", result)
	}
	if created != 0 || updated != 1 {
		t.Fatalf("upstream calls: created=%d updated=%d", created, updated)
	}
}

// paddedCSV returns a parseable CSV of exactly size bytes: a valid header and row
// followed by a single-field record that parses (and fails row validation)
// without making the payload unparseable.
func paddedCSV(size int) []byte {
	head := "sku,name,type_id,attribute_set_id\nA-1,Alpha,simple,4\n"
	if size <= len(head)+1 {
		return []byte(head)
	}
	return []byte(head + strings.Repeat("x", size-len(head)-1) + "\n")
}

// TestImportLimitIsTransportConsistent pins the relationship the audit found
// broken: a payload that passes validation must also fit the HTTP body cap once
// base64-encoded and wrapped in its JSON envelope.
func TestImportLimitIsTransportConsistent(t *testing.T) {
	if !ImportFitsHTTPBody(maxImportBytes) {
		t.Fatalf("a maximum import (%d bytes) does not fit the %d-byte body cap: needs %d",
			maxImportBytes, DefaultHTTPBodyBytes, ImportHTTPBodyBytes(maxImportBytes))
	}
	envelope, err := json.Marshal(struct {
		Instance       string `json:"instance"`
		Format         string `json:"format" jsonschema:"Payload encoding: csv or json."`
		Data           string `json:"data_base64"`
		UpdateExisting bool   `json:"update_existing"`
		IdempotencyKey string `json:"idempotency_key"`
	}{
		Instance: "default", Format: "csv", UpdateExisting: true,
		IdempotencyKey: strings.Repeat("k", 64),
		Data:           base64.StdEncoding.EncodeToString(make([]byte, maxImportBytes)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope) > DefaultHTTPBodyBytes {
		t.Fatalf("a maximum import request body is %d bytes, over the %d-byte cap",
			len(envelope), DefaultHTTPBodyBytes)
	}
	t.Logf("max import request body = %d bytes of the %d-byte cap", len(envelope), DefaultHTTPBodyBytes)
}

// TestValidateImportBoundary covers the size boundary from both sides.
func TestValidateImportBoundary(t *testing.T) {
	svc := testImportService(t)
	ctx := context.Background()

	preview, err := svc.ValidateImport(ctx, "csv", paddedCSV(maxImportBytes))
	if err != nil {
		t.Fatalf("a payload of exactly the limit must be accepted: %v", err)
	}
	if preview.Total == 0 {
		t.Fatal("payload was not parsed")
	}

	_, err = svc.ValidateImport(ctx, "csv", paddedCSV(maxImportBytes+1))
	if err == nil {
		t.Fatal("a payload one byte over the limit was accepted")
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error is not *errs.Error: %v", err)
	}
	if typed.Code != errs.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", typed.Code)
	}
	if typed.Details["limit_bytes"] != strconv.Itoa(maxImportBytes) {
		t.Fatalf("limit_bytes = %q, want %d", typed.Details["limit_bytes"], maxImportBytes)
	}
	if typed.Details["observed_bytes"] != strconv.Itoa(maxImportBytes+1) {
		t.Fatalf("observed_bytes = %q, want %d", typed.Details["observed_bytes"], maxImportBytes+1)
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error does not name the limit: %v", err)
	}
}

// TestDecodeImportPayloadBoundary covers both over-limit paths: the encoded
// length fast path and the decoded length check.
func TestDecodeImportPayloadBoundary(t *testing.T) {
	atLimit := base64.StdEncoding.EncodeToString(paddedCSV(maxImportBytes))
	decoded, err := DecodeImportPayload(atLimit)
	if err != nil {
		t.Fatalf("a payload of exactly the limit must decode: %v", err)
	}
	if len(decoded) != maxImportBytes {
		t.Fatalf("decoded %d bytes, want %d", len(decoded), maxImportBytes)
	}

	for _, size := range []int{maxImportBytes + 1, maxImportBytes + 2} {
		encoded := base64.StdEncoding.EncodeToString(paddedCSV(size))
		if _, err := DecodeImportPayload(encoded); err == nil {
			t.Fatalf("encoded payload of %d decoded bytes was accepted", size)
		} else if code := errs.CodeOf(err); code != errs.CodeInvalidArgument {
			t.Fatalf("size %d: code = %s, want invalid_argument", size, code)
		}
	}
}

// TestImportLimitsAgreeAcrossPackages is the drift guard for the three limits
// that must move together: the documented import ceiling (a capability), the
// JSON-Schema maxLength that declares it, and the HTTP body cap that must admit
// it. Lowering the ceiling to make a defect go away, or raising it without
// raising the body cap, fails here.
func TestImportLimitsAgreeAcrossPackages(t *testing.T) {
	const documentedCeiling = 8 << 20
	if maxImportBytes != documentedCeiling {
		t.Fatalf("maxImportBytes = %d, want the documented ceiling %d (a capability cut is not a fix)",
			maxImportBytes, documentedCeiling)
	}
	if maxImportRows != 5000 {
		t.Fatalf("maxImportRows = %d, want 5000", maxImportRows)
	}

	encodedLimit := base64.StdEncoding.EncodedLen(maxImportBytes)
	if encodedLimit != 11184812 {
		t.Fatalf("base64(%d) = %d, want 11184812", maxImportBytes, encodedLimit)
	}
	schemas := map[string]string{
		"import.validate": importValidateSchema,
		"import.start":    importStartSchema,
		"import.execute":  importExecuteSchema,
	}
	for name, raw := range schemas {
		var schema struct {
			Properties map[string]struct {
				MaxLength int `json:"maxLength"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatalf("%s schema is not valid JSON: %v", name, err)
		}
		if got := schema.Properties["data_base64"].MaxLength; got != encodedLimit {
			t.Fatalf("%s data_base64 maxLength = %d, want %d (base64 of maxImportBytes)",
				name, got, encodedLimit)
		}
	}

	required := int64(ImportHTTPBodyBytes(maxImportBytes))
	if DefaultHTTPBodyBytes != config.DefaultMaxBodyBytes {
		t.Fatalf("magento.DefaultHTTPBodyBytes = %d, config.DefaultMaxBodyBytes = %d: the mirrored constant drifted",
			DefaultHTTPBodyBytes, config.DefaultMaxBodyBytes)
	}
	if int64(DefaultHTTPBodyBytes) < required {
		t.Fatalf("body cap %d does not admit a maximum import, which needs %d bytes",
			DefaultHTTPBodyBytes, required)
	}
	if config.MinMaxBodyBytesForImport < required {
		t.Fatalf("config.MinMaxBodyBytesForImport = %d does not admit a maximum import (%d bytes)",
			config.MinMaxBodyBytesForImport, required)
	}
	if got := config.Default().Server.MaxBodyBytes; got != config.DefaultMaxBodyBytes {
		t.Fatalf("config.Default().Server.MaxBodyBytes = %d, want %d", got, config.DefaultMaxBodyBytes)
	}
	t.Logf("import ceiling=%d base64=%d required_body=%d default_body_cap=%d headroom=%d",
		maxImportBytes, encodedLimit, required, DefaultHTTPBodyBytes,
		int64(DefaultHTTPBodyBytes)-required)
}

// TestRealisticImportPayloadPassesBothLimits proves the documented ceiling is
// reachable: a 5,000-row CSV with realistic field sizes (a 300-character
// description per row) must pass validation and fit the shipped body cap.
func TestRealisticImportPayloadPassesBothLimits(t *testing.T) {
	const rows = 5000
	description := strings.Repeat("d", 300)
	var b strings.Builder
	b.WriteString("sku,name,price,attribute_set_id,type_id,description\n")
	for i := 0; i < rows; i++ {
		b.WriteString("SKU-" + strconv.Itoa(100000+i) + ",Product " + strconv.Itoa(i) +
			",19.99,4,simple," + description + "\n")
	}
	data := []byte(b.String())
	if len(data) > maxImportBytes {
		t.Fatalf("fixture is %d bytes, over the %d-byte ceiling", len(data), maxImportBytes)
	}

	svc := testImportService(t)
	preview, err := svc.ValidateImport(context.Background(), "csv", data)
	if err != nil {
		t.Fatalf("a realistic 5,000-row import must validate: %v", err)
	}
	if preview.Total != rows || preview.Valid != rows || preview.Invalid != 0 {
		t.Fatalf("preview total=%d valid=%d invalid=%d, want %d/%d/0",
			preview.Total, preview.Valid, preview.Invalid, rows, rows)
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	body, err := json.Marshal(struct {
		Instance       string `json:"instance"`
		Format         string `json:"format" jsonschema:"Payload encoding: csv or json."`
		Data           string `json:"data_base64"`
		UpdateExisting bool   `json:"update_existing"`
		IdempotencyKey string `json:"idempotency_key"`
	}{Instance: "default", Format: "csv", Data: encoded, UpdateExisting: true,
		IdempotencyKey: strings.Repeat("k", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 11184812 {
		t.Fatalf("base64 payload is %d bytes, over the schema maxLength", len(encoded))
	}
	if int64(len(body)) > config.DefaultMaxBodyBytes {
		t.Fatalf("request body is %d bytes, over the %d-byte cap", len(body), config.DefaultMaxBodyBytes)
	}

	// Prove it at the HTTP layer too, with the shipped default cap: the handler
	// must read the whole body and hand it to the protocol layer instead of
	// answering 413.
	reg := registry.New()
	if err := Register(reg, svc); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	handler := mcpserver.Handler(srv, mcpserver.Options{
		Registry: reg, MaxBodyBytes: config.DefaultMaxBodyBytes,
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	call, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "magento.import.validate",
			"arguments": map[string]any{
				"instance": "default", "format": "csv", "data_base64": encoded,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP layer rejected the request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("the HTTP layer rejected a %d-byte import body against the %d-byte cap",
			len(call), config.DefaultMaxBodyBytes)
	}
	t.Logf("realistic import rows=%d csv_bytes=%d base64_bytes=%d request_body_bytes=%d cap=%d http_status=%d",
		rows, len(data), len(encoded), len(call), config.DefaultMaxBodyBytes, resp.StatusCode)
}

// TestHTTPBodyCapRejectsOversizeRequest proves the raised cap is still a hard
// ceiling enforced by the transport: one byte over is a prompt 413 naming the
// limit, not a hang and not a silently truncated read.
func TestHTTPBodyCapRejectsOversizeRequest(t *testing.T) {
	reg := registry.New()
	if err := Register(reg, testImportService(t)); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the shipped default rather than a toy value: the raised cap must
	// still be a hard ceiling at exactly 16 MiB.
	bodyCap := config.DefaultMaxBodyBytes
	handler := mcpserver.Handler(srv, mcpserver.Options{Registry: reg, MaxBodyBytes: bodyCap})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	post := func(t *testing.T, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed (a hang or a dropped connection is not acceptable): %v", err)
		}
		return resp
	}

	over := make([]byte, bodyCap+1)
	for i := range over {
		over[i] = 'x'
	}
	resp := post(t, over)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a body one byte over the cap", resp.StatusCode)
	}
	var message bytes.Buffer
	_, _ = message.ReadFrom(resp.Body)
	if !strings.Contains(message.String(), strconv.FormatInt(bodyCap, 10)) {
		t.Fatalf("413 body %q does not name the limit %d", message.String(), bodyCap)
	}

	under := post(t, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	defer func() { _ = under.Body.Close() }()
	if under.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatal("a body under the cap was rejected as oversized")
	}
}

// F9: a decoded payload at the 8 MiB ceiling must fit through the shipped
// 16 MiB HTTP body cap, not just a realistic 5,000-row fixture.
func TestMaxImportPayloadPassesHTTPCap(t *testing.T) {
	data := paddedCSV(maxImportBytes)
	if len(data) != maxImportBytes {
		t.Fatalf("fixture is %d bytes, want exactly %d", len(data), maxImportBytes)
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	reg := registry.New()
	if err := Register(reg, testImportService(t)); err != nil {
		t.Fatal(err)
	}
	srv, err := mcpserver.NewMCP(mcpserver.Options{Version: "test", Registry: reg, Confirm: magentoConfirmService(t)})
	if err != nil {
		t.Fatal(err)
	}
	handler := mcpserver.Handler(srv, mcpserver.Options{
		Registry: reg, MaxBodyBytes: config.DefaultMaxBodyBytes,
	})
	ts := httptest.NewServer(handler)
	defer ts.Close()

	call, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name": "magento.import.validate",
			"arguments": map[string]any{
				"instance": "default", "format": "csv", "data_base64": encoded,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/mcp", bytes.NewReader(call))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP layer rejected the maximum import body: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusRequestEntityTooLarge {
		t.Fatalf("the HTTP layer rejected a %d-byte decoded import (request %d bytes) against the %d-byte cap",
			len(data), len(call), config.DefaultMaxBodyBytes)
	}
	t.Logf("max decoded import=%d request_body=%d cap=%d status=%d", len(data), len(call), config.DefaultMaxBodyBytes, resp.StatusCode)
}

// PERF-06: existence used to be resolved with one GET per row, so an import cost
// exactly two requests per row (lookup + write). The lookup is now batched, so a
// 10-row import costs one list request plus ten writes. This test counts the wire
// calls rather than trusting the shape of the code.
func TestExecuteImportBatchesExistenceLookups(t *testing.T) {
	var lists, perRowGets, writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/products"):
			lists++
			// Rows 5..9 already exist; rows 0..4 do not.
			_, _ = io.WriteString(w, `{"items":[{"sku":"OLD-5"},{"sku":"OLD-6"},{"sku":"OLD-7"},{"sku":"OLD-8"},{"sku":"OLD-9"}]}`)
		case r.Method == http.MethodGet:
			perRowGets++
			http.NotFound(w, r)
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			writes++
			_, _ = io.WriteString(w, `{"id":1,"sku":"X","type_id":"simple"}`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)

	var csv strings.Builder
	csv.WriteString("sku,name,type_id,price,attribute_set_id\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&csv, "NEW-%d,New %d,simple,9.99,4\n", i, i)
	}
	for i := 5; i < 10; i++ {
		fmt.Fprintf(&csv, "OLD-%d,Old %d,simple,9.99,4\n", i, i)
	}
	result, err := svc.ExecuteImport(context.Background(), "csv", []byte(csv.String()), true)
	if err != nil {
		t.Fatalf("ExecuteImport: %v", err)
	}
	if result.Total != 10 || result.Created != 5 || result.Updated != 5 {
		t.Fatalf("result = %+v, want 5 created and 5 updated", result)
	}
	if lists != 1 {
		t.Fatalf("list requests = %d, want the existence check batched into 1", lists)
	}
	if perRowGets != 0 {
		t.Fatalf("per-row GETs = %d, want 0 once the batch answered", perRowGets)
	}
	total := lists + perRowGets + writes
	if total > 11 {
		t.Fatalf("requests = %d for 10 rows, want <= 11 (was 20 before the batch)", total)
	}
}

// A store that cannot answer the batched form must not turn every row into a
// spurious create: the per-row read stays the fallback.
func TestExecuteImportFallsBackWhenBatchLookupFails(t *testing.T) {
	var perRowGets int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/products"):
			http.Error(w, "unsupported", http.StatusBadRequest)
		case r.Method == http.MethodGet:
			perRowGets++
			_, _ = io.WriteString(w, `{"id":1,"sku":"OLD-1","type_id":"simple"}`)
		case r.Method == http.MethodPut:
			_, _ = io.WriteString(w, `{"id":1,"sku":"OLD-1","type_id":"simple"}`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	svc := newImportServiceAgainst(t, server.URL)

	result, err := svc.ExecuteImport(context.Background(), "csv",
		[]byte("sku,name,type_id,price,attribute_set_id\nOLD-1,Old,simple,9.99,4\n"), true)
	if err != nil {
		t.Fatalf("ExecuteImport: %v", err)
	}
	if result.Updated != 1 || result.Created != 0 {
		t.Fatalf("result = %+v, want the row updated through the fallback read", result)
	}
	if perRowGets != 1 {
		t.Fatalf("per-row GETs = %d, want 1 fallback read", perRowGets)
	}
}

// captureImportFake records every product write so a test can assert the exact
// payload the import built. GET answers 200 for the SKUs in existing and 404 for
// everything else, which is what decides create versus update.
type captureImportFake struct {
	existing map[string]bool
	fail     map[string]string
	writes   []capturedProductWrite
}

type capturedProductWrite struct {
	method string
	sku    string
	body   map[string]any
}

func newCaptureImportFake(t *testing.T, existing map[string]bool, fail map[string]string) (*httptest.Server, *captureImportFake) {
	t.Helper()
	fake := &captureImportFake{existing: existing, fail: fail}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sku := ""
		if parts := strings.Split(r.URL.Path, "/products/"); len(parts) == 2 {
			sku = parts[1]
		}
		if r.Method == http.MethodGet {
			if fake.existing[sku] {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":1,"sku":"` + sku + `","name":"Existing","type_id":"simple"}`))
				return
			}
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode %s %s body: %v", r.Method, r.URL.Path, err)
		}
		if sku == "" {
			// A create posts to /products, so the SKU is only in the body.
			if product, ok := body["product"].(map[string]any); ok {
				sku, _ = product["sku"].(string)
			}
		}
		fake.writes = append(fake.writes, capturedProductWrite{method: r.Method, sku: sku, body: body})
		if msg := fake.fail[sku]; msg != "" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(msg))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":1,"sku":"` + sku + `","name":"Written","type_id":"simple"}`))
	}))
	t.Cleanup(server.Close)
	return server, fake
}

// productBody returns the decoded "product" object of the last write to sku.
func (f *captureImportFake) productBody(t *testing.T, sku string) map[string]any {
	t.Helper()
	for i := len(f.writes) - 1; i >= 0; i-- {
		if f.writes[i].sku != sku {
			continue
		}
		product, ok := f.writes[i].body["product"].(map[string]any)
		if !ok {
			t.Fatalf("write to %s has no product object: %+v", sku, f.writes[i].body)
		}
		return product
	}
	t.Fatalf("no write to %s; writes = %+v", sku, f.writes)
	return nil
}

// customAttributesOf flattens custom_attributes into code -> value.
func customAttributesOf(t *testing.T, product map[string]any) map[string]string {
	t.Helper()
	out := map[string]string{}
	raw, _ := product["custom_attributes"].([]any)
	for _, entry := range raw {
		attribute, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("custom attribute is not an object: %+v", entry)
		}
		code, _ := attribute["attribute_code"].(string)
		value, _ := attribute["value"].(string)
		out[code] = value
	}
	return out
}

// U-01 (P0) reached the import through Update(): the pricing slice started at
// weight, so every value landed one slot early and a weight-only import row
// rewrote the selling price. This is the import-level guard: it fails if the
// update payload ever shifts the pricing attributes again.
func TestImportUpdateKeepsPricingAttributesInPlace(t *testing.T) {
	server, fake := newCaptureImportFake(t, map[string]bool{"OLD-1": true}, nil)
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,price,special_price,weight\nOLD-1,45.00,39.99,1.25\n")
	result, err := svc.ExecuteImport(context.Background(), "csv", data, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	product := fake.productBody(t, "OLD-1")
	if product["price"] != "45.00" {
		t.Fatalf("price = %v, want 45.00", product["price"])
	}
	if product["weight"] != "1.25" {
		t.Fatalf("weight = %v, want 1.25 (static field, not a custom attribute)", product["weight"])
	}
	attributes := customAttributesOf(t, product)
	if got := attributes["special_price"]; got != "39.99" {
		t.Fatalf("special_price = %q, want 39.99; custom_attributes = %+v", got, attributes)
	}
	// The misalignment signature: weight occupied the special_price slot, so these
	// codes appeared with values that belonged to another field.
	for _, shifted := range []string{"special_from_date", "special_to_date", "cost", "tax_class_id"} {
		if value, ok := attributes[shifted]; ok {
			t.Fatalf("%s = %q was written by a row that never named it: %+v", shifted, value, attributes)
		}
	}
	if value, ok := attributes["weight"]; ok {
		t.Fatalf("weight = %q was sent as a custom attribute; Magento ignores it there", value)
	}
	if len(attributes) != 1 {
		t.Fatalf("custom_attributes = %+v, want only special_price", attributes)
	}
}

// U-65: a repricing file carries no name column. The row is an update, so
// validation must accept it and the update must not blank the stored name.
func TestImportAcceptsPriceOnlyUpdateRow(t *testing.T) {
	preview, err := testImportService(t).ValidateImport(context.Background(), "csv", []byte("sku,price\nOLD-1,39.99\n"))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Valid != 1 || preview.Invalid != 0 || len(preview.Errors) != 0 {
		t.Fatalf("preview = %+v, want one valid row", preview)
	}
	warned := false
	for _, warning := range preview.Warnings {
		if warning.Field == "name" {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %+v, want a name warning", preview.Warnings)
	}

	server, fake := newCaptureImportFake(t, map[string]bool{"OLD-1": true}, nil)
	svc := newImportServiceAgainst(t, server.URL)
	result, err := svc.ExecuteImport(context.Background(), "csv", []byte("sku,price\nOLD-1,39.99\n"), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v", result)
	}
	product := fake.productBody(t, "OLD-1")
	if name, ok := product["name"]; ok {
		t.Fatalf("name = %v was sent by a row without one; Magento would store an empty name", name)
	}
	if product["price"] != "39.99" {
		t.Fatalf("price = %v, want 39.99", product["price"])
	}
}

// A row without a name is only invalid when it turns out to create: the failure
// is per row, and the rest of the batch still runs.
func TestImportCreateWithoutNameFailsOnlyThatRow(t *testing.T) {
	server, fake := newCaptureImportFake(t, nil, nil)
	svc := newImportServiceAgainst(t, server.URL)
	data := []byte("sku,name,type_id,price,attribute_set_id\nNEW-1,,simple,9.99,4\nNEW-2,Widget,simple,8.99,4\n")
	result, err := svc.ExecuteImport(context.Background(), "csv", data, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.Failed != 1 || result.Total != 2 {
		t.Fatalf("result = %+v", result)
	}
	failed := result.Rows[0]
	if failed.SKU != "NEW-1" || failed.Status != "failed" {
		t.Fatalf("failed row = %+v", failed)
	}
	if !strings.Contains(failed.Error, "name") || !strings.Contains(failed.Error, "field=name") {
		t.Fatalf("row error = %q, want it to name the missing field", failed.Error)
	}
	if created := fake.productBody(t, "NEW-2"); created["name"] != "Widget" {
		t.Fatalf("the valid row did not create its product: %+v", created)
	}
}

// U-65 / INT-10: a failed row used to read "validation_failed: Magento rejected
// the request as invalid", which names no column. The upstream reason (verbatim
// or redacted) and the field must survive into the per-row error.
func TestImportRowErrorKeepsUpstreamReason(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantAll []string
	}{
		{
			name:    "quoted field placeholder is redacted but kept",
			body:    `{"message":"Validation Failed","errors":[{"message":"\"%field\" can not be empty.","parameters":{"field":"postcode"}}]}`,
			wantAll: []string{"can not be empty", "field=postcode"},
		},
		{
			name:    "plain business rule travels verbatim",
			body:    `{"message":"Can not save link related to Default Source or Default Stock"}`,
			wantAll: []string{"Can not save link related to Default Source or Default Stock"},
		},
		{
			// A reason that echoes a SKU stays server-side by policy (SEC-10), so
			// the row keeps the status instead of collapsing to a bare sentence.
			name:    "identifier-bearing reason keeps the status",
			body:    `{"message":"The product with SKU \"ABC123\" does not exist. Verify the product and try again."}`,
			wantAll: []string{"upstream_status=400"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := newCaptureImportFake(t, map[string]bool{"OLD-1": true}, map[string]string{"OLD-1": tc.body})
			svc := newImportServiceAgainst(t, server.URL)
			result, err := svc.ExecuteImport(context.Background(), "csv", []byte("sku,name,price\nOLD-1,Widget,39.99\n"), true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failed != 1 || len(result.Rows) != 1 {
				t.Fatalf("result = %+v", result)
			}
			rowError := result.Rows[0].Error
			for _, want := range tc.wantAll {
				if !strings.Contains(rowError, want) {
					t.Fatalf("row error = %q, want it to contain %q", rowError, want)
				}
			}
		})
	}
}

// U-65: the async enqueue rejected the whole file with the first bad row only, so
// an operator fixed one row per upload. Every invalid row is named, bounded.
func TestValidateImportShapeReportsEveryInvalidRow(t *testing.T) {
	data := []byte("sku,name,price\n,NoSku,1.00\nB-1,Ok,abc\nC-1,Ok,1.00\nC-1,Dup,1.00\n")
	_, err := ValidateImportShape("csv", data)
	if err == nil {
		t.Fatal("invalid rows must be rejected")
	}
	var typed *errs.Error
	if !errors.As(err, &typed) {
		t.Fatalf("err = %v, want a taxonomy error", err)
	}
	if got := typed.Details["invalid_rows"]; got != "3" {
		t.Fatalf("invalid_rows = %q, want 3 (rows 2, 3 and 5)", got)
	}
	for _, want := range []string{"row 2:", "row 3:", "row 5:"} {
		if !strings.Contains(typed.Details["rows"], want) {
			t.Fatalf("rows = %q, want it to contain %q", typed.Details["rows"], want)
		}
	}
	if typed.Details["row"] != "2" || typed.Details["reason"] == "" {
		t.Fatalf("details = %+v, want the first row kept for compatibility", typed.Details)
	}
}
