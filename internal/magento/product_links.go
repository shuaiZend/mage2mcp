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

const maxLinkPosition = 10000

// ProductLinkInput is one link creation request.
type ProductLinkInput struct {
	SKU              string `json:"sku"`
	LinkType         string `json:"link_type"`
	LinkedProductSKU string `json:"linked_product_sku"`
	Position         int    `json:"position"`
}

// AddProductLink creates a relation with POST /products/{sku}/links.
func (s *Service) AddProductLink(ctx context.Context, in ProductLinkInput) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateProductLink(in); err != nil {
		return err
	}
	body := productLinkBody{Entity: in}
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

type productLinkBody struct {
	Entity ProductLinkInput `json:"entity"`
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
	SKU    string `json:"sku"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// LinkBulkResult reports a partial-success bulk link change.
type LinkBulkResult struct {
	Total   int           `json:"total"`
	Changed int           `json:"changed"`
	Failed  int           `json:"failed"`
	Rows    []LinkBulkRow `json:"rows"`
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
