// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// linkTypes are the product link relations Magento exposes.
var linkTypes = map[string]bool{"related": true, "upsell": true, "crosssell": true}

// ProductLink is one product-to-product relation.
type ProductLink struct {
	SKU              string `json:"sku"`
	LinkType         string `json:"link_type"`
	LinkedProductSKU string `json:"linked_product_sku"`
	Position         int    `json:"position"`
}

// ProductLinks lists the related, upsell, or cross-sell links of a product.
func (s *Service) ProductLinks(ctx context.Context, sku, linkType string) ([]ProductLink, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if !linkTypes[linkType] {
		return nil, invalidf("link_type must be related, upsell, or crosssell")
	}
	query := make(url.Values)
	query.Set("linkType", linkType)
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku)+"/links", query, nil)
	if err != nil {
		return nil, err
	}
	var links []ProductLink
	if err := json.Unmarshal(raw, &links); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product-link payload").WithCause(err)
	}
	if links == nil {
		links = []ProductLink{}
	}
	return links, nil
}
