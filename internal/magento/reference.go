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
