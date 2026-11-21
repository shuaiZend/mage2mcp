// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shuaiZend/mage2mcp/internal/mcp/toolerr"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

const (
	writeToolTimeout = 60 * time.Second

	capabilityProductWrite      = "magento.product.write"
	capabilityConfigurableWrite = "magento.configurable.write"
	capabilityInventoryWrite    = "magento.inventory.write"

	productCreateToolName        = "magento.product.create"
	productUpdateToolName        = "magento.product.update"
	configurableCreateToolName   = "magento.configurable.create"
	configurableAddChildToolName = "magento.configurable.add_child"
	inventoryAssignToolName      = "magento.inventory.assign"

	productCreateToolTitle        = "Create Magento product"
	productUpdateToolTitle        = "Update Magento product"
	configurableCreateToolTitle   = "Create Magento configurable product"
	configurableAddChildToolTitle = "Link Magento configurable child"
	inventoryAssignToolTitle      = "Assign Magento inventory source items"

	priceInputPattern         = `^[0-9]+(\\.[0-9]{1,4})?$`
	attributeCodeInputPattern = `^[a-z0-9_]{1,64}$`
	sourceCodeInputPattern    = `^[a-z0-9_]{1,64}$`
	instanceResolutionNote    = " The instance input is required and must name a configured Magento instance."

	productCreateToolDescription = "Creates one product in the configured Magento 2 REST catalog " +
		"(POST /rest/{store_code}/V1/products) using the integration credential. " +
		"This is a write operation: it changes catalog data, is not idempotent, and a retry can create a duplicate SKU. " +
		"When quantity is supplied the product is created first and its default-source inventory is assigned afterwards, " +
		"so an inventory failure leaves the product created and the error carries the SKU and stage in its details. " +
		"Changing url_key changes the storefront URL; Magento does not create a 301 redirect for API writes. " +
		// The detailed rule lives in the server instructions, which have no length
		// cap; this carries the pointer and the one fact that changes which tool to call.
		"COPYING: discover the source type, options and attributes first; ask a human " +
		"before creating any category, attribute or attribute set. Configurable sources " +
		"need magento.configurable.create. See the server instructions. " +
		instanceResolutionNote
	productUpdateToolDescription = "Updates one product in the configured Magento 2 REST catalog " +
		"Changing url_key changes the storefront URL; Magento does not create a 301 redirect for API writes. " +
		"(PUT /rest/{store_code}/V1/products/{sku}) using the integration credential. " +
		"This is a write operation: it changes catalog data and is not idempotent as an agent action. " +
		"Only supplied fields are sent, so Magento's merge semantics preserve omitted attributes; " +
		"at least one of name, price, status, visibility, or attributes is required." +
		instanceResolutionNote
	configurableCreateToolDescription = "Creates a configurable product in the configured Magento 2 REST catalog as a staged workflow: " +
		"precheck, children, parent, links (verified by read-back), stock. The result always carries the execution record: operation_id, " +
		"outcome completed|partial, stages, completed_stages, failed_stage, orphan_skus. A partial run leaves completed writes in place and " +
		"reports unlinked children as orphan_skus, which must be reconciled. Resume with the returned operation_id: existing children and " +
		"parent are reused and missing links repaired, so a resumed run is idempotent and writes no duplicate SKU; a fresh run is not " +
		"idempotent and refuses a pre-existing SKU. Requires human confirmation. " +
		// The detailed rule lives in the server instructions; here is the part that
		// changes which call to make.
		"COPYING: discover the source type, options and attributes first, and ask a human " +
		"before creating any category, attribute or attribute set. " +
		instanceResolutionNote
	configurableAddChildToolDescription = "Links an existing simple product to a configurable parent in the configured Magento 2 REST catalog " +
		"(POST /rest/{store_code}/V1/configurable-products/{sku}/child) using the integration credential. " +
		"This is a write operation and is not idempotent: Magento rejects a duplicate link, and the child must already " +
		"carry every variant attribute value." +
		instanceResolutionNote
	inventoryAssignToolDescription = "Assigns per-source quantities in the configured Magento 2 REST catalog " +
		"(POST /rest/{store_code}/V1/inventory/source-items) using the integration credential. " +
		"This is a write operation: quantity replaces the existing source quantity instead of adding to it, and the batch " +
		"response carries no per-item outcome, so the operation is marked non-idempotent and callers verify by re-reading source items. " +
		"At most 100 items are accepted per call. MSI source items alone do not update a configurable parent's legacy stock status; verify the parent is salable after assigning child stock." +
		instanceResolutionNote

	instanceProp                = `"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; required, and must name a configured instance."}`
	confirmationProp            = `"confirmation_id": {"type": "string", "maxLength": 128, "description": "Approval id from a confirmation_required response; resend the same arguments with this id to execute."}`
	idempotencyKeyProp          = `"idempotency_key": {"type": "string", "maxLength": 256, "description": "Optional replay key. Repeating it with the same arguments returns the recorded outcome without a second upstream mutation; reusing it with different arguments is rejected as idempotency_key_reuse. Without a key one is derived from the arguments. The key is not caller-scoped, so treat it as a secret: use an unguessable value (a UUID), never a predictable one."}`
	configurableOperationIDProp = `"operation_id": {"type": "string", "pattern": "^[A-Za-z0-9._:-]{1,128}$", "description": "Optional resume handle. Pass the operation_id returned by a partial configurable.create to resume it: children and a parent that already exist are reused instead of re-created, missing links are repaired, and no duplicate SKU is written. Omit it for a fresh create, which refuses a pre-existing SKU."}`
	skuProp                     = `"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU, 1-64 characters from [A-Za-z0-9._:-]."}`
	nameProp                    = `"name": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display name shown in the storefront."}`
	setProp                     = `"attribute_set_id": {"type": "integer", "minimum": 1, "description": "Attribute set the product belongs to; it decides which custom attributes the product can carry. Use magento.attribute.sets to list the ids."}`
	// Only the types the gateway can make sellable are offered. bundle and
	// downloadable need option/link endpoints that do not exist here; advertising
	// them produced enabled, visible, unsellable products (MAG-21).
	typeIDProp           = `"type_id": {"type": "string", "enum": ["simple", "virtual"], "description": "Product type. bundle and downloadable are rejected: their options, selections, links and samples cannot be created through this gateway."}`
	priceProp            = `"price": {"type": "string", "pattern": "` + priceInputPattern + `", "description": "Decimal price string, up to 4 fraction digits."}`
	statusProp           = `"status": {"type": "integer", "enum": [1, 2], "description": "1 enabled (default), 2 disabled. A disabled product exists but is not sellable."}`
	visibilityProp       = `"visibility": {"type": "integer", "enum": [1, 2, 3, 4], "description": "1 not visible, 2 catalog only, 3 search only, 4 catalog and search (the usual value)."}`
	websiteIDsProp       = `"website_ids": {"type": "array", "items": {"type": "integer", "minimum": 1, "description": "Requested page size."}, "minItems": 1, "maxItems": 50, "description": "Websites the product is published to. The default store website is usually 1."}`
	categoryIDsProp      = `"category_ids": {"type": "array", "items": {"type": "integer", "minimum": 1, "description": "Requested page size."}, "maxItems": 500, "description": "Categories to assign the product to. Omit to leave placement untouched; an empty array clears it."}`
	attributesProp       = `"attributes": {"type": "object", "additionalProperties": {"type": "string", "maxLength": 255}, "description": "Custom EAV attributes keyed by attribute code."}`
	childAttributesProp  = `"attributes": {"type": "object", "minProperties": 1, "additionalProperties": {"type": "string", "maxLength": 255}, "description": "Variant attribute code to option value for this child, for example {\"color\": \"12\"}. Every child needs the parent's variant attributes."}`
	quantityProp         = `"quantity": {"type": "integer", "minimum": 0, "description": "Optional default-source quantity assigned after the product exists."}`
	descriptionProp      = `"description": {"type": "string", "maxLength": 65535, "description": "Full product description (HTML allowed). For SEO, write at least 300 characters of unique, keyword-relevant copy; do not duplicate other products."}`
	shortDescriptionProp = `"short_description": {"type": "string", "maxLength": 2048, "description": "Short summary shown in listings; a 1-2 sentence benefit statement works best."}`
	urlKeyProp           = `"url_key": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,254}$", "description": "SEO URL slug: lowercase letters, digits, and hyphens; include the main keyword, omit stop words."}`
	metaTitleProp        = `"meta_title": {"type": "string", "maxLength": 255, "description": "SEO title tag. 30-60 characters is the useful range for search results; front-load the keyword and brand."}`
	metaKeywordsProp     = `"meta_keywords": {"type": "string", "maxLength": 255, "description": "Optional comma-separated keywords; low ranking value, keep it short and relevant."}`
	metaDescriptionProp  = `"meta_description": {"type": "string", "maxLength": 255, "description": "SEO meta description. 120-160 characters is the useful range; state the benefit and include a call to action."}`
	seoProps             = descriptionProp + `, ` + shortDescriptionProp + `, ` + urlKeyProp + `, ` + metaTitleProp + `, ` + metaKeywordsProp + `, ` + metaDescriptionProp
	weightProp           = `"weight": {"type": "string", "pattern": "^\\d{1,12}(\\.\\d{1,4})?$", "description": "Product weight for shipping (decimal, up to 4 fraction digits)."}`
	specialPriceProp     = `"special_price": {"type": "string", "pattern": "^\\d{1,12}(\\.\\d{1,4})?$", "description": "Promotional price; pair it with special_from_date/special_to_date."}`
	specialFromProp      = `"special_from_date": {"type": "string", "pattern": "^\\d{4}-\\d{2}-\\d{2}$", "description": "Special price start date, YYYY-MM-DD."}`
	specialToProps       = `"special_to_date": {"type": "string", "pattern": "^\\d{4}-\\d{2}-\\d{2}$", "description": "Special price end date, YYYY-MM-DD; must not precede special_from_date."}`
	costProp             = `"cost": {"type": "string", "pattern": "^\\d{1,12}(\\.\\d{1,4})?$", "description": "Cost of goods, used for margin reporting."}`
	taxClassProp         = `"tax_class_id": {"type": "integer", "minimum": 0, "description": "Magento tax class id; 0 leaves the default."}`
	pricingProps         = weightProp + `, ` + specialPriceProp + `, ` + specialFromProp + `, ` + specialToProps + `, ` + costProp + `, ` + taxClassProp

	productCreateSchema        = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `, ` + nameProp + `, ` + setProp + `, ` + typeIDProp + `, ` + priceProp + `, ` + statusProp + `, ` + visibilityProp + `, ` + websiteIDsProp + `, ` + categoryIDsProp + `, ` + attributesProp + `, ` + quantityProp + `, ` + seoProps + `, ` + pricingProps + `}, "required": ["instance", "sku", "name", "attribute_set_id", "type_id", "price"], "additionalProperties": false}`
	productUpdateSchema        = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `, ` + nameProp + `, ` + priceProp + `, ` + statusProp + `, ` + visibilityProp + `, ` + attributesProp + `, ` + seoProps + `, ` + pricingProps + `, "category_ids": {"type": "array", "maxItems": 500, "items": {"type": "integer", "minimum": 1, "description": "Requested page size."}, "description": "Replaces the product's category assignments when provided; an empty array clears them."}, "website_ids": {"type": "array", "maxItems": 50, "items": {"type": "integer", "minimum": 1, "description": "Requested page size."}, "description": "Replaces the product's website assignments when provided."}}, "required": ["instance", "sku"], "additionalProperties": false}`
	configurableCreateSchema   = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + configurableOperationIDProp + `, "parent": {"type": "object", "description": "The configurable parent product. It is created first; the children are linked to it after.", "additionalProperties": false, "required": ["sku", "name", "attribute_set_id", "price"], "properties": {` + skuProp + `, ` + nameProp + `, ` + setProp + `, ` + priceProp + `, ` + statusProp + `, ` + visibilityProp + `, ` + websiteIDsProp + `, ` + categoryIDsProp + `, ` + attributesProp + `, ` + seoProps + `, ` + pricingProps + `}}, "variant_attributes": {"type": "array", "minItems": 1, "maxItems": 5, "description": "Attributes the shopper chooses between, for example color. Each must be a selectable attribute of the parent attribute set.", "items": {"type": "object", "additionalProperties": false, "required": ["attribute_id", "code", "values"], "properties": {"attribute_id": {"type": "integer", "minimum": 1}, "code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `"}, "values": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object", "additionalProperties": false, "required": ["value_index"], "properties": {"value_index": {"type": "integer", "minimum": 1}, "label": {"type": "string", "maxLength": 255, "description": "Label shown to the shopper."}}}}}}}, "children": {"type": "array", "minItems": 1, "maxItems": 50, "description": "The variant products. Every child needs a value for each variant attribute or the link is rejected.", "items": {"type": "object", "additionalProperties": false, "required": ["sku", "attributes"], "properties": {` + skuProp + `, "name": {"type": "string", "maxLength": 255, "description": "Display name."}, ` + childAttributesProp + `, ` + priceProp + `, ` + quantityProp + `, ` + seoProps + `}}}}, "required": ["instance", "parent", "variant_attributes", "children"], "additionalProperties": false}`
	configurableAddChildSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, "parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the configurable parent product."}, "child_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the variant child product."}}, "required": ["instance", "parent_sku", "child_sku"], "additionalProperties": false}`
	inventoryAssignSchema      = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, "items": {"type": "array", "minItems": 1, "maxItems": 100, "description": "Lines to act on. Each entry names an order item and a quantity; the quantities must not exceed what is still outstanding.", "items": {"type": "object", "additionalProperties": false, "required": ["sku", "source_code", "quantity"], "properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU."}, "source_code": {"type": "string", "pattern": "` + sourceCodeInputPattern + `", "description": "Inventory source code, for example default. Use magento.inventory.sources to list them."}, "quantity": {"type": "integer", "minimum": 0}, "status": {"type": "integer", "enum": [0, 1], "default": 1, "description": "1 in stock (default), 0 out of stock."}}}}}, "required": ["instance", "items"], "additionalProperties": false}`
)

type productCreateInput struct {
	Instance       string            `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU            string            `json:"sku" jsonschema:"Product SKU."`
	Name           string            `json:"name" jsonschema:"Display name."`
	AttributeSetID int               `json:"attribute_set_id"`
	TypeID         string            `json:"type_id"`
	Price          string            `json:"price"`
	Status         int               `json:"status" jsonschema:"Current status."`
	Visibility     int               `json:"visibility"`
	WebsiteIDs     []int             `json:"website_ids"`
	CategoryIDs    []int             `json:"category_ids"`
	Attributes     map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`
	Quantity       *int              `json:"quantity"`

	Description      string `json:"description"`
	ShortDescription string `json:"short_description"`
	URLKey           string `json:"url_key"`
	MetaTitle        string `json:"meta_title"`
	MetaKeywords     string `json:"meta_keywords"`
	MetaDescription  string `json:"meta_description"`

	Weight          string `json:"weight"`
	SpecialPrice    string `json:"special_price"`
	SpecialFromDate string `json:"special_from_date"`
	SpecialToDate   string `json:"special_to_date"`
	Cost            string `json:"cost"`
	TaxClassID      int    `json:"tax_class_id"`
}

func (in productCreateInput) toCreateInput() CreateProductInput {
	return CreateProductInput{
		SKU: in.SKU, Name: in.Name, AttributeSetID: in.AttributeSetID, TypeID: in.TypeID,
		Price: in.Price, Status: in.Status, Visibility: in.Visibility,
		WebsiteIDs: in.WebsiteIDs, CategoryIDs: in.CategoryIDs, Attributes: in.Attributes, Quantity: in.Quantity,
		Description: in.Description, ShortDescription: in.ShortDescription, URLKey: in.URLKey,
		MetaTitle: in.MetaTitle, MetaKeywords: in.MetaKeywords, MetaDescription: in.MetaDescription,
		Weight: in.Weight, SpecialPrice: in.SpecialPrice, SpecialFromDate: in.SpecialFromDate,
		SpecialToDate: in.SpecialToDate, Cost: in.Cost, TaxClassID: in.TaxClassID,
	}
}

type productUpdateInput struct {
	Instance   string            `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU        string            `json:"sku" jsonschema:"Product SKU."`
	Name       *string           `json:"name" jsonschema:"Display name."`
	Price      *string           `json:"price"`
	Status     *int              `json:"status" jsonschema:"Current status."`
	Visibility *int              `json:"visibility"`
	Attributes map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`

	Description      *string `json:"description"`
	ShortDescription *string `json:"short_description"`
	URLKey           *string `json:"url_key"`
	MetaTitle        *string `json:"meta_title"`
	MetaKeywords     *string `json:"meta_keywords"`
	MetaDescription  *string `json:"meta_description"`
	CategoryIDs      []int   `json:"category_ids"`
	WebsiteIDs       []int   `json:"website_ids"`

	Weight          *string `json:"weight"`
	SpecialPrice    *string `json:"special_price"`
	SpecialFromDate *string `json:"special_from_date"`
	SpecialToDate   *string `json:"special_to_date"`
	Cost            *string `json:"cost"`
	TaxClassID      *int    `json:"tax_class_id"`
}

type variantAttributeInput struct {
	AttributeID int                 `json:"attribute_id"`
	Code        string              `json:"code"`
	Values      []variantValueInput `json:"values" jsonschema:"Selectable values of this option."`
}

type variantValueInput struct {
	ValueIndex int    `json:"value_index"`
	Label      string `json:"label" jsonschema:"Label shown to the shopper."`
}

type childProductInput struct {
	SKU        string            `json:"sku" jsonschema:"Product SKU."`
	Name       string            `json:"name" jsonschema:"Display name."`
	Attributes map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`
	Price      string            `json:"price"`
	Quantity   *int              `json:"quantity"`

	Description      string `json:"description"`
	ShortDescription string `json:"short_description"`
	URLKey           string `json:"url_key"`
	MetaTitle        string `json:"meta_title"`
	MetaKeywords     string `json:"meta_keywords"`
	MetaDescription  string `json:"meta_description"`

	Weight          string `json:"weight"`
	SpecialPrice    string `json:"special_price"`
	SpecialFromDate string `json:"special_from_date"`
	SpecialToDate   string `json:"special_to_date"`
	Cost            string `json:"cost"`
	TaxClassID      int    `json:"tax_class_id"`
}

