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
	// writableTypeIDs are the types this gateway can actually make sellable.
	// downloadable and bundle are deliberately absent: creating one performs only
	// POST /products, and the package implements neither the bundle option and
	// selection endpoints nor the downloadable link and sample endpoints, so the
	// result is an enabled, visible, unsellable product reported as created.
	writableTypeIDs = map[string]struct{}{"simple": {}, "virtual": {}}
	// unsupportedTypeIDs names the missing capability, so a caller that reaches
	// validation gets the reason rather than a generic enum error.
	unsupportedTypeIDs = map[string]string{
		"bundle":       "bundle options and selections, which this gateway cannot create",
		"downloadable": "downloadable links and samples, which this gateway cannot create",
	}
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

	// SEO and content fields. They map to the standard Magento attributes
	// description, short_description, url_key, meta_title, meta_keywords, and
	// meta_description.
	Description      string
	ShortDescription string
	URLKey           string
	MetaTitle        string
	MetaKeywords     string
	MetaDescription  string

	// Pricing and shipping attributes.
	Weight          string
	SpecialPrice    string
	SpecialFromDate string
	SpecialToDate   string
	Cost            string
	TaxClassID      int
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

	Description      *string
	ShortDescription *string
	URLKey           *string
	MetaTitle        *string
	MetaKeywords     *string
	MetaDescription  *string

	// CategoryIDs and WebsiteIDs replace the product's assignments when
	// non-nil; an empty non-nil slice clears them. Nil leaves them untouched.
	CategoryIDs []int
	WebsiteIDs  []int

	Weight          *string
	SpecialPrice    *string
	SpecialFromDate *string
	SpecialToDate   *string
	Cost            *string
	TaxClassID      *int
}

type productAttribute struct {
	AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	Value         string `json:"value" jsonschema:"Value of the entry."`
}

type categoryLink struct {
	CategoryID string `json:"category_id" jsonschema:"Magento category entity id."`
	Position   int    `json:"position" jsonschema:"Sort position among siblings."`
}

type productExtension struct {
	WebsiteIDs                 []int                `json:"-"`
	CategoryLinks              []categoryLink       `json:"-"`
	ConfigurableProductOptions []configurableOption `json:"-"`
}

// MarshalJSON emits only the extension fields that are non-nil. A non-nil empty
// slice is emitted as [] so an update can explicitly clear assignments; nil is
// omitted entirely.
func (e productExtension) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, 3)
	if e.WebsiteIDs != nil {
		out["website_ids"] = e.WebsiteIDs
	}
	if e.CategoryLinks != nil {
		out["category_links"] = e.CategoryLinks
	}
	if e.ConfigurableProductOptions != nil {
		out["configurable_product_options"] = e.ConfigurableProductOptions
	}
	return json.Marshal(out)
}

type productPayload struct {
	SKU                 string             `json:"sku" jsonschema:"Product SKU."`
	Name                string             `json:"name,omitempty" jsonschema:"Display name."`
	AttributeSetID      int                `json:"attribute_set_id,omitempty"`
	TypeID              string             `json:"type_id,omitempty"`
	Price               string             `json:"price,omitempty"`
	Weight              string             `json:"weight,omitempty"`
	Status              int                `json:"status,omitempty" jsonschema:"Current status."`
	Visibility          int                `json:"visibility,omitempty"`
	CustomAttributes    []productAttribute `json:"custom_attributes,omitempty"`
	ExtensionAttributes *productExtension  `json:"extension_attributes,omitempty"`
}

