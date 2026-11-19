// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// ProductOptionValue is one value of a custom option.
type ProductOptionValue struct {
	OptionTypeID int     `json:"option_type_id"`
	Title        string  `json:"title"`
	Price        float64 `json:"price"`
	PriceType    string  `json:"price_type"`
	SKU          string  `json:"sku,omitempty"`
	SortOrder    int     `json:"sort_order"`
}

// ProductOption is one custom option (not a configurable variant option).
type ProductOption struct {
	OptionID  int                  `json:"option_id"`
	Title     string               `json:"title"`
	Type      string               `json:"type"`
	IsRequire bool                 `json:"is_require"`
	SortOrder int                  `json:"sort_order"`
	Values    []ProductOptionValue `json:"values"`
}

// ProductOptions lists a product's custom options with
// GET /products/{sku}/options.
func (s *Service) ProductOptions(ctx context.Context, sku string) ([]ProductOption, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku)+"/options", nil, nil)
	if err != nil {
		return nil, err
	}
	var options []ProductOption
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product-option payload").WithCause(err)
	}
	if options == nil {
		options = []ProductOption{}
	}
	return options, nil
}