type configurableCreateInput struct {
	Instance          string                  `json:"instance" jsonschema:"Configured instance id the result came from."`
	OperationID       string                  `json:"operation_id" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Parent            productCreateInput      `json:"parent"`
	VariantAttributes []variantAttributeInput `json:"variant_attributes"`
	Children          []childProductInput     `json:"children" jsonschema:"Variant children linked to the parent."`
}

type configurableAddChildInput struct {
	Instance  string `json:"instance" jsonschema:"Configured instance id the result came from."`
	ParentSKU string `json:"parent_sku" jsonschema:"SKU of the configurable parent product."`
	ChildSKU  string `json:"child_sku" jsonschema:"SKU of the variant child product."`
}

type inventoryAssignInput struct {
	Instance string            `json:"instance" jsonschema:"Configured instance id the result came from."`
	Items    []sourceItemInput `json:"items" jsonschema:"Entries returned by this call."`
}

type sourceItemInput struct {
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Quantity   int    `json:"quantity"`
	Status     *int   `json:"status" jsonschema:"Current status."`
}

type productWriteOutput struct {
	Product Product `json:"product" jsonschema:"The product that was created or read."`
}

type configurableCreateOutput struct {
	Parent        Product   `json:"parent" jsonschema:"The configurable parent product that was created."`
	Children      []Product `json:"children" jsonschema:"The variant products created and linked to the parent. May be shorter than requested when the create stopped at a failed stage."`
	ParentInStock bool      `json:"parent_in_stock" jsonschema:"Whether the parent reports in stock. Read parent_stock_source before trusting it: an unobserved read is not an out-of-stock."`
	// ParentStockSource states how parent_in_stock was derived (the observed
	// child source items, or "unobserved: <code>" when the read failed). Without
	// it an unobserved stock read is indistinguishable from an observed
	// out-of-stock at the agent boundary.
	ParentStockSource string `json:"parent_stock_source,omitempty" jsonschema:"How parent_in_stock was derived: the observed child source items, or \"unobserved: <code>\" when the read failed. An unobserved read is not an out-of-stock."`

	// Execution record: an agent can tell a completed create from a partial one
	// and act on the failure without another read.
	OperationID     string              `json:"operation_id,omitempty" jsonschema:"Ledger operation id. Pass it back as operation_id to resume a partial create instead of starting over."`
	Outcome         ConfigurableOutcome `json:"outcome,omitempty" jsonschema:"completed or partial. A partial create has already written products and is not rolled back."`
	Stages          []ConfigurableStage `json:"stages,omitempty" jsonschema:"Per-stage record of what ran and what it produced, in order."`
	CompletedStages []string            `json:"completed_stages,omitempty" jsonschema:"Names of the stages that finished, so a resume knows where to continue."`
	FailedStage     string              `json:"failed_stage,omitempty" jsonschema:"Stage that failed, when outcome is partial."`
	OrphanSKUs      []string            `json:"orphan_skus,omitempty" jsonschema:"SKUs that exist in Magento but are not linked to the parent. Delete them or link them before retrying, or the retry will collide."`
}

type configurableAddChildOutput struct {
	ParentSKU string `json:"parent_sku" jsonschema:"SKU of the configurable parent product."`
	ChildSKU  string `json:"child_sku" jsonschema:"SKU of the variant child product."`
	Linked    bool   `json:"linked" jsonschema:"True when the child is now linked to the parent."`
}

type inventoryAssignOutput struct {
	Assigned int `json:"assigned" jsonschema:"True when the product is now assigned."`
}

// RegisterWrites adds the Magento write tools to reg. Every tool is a write that
// requires confirmation, runs with a 60-second timeout, and is never retried
// automatically. The confirmation middleware is installed by a later slice; the
// metadata here is already the contract it enforces.
func RegisterWrites(reg *registry.Registry, svc *Service) error {
	if svc == nil {
		return errs.New(errs.CodeInternal, "Magento service is required")
	}
	return registerWriteTools(reg, singleLookup(svc))
}

// RegisterWritesWithResolver registers the write tools with per-call store
// routing. Confirmation metadata is unchanged: every write still requires
// two-step human approval.
func RegisterWritesWithResolver(reg *registry.Registry, resolver *Resolver) error {
	if resolver == nil {
		return errs.New(errs.CodeInternal, "Magento resolver is required")
	}
	return registerWriteTools(reg, resolver.lookup())
}

// registerWriteTools builds the write tools against an instance lookup.
func registerWriteTools(reg *registry.Registry, lookup serviceLookup) error {
	if reg == nil {
		return errs.New(errs.CodeInternal, "tool registry is required")
	}
	tools := []registry.Tool{
		newProductCreateTool(lookup), newProductUpdateTool(lookup), newConfigurableCreateTool(lookup),
		newConfigurableAddChildTool(lookup), newInventoryAssignTool(lookup), newImportExecuteTool(lookup),
		newProductDeleteTool(lookup), newRemoveChildTool(lookup), newUnassignSourceItemsTool(lookup),
		newMediaUploadTool(lookup), newInvoiceCreateTool(lookup),
		newShipmentCreateTool(lookup), newCreditmemoCreateTool(lookup),
		newOrderCommentTool(lookup), newOrderHoldTool(lookup, true), newOrderHoldTool(lookup, false),
		newCategoryCreateTool(lookup), newCategoryUpdateTool(lookup), newCategoryAssignProductTool(lookup),
		newProductLinkAddTool(lookup), newProductLinkRemoveTool(lookup),
		newInvoiceCaptureTool(lookup), newOrderEmailTool(lookup), newMediaDeleteTool(lookup),
		newOrderCreateTool(lookup), newTierPricesSetTool(lookup), newInventoryTransferTool(lookup),
		newCustomerCreateTool(lookup), newCustomerUpdateTool(lookup),
		newMediaUpdateTool(lookup), newOrderCancelTool(lookup), newAttributeOptionCreateTool(lookup),
		newAttributeOptionDeleteTool(lookup), newBulkProductUpdateTool(lookup), newCategoryDeleteTool(lookup),
		newCustomerAddressCreateTool(lookup), newCustomerAddressDeleteTool(lookup),
		newCustomerAddressUpdateTool(lookup), newBulkLinkTool(lookup), newBulkOptionCreateTool(lookup),
		newConfigurableUpdateTool(lookup), newInventorySourceCreateTool(lookup), newStockSourceLinkTool(lookup),
		newStockSourceUnlinkTool(lookup), newCreateVariationsTool(lookup), newSetConfigurableOptionsTool(lookup),
		newProductOptionCreateTool(lookup), newProductOptionUpdateTool(lookup), newProductOptionDeleteTool(lookup),
	}
	tools = append(tools, newDocumentEmailTools(lookup)...)
	for _, tool := range tools {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	// Validate arguments before a human is asked to approve them: a malformed
	// write must fail fast instead of consuming an approval.
	for name, schema := range writeToolSchemas() {
		if err := reg.SetInputValidator(name, schema); err != nil {
			return err
		}
	}
	return nil
}

// writeToolSchemas maps each write tool to its JSON Schema.
func writeToolSchemas() map[string]string {
	return map[string]string{
		productCreateToolName:          productCreateSchema,
		productUpdateToolName:          productUpdateSchema,
		productDeleteToolName:          productDeleteSchema,
		configurableCreateToolName:     configurableCreateSchema,
		configurableAddChildToolName:   configurableAddChildSchema,
		inventoryAssignToolName:        inventoryAssignSchema,
		importExecuteToolName:          importExecuteSchema,
		mediaUploadToolName:            mediaUploadSchema,
		mediaDeleteToolName:            mediaDeleteSchema,
		invoiceCreateToolName:          invoiceCreateSchema,
		invoiceCaptureToolName:         invoiceCaptureSchema,
		shipmentCreateToolName:         shipmentCreateSchema,
		creditmemoCreateToolName:       creditmemoCreateSchema,
		orderCommentToolName:           orderCommentSchema,
		orderHoldToolName:              orderHoldSchema,
		orderUnholdToolName:            orderHoldSchema,
		orderEmailToolName:             orderEmailSchema,
		orderCreateToolName:            orderCreateSchema,
		categoryCreateToolName:         categoryCreateSchema,
		categoryUpdateToolName:         categoryUpdateSchema,
		categoryAssignToolName:         categoryAssignSchema,
		removeChildToolName:            removeChildSchema,
		unassignSourceItemsToolName:    unassignSourceItemsSchema,
		productLinkAddToolName:         productLinkAddSchema,
		productLinkRemoveToolName:      productLinkRemoveSchema,
		tierPricesSetToolName:          tierPricesSetSchema,
		inventoryTransferToolName:      inventoryTransferSchema,
		customerCreateToolName:         customerCreateSchema,
		customerUpdateToolName:         customerUpdateSchema,
		mediaUpdateToolName:            mediaUpdateSchema,
		orderCancelToolName:            orderCancelSchema,
		attributeOptionCreateToolName:  attributeOptionCreateSchema,
		attributeOptionDeleteToolName:  attributeOptionDeleteSchema,
		bulkProductUpdateToolName:      bulkProductUpdateSchema,
		categoryDeleteToolName:         categoryDeleteSchema,
		customerAddressCreateToolName:  customerAddressCreateSchema,
		customerAddressDeleteToolName:  customerAddressDeleteSchema,
		customerAddressUpdateToolName:  customerAddressUpdateSchema,
		bulkLinkToolName:               bulkLinkSchema,
		bulkOptionCreateToolName:       bulkOptionCreateSchema,
		configurableUpdateToolName:     productUpdateSchema,
		invoiceEmailToolName:           documentEmailSchema,
		shipmentEmailToolName:          documentEmailSchema,
		creditmemoEmailToolName:        documentEmailSchema,
		inventorySourceCreateToolName:  inventorySourceCreateSchema,
		stockSourceLinkToolName:        stockSourceLinkSchema,
		stockSourceUnlinkToolName:      stockSourceUnlinkSchema,
		createVariationsToolName:       createVariationsSchema,
		setConfigurableOptionsToolName: setConfigurableOptionsSchema,
		productOptionCreateToolName:    productOptionCreateSchema,
		productOptionUpdateToolName:    productOptionUpdateSchema,
		productOptionDeleteToolName:    productOptionDeleteSchema,
	}
}

// writeDisclosure is the machine-checked side-effect/idempotency statement
// appended to every write tool description. Keeping it in one table (rather
// than hand-editing 55 descriptions) is what makes the rule enforceable:
// TestWriteToolDescriptionsDiscloseSideEffects enumerates the registry and
// fails when a write tool is missing an entry or the rendered text omits
// "Mutates:", an idempotency statement, or confirmation.
type writeDisclosure struct {
	mutates     string
	idempotency string
}

// idempotent reports whether the disclosure's idempotency statement declares a
// repeat of the same call harmless. The prose is the single source of truth: the
// protocol-level idempotentHint is derived from it (writeIdempotentHint), so the
// description an agent reads and the annotation a client acts on cannot disagree.
//
// The classification is deliberately conservative:
//   - "idempotent ..." (including "idempotent per SKU") is idempotent;
//   - "idempotent when idempotency_key is supplied ..." is NOT: the annotation is
//     a static bool, the same call without a key can duplicate, and a client that
//     reads true would retry;
//   - "resumable: ..." and "not idempotent ..." are not idempotent.
func (d writeDisclosure) idempotent() bool {
	statement := strings.ToLower(strings.TrimSpace(d.idempotency))
	if !strings.HasPrefix(statement, "idempotent") {
		return false
	}
	return !strings.Contains(statement, "when idempotency_key")
}

// writeIdempotentHint derives the protocol annotation for one write tool. A tool
// with no disclosure entry is non-idempotent: the contract test fails on the
// missing entry, and false is the safe default until it is added.
func writeIdempotentHint(name string) bool {
	disclosure, ok := writeDisclosures[name]
	if !ok {
		return false
	}
	return disclosure.idempotent()
}

var writeDisclosures = map[string]writeDisclosure{
	"magento.attributes.options.bulk_create": {mutates: "the EAV attribute option set for one attribute code", idempotency: "not fully idempotent; duplicate labels are skipped, but earlier options are not rolled back"},
	"magento.attributes.options.create":      {mutates: "the EAV attribute option set for one attribute code", idempotency: "not idempotent; a retry can create a duplicate option value"},
	"magento.attributes.options.delete":      {mutates: "the EAV attribute option set for one attribute code", idempotency: "not idempotent; a retry on a deleted option returns not_found"},
	"magento.category.assign_product":        {mutates: "the product-to-category assignment", idempotency: "idempotent; assigning the same product twice leaves the assignment in place"},
	"magento.category.create":                {mutates: "the Magento category tree", idempotency: "not idempotent; a retry can create a duplicate category or URL key conflict"},
	"magento.category.delete":                {mutates: "the category subtree and its product assignments", idempotency: "not idempotent; a retry on a deleted category returns not_found"},
	"magento.category.update":                {mutates: "the category record", idempotency: "idempotent; repeating the same field values leaves the category unchanged"},
	"magento.configurable.add_child":         {mutates: "the configurable parent's child links", idempotency: "not idempotent; Magento rejects a duplicate link"},
	"magento.configurable.create":            {mutates: "the configurable parent, its simple children, and their links", idempotency: "resumable: pass the operation_id from a partial run to reuse existing children and repair missing links without duplicating a SKU"},
	"magento.configurable.create_variations": {mutates: "the configurable parent's simple children and links", idempotency: "idempotent per SKU; existing SKUs are skipped"},
	"magento.configurable.remove_child":      {mutates: "the configurable parent's child links", idempotency: "not idempotent; a retry on an unlinked child returns not_found"},
	"magento.configurable.set_options":       {mutates: "the configurable parent's variant options", idempotency: "not idempotent; options are appended and earlier options are not rolled back"},
	"magento.configurable.update":            {mutates: "the configurable product record", idempotency: "idempotent; repeating the same field values leaves the product unchanged"},
	"magento.creditmemo.create":              {mutates: "the order's credit memo/refund record", idempotency: "idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded credit memo, and an ambiguous outcome is recorded as unknown and never retried"},
	"magento.creditmemo.email":               {mutates: "the credit memo email state", idempotency: "not idempotent; a retry can send a duplicate email"},
	"magento.customer.addresses.create":      {mutates: "the customer's address list", idempotency: "not idempotent; a retry can create a duplicate address"},
	"magento.customer.addresses.delete":      {mutates: "the customer's address list", idempotency: "not idempotent; a retry on a deleted address returns not_found"},
	"magento.customer.addresses.update":      {mutates: "the customer address record", idempotency: "idempotent; repeating the same field values leaves the address unchanged"},
	"magento.customer.create":                {mutates: "the Magento customer record", idempotency: "not idempotent; a retry can create a duplicate customer or email conflict"},
	"magento.customer.update":                {mutates: "the Magento customer record", idempotency: "idempotent; repeating the same field values leaves the customer unchanged"},
	"magento.import.cancel":                  {mutates: "the asynchronous import job state", idempotency: "not idempotent; a retry after the job reaches a terminal state returns illegal_transition"},
	"magento.import.execute":                 {mutates: "the product catalog through a bounded import", idempotency: "not idempotent; a retry can re-apply rows and earlier writes are not rolled back"},
	"magento.import.resume":                  {mutates: "the asynchronous import queue and the source job's failed rows", idempotency: "idempotent per source job; repeated resumes return the same new job"},
	"magento.import.start":                   {mutates: "the asynchronous import queue", idempotency: "not idempotent unless idempotency_key is supplied; a repeated key returns the original job id"},
	"magento.inventory.assign":               {mutates: "MSI source quantities and assignments", idempotency: "not idempotent as an agent action; quantity replaces the existing value but the batch response has no per-item outcome"},
	"magento.inventory.source_create":        {mutates: "the MSI source inventory", idempotency: "not idempotent; a retry can create a duplicate source or conflict"},
	"magento.inventory.stock_source_link":    {mutates: "the MSI stock-to-source links", idempotency: "not idempotent; a retry may be rejected as a duplicate link"},
	"magento.inventory.stock_source_unlink":  {mutates: "the MSI stock-to-source links", idempotency: "not idempotent; a retry on an unlinked source returns not_found"},
	"magento.inventory.transfer":             {mutates: "MSI source quantities on two sources", idempotency: "not idempotent; the read-modify-write is not transactional and a retry can move quantity twice"},
	"magento.inventory.unassign":             {mutates: "MSI source assignments", idempotency: "not idempotent; a retry may report a different unassigned count"},
	"magento.invoice.capture":                {mutates: "the invoice payment state and the customer's payment method", idempotency: "idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded capture, and an ambiguous outcome is recorded as unknown and never retried"},
	"magento.invoice.create":                 {mutates: "the order's invoice record", idempotency: "idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded invoice, and an ambiguous outcome is recorded as unknown and never retried"},
	"magento.invoice.email":                  {mutates: "the invoice email state", idempotency: "not idempotent; a retry can send a duplicate email"},
	"magento.order.cancel":                   {mutates: "the sales order state", idempotency: "idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded cancellation, and an ambiguous outcome is reconciled against the order state"},
	"magento.order.comment":                  {mutates: "the order status history", idempotency: "not idempotent; a retry appends a duplicate comment"},
	"magento.order.create":                   {mutates: "the sales order and the customer's payment method", idempotency: "idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded order, and an ambiguous placement is reconciled before it is recorded"},
	"magento.order.email":                    {mutates: "the order email state", idempotency: "not idempotent; a retry can send a duplicate email"},
	"magento.order.hold":                     {mutates: "the sales order state", idempotency: "not idempotent; a retry on a held order is rejected"},
	"magento.order.unhold":                   {mutates: "the sales order state", idempotency: "not idempotent; a retry on a released order is rejected"},
	"magento.product.create":                 {mutates: "the Magento product catalog and, when quantity is supplied, default-source inventory", idempotency: "not idempotent; a retry can create a duplicate SKU"},
	"magento.product.delete":                 {mutates: "the product record and its links and reviews", idempotency: "not idempotent; a retry on a deleted SKU returns not_found"},
	"magento.product.links.add":              {mutates: "product relations", idempotency: "not idempotent; Magento rejects a duplicate link"},
	"magento.product.links.bulk":             {mutates: "product relations for up to 50 targets", idempotency: "not idempotent; partial success means a retry can duplicate already-applied links"},
	"magento.product.links.remove":           {mutates: "product relations", idempotency: "not idempotent; a retry on a removed link returns not_found"},
	"magento.product.media.delete":           {mutates: "the product media gallery", idempotency: "not idempotent; a retry on a deleted media id returns not_found"},
	"magento.product.media.update":           {mutates: "the product media gallery entry", idempotency: "idempotent; repeating the same fields leaves the entry unchanged"},
	"magento.product.media.upload":           {mutates: "the product media gallery and the stored file", idempotency: "not idempotent; a retry can add a duplicate image"},
	"magento.product.options.create":         {mutates: "the product custom-option set", idempotency: "not idempotent; a retry can create a duplicate option"},
	"magento.product.options.delete":         {mutates: "the product custom-option set", idempotency: "not idempotent; a retry on a deleted option returns not_found"},
	"magento.product.options.update":         {mutates: "the product custom-option set", idempotency: "idempotent; repeating the same fields leaves the option unchanged"},
	"magento.product.tier_prices.set":        {mutates: "the product tier-price set", idempotency: "idempotent; the call replaces the SKU's tier set, so repeating the same payload leaves the same tiers"},
	"magento.product.update":                 {mutates: "the product record and its EAV attributes", idempotency: "idempotent for identical field values; a read-modify-write retry can overwrite a concurrent change"},
	"magento.products.bulk_update":           {mutates: "up to 50 product records", idempotency: "not idempotent; partial success means a retry re-applies already-updated SKUs"},
	"magento.shipment.create":                {mutates: "the order's shipment record and tracking numbers", idempotency: "not idempotent; a retry can create a duplicate shipment"},
	"magento.shipment.email":                 {mutates: "the shipment email state", idempotency: "not idempotent; a retry can send a duplicate email"},
}

// discloseWrite appends the standard write contract to a description. The
// suffix is derived from writeDisclosures, so a new write tool without an
// entry fails the contract test rather than shipping an undocumented write.
func discloseWrite(name, description string) string {
	disclosure, ok := writeDisclosures[name]
	if !ok {
		return description
	}
	lower := strings.ToLower(description)
	if !strings.Contains(lower, "write operation") {
		description = "Magento write operation: " + description
	}
	suffix := " Mutates: " + disclosure.mutates + "."
	if !strings.Contains(lower, "idempotent") {
		suffix += " Idempotency: " + disclosure.idempotency + "."
	}
	if !strings.Contains(lower, "confirmation") {
		suffix += " Requires human confirmation."
	}
	return description + suffix
}

func writeToolMetadata(name, title, description, capability string) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: discloseWrite(name, description),
		Capability: capability, Permission: registry.PermissionWrite, Timeout: writeToolTimeout,
		RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(name), DestructiveHint: destructiveHint(),
		},
	}
}

func mcpWriteTool(name, title, description, schema string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: discloseWrite(name, description),
		InputSchema: json.RawMessage(schema),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(name), DestructiveHint: destructiveHint(),
		},
	}
}

func destructiveHint() *bool {
	value := false
	return &value
}

func newProductCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productCreateToolName, productCreateToolTitle, productCreateToolDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productCreateToolName, productCreateToolTitle, productCreateToolDescription, productCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productCreateInput) (*mcp.CallToolResult, productWriteOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				product, err := svc.Create(ctx, in.toCreateInput())
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				return nil, productWriteOutput{Product: *product}, nil
			})
		return nil
	}
	return t
}

func (in productUpdateInput) toServiceInput() UpdateProductInput {
	return UpdateProductInput{
		Name: in.Name, Price: in.Price, Status: in.Status, Visibility: in.Visibility, Attributes: in.Attributes,
		Description: in.Description, ShortDescription: in.ShortDescription, URLKey: in.URLKey,
		MetaTitle: in.MetaTitle, MetaKeywords: in.MetaKeywords, MetaDescription: in.MetaDescription,
		CategoryIDs: in.CategoryIDs, WebsiteIDs: in.WebsiteIDs,
		Weight: in.Weight, SpecialPrice: in.SpecialPrice, SpecialFromDate: in.SpecialFromDate,
		SpecialToDate: in.SpecialToDate, Cost: in.Cost, TaxClassID: in.TaxClassID,
	}
}

func newProductUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productUpdateToolName, productUpdateToolTitle, productUpdateToolDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productUpdateToolName, productUpdateToolTitle, productUpdateToolDescription, productUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productUpdateInput) (*mcp.CallToolResult, productWriteOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				product, err := svc.Update(ctx, in.SKU, in.toServiceInput())
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				return nil, productWriteOutput{Product: *product}, nil
			})
		return nil
	}
	return t
}

func newConfigurableCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(configurableCreateToolName, configurableCreateToolTitle, configurableCreateToolDescription, capabilityConfigurableWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(configurableCreateToolName, configurableCreateToolTitle, configurableCreateToolDescription, configurableCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in configurableCreateInput) (*mcp.CallToolResult, configurableCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, configurableCreateOutput{}, err
				}
				result, err := svc.CreateConfigurable(ctx, in.toServiceInput())
				if err != nil {
					if result != nil && result.Outcome == ConfigurablePartial {
						// A partial run is a tool error, but the execution record is
						// the actionable part: return it alongside the uniform error
						// envelope so the agent sees the failed stage and the orphans.
						out := configurableOutputFrom(result)
						envelope := toolerr.Envelope(err)
						envelope["result"] = out
						if raw, marshalErr := json.Marshal(envelope); marshalErr == nil {
							return &mcp.CallToolResult{
								IsError:           true,
								StructuredContent: json.RawMessage(raw),
								Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
							}, out, nil
						}
					}
					return nil, configurableCreateOutput{}, err
				}
				return nil, configurableOutputFrom(result), nil
			})
		return nil
	}
	return t
}

// configurableOutputFrom forwards the service execution record to the MCP
// payload, so an agent can tell a completed create from a partial one and act on
// the failed stage and the orphans without another read.
func configurableOutputFrom(result *ConfigurableProduct) configurableCreateOutput {
	if result == nil {
		return configurableCreateOutput{}
	}
	return configurableCreateOutput{
		Parent: result.Parent, Children: result.Children,
		ParentInStock: result.ParentInStock, ParentStockSource: result.ParentStockSource,
		OperationID: result.OperationID, Outcome: result.Outcome, Stages: result.Stages,
		CompletedStages: result.CompletedStages, FailedStage: result.FailedStage,
		OrphanSKUs: result.OrphanSKUs,
	}
}

func (in configurableCreateInput) toServiceInput() CreateConfigurableInput {
	variants := make([]VariantAttribute, 0, len(in.VariantAttributes))
	for _, variant := range in.VariantAttributes {
		values := make([]VariantValue, 0, len(variant.Values))
		for _, value := range variant.Values {
			values = append(values, VariantValue(value))
		}
		variants = append(variants, VariantAttribute{AttributeID: variant.AttributeID, Code: variant.Code, Values: values})
	}
	children := make([]ChildProduct, 0, len(in.Children))
	for _, child := range in.Children {
		children = append(children, ChildProduct(child))
	}
	return CreateConfigurableInput{
		Parent: in.Parent.toCreateInput(), Variants: variants, Children: children,
		OperationID: in.OperationID,
	}
}

func newConfigurableAddChildTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(configurableAddChildToolName, configurableAddChildToolTitle, configurableAddChildToolDescription, capabilityConfigurableWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(configurableAddChildToolName, configurableAddChildToolTitle, configurableAddChildToolDescription, configurableAddChildSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in configurableAddChildInput) (*mcp.CallToolResult, configurableAddChildOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, configurableAddChildOutput{}, err
				}
				if err := svc.AddChild(ctx, in.ParentSKU, in.ChildSKU); err != nil {
					return nil, configurableAddChildOutput{}, err
				}
				return nil, configurableAddChildOutput{ParentSKU: in.ParentSKU, ChildSKU: in.ChildSKU, Linked: true}, nil
			})
		return nil
	}
	return t
}

func newInventoryAssignTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(inventoryAssignToolName, inventoryAssignToolTitle, inventoryAssignToolDescription, capabilityInventoryWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(inventoryAssignToolName, inventoryAssignToolTitle, inventoryAssignToolDescription, inventoryAssignSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in inventoryAssignInput) (*mcp.CallToolResult, inventoryAssignOutput, error) {
				items := make([]SourceItem, 0, len(in.Items))
				for _, item := range in.Items {
					status := 1
					if item.Status != nil {
						status = *item.Status
					}
					items = append(items, SourceItem{
						SKU: item.SKU, SourceCode: item.SourceCode, Quantity: item.Quantity, Status: status,
					})
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, inventoryAssignOutput{}, err
				}
				if err := svc.AssignSourceItems(ctx, items); err != nil {
					return nil, inventoryAssignOutput{}, err
				}
				return nil, inventoryAssignOutput{Assigned: len(items)}, nil
			})
		return nil
	}
	return t
}

const (
	importExecuteToolName        = "magento.import.execute"
	importExecuteToolTitle       = "Execute a validated product import"
	importExecuteToolDescription = "Magento write operation: executes a bounded CSV or JSON product import (base64, at most 8 MiB and 5000 rows) for the resolved instance. Existing SKUs are updated only when update_existing is true; failures are reported per row and earlier writes are not rolled back. Requires human confirmation."
	importExecuteCapability      = "magento.import.write"
)

type importExecuteInput struct {
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Format         string `json:"format" jsonschema:"Payload encoding: csv or json."`
	Data           string `json:"data_base64"`
	UpdateExisting bool   `json:"update_existing"`
	ConfirmationID string `json:"confirmation_id,omitempty"`
}

type importExecuteOutput struct {
	Result *ImportResult `json:"result" jsonschema:"The operation result."`
}

const importExecuteSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["instance", "format", "data_base64"],
  "properties": {
    "instance": {"type": "string", "pattern": "^[a-z0-9][a-z0-9-]{0,63}$"},
    "format": {"type": "string", "enum": ["csv", "json"]},
    "data_base64": {"type": "string", "maxLength": 11184812},
    "update_existing": {"type": "boolean", "default": false},
    "confirmation_id": {"type": "string", "maxLength": 128}
  }
}`

func newImportExecuteTool(lookup serviceLookup) registry.Tool {
	tool := writeToolMetadata(importExecuteToolName, importExecuteToolTitle, importExecuteToolDescription, importExecuteCapability)
	tool.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(importExecuteToolName, importExecuteToolTitle, importExecuteToolDescription, importExecuteSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in importExecuteInput) (*mcp.CallToolResult, importExecuteOutput, error) {
				data, err := DecodeImportPayload(in.Data)
				if err != nil {
					return nil, importExecuteOutput{}, err
				}
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, importExecuteOutput{}, err
				}
				result, err := svc.ExecuteImport(ctx, in.Format, data, in.UpdateExisting)
				if err != nil {
					return nil, importExecuteOutput{}, err
				}
				return nil, importExecuteOutput{Result: result}, nil
			})
		return nil
	}
	return tool
}

