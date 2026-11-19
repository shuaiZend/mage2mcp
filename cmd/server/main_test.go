// SPDX-License-Identifier: Apache-2.0

package main

import (
	"log/slog"
	"testing"

	"github.com/shuaiZend/mage2mcp/internal/core/config"
	"github.com/shuaiZend/mage2mcp/internal/magento"
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

func TestMagentoSignerForms(t *testing.T) {
	if _, err := magento.ParseCredential("bearer:abc"); err != nil {
		t.Fatalf("bearer: %v", err)
	}
	if _, err := magento.ParseCredential("oauth1:k:s:t:ts"); err != nil {
		t.Fatalf("oauth1: %v", err)
	}
	if _, err := magento.ParseCredential("oauth1:bad"); err == nil {
		t.Fatal("malformed oauth1 credential accepted")
	}
	if _, err := magento.ParseCredential("plain-token"); err == nil {
		t.Fatal("unsupported credential form accepted")
	}
}

func TestRegisterModulesBuildsUnifiedTool(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:x",
	}}}
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "zone-us", ZoneID: "z", AccountID: "a", Credential: "t",
	}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "giztop", PropertyID: "123456", Credential: "ga-token",
	}}}
	cfg.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("unified.traffic.compare"); !ok {
		t.Fatal("unified.traffic.compare was not registered")
	}
}

func TestRegisterModulesRequiresSourcesForBindings(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{{
		ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "bearer:x",
	}}}
	cfg.Bindings = []config.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
	if err := registerModules(cfg, registry.New()); err == nil {
		t.Fatal("expected missing cloudflare/ga4 to be rejected")
	}
}

func TestRegisterModulesSupportsMultipleCloudflareZones(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "zone-a", ZoneID: "za", Credential: "t"},
		{ID: "zone-b", ZoneID: "zb", Credential: "t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("cloudflare.analytics.traffic"); !ok {
		t.Fatal("traffic tool not registered")
	}
}

func TestConfiguredInstances(t *testing.T) {
	cfg := config.Default()
	cfg.Modules.Cloudflare = config.Module{Enabled: true, Instances: []config.Instance{{ID: "zone-a"}, {ID: "zone-b"}}}
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{{ID: "giztop"}}}
	got := configuredInstances(cfg)
	if len(got["cloudflare"]) != 2 || len(got["ga4"]) != 1 {
		t.Fatalf("instances = %+v", got)
	}
	if _, ok := got["magento"]; ok {
		t.Fatal("disabled module must not be reported")
	}
}

func TestRegisterModulesSupportsMultipleGA4Properties(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.GA4 = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "prop-a", PropertyID: "111", Credential: "t"},
		{ID: "prop-b", PropertyID: "222", Credential: "t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	for _, name := range []string{"ga4.analytics.traffic", "ga4.property.metadata", "ga4.property.capabilities", "ga4.analytics.realtime", "ga4.ecommerce.overview"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("%s not registered", name)
		}
	}
}

func TestRegisterModulesSupportsMultipleMagentoStoresForReads(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "store-us", Endpoint: "https://us.example.com", StoreCode: "default", Credential: "bearer:t"},
		{ID: "store-eu", Endpoint: "https://eu.example.com", StoreCode: "default", Credential: "bearer:t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	for _, name := range []string{"magento.product.get", "magento.product.search", "magento.import.validate"} {
		if _, ok := reg.Lookup(name); !ok {
			t.Fatalf("%s not registered", name)
		}
	}
}

func TestRegisterModulesSupportsMultiStoreWrites(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Tools.Writes = true
	cfg.Modules.Magento = config.Module{Enabled: true, Instances: []config.Instance{
		{ID: "store-us", Endpoint: "https://us.example.com", StoreCode: "default", Credential: "bearer:t"},
		{ID: "store-eu", Endpoint: "https://eu.example.com", StoreCode: "default", Credential: "bearer:t"},
	}}
	reg := registry.New()
	if err := registerModules(cfg, reg); err != nil {
		t.Fatalf("registerModules: %v", err)
	}
	if _, ok := reg.Lookup("magento.product.create"); !ok {
		t.Fatal("write tool not registered for multi-store")
	}
}

func TestBuildAuthAllowsDevInsecureOnNonLoopback(t *testing.T) {
	cfg := config.Default()
	cfg.Auth.Enabled = false
	cfg.Server.Listen = "0.0.0.0:8080"
	if _, err := buildAuth(cfg, slog.Default(), false); err == nil {
		t.Fatal("non-loopback without auth must be rejected by default")
	}
	if _, err := buildAuth(cfg, slog.Default(), true); err != nil {
		t.Fatalf("dev-insecure should allow non-loopback: %v", err)
	}
}
