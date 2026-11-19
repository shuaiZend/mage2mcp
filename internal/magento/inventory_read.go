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
	SKU                  string  `json:"sku"`
	Qty                  float64 `json:"qty"`
	IsInStock            bool    `json:"is_in_stock"`
	ManageStock          bool    `json:"manage_stock"`
	UseConfigManageStock bool    `json:"use_config_manage_stock"`
	Backorders           int     `json:"backorders"`
	MinQty               float64 `json:"min_qty"`
}

// SourceItemRead is the MSI quantity of one SKU at one source.
type SourceItemRead struct {
	SKU        string  `json:"sku"`
	SourceCode string  `json:"source_code"`
	Quantity   float64 `json:"quantity"`
	Status     int     `json:"status"`
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
		Items []SourceItemRead `json:"items"`
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
	SKU    string       `json:"sku"`
	Status *StockStatus `json:"status,omitempty"`
	Error  string       `json:"error,omitempty"`
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

// InventoryStock is one MSI stock (a sales channel group).
type InventoryStock struct {
	StockID int    `json:"stock_id"`
	Name    string `json:"name"`
}

// Sources lists the configured inventory sources.
func (s *Service) Sources(ctx context.Context) ([]InventorySource, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento inventory service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(maxInventoryList))
	raw, err := s.client.do(ctx, http.MethodGet, "/inventory/sources", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []InventorySource `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-source payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []InventorySource{}
	}
	return payload.Items, nil
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
		Items []InventoryStock `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable inventory-stock payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []InventoryStock{}
	}
	return payload.Items, nil
}
