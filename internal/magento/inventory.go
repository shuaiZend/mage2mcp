// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

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
	SKU        string `json:"sku"`
	SourceCode string `json:"source_code"`
	Quantity   int    `json:"quantity"`
	Status     int    `json:"status"`
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
	SKU        string `json:"sku"`
	SourceCode string `json:"source_code"`
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
	_, err := s.client.do(ctx, http.MethodDelete, "/inventory/source-items", nil, body)
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

// TransferSourceQty moves quantity between two sources for one SKU.
//
// Magento has no transfer endpoint, so the gateway reads the current source
// items, refuses an insufficient source, then upserts both rows in one batch.
// The batch is not transactional: a failure after the write can leave one row
// updated, so the tool is destructive and confirmation-gated.
func (s *Service) TransferSourceQty(ctx context.Context, sku, fromSource, toSource string, qty int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if !sourceCodeRE.MatchString(fromSource) || !sourceCodeRE.MatchString(toSource) {
		return invalidf("source codes must match %s", sourceCodeRE.String())
	}
	if fromSource == toSource {
		return invalidf("from_source and to_source must differ")
	}
	if qty <= 0 {
		return invalidf("quantity must be greater than zero")
	}
	items, err := s.SourceItems(ctx, []string{sku})
	if err != nil {
		return err
	}
	fromQty, toQty := 0, 0
	for _, item := range items {
		switch item.SourceCode {
		case fromSource:
			fromQty = int(item.Quantity)
		case toSource:
			toQty = int(item.Quantity)
		}
	}
	if fromQty < qty {
		return invalidf("source %q has %d units, cannot transfer %d", fromSource, fromQty, qty)
	}
	newFrom := fromQty - qty
	newTo := toQty + qty
	batch := []SourceItem{
		{SKU: sku, SourceCode: fromSource, Quantity: newFrom, Status: statusFor(newFrom)},
		{SKU: sku, SourceCode: toSource, Quantity: newTo, Status: statusFor(newTo)},
	}
	return s.AssignSourceItems(ctx, batch)
}

func statusFor(qty int) int {
	if qty <= 0 {
		return 0
	}
	return 1
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
	var source InventorySource
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-source payload").WithCause(err)
	}
	return &source, nil
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
	body := stockSourceLinkBody{Links: []stockSourceLink{{StockID: stockID, SourceCode: sourceCode, Position: position}}}
	_, err := s.client.do(ctx, http.MethodPost, "/inventory/stock-source-links", nil, body)
	return err
}

type inventorySourceBody struct {
	Source inventorySourcePayload `json:"source"`
}

type inventorySourcePayload struct {
	SourceCode string  `json:"source_code"`
	Name       string  `json:"name"`
	Enabled    bool    `json:"enabled"`
	CountryID  string  `json:"country_id,omitempty"`
	Region     string  `json:"region,omitempty"`
	City       string  `json:"city,omitempty"`
	Postcode   string  `json:"postcode,omitempty"`
	Latitude   float64 `json:"latitude,omitempty"`
	Longitude  float64 `json:"longitude,omitempty"`
}

type stockSourceLinkBody struct {
	Links []stockSourceLink `json:"links"`
}

type stockSourceLink struct {
	StockID    int    `json:"stock_id"`
	SourceCode string `json:"source_code"`
	Position   int    `json:"position"`
}
