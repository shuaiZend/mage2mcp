// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultCategoryDepth = 2
	maxCategoryDepth     = 5
	maxAttributeSets     = 200
)

// Category is the category projection this slice exposes.
type Category struct {
	ID            int    `json:"id"`
	ParentID      int    `json:"parent_id"`
	Name          string `json:"name"`
	IsActive      bool   `json:"is_active"`
	Position      int    `json:"position"`
	Level         int    `json:"level"`
	IncludeInMenu bool   `json:"include_in_menu"`
	Path          string `json:"path,omitempty"`
	ChildrenCount string `json:"children_count,omitempty"`
}

// AttributeSet is one product attribute set.
type AttributeSet struct {
	ID           int    `json:"attribute_set_id"`
	Name         string `json:"attribute_set_name"`
	EntityTypeID int    `json:"entity_type_id"`
}

// Attribute is one attribute in an attribute set.
type Attribute struct {
	ID          int    `json:"attribute_id"`
	Code        string `json:"attribute_code"`
	Label       string `json:"frontend_label"`
	Input       string `json:"frontend_input"`
	Required    bool   `json:"is_required"`
	UserDefined bool   `json:"is_user_defined"`
	Default     string `json:"default_value"`
}

// GetCategory returns one category by id.
func (s *Service) GetCategory(ctx context.Context, categoryID int) (*Category, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if categoryID <= 0 {
		return nil, invalidf("category_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/categories/"+strconv.Itoa(categoryID), nil, nil)
	if err != nil {
		return nil, err
	}
	var category Category
	if err := json.Unmarshal(raw, &category); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category payload").WithCause(err)
	}
	return &category, nil
}

// CategoryTree returns the category subtree rooted at rootID, up to depth.
func (s *Service) CategoryTree(ctx context.Context, rootID, depth int) ([]Category, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if rootID <= 0 {
		return nil, invalidf("root_id must be a positive integer")
	}
	if depth == 0 {
		depth = defaultCategoryDepth
	}
	if depth < 1 || depth > maxCategoryDepth {
		return nil, invalidf("depth must be between 1 and %d", maxCategoryDepth)
	}
	query := make(url.Values)
	query.Set("rootCategoryId", strconv.Itoa(rootID))
	query.Set("depth", strconv.Itoa(depth))
	raw, err := s.client.do(ctx, http.MethodGet, "/categories", query, nil)
	if err != nil {
		return nil, err
	}
	var root categoryNode
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category tree payload").WithCause(err)
	}
	// The tree endpoint returns nested children_data. Return a flat depth-first
	// list instead of a recursive type: callers rebuild the tree from
	// parent_id/level, and a recursive struct would make the tool schema
	// cyclic.
	flat := make([]Category, 0, 16)
	var walk func(node categoryNode)
	walk = func(node categoryNode) {
		flat = append(flat, node.Category)
		for _, child := range node.Children {
			walk(child)
		}
	}
	walk(root)
	return flat, nil
}

// categoryNode is the tree wire shape: Magento nests children as children_data.
type categoryNode struct {
	Category
	Children []categoryNode `json:"children_data"`
}

// AttributeSets lists the product attribute sets.
func (s *Service) AttributeSets(ctx context.Context) ([]AttributeSet, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(maxAttributeSets))
	raw, err := s.client.do(ctx, http.MethodGet, "/products/attribute-sets/sets/list", query, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Items []AttributeSet `json:"items"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable attribute-set payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []AttributeSet{}
	}
	return payload.Items, nil
}

// Attributes lists the attributes of one attribute set.
func (s *Service) Attributes(ctx context.Context, attributeSetID int) ([]Attribute, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if attributeSetID <= 0 {
		return nil, invalidf("attribute_set_id must be a positive integer")
	}
	path := "/products/attribute-sets/" + strconv.Itoa(attributeSetID) + "/attributes"
	raw, err := s.client.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var attributes []Attribute
	if err := json.Unmarshal(raw, &attributes); err != nil {
		// Some deployments wrap the array in an items envelope.
		var wrapped struct {
			Items []Attribute `json:"items"`
		}
		if wrappedErr := json.Unmarshal(raw, &wrapped); wrappedErr != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable attribute payload").WithCause(err)
		}
		attributes = wrapped.Items
	}
	if attributes == nil {
		attributes = []Attribute{}
	}
	return attributes, nil
}

// AttributeOption is one selectable value of an attribute.
type AttributeOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// AttributesOptions lists the options of a select/multiselect attribute.
//
// Configurable products reference options by value_index, so callers need this
// to build variant attributes.
func (s *Service) AttributesOptions(ctx context.Context, attributeCode string) ([]AttributeOption, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if !attributeCodeRE.MatchString(attributeCode) {
		return nil, invalidf("attribute code must match %s", attributeCodeRE.String())
	}
	path := "/products/attributes/" + url.PathEscape(attributeCode) + "/options"
	raw, err := s.client.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	var options []AttributeOption
	if err := json.Unmarshal(raw, &options); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable attribute-option payload").WithCause(err)
	}
	if options == nil {
		options = []AttributeOption{}
	}
	return options, nil
}

// CategoryProducts returns one page of products assigned to a category.
func (s *Service) CategoryProducts(ctx context.Context, categoryID, limit, offset int) ([]Product, int, error) {
	if s == nil || s.client == nil {
		return nil, 0, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if categoryID <= 0 {
		return nil, 0, invalidf("category_id must be a positive integer")
	}
	if limit == 0 {
		limit = defaultSearchLimit
	}
	if limit < 1 || limit > maxSearchLimit {
		return nil, 0, invalidf("limit must be between 1 and %d", maxSearchLimit)
	}
	if offset < 0 || offset%limit != 0 {
		return nil, 0, invalidf("cursor offset must be a non-negative multiple of limit")
	}
	query := make(url.Values)
	query.Set("searchCriteria[pageSize]", strconv.Itoa(limit))
	query.Set("searchCriteria[currentPage]", strconv.Itoa(offset/limit+1))
	path := "/categories/" + strconv.Itoa(categoryID) + "/products"
	raw, err := s.client.do(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return nil, 0, err
	}
	var products []Product
	var total int
	if err := json.Unmarshal(raw, &products); err == nil {
		total = len(products)
		return products, total, nil
	}
	var payload struct {
		Items      []Product `json:"items"`
		TotalCount int       `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category-product payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []Product{}
	}
	return payload.Items, payload.TotalCount, nil
}
