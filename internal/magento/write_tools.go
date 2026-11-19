// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	instanceResolutionNote    = " The instance input is resolved by the server when only one Magento instance is configured."

	productCreateToolDescription = "Creates one product in the configured Magento 2 REST catalog " +
		"(POST /rest/{store_code}/V1/products) using the integration credential. " +
		"This is a write operation: it changes catalog data, is not idempotent, and a retry can create a duplicate SKU. " +
		"When quantity is supplied the product is created first and its default-source inventory is assigned afterwards, " +
		"so an inventory failure leaves the product created and the error carries the SKU and stage in its details." +
		instanceResolutionNote
	productUpdateToolDescription = "Updates one product in the configured Magento 2 REST catalog " +
		"(PUT /rest/{store_code}/V1/products/{sku}) using the integration credential. " +
		"This is a write operation: it changes catalog data and is not idempotent as an agent action. " +
		"Only supplied fields are sent, so Magento's merge semantics preserve omitted attributes; " +
		"at least one of name, price, status, visibility, or attributes is required." +
		instanceResolutionNote
	configurableCreateToolDescription = "Creates a configurable product in the configured Magento 2 REST catalog using the integration credential: " +
		"each child simple product is created (POST /rest/{store_code}/V1/products), then the parent with type_id=configurable " +
		"and configurable_product_options, then one child link per child (POST /rest/{store_code}/V1/configurable-products/{sku}/child). " +
		"This is a write operation and it is NOT atomic: a failure leaves every completed write in place and reports " +
		"stage and completed_skus in the error details, so the caller must run compensation." +
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
		"At most 100 items are accepted per call." +
		instanceResolutionNote

	instanceProp         = `"instance": {"type": "string", "pattern": "` + instancePattern + `", "description": "Configured Magento instance id; resolved by the server when only one instance is configured."}`
	confirmationProp     = `"confirmation_id": {"type": "string", "maxLength": 128, "description": "Approval id from a confirmation_required response; resend the same arguments with this id to execute."}`
	skuProp              = `"sku": {"type": "string", "pattern": "` + skuInputPattern + `", "description": "Product SKU, 1-64 characters from [A-Za-z0-9._:-]."}`
	nameProp             = `"name": {"type": "string", "minLength": 1, "maxLength": 255}`
	setProp              = `"attribute_set_id": {"type": "integer", "minimum": 1}`
	typeIDProp           = `"type_id": {"type": "string", "enum": ["simple", "virtual", "downloadable", "bundle"]}`
	priceProp            = `"price": {"type": "string", "pattern": "` + priceInputPattern + `", "description": "Decimal price string, up to 4 fraction digits."}`
	statusProp           = `"status": {"type": "integer", "enum": [1, 2]}`
	visibilityProp       = `"visibility": {"type": "integer", "enum": [1, 2, 3, 4]}`
	websiteIDsProp       = `"website_ids": {"type": "array", "items": {"type": "integer", "minimum": 1}, "minItems": 1, "maxItems": 50}`
	categoryIDsProp      = `"category_ids": {"type": "array", "items": {"type": "integer", "minimum": 1}, "maxItems": 500}`
	attributesProp       = `"attributes": {"type": "object", "additionalProperties": {"type": "string", "maxLength": 255}, "description": "Custom EAV attributes keyed by attribute code."}`
	childAttributesProp  = `"attributes": {"type": "object", "minProperties": 1, "additionalProperties": {"type": "string", "maxLength": 255}}`
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
	productUpdateSchema        = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `, ` + nameProp + `, ` + priceProp + `, ` + statusProp + `, ` + visibilityProp + `, ` + attributesProp + `, ` + seoProps + `, ` + pricingProps + `, "category_ids": {"type": "array", "maxItems": 500, "items": {"type": "integer", "minimum": 1}, "description": "Replaces the product's category assignments when provided; an empty array clears them."}, "website_ids": {"type": "array", "maxItems": 50, "items": {"type": "integer", "minimum": 1}, "description": "Replaces the product's website assignments when provided."}}, "required": ["instance", "sku"], "additionalProperties": false}`
	configurableCreateSchema   = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, "parent": {"type": "object", "additionalProperties": false, "required": ["sku", "name", "attribute_set_id", "price"], "properties": {` + skuProp + `, ` + nameProp + `, ` + setProp + `, ` + priceProp + `, ` + statusProp + `, ` + visibilityProp + `, ` + websiteIDsProp + `, ` + categoryIDsProp + `, ` + attributesProp + `, ` + seoProps + `, ` + pricingProps + `}}, "variant_attributes": {"type": "array", "minItems": 1, "maxItems": 5, "items": {"type": "object", "additionalProperties": false, "required": ["attribute_id", "code", "values"], "properties": {"attribute_id": {"type": "integer", "minimum": 1}, "code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `"}, "values": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object", "additionalProperties": false, "required": ["value_index"], "properties": {"value_index": {"type": "integer", "minimum": 1}, "label": {"type": "string", "maxLength": 255}}}}}}}, "children": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "object", "additionalProperties": false, "required": ["sku", "attributes"], "properties": {` + skuProp + `, ` + childAttributesProp + `, ` + priceProp + `, ` + quantityProp + `, ` + seoProps + `}}}}, "required": ["instance", "parent", "variant_attributes", "children"], "additionalProperties": false}`
	configurableAddChildSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, "parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `"}, "child_sku": {"type": "string", "pattern": "` + skuInputPattern + `"}}, "required": ["instance", "parent_sku", "child_sku"], "additionalProperties": false}`
	inventoryAssignSchema      = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, "items": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object", "additionalProperties": false, "required": ["sku", "source_code", "quantity", "status"], "properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `"}, "source_code": {"type": "string", "pattern": "` + sourceCodeInputPattern + `"}, "quantity": {"type": "integer", "minimum": 0}, "status": {"type": "integer", "enum": [0, 1], "description": "1 in stock, 0 out of stock."}}}}}, "required": ["instance", "items"], "additionalProperties": false}`
)

type productCreateInput struct {
	Instance       string            `json:"instance"`
	SKU            string            `json:"sku"`
	Name           string            `json:"name"`
	AttributeSetID int               `json:"attribute_set_id"`
	TypeID         string            `json:"type_id"`
	Price          string            `json:"price"`
	Status         int               `json:"status"`
	Visibility     int               `json:"visibility"`
	WebsiteIDs     []int             `json:"website_ids"`
	CategoryIDs    []int             `json:"category_ids"`
	Attributes     map[string]string `json:"attributes"`
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
	Instance   string            `json:"instance"`
	SKU        string            `json:"sku"`
	Name       *string           `json:"name"`
	Price      *string           `json:"price"`
	Status     *int              `json:"status"`
	Visibility *int              `json:"visibility"`
	Attributes map[string]string `json:"attributes"`

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
	Values      []variantValueInput `json:"values"`
}

