// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxVariantAttributes    = 5
	maxVariantValues        = 100
	maxConfigurableChildren = 50
	maxCompletedSKUs        = 20
	maxCompletedSKUsBytes   = 900

	stageChildren = "children"
	stageParent   = "parent"
	stageOptions  = "options"
	stageLinks    = "links"
)

// VariantAttribute declares one configurable option. The option label sent to
// Magento is the attribute code, since VariantAttribute carries no label.
type VariantAttribute struct {
	AttributeID int
	Code        string
	Values      []VariantValue
}

// VariantValue is one selectable option value of a VariantAttribute.
type VariantValue struct {
	ValueIndex int
	Label      string
}

// ChildProduct is one simple variant created and linked under a configurable
// parent. Attributes must carry every variant attribute code; an empty Price
// inherits the parent price.
type ChildProduct struct {
	SKU        string
	Name       string
	Attributes map[string]string
	Price      string
	Quantity   *int

	Description      string
	ShortDescription string
	URLKey           string
	MetaTitle        string
	MetaKeywords     string
	MetaDescription  string

	Weight          string
	SpecialPrice    string
	SpecialFromDate string
	SpecialToDate   string
	Cost            string
	TaxClassID      int
}

// CreateConfigurableInput describes one configurable product to create.
type CreateConfigurableInput struct {
	Parent   CreateProductInput
	Variants []VariantAttribute
	Children []ChildProduct
}

// ConfigurableProduct is a configurable parent together with its linked children.
type ConfigurableProduct struct {
	Parent        Product
	Children      []Product
	ParentInStock bool `json:"parent_in_stock"`
}

type configurableOption struct {
	AttributeID string                    `json:"attribute_id"`
	Label       string                    `json:"label"`
	Position    int                       `json:"position"`
	Values      []configurableOptionValue `json:"values"`
}

type configurableOptionValue struct {
	ValueIndex int `json:"value_index"`
}

type configurableOptionBody struct {
	Option configurableOption `json:"option"`
}

type configurableChildBody struct {
	ChildSKU string `json:"childSku"`
}

// CreateConfigurable creates a configurable product in the documented order:
// every child simple product first, then the parent with type_id=configurable
// and its configurable_product_options, then one child link per child.
//
// This operation is NOT atomic. A failure returns an error whose Details carry
// stage (children, parent, options, or links) and completed_skus (bounded to 20
// SKUs); everything already created stays in Magento, so the caller must run
// compensation before retrying.
func (s *Service) CreateConfigurable(ctx context.Context, in CreateConfigurableInput) (*ConfigurableProduct, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	if err := validateConfigurableInput(in); err != nil {
		return nil, err
	}
	completed := make([]string, 0, len(in.Children)+1)
	children := make([]Product, 0, len(in.Children))
	for _, child := range in.Children {
		product, err := s.createChild(ctx, in.Parent, child)
		if err != nil {
			if productCreated(err) {
				completed = append(completed, child.SKU)
			}
			return nil, writeStageError(stageChildren, completed, child.SKU, err)
		}
		children = append(children, *product)
		completed = append(completed, child.SKU)
	}
	parent, err := s.createConfigurableParent(ctx, in)
	if err != nil {
		if productCreated(err) {
			completed = append(completed, in.Parent.SKU)
		}
		return nil, writeStageError(stageParent, completed, in.Parent.SKU, err)
	}
	completed = append(completed, in.Parent.SKU)
	for _, child := range in.Children {
		if err := s.AddChild(ctx, in.Parent.SKU, child.SKU); err != nil {
			return nil, writeStageError(stageLinks, completed, child.SKU, err)
		}
	}
	inStock := false
	if parent.ID > 0 {
		if err := s.setParentStockStatus(ctx, in.Parent.SKU, parent.ID); err == nil {
			inStock = true
		}
	}
	return &ConfigurableProduct{Parent: *parent, Children: children, ParentInStock: inStock}, nil
}

