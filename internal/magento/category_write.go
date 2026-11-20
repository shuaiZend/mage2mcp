// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxCategoryNameBytes = 255
	maxCategoryPosition  = 10000
)

// CategoryCreateInput is one category creation request.
type CategoryCreateInput struct {
	ParentID      int    `json:"parent_id" jsonschema:"Parent category id; 0 for the root."`
	Name          string `json:"name" jsonschema:"Display name."`
	IsActive      bool   `json:"is_active" jsonschema:"True when the entity is enabled."`
	Position      int    `json:"position" jsonschema:"Sort position among siblings."`
	IncludeInMenu bool   `json:"include_in_menu" jsonschema:"True when the category appears in the navigation menu."`
	URLKey        string `json:"url_key,omitempty"`
}

// CategoryUpdateInput is a partial category update; nil fields are untouched.
type CategoryUpdateInput struct {
	CategoryID    int
	Name          *string
	IsActive      *bool
	Position      *int
	IncludeInMenu *bool
}

// CreateCategory adds a category with POST /categories.
func (s *Service) CreateCategory(ctx context.Context, in CategoryCreateInput) (*Category, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if err := validateCategoryFields(in.ParentID, in.Name, in.Position); err != nil {
		return nil, err
	}
	if in.URLKey != "" {
		if len(in.URLKey) > 255 || strings.ContainsFunc(in.URLKey, unicode.IsControl) {
			return nil, invalidf("url_key must be at most 255 bytes without control characters")
		}
	}
	payload := categoryCreatePayload{
		ParentID: in.ParentID, Name: in.Name, IsActive: in.IsActive,
		Position: in.Position, IncludeInMenu: in.IncludeInMenu,
	}
	if in.URLKey != "" {
		payload.CustomAttributes = []categoryCustomAttribute{{AttributeCode: "url_key", Value: in.URLKey}}
	}
	body := categoryCreateBody{Category: payload}
	raw, err := s.client.do(ctx, http.MethodPost, "/categories", nil, body)
	if err != nil {
		return nil, err
	}
	var category Category
	if err := json.Unmarshal(raw, &category); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category payload").WithCause(err)
	}
	return &category, nil
}

// UpdateCategory changes a category with PUT /categories/{id}.
func (s *Service) UpdateCategory(ctx context.Context, in CategoryUpdateInput) (*Category, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if in.CategoryID <= 0 {
		return nil, invalidf("category_id must be a positive integer")
	}
	if in.Name == nil && in.IsActive == nil && in.Position == nil && in.IncludeInMenu == nil {
		return nil, invalidf("at least one field must be provided to update")
	}
	if in.Name != nil {
		if err := validateCategoryName(*in.Name); err != nil {
			return nil, err
		}
	}
	if in.Position != nil && (*in.Position < 0 || *in.Position > maxCategoryPosition) {
		return nil, invalidf("position must be between 0 and %d", maxCategoryPosition)
	}
	body := categoryUpdateBody{Category: categoryUpdatePayload{
		Name: in.Name, IsActive: in.IsActive, Position: in.Position, IncludeInMenu: in.IncludeInMenu,
	}}
	raw, err := s.client.do(ctx, http.MethodPut, "/categories/"+strconv.Itoa(in.CategoryID), nil, body)
	if err != nil {
		return nil, err
	}
	var category Category
	if err := json.Unmarshal(raw, &category); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category payload").WithCause(err)
	}
	return &category, nil
}

// AssignProductToCategory links a product with POST /categories/{id}/products.
func (s *Service) AssignProductToCategory(ctx context.Context, categoryID int, sku string, position int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if categoryID <= 0 {
		return invalidf("category_id must be a positive integer")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if position < 0 || position > maxCategoryPosition {
		return invalidf("position must be between 0 and %d", maxCategoryPosition)
	}
	body := categoryProductBody{ProductLink: categoryProductLink{
		SKU: sku, Position: position, CategoryID: strconv.Itoa(categoryID),
	}}
	_, err := s.client.do(ctx, http.MethodPost, "/categories/"+strconv.Itoa(categoryID)+"/products", nil, body)
	return err
}

type categoryCreateBody struct {
	Category categoryCreatePayload `json:"category" jsonschema:"The category that was created or read."`
}

type categoryCreatePayload struct {
	ParentID         int                       `json:"parent_id" jsonschema:"Parent category id; 0 for the root."`
	Name             string                    `json:"name" jsonschema:"Display name."`
	IsActive         bool                      `json:"is_active" jsonschema:"True when the entity is enabled."`
	Position         int                       `json:"position,omitempty" jsonschema:"Sort position among siblings."`
	IncludeInMenu    bool                      `json:"include_in_menu" jsonschema:"True when the category appears in the navigation menu."`
	CustomAttributes []categoryCustomAttribute `json:"custom_attributes,omitempty"`
}

type categoryCustomAttribute struct {
	AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	Value         string `json:"value" jsonschema:"Value of the entry."`
}

type categoryUpdateBody struct {
	Category categoryUpdatePayload `json:"category" jsonschema:"The category that was created or read."`
}

type categoryUpdatePayload struct {
	Name          *string `json:"name,omitempty" jsonschema:"Display name."`
	IsActive      *bool   `json:"is_active,omitempty" jsonschema:"True when the entity is enabled."`
	Position      *int    `json:"position,omitempty" jsonschema:"Sort position among siblings."`
	IncludeInMenu *bool   `json:"include_in_menu,omitempty" jsonschema:"True when the category appears in the navigation menu."`
}

type categoryProductBody struct {
	ProductLink categoryProductLink `json:"productLink"`
}

type categoryProductLink struct {
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	Position   int    `json:"position" jsonschema:"Sort position among siblings."`
	CategoryID string `json:"category_id" jsonschema:"Magento category entity id."`
}

func validateCategoryName(name string) error {
	if name == "" || len(name) > maxCategoryNameBytes || strings.ContainsFunc(name, unicode.IsControl) {
		return invalidf("category name must be 1-%d bytes and contain no control characters", maxCategoryNameBytes)
	}
	return nil
}

func validateCategoryFields(parentID int, name string, position int) error {
	if parentID <= 0 {
		return invalidf("parent_id must be a positive integer")
	}
	if err := validateCategoryName(name); err != nil {
		return err
	}
	if position < 0 || position > maxCategoryPosition {
		return invalidf("position must be between 0 and %d", maxCategoryPosition)
	}
	return nil
}

// DeleteCategory removes a category with DELETE /categories/{id}.
//
// Magento deletes the category and its subtree; products in the subtree lose
// that assignment but are not deleted. The call is destructive and irreversible.
func (s *Service) DeleteCategory(ctx context.Context, categoryID int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if categoryID <= 0 {
		return invalidf("category_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodDelete, "/categories/"+strconv.Itoa(categoryID), nil, nil)
	return err
}