const (
	productDeleteToolName    = "magento.product.delete"
	productDeleteToolTitle   = "Delete Magento product"
	productDeleteDescription = "Magento write operation: deletes one product by SKU " +
		"(DELETE /rest/{store_code}/V1/products/{sku}) on the resolved instance. " +
		"Destructive and irreversible: configurable children, links, and reviews are removed with it, " +
		"and Magento returns 404 for an unknown SKU so a repeated delete is not a no-op. Requires human confirmation."

	productDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `},
		"required": ["instance", "sku"], "additionalProperties": false}`
)

type productDeleteInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string `json:"sku" jsonschema:"Product SKU."`
}

type productDeleteOutput struct {
	SKU     string `json:"sku" jsonschema:"Product SKU."`
	Deleted bool   `json:"deleted" jsonschema:"True when the entry was removed."`
}

func destructiveToolMetadata(name, title, description, capability string) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: discloseWrite(name, description),
		Capability: capability, Permission: registry.PermissionDestructive, Timeout: writeToolTimeout,
		RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(name), DestructiveHint: destructiveTrue(),
		},
	}
}

func destructiveTrue() *bool {
	value := true
	return &value
}

func newProductDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(productDeleteToolName, productDeleteToolTitle, productDeleteDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: productDeleteToolName, Title: productDeleteToolTitle, Description: discloseWrite(productDeleteToolName, productDeleteDescription),
			InputSchema: json.RawMessage(productDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(productDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in productDeleteInput) (*mcp.CallToolResult, productDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, productDeleteOutput{}, err
			}
			if err := svc.DeleteProduct(ctx, in.SKU); err != nil {
				return nil, productDeleteOutput{}, err
			}
			return nil, productDeleteOutput{SKU: in.SKU, Deleted: true}, nil
		})
		return nil
	}
	return t
}

const (
	removeChildToolName    = "magento.configurable.remove_child"
	removeChildToolTitle   = "Unlink a configurable child"
	removeChildDescription = "Magento write operation: unlinks a child product from a configurable product " +
		"(DELETE /rest/{store_code}/V1/configurable-products/{parentSku}/children/{childSku}) on the resolved instance. " +
		"The child product itself is not deleted. Requires human confirmation."
	unassignSourceItemsToolName    = "magento.inventory.unassign"
	unassignSourceItemsToolTitle   = "Remove MSI source assignments"
	unassignSourceItemsDescription = "Magento write operation: removes per-source inventory assignments " +
		"(POST /rest/{store_code}/V1/inventory/source-items-delete) on the resolved instance for at most 100 sku/source pairs. " +
		"The product and its catalog stock status are unchanged; the assignment is removed. Requires human confirmation."

	removeChildSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the configurable parent product."},
		"child_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the variant child to link or unlink."}},
		"required": ["instance", "parent_sku", "child_sku"], "additionalProperties": false}`

	unassignSourceItemsSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"items": {"type": "array", "minItems": 1, "maxItems": 100, "description": "Lines to act on. Each entry names an order item and a quantity, and the quantities must not exceed what is still outstanding.", "items": {"type": "object",
			"additionalProperties": false, "required": ["sku", "source_code"],
			"properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU."},
				"source_code": {"type": "string", "pattern": "` + sourceCodeInputPattern + `", "description": "Inventory source code, for example default. Use magento.inventory.sources to list them."}}}}},
		"required": ["instance", "items"], "additionalProperties": false}`
)

type removeChildInput struct {
	Instance  string `json:"instance" jsonschema:"Configured instance id the result came from."`
	ParentSKU string `json:"parent_sku" jsonschema:"SKU of the configurable parent product."`
	ChildSKU  string `json:"child_sku" jsonschema:"SKU of the variant child product."`
}

type removeChildOutput struct {
	ParentSKU string `json:"parent_sku" jsonschema:"SKU of the configurable parent product."`
	ChildSKU  string `json:"child_sku" jsonschema:"SKU of the variant child product."`
	Unlinked  bool   `json:"unlinked" jsonschema:"True when the child was removed from the parent."`
}

type unassignSourceItemsInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Items    []struct {
		SKU        string `json:"sku" jsonschema:"Product SKU."`
		SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	} `json:"items" jsonschema:"Entries returned by this call."`
}

type unassignSourceItemsOutput struct {
	Unassigned int `json:"unassigned" jsonschema:"True when the source items were removed."`
}

func newRemoveChildTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(removeChildToolName, removeChildToolTitle, removeChildDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(removeChildToolName, removeChildToolTitle, removeChildDescription, removeChildSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in removeChildInput) (*mcp.CallToolResult, removeChildOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, removeChildOutput{}, err
				}
				if err := svc.RemoveChild(ctx, in.ParentSKU, in.ChildSKU); err != nil {
					return nil, removeChildOutput{}, err
				}
				return nil, removeChildOutput{ParentSKU: in.ParentSKU, ChildSKU: in.ChildSKU, Unlinked: true}, nil
			})
		return nil
	}
	return t
}

func newUnassignSourceItemsTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(unassignSourceItemsToolName, unassignSourceItemsToolTitle, unassignSourceItemsDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(unassignSourceItemsToolName, unassignSourceItemsToolTitle, unassignSourceItemsDescription, unassignSourceItemsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in unassignSourceItemsInput) (*mcp.CallToolResult, unassignSourceItemsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, unassignSourceItemsOutput{}, err
				}
				refs := make([]SourceItemRef, 0, len(in.Items))
				for _, item := range in.Items {
					refs = append(refs, SourceItemRef{SKU: item.SKU, SourceCode: item.SourceCode})
				}
				if err := svc.UnassignSourceItems(ctx, refs); err != nil {
					return nil, unassignSourceItemsOutput{}, err
				}
				return nil, unassignSourceItemsOutput{Unassigned: len(refs)}, nil
			})
		return nil
	}
	return t
}

const (
	mediaUploadToolName    = "magento.product.media.upload"
	mediaUploadToolTitle   = "Upload product media"
	mediaUploadDescription = "Magento write operation: adds an image to a product gallery " +
		"(POST /rest/{store_code}/V1/products/{sku}/media) on the resolved instance. " +
		"The payload is base64 and capped at 5 MiB decoded; mime_type, filename, and media types are validated. Requires human confirmation."

	mediaUploadSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"filename": {"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$", "description": "Safe file name, e.g. front.jpg."},
		"mime_type": {"type": "string", "enum": ["image/jpeg", "image/png", "image/webp", "image/gif"], "description": "Declared MIME type. It must match the type sniffed from the decoded bytes or the upload is rejected."},
		"data_base64": {"type": "string", "maxLength": 6990508, "description": "Base64 image content, at most 5 MiB decoded."},
		"label": {"type": "string", "maxLength": 255, "description": "Label shown to the shopper."},
		"position": {"type": "integer", "minimum": 0, "maximum": 1000},
		"types": {"type": "array", "items": {"type": "string", "enum": ["image", "small_image", "thumbnail", "swatch_image"]}, "maxItems": 4}
	}, "required": ["instance", "sku", "filename", "mime_type", "data_base64"], "additionalProperties": false}`
)

type mediaUploadInput struct {
	Instance   string   `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU        string   `json:"sku" jsonschema:"Product SKU."`
	Filename   string   `json:"filename"`
	MIMEType   string   `json:"mime_type"`
	DataBase64 string   `json:"data_base64"`
	Label      string   `json:"label" jsonschema:"Label shown to the shopper."`
	Position   int      `json:"position" jsonschema:"Sort position among siblings."`
	Types      []string `json:"types" jsonschema:"Media roles available for the attribute set."`
}

type mediaUploadOutput struct {
	Media MediaEntry `json:"media" jsonschema:"Gallery entry that was written."`
}

func newMediaUploadTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(mediaUploadToolName, mediaUploadToolTitle, mediaUploadDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(mediaUploadToolName, mediaUploadToolTitle, mediaUploadDescription, mediaUploadSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in mediaUploadInput) (*mcp.CallToolResult, mediaUploadOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, mediaUploadOutput{}, err
				}
				media, err := svc.UploadMedia(ctx, in.SKU, MediaUploadInput{
					Label: in.Label, Position: in.Position, Types: in.Types,
					Filename: in.Filename, MIMEType: in.MIMEType, DataBase64: in.DataBase64,
				})
				if err != nil {
					return nil, mediaUploadOutput{}, err
				}
				return nil, mediaUploadOutput{Media: *media}, nil
			})
		return nil
	}
	return t
}

