// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
	maxNameQueryBytes  = 128
)

var (
	skuRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	typeIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Product is the explicit product projection this slice exposes.
type Product struct {
	ID             int     `json:"id"`
	SKU            string  `json:"sku"`
	Name           string  `json:"name"`
	TypeID         string  `json:"type_id"`
	Status         int     `json:"status"`
	Visibility     int     `json:"visibility"`
	Price          float64 `json:"price"`
	AttributeSetID int     `json:"attribute_set_id"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

// ProductQuery selects products; zero values mean no filter except Limit (50).
// CursorOffset is a zero-based row offset and must be a multiple of Limit.
type ProductQuery struct {
	SKU          string // exact SKU filter
	NameContains string // substring filter on the product name
	TypeID       string // Magento product type code
	Status       int    // 1 enabled, 2 disabled, 0 any
	Limit        int    // page size 1-200; zero selects 50
	CursorOffset int    // zero-based row offset
}

// Service is the read-only Magento product service.
type Service struct {
	client *Client
}

// NewService returns a product service backed by client.
func NewService(client *Client) (*Service, error) {
	if client == nil {
		return nil, invalidf("Magento product service requires a client")
	}
	return &Service{client: client}, nil
}

// Get returns one product by SKU after validating the SKU.
func (s *Service) Get(ctx context.Context, sku string) (*Product, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku), nil, nil)
	if err != nil {
		return nil, err
	}
	var product Product
	if err := json.Unmarshal(raw, &product); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	return &product, nil
}

// Search returns one page of products and the total match count.
func (s *Service) Search(ctx context.Context, q ProductQuery) ([]Product, int, error) {
	if s == nil || s.client == nil {
		return nil, 0, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	limit := q.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, 0, invalidf("limit must be between 1 and %d", maxSearchLimit)
	}
	if q.CursorOffset < 0 || q.CursorOffset%limit != 0 {
		return nil, 0, invalidf("cursor offset must be a non-negative multiple of limit")
	}
	if q.SKU != "" {
		if err := validateSKU(q.SKU); err != nil {
			return nil, 0, err
		}
	}
	if q.NameContains != "" && (len(q.NameContains) > maxNameQueryBytes || strings.ContainsFunc(q.NameContains, unicode.IsControl)) {
		return nil, 0, invalidf("name query must be at most %d bytes and contain no control characters", maxNameQueryBytes)
	}
	if q.TypeID != "" && !typeIDRE.MatchString(q.TypeID) {
		return nil, 0, invalidf("type_id is not a valid Magento product type code")
	}
	if q.Status != 0 && q.Status != 1 && q.Status != 2 {
		return nil, 0, invalidf("status must be 1 (enabled) or 2 (disabled)")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(limit))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(q.CursorOffset/limit+1))
	group := 0
	addFilter := func(field, value, condition string) {
		prefix := fmt.Sprintf("searchCriteria[filter_groups][%d][filters][0]", group)
		query.Set(prefix+"[field]", field)
		query.Set(prefix+"[value]", value)
		query.Set(prefix+"[condition_type]", condition)
		group++
	}
	if q.SKU != "" {
		addFilter("sku", q.SKU, "eq")
	}
	if q.NameContains != "" {
		addFilter("name", "%"+q.NameContains+"%", "like")
	}
	if q.TypeID != "" {
		addFilter("type_id", q.TypeID, "eq")
	}
	if q.Status != 0 {
		addFilter("status", strconv.Itoa(q.Status), "eq")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products", query, nil)
	if err != nil {
		return nil, 0, err
	}
	var payload struct {
		Items      []Product `json:"items"`
		TotalCount int       `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product search payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []Product{}
	}
	return payload.Items, payload.TotalCount, nil
}
func validateSKU(sku string) error {
	if !skuRE.MatchString(sku) {
		return invalidf("sku must be 1-64 characters from [A-Za-z0-9._:-] and start with a letter or digit")
	}
	return nil
}
