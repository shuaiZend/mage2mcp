// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxSourceItemsPerCall = 100
	// defaultSourceCode is the source Magento creates for a single-source
	// installation; products created with a quantity are assigned there.
	defaultSourceCode = "default"
)

var sourceCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// SourceItem is the quantity of one SKU at one inventory source. Status 1 means
// in stock and 0 out of stock. Quantity replaces the source quantity; it does
// not add to it.
type SourceItem struct {
	SKU        string
	SourceCode string
	Quantity   int
	Status     int
}

// sourceItemPayload is one sourceItems entry of the MSI write body.
type sourceItemPayload struct {
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Quantity   int    `json:"quantity"`
	Status     int    `json:"status" jsonschema:"Current status."`
}

// sourceItemsBody is the body of POST /inventory/source-items.
type sourceItemsBody struct {
	SourceItems []sourceItemPayload `json:"sourceItems"`
}

// AssignSourceItems upserts per-source quantities with
// POST /inventory/source-items. At most 100 items are sent per call and every
// item is validated before the request. Magento treats the call as an upsert,
// but the response carries no per-item outcome, so the operation is marked
// non-idempotent and the caller re-reads source items to verify the result.
func (s *Service) AssignSourceItems(ctx context.Context, items []SourceItem) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if err := validateSourceItems(items); err != nil {
		return err
	}
	body := sourceItemsBody{SourceItems: make([]sourceItemPayload, 0, len(items))}
	for _, item := range items {
		body.SourceItems = append(body.SourceItems, sourceItemPayload(item))
	}
	_, err := s.client.do(ctx, http.MethodPost, "/inventory/source-items", nil, body)
	return err
}

