// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func testMagentoService(t *testing.T) *Service {
	t.Helper()
	client, err := NewClient(ClientConfig{BaseURL: "https://shop.example.com", StoreCode: "default", Signer: &BearerSigner{Token: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(client)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestResolverSingleStoreDefaults(t *testing.T) {
	r, err := NewResolver(map[string]*Service{"store-us": testMagentoService(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(""); err != nil {
		t.Fatalf("Resolve(\"\") = %v", err)
	}
	if _, err := r.Resolve("other"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("unknown instance err = %v", err)
	}
}

func TestResolverMultipleStoresRequireName(t *testing.T) {
	r, err := NewResolver(map[string]*Service{
		"store-us": testMagentoService(t),
		"store-eu": testMagentoService(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty instance err = %v", err)
	}
	if _, err := r.Resolve("store-eu"); err != nil {
		t.Fatalf("Resolve(store-eu): %v", err)
	}
	if got := r.Instances(); len(got) != 2 || got[0] != "store-eu" {
		t.Fatalf("instances = %v", got)
	}
}

func TestNewResolverValidation(t *testing.T) {
	if _, err := NewResolver(nil); err == nil {
		t.Fatal("empty map accepted")
	}
	if _, err := NewResolver(map[string]*Service{"": testMagentoService(t)}); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := NewResolver(map[string]*Service{"a": nil}); err == nil {
		t.Fatal("nil service accepted")
	}
}
