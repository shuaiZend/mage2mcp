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
	maxTierPrices = 50
)

// TierPrice is one quantity-based price for a customer group and website.
type TierPrice struct {
	SKU           string  `json:"sku" jsonschema:"Product SKU."`
	Price         float64 `json:"price"`
	PriceType     string  `json:"price_type"`
	WebsiteID     int     `json:"website_id" jsonschema:"Website the entity belongs to."`
	CustomerGroup string  `json:"customer_group"`
	Quantity      float64 `json:"quantity"`
}

// TierPrices returns the tier prices of one SKU.
func (s *Service) TierPrices(ctx context.Context, sku string) ([]TierPrice, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	query := make(url.Values)
	query.Set("skus", sku)
	raw, err := s.client.do(ctx, http.MethodGet, "/products/tier-prices", query, nil)
	if err != nil {
		// Older Magento builds and some hosting configurations do not expose the
		// tier-price storage route. The product resource still carries the same
		// data, so fall back to it instead of failing the read.
		if errs.CodeOf(err) != errs.CodeNotFound {
			return nil, err
		}
		return s.tierPricesFromProduct(ctx, sku)
	}
	var prices []TierPrice
	if err := json.Unmarshal(raw, &prices); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable tier-price payload").WithCause(err)
	}
	if prices == nil {
		prices = []TierPrice{}
	}
	return prices, nil
}

// tierPricesFromProduct reads the tier_prices array embedded in the product
// resource (GET /products/{sku}) when the storage route is unavailable.
func (s *Service) tierPricesFromProduct(ctx context.Context, sku string) ([]TierPrice, error) {
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		TierPrices []tierPriceFallback `json:"tier_prices"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	prices := make([]TierPrice, 0, len(payload.TierPrices))
	for _, item := range payload.TierPrices {
		group := item.CustomerGroup
		if group == "" {
			group = strconv.Itoa(item.CustomerGroupID)
		}
		quantity := item.Quantity
		if quantity == 0 {
			quantity = item.Qty
		}
		prices = append(prices, TierPrice{
			SKU: item.SKU, Price: item.Price, PriceType: item.PriceType,
			WebsiteID: item.WebsiteID, CustomerGroup: group, Quantity: quantity,
		})
	}
	return prices, nil
}

type tierPriceFallback struct {
	SKU             string  `json:"sku" jsonschema:"Product SKU."`
	Price           float64 `json:"price"`
	PriceType       string  `json:"price_type"`
	WebsiteID       int     `json:"website_id" jsonschema:"Website the entity belongs to."`
	CustomerGroupID int     `json:"customer_group_id"`
	CustomerGroup   string  `json:"customer_group"`
	Quantity        float64 `json:"quantity"`
	Qty             float64 `json:"qty" jsonschema:"Quantity at this source."`
}

// SetTierPrices replaces the tier prices of one SKU with POST
// /products/tier-prices. Magento's storage interface replaces the stored prices
// for the SKU, so the call is a full replacement, not an append.
func (s *Service) SetTierPrices(ctx context.Context, sku string, prices []TierPrice) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if err := validateTierPrices(sku, prices); err != nil {
		return err
	}
	payload := make([]tierPriceBody, 0, len(prices))
	for _, price := range prices {
		payload = append(payload, tierPriceBody{
			SKU: sku, Price: price.Price, PriceType: "fixed", WebsiteID: price.WebsiteID,
			CustomerGroup: price.CustomerGroup, Quantity: price.Quantity,
		})
	}
	_, err := s.client.do(ctx, http.MethodPost, "/products/tier-prices", nil, tierPriceSetBody{Prices: payload})
	return err
}

type tierPriceSetBody struct {
	Prices []tierPriceBody `json:"prices" jsonschema:"Tier prices now set on the product."`
}

type tierPriceBody struct {
	SKU           string  `json:"sku" jsonschema:"Product SKU."`
	Price         float64 `json:"price"`
	PriceType     string  `json:"price_type"`
	WebsiteID     int     `json:"website_id" jsonschema:"Website the entity belongs to."`
	CustomerGroup string  `json:"customer_group"`
	Quantity      float64 `json:"quantity"`
}

func validateTierPrices(sku string, prices []TierPrice) error {
	if len(prices) == 0 {
		return invalidf("at least one tier price is required; an empty list does not clear prices")
	}
	if len(prices) > maxTierPrices {
		return invalidf("at most %d tier prices are allowed per call", maxTierPrices)
	}
	seen := make(map[string]struct{}, len(prices))
	for _, price := range prices {
		if price.SKU != "" && price.SKU != sku {
			return invalidf("tier prices must all belong to sku %q", sku)
		}
		if price.Price <= 0 {
			return invalidf("tier price must be greater than zero")
		}
		if price.Quantity <= 0 {
			return invalidf("tier quantity must be greater than zero")
		}
		if price.WebsiteID < 0 {
			return invalidf("website_id must not be negative")
		}
		group := strings.TrimSpace(price.CustomerGroup)
		if group == "" || len(group) > 64 || strings.ContainsFunc(group, func(r rune) bool {
			return r != ' ' && r != '_' && r != '-' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9')
		}) {
			return invalidf("customer_group must be 1-64 letters, digits, spaces, underscores, or hyphens")
		}
		key := strconv.Itoa(price.WebsiteID) + "|" + group + "|" + strconv.FormatFloat(price.Quantity, 'f', -1, 64)
		if _, duplicate := seen[key]; duplicate {
			return invalidf("tier price for website %d, group %q, quantity %v is duplicated", price.WebsiteID, group, price.Quantity)
		}
		seen[key] = struct{}{}
	}
	return nil
}