func (s *Service) createChild(ctx context.Context, parent CreateProductInput, child ChildProduct) (*Product, error) {
	price := child.Price
	if price == "" {
		price = parent.Price
	}
	name := strings.TrimSpace(child.Name)
	if name == "" {
		name = childProductName(parent.Name, child.SKU)
	}
	return s.Create(ctx, CreateProductInput{
		SKU: child.SKU, Name: name, AttributeSetID: parent.AttributeSetID,
		TypeID: simpleTypeID, Price: price, Status: 1, Visibility: 1,
		WebsiteIDs: parent.WebsiteIDs, Attributes: child.Attributes, Quantity: child.Quantity,
		Description: child.Description, ShortDescription: child.ShortDescription, URLKey: child.URLKey,
		MetaTitle: child.MetaTitle, MetaKeywords: child.MetaKeywords, MetaDescription: child.MetaDescription,
		Weight: child.Weight, SpecialPrice: child.SpecialPrice, SpecialFromDate: child.SpecialFromDate,
		SpecialToDate: child.SpecialToDate, Cost: child.Cost, TaxClassID: child.TaxClassID,
	})
}

func (s *Service) createConfigurableParent(ctx context.Context, in CreateConfigurableInput) (*Product, error) {
	parent := in.Parent
	parent.TypeID = configurableTypeID
	parent.Status = defaultInt(parent.Status, defaultProductStatus)
	parent.Visibility = defaultInt(parent.Visibility, defaultProductVisibility)
	payload, err := buildCreatePayload(parent)
	if err != nil {
		return nil, err
	}
	options := make([]configurableOption, 0, len(in.Variants))
	for i, variant := range in.Variants {
		options = append(options, configurableOption{
			AttributeID: strconv.Itoa(variant.AttributeID), Label: variant.Code,
			Position: i, Values: optionValues(variant),
		})
	}
	if payload.ExtensionAttributes == nil {
		payload.ExtensionAttributes = &productExtension{}
	}
	payload.ExtensionAttributes.ConfigurableProductOptions = options
	raw, err := s.client.do(ctx, http.MethodPost, "/products", nil, productWriteBody{Product: payload})
	if err != nil {
		return nil, err
	}
	return decodeProduct(raw, parent.SKU)
}

// SetOptions declares variant attributes on an existing configurable product
// with POST /configurable-products/{sku}/options, one option per call. Every
// option is validated before the first write so a bad option never leaves a
// half-declared set.
func (s *Service) SetOptions(ctx context.Context, parentSKU string, options []VariantAttribute) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if err := validateSKU(parentSKU); err != nil {
		return err
	}
	if err := validateVariantAttributes(options); err != nil {
		return err
	}
	path := "/configurable-products/" + url.PathEscape(parentSKU) + "/options"
	for i, option := range options {
		body := configurableOptionBody{Option: configurableOption{
			AttributeID: strconv.Itoa(option.AttributeID), Label: option.Code, Position: i, Values: optionValues(option),
		}}
		if _, err := s.client.do(ctx, http.MethodPost, path, nil, body); err != nil {
			return stageFailure(stageOptions, parentSKU, err)
		}
	}
	return nil
}

// AddChild links an existing simple product to a configurable parent with
// POST /configurable-products/{sku}/child. Magento rejects a duplicate link, so
// the call is not idempotent; the child must already carry the variant values.
func (s *Service) AddChild(ctx context.Context, parentSKU, childSKU string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if err := validateSKU(parentSKU); err != nil {
		return err
	}
	if err := validateSKU(childSKU); err != nil {
		return err
	}
	if parentSKU == childSKU {
		return invalidf("a configurable product cannot be linked to itself")
	}
	path := "/configurable-products/" + url.PathEscape(parentSKU) + "/child"
	if _, err := s.client.do(ctx, http.MethodPost, path, nil, configurableChildBody{ChildSKU: childSKU}); err != nil {
		return stageFailure(stageLinks, parentSKU, err)
	}
	return nil
}

// Children lists the linked children of a configurable product with
// GET /configurable-products/{sku}/children.
func (s *Service) Children(ctx context.Context, parentSKU string) ([]Product, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	if err := validateSKU(parentSKU); err != nil {
		return nil, err
	}
	path := "/configurable-products/" + url.PathEscape(parentSKU) + "/children"
	raw, err := s.client.do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	children := []Product{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &children); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable configurable children payload").WithCause(err)
		}
	}
	if children == nil {
		children = []Product{}
	}
	return children, nil
}

