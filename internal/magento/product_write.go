// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxProductNameBytes      = 255
	maxAttributeValueBytes   = 255
	maxWebsiteIDs            = 50
	maxCategoryIDs           = 500
	defaultProductStatus     = 1
	defaultProductVisibility = 4
	simpleTypeID             = "simple"
	configurableTypeID       = "configurable"
	stageInventory           = "inventory"
)

var (
	priceRE         = regexp.MustCompile(`^[0-9]+(\.[0-9]{1,4})?$`)
	attributeCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	writableTypeIDs = map[string]struct{}{"simple": {}, "virtual": {}, "downloadable": {}, "bundle": {}}
)

// CreateProductInput describes one product to create. Status zero selects 1
// (enabled) and Visibility zero selects 4 (catalog, search); Price is a decimal
// string. Quantity, when non-nil, is assigned to the default inventory source
// after the product exists.
type CreateProductInput struct {
	SKU            string
	Name           string
	AttributeSetID int
	TypeID         string
	Price          string
	Status         int
	Visibility     int
	WebsiteIDs     []int
	CategoryIDs    []int
	Attributes     map[string]string
	Quantity       *int
}

// UpdateProductInput is a partial product update: nil fields are not sent, so
// Magento's PUT merge semantics preserve them. Attributes holds custom EAV
// attributes only.
type UpdateProductInput struct {
	Name       *string
	Price      *string
	Status     *int
	Visibility *int
	Attributes map[string]string
}

type productAttribute struct {
	AttributeCode string `json:"attribute_code"`
	Value         string `json:"value"`
}

type categoryLink struct {
	CategoryID string `json:"category_id"`
	Position   int    `json:"position"`
}

type productExtension struct {
	WebsiteIDs                 []int                `json:"website_ids,omitempty"`
	CategoryLinks              []categoryLink       `json:"category_links,omitempty"`
	ConfigurableProductOptions []configurableOption `json:"configurable_product_options,omitempty"`
}

type productPayload struct {
	SKU                 string             `json:"sku"`
	Name                string             `json:"name,omitempty"`
	AttributeSetID      int                `json:"attribute_set_id,omitempty"`
	TypeID              string             `json:"type_id,omitempty"`
	Price               string             `json:"price,omitempty"`
	Status              int                `json:"status,omitempty"`
	Visibility          int                `json:"visibility,omitempty"`
	CustomAttributes    []productAttribute `json:"custom_attributes,omitempty"`
	ExtensionAttributes *productExtension  `json:"extension_attributes,omitempty"`
}

type productWriteBody struct {
	Product productPayload `json:"product"`
}

// Create creates one product with POST /products and, when Quantity is set,
// assigns that quantity to the default inventory source. These are two upstream
// calls: if the inventory call fails the product already exists, so the error
// reports the partial success with the SKU and stage in its details and the
// product is not rolled back. The caller must compensate before retrying.
func (s *Service) Create(ctx context.Context, in CreateProductInput) (*Product, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	if err := validateCreateProduct(in); err != nil {
		return nil, err
	}
	in.Status = defaultInt(in.Status, defaultProductStatus)
	in.Visibility = defaultInt(in.Visibility, defaultProductVisibility)
	payload, err := buildCreatePayload(in)
	if err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodPost, "/products", nil, productWriteBody{Product: payload})
	if err != nil {
		return nil, err
	}
	product, err := decodeProduct(raw, in.SKU)
	if err != nil {
		return nil, err
	}
	if in.Quantity == nil {
		return product, nil
	}
	item := SourceItem{SKU: in.SKU, SourceCode: defaultSourceCode, Quantity: *in.Quantity, Status: 1}
	if *in.Quantity == 0 {
		item.Status = 0
	}
	if err := s.AssignSourceItems(ctx, []SourceItem{item}); err != nil {
		e := errs.New(errs.CodeOf(err), "Magento product "+in.SKU+
			" was created but assigning its inventory quantity failed; the product is not rolled back")
		e = e.WithCause(err)
		e = e.WithDetail("sku", in.SKU)
		e = e.WithDetail("stage", stageInventory)
		e = e.WithDetail("product_created", "true")
		return nil, e
	}
	return product, nil
}

// Update updates one product with PUT /products/{sku}. Only supplied fields are
// sent, so Magento merges them into the stored product; at least one of name,
// price, status, visibility, or attributes is required.
func (s *Service) Update(ctx context.Context, sku string, in UpdateProductInput) (*Product, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	payload, err := buildUpdatePayload(sku, in)
	if err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodPut, "/products/"+url.PathEscape(sku), nil, productWriteBody{Product: payload})
	if err != nil {
		return nil, err
	}
	return decodeProduct(raw, sku)
}

func (s *Service) requireClient() error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	return nil
}

func buildCreatePayload(in CreateProductInput) (productPayload, error) {
	attributes, err := customAttributes(in.Attributes)
	if err != nil {
		return productPayload{}, err
	}
	payload := productPayload{
		SKU: in.SKU, Name: strings.TrimSpace(in.Name), AttributeSetID: in.AttributeSetID,
		TypeID: in.TypeID, Price: in.Price, Status: in.Status, Visibility: in.Visibility,
		CustomAttributes: attributes,
	}
	ext := productExtension{WebsiteIDs: append([]int(nil), in.WebsiteIDs...)}
	for i, id := range in.CategoryIDs {
		ext.CategoryLinks = append(ext.CategoryLinks, categoryLink{CategoryID: strconv.Itoa(id), Position: i})
	}
	if len(ext.WebsiteIDs) > 0 || len(ext.CategoryLinks) > 0 {
		payload.ExtensionAttributes = &ext
	}
	return payload, nil
}

