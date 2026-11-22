// SPDX-License-Identifier: Apache-2.0

package magento

// Contract tests for the two places where the same fact is written down twice:
// the invoice capture semantics (struct comment + registered tool description)
// and the credit memo's shipping_amount (JSON schema + field reference page).
//
// Both were fixed in one place and left stale in the other, which is the failure
// mode these tests exist to stop: a reader believes whichever copy they happen to
// open, and the two disagree.

import (
	"os"
	"strings"
	"testing"
)

// TestInvoiceCapturedCommentMatchesTheDescription pins the capture semantics that
// U-14 corrected: `captured` is "this call asked for capture AND the invoice came
// back paid", never "a paid invoice proves the capture happened". An offline
// payment method marks the invoice paid either way, so the state alone proves
// nothing.
//
// The registered description was corrected first; this keeps the source comment
// from drifting back to the old claim, and keeps the two copies saying the same
// thing.
func TestInvoiceCapturedCommentMatchesTheDescription(t *testing.T) {
	source, err := os.ReadFile("invoice_write.go")
	if err != nil {
		t.Fatalf("read invoice_write.go: %v", err)
	}
	comment := string(source)
	description := invoiceCreateDescription

	// The exact sentences the old comment and the old description carried. Any of
	// them coming back means the "state proves capture" claim is back.
	stale := []string{
		"proves the capture flag was honoured",
		"proves whether it did",
		"the returned invoice state (2 = paid)",
		"the returned invoice state proves",
	}
	// A Go comment wraps mid-sentence, so the claim is matched against the source
	// with the comment markers and the line breaks collapsed away. Otherwise a
	// reverted sentence would slip past purely because it spans two lines.
	for where, text := range map[string]string{
		"invoice_write.go comments":  flattenProse(comment),
		"invoice.create description": flattenProse(description),
	} {
		for _, claim := range stale {
			if strings.Contains(text, claim) {
				t.Fatalf("%s still claims %q; state cannot prove a capture (checkmo marks the invoice paid either way)", where, claim)
			}
		}
	}

	// What both copies must say instead: the intent+state combination, the offline
	// payment caveat, and the "read captured, not state" instruction.
	required := []struct {
		where  string
		text   string
		phrase string
		why    string
	}{
		{"invoice_write.go comments", comment, "asked for capture", "the field combines the capture intent with the returned state"},
		{"invoice_write.go comments", comment, "came back paid", "the field requires the invoice to be paid as well"},
		{"invoice_write.go comments", comment, "checkmo", "an offline payment method is the reason state proves nothing"},
		{"invoice_write.go comments", comment, "not proof", "the comment must say the flag is not proof that money moved"},
		{"invoice.create description", description, "asked for capture", "the description must describe the same combination"},
		{"invoice.create description", description, "came back paid", "the description must describe the same combination"},
		{"invoice.create description", description, "checkmo", "the description must name the offline payment caveat"},
		{"invoice.create description", description, "read captured, not state", "the description must tell the caller what to read"},
	}
	for _, want := range required {
		if !strings.Contains(strings.ToLower(want.text), want.phrase) {
			t.Fatalf("%s does not say %q: %s", want.where, want.phrase, want.why)
		}
	}

	// The implementation must match the words: captured is the conjunction, not
	// the state alone.
	if !strings.Contains(comment, "Captured:    in.Capture && invoice.State == invoiceStatePaid") {
		t.Fatal("Captured is no longer `capture requested AND paid`; the comment and the description describe a different field")
	}
}

// TestFieldReferenceDocumentsCreditmemoShippingAmount pins the discoverability
// half of GG-01: shipping_amount was wired through the schema, the input struct
// and the handler, but the field reference page did not list it, so an agent
// reading the reference could not find the parameter at all.
//
// It also pins the three semantics, because a bare parameter name would not tell
// a caller that omitting it changes what a partial refund returns.
func TestFieldReferenceDocumentsCreditmemoShippingAmount(t *testing.T) {
	page, err := os.ReadFile("../../docs/magento-fields.md")
	if err != nil {
		t.Fatalf("read docs/magento-fields.md: %v", err)
	}
	row := fieldReferenceRow(t, string(page), "`magento.creditmemo.create`")

	lower := strings.ToLower(row)
	for _, want := range []struct{ phrase, why string }{
		{"shipping_amount", "the parameter must be listed by name"},
		{"omit", "the caller must know what omitting it does"},
		{"no shipping", "omitting must be stated as refunding no shipping for a partial refund"},
		{"remaining shipping", "the last-unit case refunds the remaining shipping"},
		{"`0`", "zero must be documented as an explicit no-refund"},
	} {
		if !strings.Contains(lower, want.phrase) {
			t.Fatalf("the magento.creditmemo.create row does not say %q: %s\nrow: %s", want.phrase, want.why, row)
		}
	}

	// The schema is the source of truth for the parameter: if it ever drops
	// shipping_amount, the documentation would describe a field that cannot be
	// sent.
	if !strings.Contains(creditmemoCreateSchema, `"shipping_amount"`) {
		t.Fatal("creditmemoCreateSchema no longer declares shipping_amount, but the field reference documents it")
	}
}

// flattenProse lowercases text and collapses comment markers and whitespace, so a
// sentence that wraps across lines still matches as one string.
func flattenProse(text string) string {
	text = strings.ReplaceAll(text, "//", " ")
	text = strings.ReplaceAll(text, "\t", " ")
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

// fieldReferenceRow returns the markdown table row that starts with name.
func fieldReferenceRow(t *testing.T, page, name string) string {
	t.Helper()
	for _, line := range strings.Split(page, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "| "+name) {
			return line
		}
	}
	t.Fatalf("docs/magento-fields.md has no row for %s", name)
	return ""
}