// GetConfigurable reads one configurable parent and its linked children. It
// returns not_found when the SKU exists but is not a configurable product.
func (s *Service) GetConfigurable(ctx context.Context, sku string) (*ConfigurableProduct, error) {
	parent, err := s.Get(ctx, sku)
	if err != nil {
		return nil, err
	}
	if parent.TypeID != configurableTypeID {
		e := errs.New(errs.CodeNotFound, "Magento product is not a configurable product")
		_ = e.WithDetail("sku", sku)
		return nil, e
	}
	children, err := s.Children(ctx, sku)
	if err != nil {
		return nil, err
	}
	return &ConfigurableProduct{Parent: *parent, Children: children}, nil
}

// validateConfigurableInput validates every SKU, attribute code, and option
// before the first write so CreateConfigurable never starts on invalid input.
func validateConfigurableInput(in CreateConfigurableInput) error {
	if err := validateProductFields(in.Parent); err != nil {
		return err
	}
	if in.Parent.TypeID != "" && in.Parent.TypeID != configurableTypeID {
		return invalidf("parent type_id must be configurable")
	}
	if in.Parent.Quantity != nil {
		return invalidf("parent quantity is not supported for configurable products; set child quantities instead")
	}
	if err := validateVariantAttributes(in.Variants); err != nil {
		return err
	}
	if len(in.Children) == 0 {
		return invalidf("configurable product requires at least one child")
	}
	if len(in.Children) > maxConfigurableChildren {
		return invalidf("configurable product supports at most %d children", maxConfigurableChildren)
	}
	codes := make(map[string]struct{}, len(in.Variants))
	allowed := make(map[string]map[string]struct{}, len(in.Variants))
	for _, variant := range in.Variants {
		codes[variant.Code] = struct{}{}
		values := make(map[string]struct{}, len(variant.Values))
		for _, value := range variant.Values {
			values[strconv.Itoa(value.ValueIndex)] = struct{}{}
		}
		allowed[variant.Code] = values
	}
	seen := map[string]struct{}{in.Parent.SKU: {}}
	combinations := make(map[string]string, len(in.Children))
	for _, child := range in.Children {
		if err := validateChildProduct(child, codes, allowed); err != nil {
			return err
		}
		if _, duplicate := seen[child.SKU]; duplicate {
			return invalidf("child sku %q is duplicated", child.SKU)
		}
		seen[child.SKU] = struct{}{}
		combination := variantCombination(child, codes)
		if previous, duplicate := combinations[combination]; duplicate {
			return invalidf("children %q and %q carry the same variant values", previous, child.SKU)
		}
		combinations[combination] = child.SKU
	}
	return nil
}

func validateVariantAttributes(attributes []VariantAttribute) error {
	if len(attributes) == 0 {
		return invalidf("at least one variant attribute is required")
	}
	if len(attributes) > maxVariantAttributes {
		return invalidf("at most %d variant attributes are supported", maxVariantAttributes)
	}
	seen := make(map[string]struct{}, len(attributes))
	for _, attribute := range attributes {
		if err := validateVariantAttribute(attribute); err != nil {
			return err
		}
		if _, duplicate := seen[attribute.Code]; duplicate {
			return invalidf("variant attribute code %q is duplicated", attribute.Code)
		}
		seen[attribute.Code] = struct{}{}
	}
	return nil
}

func validateVariantAttribute(attribute VariantAttribute) error {
	if attribute.AttributeID <= 0 {
		return invalidf("variant attribute %q must have a positive attribute_id", attribute.Code)
	}
	if err := validateAttributeCode(attribute.Code); err != nil {
		return err
	}
	if len(attribute.Values) == 0 {
		return invalidf("variant attribute %q must declare at least one value", attribute.Code)
	}
	if len(attribute.Values) > maxVariantValues {
		return invalidf("variant attribute %q supports at most %d values", attribute.Code, maxVariantValues)
	}
	seen := make(map[int]struct{}, len(attribute.Values))
	for _, value := range attribute.Values {
		if value.ValueIndex <= 0 {
			return invalidf("variant attribute %q value_index must be a positive integer", attribute.Code)
		}
		if _, duplicate := seen[value.ValueIndex]; duplicate {
			return invalidf("variant attribute %q value_index %d is duplicated", attribute.Code, value.ValueIndex)
		}
		if len(value.Label) > maxAttributeValueBytes {
			return invalidf("variant attribute %q value label must be at most %d bytes", attribute.Code, maxAttributeValueBytes)
		}
		seen[value.ValueIndex] = struct{}{}
	}
	return nil
}