const (
	invoiceCreateToolName    = "magento.invoice.create"
	invoiceCreateToolTitle   = "Create Magento invoice"
	invoiceCreateDescription = "Magento write operation: invoices an order through the workflow route " +
		"(POST /rest/{store_code}/V1/order/{orderId}/invoice) on the resolved instance, which is the route that honours capture. " +
		"capture=false records an open invoice and charges nothing; capture=true captures the payment and the returned " +
		"invoice state proves whether it did. Idempotent when idempotency_key is supplied: a repeat with the same arguments " +
		"replays the recorded invoice. An ambiguous outcome is recorded as unknown and must be polled, never retried. " +
		"Requires human confirmation."

	invoiceCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + idempotencyKeyProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id."},
		"capture": {"type": "boolean", "default": false, "description": "Capture the payment; false only records the invoice."},
		"items": {"type": "array", "minItems": 1, "maxItems": 100, "description": "Lines to act on. Each entry names an order item and a quantity, and the quantities must not exceed what is still outstanding.", "items": {"type": "object",
			"additionalProperties": false, "required": ["order_item_id", "qty"],
			"properties": {"order_item_id": {"type": "integer", "minimum": 1},
				"qty": {"type": "number", "exclusiveMinimum": 0}}}}},
		"required": ["instance", "order_id", "items"], "additionalProperties": false}`
)

type invoiceCreateInput struct {
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID        int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Capture        bool   `json:"capture"`
	IdempotencyKey string `json:"idempotency_key"`
	Items          []struct {
		OrderItemID int     `json:"order_item_id"`
		Qty         float64 `json:"qty" jsonschema:"Quantity at this source."`
	} `json:"items" jsonschema:"Entries returned by this call."`
}

type invoiceCreateOutput struct {
	Invoice     Invoice `json:"invoice" jsonschema:"The created invoice. Its entity_id is what capture and credit memo take."`
	Captured    bool    `json:"captured" jsonschema:"True when the invoice was captured at creation. False means it exists but no money has been taken."`
	OperationID string  `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool    `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so no second invoice was created."`
}

func newInvoiceCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(invoiceCreateToolName, invoiceCreateToolTitle, invoiceCreateDescription, "magento.invoice.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(invoiceCreateToolName, invoiceCreateToolTitle, invoiceCreateDescription, invoiceCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in invoiceCreateInput) (*mcp.CallToolResult, invoiceCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceCreateOutput{}, err
				}
				items := make([]InvoiceLine, 0, len(in.Items))
				for _, item := range in.Items {
					items = append(items, InvoiceLine{OrderItemID: item.OrderItemID, Qty: item.Qty})
				}
				result, err := svc.CreateInvoice(ctx, InvoiceCreateInput{
					OrderID: in.OrderID, Capture: in.Capture, Items: items,
					IdempotencyKey: in.IdempotencyKey, Instance: in.Instance,
				})
				if err != nil {
					return nil, invoiceCreateOutput{}, err
				}
				return nil, invoiceCreateOutput{
					Invoice: result.Invoice, Captured: result.Captured,
					OperationID: result.OperationID, Replayed: result.Replayed,
				}, nil
			})
		return nil
	}
	return t
}

const (
	shipmentCreateToolName    = "magento.shipment.create"
	shipmentCreateToolTitle   = "Create Magento shipment"
	shipmentCreateDescription = "Magento write operation: creates a shipment for an order " +
		"(POST /rest/{store_code}/V1/order/{orderId}/ship) on the resolved instance, with 1-100 order-item lines and up to 20 tracking numbers. " +
		"Requires human confirmation."
	creditmemoCreateToolName    = "magento.creditmemo.create"
	creditmemoCreateToolTitle   = "Create Magento credit memo"
	creditmemoCreateDescription = "Magento write operation: creates a credit memo (POST /rest/{store_code}/V1/order/{orderId}/refund) with 1-100 order-item lines and " +
		"optional adjustments on the resolved instance. The order and its invoices are read first: the memo is linked to an invoice and a quantity above " +
		"qty_invoiced - qty_refunded is rejected before any refund request. Idempotent when idempotency_key is supplied: a repeat " +
		"with the same arguments replays the recorded credit memo. An ambiguous outcome is recorded as unknown and must be polled, " +
		"never retried. Requires human confirmation."

	documentLinesProp = `"items": {"type": "array", "minItems": 1, "maxItems": 100, "description": "Lines to act on. Each entry names an order item and a quantity, and the quantities must not exceed what is still outstanding.", "items": {"type": "object",
		"additionalProperties": false, "required": ["order_item_id", "qty"],
		"properties": {"order_item_id": {"type": "integer", "minimum": 1},
			"qty": {"type": "number", "exclusiveMinimum": 0}}}}`

	shipmentCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id. Use magento.order.search to find it."},
		` + documentLinesProp + `,
		"tracks": {"type": "array", "maxItems": 20, "items": {"type": "object", "additionalProperties": false,
			"required": ["track_number", "carrier_code"],
			"properties": {"track_number": {"type": "string", "maxLength": 128},
				"title": {"type": "string", "maxLength": 255, "description": "Display title for this entry."},
				"carrier_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"}}}},
		"comment": {"type": "string", "maxLength": 1024},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "items"], "additionalProperties": false}`

	creditmemoCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + idempotencyKeyProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id."},
		"invoice_id": {"type": "integer", "minimum": 1, "description": "Invoice to refund. Required when the order has several invoices; resolved automatically when it has exactly one."},
		` + documentLinesProp + `,
		"adjustment_positive": {"type": "number", "minimum": 0},
		"adjustment_negative": {"type": "number", "minimum": 0},
		"comment": {"type": "string", "maxLength": 1024},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "items"], "additionalProperties": false}`
)

type shipmentCreateInput struct {
	Instance string               `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID  int                  `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Items    []InvoiceLine        `json:"items" jsonschema:"Entries returned by this call."`
	Tracks   []ShipmentTrackInput `json:"tracks" jsonschema:"Tracking numbers attached to the shipment."`
	Comment  string               `json:"comment"`
	Notify   bool                 `json:"notify"`
}

type shipmentCreateOutput struct {
	Shipment Shipment `json:"shipment" jsonschema:"The shipment that was created or read."`
}

type creditmemoCreateInput struct {
	Instance           string        `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID            int           `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	InvoiceID          int           `json:"invoice_id"`
	IdempotencyKey     string        `json:"idempotency_key"`
	Items              []InvoiceLine `json:"items" jsonschema:"Entries returned by this call."`
	AdjustmentPositive float64       `json:"adjustment_positive"`
	AdjustmentNegative float64       `json:"adjustment_negative"`
	Comment            string        `json:"comment"`
	Notify             bool          `json:"notify"`
}

type creditmemoCreateOutput struct {
	Creditmemo  Creditmemo `json:"creditmemo" jsonschema:"The created credit memo, which is the refund record."`
	InvoiceID   int        `json:"invoice_id" jsonschema:"Invoice the refund was issued against."`
	OperationID string     `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool       `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so the customer was not refunded twice."`
}

func newShipmentCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(shipmentCreateToolName, shipmentCreateToolTitle, shipmentCreateDescription, "magento.shipment.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(shipmentCreateToolName, shipmentCreateToolTitle, shipmentCreateDescription, shipmentCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in shipmentCreateInput) (*mcp.CallToolResult, shipmentCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, shipmentCreateOutput{}, err
				}
				lines := make([]DocumentLine, 0, len(in.Items))
				for _, item := range in.Items {
					lines = append(lines, DocumentLine(item))
				}
				shipment, err := svc.CreateShipment(ctx, ShipmentCreateInput{
					OrderID: in.OrderID, Items: lines, Tracks: in.Tracks,
					Comment: in.Comment, Notify: in.Notify,
				})
				if err != nil {
					return nil, shipmentCreateOutput{}, err
				}
				return nil, shipmentCreateOutput{Shipment: *shipment}, nil
			})
		return nil
	}
	return t
}

func newCreditmemoCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(creditmemoCreateToolName, creditmemoCreateToolTitle, creditmemoCreateDescription, "magento.creditmemo.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(creditmemoCreateToolName, creditmemoCreateToolTitle, creditmemoCreateDescription, creditmemoCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in creditmemoCreateInput) (*mcp.CallToolResult, creditmemoCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, creditmemoCreateOutput{}, err
				}
				lines := make([]DocumentLine, 0, len(in.Items))
				for _, item := range in.Items {
					lines = append(lines, DocumentLine(item))
				}
				result, err := svc.CreateCreditmemo(ctx, CreditmemoCreateInput{
					OrderID: in.OrderID, InvoiceID: in.InvoiceID, Items: lines,
					AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
					Comment: in.Comment, Notify: in.Notify,
					IdempotencyKey: in.IdempotencyKey, Instance: in.Instance,
				})
				if err != nil {
					return nil, creditmemoCreateOutput{}, err
				}
				return nil, creditmemoCreateOutput{
					Creditmemo: result.Creditmemo, InvoiceID: result.InvoiceID,
					OperationID: result.OperationID, Replayed: result.Replayed,
				}, nil
			})
		return nil
	}
	return t
}

const (
	orderCommentToolName    = "magento.order.comment"
	orderCommentToolTitle   = "Add Magento order comment"
	orderCommentDescription = "Magento write operation: appends a status-history comment to an order " +
		"(POST /rest/{store_code}/V1/orders/{id}/comments) on the resolved instance; a non-empty status also transitions the order. " +
		"Requires human confirmation."
	orderHoldToolName    = "magento.order.hold"
	orderHoldToolTitle   = "Hold a Magento order"
	orderHoldDescription = "Magento write operation: places an order on hold " +
		"(POST /rest/{store_code}/V1/orders/{id}/hold) on the resolved instance. Requires human confirmation."
	orderUnholdToolName    = "magento.order.unhold"
	orderUnholdToolTitle   = "Release a Magento order hold"
	orderUnholdDescription = "Magento write operation: releases an order hold " +
		"(POST /rest/{store_code}/V1/orders/{id}/unhold) on the resolved instance. Requires human confirmation."

	orderCommentSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id."},
		"comment": {"type": "string", "minLength": 1, "maxLength": 1024, "description": "Comment text shown in the order's status history."},
		"status": {"type": "string", "maxLength": 64, "description": "Optional new order status; omit to only add the comment."},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "comment"], "additionalProperties": false}`

	orderHoldSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id."}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type orderCommentInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID  int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Comment  string `json:"comment"`
	Status   string `json:"status" jsonschema:"Current status."`
	Notify   bool   `json:"notify"`
}

type orderHoldInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID  int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
}

type orderActionOutput struct {
	OrderID int  `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Applied bool `json:"applied" jsonschema:"True when the change was written."`
}

func newOrderCommentTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderCommentToolName, orderCommentToolTitle, orderCommentDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(orderCommentToolName, orderCommentToolTitle, orderCommentDescription, orderCommentSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderCommentInput) (*mcp.CallToolResult, orderActionOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderActionOutput{}, err
				}
				if err := svc.AddOrderComment(ctx, in.OrderID, in.Comment, in.Status, in.Notify); err != nil {
					return nil, orderActionOutput{}, err
				}
				return nil, orderActionOutput{OrderID: in.OrderID, Applied: true}, nil
			})
		return nil
	}
	return t
}

func newOrderHoldTool(lookup serviceLookup, hold bool) registry.Tool {
	name, title, description := orderHoldToolName, orderHoldToolTitle, orderHoldDescription
	if !hold {
		name, title, description = orderUnholdToolName, orderUnholdToolTitle, orderUnholdDescription
	}
	t := writeToolMetadata(name, title, description, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(name, title, description, orderHoldSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderHoldInput) (*mcp.CallToolResult, orderActionOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderActionOutput{}, err
				}
				if hold {
					err = svc.HoldOrder(ctx, in.OrderID)
				} else {
					err = svc.UnholdOrder(ctx, in.OrderID)
				}
				if err != nil {
					return nil, orderActionOutput{}, err
				}
				return nil, orderActionOutput{OrderID: in.OrderID, Applied: true}, nil
			})
		return nil
	}
	return t
}

const (
	categoryCreateToolName    = "magento.category.create"
	categoryCreateToolTitle   = "Create Magento category"
	categoryCreateDescription = "Magento write operation: creates a category under a parent " +
		"(POST /rest/{store_code}/V1/categories) on the resolved instance. Requires human confirmation. Category changes can alter product URL keys and URL rewrites; confirm the blast radius before creating, moving, renaming, or deleting a category, and plan redirects/reindex for existing links."
	categoryUpdateToolName    = "magento.category.update"
	categoryUpdateToolTitle   = "Update Magento category"
	categoryUpdateDescription = "Magento write operation: updates category name, active flag, position, or menu visibility " +
		"(PUT /rest/{store_code}/V1/categories/{id}) on the resolved instance. Requires human confirmation. Category changes can alter product URL keys and URL rewrites; confirm the blast radius before creating, moving, renaming, or deleting a category, and plan redirects/reindex for existing links."
	categoryAssignToolName    = "magento.category.assign_product"
	categoryAssignToolTitle   = "Assign a product to a category"
	categoryAssignDescription = "Magento write operation: links a product to a category " +
		"(POST /rest/{store_code}/V1/categories/{id}/products) on the resolved instance. Requires human confirmation."

	categoryCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"parent_id": {"type": "integer", "minimum": 1, "description": "Parent category id; 2 is Magento's default root."},
		"name": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display name."},
		"url_key": {"type": "string", "maxLength": 255, "description": "Optional URL key; Magento derives one from the name when omitted."},
		"is_active": {"type": "boolean", "default": true},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000},
		"include_in_menu": {"type": "boolean", "default": true}
	}, "required": ["instance", "parent_id", "name"], "additionalProperties": false}`

	categoryUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"category_id": {"type": "integer", "minimum": 1, "description": "Magento category entity id. The store root category is usually 2."},
		"name": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display name."},
		"is_active": {"type": "boolean"},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000},
		"include_in_menu": {"type": "boolean"}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`

	categoryAssignSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"category_id": {"type": "integer", "minimum": 1, "description": "Magento category entity id. The store root category is usually 2."},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU."},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000}
	}, "required": ["instance", "category_id", "sku"], "additionalProperties": false}`
)

type categoryCreateInput struct {
	Instance      string `json:"instance" jsonschema:"Configured instance id the result came from."`
	ParentID      int    `json:"parent_id" jsonschema:"Parent category id; 0 for the root."`
	Name          string `json:"name" jsonschema:"Display name."`
	URLKey        string `json:"url_key"`
	IsActive      *bool  `json:"is_active" jsonschema:"True when the entity is enabled."`
	Position      int    `json:"position" jsonschema:"Sort position among siblings."`
	IncludeInMenu *bool  `json:"include_in_menu" jsonschema:"True when the category appears in the navigation menu."`
}

type categoryCreateOutput struct {
	Category Category `json:"category" jsonschema:"The category that was created or read."`
}

type categoryUpdateInput struct {
	Instance      string  `json:"instance" jsonschema:"Configured instance id the result came from."`
	CategoryID    int     `json:"category_id" jsonschema:"Magento category entity id."`
	Name          *string `json:"name" jsonschema:"Display name."`
	IsActive      *bool   `json:"is_active" jsonschema:"True when the entity is enabled."`
	Position      *int    `json:"position" jsonschema:"Sort position among siblings."`
	IncludeInMenu *bool   `json:"include_in_menu" jsonschema:"True when the category appears in the navigation menu."`
}

type categoryUpdateOutput struct {
	Category Category `json:"category" jsonschema:"The category that was created or read."`
}

type categoryAssignInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	CategoryID int    `json:"category_id" jsonschema:"Magento category entity id."`
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	Position   int    `json:"position" jsonschema:"Sort position among siblings."`
}

type categoryAssignOutput struct {
	CategoryID int    `json:"category_id" jsonschema:"Magento category entity id."`
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	Assigned   bool   `json:"assigned" jsonschema:"True when the product is now assigned."`
}

func newCategoryCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(categoryCreateToolName, categoryCreateToolTitle, categoryCreateDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(categoryCreateToolName, categoryCreateToolTitle, categoryCreateDescription, categoryCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryCreateInput) (*mcp.CallToolResult, categoryCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryCreateOutput{}, err
				}
				isActive, includeInMenu := true, true
				if in.IsActive != nil {
					isActive = *in.IsActive
				}
				if in.IncludeInMenu != nil {
					includeInMenu = *in.IncludeInMenu
				}
				category, err := svc.CreateCategory(ctx, CategoryCreateInput{
					ParentID: in.ParentID, Name: in.Name, IsActive: isActive, URLKey: in.URLKey,
					Position: in.Position, IncludeInMenu: includeInMenu,
				})
				if err != nil {
					return nil, categoryCreateOutput{}, err
				}
				return nil, categoryCreateOutput{Category: *category}, nil
			})
		return nil
	}
	return t
}

func newCategoryUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(categoryUpdateToolName, categoryUpdateToolTitle, categoryUpdateDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(categoryUpdateToolName, categoryUpdateToolTitle, categoryUpdateDescription, categoryUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryUpdateInput) (*mcp.CallToolResult, categoryUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryUpdateOutput{}, err
				}
				category, err := svc.UpdateCategory(ctx, CategoryUpdateInput{
					CategoryID: in.CategoryID, Name: in.Name, IsActive: in.IsActive,
					Position: in.Position, IncludeInMenu: in.IncludeInMenu,
				})
				if err != nil {
					return nil, categoryUpdateOutput{}, err
				}
				return nil, categoryUpdateOutput{Category: *category}, nil
			})
		return nil
	}
	return t
}

func newCategoryAssignProductTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(categoryAssignToolName, categoryAssignToolTitle, categoryAssignDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(categoryAssignToolName, categoryAssignToolTitle, categoryAssignDescription, categoryAssignSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in categoryAssignInput) (*mcp.CallToolResult, categoryAssignOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, categoryAssignOutput{}, err
				}
				if err := svc.AssignProductToCategory(ctx, in.CategoryID, in.SKU, in.Position); err != nil {
					return nil, categoryAssignOutput{}, err
				}
				return nil, categoryAssignOutput{CategoryID: in.CategoryID, SKU: in.SKU, Assigned: true}, nil
			})
		return nil
	}
	return t
}

const (
	productLinkAddToolName    = "magento.product.links.add"
	productLinkAddToolTitle   = "Add a Magento product link"
	productLinkAddDescription = "Magento write operation: creates a related, upsell, or cross-sell link between two products " +
		"(POST /rest/{store_code}/V1/products/{sku}/links) on the resolved instance. Requires human confirmation."
	productLinkRemoveToolName    = "magento.product.links.remove"
	productLinkRemoveToolTitle   = "Remove a Magento product link"
	productLinkRemoveDescription = "Magento write operation: deletes a related, upsell, or cross-sell link " +
		"(DELETE /rest/{store_code}/V1/products/{sku}/links/{type}/{linkedSku}) on the resolved instance. Requires human confirmation."

	productLinkAddSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product the relation is defined on."},
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"], "description": "Relation kind: related (also bought), upsell (upgrade) or crosssell (add-on)."},
		"linked_product_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Other product in the relation. It must already exist and must not be the same SKU."},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000}
	}, "required": ["instance", "sku", "link_type", "linked_product_sku"], "additionalProperties": false}`

	productLinkRemoveSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product the relation is defined on."},
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"], "description": "Relation kind: related (also bought), upsell (upgrade) or crosssell (add-on)."},
		"linked_product_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Other product in the relation. It must already exist and must not be the same SKU."}
	}, "required": ["instance", "sku", "link_type", "linked_product_sku"], "additionalProperties": false}`
)

type productLinkWriteInput struct {
	Instance         string `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU              string `json:"sku" jsonschema:"Product SKU."`
	LinkType         string `json:"link_type" jsonschema:"Relation kind: related, upsell or crosssell."`
	LinkedProductSKU string `json:"linked_product_sku" jsonschema:"The other product in the relation."`
	Position         int    `json:"position" jsonschema:"Sort position among siblings."`
}

