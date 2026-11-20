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
	"sync"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
	maxBatchGetSKUs    = 50
	maxNameQueryBytes  = 128
)

var (
	skuRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	typeIDRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Product is the explicit product projection this slice exposes.
type Product struct {
	ID             int     `json:"id" jsonschema:"Magento entity id. Stable within the store, not globally unique."`
	SKU            string  `json:"sku" jsonschema:"Stock keeping unit. The identifier every other product tool takes."`
	Name           string  `json:"name" jsonschema:"Display name as shown in the storefront."`
	TypeID         string  `json:"type_id" jsonschema:"simple, virtual, configurable, bundle, downloadable or grouped."`
	Status         int     `json:"status" jsonschema:"1 enabled, 2 disabled."`
	Visibility     int     `json:"visibility" jsonschema:"1 not visible, 2 catalog, 3 search, 4 catalog and search."`
	Price          float64 `json:"price" jsonschema:"Base price in the store currency, as a number, not a string."`
	AttributeSetID int     `json:"attribute_set_id" jsonschema:"Attribute set the product belongs to; it decides which custom attributes exist."`
	CreatedAt      string  `json:"created_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	UpdatedAt      string  `json:"updated_at" jsonschema:"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
	CategoryIDs    []int   `json:"category_ids,omitempty" jsonschema:"Categories the product is assigned to. Absent when the product has none."`
	WebsiteIDs     []int   `json:"website_ids,omitempty" jsonschema:"Websites the product is published to. Absent when it is on none."`

	Description      string  `json:"description,omitempty" jsonschema:"Full description, HTML allowed. Absent when empty."`
	ShortDescription string  `json:"short_description,omitempty" jsonschema:"Short description, HTML allowed. Absent when empty."`
	URLKey           string  `json:"url_key,omitempty" jsonschema:"URL path segment for the product page. Absent when unset."`
	MetaTitle        string  `json:"meta_title,omitempty" jsonschema:"SEO title tag. 30-60 characters is the useful range."`
	MetaDescription  string  `json:"meta_description,omitempty" jsonschema:"SEO meta description. 120-160 characters is the useful range."`
	MetaKeywords     string  `json:"meta_keywords,omitempty" jsonschema:"Comma-separated keywords. Low ranking value; keep it short."`
	Weight           float64 `json:"weight,omitempty" jsonschema:"Shipping weight in the store's weight unit. Absent for virtual products."`

	// Attributes carries the product's custom attributes (for example the
	// variant attribute of a configurable child) as string values.
	Attributes map[string]string `json:"attributes,omitempty" jsonschema:"Custom attribute code to value, always as strings. Empty attributes are omitted."`
}

// UnmarshalJSON lifts category_links and website_ids out of extension_attributes
// so callers can analyse product placement without a second read.
func (p *Product) UnmarshalJSON(data []byte) error {
	type productAlias Product
	aux := &struct {
		*productAlias
		ExtensionAttributes struct {
			CategoryLinks []struct {
				CategoryID any `json:"category_id"`
			} `json:"category_links"`
			WebsiteIDs []int `json:"website_ids"`
		} `json:"extension_attributes"`
		CustomAttributes []struct {
			AttributeCode string          `json:"attribute_code"`
			Value         json.RawMessage `json:"value"`
		} `json:"custom_attributes"`
	}{productAlias: (*productAlias)(p)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	if len(aux.CustomAttributes) > 0 {
		p.Attributes = make(map[string]string, len(aux.CustomAttributes))
		for _, attribute := range aux.CustomAttributes {
			var text string
			if err := json.Unmarshal(attribute.Value, &text); err != nil {
				var number json.Number
				if err := json.Unmarshal(attribute.Value, &number); err == nil {
					text = number.String()
				} else {
					continue
				}
			}
			p.Attributes[attribute.AttributeCode] = text
		}
		if p.Description == "" {
			p.Description = p.Attributes["description"]
		}
		if p.ShortDescription == "" {
			p.ShortDescription = p.Attributes["short_description"]
		}
		if p.URLKey == "" {
			p.URLKey = p.Attributes["url_key"]
		}
		if p.MetaTitle == "" {
			p.MetaTitle = p.Attributes["meta_title"]
		}
		if p.MetaDescription == "" {
			p.MetaDescription = p.Attributes["meta_description"]
		}
		if p.MetaKeywords == "" {
			p.MetaKeywords = p.Attributes["meta_keywords"]
		}
		if p.Weight == 0 {
			if rawWeight := p.Attributes["weight"]; rawWeight != "" {
				if parsed, err := strconv.ParseFloat(rawWeight, 64); err == nil {
					p.Weight = parsed
				}
			}
		}
	}
	p.CategoryIDs = p.CategoryIDs[:0]
	for _, link := range aux.ExtensionAttributes.CategoryLinks {
		switch v := link.CategoryID.(type) {
		case string:
			if id, err := strconv.Atoi(v); err == nil {
				p.CategoryIDs = append(p.CategoryIDs, id)
			}
		case float64:
			p.CategoryIDs = append(p.CategoryIDs, int(v))
		}
	}
	p.WebsiteIDs = aux.ExtensionAttributes.WebsiteIDs
	return nil
}

// ProductQuery selects products; zero values mean no filter except Limit (50).
// CursorOffset is a zero-based row offset and must be a multiple of Limit.
type ProductQuery struct {
	SKU          string // exact SKU filter
	NameContains string // substring filter on the product name
	TypeID       string // Magento product type code
	Status       int    // 1 enabled, 2 disabled, 0 any
	PriceFrom    string // decimal lower bound (inclusive)
	PriceTo      string // decimal upper bound (inclusive)
	CreatedFrom  string // RFC 3339 or YYYY-MM-DD, inclusive
	CreatedTo    string // RFC 3339 or YYYY-MM-DD, exclusive
	Limit        int    // page size 1-200; zero selects 50
	CursorOffset int    // zero-based row offset
}

// Service is the read-only Magento product service.
type Service struct {
	client *Client
	// media fingerprints dedupe a repeated upload of the same bytes. The cache is
	// per-service (one per configured instance) and bounded; it is a best-effort
	// optimisation, so an entry that has gone stale is verified against the live
	// gallery before it is reused.
	mediaMu    sync.Mutex
	mediaByKey map[string]mediaFingerprint
	mediaOrder []string
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
	if err := validatePriceBound(q.PriceFrom, "price_from"); err != nil {
		return nil, 0, err
	}
	if err := validatePriceBound(q.PriceTo, "price_to"); err != nil {
		return nil, 0, err
	}
	createdFrom, err := parseOrderTime(q.CreatedFrom, "created_from")
	if err != nil {
		return nil, 0, err
	}
	createdTo, err := parseOrderTime(q.CreatedTo, "created_to")
	if err != nil {
		return nil, 0, err
	}
	if createdFrom != "" && createdTo != "" && createdTo < createdFrom {
		return nil, 0, invalidf("created_to must not be before created_from")
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
	if q.PriceFrom != "" {
		addFilter("price", q.PriceFrom, "gteq")
	}
	if q.PriceTo != "" {
		addFilter("price", q.PriceTo, "lteq")
	}
	if createdFrom != "" {
		addFilter("created_at", createdFrom, "gteq")
	}
	if createdTo != "" {
		addFilter("created_at", createdTo, "lt")
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

// BatchGet returns up to 50 products by SKU in one search.
func (s *Service) BatchGet(ctx context.Context, skus []string) ([]Product, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if len(skus) == 0 {
		return nil, invalidf("at least one sku is required")
	}
	if len(skus) > maxBatchGetSKUs {
		return nil, invalidf("at most %d skus are allowed per call", maxBatchGetSKUs)
	}
	seen := make(map[string]struct{}, len(skus))
	clean := make([]string, 0, len(skus))
	for _, sku := range skus {
		if err := validateSKU(sku); err != nil {
			return nil, err
		}
		if _, duplicate := seen[sku]; duplicate {
			continue
		}
		seen[sku] = struct{}{}
		clean = append(clean, sku)
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(len(clean)))
	query.Set("searchCriteria[filter_groups][0][filters][0][field]", "sku")
	query.Set("searchCriteria[filter_groups][0][filters][0][value]", strings.Join(clean, ","))
	query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "in")
	raw, err := s.client.do(ctx, http.MethodGet, "/products", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []Product `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []Product{}
	}
	return payload.Items, nil
}

var priceBoundRE = regexp.MustCompile(`^\d{1,12}(\.\d{1,4})?$`)

func validatePriceBound(value, field string) error {
	if value == "" {
		return nil
	}
	if !priceBoundRE.MatchString(value) {
		return invalidf("%s must be a non-negative decimal with at most 4 fraction digits", field)
	}
	return nil
}

func validateSKU(sku string) error {
	if !skuRE.MatchString(sku) {
		return invalidf("sku must be 1-64 characters from [A-Za-z0-9._:-] and start with a letter or digit")
	}
	return nil
}
