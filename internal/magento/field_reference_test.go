// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"os"
	"strings"
	"testing"
)

// TestFieldReferenceMatchesSchemas keeps docs/magento-fields.md from drifting:
// every SEO/content field and the confirmation field must appear in both the
// reference and the write schemas.
func TestFieldReferenceMatchesSchemas(t *testing.T) {
	raw, err := os.ReadFile("../../docs/magento-fields.md")
	if err != nil {
		t.Fatalf("read field reference: %v", err)
	}
	reference := string(raw)
	for _, field := range []string{
		"description", "short_description", "url_key", "meta_title", "meta_keywords", "meta_description",
	} {
		if !strings.Contains(reference, "`"+field+"`") {
			t.Fatalf("field reference does not document %q", field)
		}
		if !strings.Contains(productCreateSchema, `"`+field+`"`) {
			t.Fatalf("product create schema lacks %q", field)
		}
	}
	if !strings.Contains(reference, "confirmation_id") {
		t.Fatal("field reference does not mention confirmation_id")
	}
	for _, tool := range []string{
		"magento.product.create", "magento.configurable.create", "magento.invoice.create",
		"magento.shipment.create", "magento.creditmemo.create", "magento.product.media.upload",
		"magento.import.start", "magento.import.execute",
	} {
		if !strings.Contains(reference, tool) {
			t.Fatalf("field reference does not mention %s", tool)
		}
	}
}