type productLinkWriteOutput struct {
	SKU              string `json:"sku" jsonschema:"Product SKU."`
	LinkType         string `json:"link_type" jsonschema:"Relation kind: related, upsell or crosssell."`
	LinkedProductSKU string `json:"linked_product_sku" jsonschema:"The other product in the relation."`
	Applied          bool   `json:"applied" jsonschema:"True when the change was written."`
}

func newProductLinkAddTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productLinkAddToolName, productLinkAddToolTitle, productLinkAddDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productLinkAddToolName, productLinkAddToolTitle, productLinkAddDescription, productLinkAddSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productLinkWriteInput) (*mcp.CallToolResult, productLinkWriteOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productLinkWriteOutput{}, err
				}
				if err := svc.AddProductLink(ctx, ProductLinkInput{
					SKU: in.SKU, LinkType: in.LinkType, LinkedProductSKU: in.LinkedProductSKU, Position: in.Position,
				}); err != nil {
					return nil, productLinkWriteOutput{}, err
				}
				return nil, productLinkWriteOutput{SKU: in.SKU, LinkType: in.LinkType, LinkedProductSKU: in.LinkedProductSKU, Applied: true}, nil
			})
		return nil
	}
	return t
}

func newProductLinkRemoveTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productLinkRemoveToolName, productLinkRemoveToolTitle, productLinkRemoveDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productLinkRemoveToolName, productLinkRemoveToolTitle, productLinkRemoveDescription, productLinkRemoveSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productLinkWriteInput) (*mcp.CallToolResult, productLinkWriteOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productLinkWriteOutput{}, err
				}
				if err := svc.RemoveProductLink(ctx, in.SKU, in.LinkType, in.LinkedProductSKU); err != nil {
					return nil, productLinkWriteOutput{}, err
				}
				return nil, productLinkWriteOutput{SKU: in.SKU, LinkType: in.LinkType, LinkedProductSKU: in.LinkedProductSKU, Applied: true}, nil
			})
		return nil
	}
	return t
}

const (
	invoiceCaptureToolName    = "magento.invoice.capture"
	invoiceCaptureToolTitle   = "Capture a Magento invoice"
	invoiceCaptureDescription = "Magento write operation: captures the payment for an existing invoice " +
		"(POST /rest/{store_code}/V1/invoices/{id}/capture) on the resolved instance. The invoice is read first: an already-paid or canceled invoice is a " +
		"conflict and no capture request is sent. Idempotent when idempotency_key is supplied: a repeat with the same arguments " +
		"replays the recorded capture. An ambiguous outcome is recorded as unknown and must be polled, never retried. " +
		"Requires human confirmation."

	invoiceCaptureSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + idempotencyKeyProp + `,
		"invoice_id": {"type": "integer", "minimum": 1, "description": "Invoice entity id from magento.invoice.list or invoice.create."}
	}, "required": ["instance", "invoice_id"], "additionalProperties": false}`

	orderEmailToolName    = "magento.order.email"
	orderEmailToolTitle   = "Send a Magento order email"
	orderEmailDescription = "Magento write operation: sends the order confirmation email " +
		"(POST /rest/{store_code}/V1/orders/{id}/emails) on the resolved instance. Requires human confirmation."

	orderEmailSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id."}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type invoiceCaptureInput struct {
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	InvoiceID      int    `json:"invoice_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type invoiceCaptureOutput struct {
	InvoiceID   int    `json:"invoice_id" jsonschema:"Invoice the capture was attempted for."`
	Captured    bool   `json:"captured" jsonschema:"True when the money was taken. False with no error means the store accepted the call but reported no capture."`
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome. A capture that returned operation_unknown may still have moved money."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so money was not taken twice."`
}

type orderEmailInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID  int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
}

type orderEmailOutput struct {
	OrderID int  `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Sent    bool `json:"sent" jsonschema:"True when the store accepted the request to send."`
}

func newInvoiceCaptureTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(invoiceCaptureToolName, invoiceCaptureToolTitle, invoiceCaptureDescription, "magento.invoice.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(invoiceCaptureToolName, invoiceCaptureToolTitle, invoiceCaptureDescription, invoiceCaptureSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in invoiceCaptureInput) (*mcp.CallToolResult, invoiceCaptureOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceCaptureOutput{}, err
				}
				result, err := svc.CaptureInvoice(ctx, CaptureInvoiceInput{
					InvoiceID: in.InvoiceID, IdempotencyKey: in.IdempotencyKey, Instance: in.Instance,
				})
				if err != nil {
					return nil, invoiceCaptureOutput{}, err
				}
				return nil, invoiceCaptureOutput{
					InvoiceID: result.InvoiceID, Captured: result.Captured,
					OperationID: result.OperationID, Replayed: result.Replayed,
				}, nil
			})
		return nil
	}
	return t
}

func newOrderEmailTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderEmailToolName, orderEmailToolTitle, orderEmailDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(orderEmailToolName, orderEmailToolTitle, orderEmailDescription, orderEmailSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderEmailInput) (*mcp.CallToolResult, orderEmailOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderEmailOutput{}, err
				}
				if err := svc.SendOrderEmail(ctx, in.OrderID); err != nil {
					return nil, orderEmailOutput{}, err
				}
				return nil, orderEmailOutput{OrderID: in.OrderID, Sent: true}, nil
			})
		return nil
	}
	return t
}

const (
	mediaDeleteToolName    = "magento.product.media.delete"
	mediaDeleteToolTitle   = "Delete product media"
	mediaDeleteDescription = "Magento write operation: deletes one media entry from a product gallery " +
		"(DELETE /rest/{store_code}/V1/products/{sku}/media/{id}) on the resolved instance. Irreversible. Requires human confirmation."

	mediaDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"media_id": {"type": "integer", "minimum": 1, "description": "Gallery entry id from product.media, not a file name."}
	}, "required": ["instance", "sku", "media_id"], "additionalProperties": false}`
)

type mediaDeleteInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string `json:"sku" jsonschema:"Product SKU."`
	MediaID  int    `json:"media_id" jsonschema:"Gallery entry id."`
}

type mediaDeleteOutput struct {
	SKU     string `json:"sku" jsonschema:"Product SKU."`
	MediaID int    `json:"media_id" jsonschema:"Gallery entry id."`
	Deleted bool   `json:"deleted" jsonschema:"True when the entry was removed."`
}

func newMediaDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(mediaDeleteToolName, mediaDeleteToolTitle, mediaDeleteDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: mediaDeleteToolName, Title: mediaDeleteToolTitle, Description: discloseWrite(mediaDeleteToolName, mediaDeleteDescription),
			InputSchema: json.RawMessage(mediaDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(mediaDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in mediaDeleteInput) (*mcp.CallToolResult, mediaDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, mediaDeleteOutput{}, err
			}
			if err := svc.DeleteMedia(ctx, in.SKU, in.MediaID); err != nil {
				return nil, mediaDeleteOutput{}, err
			}
			return nil, mediaDeleteOutput{SKU: in.SKU, MediaID: in.MediaID, Deleted: true}, nil
		})
		return nil
	}
	return t
}

const (
	orderCreateToolName    = "magento.order.create"
	orderCreateToolTitle   = "Create a Magento order"
	orderCreateDescription = "Magento write operation: places an order through the admin cart flow (cart, items, shipping information, then " +
		"PUT /rest/{store_code}/V1/carts/{cartId}/order) on the resolved instance. Totals are computed by Magento, never by the caller. " +
		"Idempotent when idempotency_key is supplied: a repeat with the same arguments replays the recorded order. An ambiguous " +
		"placement is reconciled by reading the customer's recent orders back; an inconclusive read returns operation_unknown " +
		"and must be polled, never retried. Requires human confirmation."

	addressSchema = `{"type": "object", "description": "Postal address for the order. Billing and shipping may be the same; both are required because Magento prices tax and shipping from them.", "additionalProperties": false,
		"required": ["firstname", "lastname", "street", "city", "postcode", "country_id", "telephone"],
		"properties": {
			"firstname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Recipient given name."},
			"lastname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Recipient family name."},
			"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}, "description": "Street lines, most specific first. Magento stores up to four."},
			"city": {"type": "string", "minLength": 1, "maxLength": 255, "description": "City or locality."},
			"postcode": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Postal code as the destination country writes it; not validated against the country."},
			"country_id": {"type": "string", "pattern": "^[A-Z]{2}$", "description": "ISO 3166-1 alpha-2 country code, uppercase, for example PL or US."},
			"region_id": {"type": "integer", "minimum": 1, "description": "Numeric region (state/province) id. Magento requires it for countries that have a region directory (Poland among them) and rejects the address otherwise."},
			"telephone": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Contact phone number for this address."},
			"region": {"type": "string", "maxLength": 255}
		}}`

	orderCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + idempotencyKeyProp + `,
		"customer_id": {"type": "integer", "minimum": 1, "description": "Existing Magento customer id the order is placed for. The cart is created under this customer, so it must exist."},
		"store_id": {"type": "integer", "minimum": 0, "description": "Not supported: the store is selected by the instance's store_code, and a non-zero value is rejected with unsupported_capability rather than silently ignored."},
		"items": {"type": "array", "minItems": 1, "maxItems": 50, "description": "Order lines. Each SKU must be sellable on its own: a configurable parent needs the child SKU, and a product with required custom options cannot be added here.", "items": {"type": "object",
			"additionalProperties": false, "required": ["sku", "qty"],
			"properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU to add to the cart."},
				"qty": {"type": "number", "exclusiveMinimum": 0, "description": "Quantity as a number, not a string. Fractional quantities are allowed only for products configured to accept them."}}}},
		"billing_address": ` + addressSchema + `,
		"shipping_address": ` + addressSchema + `,
		"shipping_carrier_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Shipping carrier code, for example flatrate. It must be enabled for the store, or the store rejects the placement."},
		"shipping_method_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Shipping method within the carrier, for example flatrate. Paired with shipping_carrier_code."},
		"payment_method": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Enabled Magento payment method code, e.g. checkmo."}
	}, "required": ["instance", "customer_id", "items", "billing_address", "shipping_address",
		"shipping_carrier_code", "shipping_method_code", "payment_method"], "additionalProperties": false}`
)

type orderAddressWire struct {
	Firstname string   `json:"firstname" jsonschema:"Given name."`
	Lastname  string   `json:"lastname" jsonschema:"Family name."`
	Street    []string `json:"street" jsonschema:"Street lines, most specific first."`
	City      string   `json:"city" jsonschema:"City or locality."`
	Postcode  string   `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	CountryID string   `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone string   `json:"telephone" jsonschema:"Contact phone number."`
	Region    string   `json:"region" jsonschema:"Region name."`
	RegionID  int      `json:"region_id" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
}

type orderCreateInput struct {
	Instance            string           `json:"instance" jsonschema:"Configured instance id the result came from."`
	CustomerID          int              `json:"customer_id"`
	StoreID             int              `json:"store_id" jsonschema:"Store view the entity belongs to."`
	IdempotencyKey      string           `json:"idempotency_key"`
	Items               []OrderItemInput `json:"items" jsonschema:"Entries returned by this call."`
	BillingAddress      orderAddressWire `json:"billing_address"`
	ShippingAddress     orderAddressWire `json:"shipping_address"`
	ShippingCarrierCode string           `json:"shipping_carrier_code"`
	ShippingMethodCode  string           `json:"shipping_method_code"`
	PaymentMethod       string           `json:"payment_method"`
}

type orderCreateOutput struct {
	Order Order `json:"order" jsonschema:"The placed order. Its entity_id is what every downstream invoice, shipment and credit memo call takes."`
	// OperationID names the ledger row for this placement. Keep it: it is how an
	// unknown outcome is polled and how a reconciliation is traced.
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id. Absent only when no durable ledger is configured. Poll it after an operation_unknown."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when this call returned a previously recorded result for the same idempotency_key, so no second order was placed."`
	Reconciled  bool   `json:"reconciled,omitempty" jsonschema:"True when the placement's response was lost and the order was found by reading the customer's recent orders. The order is real; it was confirmed rather than acknowledged."`
}

func (a orderAddressWire) toInput() OrderAddressInput {
	return OrderAddressInput(a)
}

func newOrderCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderCreateToolName, orderCreateToolTitle, orderCreateDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(orderCreateToolName, orderCreateToolTitle, orderCreateDescription, orderCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderCreateInput) (*mcp.CallToolResult, orderCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderCreateOutput{}, err
				}
				result, err := svc.CreateOrder(ctx, CreateOrderInput{
					CustomerID: in.CustomerID, StoreID: in.StoreID, Items: in.Items,
					BillingAddress: in.BillingAddress.toInput(), ShippingAddress: in.ShippingAddress.toInput(),
					ShippingCarrierCode: in.ShippingCarrierCode, ShippingMethodCode: in.ShippingMethodCode,
					PaymentMethod:  in.PaymentMethod,
					IdempotencyKey: in.IdempotencyKey, Instance: in.Instance,
				})
				if err != nil {
					return nil, orderCreateOutput{}, err
				}
				return nil, orderCreateOutput{
					Order: result.Order, OperationID: result.OperationID,
					Replayed: result.Replayed, Reconciled: result.Reconciled,
				}, nil
			})
		return nil
	}
	return t
}

const (
	tierPricesSetToolName    = "magento.product.tier_prices.set"
	tierPricesSetToolTitle   = "Replace Magento tier prices"
	tierPricesSetDescription = "Magento write operation: replaces the tier prices of one product " +
		"(POST /rest/{store_code}/V1/products/tier-prices) on the resolved instance with 1-50 fixed-price tiers. " +
		"Magento's storage interface replaces the SKU's stored tiers, so omitted tiers are removed. Requires human confirmation."

	tierPricesSetSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"prices": {"type": "array", "minItems": 1, "maxItems": 50, "description": "Tier prices to set. They replace the product's existing tier prices.", "items": {"type": "object",
			"additionalProperties": false, "required": ["price", "customer_group", "quantity"],
			"properties": {"price": {"type": "number", "exclusiveMinimum": 0},
				"website_id": {"type": "integer", "minimum": 0},
				"customer_group": {"type": "string", "minLength": 1, "maxLength": 64},
				"quantity": {"type": "number", "exclusiveMinimum": 0}}}}
	}, "required": ["instance", "sku", "prices"], "additionalProperties": false}`
)

type tierPriceWire struct {
	Price         float64 `json:"price"`
	WebsiteID     int     `json:"website_id" jsonschema:"Website the entity belongs to."`
	CustomerGroup string  `json:"customer_group"`
	Quantity      float64 `json:"quantity"`
}

type tierPricesSetInput struct {
	Instance string          `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string          `json:"sku" jsonschema:"Product SKU."`
	Prices   []tierPriceWire `json:"prices" jsonschema:"Tier prices now set on the product."`
}

type tierPricesSetOutput struct {
	SKU    string `json:"sku" jsonschema:"Product SKU."`
	Prices int    `json:"prices" jsonschema:"Tier prices now set on the product."`
	Set    bool   `json:"set" jsonschema:"True when the values were written."`
}

func newTierPricesSetTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(tierPricesSetToolName, tierPricesSetToolTitle, tierPricesSetDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(tierPricesSetToolName, tierPricesSetToolTitle, tierPricesSetDescription, tierPricesSetSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in tierPricesSetInput) (*mcp.CallToolResult, tierPricesSetOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, tierPricesSetOutput{}, err
				}
				prices := make([]TierPrice, 0, len(in.Prices))
				for _, price := range in.Prices {
					prices = append(prices, TierPrice{
						SKU: in.SKU, Price: price.Price, PriceType: "fixed",
						WebsiteID: price.WebsiteID, CustomerGroup: price.CustomerGroup, Quantity: price.Quantity,
					})
				}
				if err := svc.SetTierPrices(ctx, in.SKU, prices); err != nil {
					return nil, tierPricesSetOutput{}, err
				}
				return nil, tierPricesSetOutput{SKU: in.SKU, Prices: len(prices), Set: true}, nil
			})
		return nil
	}
	return t
}

// derefInt renders an optional observed quantity for the transfer output, which
// is only produced for an applied transfer.
func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