// validateSourceItems bounds and checks a source-item batch.
func validateSourceItems(items []SourceItem) error {
	if len(items) == 0 {
		return invalidf("at least one source item is required")
	}
	if len(items) > maxSourceItemsPerCall {
		return invalidf("at most %d source items are allowed per call", maxSourceItemsPerCall)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if err := validateSKU(item.SKU); err != nil {
			return err
		}
		if !sourceCodeRE.MatchString(item.SourceCode) {
			return invalidf("source code must match %s", sourceCodeRE.String())
		}
		if item.Quantity < 0 {
			return invalidf("source item quantity must not be negative")
		}
		if item.Status != 0 && item.Status != 1 {
			return invalidf("source item status must be 1 (in stock) or 0 (out of stock)")
		}
		key := item.SKU + "\x00" + item.SourceCode
		if _, duplicate := seen[key]; duplicate {
			return invalidf("source item for sku %q and source %q is duplicated", item.SKU, item.SourceCode)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// SourceItemRef identifies one SKU at one inventory source.
type SourceItemRef struct {
	SKU        string
	SourceCode string
}

// sourceItemRefPayload is one sourceItems entry of the MSI unassign body.
type sourceItemRefPayload struct {
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
}

// sourceItemsRefBody is the body of DELETE /inventory/source-items.
type sourceItemsRefBody struct {
	SourceItems []sourceItemRefPayload `json:"sourceItems"`
}

// UnassignSourceItems removes per-source assignments with
// DELETE /inventory/source-items. At most 100 items are sent per call and every
// item is validated before the request.
func (s *Service) UnassignSourceItems(ctx context.Context, refs []SourceItemRef) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if err := validateSourceItemRefs(refs); err != nil {
		return err
	}
	body := sourceItemsRefBody{SourceItems: make([]sourceItemRefPayload, 0, len(refs))}
	for _, ref := range refs {
		body.SourceItems = append(body.SourceItems, sourceItemRefPayload(ref))
	}
	// Magento's MSI delete endpoints are POSTs with a -delete suffix; the DELETE form
	// is not a route at all and returned 404, so this tool never worked on a standard
	// instance.
	_, err := s.client.do(ctx, http.MethodPost, "/inventory/source-items-delete", nil, body)
	return err
}

// validateSourceItemRefs bounds and checks an unassign batch.
func validateSourceItemRefs(refs []SourceItemRef) error {
	if len(refs) == 0 {
		return invalidf("at least one source item is required")
	}
	if len(refs) > maxSourceItemsPerCall {
		return invalidf("at most %d source items are allowed per call", maxSourceItemsPerCall)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateSKU(ref.SKU); err != nil {
			return err
		}
		if !sourceCodeRE.MatchString(ref.SourceCode) {
			return invalidf("source code must match %s", sourceCodeRE.String())
		}
		key := ref.SKU + "\x00" + ref.SourceCode
		if _, duplicate := seen[key]; duplicate {
			return invalidf("source item for sku %q and source %q is duplicated", ref.SKU, ref.SourceCode)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// transferLockStripes bounds the per-SKU in-process guard; transfers hashing to
// the same stripe serialise with each other.
const transferLockStripes = 64

// transferLocks is the process-wide guard for same-SKU transfers, keyed by
// client and SKU so two configured instances never contend. It is a mitigation
// only: a second gateway replica has its own array, which is why
// TransferSourceQty also re-reads the sources before writing.
var transferLocks [transferLockStripes]sync.Mutex

// TransferOutcome classifies one source transfer.
type TransferOutcome string

const (
	// TransferApplied means both source rows reached the intended post-state and
	// the post-write read-back confirmed it.
	TransferApplied TransferOutcome = "applied"
	// TransferConflict means another writer changed one of the two source rows
	// between the read and the write. No write was issued by this transfer.
	TransferConflict TransferOutcome = "conflict"
	// TransferVerificationMismatch means Magento accepted the write but the
	// read-back does not match the intended post-state.
	TransferVerificationMismatch TransferOutcome = "verification_mismatch"
	// TransferPartial means exactly one of the two source rows reached its
	// intended value and the other did not. The applied row is named.
	TransferPartial TransferOutcome = "partial"
	// TransferNotApplied means the write failed and neither source row changed.
	TransferNotApplied TransferOutcome = "not_applied"
	// TransferNeedsVerification means the outcome is ambiguous: the write or the
	// read-back failed and the observed state cannot be classified. The gateway
	// never compensates on its own; an operator decides.
	TransferNeedsVerification TransferOutcome = "needs_verification"
)

// TransferResult is the observable record of one source transfer: what was
// asked, what was observed before, what was written, and what was observed
// afterwards. Only TransferApplied is a success; every other outcome is
// returned together with a typed error that carries the same facts.
type TransferResult struct {
	TransferID string          `json:"transfer_id"`
	SKU        string          `json:"sku" jsonschema:"Product SKU."`
	FromSource string          `json:"from_source"`
	ToSource   string          `json:"to_source"`
	Quantity   int             `json:"quantity"`
	Outcome    TransferOutcome `json:"outcome"`
	Detail     string          `json:"detail,omitempty"`

	// Quantities observed before the write (first read) and after the attempt
	// (post-write read-back, or the second read when the transfer was aborted).
	// FromAfter and ToAfter are omitted when AfterObserved is false: a zero is
	// not an observation, and the only honest reading of an unobserved
	// post-state is "unknown".
	FromBefore    int  `json:"from_before"`
	FromAfter     *int `json:"from_after,omitempty"`
	ToBefore      int  `json:"to_before"`
	ToAfter       *int `json:"to_after,omitempty"`
	AfterObserved bool `json:"after_observed"`

	// The post-state the transfer intended to write.
	IntendedFrom int `json:"intended_from"`
	IntendedTo   int `json:"intended_to"`

	// Whether each row reached its intended value.
	FromApplied bool `json:"from_applied"`
	ToApplied   bool `json:"to_applied"`
}

// AppliedRows names the rows that reached their intended value: "both", "from",
// "to", "none", or "unknown".
//
// It reports "unknown" whenever the transfer could not establish the post-state
// (outcome needs_verification, or the read-back failed). Claiming "none" there
// would be false precision: an agent keying on it could conclude that nothing was
// applied and retry, which is exactly the double movement this report exists to
// prevent.
func (r *TransferResult) AppliedRows() string {
	if r.Outcome == TransferNeedsVerification || !r.AfterObserved {
		return "unknown"
	}
	switch {
	case r.FromApplied && r.ToApplied:
		return "both"
	case r.FromApplied:
		return "from"
	case r.ToApplied:
		return "to"
	default:
		return "none"
	}
}

// TransferSourceQty moves quantity between two sources for one SKU.
//
// Magento has no atomic transfer endpoint, so the gateway reads both source
// quantities, re-reads them immediately before writing, then upserts both rows
// in one batch and reads them back. Every step is reported in a TransferResult
// carrying a transfer id and the observed before/after quantities of both
// sources.
//
// Concurrency: same-SKU transfers are serialised by an in-process guard
// (the transferLocks stripes) *and* by a compare-before-write re-read that aborts with
// TransferConflict when another writer moved either source. The in-process guard
// alone is not a defence: a second gateway replica has its own locks, so the
// re-read is what detects the cross-replica lost update.
//
// Known residual: MSI exposes no conditional write (the source-item upsert always
// takes absolute values), so two concurrent transfers of the same SKU on
// different replicas can still be lost when the second writer's re-read happens
// to observe values identical to the first writer's pre-state. Closing that
// window needs a compare-and-set upstream or a serialising ledger; until then the
// residual is accepted and documented rather than hidden.
//
// Ambiguity: the transfer never compensates on its own. When the write fails
// after one row landed (TransferPartial) or the state cannot be classified
// (TransferNeedsVerification) it returns the observed state and a typed error so
// an operator can decide; nothing is retried or rolled back.
func (s *Service) TransferSourceQty(ctx context.Context, sku, fromSource, toSource string, qty int) (*TransferResult, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if !sourceCodeRE.MatchString(fromSource) || !sourceCodeRE.MatchString(toSource) {
		return nil, invalidf("source codes must match %s", sourceCodeRE.String())
	}
	if fromSource == toSource {
		return nil, invalidf("from_source and to_source must differ")
	}
	if qty <= 0 {
		return nil, invalidf("quantity must be greater than zero")
	}
	transferID, err := newTransferID()
	if err != nil {
		return nil, err
	}
	unlock := s.lockTransferSKU(sku)
	defer unlock()

	result := &TransferResult{
		TransferID: transferID, SKU: sku, FromSource: fromSource, ToSource: toSource, Quantity: qty,
	}

	fromBefore, toBefore, err := s.readSourcePair(ctx, sku, fromSource, toSource)
	if err != nil {
		return nil, err
	}
	result.FromBefore, result.ToBefore = fromBefore, toBefore
	if fromBefore < qty {
		return nil, invalidf("source %q has %d units, cannot transfer %d", fromSource, fromBefore, qty)
	}

	// Compare before writing: if either row moved since the read, another writer
	// is in flight and an absolute write would clobber it (the MAG-10 lost
	// update). Abort without writing.
	fromCheck, toCheck, err := s.readSourcePair(ctx, sku, fromSource, toSource)
	if err != nil {
		return nil, err
	}
	result.IntendedFrom, result.IntendedTo = fromBefore-qty, toBefore+qty
	if fromCheck != fromBefore || toCheck != toBefore {
		result.FromAfter, result.ToAfter = &fromCheck, &toCheck
		result.AfterObserved = true
		result.Outcome = TransferConflict
		result.Detail = fmt.Sprintf(
			"another writer changed the source quantities between the read and the write (from %d→%d, to %d→%d); no write was issued",
			fromBefore, fromCheck, toBefore, toCheck)
		return result, transferError(result, nil)
	}

	writeErr := s.AssignSourceItems(ctx, []SourceItem{
		{SKU: sku, SourceCode: fromSource, Quantity: result.IntendedFrom, Status: statusFor(result.IntendedFrom)},
		{SKU: sku, SourceCode: toSource, Quantity: result.IntendedTo, Status: statusFor(result.IntendedTo)},
	})

	fromAfter, toAfter, readErr := s.readSourcePair(ctx, sku, fromSource, toSource)
	if readErr != nil {
		result.Outcome = TransferNeedsVerification
		result.Detail = "the post-write read failed (" + string(errs.CodeOf(readErr)) +
			"); verify both source quantities before retrying"
		return result, transferError(result, writeErrOr(writeErr, readErr))
	}
	result.FromAfter, result.ToAfter = &fromAfter, &toAfter
	result.AfterObserved = true
	result.FromApplied = fromAfter == result.IntendedFrom
	result.ToApplied = toAfter == result.IntendedTo

	switch {
	case result.FromApplied && result.ToApplied:
		// The read-back is the ground truth: both rows hold the intended state,
		// even if the write reported an error after committing.
		result.Outcome = TransferApplied
		if writeErr != nil {
			result.Detail = "the write reported an error but both rows reached the intended state"
		}
		return result, nil
	case writeErr == nil:
		result.Outcome = TransferVerificationMismatch
		result.Detail = fmt.Sprintf(
			"Magento accepted the write but the read-back does not match the intended post-state (from %d, intended %d; to %d, intended %d)",
			fromAfter, result.IntendedFrom, toAfter, result.IntendedTo)
	case result.FromApplied != result.ToApplied:
		result.Outcome = TransferPartial
		result.Detail = fmt.Sprintf(
			"only the %s row was applied; the %s row was not (from %d, intended %d; to %d, intended %d)",
			result.AppliedRows(), otherRow(result.AppliedRows()), fromAfter, result.IntendedFrom, toAfter, result.IntendedTo)
	case fromAfter == fromBefore && toAfter == toBefore:
		if errs.CodeOf(writeErr) == errs.CodeUpstreamTimeout {
			// A timeout proves nothing: the response was lost while Magento may
			// still commit the write, so the transfer stays unverified.
			result.Outcome = TransferNeedsVerification
			result.Detail = "the write timed out with the source quantities unchanged; Magento may still commit it, verify both sources before retrying"
		} else {
			result.Outcome = TransferNotApplied
			result.Detail = "the write was rejected and neither source row changed"
		}
	default:
		result.Outcome = TransferNeedsVerification
		result.Detail = fmt.Sprintf(
			"the write failed and the observed state matches neither the before nor the intended state (from %d, before %d, intended %d; to %d, before %d, intended %d)",
			fromAfter, fromBefore, result.IntendedFrom, toAfter, toBefore, result.IntendedTo)
	}
	return result, transferError(result, writeErr)
}

// statusFor derives the source-item status from a quantity: a source holding no
// units is out of stock.
func statusFor(qty int) int {
	if qty <= 0 {
		return 0
	}
	return 1
}

// otherRow names the row that was not applied, for the partial report.
func otherRow(applied string) string {
	if applied == "from" {
		return "to"
	}
	return "from"
}

// writeErrOr returns the write error when present, otherwise the read error.
func writeErrOr(writeErr, readErr error) error {
	if writeErr != nil {
		return writeErr
	}
	return readErr
}

// observedAfter renders an observed post-quantity, or "unknown" when the
// read-back never happened.
func observedAfter(value *int) string {
	if value == nil {
		return "unknown"
	}
	return strconv.Itoa(*value)
}

// transferError renders a non-applied outcome as a typed error carrying the
// transfer id, the outcome, and the observed state, so an operator can decide
// what to do without issuing another read.
func transferError(result *TransferResult, cause error) error {
	code := errs.CodeConflict
	var message string
	switch result.Outcome {
	case TransferNotApplied:
		if cause != nil {
			code = errs.CodeOf(cause)
		}
		message = fmt.Sprintf("the transfer of %d units for SKU %q was rejected and neither source row changed", result.Quantity, result.SKU)
		if cause != nil {
			var typed *errs.Error
			if errors.As(cause, &typed) && typed.Message != "" {
				message += ": " + typed.Message
			}
		}
	case TransferPartial:
		message = fmt.Sprintf("the transfer of %d units for SKU %q is incomplete: only the %s row was applied; no compensation was attempted, verify both sources",
			result.Quantity, result.SKU, result.AppliedRows())
	case TransferNeedsVerification:
		message = fmt.Sprintf("the outcome of the transfer of %d units for SKU %q is unknown; verify both source quantities before retrying",
			result.Quantity, result.SKU)
	default:
		message = fmt.Sprintf("the transfer of %d units for SKU %q did not complete: %s",
			result.Quantity, result.SKU, result.Detail)
	}
	e := errs.New(code, message)
	if cause != nil {
		e = e.WithCause(cause)
	}
	if result.Outcome == TransferNotApplied {
		// The read-back proved that neither row changed, so retrying cannot
		// double-move stock. Every other outcome must not be retried blindly.
		e.Retryable = true
	}
	e = e.WithDetail("transfer_id", result.TransferID)
	e = e.WithDetail("outcome", string(result.Outcome))
	e = e.WithDetail("sku", result.SKU)
	e = e.WithDetail("from_source", result.FromSource)
	e = e.WithDetail("to_source", result.ToSource)
	e = e.WithDetail("quantity", strconv.Itoa(result.Quantity))
	e = e.WithDetail("from_observed", fmt.Sprintf("before=%d after=%s intended=%d",
		result.FromBefore, observedAfter(result.FromAfter), result.IntendedFrom))
	e = e.WithDetail("to_observed", fmt.Sprintf("before=%d after=%s intended=%d",
		result.ToBefore, observedAfter(result.ToAfter), result.IntendedTo))
	e = e.WithDetail("applied_rows", result.AppliedRows())
	e = e.WithDetail("after_observed", strconv.FormatBool(result.AfterObserved))
	return e
}

// readSourcePair returns the current quantities of sku at fromSource and
// toSource. A missing source item reads as 0, which is how MSI treats an
// unassigned source.
func (s *Service) readSourcePair(ctx context.Context, sku, fromSource, toSource string) (int, int, error) {
	items, err := s.SourceItems(ctx, []string{sku})
	if err != nil {
		return 0, 0, err
	}
	if len(items) >= maxSourceItemsRead {
		// The read hit its page cap, so a source row may be missing from the
		// response and would be mistaken for 0. Refuse to derive a write from an
		// incomplete observation (see MAG-13).
		return 0, 0, errs.New(errs.CodeInsufficientData,
			"the source-item read hit its page cap; the source quantities cannot be observed")
	}
	from, to := 0, 0
	for _, item := range items {
		switch item.SourceCode {
		case fromSource, toSource:
			whole, err := wholeQuantity(item)
			if err != nil {
				return 0, 0, err
			}
			if item.SourceCode == fromSource {
				from = whole
			} else {
				to = whole
			}
		}
	}
	return from, to, nil
}

// wholeQuantity converts an observed source quantity to whole units.
//
// The gateway writes integer quantities (SourceItem.Quantity is an int), so a
// fractional row is refused rather than truncated: reading 7.9 as 7, writing 7,
// and then reporting the transfer as applied would silently destroy stock
// (MAG-R07).
func wholeQuantity(item SourceItemRead) (int, error) {
	if item.Quantity != math.Trunc(item.Quantity) || item.Quantity < 0 {
		return 0, errs.New(errs.CodeUnsupportedCapability,
			"the observed source quantity is fractional or negative; this gateway transfers whole units only and will not truncate it").
			WithDetail("sku", item.SKU).
			WithDetail("source_code", item.SourceCode).
			WithDetail("quantity", strconv.FormatFloat(item.Quantity, 'f', -1, 64))
	}
	return int(item.Quantity), nil
}

// lockTransferSKU serialises transfers for one SKU inside this process. It is a
// mitigation, not a correctness guarantee: a second gateway replica has its own
// locks, which is why TransferSourceQty also re-reads the sources before writing.
func (s *Service) lockTransferSKU(sku string) func() {
	hasher := fnv.New32a()
	_, _ = fmt.Fprintf(hasher, "%p\x00%s", s.client, sku)
	lock := &transferLocks[hasher.Sum32()%transferLockStripes]
	lock.Lock()
	return lock.Unlock
}

// newTransferID returns a random, caller-visible transfer id.
func newTransferID() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", errs.New(errs.CodeInternal, "cannot generate a transfer id").WithCause(err)
	}
	return "tr_" + hex.EncodeToString(raw[:]), nil
}

// InventorySourceInput describes one MSI source to create.
type InventorySourceInput struct {
	SourceCode string
	Name       string
	Enabled    bool
	CountryID  string
	Region     string
	City       string
	Postcode   string
	Latitude   float64
	Longitude  float64
}

// CreateSource adds an MSI source with POST /inventory/sources.
func (s *Service) CreateSource(ctx context.Context, in InventorySourceInput) (*InventorySource, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if !sourceCodeRE.MatchString(in.SourceCode) {
		return nil, invalidf("source_code must match %s", sourceCodeRE.String())
	}
	if trimmed := strings.TrimSpace(in.Name); trimmed == "" || len(in.Name) > 255 {
		return nil, invalidf("name is required and must be at most 255 bytes")
	}
	if in.CountryID != "" && (len(in.CountryID) != 2 || strings.ToUpper(in.CountryID) != in.CountryID) {
		return nil, invalidf("country_id must be a two-letter uppercase ISO country code")
	}
	if in.Latitude < -90 || in.Latitude > 90 || in.Longitude < -180 || in.Longitude > 180 {
		return nil, invalidf("latitude must be -90..90 and longitude -180..180")
	}
	body := inventorySourceBody{Source: inventorySourcePayload(in)}
	raw, err := s.client.do(ctx, http.MethodPost, "/inventory/sources", nil, body)
	if err != nil {
		return nil, err
	}
	// A successful create answers with an empty array (`[]`) or no body at all,
	// not with the created object. Treating that as an unreadable payload told the
	// caller the create had failed when the source now exists, so an empty answer
	// is success and the source is read back by code.
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("[]")) || bytes.Equal(trimmed, []byte("null")) {
		return s.sourceByCode(ctx, in.SourceCode)
	}
	var source InventorySource
	if err := json.Unmarshal(trimmed, &source); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-source payload").WithCause(err)
	}
	return &source, nil
}

// sourceByCode reads one source back after a create that returned no body.
func (s *Service) sourceByCode(ctx context.Context, code string) (*InventorySource, error) {
	sources, _, err := s.Sources(ctx)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		if sources[i].SourceCode == code {
			return &sources[i], nil
		}
	}
	return nil, errs.New(errs.CodeUpstreamUnavailable,
		"Magento accepted the inventory-source create but the source is not listed; verify it before retrying").
		WithDetail("source_code", code)
}