type variantValueInput struct {
	ValueIndex int    `json:"value_index"`
	Label      string `json:"label"`
}

type childProductInput struct {
	SKU        string            `json:"sku"`
	Attributes map[string]string `json:"attributes"`
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
	Instance          string                  `json:"instance"`
	Parent            productCreateInput      `json:"parent"`
	VariantAttributes []variantAttributeInput `json:"variant_attributes"`
	Children          []childProductInput     `json:"children"`
}

type configurableAddChildInput struct {
	Instance  string `json:"instance"`
	ParentSKU string `json:"parent_sku"`
	ChildSKU  string `json:"child_sku"`
}

type inventoryAssignInput struct {
	Instance string            `json:"instance"`
	Items    []sourceItemInput `json:"items"`
}

type sourceItemInput struct {
	SKU        string `json:"sku"`
	SourceCode string `json:"source_code"`
	Quantity   int    `json:"quantity"`
	Status     int    `json:"status"`
}

type productWriteOutput struct {
	Product Product `json:"product"`
}

type configurableCreateOutput struct {
	Parent   Product   `json:"parent"`
	Children []Product `json:"children"`
}

type configurableAddChildOutput struct {
	ParentSKU string `json:"parent_sku"`
	ChildSKU  string `json:"child_sku"`
	Linked    bool   `json:"linked"`
}

type inventoryAssignOutput struct {
	Assigned int `json:"assigned"`
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
		newConfigurableUpdateTool(lookup),
	}
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
		productCreateToolName:         productCreateSchema,
		productUpdateToolName:         productUpdateSchema,
		productDeleteToolName:         productDeleteSchema,
		configurableCreateToolName:    configurableCreateSchema,
		configurableAddChildToolName:  configurableAddChildSchema,
		inventoryAssignToolName:       inventoryAssignSchema,
		importExecuteToolName:         importExecuteSchema,
		mediaUploadToolName:           mediaUploadSchema,
		mediaDeleteToolName:           mediaDeleteSchema,
		invoiceCreateToolName:         invoiceCreateSchema,
		invoiceCaptureToolName:        invoiceCaptureSchema,
		shipmentCreateToolName:        shipmentCreateSchema,
		creditmemoCreateToolName:      creditmemoCreateSchema,
		orderCommentToolName:          orderCommentSchema,
		orderHoldToolName:             orderHoldSchema,
		orderUnholdToolName:           orderHoldSchema,
		orderEmailToolName:            orderEmailSchema,
		orderCreateToolName:           orderCreateSchema,
		categoryCreateToolName:        categoryCreateSchema,
		categoryUpdateToolName:        categoryUpdateSchema,
		categoryAssignToolName:        categoryAssignSchema,
		removeChildToolName:           removeChildSchema,
		unassignSourceItemsToolName:   unassignSourceItemsSchema,
		productLinkAddToolName:        productLinkAddSchema,
		productLinkRemoveToolName:     productLinkRemoveSchema,
		tierPricesSetToolName:         tierPricesSetSchema,
		inventoryTransferToolName:     inventoryTransferSchema,
		customerCreateToolName:        customerCreateSchema,
		customerUpdateToolName:        customerUpdateSchema,
		mediaUpdateToolName:           mediaUpdateSchema,
		orderCancelToolName:           orderCancelSchema,
		attributeOptionCreateToolName: attributeOptionCreateSchema,
		attributeOptionDeleteToolName: attributeOptionDeleteSchema,
		bulkProductUpdateToolName:     bulkProductUpdateSchema,
		categoryDeleteToolName:        categoryDeleteSchema,
		customerAddressCreateToolName: customerAddressCreateSchema,
		customerAddressDeleteToolName: customerAddressDeleteSchema,
		customerAddressUpdateToolName: customerAddressUpdateSchema,
		bulkLinkToolName:              bulkLinkSchema,
		bulkOptionCreateToolName:      bulkOptionCreateSchema,
		configurableUpdateToolName:    productUpdateSchema,
	}
}

func writeToolMetadata(name, title, description, capability string) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: description,
		Capability: capability, Permission: registry.PermissionWrite, Timeout: writeToolTimeout,
		RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveHint(),
		},
	}
}

func mcpWriteTool(name, title, description, schema string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Title: title, Description: description,
		InputSchema: json.RawMessage(schema),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveHint(),
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
		mcp.AddTool(srv, mcpWriteTool(productCreateToolName, productCreateToolTitle, productCreateToolDescription, productCreateSchema),
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
		mcp.AddTool(srv, mcpWriteTool(productUpdateToolName, productUpdateToolTitle, productUpdateToolDescription, productUpdateSchema),
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
		mcp.AddTool(srv, mcpWriteTool(configurableCreateToolName, configurableCreateToolTitle, configurableCreateToolDescription, configurableCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in configurableCreateInput) (*mcp.CallToolResult, configurableCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, configurableCreateOutput{}, err
				}
				result, err := svc.CreateConfigurable(ctx, in.toServiceInput())
				if err != nil {
					return nil, configurableCreateOutput{}, err
				}
				return nil, configurableCreateOutput{Parent: result.Parent, Children: result.Children}, nil
			})
		return nil
	}
	return t
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
	return CreateConfigurableInput{Parent: in.Parent.toCreateInput(), Variants: variants, Children: children}
}

func newConfigurableAddChildTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(configurableAddChildToolName, configurableAddChildToolTitle, configurableAddChildToolDescription, capabilityConfigurableWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(configurableAddChildToolName, configurableAddChildToolTitle, configurableAddChildToolDescription, configurableAddChildSchema),
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
		mcp.AddTool(srv, mcpWriteTool(inventoryAssignToolName, inventoryAssignToolTitle, inventoryAssignToolDescription, inventoryAssignSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in inventoryAssignInput) (*mcp.CallToolResult, inventoryAssignOutput, error) {
				items := make([]SourceItem, 0, len(in.Items))
				for _, item := range in.Items {
					items = append(items, SourceItem(item))
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
	Instance       string `json:"instance"`
	Format         string `json:"format"`
	Data           string `json:"data_base64"`
	UpdateExisting bool   `json:"update_existing"`
	ConfirmationID string `json:"confirmation_id,omitempty"`
}

type importExecuteOutput struct {
	Result *ImportResult `json:"result"`
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
		mcp.AddTool(srv, mcpWriteTool(importExecuteToolName, importExecuteToolTitle, importExecuteToolDescription, importExecuteSchema),
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
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
}

type productDeleteOutput struct {
	SKU     string `json:"sku"`
	Deleted bool   `json:"deleted"`
}

func destructiveToolMetadata(name, title, description, capability string) registry.Tool {
	return registry.Tool{
		Name: name, Title: title, Description: description,
		Capability: capability, Permission: registry.PermissionDestructive, Timeout: writeToolTimeout,
		RequiresConfirmation: true,
		Annotations: mcp.ToolAnnotations{
			ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue(),
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
		mcp.AddTool(srv, &mcp.Tool{
			Name: productDeleteToolName, Title: productDeleteToolTitle, Description: productDeleteDescription,
			InputSchema: json.RawMessage(productDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
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
		"(DELETE /rest/{store_code}/V1/inventory/source-items) on the resolved instance for at most 100 sku/source pairs. " +
		"The product and its catalog stock status are unchanged; the assignment is removed. Requires human confirmation."

	removeChildSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"parent_sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
		"child_sku": {"type": "string", "pattern": "` + skuInputPattern + `"}},
		"required": ["instance", "parent_sku", "child_sku"], "additionalProperties": false}`

	unassignSourceItemsSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"items": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object",
			"additionalProperties": false, "required": ["sku", "source_code"],
			"properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
				"source_code": {"type": "string", "pattern": "` + sourceCodeInputPattern + `"}}}}},
		"required": ["instance", "items"], "additionalProperties": false}`
)

type removeChildInput struct {
	Instance  string `json:"instance"`
	ParentSKU string `json:"parent_sku"`
	ChildSKU  string `json:"child_sku"`
}

type removeChildOutput struct {
	ParentSKU string `json:"parent_sku"`
	ChildSKU  string `json:"child_sku"`
	Unlinked  bool   `json:"unlinked"`
}

type unassignSourceItemsInput struct {
	Instance string `json:"instance"`
	Items    []struct {
		SKU        string `json:"sku"`
		SourceCode string `json:"source_code"`
	} `json:"items"`
}

type unassignSourceItemsOutput struct {
	Unassigned int `json:"unassigned"`
}

func newRemoveChildTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(removeChildToolName, removeChildToolTitle, removeChildDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(removeChildToolName, removeChildToolTitle, removeChildDescription, removeChildSchema),
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
		mcp.AddTool(srv, mcpWriteTool(unassignSourceItemsToolName, unassignSourceItemsToolTitle, unassignSourceItemsDescription, unassignSourceItemsSchema),
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
		"mime_type": {"type": "string", "enum": ["image/jpeg", "image/png", "image/webp", "image/gif"]},
		"data_base64": {"type": "string", "maxLength": 6990508, "description": "Base64 image content, at most 5 MiB decoded."},
		"label": {"type": "string", "maxLength": 255},
		"position": {"type": "integer", "minimum": 0, "maximum": 1000},
		"types": {"type": "array", "items": {"type": "string", "enum": ["image", "small_image", "thumbnail", "swatch_image"]}, "maxItems": 4}
	}, "required": ["instance", "sku", "filename", "mime_type", "data_base64"], "additionalProperties": false}`
)

type mediaUploadInput struct {
	Instance   string   `json:"instance"`
	SKU        string   `json:"sku"`
	Filename   string   `json:"filename"`
	MIMEType   string   `json:"mime_type"`
	DataBase64 string   `json:"data_base64"`
	Label      string   `json:"label"`
	Position   int      `json:"position"`
	Types      []string `json:"types"`
}

type mediaUploadOutput struct {
	Media MediaEntry `json:"media"`
}

func newMediaUploadTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(mediaUploadToolName, mediaUploadToolTitle, mediaUploadDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(mediaUploadToolName, mediaUploadToolTitle, mediaUploadDescription, mediaUploadSchema),
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
	invoiceCreateDescription = "Magento write operation: creates an invoice for an order " +
		"(POST /rest/{store_code}/V1/invoices) on the resolved instance. capture defaults to false and does not charge " +
		"the payment method; set it to true only when the charge is intended. Requires human confirmation."

	invoiceCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1, "description": "Magento order entity id."},
		"capture": {"type": "boolean", "default": false, "description": "Capture the payment; false only records the invoice."},
		"items": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object",
			"additionalProperties": false, "required": ["order_item_id", "qty"],
			"properties": {"order_item_id": {"type": "integer", "minimum": 1},
				"qty": {"type": "number", "exclusiveMinimum": 0}}}}},
		"required": ["instance", "order_id", "items"], "additionalProperties": false}`
)

type invoiceCreateInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
	Capture  bool   `json:"capture"`
	Items    []struct {
		OrderItemID int     `json:"order_item_id"`
		Qty         float64 `json:"qty"`
	} `json:"items"`
}

type invoiceCreateOutput struct {
	Invoice Invoice `json:"invoice"`
}

func newInvoiceCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(invoiceCreateToolName, invoiceCreateToolTitle, invoiceCreateDescription, "magento.invoice.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(invoiceCreateToolName, invoiceCreateToolTitle, invoiceCreateDescription, invoiceCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in invoiceCreateInput) (*mcp.CallToolResult, invoiceCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceCreateOutput{}, err
				}
				items := make([]InvoiceLine, 0, len(in.Items))
				for _, item := range in.Items {
					items = append(items, InvoiceLine{OrderItemID: item.OrderItemID, Qty: item.Qty})
				}
				invoice, err := svc.CreateInvoice(ctx, InvoiceCreateInput{OrderID: in.OrderID, Capture: in.Capture, Items: items})
				if err != nil {
					return nil, invoiceCreateOutput{}, err
				}
				return nil, invoiceCreateOutput{Invoice: *invoice}, nil
			})
		return nil
	}
	return t
}

const (
	shipmentCreateToolName    = "magento.shipment.create"
	shipmentCreateToolTitle   = "Create Magento shipment"
	shipmentCreateDescription = "Magento write operation: creates a shipment for an order " +
		"(POST /rest/{store_code}/V1/shipments) on the resolved instance, with 1-100 order-item lines and up to 20 tracking numbers. " +
		"Requires human confirmation."
	creditmemoCreateToolName    = "magento.creditmemo.create"
	creditmemoCreateToolTitle   = "Create Magento credit memo"
	creditmemoCreateDescription = "Magento write operation: creates a credit memo (refund record) for an order " +
		"(POST /rest/{store_code}/V1/creditmemos) on the resolved instance with 1-100 order-item lines and optional adjustments. " +
		"Requires human confirmation; actual payment refunds depend on the payment method."

	documentLinesProp = `"items": {"type": "array", "minItems": 1, "maxItems": 100, "items": {"type": "object",
		"additionalProperties": false, "required": ["order_item_id", "qty"],
		"properties": {"order_item_id": {"type": "integer", "minimum": 1},
			"qty": {"type": "number", "exclusiveMinimum": 0}}}}`

	shipmentCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1},
		` + documentLinesProp + `,
		"tracks": {"type": "array", "maxItems": 20, "items": {"type": "object", "additionalProperties": false,
			"required": ["track_number", "carrier_code"],
			"properties": {"track_number": {"type": "string", "maxLength": 128},
				"title": {"type": "string", "maxLength": 255},
				"carrier_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"}}}},
		"comment": {"type": "string", "maxLength": 1024},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "items"], "additionalProperties": false}`

	creditmemoCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1},
		` + documentLinesProp + `,
		"adjustment_positive": {"type": "number", "minimum": 0},
		"adjustment_negative": {"type": "number", "minimum": 0},
		"comment": {"type": "string", "maxLength": 1024},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "items"], "additionalProperties": false}`
)