const (
	inventoryTransferToolName    = "magento.inventory.transfer"
	inventoryTransferToolTitle   = "Transfer Magento source quantity"
	inventoryTransferDescription = "Magento write operation: moves whole units for one SKU between MSI sources (read, re-read to " +
		"detect a concurrent writer, upsert both rows, read them back) on the resolved instance. Refuses an insufficient source and a " +
		"fractional observed quantity. The result carries a transfer_id, the observed before/after quantities of both sources, and an " +
		"outcome; needs_verification means unknown: applied_rows is \"unknown\", so verify both sources and do NOT retry. A partial or " +
		"conflicting write is an error naming the applied rows and is never retried or compensated automatically. Known residual: a " +
		"concurrent transfer of the same SKU on another replica can still be lost when the re-read sees identical values. Destructive " +
		"and confirmation-gated."

	inventoryTransferSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"from_source": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Source code the stock leaves, for example default. Use magento.inventory.sources to list them."},
		"to_source": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Source code the stock arrives at. Must differ from from_source."},
		"quantity": {"type": "integer", "minimum": 1, "description": "Whole units to move. Physical source quantity, not salable quantity: reservations are not adjusted by a transfer."}
	}, "required": ["instance", "sku", "from_source", "to_source", "quantity"], "additionalProperties": false}`
)

type inventoryTransferInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU        string `json:"sku" jsonschema:"Product SKU."`
	FromSource string `json:"from_source"`
	ToSource   string `json:"to_source"`
	Quantity   int    `json:"quantity"`
}

type inventoryTransferOutput struct {
	TransferID  string `json:"transfer_id" jsonschema:"Identifier of this transfer, for correlating the before/after readings."`
	SKU         string `json:"sku" jsonschema:"Product whose stock moved."`
	FromSource  string `json:"from_source" jsonschema:"Source the units left."`
	ToSource    string `json:"to_source" jsonschema:"Source the units arrived at."`
	Quantity    int    `json:"quantity" jsonschema:"Whole units requested. Compare from_before/from_after to see what actually moved."`
	FromBefore  int    `json:"from_before" jsonschema:"Physical quantity at from_source read immediately before the write."`
	FromAfter   int    `json:"from_after" jsonschema:"Physical quantity at from_source read back after the write."`
	ToBefore    int    `json:"to_before" jsonschema:"Physical quantity at to_source read immediately before the write."`
	ToAfter     int    `json:"to_after" jsonschema:"Physical quantity at to_source read back after the write."`
	Outcome     string `json:"outcome" jsonschema:"applied, needs_verification or failed. needs_verification means the write returned but the read-back did not confirm it, so check the quantities before retrying."`
	Transferred bool   `json:"transferred" jsonschema:"True only when the read-back shows both sources at the expected quantities."`
}

func newInventoryTransferTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(inventoryTransferToolName, inventoryTransferToolTitle, inventoryTransferDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: inventoryTransferToolName, Title: inventoryTransferToolTitle, Description: discloseWrite(inventoryTransferToolName, inventoryTransferDescription),
			InputSchema: json.RawMessage(inventoryTransferSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(inventoryTransferToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in inventoryTransferInput) (*mcp.CallToolResult, inventoryTransferOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, inventoryTransferOutput{}, err
			}
			result, err := svc.TransferSourceQty(ctx, in.SKU, in.FromSource, in.ToSource, in.Quantity)
			if err != nil {
				return nil, inventoryTransferOutput{}, err
			}
			return nil, inventoryTransferOutput{
				TransferID: result.TransferID, SKU: result.SKU, FromSource: result.FromSource,
				ToSource: result.ToSource, Quantity: result.Quantity,
				FromBefore: result.FromBefore, FromAfter: derefInt(result.FromAfter),
				ToBefore: result.ToBefore, ToAfter: derefInt(result.ToAfter),
				Outcome: string(result.Outcome), Transferred: result.Outcome == TransferApplied,
			}, nil
		})
		return nil
	}
	return t
}

const (
	customerCreateToolName    = "magento.customer.create"
	customerCreateToolTitle   = "Create a Magento customer"
	customerCreateDescription = "Magento write operation: creates a customer " +
		"(POST /rest/{store_code}/V1/customers) on the resolved instance. PII is accepted because a customer requires it, " +
		"but the response is the PII-safe projection (id, group, website, store, timestamps) and never echoes email or name. " +
		"Requires human confirmation."
	customerUpdateToolName    = "magento.customer.update"
	customerUpdateTitle       = "Update a Magento customer"
	customerUpdateDescription = "Magento write operation: updates customer email, first/last name, or group " +
		"(PUT /rest/{store_code}/V1/customers/{id}) on the resolved instance. Unset fields are omitted; the response is PII-safe. " +
		"Requires human confirmation."

	customerCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"email": {"type": "string", "format": "email", "maxLength": 254, "description": "Customer email. It is the login identity and must be unique in the store."},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Given name."},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Family name."},
		"website_id": {"type": "integer", "minimum": 1, "description": "Website the account belongs to. The default store website is usually 1."},
		"store_id": {"type": "integer", "minimum": 0, "description": "Store view the account is created in. 0 selects the default."},
		"group_id": {"type": "integer", "minimum": 0, "description": "Customer group id, which decides pricing. 0 selects the store default."}
	}, "required": ["instance", "email", "firstname", "lastname", "website_id"], "additionalProperties": false}`

	customerUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"customer_id": {"type": "integer", "minimum": 1, "description": "Existing Magento customer id."},
		"email": {"type": "string", "format": "email", "maxLength": 254},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"group_id": {"type": "integer", "minimum": 0}
	}, "required": ["instance", "customer_id"], "additionalProperties": false}`
)

type customerCreateWire struct {
	Instance  string `json:"instance" jsonschema:"Configured instance id the result came from."`
	Email     string `json:"email" jsonschema:"Customer email."`
	Firstname string `json:"firstname" jsonschema:"Given name."`
	Lastname  string `json:"lastname" jsonschema:"Family name."`
	WebsiteID int    `json:"website_id" jsonschema:"Website the entity belongs to."`
	StoreID   int    `json:"store_id" jsonschema:"Store view the entity belongs to."`
	GroupID   int    `json:"group_id" jsonschema:"Customer group id, which decides pricing."`
}

type customerCreateOutput struct {
	Customer Customer `json:"customer" jsonschema:"The customer that was created or read."`
}

type customerUpdateWire struct {
	Instance   string  `json:"instance" jsonschema:"Configured instance id the result came from."`
	CustomerID int     `json:"customer_id"`
	Email      *string `json:"email" jsonschema:"Customer email."`
	Firstname  *string `json:"firstname" jsonschema:"Given name."`
	Lastname   *string `json:"lastname" jsonschema:"Family name."`
	GroupID    *int    `json:"group_id" jsonschema:"Customer group id, which decides pricing."`
}

type customerUpdateOutput struct {
	Customer Customer `json:"customer" jsonschema:"The customer that was created or read."`
}

func newCustomerCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerCreateToolName, customerCreateToolTitle, customerCreateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(customerCreateToolName, customerCreateToolTitle, customerCreateDescription, customerCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerCreateWire) (*mcp.CallToolResult, customerCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerCreateOutput{}, err
				}
				customer, err := svc.CreateCustomer(ctx, CustomerCreateInput{
					Email: in.Email, Firstname: in.Firstname, Lastname: in.Lastname,
					WebsiteID: in.WebsiteID, StoreID: in.StoreID, GroupID: in.GroupID,
				})
				if err != nil {
					return nil, customerCreateOutput{}, err
				}
				return nil, customerCreateOutput{Customer: *customer}, nil
			})
		return nil
	}
	return t
}

func newCustomerUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerUpdateToolName, customerUpdateTitle, customerUpdateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(customerUpdateToolName, customerUpdateTitle, customerUpdateDescription, customerUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerUpdateWire) (*mcp.CallToolResult, customerUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerUpdateOutput{}, err
				}
				customer, err := svc.UpdateCustomer(ctx, CustomerUpdateInput{
					CustomerID: in.CustomerID, Email: in.Email,
					Firstname: in.Firstname, Lastname: in.Lastname, GroupID: in.GroupID,
				})
				if err != nil {
					return nil, customerUpdateOutput{}, err
				}
				return nil, customerUpdateOutput{Customer: *customer}, nil
			})
		return nil
	}
	return t
}

const (
	mediaUpdateToolName    = "magento.product.media.update"
	mediaUpdateToolTitle   = "Update product media"
	mediaUpdateDescription = "Magento write operation: updates one gallery entry (label, position, types, disabled) " +
		"(PUT /rest/{store_code}/V1/products/{sku}/media/{id}) on the resolved instance. Unset fields are omitted. Requires human confirmation."
	orderCancelToolName    = "magento.order.cancel"
	orderCancelToolTitle   = "Cancel a Magento order"
	orderCancelDescription = "Magento write operation: cancels an order " +
		"(POST /rest/{store_code}/V1/orders/{id}/cancel) on the resolved instance. The order is read first: an order that is already " +
		"canceled, complete or closed is a conflict and no request is sent. Irreversible. Idempotent when idempotency_key is supplied: " +
		"a repeat with the same arguments replays the recorded cancellation; an ambiguous outcome is resolved by reading the order " +
		"back, and an unreadable order returns operation_unknown and must be polled, never retried. Requires human confirmation."

	mediaUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"media_id": {"type": "integer", "minimum": 1, "description": "Gallery entry id from product.media, not a file name."},
		"label": {"type": "string", "maxLength": 255, "description": "Label shown to the shopper."},
		"position": {"type": "integer", "minimum": 0, "maximum": 1000},
		"types": {"type": "array", "items": {"type": "string", "enum": ["image", "small_image", "thumbnail", "swatch_image"]}, "maxItems": 4},
		"disabled": {"type": "boolean"}
	}, "required": ["instance", "sku", "media_id"], "additionalProperties": false}`

	orderCancelSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + idempotencyKeyProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id, not the increment id."}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type mediaUpdateInput struct {
	Instance string   `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string   `json:"sku" jsonschema:"Product SKU."`
	MediaID  int      `json:"media_id" jsonschema:"Gallery entry id."`
	Label    *string  `json:"label" jsonschema:"Label shown to the shopper."`
	Position *int     `json:"position" jsonschema:"Sort position among siblings."`
	Types    []string `json:"types" jsonschema:"Media roles available for the attribute set."`
	Disabled *bool    `json:"disabled" jsonschema:"True when the entry is hidden in the storefront."`
}

type mediaUpdateOutput struct {
	Media MediaEntry `json:"media" jsonschema:"Gallery entry that was written."`
}

type orderCancelInput struct {
	Instance       string `json:"instance" jsonschema:"Configured instance id the result came from."`
	OrderID        int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	IdempotencyKey string `json:"idempotency_key"`
}

type orderCancelOutput struct {
	OrderID     int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Canceled    bool   `json:"canceled" jsonschema:"True when the order is now canceled."`
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
	Reconciled  bool   `json:"reconciled,omitempty" jsonschema:"True when the result was confirmed by reading the store back rather than by its response."`
}

func newMediaUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(mediaUpdateToolName, mediaUpdateToolTitle, mediaUpdateDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(mediaUpdateToolName, mediaUpdateToolTitle, mediaUpdateDescription, mediaUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in mediaUpdateInput) (*mcp.CallToolResult, mediaUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, mediaUpdateOutput{}, err
				}
				media, err := svc.UpdateMedia(ctx, in.SKU, in.MediaID, MediaUpdateInput{
					Label: in.Label, Position: in.Position, Types: in.Types, Disabled: in.Disabled,
				})
				if err != nil {
					return nil, mediaUpdateOutput{}, err
				}
				return nil, mediaUpdateOutput{Media: *media}, nil
			})
		return nil
	}
	return t
}

func newOrderCancelTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(orderCancelToolName, orderCancelToolTitle, orderCancelDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: orderCancelToolName, Title: orderCancelToolTitle, Description: discloseWrite(orderCancelToolName, orderCancelDescription),
			InputSchema: json.RawMessage(orderCancelSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(orderCancelToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in orderCancelInput) (*mcp.CallToolResult, orderCancelOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, orderCancelOutput{}, err
			}
			result, err := svc.CancelOrder(ctx, CancelOrderInput{
				OrderID: in.OrderID, IdempotencyKey: in.IdempotencyKey, Instance: in.Instance,
			})
			if err != nil {
				return nil, orderCancelOutput{}, err
			}
			return nil, orderCancelOutput{
				OrderID: result.OrderID, Canceled: result.Canceled,
				OperationID: result.OperationID, Replayed: result.Replayed, Reconciled: result.Reconciled,
			}, nil
		})
		return nil
	}
	return t
}

const (
	attributeOptionCreateToolName    = "magento.attributes.options.create"
	attributeOptionCreateToolTitle   = "Create a Magento attribute option"
	attributeOptionCreateDescription = "Magento write operation: adds a value to a select or multiselect attribute " +
		"(POST /rest/{store_code}/V1/products/attributes/{code}/options) on the resolved instance. " +
		"Useful for adding configurable variant values before creating the product. Requires human confirmation. " +
		"Attribute options are schema: deleting an option that products reference (especially a configurable variant) breaks those products; check usage first."

	attributeOptionCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `", "description": "Attribute code such as color. It is the code, not the attribute id or label."},
		"label": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Label shown to the shopper."}
	}, "required": ["instance", "attribute_code", "label"], "additionalProperties": false}`
)

type attributeOptionCreateInput struct {
	Instance      string `json:"instance" jsonschema:"Configured instance id the result came from."`
	AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	Label         string `json:"label" jsonschema:"Label shown to the shopper."`
}

type attributeOptionCreateOutput struct {
	Option AttributeOption `json:"option" jsonschema:"The option that was created or read."`
}

func newAttributeOptionCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(attributeOptionCreateToolName, attributeOptionCreateToolTitle, attributeOptionCreateDescription, "magento.attribute.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(attributeOptionCreateToolName, attributeOptionCreateToolTitle, attributeOptionCreateDescription, attributeOptionCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in attributeOptionCreateInput) (*mcp.CallToolResult, attributeOptionCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, attributeOptionCreateOutput{}, err
				}
				option, err := svc.CreateAttributeOption(ctx, in.AttributeCode, in.Label)
				if err != nil {
					return nil, attributeOptionCreateOutput{}, err
				}
				return nil, attributeOptionCreateOutput{Option: *option}, nil
			})
		return nil
	}
	return t
}

const (
	attributeOptionDeleteToolName    = "magento.attributes.options.delete"
	attributeOptionDeleteToolTitle   = "Delete a Magento attribute option"
	attributeOptionDeleteDescription = "Magento write operation: deletes a select or multiselect option " +
		"(DELETE /rest/{store_code}/V1/products/attributes/{code}/options/{id}) on the resolved instance. " +
		"Destructive: products and configurable variants referencing the option make Magento reject the call. Requires human confirmation."

	attributeOptionDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `", "description": "Attribute code such as color. It is the code, not the attribute id or label."},
		"option_id": {"type": "integer", "minimum": 1, "description": "Custom option id from product.options, or the attribute option id for attribute tools."}
	}, "required": ["instance", "attribute_code", "option_id"], "additionalProperties": false}`
)

type attributeOptionDeleteInput struct {
	Instance      string `json:"instance" jsonschema:"Configured instance id the result came from."`
	AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	OptionID      int    `json:"option_id" jsonschema:"Custom option id."`
}

type attributeOptionDeleteOutput struct {
	AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	OptionID      int    `json:"option_id" jsonschema:"Custom option id."`
	Deleted       bool   `json:"deleted" jsonschema:"True when the entry was removed."`
}

func newAttributeOptionDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(attributeOptionDeleteToolName, attributeOptionDeleteToolTitle, attributeOptionDeleteDescription, "magento.attribute.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: attributeOptionDeleteToolName, Title: attributeOptionDeleteToolTitle, Description: discloseWrite(attributeOptionDeleteToolName, attributeOptionDeleteDescription),
			InputSchema: json.RawMessage(attributeOptionDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(attributeOptionDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in attributeOptionDeleteInput) (*mcp.CallToolResult, attributeOptionDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, attributeOptionDeleteOutput{}, err
			}
			if err := svc.DeleteAttributeOption(ctx, in.AttributeCode, in.OptionID); err != nil {
				return nil, attributeOptionDeleteOutput{}, err
			}
			return nil, attributeOptionDeleteOutput{AttributeCode: in.AttributeCode, OptionID: in.OptionID, Deleted: true}, nil
		})
		return nil
	}
	return t
}

const (
	bulkProductUpdateToolName    = "magento.products.bulk_update"
	bulkProductUpdateToolTitle   = "Bulk update Magento products"
	bulkProductUpdateDescription = "Magento write operation: applies the same status, visibility, or custom attributes to up to 50 products " +
		"(one PUT per SKU) on the resolved instance. Partial success: failed SKUs are reported per row and earlier updates are not rolled back. " +
		"Name and price are not part of the bulk update. Requires human confirmation."

	bulkProductUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"skus": {"type": "array", "minItems": 1, "maxItems": 50, "description": "Products to update. Every SKU must exist or the whole call is rejected.", "items": {"type": "string", "pattern": "` + skuInputPattern + `"}},
		"status": {"type": "integer", "enum": [1, 2]},
		"visibility": {"type": "integer", "enum": [1, 2, 3, 4]},
		"attributes": {"type": "object", "additionalProperties": {"type": "string", "maxLength": 255}}
	}, "required": ["instance", "skus"], "additionalProperties": false}`
)

type bulkProductUpdateInput struct {
	Instance   string            `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKUs       []string          `json:"skus"`
	Status     *int              `json:"status" jsonschema:"Current status."`
	Visibility *int              `json:"visibility"`
	Attributes map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`
}

type bulkProductUpdateOutput struct {
	Result BulkUpdateResult `json:"result" jsonschema:"The operation result."`
}

func newBulkProductUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkProductUpdateToolName, bulkProductUpdateToolTitle, bulkProductUpdateDescription, capabilityProductWrite)
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(bulkProductUpdateToolName, bulkProductUpdateToolTitle, bulkProductUpdateDescription, bulkProductUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in bulkProductUpdateInput) (*mcp.CallToolResult, bulkProductUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, bulkProductUpdateOutput{}, err
				}
				result, err := svc.BulkUpdateProducts(ctx, in.SKUs, UpdateProductInput{
					Status: in.Status, Visibility: in.Visibility, Attributes: in.Attributes,
				})
				if err != nil {
					return nil, bulkProductUpdateOutput{}, err
				}
				return nil, bulkProductUpdateOutput{Result: *result}, nil
			})
		return nil
	}
	return t
}