// LinkStockSource attaches a source to a stock with
// POST /inventory/stock-source-links.
func (s *Service) LinkStockSource(ctx context.Context, stockID int, sourceCode string, position int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if stockID <= 0 {
		return invalidf("stock_id must be a positive integer")
	}
	if !sourceCodeRE.MatchString(sourceCode) {
		return invalidf("source_code must match %s", sourceCodeRE.String())
	}
	if position < 0 {
		return invalidf("position must not be negative")
	}
	body := stockSourceLinkBody{Links: []stockSourceLink{{StockID: stockID, SourceCode: sourceCode, Priority: position}}}
	_, err := s.client.do(ctx, http.MethodPost, "/inventory/stock-source-links", nil, body)
	return err
}

type inventorySourceBody struct {
	Source inventorySourcePayload `json:"source" jsonschema:"Where the value came from."`
}

type inventorySourcePayload struct {
	SourceCode string  `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Name       string  `json:"name" jsonschema:"Display name."`
	Enabled    bool    `json:"enabled" jsonschema:"True when the entry is enabled."`
	CountryID  string  `json:"country_id,omitempty" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Region     string  `json:"region,omitempty" jsonschema:"Region name."`
	City       string  `json:"city,omitempty" jsonschema:"City or locality."`
	Postcode   string  `json:"postcode,omitempty" jsonschema:"Postal code as the destination country writes it."`
	Latitude   float64 `json:"latitude,omitempty" jsonschema:"Latitude of the source."`
	Longitude  float64 `json:"longitude,omitempty" jsonschema:"Longitude of the source."`
}

type stockSourceLinkBody struct {
	Links []stockSourceLink `json:"links" jsonschema:"Product relations of the requested kind."`
}

type stockSourceLink struct {
	StockID    int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	// Magento's stock-source link endpoint takes `priority`, not `position`. Sending
	// `position` is rejected with "%fieldName is not supported", which read as a bad
	// store_code and hid the real cause.
	Priority int `json:"priority" jsonschema:"Sort position among siblings."`
}

// UnlinkStockSource detaches a source from a stock with
// POST /inventory/stock-source-links-delete. Products keep their source items; the
// stock simply stops selling from that source.
func (s *Service) UnlinkStockSource(ctx context.Context, stockID int, sourceCode string) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if stockID <= 0 {
		return invalidf("stock_id must be a positive integer")
	}
	if !sourceCodeRE.MatchString(sourceCode) {
		return invalidf("source_code must match %s", sourceCodeRE.String())
	}
	body := stockSourceLinkBody{Links: []stockSourceLink{{StockID: stockID, SourceCode: sourceCode}}}
	// Same shape as the source-item delete: POST with a -delete suffix, not DELETE.
	_, err := s.client.do(ctx, http.MethodPost, "/inventory/stock-source-links-delete", nil, body)
	return err
}
