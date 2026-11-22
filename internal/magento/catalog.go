// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	defaultCategoryDepth = 2
	maxCategoryDepth     = 5
	maxAttributeSets     = 200
)

// Category is the category projection this slice exposes.
type Category struct {
	ID            int    `json:"id" jsonschema:"Magento category entity id."`
	ParentID      int    `json:"parent_id" jsonschema:"Parent category id; 0 for the root."`
	Name          string `json:"name" jsonschema:"Display name."`
	IsActive      bool   `json:"is_active" jsonschema:"True when the category is enabled."`
	Position      int    `json:"position" jsonschema:"Sort position among siblings."`
	Level         int    `json:"level" jsonschema:"Depth in the category tree; the root is 1."`
	IncludeInMenu bool   `json:"include_in_menu" jsonschema:"True when the category appears in the navigation menu."`
	Path          string `json:"path,omitempty" jsonschema:"Full path of category ids from the root."`
	ChildrenCount string `json:"children_count,omitempty" jsonschema:"Number of direct children."`
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
	Code        string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
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
		Items []AttributeSet `json:"items" jsonschema:"Entries returned by this call."`
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
			Items []Attribute `json:"items" jsonschema:"Entries returned by this call."`
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
	Label string `json:"label" jsonschema:"Label shown to the shopper."`
	Value string `json:"value" jsonschema:"Value of the entry."`
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
	if err := json.Unmarshal(raw, &products); err == nil {
		hydrated, err := s.hydrateCategoryProducts(ctx, products)
		if err != nil {
			return nil, 0, err
		}
		return hydrated, len(hydrated), nil
	}
	var payload struct {
		Items      []Product `json:"items" jsonschema:"Entries returned by this call."`
		TotalCount int       `json:"total_count"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, 0, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable category-product payload").WithCause(err)
	}
	if payload.Items == nil {
		payload.Items = []Product{}
	}
	hydrated, err := s.hydrateCategoryProducts(ctx, payload.Items)
	if err != nil {
		return nil, 0, err
	}
	return hydrated, payload.TotalCount, nil
}

// hydrationBatchSize bounds one enrichment search. The batch size, not the
// caller's page limit, decides searchCriteria[pageSize] (F3).
const hydrationBatchSize = 100

// hydrateCategoryProducts fills in the product fields Magento's category listing
// leaves empty.
//
// GET /categories/{id}/products answers with ProductInterface rows that carry
// only sku and id; every other field decodes as its zero value, so the tool used
// to return name "", price 0 and status 0 for real products (U-36). One extra
// search keyed on the page's SKUs costs a single request and returns the full
// projection.
func (s *Service) hydrateCategoryProducts(ctx context.Context, products []Product) ([]Product, error) {
	if len(products) == 0 {
		return products, nil
	}
	missing := false
	skus := make([]string, 0, len(products))
	for _, product := range products {
		if product.Name == "" {
			missing = true
		}
		if product.SKU != "" {
			skus = append(skus, product.SKU)
		}
	}
	if !missing || len(skus) == 0 {
		return products, nil
	}
	// The page size of the enrichment search is derived from the rows actually
	// returned by the category listing, never from the caller's `limit`: this
	// store ignores searchCriteria[pageSize] on the listing route, so a limit of
	// 2 came back as 182 rows and only 2 of them were enriched (F3). Each batch
	// asks for exactly the number of SKUs it carries.
	bySKU := make(map[string]Product, len(skus))
	for start := 0; start < len(skus); start += hydrationBatchSize {
		end := start + hydrationBatchSize
		if end > len(skus) {
			end = len(skus)
		}
		batch := skus[start:end]
		query := make(url.Values)
		query.Set("searchCriteria[pageSize]", strconv.Itoa(len(batch)))
		query.Set("searchCriteria[filter_groups][0][filters][0][field]", "sku")
		query.Set("searchCriteria[filter_groups][0][filters][0][value]", strings.Join(batch, ","))
		query.Set("searchCriteria[filter_groups][0][filters][0][condition_type]", "in")
		raw, err := s.client.do(ctx, http.MethodGet, "/products", query, nil)
		if err != nil {
			// The listing is still a usable answer (skus are correct); do not fail
			// the read because the enrichment could not run.
			continue
		}
		var payload struct {
			Items []Product `json:"items" jsonschema:"Entries returned by this call."`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			continue
		}
		for _, product := range payload.Items {
			bySKU[product.SKU] = product
		}
	}
	for i, product := range products {
		if full, ok := bySKU[product.SKU]; ok {
			products[i] = full
		}
	}
	return products, nil
}

// CreateAttributeOption adds a value to a select/multiselect attribute with
// POST /products/attributes/{code}/options.
func (s *Service) CreateAttributeOption(ctx context.Context, attributeCode, label string) (*AttributeOption, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if !attributeCodeRE.MatchString(attributeCode) {
		return nil, invalidf("attribute code must match %s", attributeCodeRE.String())
	}
	if err := validateCategoryName(label); err != nil {
		return nil, invalidf("option label must be 1-255 bytes without control characters")
	}
	body := attributeOptionBody{Option: attributeOptionPayload{Label: label}}
	raw, err := s.client.do(ctx, http.MethodPost, "/products/attributes/"+url.PathEscape(attributeCode)+"/options", nil, body)
	if err != nil {
		return nil, err
	}
	// The new option id comes back in one of three shapes depending on the build:
	// a JSON string, a bare number, or an object carrying label and value. A number
	// used to fall through both string and object decoding, so the option was
	// created and the caller was told the payload was unreadable — leaving it
	// without the value_index it needs to build a configurable variant, and a retry
	// created a second option.
	if value, ok := decodeAttributeOptionID(raw); ok {
		return &AttributeOption{Label: label, Value: value}, nil
	}
	var option AttributeOption
	if err := json.Unmarshal(raw, &option); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable attribute-option payload").WithCause(err)
	}
	return &option, nil
}

// decodeAttributeOptionID reads a scalar option id as a string or a number.
func decodeAttributeOptionID(raw []byte) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", false
	}
	var text string
	if err := json.Unmarshal(trimmed, &text); err == nil {
		text = strings.TrimSpace(text)
		return text, text != ""
	}
	var number json.Number
	if err := json.Unmarshal(trimmed, &number); err == nil {
		text = strings.TrimSpace(number.String())
		return text, text != ""
	}
	return "", false
}

type attributeOptionBody struct {
	Option attributeOptionPayload `json:"option" jsonschema:"The option that was created or read."`
}

type attributeOptionPayload struct {
	Label string `json:"label" jsonschema:"Label shown to the shopper."`
}

// DeleteAttributeOption removes one select/multiselect option with
// DELETE /products/attributes/{code}/options/{id}. Options referenced by
// products cannot be removed; Magento rejects the call in that case.
func (s *Service) DeleteAttributeOption(ctx context.Context, attributeCode string, optionID int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if !attributeCodeRE.MatchString(attributeCode) {
		return invalidf("attribute code must match %s", attributeCodeRE.String())
	}
	if optionID <= 0 {
		return invalidf("option_id must be a positive integer")
	}
	path := "/products/attributes/" + url.PathEscape(attributeCode) + "/options/" + strconv.Itoa(optionID)
	_, err := s.client.do(ctx, http.MethodDelete, path, nil, nil)
	return err
}

const maxBulkOptions = 50

// OptionBulkRow is one option's outcome in a bulk create.
type OptionBulkRow struct {
	Label  string `json:"label" jsonschema:"Label shown to the shopper."`
	Value  string `json:"value,omitempty" jsonschema:"Value of the entry."`
	Status string `json:"status" jsonschema:"Current status."`
	Error  string `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// OptionBulkResult reports a partial-success option creation.
type OptionBulkResult struct {
	Total   int             `json:"total" jsonschema:"Number of option labels in this request."`
	Created int             `json:"created" jsonschema:"Rows that created a product."`
	Failed  int             `json:"failed" jsonschema:"Rows that failed."`
	Rows    []OptionBulkRow `json:"rows" jsonschema:"Per-row results, in input order."`
}

// BulkCreateAttributeOptions adds up to 50 options to one attribute. Each label
// is a separate call; failures are reported per row and earlier options stay.
func (s *Service) BulkCreateAttributeOptions(ctx context.Context, attributeCode string, labels []string) (*OptionBulkResult, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento catalog service is not configured")
	}
	if !attributeCodeRE.MatchString(attributeCode) {
		return nil, invalidf("attribute code must match %s", attributeCodeRE.String())
	}
	if len(labels) == 0 {
		return nil, invalidf("at least one option label is required")
	}
	if len(labels) > maxBulkOptions {
		return nil, invalidf("at most %d option labels are allowed per call", maxBulkOptions)
	}
	seen := make(map[string]struct{}, len(labels))
	unique := make([]string, 0, len(labels))
	for _, label := range labels {
		key := strings.ToUpper(strings.TrimSpace(label))
		if key == "" {
			return nil, invalidf("option labels must not be empty")
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, label)
	}
	// Bounded fan-out: each label is an independent create and failures are reported
	// per row, so running them one at a time only multiplied the round-trips.
	rows := make([]OptionBulkRow, len(unique))
	sem := make(chan struct{}, maxBulkWriteConcurrency)
	var wg sync.WaitGroup
	for i, label := range unique {
		wg.Add(1)
		go func(i int, label string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				rows[i] = OptionBulkRow{Label: label, Status: "failed", Error: "the request was canceled"}
				return
			}
			defer func() { <-sem }()
			option, err := s.CreateAttributeOption(ctx, attributeCode, label)
			if err != nil {
				rows[i] = OptionBulkRow{Label: label, Status: "failed", Error: importErrorMessage(err)}
				return
			}
			rows[i] = OptionBulkRow{Label: label, Status: "created", Value: option.Value}
		}(i, label)
	}
	wg.Wait()
	result := &OptionBulkResult{Total: len(labels), Rows: rows}
	for _, row := range rows {
		if row.Status == "failed" {
			result.Failed++
			continue
		}
		result.Created++
	}
	return result, nil
}
