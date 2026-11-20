// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// CustomerGroup is one customer group.
type CustomerGroup struct {
	ID         int    `json:"id"`
	Code       string `json:"code"`
	TaxClassID int    `json:"tax_class_id"`
}

// TaxClass is one tax class.
type TaxClass struct {
	ClassID   int    `json:"class_id"`
	ClassName string `json:"class_name"`
	ClassType string `json:"class_type"`
}

// CustomerGroups lists customer groups with GET /customerGroups.
func (s *Service) CustomerGroups(ctx context.Context) ([]CustomerGroup, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento customer service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", "100")
	raw, err := s.client.do(ctx, http.MethodGet, "/customerGroups/search", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Items []CustomerGroup `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer-group payload").WithCause(err)
	}
	if envelope.Items == nil {
		envelope.Items = []CustomerGroup{}
	}
	return envelope.Items, nil
}

// TaxClasses lists tax classes with GET /taxClasses.
func (s *Service) TaxClasses(ctx context.Context) ([]TaxClass, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", "100")
	raw, err := s.client.do(ctx, http.MethodGet, "/taxClasses/search", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Items []TaxClass `json:"items"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable tax-class payload").WithCause(err)
	}
	if envelope.Items == nil {
		envelope.Items = []TaxClass{}
	}
	return envelope.Items, nil
}

// StoreConfig is one Magento store view configuration.
type StoreConfig struct {
	ID               int    `json:"id"`
	Code             string `json:"code"`
	WebsiteID        int    `json:"website_id"`
	Locale           string `json:"locale"`
	BaseCurrencyCode string `json:"base_currency_code"`
	DisplayCurrency  string `json:"default_display_currency_code"`
	Timezone         string `json:"timezone"`
	BaseURL          string `json:"base_url"`
}

// StoreConfigs lists store views with GET /store/storeConfigs.
func (s *Service) StoreConfigs(ctx context.Context) ([]StoreConfig, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento store service is not configured")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/store/storeConfigs", nil, nil)
	if err != nil {
		return nil, err
	}
	var stores []StoreConfig
	if err := json.Unmarshal(raw, &stores); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable store-config payload").WithCause(err)
	}
	if stores == nil {
		stores = []StoreConfig{}
	}
	return stores, nil
}

// CategoryFlat is one category with its materialized path.
type CategoryFlat struct {
	ID       int    `json:"id"`
	ParentID int    `json:"parent_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Level    int    `json:"level"`
}

// FlatCategories flattens GET /categories into id → path rows.
func (s *Service) FlatCategories(ctx context.Context) ([]CategoryFlat, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/categories", nil, nil)
	if err != nil {
		return nil, err
	}
	var root struct {
		ID           int               `json:"id"`
		ParentID     int               `json:"parent_id"`
		Name         string            `json:"name"`
		Level        int               `json:"level"`
		ChildrenData []json.RawMessage `json:"children_data"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category payload").WithCause(err)
	}
	out := make([]CategoryFlat, 0, 256)
	var walk func(node json.RawMessage, parentPath string)
	walk = func(node json.RawMessage, parentPath string) {
		var n struct {
			ID           int               `json:"id"`
			ParentID     int               `json:"parent_id"`
			Name         string            `json:"name"`
			Level        int               `json:"level"`
			ChildrenData []json.RawMessage `json:"children_data"`
		}
		if err := json.Unmarshal(node, &n); err != nil {
			return
		}
		path := n.Name
		if parentPath != "" {
			path = parentPath + "/" + n.Name
		}
		out = append(out, CategoryFlat{ID: n.ID, ParentID: n.ParentID, Name: n.Name, Path: path, Level: n.Level})
		for _, child := range n.ChildrenData {
			walk(child, path)
		}
	}
	walk(raw, "")
	return out, nil
}
