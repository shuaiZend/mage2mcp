// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func validTool() Tool {
	return Tool{
		Name:        "magento.product.get",
		Title:       "Get product",
		Description: "Returns one product by SKU.",
		Capability:  "magento.product.read",
		Permission:  PermissionRead,
		Timeout:     30 * time.Second,
		Annotations: mcp.ToolAnnotations{ReadOnlyHint: true},
		Register:    func(*mcp.Server) error { return nil },
	}
}

func TestRegisterAcceptsValidTool(t *testing.T) {
	r := New()
	if err := r.Register(validTool()); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d", r.Len())
	}
	if _, ok := r.Lookup("magento.product.get"); !ok {
		t.Fatal("tool not found")
	}
}

func TestRegisterRejectsInvalidTools(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Tool)
	}{
		{"bad name", func(t *Tool) { t.Name = "ProductGet" }},
		{"missing module prefix", func(t *Tool) { t.Name = "product.get" }},
		{"empty title", func(t *Tool) { t.Title = "" }},
		{"long title", func(t *Tool) { t.Title = string(make([]byte, 61)) }},
		{"empty description", func(t *Tool) { t.Description = "" }},
		{"missing capability", func(t *Tool) { t.Capability = "" }},
		{"zero timeout", func(t *Tool) { t.Timeout = 0 }},
		{"excessive timeout", func(t *Tool) { t.Timeout = 3 * time.Minute }},
		{"nil register", func(t *Tool) { t.Register = nil }},
		{"read without hint", func(t *Tool) { t.Annotations.ReadOnlyHint = false }},
		{"unknown permission", func(t *Tool) { t.Permission = "superuser" }},
		{"destructive without confirmation", func(t *Tool) {
			t.Permission = PermissionDestructive
			t.Annotations = mcp.ToolAnnotations{DestructiveHint: boolPtr(true)}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := validTool()
			tc.mutate(&tool)
			if err := Validate(tool); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestRegisterRejectsDuplicate(t *testing.T) {
	r := New()
	if err := r.Register(validTool()); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(validTool()); err == nil {
		t.Fatal("expected duplicate rejection")
	}
}

func TestListIsSorted(t *testing.T) {
	r := New()
	b := validTool()
	b.Name = "magento.product.search"
	a := validTool()
	if err := r.Register(b); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	list := r.List()
	if list[0].Name != "magento.product.get" || list[1].Name != "magento.product.search" {
		t.Fatalf("not sorted: %v", list)
	}
}

func boolPtr(v bool) *bool { return &v }

func TestInputValidator(t *testing.T) {
	validator, err := NewInputValidator(`{"type":"object","required":["sku"],"additionalProperties":false,"properties":{"sku":{"type":"string"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := validator([]byte(`{"sku":"SKU-1"}`)); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	if err := validator([]byte(`{"sku":"SKU-1","extra":true}`)); err == nil {
		t.Fatal("additional property accepted")
	}
	if err := validator([]byte(`{}`)); err == nil {
		t.Fatal("missing required property accepted")
	}
	if _, err := NewInputValidator(`{not json`); err == nil {
		t.Fatal("invalid schema accepted")
	}
}