func validateChildProduct(child ChildProduct, codes map[string]struct{}, allowed map[string]map[string]struct{}) error {
	if err := validateSKU(child.SKU); err != nil {
		return err
	}
	if child.Price != "" {
		if err := validatePrice(child.Price); err != nil {
			return err
		}
	}
	if child.Quantity != nil && *child.Quantity < 0 {
		return invalidf("child quantity must not be negative")
	}
	if _, err := customAttributes(child.Attributes); err != nil {
		return err
	}
	for code := range codes {
		value := strings.TrimSpace(child.Attributes[code])
		if value == "" {
			return invalidf("child %q must carry a value for variant attribute %q", child.SKU, code)
		}
		if _, ok := allowed[code][value]; !ok {
			return invalidf("child %q value %q for variant attribute %q is not one of the declared value_index values",
				child.SKU, value, code)
		}
	}
	return nil
}

// variantCombination builds a stable key of a child's variant values so two
// children cannot describe the same variant.
func variantCombination(child ChildProduct, codes map[string]struct{}) string {
	names := make([]string, 0, len(codes))
	for code := range codes {
		names = append(names, code)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, code := range names {
		builder.WriteString(code)
		builder.WriteByte('=')
		builder.WriteString(strings.TrimSpace(child.Attributes[code]))
		builder.WriteByte(';')
	}
	return builder.String()
}

func optionValues(attribute VariantAttribute) []configurableOptionValue {
	values := make([]configurableOptionValue, 0, len(attribute.Values))
	for _, value := range attribute.Values {
		values = append(values, configurableOptionValue{ValueIndex: value.ValueIndex})
	}
	return values
}

func childProductName(parentName, sku string) string {
	name := strings.TrimSpace(parentName + " " + sku)
	if len(name) <= maxProductNameBytes {
		return name
	}
	cut := maxProductNameBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return strings.TrimSpace(name[:cut])
}

func productCreated(err error) bool {
	var e *errs.Error
	return errors.As(err, &e) && e.Details["product_created"] == "true"
}

func stageFailure(stage, sku string, cause error) error {
	e := errs.New(errs.CodeOf(cause), stageMessage(stage, cause))
	e = e.WithCause(cause)
	e = e.WithDetail("stage", stage)
	if sku != "" {
		_ = e.WithDetail("sku", sku)
	}
	return e
}

// writeStageError reports a non-atomic failure with its stage and completed SKUs.
func writeStageError(stage string, completed []string, failedSKU string, cause error) error {
	e := errs.New(errs.CodeOf(cause), stageMessage(stage, cause)+
		"; completed writes are not rolled back and require compensation")
	e = e.WithCause(cause)
	e = e.WithDetail("stage", stage)
	e = e.WithDetail("completed_skus", boundedSKUs(completed))
	if failedSKU != "" {
		_ = e.WithDetail("failed_sku", failedSKU)
	}
	return e
}

func stageMessage(stage string, cause error) string {
	message := "Magento write failed at stage " + stage
	var e *errs.Error
	if errors.As(cause, &e) && e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// boundedSKUs renders at most maxCompletedSKUs comma-separated SKUs within the
// error detail size budget.
func boundedSKUs(skus []string) string {
	if len(skus) == 0 {
		return ""
	}
	if len(skus) > maxCompletedSKUs {
		skus = skus[:maxCompletedSKUs]
	}
	joined := strings.Join(skus, ",")
	if len(joined) > maxCompletedSKUsBytes {
		joined = joined[:maxCompletedSKUsBytes]
	}
	return joined
}

// RemoveChild unlinks a child product from a configurable product with
// DELETE /configurable-products/{sku}/children/{childSku}.
func (s *Service) RemoveChild(ctx context.Context, parentSKU, childSKU string) error {
	if err := s.requireClient(); err != nil {
		return err
	}
	if err := validateSKU(parentSKU); err != nil {
		return err
	}
	if err := validateSKU(childSKU); err != nil {
		return err
	}
	if parentSKU == childSKU {
		return invalidf("a configurable product cannot be unlinked from itself")
	}
	path := "/configurable-products/" + url.PathEscape(parentSKU) + "/children/" + url.PathEscape(childSKU)
	if _, err := s.client.do(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return stageFailure(stageLinks, parentSKU, err)
	}
	return nil
}

// UpdateConfigurable updates a configurable parent through the product update
// endpoint, refusing SKUs that are not configurable.
func (s *Service) UpdateConfigurable(ctx context.Context, sku string, in UpdateProductInput) (*Product, error) {
	product, err := s.Get(ctx, sku)
	if err != nil {
		return nil, err
	}
	if product.TypeID != "configurable" {
		return nil, invalidf("product %q is %s, not configurable", sku, product.TypeID)
	}
	return s.Update(ctx, sku, in)
}

const maxVariations = 50

// VariationInput is one child product to create under an existing parent.
type VariationInput struct {
	SKU        string
	Name       string
	Attributes map[string]string
}

// VariationRow is one variation's outcome.
type VariationRow struct {
	SKU    string `json:"sku"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// VariationResult reports a partial-success variation creation.
type VariationResult struct {
	Total   int            `json:"total"`
	Created int            `json:"created"`
	Skipped int            `json:"skipped"`
	Failed  int            `json:"failed"`
	Rows    []VariationRow `json:"rows"`
}

// CreateVariations creates simple children for an existing configurable parent
// and links them. Existing SKUs are skipped, so the call is idempotent per SKU;
// failures are reported per row and earlier children are not rolled back.
func (s *Service) CreateVariations(ctx context.Context, parentSKU string, variants []VariationInput) (*VariationResult, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(parentSKU); err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, invalidf("at least one variation is required")
	}
	if len(variants) > maxVariations {
		return nil, invalidf("at most %d variations are allowed per call", maxVariations)
	}
	seen := make(map[string]struct{}, len(variants))
	for _, variant := range variants {
		if err := validateSKU(variant.SKU); err != nil {
			return nil, err
		}
		key := strings.ToUpper(variant.SKU)
		if _, duplicate := seen[key]; duplicate {
			return nil, invalidf("sku %q is duplicated", variant.SKU)
		}
		seen[key] = struct{}{}
		if len(variant.Attributes) == 0 {
			return nil, invalidf("variation %q needs at least one attribute value", variant.SKU)
		}
		for code, value := range variant.Attributes {
			if !attributeCodeRE.MatchString(code) {
				return nil, invalidf("attribute code %q is invalid", code)
			}
			if strings.TrimSpace(value) == "" {
				return nil, invalidf("attribute %q has an empty value for %q", code, variant.SKU)
			}
		}
	}
	parent, err := s.Get(ctx, parentSKU)
	if err != nil {
		return nil, err
	}
	if parent.TypeID != "configurable" {
		return nil, invalidf("product %q is %s, not configurable", parentSKU, parent.TypeID)
	}
	result := &VariationResult{Total: len(variants), Rows: make([]VariationRow, 0, len(variants))}
	for _, variant := range variants {
		row := VariationRow{SKU: variant.SKU}
		if _, err := s.Get(ctx, variant.SKU); err == nil {
			row.Status = "exists"
			result.Skipped++
			result.Rows = append(result.Rows, row)
			continue
		} else if errs.CodeOf(err) != errs.CodeNotFound {
			row.Status = "failed"
			row.Error = importErrorMessage(err)
			result.Failed++
			result.Rows = append(result.Rows, row)
			continue
		}
		name := parent.Name + " " + variant.SKU
		child, err := s.Create(ctx, CreateProductInput{
			SKU: variant.SKU, Name: name, AttributeSetID: parent.AttributeSetID,
			TypeID: "simple", Price: fmt.Sprintf("%.2f", parent.Price),
			Status: 1, Visibility: 4, Attributes: variant.Attributes,
		})
		if err != nil {
			row.Status = "failed"
			row.Error = importErrorMessage(err)
			result.Failed++
			result.Rows = append(result.Rows, row)
			continue
		}
		if err := s.AddChild(ctx, parentSKU, child.SKU); err != nil {
			row.Status = "failed"
			row.Error = importErrorMessage(err)
			result.Failed++
			result.Rows = append(result.Rows, row)
			continue
		}
		row.Status = "created"
		result.Created++
		result.Rows = append(result.Rows, row)
	}
	return result, nil
}

const (
	maxConfigurableOptions = 10
	maxOptionValues        = 100
)

// ConfigurableOptionInput declares one variant attribute and its option values.
type ConfigurableOptionInput struct {
	AttributeID int
	Label       string
	Position    int
	Values      []int
}

// SetConfigurableOptions declares the variant attributes (and value ordering) of
// an existing configurable parent with
// POST /configurable-products/{sku}/options.
//
// Magento saves one option per call; a failure leaves earlier options in place.
func (s *Service) SetConfigurableOptions(ctx context.Context, sku string, options []ConfigurableOptionInput) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if len(options) == 0 {
		return invalidf("at least one option is required")
	}
	if len(options) > maxConfigurableOptions {
		return invalidf("at most %d options are allowed per call", maxConfigurableOptions)
	}
	seen := make(map[int]struct{}, len(options))
	for _, option := range options {
		if option.AttributeID <= 0 {
			return invalidf("attribute_id must be a positive integer")
		}
		if _, duplicate := seen[option.AttributeID]; duplicate {
			return invalidf("attribute_id %d is duplicated", option.AttributeID)
		}
		seen[option.AttributeID] = struct{}{}
		if trimmed := strings.TrimSpace(option.Label); trimmed == "" || len(option.Label) > 255 {
			return invalidf("label is required and must be at most 255 bytes")
		}
		if option.Position < 0 || option.Position > 1000 {
			return invalidf("position must be between 0 and 1000")
		}
		if len(option.Values) == 0 || len(option.Values) > maxOptionValues {
			return invalidf("values must have 1-%d entries", maxOptionValues)
		}
		values := make(map[int]struct{}, len(option.Values))
		for _, value := range option.Values {
			if value <= 0 {
				return invalidf("value indexes must be positive integers")
			}
			if _, duplicate := values[value]; duplicate {
				return invalidf("value index %d is duplicated", value)
			}
			values[value] = struct{}{}
		}
	}
	parent, err := s.Get(ctx, sku)
	if err != nil {
		return err
	}
	if parent.TypeID != "configurable" {
		return invalidf("product %q is %s, not configurable", sku, parent.TypeID)
	}
	for _, option := range options {
		values := make([]configurableOptionValue, 0, len(option.Values))
		for _, value := range option.Values {
			values = append(values, configurableOptionValue{ValueIndex: value})
		}
		body := configurableOptionBody{Option: configurableOption{
			AttributeID: strconv.Itoa(option.AttributeID), Label: option.Label, Position: option.Position, Values: values,
		}}
		if _, err := s.client.do(ctx, http.MethodPost,
			"/configurable-products/"+url.PathEscape(sku)+"/options", nil, body); err != nil {
			return err
		}
	}
	return nil
}

// setParentStockStatus makes a configurable parent salable after its children
// are linked. Magento keeps a legacy stock item for the parent whose
// is_in_stock flag is not updated by MSI source items alone.
func (s *Service) setParentStockStatus(ctx context.Context, sku string, productID int) error {
	body := struct {
		StockItem struct {
			IsInStock bool `json:"is_in_stock"`
		} `json:"stockItem"`
	}{}
	body.StockItem.IsInStock = true
	_, err := s.client.do(ctx, http.MethodPut,
		"/products/"+url.PathEscape(sku)+"/stockItems/"+strconv.Itoa(productID), nil, body)
	return err
}