type shipmentCreateInput struct {
	Instance string               `json:"instance"`
	OrderID  int                  `json:"order_id"`
	Items    []InvoiceLine        `json:"items"`
	Tracks   []ShipmentTrackInput `json:"tracks"`
	Comment  string               `json:"comment"`
	Notify   bool                 `json:"notify"`
}

type shipmentCreateOutput struct {
	Shipment Shipment `json:"shipment"`
}

type creditmemoCreateInput struct {
	Instance           string        `json:"instance"`
	OrderID            int           `json:"order_id"`
	Items              []InvoiceLine `json:"items"`
	AdjustmentPositive float64       `json:"adjustment_positive"`
	AdjustmentNegative float64       `json:"adjustment_negative"`
	Comment            string        `json:"comment"`
	Notify             bool          `json:"notify"`
}

type creditmemoCreateOutput struct {
	Creditmemo Creditmemo `json:"creditmemo"`
}

func newShipmentCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(shipmentCreateToolName, shipmentCreateToolTitle, shipmentCreateDescription, "magento.shipment.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(shipmentCreateToolName, shipmentCreateToolTitle, shipmentCreateDescription, shipmentCreateSchema),
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
		mcp.AddTool(srv, mcpWriteTool(creditmemoCreateToolName, creditmemoCreateToolTitle, creditmemoCreateDescription, creditmemoCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in creditmemoCreateInput) (*mcp.CallToolResult, creditmemoCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, creditmemoCreateOutput{}, err
				}
				lines := make([]DocumentLine, 0, len(in.Items))
				for _, item := range in.Items {
					lines = append(lines, DocumentLine(item))
				}
				creditmemo, err := svc.CreateCreditmemo(ctx, CreditmemoCreateInput{
					OrderID: in.OrderID, Items: lines,
					AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
					Comment: in.Comment, Notify: in.Notify,
				})
				if err != nil {
					return nil, creditmemoCreateOutput{}, err
				}
				return nil, creditmemoCreateOutput{Creditmemo: *creditmemo}, nil
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
		"order_id": {"type": "integer", "minimum": 1},
		"comment": {"type": "string", "minLength": 1, "maxLength": 1024},
		"status": {"type": "string", "maxLength": 64, "description": "Optional new order status; omit to only add the comment."},
		"notify": {"type": "boolean", "default": false}
	}, "required": ["instance", "order_id", "comment"], "additionalProperties": false}`

	orderHoldSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type orderCommentInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
	Comment  string `json:"comment"`
	Status   string `json:"status"`
	Notify   bool   `json:"notify"`
}

type orderHoldInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
}

type orderActionOutput struct {
	OrderID int  `json:"order_id"`
	Applied bool `json:"applied"`
}

func newOrderCommentTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderCommentToolName, orderCommentToolTitle, orderCommentDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(orderCommentToolName, orderCommentToolTitle, orderCommentDescription, orderCommentSchema),
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
		mcp.AddTool(srv, mcpWriteTool(name, title, description, orderHoldSchema),
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
		"(POST /rest/{store_code}/V1/categories) on the resolved instance. Requires human confirmation."
	categoryUpdateToolName    = "magento.category.update"
	categoryUpdateToolTitle   = "Update Magento category"
	categoryUpdateDescription = "Magento write operation: updates category name, active flag, position, or menu visibility " +
		"(PUT /rest/{store_code}/V1/categories/{id}) on the resolved instance. Requires human confirmation."
	categoryAssignToolName    = "magento.category.assign_product"
	categoryAssignToolTitle   = "Assign a product to a category"
	categoryAssignDescription = "Magento write operation: links a product to a category " +
		"(POST /rest/{store_code}/V1/categories/{id}/products) on the resolved instance. Requires human confirmation."

	categoryCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"parent_id": {"type": "integer", "minimum": 1, "description": "Parent category id; 2 is Magento's default root."},
		"name": {"type": "string", "minLength": 1, "maxLength": 255},
		"is_active": {"type": "boolean", "default": true},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000},
		"include_in_menu": {"type": "boolean", "default": true}
	}, "required": ["instance", "parent_id", "name"], "additionalProperties": false}`

	categoryUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"category_id": {"type": "integer", "minimum": 1},
		"name": {"type": "string", "minLength": 1, "maxLength": 255},
		"is_active": {"type": "boolean"},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000},
		"include_in_menu": {"type": "boolean"}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`

	categoryAssignSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"category_id": {"type": "integer", "minimum": 1},
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000}
	}, "required": ["instance", "category_id", "sku"], "additionalProperties": false}`
)

type categoryCreateInput struct {
	Instance      string `json:"instance"`
	ParentID      int    `json:"parent_id"`
	Name          string `json:"name"`
	IsActive      *bool  `json:"is_active"`
	Position      int    `json:"position"`
	IncludeInMenu *bool  `json:"include_in_menu"`
}

type categoryCreateOutput struct {
	Category Category `json:"category"`
}

type categoryUpdateInput struct {
	Instance      string  `json:"instance"`
	CategoryID    int     `json:"category_id"`
	Name          *string `json:"name"`
	IsActive      *bool   `json:"is_active"`
	Position      *int    `json:"position"`
	IncludeInMenu *bool   `json:"include_in_menu"`
}

type categoryUpdateOutput struct {
	Category Category `json:"category"`
}

type categoryAssignInput struct {
	Instance   string `json:"instance"`
	CategoryID int    `json:"category_id"`
	SKU        string `json:"sku"`
	Position   int    `json:"position"`
}

type categoryAssignOutput struct {
	CategoryID int    `json:"category_id"`
	SKU        string `json:"sku"`
	Assigned   bool   `json:"assigned"`
}

func newCategoryCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(categoryCreateToolName, categoryCreateToolTitle, categoryCreateDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(categoryCreateToolName, categoryCreateToolTitle, categoryCreateDescription, categoryCreateSchema),
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
					ParentID: in.ParentID, Name: in.Name, IsActive: isActive,
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
		mcp.AddTool(srv, mcpWriteTool(categoryUpdateToolName, categoryUpdateToolTitle, categoryUpdateDescription, categoryUpdateSchema),
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
		mcp.AddTool(srv, mcpWriteTool(categoryAssignToolName, categoryAssignToolTitle, categoryAssignDescription, categoryAssignSchema),
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
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"]},
		"linked_product_sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
		"position": {"type": "integer", "minimum": 0, "maximum": 10000}
	}, "required": ["instance", "sku", "link_type", "linked_product_sku"], "additionalProperties": false}`

	productLinkRemoveSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"]},
		"linked_product_sku": {"type": "string", "pattern": "` + skuInputPattern + `"}
	}, "required": ["instance", "sku", "link_type", "linked_product_sku"], "additionalProperties": false}`
)

type productLinkWriteInput struct {
	Instance         string `json:"instance"`
	SKU              string `json:"sku"`
	LinkType         string `json:"link_type"`
	LinkedProductSKU string `json:"linked_product_sku"`
	Position         int    `json:"position"`
}

type productLinkWriteOutput struct {
	SKU              string `json:"sku"`
	LinkType         string `json:"link_type"`
	LinkedProductSKU string `json:"linked_product_sku"`
	Applied          bool   `json:"applied"`
}

func newProductLinkAddTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(productLinkAddToolName, productLinkAddToolTitle, productLinkAddDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(productLinkAddToolName, productLinkAddToolTitle, productLinkAddDescription, productLinkAddSchema),
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
		mcp.AddTool(srv, mcpWriteTool(productLinkRemoveToolName, productLinkRemoveToolTitle, productLinkRemoveDescription, productLinkRemoveSchema),
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
		"(POST /rest/{store_code}/V1/invoices/{id}/capture) on the resolved instance. " +
		"This charges the payment method and moves money; refunds are a separate credit memo. Requires human confirmation."
	orderEmailToolName    = "magento.order.email"
	orderEmailToolTitle   = "Send a Magento order email"
	orderEmailDescription = "Magento write operation: sends the order confirmation email " +
		"(POST /rest/{store_code}/V1/orders/{id}/emails) on the resolved instance. Requires human confirmation."

	invoiceCaptureSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"invoice_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "invoice_id"], "additionalProperties": false}`

	orderEmailSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type invoiceCaptureInput struct {
	Instance  string `json:"instance"`
	InvoiceID int    `json:"invoice_id"`
}

type invoiceCaptureOutput struct {
	InvoiceID int  `json:"invoice_id"`
	Captured  bool `json:"captured"`
}

type orderEmailInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
}

type orderEmailOutput struct {
	OrderID int  `json:"order_id"`
	Sent    bool `json:"sent"`
}

func newInvoiceCaptureTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(invoiceCaptureToolName, invoiceCaptureToolTitle, invoiceCaptureDescription, "magento.invoice.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(invoiceCaptureToolName, invoiceCaptureToolTitle, invoiceCaptureDescription, invoiceCaptureSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in invoiceCaptureInput) (*mcp.CallToolResult, invoiceCaptureOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, invoiceCaptureOutput{}, err
				}
				if err := svc.CaptureInvoice(ctx, in.InvoiceID); err != nil {
					return nil, invoiceCaptureOutput{}, err
				}
				return nil, invoiceCaptureOutput{InvoiceID: in.InvoiceID, Captured: true}, nil
			})
		return nil
	}
	return t
}

func newOrderEmailTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderEmailToolName, orderEmailToolTitle, orderEmailDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(orderEmailToolName, orderEmailToolTitle, orderEmailDescription, orderEmailSchema),
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
		"media_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "sku", "media_id"], "additionalProperties": false}`
)

type mediaDeleteInput struct {
	Instance string `json:"instance"`
	SKU      string `json:"sku"`
	MediaID  int    `json:"media_id"`
}

type mediaDeleteOutput struct {
	SKU     string `json:"sku"`
	MediaID int    `json:"media_id"`
	Deleted bool   `json:"deleted"`
}

func newMediaDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(mediaDeleteToolName, mediaDeleteToolTitle, mediaDeleteDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{
			Name: mediaDeleteToolName, Title: mediaDeleteToolTitle, Description: mediaDeleteDescription,
			InputSchema: json.RawMessage(mediaDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
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
	orderCreateDescription = "Magento write operation: places an order for an existing customer through the admin cart flow " +
		"(cart, items, shipping information, then POST /rest/{store_code}/V1/carts/{cartId}/order) on the resolved instance. " +
		"Totals are computed by Magento and never supplied by the caller. Requires human confirmation; " +
		"prerequisites: the customer exists, the payment method is enabled, and the shipping method is available for the address."

	addressSchema = `{"type": "object", "additionalProperties": false,
		"required": ["firstname", "lastname", "street", "city", "postcode", "country_id", "telephone"],
		"properties": {
			"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
			"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
			"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}},
			"city": {"type": "string", "minLength": 1, "maxLength": 255},
			"postcode": {"type": "string", "minLength": 1, "maxLength": 255},
			"country_id": {"type": "string", "pattern": "^[A-Z]{2}$"},
			"telephone": {"type": "string", "minLength": 1, "maxLength": 255},
			"region": {"type": "string", "maxLength": 255}
		}}`

	orderCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"customer_id": {"type": "integer", "minimum": 1},
		"store_id": {"type": "integer", "minimum": 0},
		"items": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "object",
			"additionalProperties": false, "required": ["sku", "qty"],
			"properties": {"sku": {"type": "string", "pattern": "` + skuInputPattern + `"},
				"qty": {"type": "number", "exclusiveMinimum": 0}}}},
		"billing_address": ` + addressSchema + `,
		"shipping_address": ` + addressSchema + `,
		"shipping_carrier_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"},
		"shipping_method_code": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"},
		"payment_method": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$", "description": "Enabled Magento payment method code, e.g. checkmo."}
	}, "required": ["instance", "customer_id", "items", "billing_address", "shipping_address",
		"shipping_carrier_code", "shipping_method_code", "payment_method"], "additionalProperties": false}`
)

type orderAddressWire struct {
	Firstname string   `json:"firstname"`
	Lastname  string   `json:"lastname"`
	Street    []string `json:"street"`
	City      string   `json:"city"`
	Postcode  string   `json:"postcode"`
	CountryID string   `json:"country_id"`
	Telephone string   `json:"telephone"`
	Region    string   `json:"region"`
}

type orderCreateInput struct {
	Instance            string           `json:"instance"`
	CustomerID          int              `json:"customer_id"`
	StoreID             int              `json:"store_id"`
	Items               []OrderItemInput `json:"items"`
	BillingAddress      orderAddressWire `json:"billing_address"`
	ShippingAddress     orderAddressWire `json:"shipping_address"`
	ShippingCarrierCode string           `json:"shipping_carrier_code"`
	ShippingMethodCode  string           `json:"shipping_method_code"`
	PaymentMethod       string           `json:"payment_method"`
}

type orderCreateOutput struct {
	Order Order `json:"order"`
}

func (a orderAddressWire) toInput() OrderAddressInput {
	return OrderAddressInput(a)
}

func newOrderCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(orderCreateToolName, orderCreateToolTitle, orderCreateDescription, "magento.order.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(orderCreateToolName, orderCreateToolTitle, orderCreateDescription, orderCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in orderCreateInput) (*mcp.CallToolResult, orderCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, orderCreateOutput{}, err
				}
				order, err := svc.CreateOrder(ctx, CreateOrderInput{
					CustomerID: in.CustomerID, StoreID: in.StoreID, Items: in.Items,
					BillingAddress: in.BillingAddress.toInput(), ShippingAddress: in.ShippingAddress.toInput(),
					ShippingCarrierCode: in.ShippingCarrierCode, ShippingMethodCode: in.ShippingMethodCode,
					PaymentMethod: in.PaymentMethod,
				})
				if err != nil {
					return nil, orderCreateOutput{}, err
				}
				return nil, orderCreateOutput{Order: *order}, nil
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
		"prices": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "object",
			"additionalProperties": false, "required": ["price", "customer_group", "quantity"],
			"properties": {"price": {"type": "number", "exclusiveMinimum": 0},
				"website_id": {"type": "integer", "minimum": 0},
				"customer_group": {"type": "string", "minLength": 1, "maxLength": 64},
				"quantity": {"type": "number", "exclusiveMinimum": 0}}}}
	}, "required": ["instance", "sku", "prices"], "additionalProperties": false}`
)