func buildUpdatePayload(sku string, in UpdateProductInput) (productPayload, error) {
	attributes, err := customAttributes(in.Attributes)
	if err != nil {
		return productPayload{}, err
	}
	payload := productPayload{SKU: sku, CustomAttributes: attributes}
	if in.Name != nil {
		if err := validateProductName(*in.Name); err != nil {
			return productPayload{}, err
		}
		payload.Name = strings.TrimSpace(*in.Name)
	}
	if in.Price != nil {
		if err := validatePrice(*in.Price); err != nil {
			return productPayload{}, err
		}
		payload.Price = *in.Price
	}
	if in.Status != nil {
		if err := validateStatusVisibility(*in.Status, 0); err != nil || *in.Status == 0 {
			return productPayload{}, invalidf("status must be 1 (enabled) or 2 (disabled)")
		}
		payload.Status = *in.Status
	}
	if in.Visibility != nil {
		if err := validateStatusVisibility(0, *in.Visibility); err != nil || *in.Visibility == 0 {
			return productPayload{}, invalidf("visibility must be between 1 and 4")
		}
		payload.Visibility = *in.Visibility
	}
	if payload.Name == "" && payload.Price == "" && payload.Status == 0 && payload.Visibility == 0 && len(attributes) == 0 {
		return productPayload{}, invalidf("update requires at least one of name, price, status, visibility, or attributes")
	}
	return payload, nil
}

func decodeProduct(raw []byte, fallbackSKU string) (*Product, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &Product{SKU: fallbackSKU}, nil
	}
	var product Product
	if err := json.Unmarshal(raw, &product); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	if product.SKU == "" {
		product.SKU = fallbackSKU
	}
	return &product, nil
}

func validateCreateProduct(in CreateProductInput) error {
	if err := validateProductFields(in); err != nil {
		return err
	}
	if _, ok := writableTypeIDs[in.TypeID]; !ok {
		return invalidf("type_id must be one of simple, virtual, downloadable, bundle")
	}
	return nil
}

// validateProductFields checks the fields shared by product and configurable
// parent writes; it deliberately does not constrain TypeID.
func validateProductFields(in CreateProductInput) error {
	if err := validateSKU(in.SKU); err != nil {
		return err
	}
	if err := validateProductName(in.Name); err != nil {
		return err
	}
	if in.AttributeSetID <= 0 {
		return invalidf("attribute_set_id must be a positive integer")
	}
	if err := validatePrice(in.Price); err != nil {
		return err
	}
	if err := validateStatusVisibility(in.Status, in.Visibility); err != nil {
		return err
	}
	if err := validatePositiveIDs("website_ids", in.WebsiteIDs, maxWebsiteIDs); err != nil {
		return err
	}
	if err := validatePositiveIDs("category_ids", in.CategoryIDs, maxCategoryIDs); err != nil {
		return err
	}
	if in.Quantity != nil && *in.Quantity < 0 {
		return invalidf("quantity must not be negative")
	}
	_, err := customAttributes(in.Attributes)
	return err
}

func validateProductName(name string) error {
	trimmed := strings.TrimSpace(name)
	switch {
	case trimmed == "":
		return invalidf("product name must not be empty")
	case len(trimmed) > maxProductNameBytes:
		return invalidf("product name must be at most %d bytes", maxProductNameBytes)
	case strings.ContainsFunc(trimmed, unicode.IsControl):
		return invalidf("product name must not contain control characters")
	}
	return nil
}

func validatePrice(price string) error {
	if !priceRE.MatchString(price) {
		return invalidf("price must be a decimal string matching %s", priceRE.String())
	}
	return nil
}

// validateStatusVisibility accepts 0 (use the default) or the Magento codes.
func validateStatusVisibility(status, visibility int) error {
	if status != 0 && status != 1 && status != 2 {
		return invalidf("status must be 1 (enabled) or 2 (disabled)")
	}
	if visibility < 0 || visibility > 4 {
		return invalidf("visibility must be between 1 and 4")
	}
	return nil
}

func validatePositiveIDs(field string, ids []int, limit int) error {
	if len(ids) > limit {
		return invalidf("%s must contain at most %d entries", field, limit)
	}
	for _, id := range ids {
		if id <= 0 {
			return invalidf("%s must contain only positive ids", field)
		}
	}
	return nil
}

func validateAttributeCode(code string) error {
	if !attributeCodeRE.MatchString(code) {
		return invalidf("attribute code must match %s", attributeCodeRE.String())
	}
	return nil
}

// customAttributes validates attrs and renders them in deterministic code order.
func customAttributes(attrs map[string]string) ([]productAttribute, error) {
	codes := make([]string, 0, len(attrs))
	for code := range attrs {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	out := make([]productAttribute, 0, len(codes))
	for _, code := range codes {
		if err := validateAttributeCode(code); err != nil {
			return nil, err
		}
		if len(attrs[code]) > maxAttributeValueBytes {
			return nil, invalidf("attribute %q value must be at most %d bytes", code, maxAttributeValueBytes)
		}
		out = append(out, productAttribute{AttributeCode: code, Value: attrs[code]})
	}
	return out, nil
}

func defaultInt(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}
