// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Import limits. They are enforced before any row is inspected so a hostile
// payload cannot allocate unbounded memory.
const (
	maxImportBytes = 8 << 20
	maxImportRows  = 5000
	maxImportCols  = 64
)

// ImportRow is one parsed product row.
type ImportRow struct {
	RowNumber  int               `json:"row_number"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	TypeID     string            `json:"type_id"`
	Price      string            `json:"price,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// ImportRowError describes one rejected row.
type ImportRowError struct {
	RowNumber int    `json:"row_number"`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

// ImportWarning is a non-blocking content/SEO suggestion for one row.
type ImportWarning struct {
	RowNumber int    `json:"row_number"`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

// ImportPreview is the validation result. Nothing is written upstream.
type ImportPreview struct {
	Format            string           `json:"format"`
	Total             int              `json:"total"`
	Valid             int              `json:"valid"`
	Invalid           int              `json:"invalid"`
	Errors            []ImportRowError `json:"errors,omitempty"`
	Warnings          []ImportWarning  `json:"warnings,omitempty"`
	WarningsTruncated bool             `json:"warnings_truncated"`
	Sample            []ImportRow      `json:"sample,omitempty"`
}

// ValidateImport parses and validates a CSV or JSON product import.
// It is read-only: it never contacts Magento.
func (s *Service) ValidateImport(_ context.Context, format string, data []byte) (*ImportPreview, error) {
	if len(data) == 0 {
		return nil, errs.New(errs.CodeInvalidArgument, "import payload is empty")
	}
	if len(data) > maxImportBytes {
		return nil, errs.New(errs.CodeInvalidArgument, "import payload exceeds 8 MiB").
			WithDetail("limit_bytes", fmt.Sprint(maxImportBytes))
	}
	var rows []ImportRow
	var err error
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		rows, err = parseImportCSV(data)
	case "json":
		rows, err = parseImportJSON(data)
	default:
		return nil, errs.New(errs.CodeUnsupportedFormat, "format must be csv or json").
			WithDetail("format", format)
	}
	if err != nil {
		return nil, err
	}
	preview := &ImportPreview{Format: strings.ToLower(format), Total: len(rows)}
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		if problems := validateImportRow(row, seen); len(problems) > 0 {
			preview.Invalid++
			preview.Errors = append(preview.Errors, problems...)
			continue
		}
		preview.Valid++
		for _, warning := range importSEOWarnings(row) {
			if len(preview.Warnings) >= maxImportWarnings {
				preview.WarningsTruncated = true
				break
			}
			preview.Warnings = append(preview.Warnings, warning)
		}
		if len(preview.Sample) < 5 {
			preview.Sample = append(preview.Sample, row)
		}
	}
	return preview, nil
}

const maxImportWarnings = 200

// importSEOWarnings reports missing or weak content/SEO columns without failing
// the row: imports stay usable, but the operator sees what to improve.
func importSEOWarnings(row ImportRow) []ImportWarning {
	var warnings []ImportWarning
	add := func(field, message string) {
		warnings = append(warnings, ImportWarning{RowNumber: row.RowNumber, Field: field, Message: message})
	}
	description := row.Attributes["description"]
	switch {
	case description == "":
		add("description", "missing description; aim for 300+ characters of unique copy")
	case len(description) < 300:
		add("description", "description is shorter than 300 characters")
	}
	if row.Attributes["url_key"] == "" {
		add("url_key", "missing url_key; Magento can derive it, but an explicit keyword slug is safer")
	}
	metaTitle := row.Attributes["meta_title"]
	switch {
	case metaTitle == "":
		add("meta_title", "missing meta_title; write 30-60 characters")
	case len(metaTitle) < 30 || len(metaTitle) > 60:
		add("meta_title", "meta_title is outside the 30-60 character range")
	}
	metaDescription := row.Attributes["meta_description"]
	switch {
	case metaDescription == "":
		add("meta_description", "missing meta_description; write 120-160 characters")
	case len(metaDescription) < 120 || len(metaDescription) > 160:
		add("meta_description", "meta_description is outside the 120-160 character range")
	}
	return warnings
}

// DecodeImportPayload decodes a base64 import body with a size cap.
func DecodeImportPayload(encoded string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidArgument, "import payload is not valid base64").WithCause(err)
	}
	if len(decoded) > maxImportBytes {
		return nil, errs.New(errs.CodeInvalidArgument, "import payload exceeds 8 MiB")
	}
	return decoded, nil
}

