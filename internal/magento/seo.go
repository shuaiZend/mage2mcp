// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// SEOCheck is one deterministic content/SEO finding for a product.
type SEOCheck struct {
	Field    string `json:"field"`
	Status   string `json:"status" jsonschema:"Current status."` // pass, warn, fail
	Message  string `json:"message"`
	Optional bool   `json:"optional,omitempty"`
}

// SEOAudit reports content and SEO completeness. It never invents copy: it
// points at missing or weak fields so an operator or agent can improve them.
type SEOAudit struct {
	SKU    string     `json:"sku" jsonschema:"Product SKU."`
	Score  int        `json:"score" jsonschema:"0-100 completeness score for the SEO fields."` // percentage of passing required checks
	Checks []SEOCheck `json:"checks" jsonschema:"Per-check results of the SEO audit."`
}

// AuditSEO reads one product and evaluates its content/SEO fields.
func (s *Service) AuditSEO(ctx context.Context, sku string) (*SEOAudit, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento product service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku), nil, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		CustomAttributes []struct {
			AttributeCode string `json:"attribute_code" jsonschema:"Attribute code, not its id or label."`
			Value         any    `json:"value" jsonschema:"Value of the entry."`
		} `json:"custom_attributes"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable product payload").WithCause(err)
	}
	values := make(map[string]string, len(payload.CustomAttributes))
	for _, attr := range payload.CustomAttributes {
		if text, ok := attr.Value.(string); ok {
			values[attr.AttributeCode] = text
		}
	}
	audit := &SEOAudit{SKU: sku, Checks: make([]SEOCheck, 0, 6)}
	add := func(field, status, message string) {
		audit.Checks = append(audit.Checks, SEOCheck{Field: field, Status: status, Message: message})
	}
	description := values["description"]
	switch {
	case description == "":
		add("description", "fail", "missing description; add at least 300 characters of unique, keyword-relevant copy")
	case len(description) < 300:
		add("description", "warn", fmt.Sprintf("description is %d characters; aim for 300+", len(description)))
	default:
		add("description", "pass", "description length is adequate")
	}
	if values["short_description"] == "" {
		add("short_description", "warn", "missing short description; add a 1-2 sentence benefit statement")
	} else {
		add("short_description", "pass", "short description present")
	}
	urlKey := values["url_key"]
	switch {
	case urlKey == "":
		add("url_key", "warn", "missing url_key; Magento can derive it, but an explicit keyword slug is safer")
	case !urlKeyRE.MatchString(urlKey):
		add("url_key", "warn", "url_key should be lowercase letters, digits, and hyphens")
	default:
		add("url_key", "pass", "url_key is a valid slug")
	}
	metaTitle := values["meta_title"]
	switch {
	case metaTitle == "":
		add("meta_title", "warn", "missing meta_title; write 30-60 characters with the keyword and brand")
	case len(metaTitle) < 30 || len(metaTitle) > 60:
		add("meta_title", "warn", fmt.Sprintf("meta_title is %d characters; 30-60 is the useful range", len(metaTitle)))
	default:
		add("meta_title", "pass", "meta_title length is in the useful range")
	}
	metaDescription := values["meta_description"]
	switch {
	case metaDescription == "":
		add("meta_description", "warn", "missing meta_description; write 120-160 characters with a benefit and call to action")
	case len(metaDescription) < 120 || len(metaDescription) > 160:
		add("meta_description", "warn", fmt.Sprintf("meta_description is %d characters; 120-160 is the useful range", len(metaDescription)))
	default:
		add("meta_description", "pass", "meta_description length is in the useful range")
	}
	if values["meta_keywords"] == "" {
		add("meta_keywords", "warn", "optional: meta_keywords has little ranking value, but a short relevant list is harmless")
		audit.Checks[len(audit.Checks)-1].Optional = true
	} else {
		add("meta_keywords", "pass", "meta_keywords present")
	}
	scoreSEO(audit)
	return audit, nil
}

// scoreSEO computes the percentage of passing required checks; optional checks
// (for example meta_keywords on Magento builds without that attribute) do not
// lower the score.
func scoreSEO(audit *SEOAudit) {
	passed, required := 0, 0
	for _, check := range audit.Checks {
		if check.Optional {
			continue
		}
		required++
		if check.Status == "pass" {
			passed++
		}
	}
	if required > 0 {
		audit.Score = passed * 100 / required
	}
}
