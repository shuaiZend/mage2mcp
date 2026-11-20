// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// ProductOptionValue is one value of a custom option.
type ProductOptionValue struct {
	OptionTypeID int     `json:"option_type_id"`
	Title        string  `json:"title" jsonschema:"Display title for this entry."`
	Price        float64 `json:"price"`
	PriceType    string  `json:"price_type"`
	SKU          string  `json:"sku,omitempty" jsonschema:"Product SKU."`
	SortOrder    int     `json:"sort_order" jsonschema:"Sort position among siblings."`
}

// ProductOption is one custom option (not a configurable variant option).
type ProductOption struct {
	OptionID  int                  `json:"option_id" jsonschema:"Custom option id."`
	Title     string               `json:"title" jsonschema:"Display title for this entry."`
	Type      string               `json:"type" jsonschema:"Input control type."`
	IsRequire bool                 `json:"is_require" jsonschema:"True when the shopper must choose this option."`
	SortOrder int                  `json:"sort_order" jsonschema:"Sort position among siblings."`
	Values    []ProductOptionValue `json:"values" jsonschema:"Selectable values of this option."`
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

const maxCustomOptionValues = 50

var customOptionTypes = map[string]bool{
	"field": true, "area": true, "file": true, "drop_down": true, "radio": true,
	"checkbox": true, "multiple": true, "date": true, "date_time": true, "time": true,
}

// CustomOptionValueInput is one selectable value of a custom option.
type CustomOptionValueInput struct {
	Title     string
	Price     float64
	PriceType string
	SKU       string
	SortOrder int
}

// CustomOptionInput describes one custom option to create.
type CustomOptionInput struct {
	Title     string
	Type      string
	IsRequire bool
	SortOrder int
	Values    []CustomOptionValueInput
}

// CreateProductOption adds a custom option with POST /products/{sku}/options.
func (s *Service) CreateProductOption(ctx context.Context, sku string, in CustomOptionInput) (*ProductOption, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	values, err := validateCustomOption(in)
	if err != nil {
		return nil, err
	}
	body := customOptionBody{Option: customOptionPayload{
		Title: in.Title, Type: in.Type, IsRequire: in.IsRequire, SortOrder: in.SortOrder, Values: values,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/products/"+url.PathEscape(sku)+"/options", nil, body)
	if err != nil {
		if errs.CodeOf(err) != errs.CodeNotFound {
			return nil, err
		}
		// Some Magento builds expose the custom-option route read-only. Fall
		// back to saving the option through the product update endpoint.
		fallback := struct {
			Product struct {
				SKU     string                `json:"sku" jsonschema:"Product SKU."`
				Options []customOptionPayload `json:"options" jsonschema:"Custom options of the product."`
			} `json:"product" jsonschema:"The product that was created or read."`
		}{}
		fallback.Product.SKU = sku
		fallback.Product.Options = []customOptionPayload{body.Option}
		if _, fallbackErr := s.client.do(ctx, http.MethodPut, "/products/"+url.PathEscape(sku), nil, fallback); fallbackErr != nil {
			e := errs.New(errs.CodeUnsupportedCapability,
				"this Magento instance does not expose custom-option creation; saving the option through the product update also failed").
				WithDetail("option_route_status", "404").
				WithDetail("fallback_code", string(errs.CodeOf(fallbackErr)))
			var typed *errs.Error
			if errors.As(fallbackErr, &typed) {
				if message := typed.Details["upstream_message"]; message != "" {
					e = e.WithDetail("fallback_message", message)
				}
			}
			return nil, e
		}
		// The fallback write reported success. Never fabricate the option from the
		// request: read it back and, if it is not there, say so. A synthetic
		// success would tell the caller an option exists when the product does
		// not list it.
		options, listErr := s.ProductOptions(ctx, sku)
		if listErr != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable,
				"Magento accepted the custom-option write but the product options could not be read back; verify the product before retrying").
				WithDetail("sku", sku).
				WithDetail("option_title", in.Title).
				WithCause(listErr)
		}
		for i := range options {
			if options[i].Title == in.Title {
				return &options[i], nil
			}
		}
		return nil, errs.New(errs.CodeConflict,
			"Magento accepted the custom-option write but the product does not list the option; verify the product before retrying").
			WithDetail("sku", sku).
			WithDetail("option_title", in.Title)
	}
	var option ProductOption
	if err := json.Unmarshal(raw, &option); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product-option payload").WithCause(err)
	}
	return &option, nil
}