const (
	categoryDeleteToolName    = "magento.category.delete"
	categoryDeleteToolTitle   = "Delete a Magento category"
	categoryDeleteDescription = "Magento write operation: deletes a category and its subtree " +
		"(DELETE /rest/{store_code}/V1/categories/{id}) on the resolved instance. Products in the subtree lose that assignment but are not deleted. " +
		"Irreversible; requires human confirmation."

	categoryDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"category_id": {"type": "integer", "minimum": 1, "description": "Magento category entity id. The store root category is usually 2."}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`
)

type categoryDeleteInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	CategoryID int    `json:"category_id" jsonschema:"Magento category entity id."`
}

type categoryDeleteOutput struct {
	CategoryID int  `json:"category_id" jsonschema:"Magento category entity id."`
	Deleted    bool `json:"deleted" jsonschema:"True when the entry was removed."`
}

func newCategoryDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(categoryDeleteToolName, categoryDeleteToolTitle, categoryDeleteDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: categoryDeleteToolName, Title: categoryDeleteToolTitle, Description: discloseWrite(categoryDeleteToolName, categoryDeleteDescription),
			InputSchema: json.RawMessage(categoryDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(categoryDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in categoryDeleteInput) (*mcp.CallToolResult, categoryDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, categoryDeleteOutput{}, err
			}
			if err := svc.DeleteCategory(ctx, in.CategoryID); err != nil {
				return nil, categoryDeleteOutput{}, err
			}
			return nil, categoryDeleteOutput{CategoryID: in.CategoryID, Deleted: true}, nil
		})
		return nil
	}
	return t
}

const (
	customerAddressCreateToolName    = "magento.customer.addresses.create"
	customerAddressCreateToolTitle   = "Create a Magento customer address"
	customerAddressCreateDescription = "Magento write operation: adds an address to a customer " +
		"(POST /rest/{store_code}/V1/customers/{id}/addresses) on the resolved instance. PII is accepted because an address requires it; " +
		"the response is the PII-safe projection (country, region, default flags). Requires human confirmation."
	customerAddressDeleteToolName    = "magento.customer.addresses.delete"
	customerAddressDeleteTitle       = "Delete a Magento customer address"
	customerAddressDeleteDescription = "Magento write operation: deletes a customer address " +
		"(DELETE /rest/{store_code}/V1/addresses/{id}) on the resolved instance. Irreversible; requires human confirmation."

	customerAddressCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"customer_id": {"type": "integer", "minimum": 1, "description": "Existing Magento customer the address is added to."},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Recipient given name."},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Recipient family name."},
		"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}, "description": "Street lines, most specific first."},
		"city": {"type": "string", "minLength": 1, "maxLength": 255, "description": "City or locality."},
		"postcode": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Postal code as the destination country writes it."},
		"country_id": {"type": "string", "pattern": "^[A-Z]{2}$", "description": "ISO 3166-1 alpha-2 country code, uppercase."},
		"telephone": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Contact phone number for this address."},
		"region": {"type": "string", "maxLength": 255, "description": "Region name. Use region_id instead for countries that have a region directory."},
		"region_id": {"type": "integer", "minimum": 1, "description": "Numeric region (state/province) id. Magento requires it for countries that have a region directory (Poland among them)."},
		"default_billing": {"type": "boolean", "default": false},
		"default_shipping": {"type": "boolean", "default": false}
	}, "required": ["instance", "customer_id", "firstname", "lastname", "street", "city", "postcode", "country_id", "telephone"], "additionalProperties": false}`

	customerAddressDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"address_id": {"type": "integer", "minimum": 1, "description": "Customer address id from magento.customer.addresses."}
	}, "required": ["instance", "address_id"], "additionalProperties": false}`
)

type customerAddressCreateInput struct {
	Instance        string   `json:"instance" jsonschema:"Configured instance id the result came from."`
	CustomerID      int      `json:"customer_id"`
	Firstname       string   `json:"firstname" jsonschema:"Given name."`
	Lastname        string   `json:"lastname" jsonschema:"Family name."`
	Street          []string `json:"street" jsonschema:"Street lines, most specific first."`
	City            string   `json:"city" jsonschema:"City or locality."`
	Postcode        string   `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	CountryID       string   `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone       string   `json:"telephone" jsonschema:"Contact phone number."`
	Region          string   `json:"region" jsonschema:"Region name."`
	RegionID        int      `json:"region_id" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
	DefaultBilling  bool     `json:"default_billing" jsonschema:"True when this is the default billing address."`
	DefaultShipping bool     `json:"default_shipping" jsonschema:"True when this is the default shipping address."`
}

type customerAddressCreateOutput struct {
	Address CustomerAddress `json:"address" jsonschema:"The customer address that was written."`
}

type customerAddressDeleteInput struct {
	Instance  string `json:"instance" jsonschema:"Configured instance id the result came from."`
	AddressID int    `json:"address_id" jsonschema:"Customer address id."`
}

type customerAddressDeleteOutput struct {
	AddressID int  `json:"address_id" jsonschema:"Customer address id."`
	Deleted   bool `json:"deleted" jsonschema:"True when the entry was removed."`
}

func newCustomerAddressCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerAddressCreateToolName, customerAddressCreateToolTitle, customerAddressCreateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(customerAddressCreateToolName, customerAddressCreateToolTitle, customerAddressCreateDescription, customerAddressCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerAddressCreateInput) (*mcp.CallToolResult, customerAddressCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerAddressCreateOutput{}, err
				}
				address, err := svc.CreateCustomerAddress(ctx, in.CustomerID, CustomerAddressInput{
					Firstname: in.Firstname, Lastname: in.Lastname, Street: in.Street,
					City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
					Telephone: in.Telephone, Region: in.Region, RegionID: in.RegionID,
					DefaultBilling: in.DefaultBilling, DefaultShipping: in.DefaultShipping,
				})
				if err != nil {
					return nil, customerAddressCreateOutput{}, err
				}
				return nil, customerAddressCreateOutput{Address: *address}, nil
			})
		return nil
	}
	return t
}

func newCustomerAddressDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(customerAddressDeleteToolName, customerAddressDeleteTitle, customerAddressDeleteDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: customerAddressDeleteToolName, Title: customerAddressDeleteTitle, Description: discloseWrite(customerAddressDeleteToolName, customerAddressDeleteDescription),
			InputSchema: json.RawMessage(customerAddressDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(customerAddressDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in customerAddressDeleteInput) (*mcp.CallToolResult, customerAddressDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, customerAddressDeleteOutput{}, err
			}
			if err := svc.DeleteCustomerAddress(ctx, in.AddressID); err != nil {
				return nil, customerAddressDeleteOutput{}, err
			}
			return nil, customerAddressDeleteOutput{AddressID: in.AddressID, Deleted: true}, nil
		})
		return nil
	}
	return t
}

const (
	customerAddressUpdateToolName    = "magento.customer.addresses.update"
	customerAddressUpdateTitle       = "Update a Magento customer address"
	customerAddressUpdateDescription = "Magento write operation: partially updates a customer address " +
		"(PUT /rest/{store_code}/V1/customers/{id}/addresses/{addressId}) on the resolved instance. Unset fields are omitted; " +
		"the response is the PII-safe projection. Requires human confirmation."

	customerAddressUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"customer_id": {"type": "integer", "minimum": 1, "description": "Existing Magento customer id."},
		"address_id": {"type": "integer", "minimum": 1, "description": "Customer address id from magento.customer.addresses."},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}},
		"city": {"type": "string", "minLength": 1, "maxLength": 255},
		"postcode": {"type": "string", "minLength": 1, "maxLength": 255},
		"country_id": {"type": "string", "pattern": "^[A-Z]{2}$"},
		"telephone": {"type": "string", "minLength": 1, "maxLength": 255},
		"region": {"type": "string", "maxLength": 255},
		"region_id": {"type": "integer", "minimum": 1, "description": "Numeric region (state/province) id, required by Magento for countries with a region directory."},
		"default_billing": {"type": "boolean"},
		"default_shipping": {"type": "boolean"}
	}, "required": ["instance", "customer_id", "address_id"], "additionalProperties": false}`
)

type customerAddressUpdateInput struct {
	Instance        string    `json:"instance" jsonschema:"Configured instance id the result came from."`
	CustomerID      int       `json:"customer_id"`
	AddressID       int       `json:"address_id" jsonschema:"Customer address id."`
	Firstname       *string   `json:"firstname" jsonschema:"Given name."`
	Lastname        *string   `json:"lastname" jsonschema:"Family name."`
	Street          *[]string `json:"street" jsonschema:"Street lines, most specific first."`
	City            *string   `json:"city" jsonschema:"City or locality."`
	Postcode        *string   `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	CountryID       *string   `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Telephone       *string   `json:"telephone" jsonschema:"Contact phone number."`
	Region          *string   `json:"region" jsonschema:"Region name."`
	RegionID        *int      `json:"region_id" jsonschema:"Numeric region id; Magento requires it for countries with a region directory."`
	DefaultBilling  *bool     `json:"default_billing" jsonschema:"True when this is the default billing address."`
	DefaultShipping *bool     `json:"default_shipping" jsonschema:"True when this is the default shipping address."`
}

type customerAddressUpdateOutput struct {
	Address CustomerAddress `json:"address" jsonschema:"The customer address that was written."`
}

func newCustomerAddressUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerAddressUpdateToolName, customerAddressUpdateTitle, customerAddressUpdateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(customerAddressUpdateToolName, customerAddressUpdateTitle, customerAddressUpdateDescription, customerAddressUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerAddressUpdateInput) (*mcp.CallToolResult, customerAddressUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerAddressUpdateOutput{}, err
				}
				address, err := svc.UpdateCustomerAddress(ctx, in.CustomerID, in.AddressID, CustomerAddressUpdateInput{
					Firstname: in.Firstname, Lastname: in.Lastname, Street: in.Street,
					City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
					Telephone: in.Telephone, Region: in.Region, RegionID: in.RegionID,
					DefaultBilling: in.DefaultBilling, DefaultShipping: in.DefaultShipping,
				})
				if err != nil {
					return nil, customerAddressUpdateOutput{}, err
				}
				return nil, customerAddressUpdateOutput{Address: *address}, nil
			})
		return nil
	}
	return t
}

const (
	bulkLinkToolName    = "magento.product.links.bulk"
	bulkLinkToolTitle   = "Bulk change Magento product links"
	bulkLinkDescription = "Magento write operation: adds (POST /rest/{store_code}/V1/products/{sku}/links) or removes " +
		"(DELETE /rest/{store_code}/V1/products/{sku}/links/{type}/{linkedSku}) one link type (related, upsell, crosssell) for up to 50 target SKUs " +
		"on one product, one call per target, with a per-target partial-success report. Earlier changes are not rolled back. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	bulkLinkSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"], "description": "Relation kind: related (also bought), upsell (upgrade) or crosssell (add-on)."},
		"action": {"type": "string", "enum": ["add", "remove"], "description": "add creates the relations, remove deletes them. A remove for a relation that does not exist fails."},
		"linked_skus": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string", "pattern": "` + skuInputPattern + `"}, "description": "Products to relate to sku. Each must already exist; the whole batch is one call."}
	}, "required": ["instance", "sku", "link_type", "action", "linked_skus"], "additionalProperties": false}`
)

type bulkLinkInput struct {
	Instance   string   `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU        string   `json:"sku" jsonschema:"Product SKU."`
	LinkType   string   `json:"link_type" jsonschema:"Relation kind: related, upsell or crosssell."`
	Action     string   `json:"action" jsonschema:"create, update or skipped."`
	LinkedSKUs []string `json:"linked_skus"`
}

type bulkLinkOutput struct {
	Result LinkBulkResult `json:"result" jsonschema:"The operation result."`
}

func newBulkLinkTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkLinkToolName, bulkLinkToolTitle, bulkLinkDescription, "magento.product.write")
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(bulkLinkToolName, bulkLinkToolTitle, bulkLinkDescription, bulkLinkSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in bulkLinkInput) (*mcp.CallToolResult, bulkLinkOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, bulkLinkOutput{}, err
				}
				result, err := svc.BulkChangeLinks(ctx, in.SKU, in.LinkType, in.Action, in.LinkedSKUs)
				if err != nil {
					return nil, bulkLinkOutput{}, err
				}
				return nil, bulkLinkOutput{Result: *result}, nil
			})
		return nil
	}
	return t
}

const (
	bulkOptionCreateToolName    = "magento.attributes.options.bulk_create"
	bulkOptionCreateToolTitle   = "Bulk create Magento attribute options"
	bulkOptionCreateDescription = "Magento write operation: adds up to 50 option labels to one select or multiselect attribute " +
		"(POST /rest/{store_code}/V1/products/attributes/{code}/options, one call per label) with a per-label partial-success report. " +
		"Duplicate labels are skipped; earlier options stay on failure. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	bulkOptionCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `", "description": "Attribute code such as color. It is the code, not the attribute id or label."},
		"labels": {"type": "array", "minItems": 1, "maxItems": 50, "description": "Option labels to create, one option per label.", "items": {"type": "string", "minLength": 1, "maxLength": 255}}
	}, "required": ["instance", "attribute_code", "labels"], "additionalProperties": false}`
)

type bulkOptionCreateInput struct {
	Instance      string   `json:"instance" jsonschema:"Configured instance id the result came from."`
	AttributeCode string   `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
	Labels        []string `json:"labels"`
}

type bulkOptionCreateOutput struct {
	Result OptionBulkResult `json:"result" jsonschema:"The operation result."`
}

func newBulkOptionCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkOptionCreateToolName, bulkOptionCreateToolTitle, bulkOptionCreateDescription, "magento.attribute.write")
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(bulkOptionCreateToolName, bulkOptionCreateToolTitle, bulkOptionCreateDescription, bulkOptionCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in bulkOptionCreateInput) (*mcp.CallToolResult, bulkOptionCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, bulkOptionCreateOutput{}, err
				}
				result, err := svc.BulkCreateAttributeOptions(ctx, in.AttributeCode, in.Labels)
				if err != nil {
					return nil, bulkOptionCreateOutput{}, err
				}
				return nil, bulkOptionCreateOutput{Result: *result}, nil
			})
		return nil
	}
	return t
}

const (
	configurableUpdateToolName    = "magento.configurable.update"
	configurableUpdateToolTitle   = "Update a Magento configurable product"
	configurableUpdateDescription = "Magento write operation: updates a configurable parent's name, price, status, visibility, attributes, SEO fields, " +
		"pricing fields, or assignments via the product update endpoint (PUT /rest/{store_code}/V1/products/{sku}). " +
		"Refuses SKUs whose type is not configurable. The instance input is required and must name a configured Magento instance. " +
		"Requires human confirmation."
)

func newConfigurableUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(configurableUpdateToolName, configurableUpdateToolTitle, configurableUpdateDescription, capabilityConfigurableWrite)
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(configurableUpdateToolName, configurableUpdateToolTitle, configurableUpdateDescription, productUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productUpdateInput) (*mcp.CallToolResult, productWriteOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				product, err := svc.UpdateConfigurable(ctx, in.SKU, in.toServiceInput())
				if err != nil {
					return nil, productWriteOutput{}, err
				}
				return nil, productWriteOutput{Product: *product}, nil
			})
		return nil
	}
	return t
}

const (
	invoiceEmailToolName    = "magento.invoice.email"
	shipmentEmailToolName   = "magento.shipment.email"
	creditmemoEmailToolName = "magento.creditmemo.email"

	invoiceEmailDescription = "Magento write operation: sends the invoice email (POST /rest/{store_code}/V1/invoices/{id}/emails) on the resolved instance. " +
		"The recipient is the order's customer email; the gateway never accepts an address. The instance input is required and must name a configured Magento instance. Requires human confirmation."
	shipmentEmailDescription = "Magento write operation: sends the shipment email (POST /rest/{store_code}/V1/shipment/{id}/emails) on the resolved instance. " +
		"The recipient is the order's customer email; the gateway never accepts an address. The instance input is required and must name a configured Magento instance. Requires human confirmation."
	creditmemoEmailDescription = "Magento write operation: sends the credit memo email (POST /rest/{store_code}/V1/creditmemo/{id}/emails) on the resolved instance. " +
		"The recipient is the order's customer email; the gateway never accepts an address. The instance input is required and must name a configured Magento instance. Requires human confirmation."

	documentEmailSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"document_id": {"type": "integer", "minimum": 1, "description": "Document entity id of the invoice, shipment or credit memo to email."}
	}, "required": ["instance", "document_id"], "additionalProperties": false}`
)

type documentEmailInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	DocumentID int    `json:"document_id" jsonschema:"Document entity id that was emailed."`
}

type documentEmailOutput struct {
	DocumentID int    `json:"document_id" jsonschema:"Document entity id that was emailed."`
	Kind       string `json:"kind" jsonschema:"Kind of the entry."`
	Sent       bool   `json:"sent" jsonschema:"True when the store accepted the request to send."`
}

func newDocumentEmailTools(lookup serviceLookup) []registry.Tool {
	return []registry.Tool{
		newDocumentEmailTool(lookup, invoiceEmailToolName, "Send a Magento invoice email", invoiceEmailDescription, "invoice"),
		newDocumentEmailTool(lookup, shipmentEmailToolName, "Send a Magento shipment email", shipmentEmailDescription, "shipment"),
		newDocumentEmailTool(lookup, creditmemoEmailToolName, "Send a Magento credit memo email", creditmemoEmailDescription, "creditmemo"),
	}
}

func newDocumentEmailTool(lookup serviceLookup, name, title, description, kind string) registry.Tool {
	t := writeToolMetadata(name, title, description, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(name, title, description, documentEmailSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in documentEmailInput) (*mcp.CallToolResult, documentEmailOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, documentEmailOutput{}, err
				}
				if err := svc.SendDocumentEmail(ctx, kind, in.DocumentID); err != nil {
					return nil, documentEmailOutput{}, err
				}
				return nil, documentEmailOutput{DocumentID: in.DocumentID, Kind: kind, Sent: true}, nil
			})
		return nil
	}
	return t
}

const (
	inventorySourceCreateToolName    = "magento.inventory.source_create"
	inventorySourceCreateToolTitle   = "Create a Magento inventory source"
	inventorySourceCreateDescription = "Magento write operation: creates an MSI source " +
		"(POST /rest/{store_code}/V1/inventory/sources) on the resolved instance with code, name, enabled flag, and location. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."
	stockSourceLinkToolName    = "magento.inventory.stock_source_link"
	stockSourceLinkToolTitle   = "Link a source to a Magento stock"
	stockSourceLinkDescription = "Magento write operation: attaches an MSI source to a stock " +
		"(POST /rest/{store_code}/V1/inventory/stock-source-links) on the resolved instance with a position. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	inventorySourceCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"source_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Inventory source code, for example default."},
		"name": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display name."},
		"enabled": {"type": "boolean", "default": true},
		"country_id": {"type": "string", "pattern": "^[A-Z]{2}$"},
		"region": {"type": "string", "maxLength": 255},
		"city": {"type": "string", "maxLength": 255},
		"postcode": {"type": "string", "maxLength": 255},
		"latitude": {"type": "number", "minimum": -90, "maximum": 90},
		"longitude": {"type": "number", "minimum": -180, "maximum": 180}
	}, "required": ["instance", "source_code", "name"], "additionalProperties": false}`

	stockSourceLinkSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"stock_id": {"type": "integer", "minimum": 1, "description": "Stock id the source is attached to. The default stock is 1."},
		"source_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Inventory source code to attach or detach, for example default."},
		"position": {"type": "integer", "minimum": 0, "description": "Priority of the source within the stock; lower is used first."}
	}, "required": ["instance", "stock_id", "source_code"], "additionalProperties": false}`
)

type inventorySourceCreateInput struct {
	Instance   string  `json:"instance" jsonschema:"Configured instance id the result came from."`
	SourceCode string  `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Name       string  `json:"name" jsonschema:"Display name."`
	Enabled    bool    `json:"enabled" jsonschema:"True when the entry is enabled."`
	CountryID  string  `json:"country_id" jsonschema:"ISO 3166-1 alpha-2 country code, uppercase."`
	Region     string  `json:"region" jsonschema:"Region name."`
	City       string  `json:"city" jsonschema:"City or locality."`
	Postcode   string  `json:"postcode" jsonschema:"Postal code as the destination country writes it."`
	Latitude   float64 `json:"latitude" jsonschema:"Latitude of the source."`
	Longitude  float64 `json:"longitude" jsonschema:"Longitude of the source."`
}