type tierPriceWire struct {
	Price         float64 `json:"price"`
	WebsiteID     int     `json:"website_id"`
	CustomerGroup string  `json:"customer_group"`
	Quantity      float64 `json:"quantity"`
}

type tierPricesSetInput struct {
	Instance string          `json:"instance"`
	SKU      string          `json:"sku"`
	Prices   []tierPriceWire `json:"prices"`
}

type tierPricesSetOutput struct {
	SKU    string `json:"sku"`
	Prices int    `json:"prices"`
	Set    bool   `json:"set"`
}

func newTierPricesSetTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(tierPricesSetToolName, tierPricesSetToolTitle, tierPricesSetDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(tierPricesSetToolName, tierPricesSetToolTitle, tierPricesSetDescription, tierPricesSetSchema),
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

const (
	inventoryTransferToolName    = "magento.inventory.transfer"
	inventoryTransferToolTitle   = "Transfer Magento source quantity"
	inventoryTransferDescription = "Magento write operation: moves quantity for one SKU from one MSI source to another " +
		"(read source items, then upsert both rows) on the resolved instance. Refuses an insufficient source. " +
		"The batch is not transactional, so a failure can leave one row updated; destructive and confirmation-gated."

	inventoryTransferSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"from_source": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"},
		"to_source": {"type": "string", "pattern": "^[a-z0-9_]{1,64}$"},
		"quantity": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "sku", "from_source", "to_source", "quantity"], "additionalProperties": false}`
)

type inventoryTransferInput struct {
	Instance   string `json:"instance"`
	SKU        string `json:"sku"`
	FromSource string `json:"from_source"`
	ToSource   string `json:"to_source"`
	Quantity   int    `json:"quantity"`
}

type inventoryTransferOutput struct {
	SKU         string `json:"sku"`
	FromSource  string `json:"from_source"`
	ToSource    string `json:"to_source"`
	Quantity    int    `json:"quantity"`
	Transferred bool   `json:"transferred"`
}

func newInventoryTransferTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(inventoryTransferToolName, inventoryTransferToolTitle, inventoryTransferDescription, "magento.inventory.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{
			Name: inventoryTransferToolName, Title: inventoryTransferToolTitle, Description: inventoryTransferDescription,
			InputSchema: json.RawMessage(inventoryTransferSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in inventoryTransferInput) (*mcp.CallToolResult, inventoryTransferOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, inventoryTransferOutput{}, err
			}
			if err := svc.TransferSourceQty(ctx, in.SKU, in.FromSource, in.ToSource, in.Quantity); err != nil {
				return nil, inventoryTransferOutput{}, err
			}
			return nil, inventoryTransferOutput{
				SKU: in.SKU, FromSource: in.FromSource, ToSource: in.ToSource,
				Quantity: in.Quantity, Transferred: true,
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
		"email": {"type": "string", "format": "email", "maxLength": 254},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"website_id": {"type": "integer", "minimum": 1},
		"store_id": {"type": "integer", "minimum": 0},
		"group_id": {"type": "integer", "minimum": 0}
	}, "required": ["instance", "email", "firstname", "lastname", "website_id"], "additionalProperties": false}`

	customerUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"customer_id": {"type": "integer", "minimum": 1},
		"email": {"type": "string", "format": "email", "maxLength": 254},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"group_id": {"type": "integer", "minimum": 0}
	}, "required": ["instance", "customer_id"], "additionalProperties": false}`
)

type customerCreateWire struct {
	Instance  string `json:"instance"`
	Email     string `json:"email"`
	Firstname string `json:"firstname"`
	Lastname  string `json:"lastname"`
	WebsiteID int    `json:"website_id"`
	StoreID   int    `json:"store_id"`
	GroupID   int    `json:"group_id"`
}

type customerCreateOutput struct {
	Customer Customer `json:"customer"`
}

type customerUpdateWire struct {
	Instance   string  `json:"instance"`
	CustomerID int     `json:"customer_id"`
	Email      *string `json:"email"`
	Firstname  *string `json:"firstname"`
	Lastname   *string `json:"lastname"`
	GroupID    *int    `json:"group_id"`
}

type customerUpdateOutput struct {
	Customer Customer `json:"customer"`
}

func newCustomerCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerCreateToolName, customerCreateToolTitle, customerCreateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(customerCreateToolName, customerCreateToolTitle, customerCreateDescription, customerCreateSchema),
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
		mcp.AddTool(srv, mcpWriteTool(customerUpdateToolName, customerUpdateTitle, customerUpdateDescription, customerUpdateSchema),
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
		"(POST /rest/{store_code}/V1/orders/{id}/cancel) on the resolved instance. Irreversible; Magento rejects it when the order is not cancellable. " +
		"Requires human confirmation."

	mediaUpdateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"media_id": {"type": "integer", "minimum": 1},
		"label": {"type": "string", "maxLength": 255},
		"position": {"type": "integer", "minimum": 0, "maximum": 1000},
		"types": {"type": "array", "items": {"type": "string", "enum": ["image", "small_image", "thumbnail", "swatch_image"]}, "maxItems": 4},
		"disabled": {"type": "boolean"}
	}, "required": ["instance", "sku", "media_id"], "additionalProperties": false}`

	orderCancelSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"order_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "order_id"], "additionalProperties": false}`
)