type customOptionBody struct {
	Option customOptionPayload `json:"option" jsonschema:"The option that was created or read."`
}

type customOptionPayload struct {
	OptionID  int                        `json:"option_id,omitempty" jsonschema:"Custom option id."`
	Title     string                     `json:"title" jsonschema:"Display title for this entry."`
	Type      string                     `json:"type" jsonschema:"Input control type."`
	IsRequire bool                       `json:"is_require" jsonschema:"True when the shopper must choose this option."`
	SortOrder int                        `json:"sort_order" jsonschema:"Sort position among siblings."`
	Values    []customOptionValuePayload `json:"values,omitempty" jsonschema:"Selectable values of this option."`
}

type customOptionValuePayload struct {
	Title     string  `json:"title" jsonschema:"Display title for this entry."`
	Price     float64 `json:"price"`
	PriceType string  `json:"price_type"`
	SKU       string  `json:"sku,omitempty" jsonschema:"Product SKU."`
	SortOrder int     `json:"sort_order" jsonschema:"Sort position among siblings."`
}

func validateCustomOption(in CustomOptionInput) ([]customOptionValuePayload, error) {
	if trimmed := strings.TrimSpace(in.Title); trimmed == "" || len(in.Title) > 255 {
		return nil, invalidf("title is required and must be at most 255 bytes")
	}
	if !customOptionTypes[in.Type] {
		return nil, invalidf("type must be one of field, area, file, drop_down, radio, checkbox, multiple, date, date_time, time")
	}
	if in.SortOrder < 0 {
		return nil, invalidf("sort_order must not be negative")
	}
	needsValues := in.Type == "drop_down" || in.Type == "radio" || in.Type == "checkbox" || in.Type == "multiple"
	if needsValues {
		if len(in.Values) == 0 || len(in.Values) > maxCustomOptionValues {
			return nil, invalidf("type %q requires 1-%d values", in.Type, maxCustomOptionValues)
		}
	} else if len(in.Values) != 0 {
		return nil, invalidf("type %q does not accept values", in.Type)
	}
	values := make([]customOptionValuePayload, 0, len(in.Values))
	for _, value := range in.Values {
		if trimmed := strings.TrimSpace(value.Title); trimmed == "" || len(value.Title) > 255 {
			return nil, invalidf("value title is required and must be at most 255 bytes")
		}
		if value.Price < 0 {
			return nil, invalidf("value price must not be negative")
		}
		priceType := value.PriceType
		if priceType == "" {
			priceType = "fixed"
		}
		if priceType != "fixed" && priceType != "percent" {
			return nil, invalidf("value price_type must be fixed or percent")
		}
		if value.SortOrder < 0 {
			return nil, invalidf("value sort_order must not be negative")
		}
		values = append(values, customOptionValuePayload{
			Title: value.Title, Price: value.Price, PriceType: priceType,
			SKU: value.SKU, SortOrder: value.SortOrder,
		})
	}
	return values, nil
}

// UpdateProductOption changes a custom option with
// PUT /products/{sku}/options/{optionId}.
func (s *Service) UpdateProductOption(ctx context.Context, sku string, optionID int, in CustomOptionInput) (*ProductOption, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if optionID <= 0 {
		return nil, invalidf("option_id must be a positive integer")
	}
	values, err := validateCustomOption(in)
	if err != nil {
		return nil, err
	}
	body := customOptionBody{Option: customOptionPayload{
		OptionID: optionID, Title: in.Title, Type: in.Type, IsRequire: in.IsRequire, SortOrder: in.SortOrder, Values: values,
	}}
	raw, err := s.client.do(ctx, http.MethodPut,
		"/products/"+url.PathEscape(sku)+"/options/"+strconv.Itoa(optionID), nil, body)
	if err != nil {
		return nil, err
	}
	var option ProductOption
	if err := json.Unmarshal(raw, &option); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product-option payload").WithCause(err)
	}
	return &option, nil
}

// DeleteProductOption removes a custom option with
// DELETE /products/{sku}/options/{optionId}.
func (s *Service) DeleteProductOption(ctx context.Context, sku string, optionID int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if optionID <= 0 {
		return invalidf("option_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodDelete,
		"/products/"+url.PathEscape(sku)+"/options/"+strconv.Itoa(optionID), nil, nil)
	return err
}
