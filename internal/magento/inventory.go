// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"regexp"
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
