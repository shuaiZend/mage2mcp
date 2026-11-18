// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func testGA4Service(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(Config{PropertyID: "123456", TokenSource: StaticToken("t")})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestResolverSingleInstanceDefaults(t *testing.T) {
	r, err := NewResolver(map[string]*Service{"giztop": testGA4Service(t)})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := r.Resolve("")
	if err != nil || svc == nil {
		t.Fatalf("Resolve(\"\") = %v, %v", svc, err)
	}
	if _, err := r.Resolve("other"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("unknown instance err = %v", err)
	}
	if got := r.Instances(); len(got) != 1 || got[0] != "giztop" {
		t.Fatalf("instances = %v", got)
	}
}

func TestResolverMultipleInstancesRequiresName(t *testing.T) {
	r, err := NewResolver(map[string]*Service{
		"a": testGA4Service(t),
		"b": testGA4Service(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty instance err = %v, want invalid_argument", err)
	}
	if _, err := r.Resolve("b"); err != nil {
		t.Fatalf("Resolve(b): %v", err)
	}
	if got := r.Instances(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("instances = %v", got)
	}
}

func TestNewResolverValidation(t *testing.T) {
	if _, err := NewResolver(nil); err == nil {
		t.Fatal("empty map accepted")
	}
	if _, err := NewResolver(map[string]*Service{"": testGA4Service(t)}); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := NewResolver(map[string]*Service{"a": nil}); err == nil {
		t.Fatal("nil service accepted")
	}
}
