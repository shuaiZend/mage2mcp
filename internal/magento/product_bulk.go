// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// maxBulkWriteConcurrency bounds the in-flight writes of one bulk call. The client
// limiter is the real cap on concurrent requests; this keeps the goroutine count from
// running far ahead of it.
const maxBulkWriteConcurrency = 8

const maxBulkUpdateSKUs = 50

// BulkUpdateRow is one SKU's outcome in a bulk update.
type BulkUpdateRow struct {
	SKU    string `json:"sku" jsonschema:"Product SKU."`
	Status string `json:"status" jsonschema:"Current status."`
	Error  string `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// BulkUpdateResult reports a partial-success bulk update. Updates are applied
// per SKU and never rolled back.
type BulkUpdateResult struct {
	Total   int             `json:"total" jsonschema:"Number of SKUs in this request."`
	Updated int             `json:"updated" jsonschema:"Rows that updated a product."`
	Failed  int             `json:"failed" jsonschema:"Rows that failed."`
	Rows    []BulkUpdateRow `json:"rows" jsonschema:"Per-row results, in input order."`
}

// ensureBulkSKUsExist rejects a bulk request that names a SKU with no product.
//
// The tool schema promises "every SKU must exist or the whole call is rejected";
// without this check a typo produced a per-row failure (or, worse, a silent
// no-op) while the caller read the result as partial success. The check is
// best-effort: if the product search is unavailable the writes proceed and the
// per-row results remain the record.
func (s *Service) ensureBulkSKUsExist(ctx context.Context, skus []string) error {
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(len(skus)))
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "sku")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", strings.Join(skus, ","))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "in")
	raw, err := s.client.do(ctx, http.MethodGet, "/products", query, nil)
	if err != nil {
		return nil
	}
	var payload struct {
		Items []struct {
			SKU string `json:"sku"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if payload.Items == nil {
		// The response is not a product-search result (for example an instance
		// whose search route answers with a product object): treat the check as
		// unavailable rather than rejecting every SKU.
		return nil
	}
	found := make(map[string]struct{}, len(payload.Items))
	for _, item := range payload.Items {
		found[strings.ToUpper(item.SKU)] = struct{}{}
	}
	missing := make([]string, 0, len(skus))
	for _, sku := range skus {
		if _, ok := found[strings.ToUpper(sku)]; !ok {
			missing = append(missing, sku)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return errs.New(errs.CodeInvalidArgument,
		"unknown SKU(s): "+strings.Join(missing, ", ")+"; no product was updated").
		WithDetail("unknown_skus", strings.Join(missing, ","))
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
	for _, sku := range skus {
		if err := validateSKU(sku); err != nil {
			return nil, err
		}
		key := strings.ToUpper(sku)
		if _, duplicate := seen[key]; duplicate {
			// A duplicate used to be folded silently, which made Total disagree
			// with the number of writes. Reject it instead: the schema promises
			// that every SKU in the request is acted on.
			return nil, invalidf("sku %q is duplicated in the request", sku)
		}
		seen[key] = struct{}{}
	}
	if err := s.ensureBulkSKUsExist(ctx, skus); err != nil {
		return nil, err
	}
	unique := skus
	// Bounded fan-out: the writes are independent and partial success is already
	// modelled per row, so running them one at a time only multiplied the round-trips.
	// The client limiter remains the real cap on concurrent requests.
	rows := make([]BulkUpdateRow, len(unique))
	sem := make(chan struct{}, maxBulkWriteConcurrency)
	var wg sync.WaitGroup
	for i, sku := range unique {
		wg.Add(1)
		go func(i int, sku string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				rows[i] = BulkUpdateRow{SKU: sku, Status: "failed", Error: "the request was canceled"}
				return
			}
			defer func() { <-sem }()
			if _, err := s.Update(ctx, sku, update); err != nil {
				rows[i] = BulkUpdateRow{SKU: sku, Status: "failed", Error: importErrorMessage(err)}
				return
			}
			rows[i] = BulkUpdateRow{SKU: sku, Status: "updated"}
		}(i, sku)
	}
	wg.Wait()
	result := &BulkUpdateResult{Total: len(skus), Rows: rows}
	for _, row := range rows {
		if row.Status == "failed" {
			result.Failed++
			continue
		}
		result.Updated++
	}
	return result, nil
}
