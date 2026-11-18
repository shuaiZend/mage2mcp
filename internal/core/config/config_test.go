// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("MAGE2MCP_AUTH_AUDIENCE", "aud-from-env")
	path := write(t, `
server:
  listen: "127.0.0.1:9090"
auth:
  enabled: true
  issuer: "https://team.cloudflareaccess.com"
  jwks_url: "https://team.cloudflareaccess.com/cdn-cgi/access/certs"
log:
  level: debug
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "127.0.0.1:9090" || cfg.Auth.Audience != "aud-from-env" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Server.MaxBodyBytes != 1<<20 || cfg.Server.ShutdownGrace != 10*time.Second {
		t.Fatalf("defaults not applied: %+v", cfg.Server)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	path := write(t, "server:\n  listen: \"127.0.0.1:1\"\n  typo: true\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
}

func TestValidateAuthRequiresIssuerAndAudience(t *testing.T) {
	cfg := Default()
	cfg.Auth.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected auth validation failure")
	}
}

func TestValidateModuleInstance(t *testing.T) {
	cfg := Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = Module{Enabled: true, Instances: []Instance{{ID: "store-us"}}}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected missing credential to be rejected")
	}
}

func TestEnvExpansion(t *testing.T) {
	t.Setenv("TEST_ISSUER", "https://issuer.example.com")
	path := write(t, "auth:\n  enabled: false\n  issuer: \"${TEST_ISSUER}\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Issuer != "https://issuer.example.com" {
		t.Fatalf("expansion failed: %q", cfg.Auth.Issuer)
	}
}

func TestValidateRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"empty listen", func(c *Config) { c.Server.Listen = "" }},
		{"negative grace", func(c *Config) { c.Server.ShutdownGrace = -1 }},
		{"zero body cap", func(c *Config) { c.Server.MaxBodyBytes = 0 }},
		{"bad log level", func(c *Config) { c.Log.Level = "verbose" }},
		{"bad log format", func(c *Config) { c.Log.Format = "xml" }},
		{"module without instances", func(c *Config) { c.Modules.GA4 = Module{Enabled: true} }},
		{"duplicate instance", func(c *Config) {
			c.Modules.GA4 = Module{Enabled: true, Instances: []Instance{
				{ID: "a", Credential: "x"}, {ID: "a", Credential: "y"},
			}}
		}},
		{"non-https issuer", func(c *Config) {
			c.Auth = Auth{Enabled: true, Issuer: "http://insecure", Audience: "a", JWKSURL: "https://x"}
		}},
		{"non-https jwks", func(c *Config) {
			c.Auth = Auth{Enabled: true, Issuer: "https://i", Audience: "a", JWKSURL: "http://x"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Auth.Enabled = false
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatalf("expected %s to be rejected", tc.name)
			}
		})
	}
}

func TestEnvOverridesListenAndLog(t *testing.T) {
	t.Setenv("MAGE2MCP_LISTEN", "127.0.0.1:9999")
	t.Setenv("MAGE2MCP_LOG_LEVEL", "error")
	path := write(t, "auth:\n  enabled: false\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:9999" || cfg.Log.Level != "error" {
		t.Fatalf("env overrides not applied: %+v", cfg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestBindingValidation(t *testing.T) {
	base := func() Config {
		c := Default()
		c.Auth.Enabled = false
		c.Modules.Magento = Module{Enabled: true, Instances: []Instance{{ID: "store-us", Credential: "x"}}}
		c.Modules.Cloudflare = Module{Enabled: true, Instances: []Instance{{ID: "zone-us", Credential: "x"}}}
		c.Modules.GA4 = Module{Enabled: true, Instances: []Instance{{ID: "giztop", Credential: "x"}}}
		return c
	}
	valid := base()
	valid.Bindings = []Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	unknown := base()
	unknown.Bindings = []Binding{{ID: "b1", Magento: "nope", Cloudflare: "zone-us", GA4: "giztop"}}
	if err := unknown.Validate(); err == nil {
		t.Fatal("unknown instance accepted")
	}
	dup := base()
	dup.Bindings = []Binding{
		{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"},
		{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"},
	}
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate binding accepted")
	}
}

func TestInstanceIDsMustBeGloballyUnique(t *testing.T) {
	cfg := Default()
	cfg.Auth.Enabled = false
	cfg.Modules.Magento = Module{Enabled: true, Instances: []Instance{{ID: "shared", Credential: "x"}}}
	cfg.Modules.GA4 = Module{Enabled: true, Instances: []Instance{{ID: "shared", Credential: "x"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("cross-module duplicate instance id accepted")
	}
	cfg.Modules.GA4.Instances[0].ID = "giztop"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("distinct ids rejected: %v", err)
	}
}
