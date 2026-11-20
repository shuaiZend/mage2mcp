// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"

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
	raw, err := s.client.do(ctx, http.MethodGet, "/customerGroups", nil, nil)
	if err != nil {
		return nil, err
	}
	var groups []CustomerGroup
	if err := json.Unmarshal(raw, &groups); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable customer-group payload").WithCause(err)
	}
	if groups == nil {
		groups = []CustomerGroup{}
	}
	return groups, nil
}

// TaxClasses lists tax classes with GET /taxClasses.
func (s *Service) TaxClasses(ctx context.Context) ([]TaxClass, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/taxClasses", nil, nil)
	if err != nil {
		return nil, err
	}
	var classes []TaxClass
	if err := json.Unmarshal(raw, &classes); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable tax-class payload").WithCause(err)
	}
	if classes == nil {
		classes = []TaxClass{}
	}
	return classes, nil
}
