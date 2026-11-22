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
//
// The ceiling is a capability, so it is declared first and the transport is sized
// to honour it — not the other way round. 8 MiB decoded encodes to 11,184,812
// bytes of base64; the MCP body cap that must admit that plus the JSON envelope
// is internal/core/config.DefaultMaxBodyBytes (16 MiB). Lowering this constant
// would silently shrink a documented feature, so it stays at the documented
// value and the three limits (schema maxLength, decoder, body cap) are pinned by
// TestImportLimitsAgreeAcrossPackages.
const (
	maxImportBytes = 8 << 20
	maxImportRows  = 5000
	maxImportCols  = 64

	// DefaultHTTPBodyBytes mirrors internal/core/config.DefaultMaxBodyBytes. It
	// is repeated here so an import error can report the body cap without the
	// magento package depending on config; the drift test asserts the two are
	// equal.
	DefaultHTTPBodyBytes = 16 << 20
	// importEnvelopeBytes bounds the JSON around data_base64 (instance, format,
	// update_existing, idempotency_key, braces, and escaping). The measured
	// envelope for a maximum import is ~162 bytes; 4 KiB is deliberate headroom.
	importEnvelopeBytes = 4 << 10
)

// MaxImportBytes is the largest decoded import payload accepted, in bytes.
const MaxImportBytes = maxImportBytes

// ImportHTTPBodyBytes returns the MCP request-body size an import of decodedLen
// bytes needs once base64-encoded and wrapped in its JSON envelope.
func ImportHTTPBodyBytes(decodedLen int) int {
	return base64.StdEncoding.EncodedLen(decodedLen) + importEnvelopeBytes
}

// ImportFitsHTTPBody reports whether a decoded payload of decodedLen bytes fits
// the default transport body cap.
func ImportFitsHTTPBody(decodedLen int) bool {
	return ImportHTTPBodyBytes(decodedLen) <= DefaultHTTPBodyBytes
}

// importTooLargeError names the limit and the observed size so an operator can
// split the batch without guessing which cap was hit.
func importTooLargeError(observed int, encoded bool) error {
	unit := ""
	if encoded {
		unit = " (base64-encoded)"
	}
	e := errs.New(errs.CodeInvalidArgument,
		fmt.Sprintf("import payload is %d bytes%s; the limit is %d bytes decoded (%d bytes encoded)",
			observed, unit, maxImportBytes, base64.StdEncoding.EncodedLen(maxImportBytes)))
	e = e.WithDetail("limit_bytes", fmt.Sprint(maxImportBytes))
	e = e.WithDetail("observed_bytes", fmt.Sprint(observed))
	e = e.WithDetail("max_rows", fmt.Sprint(maxImportRows))
	e = e.WithDetail("http_body_limit_bytes", fmt.Sprint(DefaultHTTPBodyBytes))
	return e
}

// ImportRow is one parsed product row.
type ImportRow struct {
	RowNumber  int               `json:"row_number" jsonschema:"1-based row number in the import payload."`
	SKU        string            `json:"sku" jsonschema:"Product SKU."`
	Name       string            `json:"name" jsonschema:"Display name."`
	TypeID     string            `json:"type_id"`
	Price      string            `json:"price,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty" jsonschema:"Attributes of the requested attribute set."`
}

