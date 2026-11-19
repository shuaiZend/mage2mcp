// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const maxBulkUpdateSKUs = 50

// BulkUpdateRow is one SKU's outcome in a bulk update.
type BulkUpdateRow struct {
	SKU    string `json:"sku"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// BulkUpdateResult reports a partial-success bulk update. Updates are applied
// per SKU and never rolled back.
type BulkUpdateResult struct {
	Total   int             `json:"total"`
	Updated int             `json:"updated"`
	Failed  int             `json:"failed"`
	Rows    []BulkUpdateRow `json:"rows"`
}

// BulkUpdateProducts applies the same partial update to up to 50 SKUs.
//
// Each SKU is a separate PUT; failures are recorded per SKU and earlier updates
// are not rolled back. The caller must treat the result as partial success.
func (s *Service) BulkUpdateProducts(ctx context.Context, skus []string, update UpdateProductInput) (*BulkUpdateResult, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if len(skus) == 0 {
		return nil, invalidf("at least one sku is required")
	}
	if len(skus) > maxBulkUpdateSKUs {
		return nil, invalidf("at most %d skus are allowed per call", maxBulkUpdateSKUs)
	}
	if update.Name != nil || update.Price != nil {
		return nil, invalidf("bulk update supports status, visibility, and attributes only")
	}
	if update.Status == nil && update.Visibility == nil && len(update.Attributes) == 0 {
		return nil, invalidf("at least one of status, visibility, or attributes is required")
	}
	seen := make(map[string]struct{}, len(skus))
	result := &BulkUpdateResult{Total: len(skus), Rows: make([]BulkUpdateRow, 0, len(skus))}
	for _, sku := range skus {
		if err := validateSKU(sku); err != nil {
			return nil, err
		}
		key := strings.ToUpper(sku)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		row := BulkUpdateRow{SKU: sku}
		if _, err := s.Update(ctx, sku, update); err != nil {
			row.Status = "failed"
			row.Error = importErrorMessage(err)
			result.Failed++
		} else {
			row.Status = "updated"
			result.Updated++
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}
