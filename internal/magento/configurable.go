// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

	stagePrecheck = "precheck"
	stageChildren = "children"
	stageParent   = "parent"
	stageOptions  = "options"
	stageLinks    = "links"
	stageStock    = "stock"
)

// configurableStages is the fixed order of the create-configurable workflow.
var configurableStages = []string{stagePrecheck, stageChildren, stageParent, stageLinks, stageStock}

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

	// OperationID resumes an earlier attempt of the same create. When set, SKUs
	// that already exist upstream are reused (and missing links repaired)
	// instead of colliding; when empty the run is a fresh create and any
	// pre-existing SKU aborts in precheck.
	//
	// The resume state is the upstream read-back: the id is a caller-carried
	// handle that selects resume mode, and this workflow does not record the
	// intent in the durable operation ledger (that ledger covers the
	// money-moving tools).
	OperationID string
}

// ConfigurableProduct is a configurable parent together with its linked children.
//
// ParentInStock reports whether the parent is salable as far as the gateway
// could observe it, and ParentStockSource states how that value was derived so
// the caller never has to guess. CreateConfigurable derives it from the MSI
// source items of the children; GetConfigurable leaves both fields zero.
type ConfigurableProduct struct {
	Parent            Product
	Children          []Product
	ParentInStock     bool   `json:"parent_in_stock"`
	ParentStockSource string `json:"parent_stock_source,omitempty"`

	// Execution record. CreateConfigurable always sets it; GetConfigurable
	// leaves it empty.
	OperationID     string              `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Outcome         ConfigurableOutcome `json:"outcome,omitempty"`
	Stages          []ConfigurableStage `json:"stages,omitempty"`
	CompletedStages []string            `json:"completed_stages,omitempty"`
	FailedStage     string              `json:"failed_stage,omitempty"`
	OrphanSKUs      []string            `json:"orphan_skus,omitempty"`
}

// ConfigurableOutcome is the overall outcome of a create-configurable run.
type ConfigurableOutcome string

const (
	// ConfigurableCompleted means every stage completed or was already satisfied
	// upstream.
	ConfigurableCompleted ConfigurableOutcome = "completed"
	// ConfigurablePartial means at least one stage failed or was left pending.
	// The result names the completed stages, the failed stage, and any orphaned
	// SKUs, and the call returns a typed error.
	ConfigurablePartial ConfigurableOutcome = "partial"
)

// ConfigurableStageState is the state of one workflow stage.
type ConfigurableStageState string

const (
	// ConfigurableStagePending means the stage never ran (an earlier stage failed).
	ConfigurableStagePending ConfigurableStageState = "pending"
	// ConfigurableStageSkipped means the stage was already satisfied upstream.
	ConfigurableStageSkipped ConfigurableStageState = "skipped"
	// ConfigurableStageCompleted means this run did the stage's work.
	ConfigurableStageCompleted ConfigurableStageState = "completed"
	// ConfigurableStageFailed means the stage's work failed.
	ConfigurableStageFailed ConfigurableStageState = "failed"
)

// ConfigurableStage is one ordered step of the create-configurable workflow.
type ConfigurableStage struct {
	Name   string                 `json:"name" jsonschema:"Display name."`
	State  ConfigurableStageState `json:"state" jsonschema:"Current state."`
	Detail string                 `json:"detail,omitempty"`
}

type configurableOption struct {
	AttributeID string                    `json:"attribute_id"`
	Label       string                    `json:"label" jsonschema:"Label shown to the shopper."`
	Position    int                       `json:"position" jsonschema:"Sort position among siblings."`
	Values      []configurableOptionValue `json:"values" jsonschema:"Selectable values of this option."`
}

type configurableOptionValue struct {
	ValueIndex int `json:"value_index"`
}

type configurableOptionBody struct {
	Option configurableOption `json:"option" jsonschema:"The option that was created or read."`
}

type configurableChildBody struct {
	ChildSKU string `json:"childSku"`
}

// CreateConfigurable creates a configurable product as a staged, resumable
// workflow: precheck -> children -> parent -> links -> stock.
//
// PRECHECK runs before the first write: local validation (SKUs, attribute codes,
// duplicate variant combinations), the variant attributes' membership in the
// parent attribute set, and an upstream read of every SKU. A fresh create (no
// OperationID) aborts on any pre-existing SKU with a conflict and zero writes.
//
// The result always carries the execution record: OperationID, Outcome
// (completed or partial), the ordered Stages with their state and detail, the
// completed stage names, the failed stage, and any OrphanSKUs (children that
// exist upstream but are not linked to the parent). A partial run returns the
// record together with a typed error, so it can never be mistaken for a success.
//
// RESUME: pass the OperationID of a failed attempt to reuse what already exists.
// Existing children and a parent that match the request are skipped (never
// re-POSTed), missing links are added, and an orphan left by a failed link stage
// is repaired. The upstream read-back is the resume state; the durable operation
// ledger records the intent across processes.
//
// VERIFICATION: after linking, the parent's children are read back; a link the
// API accepted but the parent does not report fails the links stage.
//
// COMPENSATION: nothing is deleted. A partial run reports the orphans and the
// completed stages and leaves the decision to the operator, because the gateway
// has no reliable evidence that a given product was created by this operation
// (that evidence belongs to the operation ledger).
func (s *Service) CreateConfigurable(ctx context.Context, in CreateConfigurableInput) (*ConfigurableProduct, error) {
	if err := s.requireClient(); err != nil {
		return nil, err
	}
	if err := validateConfigurableInput(in); err != nil {
		return nil, err
	}
	operationID, resume, err := configurableOperationID(in)
	if err != nil {
		return nil, err
	}
	result := newConfigurableRun(operationID)

	// PRECHECK: no write happens before this returns clean.
	pre, err := s.precheckConfigurable(ctx, in, resume, result)
	if err != nil {
		return nil, err
	}
	result.finishStage(stagePrecheck, ConfigurableStageCompleted, pre.detail)

	// CHILDREN: create only what is missing; a resumed run reuses the rest.
	children := make([]Product, 0, len(in.Children))
	existing := make([]string, 0, len(in.Children)+1)
	createdChildren, reusedChildren := 0, 0
	for _, child := range in.Children {
		if product, ok := pre.children[child.SKU]; ok {
			children = append(children, *product)
			existing = append(existing, child.SKU)
			reusedChildren++
			continue
		}
		product, err := s.createChild(ctx, in.Parent, child)
		if err != nil {
			if productCreated(err) {
				existing = append(existing, child.SKU)
			}
			result.Children = children
			result.OrphanSKUs = append([]string(nil), existing...)
			result.finishStage(stageChildren, ConfigurableStageFailed, stageDetail(err))
			return result, configurableStageError(result, existing, child.SKU, err)
		}
		children = append(children, *product)
		existing = append(existing, child.SKU)
		createdChildren++
	}
	childrenState := ConfigurableStageCompleted
	if createdChildren == 0 {
		childrenState = ConfigurableStageSkipped
	}
	result.finishStage(stageChildren, childrenState,
		fmt.Sprintf("created %d, reused %d", createdChildren, reusedChildren))

	// PARENT: reuse an existing configurable parent on resume.
	parent := pre.parent
	if parent != nil {
		result.finishStage(stageParent, ConfigurableStageSkipped, "parent already exists")
	} else {
		created, err := s.createConfigurableParent(ctx, in)
		if err != nil {
			result.Children = children
			result.OrphanSKUs = append([]string(nil), existing...)
			result.finishStage(stageParent, ConfigurableStageFailed, stageDetail(err))
			return result, configurableStageError(result, existing, in.Parent.SKU, err)
		}
		parent = created
		result.finishStage(stageParent, ConfigurableStageCompleted, "created")
	}
	existing = append(existing, in.Parent.SKU)

	// LINKS: link what is missing, then verify the association by reading it back.
	linkedNow := make(map[string]bool, len(pre.linked)+len(in.Children))
	for sku := range pre.linked {
		linkedNow[sku] = true
	}
	addedLinks, reusedLinks := 0, 0
	var linkErr error
	var failedLink string
	for _, child := range in.Children {
		if linkedNow[child.SKU] {
			reusedLinks++
			continue
		}
		if err := s.AddChild(ctx, in.Parent.SKU, child.SKU); err != nil {
			linkErr, failedLink = err, child.SKU
			break
		}
		linkedNow[child.SKU] = true
		addedLinks++
	}
	actual, verifyErr := s.Children(ctx, in.Parent.SKU)
	verified := make(map[string]bool, len(actual))
	for _, child := range actual {
		verified[child.SKU] = true
	}
	switch {
	case linkErr != nil:
		detail := "link failed for " + failedLink + ": " + stageDetail(linkErr)
		if verifyErr != nil {
			detail += "; verification read failed: " + string(errs.CodeOf(verifyErr))
		}
		result.Parent, result.Children = *parent, children
		result.OrphanSKUs = missingChildSKUs(in.Children, linkedNow)
		result.finishStage(stageLinks, ConfigurableStageFailed, detail)
		return result, configurableStageError(result, existing, failedLink, linkErr)
	case verifyErr != nil:
		result.Parent, result.Children = *parent, children
		result.OrphanSKUs = missingChildSKUs(in.Children, linkedNow)
		result.finishStage(stageLinks, ConfigurableStageFailed,
			"link verification read failed: "+string(errs.CodeOf(verifyErr)))
		return result, configurableStageError(result, existing, "", verifyErr)
	case len(missingChildSKUs(in.Children, verified)) > 0:
		missing := missingChildSKUs(in.Children, verified)
		err := errs.New(errs.CodeConflict,
			"Magento accepted the child links but the parent does not report them")
		result.Parent, result.Children = *parent, children
		result.OrphanSKUs = missing
		result.finishStage(stageLinks, ConfigurableStageFailed,
			"verification: not linked: "+strings.Join(missing, ","))
		return result, configurableStageError(result, existing, missing[0], err)
	}
	linksState := ConfigurableStageCompleted
	if addedLinks == 0 {
		linksState = ConfigurableStageSkipped
	}
	result.finishStage(stageLinks, linksState,
		fmt.Sprintf("linked %d, reused %d, verified %d of %d",
			addedLinks, reusedLinks, len(in.Children), len(in.Children)))

	// STOCK: derive the salable state from the children's source items (MAG-09)
	// and write it; a failed write fails the stage (MAG-08).
	inStock, stockSource, observeErr := s.observeParentStock(ctx, in.Children)
	result.Parent, result.Children = *parent, children
	if observeErr != nil {
		result.ParentInStock = false
		result.ParentStockSource = "unobserved: " + string(errs.CodeOf(observeErr))
		result.finishStage(stageStock, ConfigurableStageSkipped,
			result.ParentStockSource+"; no stock status written")
		result.Outcome = ConfigurableCompleted
		return result, nil
	}
	if parent.ID > 0 {
		if err := s.setParentStockStatus(ctx, in.Parent.SKU, parent.ID, inStock); err != nil {
			result.finishStage(stageStock, ConfigurableStageFailed, stageDetail(err))
			return result, configurableStageError(result, existing, in.Parent.SKU, err)
		}
	} else {
		inStock, stockSource = false, "parent_id_missing: stock status not set"
		result.ParentInStock, result.ParentStockSource = inStock, stockSource
		result.finishStage(stageStock, ConfigurableStageSkipped, stockSource)
		result.Outcome = ConfigurableCompleted
		return result, nil
	}
	result.ParentInStock, result.ParentStockSource = inStock, stockSource
	result.finishStage(stageStock, ConfigurableStageCompleted, stockSource)
	result.Outcome = ConfigurableCompleted
	return result, nil
}

// newConfigurableRun returns an execution record whose default outcome is
// partial: only a run that reaches the end sets ConfigurableCompleted, so an
// early return can never look like a success.
func newConfigurableRun(operationID string) *ConfigurableProduct {
	stages := make([]ConfigurableStage, 0, len(configurableStages))
	for _, name := range configurableStages {
		stages = append(stages, ConfigurableStage{Name: name, State: ConfigurableStagePending})
	}
	return &ConfigurableProduct{OperationID: operationID, Outcome: ConfigurablePartial, Stages: stages}
}

// finishStage records one stage's final state.
func (r *ConfigurableProduct) finishStage(name string, state ConfigurableStageState, detail string) {
	for i := range r.Stages {
		if r.Stages[i].Name != name {
			continue
		}
		r.Stages[i].State = state
		r.Stages[i].Detail = detail
	}
	switch state {
	case ConfigurableStageCompleted, ConfigurableStageSkipped:
		r.CompletedStages = append(r.CompletedStages, name)
	case ConfigurableStageFailed:
		r.FailedStage = name
	}
}

// configurableOperationID returns the operation id for this run and whether the
// caller asked to resume an earlier attempt.
func configurableOperationID(in CreateConfigurableInput) (string, bool, error) {
	if in.OperationID == "" {
		var raw [8]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", false, errs.New(errs.CodeInternal, "cannot generate an operation id").WithCause(err)
		}
		return "cfg_" + hex.EncodeToString(raw[:]), false, nil
	}
	if !configurableOperationIDRE.MatchString(in.OperationID) {
		return "", false, invalidf("operation_id must match %s", configurableOperationIDRE.String())
	}
	return in.OperationID, true, nil
}

var configurableOperationIDRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// configurablePrecheck is the upstream state observed before the first write.
type configurablePrecheck struct {
	children map[string]*Product
	parent   *Product
	linked   map[string]bool
	detail   string
}

// precheckConfigurable validates variant attributes, then reads every SKU the
// run would write. A fresh create aborts on any pre-existing SKU; a resume
// adopts existing products that match the request.
func (s *Service) precheckConfigurable(ctx context.Context, in CreateConfigurableInput, resume bool, result *ConfigurableProduct) (*configurablePrecheck, error) {
	pre := &configurablePrecheck{
		children: make(map[string]*Product, len(in.Children)),
		linked:   make(map[string]bool),
	}
	attributeDetail, err := s.checkVariantAttributes(ctx, in)
	if err != nil {
		result.finishStage(stagePrecheck, ConfigurableStageFailed, stageDetail(err))
		return nil, configurablePrecheckError(result, err)
	}
	var collisions []string
	for _, child := range in.Children {
		product, getErr := s.Get(ctx, child.SKU)
		switch {
		case getErr == nil:
			if !resume || !sameChildShape(product, child, in.Variants) {
				collisions = append(collisions, child.SKU)
				continue
			}
			pre.children[child.SKU] = product
		case errs.CodeOf(getErr) == errs.CodeNotFound:
		default:
			result.finishStage(stagePrecheck, ConfigurableStageFailed, stageDetail(getErr))
			return nil, configurablePrecheckError(result, getErr)
		}
	}
	parentDetail := "absent"
	parent, getErr := s.Get(ctx, in.Parent.SKU)
	switch {
	case getErr == nil:
		if !resume || parent.TypeID != configurableTypeID {
			collisions = append(collisions, in.Parent.SKU)
			break
		}
		linked, linkErr := s.Children(ctx, in.Parent.SKU)
		if linkErr != nil {
			result.finishStage(stagePrecheck, ConfigurableStageFailed, stageDetail(linkErr))
			return nil, configurablePrecheckError(result, linkErr)
		}
		pre.parent = parent
		for _, child := range linked {
			pre.linked[child.SKU] = true
		}
		parentDetail = fmt.Sprintf("present with %d linked children", len(linked))
	case errs.CodeOf(getErr) == errs.CodeNotFound:
	default:
		result.finishStage(stagePrecheck, ConfigurableStageFailed, stageDetail(getErr))
		return nil, configurablePrecheckError(result, getErr)
	}
	if len(collisions) > 0 {
		sort.Strings(collisions)
		e := errs.New(errs.CodeConflict,
			"SKU already exists in Magento and a fresh create never overwrites it: "+strings.Join(collisions, ", ")+
				" (resume with the operation id of the earlier attempt to reuse the products it created)")
		e = e.WithDetail("collision_skus", strings.Join(collisions, ","))
		result.finishStage(stagePrecheck, ConfigurableStageFailed, "SKU collision: "+strings.Join(collisions, ", "))
		return nil, configurablePrecheckError(result, e)
	}
	pre.detail = fmt.Sprintf("%s; %d of %d children already present; parent %s",
		attributeDetail, len(pre.children), len(in.Children), parentDetail)
	return pre, nil
}

// checkVariantAttributes verifies that every variant attribute is a selectable
// attribute of the parent's attribute set. The attribute-set read is best
// effort: when it cannot be read the precheck records that membership was not
// verified rather than blocking the create on an unrelated permission.
func (s *Service) checkVariantAttributes(ctx context.Context, in CreateConfigurableInput) (string, error) {
	attributes, err := s.Attributes(ctx, in.Parent.AttributeSetID)
	if err != nil {
		return "attribute set " + strconv.Itoa(in.Parent.AttributeSetID) +
			" unreadable (" + string(errs.CodeOf(err)) + "); variant membership not verified", nil
	}
	if len(attributes) == 0 {
		return "attribute set " + strconv.Itoa(in.Parent.AttributeSetID) +
			" returned no attributes; variant membership not verified", nil
	}
	known := make(map[string]Attribute, len(attributes))
	for _, attribute := range attributes {
		known[attribute.Code] = attribute
	}
	var invalid []string
	for _, variant := range in.Variants {
		attribute, ok := known[variant.Code]
		if !ok {
			invalid = append(invalid, variant.Code+" (absent from attribute set)")
			continue
		}
		switch attribute.Input {
		case "select", "multiselect", "swatch_visual", "swatch_text":
		default:
			invalid = append(invalid, variant.Code+" (frontend_input "+attribute.Input+")")
		}
	}
	if len(invalid) > 0 {
		return "", invalidf("variant attributes are not selectable attributes of attribute set %d: %s",
			in.Parent.AttributeSetID, strings.Join(invalid, ", "))
	}
	return fmt.Sprintf("attribute set %d carries %d of %d variant attributes",
		in.Parent.AttributeSetID, len(in.Variants), len(in.Variants)), nil
}

// sameChildShape reports whether an existing simple product looks like the child
// this request would create, which is the evidence a resume needs to reuse it.
// A variant attribute the projection did not return is not treated as a
// mismatch: Magento omits attributes outside the attribute set.
func sameChildShape(product *Product, child ChildProduct, variants []VariantAttribute) bool {
	if product.TypeID != simpleTypeID {
		return false
	}
	for _, variant := range variants {
		value, ok := product.Attributes[variant.Code]
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if strings.TrimSpace(value) != strings.TrimSpace(child.Attributes[variant.Code]) {
			return false
		}
	}
	return true
}

// missingChildSKUs returns the requested child SKUs that are not present.
func missingChildSKUs(children []ChildProduct, present map[string]bool) []string {
	missing := make([]string, 0, len(children))
	for _, child := range children {
		if !present[child.SKU] {
			missing = append(missing, child.SKU)
		}
	}
	return missing
}

// stageDetail renders an error for a stage record without leaking the upstream
// body.
func stageDetail(err error) string {
	var typed *errs.Error
	if errors.As(err, &typed) && typed.Message != "" {
		return typed.Message
	}
	return err.Error()
}

// configurableStageError renders a partial run as a typed error carrying the
// execution record, so a caller that only sees the error still knows the
// operation id, the failed stage, the completed stages, and the orphans.
func configurableStageError(result *ConfigurableProduct, completed []string, failedSKU string, cause error) error {
	message := fmt.Sprintf("Magento configurable product create is %s at stage %s", result.Outcome, result.FailedStage)
	if detail := stageDetail(cause); detail != "" {
		message += ": " + detail
	}
	message += "; completed writes are not rolled back and require compensation"
	e := errs.New(errs.CodeOf(cause), message)
	e = e.WithCause(cause)
	e = e.WithDetail("stage", result.FailedStage)
	completedSKUs, omitted := boundedSKUs(completed)
	e = e.WithDetail("completed_skus", completedSKUs)
	if omitted > 0 {
		e = e.WithDetail("completed_total", strconv.Itoa(len(completed)))
		e = e.WithDetail("completed_omitted", strconv.Itoa(omitted))
	}
	if failedSKU != "" {
		e = e.WithDetail("failed_sku", failedSKU)
	}
	e = e.WithDetail("operation_id", result.OperationID)
	e = e.WithDetail("outcome", string(result.Outcome))
	e = e.WithDetail("failed_stage", result.FailedStage)
	e = e.WithDetail("completed_stages", strings.Join(result.CompletedStages, ","))
	if len(result.OrphanSKUs) > 0 {
		e = e.WithDetail("orphan_skus", strings.Join(result.OrphanSKUs, ","))
	}
	return e
}

// configurablePrecheckError renders a precheck failure. Nothing was written, so
// the error carries no completed SKUs.
func configurablePrecheckError(result *ConfigurableProduct, cause error) error {
	e := errs.New(errs.CodeOf(cause), "configurable product create failed at precheck: "+stageDetail(cause))
	e = e.WithCause(cause)
	e = e.WithDetail("stage", stagePrecheck)
	e = e.WithDetail("operation_id", result.OperationID)
	e = e.WithDetail("outcome", string(result.Outcome))
	var typed *errs.Error
	if errors.As(cause, &typed) {
		if collisions := typed.Details["collision_skus"]; collisions != "" {
			e = e.WithDetail("collision_skus", collisions)
		}
	}
	return e
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
	return preserveUpstream(e, cause)
}

// upstreamDetailKeys are the cause details worth carrying through a stage wrapper.
// Without them the caller is told "verify store_code and field values" when the
// real problem is a specific attribute value, and the retry that follows
// re-creates products.
var upstreamDetailKeys = []string{"upstream_status", "upstream_code", "store_code"}

// maxUpstreamMessageBytes bounds the upstream text copied into a detail. The full
// message can be long and can quote store data; a bounded prefix is enough to
// diagnose without turning the error into a data channel.
const maxUpstreamMessageBytes = 512

// preserveUpstream copies the upstream diagnostics from cause onto e. Retryability
// is deliberately not copied: a stage wrapper describes a partly applied write, and
// a blind retry re-creates whatever the stage already committed.
func preserveUpstream(e *errs.Error, cause error) error {
	var from *errs.Error
	if !errors.As(cause, &from) {
		return e
	}
	for _, key := range upstreamDetailKeys {
		if value := from.Details[key]; value != "" {
			e = e.WithDetail(key, value)
		}
	}
	// The upstream text stays server-side: it is logged, never serialised into the
	// tool result the agent sees (SEC-10).
	if from.Upstream != "" {
		e = e.WithUpstream(from.Upstream)
	}
	if message := strings.TrimSpace(from.Details["upstream_message"]); message != "" {
		if len(message) > maxUpstreamMessageBytes {
			message = message[:maxUpstreamMessageBytes] + "…"
		}
		e = e.WithUpstream(message)
	}
	return e
}

// writeStageError reports a non-atomic failure with its stage and completed SKUs.
// The completed list is the caller's only compensation record, so it is never
// abbreviated mid-SKU: whole SKUs are included until the detail budget is spent
// and the remainder is reported as completed_omitted/completed_total.
//
//nolint:unparam // completed is part of the shared helper's contract; the cart flow passes nil.
func writeStageError(stage string, completed []string, failedSKU string, cause error) error {
	e := errs.New(errs.CodeOf(cause), stageMessage(stage, cause)+
		"; completed writes are not rolled back and require compensation")
	e = e.WithCause(cause)
	e = e.WithDetail("stage", stage)
	completedSKUs, omitted := boundedSKUs(completed)
	e = e.WithDetail("completed_skus", completedSKUs)
	if omitted > 0 {
		e = e.WithDetail("completed_total", strconv.Itoa(len(completed)))
		e = e.WithDetail("completed_omitted", strconv.Itoa(omitted))
	}
	if failedSKU != "" {
		_ = e.WithDetail("failed_sku", failedSKU)
	}
	return preserveUpstream(e, cause)
}

func stageMessage(stage string, cause error) string {
	message := "Magento write failed at stage " + stage
	var e *errs.Error
	if errors.As(cause, &e) && e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

// boundedSKUs renders as many whole SKUs as fit the detail budget, at most
// maxCompletedSKUs entries, and returns how many were left out. A SKU that does
// not fit is dropped entirely: a partial token would name a product that does
// not exist and corrupt the compensation list.
func boundedSKUs(skus []string) (string, int) {
	total := len(skus)
	if total == 0 {
		return "", 0
	}
	if len(skus) > maxCompletedSKUs {
		skus = skus[:maxCompletedSKUs]
	}
	kept := make([]string, 0, len(skus))
	used := 0
	for _, sku := range skus {
		needed := len(sku)
		if len(kept) > 0 {
			needed++ // comma separator
		}
		if used+needed > maxCompletedSKUsBytes {
			break
		}
		kept = append(kept, sku)
		used += needed
	}
	return strings.Join(kept, ","), total - len(kept)
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
	SKU    string `json:"sku" jsonschema:"Product SKU."`
	Status string `json:"status" jsonschema:"Current status."`
	Error  string `json:"error,omitempty" jsonschema:"Why the row failed."`
}

// VariationResult reports a partial-success variation creation.
type VariationResult struct {
	Total   int            `json:"total" jsonschema:"Number of records the store holds; may exceed the number returned."`
	Created int            `json:"created" jsonschema:"Rows that created a product."`
	Linked  int            `json:"linked" jsonschema:"True when the child is now linked to the parent."`
	Skipped int            `json:"skipped" jsonschema:"Rows skipped because the product existed."`
	Failed  int            `json:"failed" jsonschema:"Rows that failed."`
	Rows    []VariationRow `json:"rows" jsonschema:"Per-row results, in input order."`
}

// CreateVariations creates simple children for an existing configurable parent
// and links them. An existing SKU that is already linked is skipped; an existing
// SKU that is not linked is an orphan left by an earlier failed link and is
// repaired (linked) rather than reported as "exists" and abandoned, so the call
// is idempotent per SKU. Failures are reported per row and earlier children are
// not rolled back.
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
	linkedChildren, err := s.Children(ctx, parentSKU)
	if err != nil {
		return nil, err
	}
	linked := make(map[string]bool, len(linkedChildren))
	for _, child := range linkedChildren {
		linked[child.SKU] = true
	}
	result := &VariationResult{Total: len(variants), Rows: make([]VariationRow, 0, len(variants))}
	for _, variant := range variants {
		row := VariationRow{SKU: variant.SKU}
		if _, err := s.Get(ctx, variant.SKU); err == nil {
			if linked[variant.SKU] {
				row.Status = "exists"
				result.Skipped++
				result.Rows = append(result.Rows, row)
				continue
			}
			// The child exists but is not linked to the parent: an orphan left
			// by an earlier failed link. Repair it instead of abandoning it.
			if err := s.AddChild(ctx, parentSKU, variant.SKU); err != nil {
				row.Status = "failed"
				row.Error = importErrorMessage(err)
				result.Failed++
			} else {
				row.Status = "linked"
				result.Linked++
				linked[variant.SKU] = true
			}
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
		linked[variant.SKU] = true
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

// observeParentStock derives whether a configurable parent is salable from the
// MSI source items of its children, and returns a human-readable derivation.
//
// A child counts as in stock when at least one of its source items is enabled
// (status 1) with a positive quantity; the parent counts as in stock when at
// least one child is. Reservations and the stock-source topology are not
// applied, so the value is a conservative proxy for salable quantity: it can
// under-report (a child whose source is not linked to the parent's stock still
// counts) but it never reports stock that no source holds.
func (s *Service) observeParentStock(ctx context.Context, children []ChildProduct) (bool, string, error) {
	skus := make([]string, 0, len(children))
	for _, child := range children {
		skus = append(skus, child.SKU)
	}
	inStockChildren := 0
	units := 0.0
	for start := 0; start < len(skus); start += maxSourceItemQuerySKUs {
		end := start + maxSourceItemQuerySKUs
		if end > len(skus) {
			end = len(skus)
		}
		items, err := s.SourceItems(ctx, skus[start:end])
		if err != nil {
			return false, "", err
		}
		if len(items) >= maxSourceItemsRead {
			// The read hit its page cap, so the observation is incomplete and a
			// derived status could take a stocked parent offline. Report the
			// unobserved state instead (see MAG-13).
			return false, "", errs.New(errs.CodeInsufficientData,
				"the child source-item read hit its page cap; the parent stock status cannot be derived")
		}
		perSKU := make(map[string]float64, len(items))
		for _, item := range items {
			if item.Status != 1 || item.Quantity <= 0 {
				continue
			}
			perSKU[item.SKU] += item.Quantity
		}
		for _, quantity := range perSKU {
			units += quantity
		}
		inStockChildren += len(perSKU)
	}
	source := fmt.Sprintf("source_items: %d of %d children in stock, %s units observed",
		inStockChildren, len(skus), strconv.FormatFloat(units, 'f', -1, 64))
	return inStockChildren > 0, source, nil
}

// setParentStockStatus writes the legacy stock item's is_in_stock flag for a
// configurable parent. Magento keeps a legacy stock item for the parent whose
// flag is not updated by MSI source items alone; the value comes from
// observeParentStock so the parent is never forced in stock.
func (s *Service) setParentStockStatus(ctx context.Context, sku string, productID int, inStock bool) error {
	body := struct {
		StockItem struct {
			IsInStock bool `json:"is_in_stock" jsonschema:"True when the product is sellable at this source."`
		} `json:"stockItem"`
	}{}
	body.StockItem.IsInStock = inStock
	_, err := s.client.do(ctx, http.MethodPut,
		"/products/"+url.PathEscape(sku)+"/stockItems/"+strconv.Itoa(productID), nil, body)
	return err
}