type mediaUpdateInput struct {
	Instance string   `json:"instance"`
	SKU      string   `json:"sku"`
	MediaID  int      `json:"media_id"`
	Label    *string  `json:"label"`
	Position *int     `json:"position"`
	Types    []string `json:"types"`
	Disabled *bool    `json:"disabled"`
}

type mediaUpdateOutput struct {
	Media MediaEntry `json:"media"`
}

type orderCancelInput struct {
	Instance string `json:"instance"`
	OrderID  int    `json:"order_id"`
}

type orderCancelOutput struct {
	OrderID  int  `json:"order_id"`
	Canceled bool `json:"canceled"`
}

func newMediaUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(mediaUpdateToolName, mediaUpdateToolTitle, mediaUpdateDescription, capabilityProductWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(mediaUpdateToolName, mediaUpdateToolTitle, mediaUpdateDescription, mediaUpdateSchema),
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
		mcp.AddTool(srv, &mcp.Tool{
			Name: orderCancelToolName, Title: orderCancelToolTitle, Description: orderCancelDescription,
			InputSchema: json.RawMessage(orderCancelSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in orderCancelInput) (*mcp.CallToolResult, orderCancelOutput, error) {
			svc, err := lookup(in.Instance)
			if err != nil {
				return nil, orderCancelOutput{}, err
			}
			if err := svc.CancelOrder(ctx, in.OrderID); err != nil {
				return nil, orderCancelOutput{}, err
			}
			return nil, orderCancelOutput{OrderID: in.OrderID, Canceled: true}, nil
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
		"Useful for adding configurable variant values before creating the product. Requires human confirmation."

	attributeOptionCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `"},
		"label": {"type": "string", "minLength": 1, "maxLength": 255}
	}, "required": ["instance", "attribute_code", "label"], "additionalProperties": false}`
)

type attributeOptionCreateInput struct {
	Instance      string `json:"instance"`
	AttributeCode string `json:"attribute_code"`
	Label         string `json:"label"`
}

type attributeOptionCreateOutput struct {
	Option AttributeOption `json:"option"`
}

func newAttributeOptionCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(attributeOptionCreateToolName, attributeOptionCreateToolTitle, attributeOptionCreateDescription, "magento.attribute.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(attributeOptionCreateToolName, attributeOptionCreateToolTitle, attributeOptionCreateDescription, attributeOptionCreateSchema),
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
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `"},
		"option_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "attribute_code", "option_id"], "additionalProperties": false}`
)

type attributeOptionDeleteInput struct {
	Instance      string `json:"instance"`
	AttributeCode string `json:"attribute_code"`
	OptionID      int    `json:"option_id"`
}

type attributeOptionDeleteOutput struct {
	AttributeCode string `json:"attribute_code"`
	OptionID      int    `json:"option_id"`
	Deleted       bool   `json:"deleted"`
}

func newAttributeOptionDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(attributeOptionDeleteToolName, attributeOptionDeleteToolTitle, attributeOptionDeleteDescription, "magento.attribute.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{
			Name: attributeOptionDeleteToolName, Title: attributeOptionDeleteToolTitle, Description: attributeOptionDeleteDescription,
			InputSchema: json.RawMessage(attributeOptionDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
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
		"skus": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string", "pattern": "` + skuInputPattern + `"}},
		"status": {"type": "integer", "enum": [1, 2]},
		"visibility": {"type": "integer", "enum": [1, 2, 3, 4]},
		"attributes": {"type": "object", "additionalProperties": {"type": "string", "maxLength": 255}}
	}, "required": ["instance", "skus"], "additionalProperties": false}`
)

type bulkProductUpdateInput struct {
	Instance   string            `json:"instance"`
	SKUs       []string          `json:"skus"`
	Status     *int              `json:"status"`
	Visibility *int              `json:"visibility"`
	Attributes map[string]string `json:"attributes"`
}

type bulkProductUpdateOutput struct {
	Result BulkUpdateResult `json:"result"`
}

func newBulkProductUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkProductUpdateToolName, bulkProductUpdateToolTitle, bulkProductUpdateDescription, capabilityProductWrite)
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(bulkProductUpdateToolName, bulkProductUpdateToolTitle, bulkProductUpdateDescription, bulkProductUpdateSchema),
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
		"category_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "category_id"], "additionalProperties": false}`
)

type categoryDeleteInput struct {
	Instance   string `json:"instance"`
	CategoryID int    `json:"category_id"`
}

type categoryDeleteOutput struct {
	CategoryID int  `json:"category_id"`
	Deleted    bool `json:"deleted"`
}

func newCategoryDeleteTool(lookup serviceLookup) registry.Tool {
	t := destructiveToolMetadata(categoryDeleteToolName, categoryDeleteToolTitle, categoryDeleteDescription, "magento.category.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, &mcp.Tool{
			Name: categoryDeleteToolName, Title: categoryDeleteToolTitle, Description: categoryDeleteDescription,
			InputSchema: json.RawMessage(categoryDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
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
		"customer_id": {"type": "integer", "minimum": 1},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}},
		"city": {"type": "string", "minLength": 1, "maxLength": 255},
		"postcode": {"type": "string", "minLength": 1, "maxLength": 255},
		"country_id": {"type": "string", "pattern": "^[A-Z]{2}$"},
		"telephone": {"type": "string", "minLength": 1, "maxLength": 255},
		"region": {"type": "string", "maxLength": 255},
		"default_billing": {"type": "boolean", "default": false},
		"default_shipping": {"type": "boolean", "default": false}
	}, "required": ["instance", "customer_id", "firstname", "lastname", "street", "city", "postcode", "country_id", "telephone"], "additionalProperties": false}`

	customerAddressDeleteSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"address_id": {"type": "integer", "minimum": 1}
	}, "required": ["instance", "address_id"], "additionalProperties": false}`
)

type customerAddressCreateInput struct {
	Instance        string   `json:"instance"`
	CustomerID      int      `json:"customer_id"`
	Firstname       string   `json:"firstname"`
	Lastname        string   `json:"lastname"`
	Street          []string `json:"street"`
	City            string   `json:"city"`
	Postcode        string   `json:"postcode"`
	CountryID       string   `json:"country_id"`
	Telephone       string   `json:"telephone"`
	Region          string   `json:"region"`
	DefaultBilling  bool     `json:"default_billing"`
	DefaultShipping bool     `json:"default_shipping"`
}

