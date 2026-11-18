// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"testing"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func testCloudflareService(t *testing.T) *Service {
	t.Helper()
	client, err := NewClient(Config{APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "t", ZoneID: "z"})
	if err != nil {
		t.Fatal(err)
	}
	return NewService(client)
}

func TestResolverSingleZoneDefaults(t *testing.T) {
	r, err := NewResolver(map[string]*Service{"zone-us": testCloudflareService(t)})
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

func TestResolverMultipleZonesRequireName(t *testing.T) {
	r, err := NewResolver(map[string]*Service{
		"zone-a": testCloudflareService(t),
		"zone-b": testCloudflareService(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(""); errs.CodeOf(err) != errs.CodeInvalidArgument {
		t.Fatalf("empty instance err = %v", err)
	}
	if _, err := r.Resolve("zone-b"); err != nil {
		t.Fatalf("Resolve(zone-b): %v", err)
	}
	if got := r.Instances(); len(got) != 2 || got[0] != "zone-a" {
		t.Fatalf("instances = %v", got)
	}
}

func TestNewResolverValidation(t *testing.T) {
	if _, err := NewResolver(nil); err == nil {
		t.Fatal("empty map accepted")
	}
	if _, err := NewResolver(map[string]*Service{"": testCloudflareService(t)}); err == nil {
		t.Fatal("empty id accepted")
	}
	if _, err := NewResolver(map[string]*Service{"a": nil}); err == nil {
		t.Fatal("nil service accepted")
	}
}
