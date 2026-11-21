// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// linkTypes are the product link relations Magento exposes.
var linkTypes = map[string]bool{"related": true, "upsell": true, "crosssell": true}

// ProductLink is one product-to-product relation.
type ProductLink struct {
	SKU              string `json:"sku" jsonschema:"Product SKU."`
	LinkType         string `json:"link_type" jsonschema:"Relation kind: related, upsell or crosssell."`
	LinkedProductSKU string `json:"linked_product_sku" jsonschema:"The other product in the relation."`
	Position         int    `json:"position" jsonschema:"Sort position among siblings."`
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
		// Some deployments do not expose the links route; the product resource
		// carries the same product_links array.
		if errs.CodeOf(err) != errs.CodeNotFound {
			return nil, err
		}
		return s.productLinksFromProduct(ctx, sku, linkType)
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

const maxLinkPosition = 10000

// ProductLinkInput is one link creation request.
type ProductLinkInput struct {
	SKU              string `json:"sku" jsonschema:"Product SKU."`
	LinkType         string `json:"link_type" jsonschema:"Relation kind: related, upsell or crosssell."`
	LinkedProductSKU string `json:"linked_product_sku" jsonschema:"The other product in the relation."`
	Position         int    `json:"position" jsonschema:"Sort position among siblings."`
}

// AddProductLink creates a relation with POST /products/{sku}/links.
func (s *Service) AddProductLink(ctx context.Context, in ProductLinkInput) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateProductLink(in); err != nil {
		return err
	}
	body := productLinkBody{Items: []ProductLinkInput{in}}
	_, err := s.client.do(ctx, http.MethodPost, "/products/"+url.PathEscape(in.SKU)+"/links", nil, body)
	return err
}

// RemoveProductLink deletes a relation with
// DELETE /products/{sku}/links/{type}/{linkedSku}.
func (s *Service) RemoveProductLink(ctx context.Context, sku, linkType, linkedSKU string) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if err := validateSKU(linkedSKU); err != nil {
		return err
	}
	if sku == linkedSKU {
		return invalidf("a product cannot link to itself")
	}
	if !linkTypes[linkType] {
		return invalidf("link_type must be related, upsell, or crosssell")
	}
	path := "/products/" + url.PathEscape(sku) + "/links/" + url.PathEscape(linkType) + "/" + url.PathEscape(linkedSKU)
	_, err := s.client.do(ctx, http.MethodDelete, path, nil, nil)
	return err
}

// productLinkBody wraps one link in the array shape the endpoint expects.
//
// POST /products/{sku}/links takes `{"items": [...]}`; sending `{"entity": ...}`
// is rejected with `"%fieldName" is required` naming `items`.
type productLinkBody struct {
	Items []ProductLinkInput `json:"items" jsonschema:"Entries returned by this call."`
}

func validateProductLink(in ProductLinkInput) error {
	if err := validateSKU(in.SKU); err != nil {
		return err
	}
	if err := validateSKU(in.LinkedProductSKU); err != nil {
		return err
	}
	if in.SKU == in.LinkedProductSKU {
		return invalidf("a product cannot link to itself")
	}
	if !linkTypes[in.LinkType] {
		return invalidf("link_type must be related, upsell, or crosssell")
	}
	if in.Position < 0 || in.Position > maxLinkPosition {
		return invalidf("position must be between 0 and %d", maxLinkPosition)
	}
	return nil
}

const maxBulkLinks = 50

// LinkBulkRow is one target SKU's outcome in a bulk link operation.
type LinkBulkRow struct {
	SKU    string `json:"sku" jsonschema:"Product SKU."`
	Status string `json:"status" jsonschema:"Current status."`
	Error  string `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// LinkBulkResult reports a partial-success bulk link change.
type LinkBulkResult struct {
	Total   int           `json:"total" jsonschema:"Number of link targets in this request."`
	Changed int           `json:"changed" jsonschema:"Fields that actually changed."`
	Failed  int           `json:"failed" jsonschema:"Rows that failed."`
	Rows    []LinkBulkRow `json:"rows" jsonschema:"Per-row results, in input order."`
}

// BulkChangeLinks adds or removes one link type for up to 50 target SKUs.
// Each target is a separate call; failures are reported per row and earlier
// changes are not rolled back.
func (s *Service) BulkChangeLinks(ctx context.Context, sku, linkType, action string, targets []string) (*LinkBulkResult, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if !linkTypes[linkType] {
		return nil, invalidf("link_type must be related, upsell, or crosssell")
	}
	if action != "add" && action != "remove" {
		return nil, invalidf("action must be add or remove")
	}
	if len(targets) == 0 {
		return nil, invalidf("at least one target sku is required")
	}
	if len(targets) > maxBulkLinks {
		return nil, invalidf("at most %d target skus are allowed per call", maxBulkLinks)
	}
	result := &LinkBulkResult{Total: len(targets), Rows: make([]LinkBulkRow, 0, len(targets))}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if err := validateSKU(target); err != nil {
			return nil, err
		}
		key := strings.ToUpper(target)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		row := LinkBulkRow{SKU: target}
		var err error
		if action == "add" {
			err = s.AddProductLink(ctx, ProductLinkInput{SKU: sku, LinkedProductSKU: target, LinkType: linkType})
		} else {
			err = s.RemoveProductLink(ctx, sku, linkType, target)
		}
		if err != nil {
			row.Status = "failed"
			row.Error = importErrorMessage(err)
			result.Failed++
		} else {
			row.Status = "changed"
			result.Changed++
		}
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

// productLinksFromProduct filters the product_links array embedded in the
// product resource when the dedicated links route is unavailable.
func (s *Service) productLinksFromProduct(ctx context.Context, sku, linkType string) ([]ProductLink, error) {
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		ProductLinks []ProductLink `json:"product_links"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	links := make([]ProductLink, 0, len(payload.ProductLinks))
	for _, link := range payload.ProductLinks {
		if link.LinkType == linkType {
			links = append(links, link)
		}
	}
	return links, nil
}