type customerAddressCreateOutput struct {
	Address CustomerAddress `json:"address"`
}

type customerAddressDeleteInput struct {
	Instance  string `json:"instance"`
	AddressID int    `json:"address_id"`
}

type customerAddressDeleteOutput struct {
	AddressID int  `json:"address_id"`
	Deleted   bool `json:"deleted"`
}

func newCustomerAddressCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerAddressCreateToolName, customerAddressCreateToolTitle, customerAddressCreateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(customerAddressCreateToolName, customerAddressCreateToolTitle, customerAddressCreateDescription, customerAddressCreateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerAddressCreateInput) (*mcp.CallToolResult, customerAddressCreateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerAddressCreateOutput{}, err
				}
				address, err := svc.CreateCustomerAddress(ctx, in.CustomerID, CustomerAddressInput{
					Firstname: in.Firstname, Lastname: in.Lastname, Street: in.Street,
					City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
					Telephone: in.Telephone, Region: in.Region,
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
		mcp.AddTool(srv, &mcp.Tool{
			Name: customerAddressDeleteToolName, Title: customerAddressDeleteTitle, Description: customerAddressDeleteDescription,
			InputSchema: json.RawMessage(customerAddressDeleteSchema),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: false, DestructiveHint: destructiveTrue()},
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
		"customer_id": {"type": "integer", "minimum": 1},
		"address_id": {"type": "integer", "minimum": 1},
		"firstname": {"type": "string", "minLength": 1, "maxLength": 255},
		"lastname": {"type": "string", "minLength": 1, "maxLength": 255},
		"street": {"type": "array", "minItems": 1, "maxItems": 4, "items": {"type": "string", "minLength": 1, "maxLength": 255}},
		"city": {"type": "string", "minLength": 1, "maxLength": 255},
		"postcode": {"type": "string", "minLength": 1, "maxLength": 255},
		"country_id": {"type": "string", "pattern": "^[A-Z]{2}$"},
		"telephone": {"type": "string", "minLength": 1, "maxLength": 255},
		"region": {"type": "string", "maxLength": 255},
		"default_billing": {"type": "boolean"},
		"default_shipping": {"type": "boolean"}
	}, "required": ["instance", "customer_id", "address_id"], "additionalProperties": false}`
)

type customerAddressUpdateInput struct {
	Instance        string    `json:"instance"`
	CustomerID      int       `json:"customer_id"`
	AddressID       int       `json:"address_id"`
	Firstname       *string   `json:"firstname"`
	Lastname        *string   `json:"lastname"`
	Street          *[]string `json:"street"`
	City            *string   `json:"city"`
	Postcode        *string   `json:"postcode"`
	CountryID       *string   `json:"country_id"`
	Telephone       *string   `json:"telephone"`
	Region          *string   `json:"region"`
	DefaultBilling  *bool     `json:"default_billing"`
	DefaultShipping *bool     `json:"default_shipping"`
}

type customerAddressUpdateOutput struct {
	Address CustomerAddress `json:"address"`
}

func newCustomerAddressUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(customerAddressUpdateToolName, customerAddressUpdateTitle, customerAddressUpdateDescription, "magento.customer.write")
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(customerAddressUpdateToolName, customerAddressUpdateTitle, customerAddressUpdateDescription, customerAddressUpdateSchema),
			func(ctx context.Context, _ *mcp.CallToolRequest, in customerAddressUpdateInput) (*mcp.CallToolResult, customerAddressUpdateOutput, error) {
				svc, err := lookup(in.Instance)
				if err != nil {
					return nil, customerAddressUpdateOutput{}, err
				}
				address, err := svc.UpdateCustomerAddress(ctx, in.CustomerID, in.AddressID, CustomerAddressUpdateInput{
					Firstname: in.Firstname, Lastname: in.Lastname, Street: in.Street,
					City: in.City, Postcode: in.Postcode, CountryID: in.CountryID,
					Telephone: in.Telephone, Region: in.Region,
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
		"The instance input is resolved by the server when only one Magento instance is configured. Requires human confirmation."

	bulkLinkSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `, ` + skuProp + `,
		"link_type": {"type": "string", "enum": ["related", "upsell", "crosssell"]},
		"action": {"type": "string", "enum": ["add", "remove"]},
		"linked_skus": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string", "pattern": "` + skuInputPattern + `"}}
	}, "required": ["instance", "sku", "link_type", "action", "linked_skus"], "additionalProperties": false}`
)

type bulkLinkInput struct {
	Instance   string   `json:"instance"`
	SKU        string   `json:"sku"`
	LinkType   string   `json:"link_type"`
	Action     string   `json:"action"`
	LinkedSKUs []string `json:"linked_skus"`
}

type bulkLinkOutput struct {
	Result LinkBulkResult `json:"result"`
}

func newBulkLinkTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkLinkToolName, bulkLinkToolTitle, bulkLinkDescription, "magento.product.write")
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(bulkLinkToolName, bulkLinkToolTitle, bulkLinkDescription, bulkLinkSchema),
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
		"The instance input is resolved by the server when only one Magento instance is configured. Requires human confirmation."

	bulkOptionCreateSchema = `{"type": "object", "properties": {` + instanceProp + `, ` + confirmationProp + `,
		"attribute_code": {"type": "string", "pattern": "` + attributeCodeInputPattern + `"},
		"labels": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string", "minLength": 1, "maxLength": 255}}
	}, "required": ["instance", "attribute_code", "labels"], "additionalProperties": false}`
)

type bulkOptionCreateInput struct {
	Instance      string   `json:"instance"`
	AttributeCode string   `json:"attribute_code"`
	Labels        []string `json:"labels"`
}

type bulkOptionCreateOutput struct {
	Result OptionBulkResult `json:"result"`
}

func newBulkOptionCreateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(bulkOptionCreateToolName, bulkOptionCreateToolTitle, bulkOptionCreateDescription, "magento.attribute.write")
	t.Timeout = 120 * time.Second
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(bulkOptionCreateToolName, bulkOptionCreateToolTitle, bulkOptionCreateDescription, bulkOptionCreateSchema),
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
		"Refuses SKUs whose type is not configurable. The instance input is resolved by the server when only one Magento instance is configured. " +
		"Requires human confirmation."
)

func newConfigurableUpdateTool(lookup serviceLookup) registry.Tool {
	t := writeToolMetadata(configurableUpdateToolName, configurableUpdateToolTitle, configurableUpdateDescription, capabilityConfigurableWrite)
	t.Register = func(srv *mcp.Server) error {
		mcp.AddTool(srv, mcpWriteTool(configurableUpdateToolName, configurableUpdateToolTitle, configurableUpdateDescription, productUpdateSchema),
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