type productWriteBody struct {
	Product productPayload `json:"product" jsonschema:"The product that was created or read."`
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
	if _, ok := in.Attributes["weight"]; ok {
		return productPayload{}, invalidf("weight must be set with the weight field, not as a custom attribute")
	}
	attributes, err := customAttributes(in.Attributes)
	if err != nil {
		return productPayload{}, err
	}
	seo, err := seoAttributes(in.Description, in.ShortDescription, in.URLKey,
		in.MetaTitle, in.MetaKeywords, in.MetaDescription)
	if err != nil {
		return productPayload{}, err
	}
	attributes = append(attributes, seo...)
	pricing, err := pricingAttributes(in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost, in.TaxClassID)
	if err != nil {
		return productPayload{}, err
	}
	attributes = append(attributes, pricing...)
	payload := productPayload{
		SKU: in.SKU, Name: strings.TrimSpace(in.Name), AttributeSetID: in.AttributeSetID,
		TypeID: in.TypeID, Price: in.Price, Weight: in.Weight, Status: in.Status, Visibility: in.Visibility,
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
	if _, ok := in.Attributes["weight"]; ok {
		return productPayload{}, invalidf("weight must be set with the weight field, not as a custom attribute")
	}
	attributes, err := customAttributes(in.Attributes)
	if err != nil {
		return productPayload{}, err
	}
	updateSEO := []*string{in.Description, in.ShortDescription, in.URLKey, in.MetaTitle, in.MetaKeywords, in.MetaDescription}
	seo, err := seoAttributesFromPointers(updateSEO)
	if err != nil {
		return productPayload{}, err
	}
	attributes = append(attributes, seo...)
	pricing, err := pricingAttributesFromPointers([]*string{
		in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost,
	}, in.TaxClassID)
	if err != nil {
		return productPayload{}, err
	}
	attributes = append(attributes, pricing...)
	payload := productPayload{SKU: sku, CustomAttributes: attributes}
	if in.CategoryIDs != nil || in.WebsiteIDs != nil {
		ext := &productExtension{}
		if in.WebsiteIDs != nil {
			ext.WebsiteIDs = append([]int{}, in.WebsiteIDs...)
		}
		if in.CategoryIDs != nil {
			for i, id := range in.CategoryIDs {
				ext.CategoryLinks = append(ext.CategoryLinks, categoryLink{CategoryID: strconv.Itoa(id), Position: i})
			}
			if ext.CategoryLinks == nil {
				ext.CategoryLinks = []categoryLink{}
			}
		}
		payload.ExtensionAttributes = ext
	}
	if in.Name != nil {
		if err := validateProductName(*in.Name); err != nil {
			return productPayload{}, err
		}
		payload.Name = strings.TrimSpace(*in.Name)
	}
	if in.Weight != nil {
		if err := validatePricingFields(*in.Weight, "", "", "", "", 0); err != nil {
			return productPayload{}, err
		}
		payload.Weight = *in.Weight
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
	if in.CategoryIDs != nil {
		if err := validatePositiveIDs("category_ids", in.CategoryIDs, maxCategoryIDs); err != nil {
			return productPayload{}, err
		}
	}
	if in.WebsiteIDs != nil {
		if err := validatePositiveIDs("website_ids", in.WebsiteIDs, maxWebsiteIDs); err != nil {
			return productPayload{}, err
		}
	}
	seoProvided := false
	for _, value := range updateSEO {
		if value != nil {
			seoProvided = true
			break
		}
	}
	if in.Weight != nil || in.SpecialPrice != nil || in.SpecialFromDate != nil ||
		in.SpecialToDate != nil || in.Cost != nil || in.TaxClassID != nil {
		seoProvided = true
	}
	if payload.Name == "" && payload.Price == "" && payload.Status == 0 && payload.Visibility == 0 &&
		len(attributes) == 0 && !seoProvided && in.CategoryIDs == nil && in.WebsiteIDs == nil {
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
	if reason, ok := unsupportedTypeIDs[in.TypeID]; ok {
		return invalidf("type_id %q requires %s; create it in the admin or import it, then manage it here", in.TypeID, reason)
	}
	if _, ok := writableTypeIDs[in.TypeID]; !ok {
		return invalidf("type_id must be one of simple, virtual")
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
	if err := validateSEOValues(in.Description, in.ShortDescription, in.URLKey,
		in.MetaTitle, in.MetaKeywords, in.MetaDescription); err != nil {
		return err
	}
	if err := validatePricingFields(in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost, in.TaxClassID); err != nil {
		return err
	}
	if err := ensureNoReservedDuplicates(in.Attributes); err != nil {
		return err
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

// DeleteProduct deletes one product by SKU.
//
// Magento returns 404 for an unknown SKU, so a repeated delete is not a
// successful no-op; the tool is destructive and requires confirmation.
func (s *Service) DeleteProduct(ctx context.Context, sku string) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	_, err := s.client.do(ctx, http.MethodDelete, "/products/"+url.PathEscape(sku), nil, nil)
	return err
}

const (
	maxDescriptionBytes      = 65535
	maxShortDescriptionBytes = 2048
	maxMetaBytes             = 255
)

var urlKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,254}$`)

var seoAttributeOrder = []string{
	"description", "short_description", "url_key", "meta_title", "meta_keywords", "meta_description",
}

// validateSEOValues checks content and SEO field bounds. Lengths follow the
// Magento column sizes; the tool descriptions carry the recommended values for
// search results (meta_title about 60 characters, meta_description 120-160).
func validateSEOValues(description, shortDescription, urlKey, metaTitle, metaKeywords, metaDescription string) error {
	checks := []struct {
		code  string
		value string
		max   int
	}{
		{"description", description, maxDescriptionBytes},
		{"short_description", shortDescription, maxShortDescriptionBytes},
		{"meta_title", metaTitle, maxMetaBytes},
		{"meta_keywords", metaKeywords, maxMetaBytes},
		{"meta_description", metaDescription, maxMetaBytes},
	}
	for _, check := range checks {
		if err := validateSEOAttribute(check.code, check.value); err != nil {
			return err
		}
	}
	if urlKey != "" && !urlKeyRE.MatchString(urlKey) {
		return invalidf("url_key must be lowercase letters, digits, and hyphens, starting with a letter or digit")
	}
	return nil
}

// validateSEOAttribute checks one SEO/content attribute; import validation
// reuses it for CSV/JSON rows.
func validateSEOAttribute(code, value string) error {
	if value == "" {
		return nil
	}
	var limit int
	switch code {
	case "description":
		limit = maxDescriptionBytes
	case "short_description":
		limit = maxShortDescriptionBytes
	case "url_key":
		if !urlKeyRE.MatchString(value) {
			return invalidf("url_key must be lowercase letters, digits, and hyphens")
		}
		return nil
	case "meta_title", "meta_keywords", "meta_description":
		limit = maxMetaBytes
	default:
		return nil
	}
	if len(value) > limit {
		return invalidf("%s must be at most %d bytes", code, limit)
	}
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			return invalidf("%s must not contain control characters", code)
		}
	}
	return nil
}

func seoAttributes(description, shortDescription, urlKey, metaTitle, metaKeywords, metaDescription string) ([]productAttribute, error) {
	return seoAttributesFromPointers([]*string{
		stringPtrIfSet(description), stringPtrIfSet(shortDescription), stringPtrIfSet(urlKey),
		stringPtrIfSet(metaTitle), stringPtrIfSet(metaKeywords), stringPtrIfSet(metaDescription),
	})
}

func seoAttributesFromPointers(values []*string) ([]productAttribute, error) {
	out := make([]productAttribute, 0, len(values))
	for i, code := range seoAttributeOrder {
		if i >= len(values) || values[i] == nil {
			continue
		}
		if err := validateSEOAttribute(code, *values[i]); err != nil {
			return nil, err
		}
		out = append(out, productAttribute{AttributeCode: code, Value: *values[i]})
	}
	return out, nil
}

func stringPtrIfSet(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

var (
	weightRE      = regexp.MustCompile(`^\d{1,12}(\.\d{1,4})?$`)
	specialDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

var pricingAttributeOrder = []string{
	"special_price", "special_from_date", "special_to_date", "cost", "tax_class_id",
}

// validatePricingFields checks pricing/shipping attribute bounds. Dates are
// YYYY-MM-DD; Magento accepts an empty value to clear them.
func validatePricingFields(weight, specialPrice, specialFrom, specialTo, cost string, taxClassID int) error {
	if weight != "" && !weightRE.MatchString(weight) {
		return invalidf("weight must be a non-negative decimal with at most 4 fraction digits")
	}
	if specialPrice != "" && !priceRE.MatchString(specialPrice) {
		return invalidf("special_price must be a non-negative decimal with at most 4 fraction digits")
	}
	if cost != "" && !priceRE.MatchString(cost) {
		return invalidf("cost must be a non-negative decimal with at most 4 fraction digits")
	}
	for field, value := range map[string]string{"special_from_date": specialFrom, "special_to_date": specialTo} {
		if value != "" && !specialDateRE.MatchString(value) {
			return invalidf("%s must be a YYYY-MM-DD date", field)
		}
	}
	if specialFrom != "" && specialTo != "" && specialTo < specialFrom {
		return invalidf("special_to_date must not be before special_from_date")
	}
	if taxClassID < 0 {
		return invalidf("tax_class_id must not be negative")
	}
	return nil
}

func pricingAttributes(_, specialPrice, specialFrom, specialTo, cost string, taxClassID int) ([]productAttribute, error) {
	// weight is a static product field, not a custom attribute; sending it as a
	// custom attribute is silently ignored by Magento.
	values := []*string{stringPtrIfSet(specialPrice), stringPtrIfSet(specialFrom), stringPtrIfSet(specialTo), stringPtrIfSet(cost)}
	var taxPointer *int
	if taxClassID > 0 {
		taxPointer = &taxClassID
	}
	return pricingAttributesFromPointers(values, taxPointer)
}

func pricingAttributesFromPointers(values []*string, taxClassID *int) ([]productAttribute, error) {
	out := make([]productAttribute, 0, len(values)+1)
	for i, code := range pricingAttributeOrder {
		if i >= len(values) || values[i] == nil {
			continue
		}
		out = append(out, productAttribute{AttributeCode: code, Value: *values[i]})
	}
	if taxClassID != nil {
		if *taxClassID < 0 {
			return nil, invalidf("tax_class_id must not be negative")
		}
		out = append(out, productAttribute{AttributeCode: "tax_class_id", Value: strconv.Itoa(*taxClassID)})
	}
	return out, nil
}

// ensureNoReservedDuplicates rejects reserved attribute codes set through the
// raw attributes map; each has a dedicated typed field.
func ensureNoReservedDuplicates(attributes map[string]string) error {
	reserved := append([]string{}, seoAttributeOrder...)
	reserved = append(reserved, pricingAttributeOrder...)
	for _, code := range reserved {
		if _, exists := attributes[code]; exists {
			return invalidf("%s must be set through its dedicated field, not the attributes map", code)
		}
	}
	return nil
}
