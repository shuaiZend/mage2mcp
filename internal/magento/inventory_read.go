// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxSourceItemQuerySKUs = 20
	maxSourceItemsRead     = 100
)

// StockStatus is the legacy cataloginventory stock projection for one SKU.
type StockStatus struct {
	SKU                  string  `json:"sku" jsonschema:"Product SKU."`
	Qty                  float64 `json:"qty" jsonschema:"Quantity at this source."`
	IsInStock            bool    `json:"is_in_stock" jsonschema:"True when the product is sellable at this source."`
	ManageStock          bool    `json:"manage_stock" jsonschema:"True when Magento manages stock for this product."`
	UseConfigManageStock bool    `json:"use_config_manage_stock" jsonschema:"True when the source uses the store-wide stock settings."`
	Backorders           int     `json:"backorders" jsonschema:"Backorder policy: 0 no backorders, 1 allow below zero, 2 allow always."`
	MinQty               float64 `json:"min_qty" jsonschema:"Minimum quantity below which the product counts as out of stock."`
}

// SourceItemRead is the MSI quantity of one SKU at one source.
type SourceItemRead struct {
	SKU        string  `json:"sku" jsonschema:"Product SKU."`
	SourceCode string  `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Quantity   float64 `json:"quantity"`
	Status     int     `json:"status" jsonschema:"Current status."`
}

// StockStatus returns the legacy catalog inventory status for one SKU.
//
// On a multi-source installation this is the aggregated catalog value; use
// SourceItems for per-source quantities.
func (s *Service) StockStatus(ctx context.Context, sku string) (*StockStatus, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/stockItems/"+url.PathEscape(sku), nil, nil)
	if err != nil {
		return nil, err
	}
	var status StockStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable stock payload").WithCause(err)
	}
	status.SKU = sku
	return &status, nil
}

// SourceItems returns MSI source quantities for up to 20 SKUs.
func (s *Service) SourceItems(ctx context.Context, skus []string) ([]SourceItemRead, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if len(skus) == 0 {
		return nil, invalidf("at least one sku is required")
	}
	if len(skus) > maxSourceItemQuerySKUs {
		return nil, invalidf("at most %d skus are allowed per call", maxSourceItemQuerySKUs)
	}
	clean := make([]string, 0, len(skus))
	for _, sku := range skus {
		if err := validateSKU(sku); err != nil {
			return nil, err
		}
		clean = append(clean, sku)
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(maxSourceItemsRead))
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "sku")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", strings.Join(clean, ","))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "in")
	raw, err := s.client.do(ctx, http.MethodGet, "/inventory/source-items", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []SourceItemRead `json:"items" jsonschema:"Entries returned by this call."`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable source-item payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []SourceItemRead{}
	}
	return payload.Items, nil
}

const maxStockStatusBatch = 25

// StockStatusBatchItem is one SKU's stock result or its per-SKU error.
type StockStatusBatchItem struct {
	SKU    string       `json:"sku" jsonschema:"Product SKU."`
	Status *StockStatus `json:"status,omitempty" jsonschema:"Current status."`
	Error  string       `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// StockStatuses reads the stock status of up to 25 SKUs. Magento has no batch
// endpoint for catalog inventory, so the calls are sequential and each SKU
// reports its own error instead of failing the whole batch.
func (s *Service) StockStatuses(ctx context.Context, skus []string) ([]StockStatusBatchItem, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	if len(skus) == 0 {
		return nil, invalidf("at least one sku is required")
	}
	if len(skus) > maxStockStatusBatch {
		return nil, invalidf("at most %d skus are allowed per call", maxStockStatusBatch)
	}
	seen := make(map[string]struct{}, len(skus))
	results := make([]StockStatusBatchItem, 0, len(skus))
	for _, sku := range skus {
		if err := validateSKU(sku); err != nil {
			return nil, err
		}
		if _, duplicate := seen[sku]; duplicate {
			continue
		}
		seen[sku] = struct{}{}
		status, err := s.StockStatus(ctx, sku)
		if err != nil {
			results = append(results, StockStatusBatchItem{SKU: sku, Error: importErrorMessage(err)})
			continue
		}
		results = append(results, StockStatusBatchItem{SKU: sku, Status: status})
	}
	return results, nil
}

const maxInventoryList = 100

// InventorySource is one MSI source.
type InventorySource struct {
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

// InventoryStock is one MSI stock (a sales channel group).
type InventoryStock struct {
	StockID int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	Name    string `json:"name" jsonschema:"Display name."`
}

// Sources lists the configured inventory sources and reports how many the store
// holds.
//
// The list is capped at maxInventoryList, so a caller that only receives the slice
// cannot tell a complete answer from a capped one — and could conclude a source
// does not exist when it is merely past the cap. The total comes from the store
// and is at least the number returned.
func (s *Service) Sources(ctx context.Context) ([]InventorySource, int, error) {
	if s == nil || s.client == nil {
		return nil, 0, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(maxInventoryList))
	raw, err := s.client.do(ctx, http.MethodGet, "/inventory/sources", query, nil)
	if err != nil {
		return nil, 0, err
	}
	var payload struct {
		Items      []InventorySource `json:"items" jsonschema:"Entries returned by this call."`
		TotalCount int               `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-source payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []InventorySource{}
	}
	total := payload.TotalCount
	if total < len(payload.Items) {
		total = len(payload.Items)
	}
	return payload.Items, total, nil
}

// Stocks lists the configured inventory stocks.
func (s *Service) Stocks(ctx context.Context) ([]InventoryStock, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(maxInventoryList))
	raw, err := s.client.do(ctx, http.MethodGet, "/inventory/stocks", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []InventoryStock `json:"items" jsonschema:"Entries returned by this call."`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-stock payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []InventoryStock{}
	}
	return payload.Items, nil
}
