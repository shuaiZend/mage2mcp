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
	SKU           string  `json:"sku"`
	Price         float64 `json:"price"`
	PriceType     string  `json:"price_type"`
	WebsiteID     int     `json:"website_id"`
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
		return nil, err
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
	Prices []tierPriceBody `json:"prices"`
}

type tierPriceBody struct {
	SKU           string  `json:"sku"`
	Price         float64 `json:"price"`
	PriceType     string  `json:"price_type"`
	WebsiteID     int     `json:"website_id"`
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