type inventorySourceCreateOutput struct {
	Source InventorySource `json:"source" jsonschema:"Where the value came from."`
}

type stockSourceLinkInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	StockID    int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Position   int    `json:"position" jsonschema:"Sort position among siblings."`
}

type stockSourceLinkOutput struct {
	StockID    int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Linked     bool   `json:"linked" jsonschema:"True when the child is now linked to the parent."`
}

func newInventorySourceCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(inventorySourceCreateToolName, inventorySourceCreateToolTitle, inventorySourceCreateDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(inventorySourceCreateToolName, inventorySourceCreateToolTitle, inventorySourceCreateDescription, inventorySourceCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in inventorySourceCreateInput) (*mcp.CallToolResult, inventorySourceCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, inventorySourceCreateOutput{}, err
				}
				source, err := svc.CreateSource(ctx, InventorySourceInput{
					SourceCode: in.SourceCode, Name: in.Name, Enabled: in.Enabled,
					CountryID: in.CountryID, Region: in.Region, City: in.City, Postcode: in.Postcode,
					Latitude: in.Latitude, Longitude: in.Longitude,
				})
				if err != nil {
					return nil, inventorySourceCreateOutput{}, err
				}
				return nil, inventorySourceCreateOutput{Source: *source}, nil
			})
		return nil
	}
	return t
}

func newStockSourceLinkTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(stockSourceLinkToolName, stockSourceLinkToolTitle, stockSourceLinkDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(stockSourceLinkToolName, stockSourceLinkToolTitle, stockSourceLinkDescription, stockSourceLinkSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in stockSourceLinkInput) (*mcp.CallToolResult, stockSourceLinkOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, stockSourceLinkOutput{}, err
				}
				if err := svc.LinkStockSource(ctx, in.StockID, in.SourceCode, in.Position); err != nil {
					return nil, stockSourceLinkOutput{}, err
				}
				return nil, stockSourceLinkOutput{StockID: in.StockID, SourceCode: in.SourceCode, Linked: true}, nil
			})
		return nil
	}
	return t
}

const (
	stockSourceUnlinkToolName    = "magento.inventory.stock_source_unlink"
	stockSourceUnlinkToolTitle   = "Unlink a source from a Magento stock"
	stockSourceUnlinkDescription = "Magento write operation: detaches an MSI source from a stock " +
		"(POST /rest/{store_code}/V1/inventory/stock-source-links-delete) on the resolved instance. Products keep their source items; " +
		"the stock stops selling from that source. Destructive and confirmation-gated. " +
		"The instance input is required and must name a configured Magento instance."

	stockSourceUnlinkSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"stock_id": {"type": "integer", "minimum": 1, "description": "Inventory stock id. The default stock is 1."},
		"source_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Inventory source code, for example default."}
	}, "required": ["instance", "stock_id", "source_code"], "additionalProperties": false}`
)

type stockSourceUnlinkInput struct {
	Instance   string `json:"instance" jsonschema:"Configured instance id the result came from."`
	StockID    int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
}

type stockSourceUnlinkOutput struct {
	StockID    int    `json:"stock_id" jsonschema:"Inventory stock id; the default stock is 1."`
	SourceCode string `json:"source_code" jsonschema:"Inventory source code, for example default."`
	Unlinked   bool   `json:"unlinked" jsonschema:"True when the child was removed from the parent."`
}

func newStockSourceUnlinkTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(stockSourceUnlinkToolName, stockSourceUnlinkToolTitle, stockSourceUnlinkDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: stockSourceUnlinkToolName, Title: stockSourceUnlinkToolTitle, Description: discloseWrite(stockSourceUnlinkToolName, stockSourceUnlinkDescription),
			InputSchema: json.RawMessage(stockSourceUnlinkSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(stockSourceUnlinkToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in stockSourceUnlinkInput) (*mcp.CallToolResult, stockSourceUnlinkOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, stockSourceUnlinkOutput{}, err
			}
			if err := svc.UnlinkStockSource(ctx, in.StockID, in.SourceCode); err != nil {
				return nil, stockSourceUnlinkOutput{}, err
			}
			return nil, stockSourceUnlinkOutput{StockID: in.StockID, SourceCode: in.SourceCode, Unlinked: true}, nil
		})
		return nil
	}
	return t
}

const (
	createVariationsToolName    = "magento.configurable.create_variations"
	createVariationsToolTitle   = "Create configurable variations"
	createVariationsDescription = "Magento write operation: creates simple children for an existing configurable parent and links them " +
		"(POST /rest/{store_code}/V1/products then POST /rest/{store_code}/V1/configurable-products/{sku}/child) on the resolved instance. " +
		"Existing SKUs are skipped, so the call is idempotent per SKU; failures are reported per row and earlier children are not rolled back. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	createVariationsSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the configurable parent product."},
		"variants": {"type": "array", "minItems": 1, "maxItems": 50, "description": "Variants to create under the parent. Each needs the variant attribute values and a unique SKU.", "items": {"type": "object",
			"additionalProperties": false, "required": ["sku", "attributes"],
			"properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU."},
				"attributes": {"type": "object", "minProperties": 1, "additionalProperties": {"type": "string", "minLength": 1, "maxLength": 255}}}}}
	}, "required": ["instance", "parent_sku", "variants"], "additionalProperties": false}`
)

type variationWire struct {
	SKU        string            `json:"sku" jsonschema:"Product SKU."`
	Name       string            `json:"name" jsonschema:"Display name."`
	Attributes map[string]string `json:"attributes" jsonschema:"Attributes of the requested attribute set."`
}

type createVariationsInput struct {
	Instance  string          `json:"instance" jsonschema:"Configured instance id the result came from."`
	ParentSKU string          `json:"parent_sku" jsonschema:"SKU of the configurable parent product."`
	Variants  []variationWire `json:"variants"`
}

type createVariationsOutput struct {
	Result VariationResult `json:"result" jsonschema:"The operation result."`
}

func newCreateVariationsTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(createVariationsToolName, createVariationsToolTitle, createVariationsDescription, capabilityConfigurableWrite)
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(createVariationsToolName, createVariationsToolTitle, createVariationsDescription, createVariationsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in createVariationsInput) (*mcp.CallToolResult, createVariationsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, createVariationsOutput{}, err
				}
				variants := make([]VariationInput, 0, len(in.Variants))
				for _, variant := range in.Variants {
					variants = append(variants, VariationInput(variant))
				}
				result, err := svc.CreateVariations(ctx, in.ParentSKU, variants)
				if err != nil {
					return nil, createVariationsOutput{}, err
				}
				return nil, createVariationsOutput{Result: *result}, nil
			})
		return nil
	}
	return t
}

const (
	setConfigurableOptionsToolName    = "magento.configurable.set_options"
	setConfigurableOptionsToolTitle   = "Set configurable variant options"
	setConfigurableOptionsDescription = "Magento write operation: declares the variant attributes and value ordering of an existing configurable parent " +
		"(POST /rest/{store_code}/V1/configurable-products/{sku}/options, one option per call) on the resolved instance. " +
		"Earlier options stay if a later one fails. The instance input is required and must name a configured Magento instance. " +
		"Requires human confirmation."

	setConfigurableOptionsSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "SKU of the configurable product whose variant options are replaced."},
		"options": {"type": "array", "minItems": 1, "maxItems": 10, "description": "Variant attributes the parent offers. This replaces the existing set.", "items": {"type": "object",
			"additionalProperties": false, "required": ["attribute_id", "label", "values"],
			"properties": {"attribute_id": {"type": "integer", "minimum": 1},
				"label": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Label shown to the shopper."},
				"position": {"type": "integer", "minimum": 0, "maximum": 1000},
				"values": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "integer", "minimum": 1, "description": "Requested page size."}}}}}
	}, "required": ["instance", "sku", "options"], "additionalProperties": false}`
)

type configurableOptionWire struct {
	AttributeID int    `json:"attribute_id"`
	Label       string `json:"label" jsonschema:"Label shown to the shopper."`
	Position    int    `json:"position" jsonschema:"Sort position among siblings."`
	Values      []int  `json:"values" jsonschema:"Selectable values of this option."`
}

type setConfigurableOptionsInput struct {
	Instance string                   `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string                   `json:"sku" jsonschema:"Product SKU."`
	Options  []configurableOptionWire `json:"options" jsonschema:"Custom options of the product."`
}

type setConfigurableOptionsOutput struct {
	SKU     string `json:"sku" jsonschema:"Product SKU."`
	Options int    `json:"options" jsonschema:"Custom options of the product."`
	Set     bool   `json:"set" jsonschema:"True when the values were written."`
}

func newSetConfigurableOptionsTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(setConfigurableOptionsToolName, setConfigurableOptionsToolTitle, setConfigurableOptionsDescription, capabilityConfigurableWrite)
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(setConfigurableOptionsToolName, setConfigurableOptionsToolTitle, setConfigurableOptionsDescription, setConfigurableOptionsSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in setConfigurableOptionsInput) (*mcp.CallToolResult, setConfigurableOptionsOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, setConfigurableOptionsOutput{}, err
				}
				options := make([]ConfigurableOptionInput, 0, len(in.Options))
				for _, option := range in.Options {
					options = append(options, ConfigurableOptionInput(option))
				}
				if err := svc.SetConfigurableOptions(ctx, in.SKU, options); err != nil {
					return nil, setConfigurableOptionsOutput{}, err
				}
				return nil, setConfigurableOptionsOutput{SKU: in.SKU, Options: len(options), Set: true}, nil
			})
		return nil
	}
	return t
}

const (
	productOptionCreateToolName    = "magento.product.options.create"
	productOptionCreateToolTitle   = "Create a Magento product custom option"
	productOptionCreateDescription = "Magento write operation: adds one custom option to a product " +
		"(POST /rest/{store_code}/V1/products/{sku}/options) on the resolved instance. Selectable types require 1-50 values with fixed or percent pricing. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	productOptionCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"title": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Label shown to the shopper for this custom option."},
		"type": {"type": "string", "enum": ["field", "area", "file", "drop_down", "radio", "checkbox", "multiple", "date", "date_time", "time"], "description": "Input control. drop_down, radio, checkbox and multiple take values; the others do not."},
		"is_require": {"type": "boolean", "default": false},
		"sort_order": {"type": "integer", "minimum": 0},
		"values": {"type": "array", "maxItems": 50, "items": {"type": "object", "additionalProperties": false,
			"required": ["title"], "properties": {"title": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display title for this entry."},
				"price": {"type": "number", "minimum": 0},
				"price_type": {"type": "string", "enum": ["fixed", "percent"]},
				"sku": {"type": "string", "maxLength": 64, "description": "Product SKU."},
				"sort_order": {"type": "integer", "minimum": 0}}}}
	}, "required": ["instance", "sku", "title", "type"], "additionalProperties": false}`
)

type customOptionValueWire struct {
	Title     string  `json:"title" jsonschema:"Display title for this entry."`
	Price     float64 `json:"price"`
	PriceType string  `json:"price_type"`
	SKU       string  `json:"sku" jsonschema:"Product SKU."`
	SortOrder int     `json:"sort_order" jsonschema:"Sort position among siblings."`
}

type productOptionCreateInput struct {
	Instance  string                  `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU       string                  `json:"sku" jsonschema:"Product SKU."`
	Title     string                  `json:"title" jsonschema:"Display title for this entry."`
	Type      string                  `json:"type" jsonschema:"Input control type."`
	IsRequire bool                    `json:"is_require" jsonschema:"True when the shopper must choose this option."`
	SortOrder int                     `json:"sort_order" jsonschema:"Sort position among siblings."`
	Values    []customOptionValueWire `json:"values" jsonschema:"Selectable values of this option."`
}

type productOptionCreateOutput struct {
	Option ProductOption `json:"option" jsonschema:"The option that was created or read."`
}

func newProductOptionCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productOptionCreateToolName, productOptionCreateToolTitle, productOptionCreateDescription, "magento.product.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productOptionCreateToolName, productOptionCreateToolTitle, productOptionCreateDescription, productOptionCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productOptionCreateInput) (*mcp.CallToolResult, productOptionCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productOptionCreateOutput{}, err
				}
				values := make([]CustomOptionValueInput, 0, len(in.Values))
				for _, value := range in.Values {
					values = append(values, CustomOptionValueInput(value))
				}
				option, err := svc.CreateProductOption(ctx, in.SKU, CustomOptionInput{
					Title: in.Title, Type: in.Type, IsRequire: in.IsRequire, SortOrder: in.SortOrder, Values: values,
				})
				if err != nil {
					return nil, productOptionCreateOutput{}, err
				}
				return nil, productOptionCreateOutput{Option: *option}, nil
			})
		return nil
	}
	return t
}

const (
	productOptionUpdateToolName    = "magento.product.options.update"
	productOptionUpdateToolTitle   = "Update a Magento product custom option"
	productOptionUpdateDescription = "Magento write operation: updates one custom option " +
		"(PUT /rest/{store_code}/V1/products/{sku}/options/{optionId}) on the resolved instance. Selectable types require 1-50 values with fixed or percent pricing. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."
	productOptionDeleteToolName    = "magento.product.options.delete"
	productOptionDeleteToolTitle   = "Delete a Magento product custom option"
	productOptionDeleteDescription = "Magento write operation: deletes one custom option " +
		"(DELETE /rest/{store_code}/V1/products/{sku}/options/{optionId}) on the resolved instance. Irreversible; existing selections in carts are not restored. " +
		"The instance input is required and must name a configured Magento instance. Requires human confirmation."

	productOptionUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"option_id": {"type": "integer", "minimum": 1, "description": "Custom option id from product.options, or the attribute option id for attribute tools."},
		"title": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display title for this entry."},
		"type": {"type": "string", "enum": ["field", "area", "file", "drop_down", "radio", "checkbox", "multiple", "date", "date_time", "time"], "description": "Input control. drop_down, radio, checkbox and multiple take values; the others do not."},
		"is_require": {"type": "boolean", "default": false},
		"sort_order": {"type": "integer", "minimum": 0},
		"values": {"type": "array", "maxItems": 50, "items": {"type": "object", "additionalProperties": false,
			"required": ["title"], "properties": {"title": {"type": "string", "minLength": 1, "maxLength": 255, "description": "Display title for this entry."},
				"price": {"type": "number", "minimum": 0},
				"price_type": {"type": "string", "enum": ["fixed", "percent"]},
				"sku": {"type": "string", "maxLength": 64, "description": "Product SKU."},
				"sort_order": {"type": "integer", "minimum": 0}}}}
	}, "required": ["instance", "sku", "option_id", "title", "type"], "additionalProperties": false}`

	productOptionDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"option_id": {"type": "integer", "minimum": 1, "description": "Custom option id from product.options, or the attribute option id for attribute tools."}
	}, "required": ["instance", "sku", "option_id"], "additionalProperties": false}`
)

type productOptionUpdateInput struct {
	Instance  string                  `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU       string                  `json:"sku" jsonschema:"Product SKU."`
	OptionID  int                     `json:"option_id" jsonschema:"Custom option id."`
	Title     string                  `json:"title" jsonschema:"Display title for this entry."`
	Type      string                  `json:"type" jsonschema:"Input control type."`
	IsRequire bool                    `json:"is_require" jsonschema:"True when the shopper must choose this option."`
	SortOrder int                     `json:"sort_order" jsonschema:"Sort position among siblings."`
	Values    []customOptionValueWire `json:"values" jsonschema:"Selectable values of this option."`
}

type productOptionUpdateOutput struct {
	Option ProductOption `json:"option" jsonschema:"The option that was created or read."`
}

type productOptionDeleteInput struct {
	Instance string `json:"instance" jsonschema:"Configured instance id the result came from."`
	SKU      string `json:"sku" jsonschema:"Product SKU."`
	OptionID int    `json:"option_id" jsonschema:"Custom option id."`
}

type productOptionDeleteOutput struct {
	SKU      string `json:"sku" jsonschema:"Product SKU."`
	OptionID int    `json:"option_id" jsonschema:"Custom option id."`
	Deleted  bool   `json:"deleted" jsonschema:"True when the entry was removed."`
}

func newProductOptionUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productOptionUpdateToolName, productOptionUpdateToolTitle, productOptionUpdateDescription, "magento.product.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, mcpWriteTool(productOptionUpdateToolName, productOptionUpdateToolTitle, productOptionUpdateDescription, productOptionUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in productOptionUpdateInput) (*mcp.CallToolResult, productOptionUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, productOptionUpdateOutput{}, err
				}
				values := make([]CustomOptionValueInput, 0, len(in.Values))
				for _, value := range in.Values {
					values = append(values, CustomOptionValueInput(value))
				}
				option, err := svc.UpdateProductOption(ctx, in.SKU, in.OptionID, CustomOptionInput{
					Title: in.Title, Type: in.Type, IsRequire: in.IsRequire, SortOrder: in.SortOrder, Values: values,
				})
				if err != nil {
					return nil, productOptionUpdateOutput{}, err
				}
				return nil, productOptionUpdateOutput{Option: *option}, nil
			})
		return nil
	}
	return t
}

func newProductOptionDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(productOptionDeleteToolName, productOptionDeleteToolTitle, productOptionDeleteDescription, "magento.product.write")
	t.Register = func(srv *mcp.Server) error {
		toolerr.Add(srv, &mcp.Tool{
			Name: productOptionDeleteToolName, Title: productOptionDeleteToolTitle, Description: discloseWrite(productOptionDeleteToolName, productOptionDeleteDescription),
			InputSchema: json.RawMessage(productOptionDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: writeIdempotentHint(productOptionDeleteToolName), DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in productOptionDeleteInput) (*mcp.CallToolResult, productOptionDeleteOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, productOptionDeleteOutput{}, err
			}
			if err := svc.DeleteProductOption(ctx, in.SKU, in.OptionID); err != nil {
				return nil, productOptionDeleteOutput{}, err
			}
			return nil, productOptionDeleteOutput{SKU: in.SKU, OptionID: in.OptionID, Deleted: true}, nil
		})
		return nil
	}
	return t
}