func parseImportCSV(data []byte) ([]ImportRow, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, errs.New(errs.CodeValidationFailed, "CSV header is missing or malformed").WithCause(err)
	}
	if len(header) == 0 || len(header) > maxImportCols {
		return nil, errs.New(errs.CodeValidationFailed, "CSV header must have 1-64 columns")
	}
	columns := make([]string, len(header))
	for i, name := range header {
		columns[i] = strings.ToLower(strings.TrimSpace(name))
	}
	var rows []ImportRow
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errs.New(errs.CodeValidationFailed, "CSV row is malformed").WithCause(err)
		}
		if len(rows) >= maxImportRows {
			return nil, errs.New(errs.CodeValidationFailed, "import exceeds 5000 rows")
		}
		row := ImportRow{RowNumber: len(rows) + 2, Attributes: map[string]string{}}
		for i, value := range record {
			if i >= len(columns) {
				break
			}
			value = strings.TrimSpace(value)
			switch columns[i] {
			case "sku":
				row.SKU = value
			case "name":
				row.Name = value
			case "type_id", "type":
				row.TypeID = value
			case "price":
				row.Price = value
			default:
				if value != "" {
					row.Attributes[columns[i]] = value
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseImportJSON(data []byte) ([]ImportRow, error) {
	var raw []struct {
		SKU        string            `json:"sku"`
		Name       string            `json:"name"`
		TypeID     string            `json:"type_id"`
		Price      string            `json:"price"`
		Attributes map[string]string `json:"attributes"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return nil, errs.New(errs.CodeValidationFailed, "JSON import must be an array of objects").WithCause(err)
	}
	if len(raw) > maxImportRows {
		return nil, errs.New(errs.CodeValidationFailed, "import exceeds 5000 rows")
	}
	rows := make([]ImportRow, 0, len(raw))
	for i, item := range raw {
		row := ImportRow{RowNumber: i + 1, SKU: strings.TrimSpace(item.SKU),
			Name: strings.TrimSpace(item.Name), TypeID: strings.TrimSpace(item.TypeID),
			Price: strings.TrimSpace(item.Price), Attributes: map[string]string{}}
		for code, value := range item.Attributes {
			if strings.TrimSpace(value) != "" {
				row.Attributes[strings.ToLower(code)] = strings.TrimSpace(value)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func validateImportRow(row ImportRow, seen map[string]int) []ImportRowError {
	var problems []ImportRowError
	add := func(field, message string) {
		problems = append(problems, ImportRowError{RowNumber: row.RowNumber, Field: field, Message: message})
	}
	if err := validateSKU(row.SKU); err != nil {
		add("sku", "invalid or missing SKU")
	} else if first, exists := seen[row.SKU]; exists {
		add("sku", fmt.Sprintf("duplicate SKU (first seen on row %d)", first))
	} else {
		seen[row.SKU] = row.RowNumber
	}
	if row.Name == "" || len(row.Name) > 255 {
		add("name", "name is required and must be at most 255 characters")
	}
	switch row.TypeID {
	case "", "simple", "virtual", "downloadable", "bundle":
	default:
		add("type_id", "type_id must be simple, virtual, downloadable, or bundle")
	}
	if row.Price != "" && !priceRE.MatchString(row.Price) {
		add("price", "price must be a non-negative decimal with at most 4 decimals")
	}
	for code, value := range row.Attributes {
		if !attributeCodeRE.MatchString(code) {
			add("attributes", "attribute code must match ^[a-z0-9_]{1,64}$: "+code)
			continue
		}
		if err := validateSEOAttribute(code, value); err != nil {
			add(code, err.Error())
		}
	}
	return problems
}

// ImportResultRow is the outcome of one executed row.
type ImportResultRow struct {
	RowNumber int    `json:"row_number"`
	SKU       string `json:"sku"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

// ImportResult reports a partial-success execution. It never claims atomicity.
type ImportResult struct {
	Total   int               `json:"total"`
	Created int               `json:"created"`
	Updated int               `json:"updated"`
	Skipped int               `json:"skipped"`
	Failed  int               `json:"failed"`
	Rows    []ImportResultRow `json:"rows"`
}

// ExecuteImport validates and executes a bounded product import.
//
// Rows are processed in order. A row that already exists is updated only when
// updateExisting is true; otherwise it is skipped. Failures are recorded per
// row and do not roll back earlier writes: the caller receives a partial-success
// report and must decide whether to retry the failed rows.
func (s *Service) ExecuteImport(ctx context.Context, format string, data []byte, updateExisting bool) (*ImportResult, error) {
	return s.ExecuteImportWithProgress(ctx, format, data, updateExisting, nil)
}

// ExecuteImportWithProgress is ExecuteImport with a progress callback invoked at
// the start, every 25 rows, and at the end. The callback receives rows done and
// the total, and may be called from the same goroutine only.
func (s *Service) ExecuteImportWithProgress(ctx context.Context, format string, data []byte, updateExisting bool, progress func(done, total int)) (*ImportResult, error) {
	preview, err := s.ValidateImport(ctx, format, data)
	if err != nil {
		return nil, err
	}
	if preview.Invalid > 0 {
		return nil, errs.New(errs.CodeValidationFailed, "import has invalid rows; fix them before executing").
			WithDetail("invalid_rows", fmt.Sprint(preview.Invalid))
	}
	rows, err := parseImportRows(format, data)
	if err != nil {
		return nil, err
	}
	result := &ImportResult{Total: len(rows)}
	if progress != nil {
		progress(0, len(rows))
	}
	for i, row := range rows {
		outcome := ImportResultRow{RowNumber: row.RowNumber, SKU: row.SKU}
		_, getErr := s.Get(ctx, row.SKU)
		switch {
		case getErr == nil:
			if !updateExisting {
				outcome.Action, outcome.Status = "exists", "skipped"
				result.Skipped++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			update := UpdateProductInput{Name: &row.Name, Attributes: row.Attributes}
			if row.Price != "" {
				update.Price = &row.Price
			}
			if _, err := s.Update(ctx, row.SKU, update); err != nil {
				outcome.Action, outcome.Status, outcome.Error = "update", "failed", importErrorMessage(err)
				result.Failed++
			} else {
				outcome.Action, outcome.Status = "update", "succeeded"
				result.Updated++
			}
		case errs.CodeOf(getErr) == errs.CodeNotFound:
			attrSetID, convErr := importAttributeSetID(row)
			if convErr != nil {
				outcome.Action, outcome.Status, outcome.Error = "create", "failed", importErrorMessage(convErr)
				result.Failed++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			create := CreateProductInput{SKU: row.SKU, Name: row.Name, Price: row.Price,
				AttributeSetID: attrSetID, Attributes: row.Attributes}
			if row.TypeID != "" {
				create.TypeID = row.TypeID
			}
			if _, err := s.Create(ctx, create); err != nil {
				outcome.Action, outcome.Status, outcome.Error = "create", "failed", importErrorMessage(err)
				result.Failed++
			} else {
				outcome.Action, outcome.Status = "create", "succeeded"
				result.Created++
			}
		default:
			outcome.Action, outcome.Status, outcome.Error = "lookup", "failed", importErrorMessage(getErr)
			result.Failed++
		}
		result.Rows = append(result.Rows, outcome)
		if progress != nil && (i+1)%25 == 0 {
			progress(i+1, len(rows))
		}
	}
	if progress != nil {
		progress(len(rows), len(rows))
	}
	return result, nil
}

// importAttributeSetID reads the attribute_set_id attribute from the row.
// New products require one; there is no safe default across stores.
func importAttributeSetID(row ImportRow) (int, error) {
	raw, ok := row.Attributes["attribute_set_id"]
	if !ok {
		return 0, errs.New(errs.CodeValidationFailed, "attribute_set_id is required to create a product").
			WithDetail("row", fmt.Sprint(row.RowNumber))
	}
	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || id <= 0 {
		return 0, errs.New(errs.CodeValidationFailed, "attribute_set_id must be a positive integer").
			WithDetail("row", fmt.Sprint(row.RowNumber))
	}
	delete(row.Attributes, "attribute_set_id")
	return id, nil
}

func importErrorMessage(err error) string {
	var typed *errs.Error
	if errors.As(err, &typed) {
		return string(typed.Code) + ": " + typed.Message
	}
	return "internal error"
}

func parseImportRows(format string, data []byte) ([]ImportRow, error) {
	switch format {
	case "csv":
		return parseImportCSV(data)
	case "json":
		return parseImportJSON(data)
	default:
		return nil, errs.New(errs.CodeUnsupportedFormat, "format must be csv or json")
	}
}

// ValidateImportShape validates the payload structure and rows without calling
// Magento. It is used when enqueueing an asynchronous import so obviously bad
// batches are rejected before a job is created.
func ValidateImportShape(format string, data []byte) (int, error) {
	rows, err := parseImportRows(format, data)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, errs.New(errs.CodeValidationFailed, "import contains no rows")
	}
	if len(rows) > maxImportRows {
		return 0, errs.New(errs.CodeValidationFailed, "import exceeds 5000 rows").
			WithDetail("rows", fmt.Sprint(len(rows)))
	}
	seen := make(map[string]int, len(rows))
	for _, row := range rows {
		if rowErrors := validateImportRow(row, seen); len(rowErrors) > 0 {
			return 0, errs.New(errs.CodeValidationFailed, "import has invalid rows").
				WithDetail("row", fmt.Sprint(row.RowNumber)).
				WithDetail("reason", rowErrors[0].Message)
		}
	}
	return len(rows), nil
}

const maxReportFailures = 100

// ImportFailure is one failed row in a compact async report.
type ImportFailure struct {
	RowNumber int    `json:"row_number"`
	SKU       string `json:"sku"`
	Action    string `json:"action"`
	Error     string `json:"error"`
}

// ImportReport is the bounded summary stored on an asynchronous import job.
type ImportReport struct {
	Total             int             `json:"total"`
	Created           int             `json:"created"`
	Updated           int             `json:"updated"`
	Skipped           int             `json:"skipped"`
	Failed            int             `json:"failed"`
	Failures          []ImportFailure `json:"failures,omitempty"`
	FailuresTruncated bool            `json:"failures_truncated"`
}

// CompactImportReport reduces a full execution result to a bounded report so a
// job row never stores thousands of per-row entries.
func CompactImportReport(result *ImportResult) *ImportReport {
	if result == nil {
		return &ImportReport{}
	}
	report := &ImportReport{
		Total: result.Total, Created: result.Created, Updated: result.Updated,
		Skipped: result.Skipped, Failed: result.Failed,
	}
	for _, row := range result.Rows {
		if row.Status != "failed" {
			continue
		}
		if len(report.Failures) >= maxReportFailures {
			report.FailuresTruncated = true
			break
		}
		report.Failures = append(report.Failures, ImportFailure{
			RowNumber: row.RowNumber, SKU: row.SKU, Action: row.Action, Error: row.Error,
		})
	}
	return report
}