// ImportRowError describes one rejected row.
type ImportRowError struct {
	RowNumber int    `json:"row_number" jsonschema:"1-based row number in the import payload."`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

// ImportWarning is a non-blocking content/SEO suggestion for one row.
type ImportWarning struct {
	RowNumber int    `json:"row_number" jsonschema:"1-based row number in the import payload."`
	Field     string `json:"field"`
	Message   string `json:"message"`
}

// ImportPreview is the validation result. Nothing is written upstream.
type ImportPreview struct {
	Format            string           `json:"format" jsonschema:"Payload encoding: csv or json."`
	Total             int              `json:"total" jsonschema:"Number of rows in this payload."`
	Valid             int              `json:"valid" jsonschema:"Rows that passed validation."`
	Invalid           int              `json:"invalid" jsonschema:"Rows that failed validation."`
	Errors            []ImportRowError `json:"errors,omitempty" jsonschema:"Validation errors, one per invalid row."`
	Warnings          []ImportWarning  `json:"warnings,omitempty" jsonschema:"Non-fatal validation warnings."`
	WarningsTruncated bool             `json:"warnings_truncated" jsonschema:"True when more warnings exist than were returned."`
	Sample            []ImportRow      `json:"sample,omitempty" jsonschema:"The first few rows, for eyeballing the payload."`
}

// ValidateImport parses and validates a CSV or JSON product import.
// It is read-only: it never contacts Magento, so the context is accepted for
// signature symmetry with the other Service methods and never read.
func (s *Service) ValidateImport(_ context.Context, format string, data []byte) (*ImportPreview, error) {
	preview, _, err := s.parseAndValidate(format, data)
	return preview, err
}

// parseAndValidate parses once and returns both the preview and the rows.
//
// Execution used to call ValidateImport and then parse the same payload again, so
// every import paid twice for the slowest step: for 5,000 rows that is ~7 MB of
// allocation per parse and a doubled window with the whole payload resident.
func (s *Service) parseAndValidate(format string, data []byte) (*ImportPreview, []ImportRow, error) {
	if len(data) == 0 {
		return nil, nil, errs.New(errs.CodeInvalidArgument, "import payload is empty")
	}
	if len(data) > maxImportBytes {
		return nil, nil, importTooLargeError(len(data), false)
	}
	var rows []ImportRow
	var err error
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "csv":
		rows, err = parseImportCSV(data)
	case "json":
		rows, err = parseImportJSON(data)
	default:
		return nil, nil, errs.New(errs.CodeUnsupportedFormat, "format must be csv or json").
			WithDetail("format", format)
	}
	if err != nil {
		return nil, nil, err
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
	return preview, rows, nil
}

const maxImportWarnings = 200

// importSEOWarnings reports missing or weak content/SEO columns without failing
// the row: imports stay usable, but the operator sees what to improve.
func importSEOWarnings(row ImportRow) []ImportWarning {
	var warnings []ImportWarning
	add := func(field, message string) {
		warnings = append(warnings, ImportWarning{RowNumber: row.RowNumber, Field: field, Message: message})
	}
	// The row is valid without a name because it may be an update; a create
	// without one fails at execution, so say so before the write is attempted.
	if strings.TrimSpace(row.Name) == "" {
		add("name", "missing name; required when this row creates a product, ignored for an update")
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
//
// The encoded length is checked first so an oversized body is rejected without
// paying for the decode allocation, and so the error names the same limit the
// transport enforces.
func DecodeImportPayload(encoded string) ([]byte, error) {
	if len(encoded) > base64.StdEncoding.EncodedLen(maxImportBytes) {
		return nil, importTooLargeError(len(encoded), true)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errs.New(errs.CodeInvalidArgument, "import payload is not valid base64").WithCause(err)
	}
	if len(decoded) > maxImportBytes {
		return nil, importTooLargeError(len(decoded), false)
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
		SKU        string            `json:"sku" jsonschema:"Product SKU."`
		Name       string            `json:"name" jsonschema:"Display name."`
		TypeID     string            `json:"type_id"`
		Price      string            `json:"price"`
		Attributes map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`
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
	// A name is required to create a product, but an update row that only
	// reprices an existing SKU must not be rejected for omitting it: whether the
	// row creates or updates needs Magento, and validation is deliberately
	// offline (U-65). A missing name is a warning here and a per-row failure at
	// execution time when the row turns out to be a create.
	if len(row.Name) > 255 {
		add("name", "name must be at most 255 characters")
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
		if err := validateReservedAttribute(code, value); err != nil {
			add(code, err.Error())
		}
	}
	return problems
}

// ImportResultRow is the outcome of one executed row.
type ImportResultRow struct {
	RowNumber int    `json:"row_number" jsonschema:"1-based row number in the import payload."`
	SKU       string `json:"sku" jsonschema:"Product SKU."`
	Action    string `json:"action" jsonschema:"create, update or skipped."`
	Status    string `json:"status" jsonschema:"Current status."`
	Error     string `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// existingSKUs resolves which import rows already exist in Magento, in batches.
//
// The second return value is false when the batched form could not be answered,
// in which case the caller must fall back to a per-row read: a store that rejects
// `sku in (...)` must not turn every row into a spurious create.
func (s *Service) existingSKUs(ctx context.Context, rows []ImportRow) (map[string]struct{}, bool) {
	found := make(map[string]struct{}, len(rows))
	skus := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, duplicate := seen[row.SKU]; duplicate {
			continue
		}
		seen[row.SKU] = struct{}{}
		skus = append(skus, row.SKU)
	}
	for start := 0; start < len(skus); start += maxBatchGetSKUs {
		end := start + maxBatchGetSKUs
		if end > len(skus) {
			end = len(skus)
		}
		products, err := s.BatchGet(ctx, skus[start:end])
		if err != nil {
			return nil, false
		}
		for _, product := range products {
			found[product.SKU] = struct{}{}
		}
	}
	return found, true
}

// ImportResult reports a partial-success execution. It never claims atomicity.
type ImportResult struct {
	Total   int               `json:"total" jsonschema:"Number of rows in this batch."`
	Created int               `json:"created" jsonschema:"Rows that created a product."`
	Updated int               `json:"updated" jsonschema:"Rows that updated a product."`
	Skipped int               `json:"skipped" jsonschema:"Rows skipped because the product existed."`
	Failed  int               `json:"failed" jsonschema:"Rows that failed."`
	Rows    []ImportResultRow `json:"rows" jsonschema:"Per-row results, in input order."`
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
	return s.executeImportRows(ctx, format, data, updateExisting, nil, progress)
}

// ExecuteImportRows executes only the given row numbers (used by import resume).
// An empty list is equivalent to ExecuteImport.
func (s *Service) ExecuteImportRows(ctx context.Context, format string, data []byte, updateExisting bool, onlyRows []int) (*ImportResult, error) {
	return s.executeImportRows(ctx, format, data, updateExisting, onlyRows, nil)
}

func (s *Service) executeImportRows(ctx context.Context, format string, data []byte, updateExisting bool, onlyRows []int, progress func(done, total int)) (*ImportResult, error) {
	preview, rows, err := s.parseAndValidate(format, data)
	if err != nil {
		return nil, err
	}
	if preview.Invalid > 0 {
		return nil, errs.New(errs.CodeValidationFailed, "import has invalid rows; fix them before executing").
			WithDetail("invalid_rows", fmt.Sprint(preview.Invalid))
	}
	if rows == nil {
		return nil, err
	}
	if len(onlyRows) > 0 {
		selected := make(map[int]bool, len(onlyRows))
		for _, row := range onlyRows {
			selected[row] = true
		}
		filtered := rows[:0]
		for _, row := range rows {
			if selected[row.RowNumber] {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	result := &ImportResult{Total: len(rows)}
	if progress != nil {
		progress(0, len(rows))
	}
	// Existence is resolved in batches before the loop. Asking per row cost one
	// GET per row on top of the write, so a 5,000-row import issued 10,000 requests
	// where ~200 suffice. The batch answer is authoritative for rows that already
	// existed; SKUs created during this run are added as they succeed, so a
	// duplicate SKU later in the file still updates rather than collides.
	existing, known := s.existingSKUs(ctx, rows)
	for i, row := range rows {
		outcome := ImportResultRow{RowNumber: row.RowNumber, SKU: row.SKU}
		getErr := error(nil)
		if known {
			if _, present := existing[row.SKU]; !present {
				getErr = errs.New(errs.CodeNotFound, "Magento has no such resource or route")
			}
		} else {
			// The batch pre-fetch failed: fall back to the per-row read so behaviour
			// is unchanged when the store cannot answer the batched form.
			_, getErr = s.Get(ctx, row.SKU)
		}
		switch {
		case getErr == nil:
			if !updateExisting {
				outcome.Action, outcome.Status = "exists", "skipped"
				result.Skipped++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			// A price-only row carries no name. Sending an empty name would
			// overwrite the product's name with "", so the field is omitted and
			// Magento's merge semantics keep the stored value (U-65).
			update := UpdateProductInput{}
			if row.Name != "" {
				name := row.Name
				update.Name = &name
			}
			if err := applyImportUpdateAttributes(row.Attributes, &update); err != nil {
				outcome.Action, outcome.Status, outcome.Error = "update", "failed", importErrorMessage(err)
				result.Failed++
				result.Rows = append(result.Rows, outcome)
				continue
			}
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
			// Validation cannot know whether a row creates or updates, so the
			// name requirement is enforced here, where the answer is known, and
			// only the row that needs a name fails.
			if strings.TrimSpace(row.Name) == "" {
				outcome.Action, outcome.Status = "create", "failed"
				outcome.Error = importErrorMessage(errs.New(errs.CodeValidationFailed,
					"name is required to create a product").WithDetail("field", "name"))
				result.Failed++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			attrSetID, convErr := importAttributeSetID(row)
			if convErr != nil {
				outcome.Action, outcome.Status, outcome.Error = "create", "failed", importErrorMessage(convErr)
				result.Failed++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			create := CreateProductInput{SKU: row.SKU, Name: row.Name, Price: row.Price,
				AttributeSetID: attrSetID}
			if err := applyImportAttributes(row.Attributes, &create); err != nil {
				outcome.Action, outcome.Status, outcome.Error = "create", "failed", importErrorMessage(err)
				result.Failed++
				result.Rows = append(result.Rows, outcome)
				continue
			}
			if row.TypeID != "" {
				create.TypeID = row.TypeID
			}
			if _, err := s.Create(ctx, create); err != nil {
				outcome.Action, outcome.Status, outcome.Error = "create", "failed", importErrorMessage(err)
				result.Failed++
			} else {
				outcome.Action, outcome.Status = "create", "succeeded"
				result.Created++
				// A later row with the same SKU must update what this row created,
				// not collide with it.
				if known {
					existing[row.SKU] = struct{}{}
				}
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

// importErrorMessage renders one failed row for the caller.
//
// The code and the gateway's own message are not enough to fix a row: "Magento
// rejected the request as invalid" says nothing about which column broke, so the
// upstream reason travels with it. When the client withheld the reason (it does
// so for text naming identifiers), the field and status it did carry are kept
// instead, which is still enough to find the column (U-65 / INT-10).
func importErrorMessage(err error) string {
	var typed *errs.Error
	if !errors.As(err, &typed) {
		if err != nil && err.Error() != "" {
			return err.Error()
		}
		return "internal error"
	}
	message := string(typed.Code) + ": " + typed.Message
	var parts []string
	upstream := typed.Details["upstream_message"]
	if upstream == "" {
		// A reason that named an identifier is carried redacted instead of being
		// dropped; either form tells the operator what the row got wrong.
		upstream = typed.Details["upstream_reason"]
	}
	if upstream != "" {
		parts = append(parts, upstream)
	}
	field := typed.Details["upstream_field"]
	if field == "" {
		field = typed.Details["field"]
	}
	if field != "" {
		parts = append(parts, "field="+field)
	}
	if reason := typed.Details["reason"]; reason != "" {
		parts = append(parts, "reason="+reason)
	}
	if status := typed.Details["upstream_status"]; status != "" && upstream == "" {
		parts = append(parts, "upstream_status="+status)
	}
	if len(parts) > 0 {
		message += " (" + strings.Join(parts, ", ") + ")"
	}
	return message
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
	// Reporting only the first bad row made an operator fix one row per call
	// against a 5,000-row file. Every invalid row is named, bounded by the same
	// cap the async report uses (U-65 / INT-10).
	invalid := 0
	firstRow, firstReason := 0, ""
	summaries := make([]string, 0, maxReportFailures)
	for _, row := range rows {
		rowErrors := validateImportRow(row, seen)
		if len(rowErrors) == 0 {
			continue
		}
		invalid++
		if firstRow == 0 {
			firstRow, firstReason = row.RowNumber, rowErrors[0].Message
		}
		if len(summaries) < maxReportFailures {
			summaries = append(summaries, fmt.Sprintf("row %d: %s", row.RowNumber, rowErrors[0].Message))
		}
	}
	if invalid > 0 {
		e := errs.New(errs.CodeValidationFailed, "import has invalid rows").
			WithDetail("row", fmt.Sprint(firstRow)).
			WithDetail("reason", firstReason).
			WithDetail("invalid_rows", fmt.Sprint(invalid)).
			WithDetail("rows", strings.Join(summaries, "; "))
		if invalid > len(summaries) {
			e = e.WithDetail("rows_truncated", "true")
		}
		return 0, e
	}
	return len(rows), nil
}

const maxReportFailures = 100

// ImportFailure is one failed row in a compact async report.
type ImportFailure struct {
	RowNumber int    `json:"row_number" jsonschema:"1-based row number in the import payload."`
	SKU       string `json:"sku" jsonschema:"Product SKU."`
	Action    string `json:"action" jsonschema:"create, update or skipped."`
	Error     string `json:"error" jsonschema:"Why the row failed."`
}

// ImportReport is the bounded summary stored on an asynchronous import job.
type ImportReport struct {
	Total             int             `json:"total" jsonschema:"Number of rows in this import."`
	Created           int             `json:"created" jsonschema:"Rows that created a product."`
	Updated           int             `json:"updated" jsonschema:"Rows that updated a product."`
	Skipped           int             `json:"skipped" jsonschema:"Rows skipped because the product existed."`
	Failed            int             `json:"failed" jsonschema:"Rows that failed."`
	Failures          []ImportFailure `json:"failures,omitempty" jsonschema:"Per-row failures, bounded by the report cap."`
	FailuresTruncated bool            `json:"failures_truncated" jsonschema:"True when more rows failed than the report carries; the counts still cover every row."`
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

// reservedImportAttributes are the attribute codes with dedicated typed fields
// on the product write inputs. Imports must route them through those fields, or
// the duplicate rejection would fail every row.
var reservedImportAttributes = map[string]bool{
	"description": true, "short_description": true, "url_key": true,
	"meta_title": true, "meta_keywords": true, "meta_description": true,
	"weight": true, "special_price": true, "special_from_date": true,
	"special_to_date": true, "cost": true, "tax_class_id": true,
	// status and visibility are top-level product fields, not EAV attributes.
	// Sending them as attributes made Magento ignore them, so an import that said
	// "disabled, not visible" produced an enabled product on the storefront.
	"status": true, "visibility": true,
}

// validateReservedAttribute validates a SEO/pricing attribute value from an
// import row. Non-reserved codes are not checked here.
func validateReservedAttribute(code, value string) error {
	if !reservedImportAttributes[code] {
		return nil
	}
	switch code {
	case "weight", "special_price", "cost":
		if value != "" && !priceRE.MatchString(value) {
			return invalidf("%s must be a non-negative decimal with at most 4 fraction digits", code)
		}
		return nil
	case "special_from_date", "special_to_date":
		if value != "" && !specialDateRE.MatchString(value) {
			return invalidf("%s must be a YYYY-MM-DD date", code)
		}
		return nil
	case "tax_class_id":
		if value != "" {
			if _, err := strconv.Atoi(value); err != nil {
				return invalidf("tax_class_id must be an integer")
			}
		}
		return nil
	default:
		return validateSEOAttribute(code, value)
	}
}

// splitImportAttributes separates reserved codes from custom EAV attributes.
func splitImportAttributes(attrs map[string]string) (map[string]string, map[string]string) {
	reserved := make(map[string]string, len(attrs))
	custom := make(map[string]string, len(attrs))
	for code, value := range attrs {
		if reservedImportAttributes[code] {
			reserved[code] = value
			continue
		}
		custom[code] = value
	}
	return reserved, custom
}

// applyImportAttributes fills the typed SEO/pricing fields and custom attributes
// of a create input from an import row.
func applyImportAttributes(attrs map[string]string, in *CreateProductInput) error {
	reserved, custom := splitImportAttributes(attrs)
	in.Attributes = custom
	in.Description = reserved["description"]
	in.ShortDescription = reserved["short_description"]
	in.URLKey = reserved["url_key"]
	in.MetaTitle = reserved["meta_title"]
	in.MetaKeywords = reserved["meta_keywords"]
	in.MetaDescription = reserved["meta_description"]
	in.Weight = reserved["weight"]
	in.SpecialPrice = reserved["special_price"]
	in.SpecialFromDate = reserved["special_from_date"]
	in.SpecialToDate = reserved["special_to_date"]
	in.Cost = reserved["cost"]
	if raw := reserved["tax_class_id"]; raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil {
			return invalidf("tax_class_id must be an integer")
		}
		in.TaxClassID = id
	}
	if raw := reserved["status"]; raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil || (status != 1 && status != 2) {
			return invalidf("status must be 1 (enabled) or 2 (disabled)")
		}
		in.Status = status
	}
	if raw := reserved["visibility"]; raw != "" {
		visibility, err := strconv.Atoi(raw)
		if err != nil || visibility < 1 || visibility > 4 {
			return invalidf("visibility must be 1 (not visible), 2 (catalog), 3 (search) or 4 (catalog and search)")
		}
		in.Visibility = visibility
	}
	return nil
}

// applyImportUpdateAttributes fills the typed fields of an update input.
func applyImportUpdateAttributes(attrs map[string]string, in *UpdateProductInput) error {
	reserved, custom := splitImportAttributes(attrs)
	in.Attributes = custom
	if value, ok := reserved["description"]; ok {
		in.Description = &value
	}
	if value, ok := reserved["short_description"]; ok {
		in.ShortDescription = &value
	}
	if value, ok := reserved["url_key"]; ok {
		in.URLKey = &value
	}
	if value, ok := reserved["meta_title"]; ok {
		in.MetaTitle = &value
	}
	if value, ok := reserved["meta_keywords"]; ok {
		in.MetaKeywords = &value
	}
	if value, ok := reserved["meta_description"]; ok {
		in.MetaDescription = &value
	}
	if value, ok := reserved["weight"]; ok {
		in.Weight = &value
	}
	if value, ok := reserved["special_price"]; ok {
		in.SpecialPrice = &value
	}
	if value, ok := reserved["special_from_date"]; ok {
		in.SpecialFromDate = &value
	}
	if value, ok := reserved["special_to_date"]; ok {
		in.SpecialToDate = &value
	}
	if value, ok := reserved["cost"]; ok {
		in.Cost = &value
	}
	if raw, ok := reserved["tax_class_id"]; ok && raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil {
			return invalidf("tax_class_id must be an integer")
		}
		in.TaxClassID = &id
	}
	if raw, ok := reserved["status"]; ok && raw != "" {
		status, err := strconv.Atoi(raw)
		if err != nil || (status != 1 && status != 2) {
			return invalidf("status must be 1 (enabled) or 2 (disabled)")
		}
		in.Status = &status
	}
	if raw, ok := reserved["visibility"]; ok && raw != "" {
		visibility, err := strconv.Atoi(raw)
		if err != nil || visibility < 1 || visibility > 4 {
			return invalidf("visibility must be 1 (not visible), 2 (catalog), 3 (search) or 4 (catalog and search)")
		}
		in.Visibility = &visibility
	}
	return nil
}
