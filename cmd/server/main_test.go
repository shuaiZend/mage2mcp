// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

func TestIsLoopbackListen(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8080": true,
		"localhost:8080": true,
		"[::1]:8080":     true,
		"0.0.0.0:8080":   false,
		":8080":          false,
		"10.0.0.5:8080":  false,
		"garbage":        false,
	}
	for listen, want := range cases {
		if got := isLoopbackListen(listen); got != want {
			t.Errorf("isLoopbackListen(%q) = %v, want %v", listen, got, want)
		}
	}
}

func TestRegisterModulesBuildsTools(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default",
		Credential: "bearer:test-token",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone-us", ZoneID: "zone-id", AccountID: "account-id", Credential: "cf-token",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "giztop", PropertyID: "123456", Credential: "ga-token",
	}}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	want := map[string]bool{
		"magento.product.get":          false,
		"magento.product.search":       false,
		"cloudflare.analytics.traffic": false,
		"cloudflare.security.summary":  false,
		"ga4.analytics.traffic":        false,
		"ga4.property.metadata":        false,
	}
	for _, tool := range reg.List() {
		if _, ok := want[tool.Name]; ok {
			want[tool.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tool %s was not registered", name)
		}
	}
}

func TestRegisterModulesRejectsMultipleInstances(t *testing.T) {
	cfg := config.Default()
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "a", Endpoint: "https://a.example.com", StoreCode: "default", Credential: "bearer:x"},
		{ID: "b", Endpoint: "https://b.example.com", StoreCode: "default", Credential: "bearer:y"},
	}}
	if err := registerModules(cfg, registry.New()); err == nil {
		t.Fatal("expected multi-instance to be rejected")
	}
}

func TestMagentoSignerForms(t *testing.T) {
	if _, err := magentoSigner("bearer:abc"); err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if _, err := magentoSigner("oauth1:k:s:t:ts"); err != nil {
		t.Fatalf("oauth1: %v", err)
	}
	if _, err := magentoSigner("oauth1:bad"); err == nil {
		t.Fatal("malformed oauth1 credential accepted")
	}
	if _, err := magentoSigner("plain-token"); err == nil {
		t.Fatal("unsupported credential form accepted")
	}
}
