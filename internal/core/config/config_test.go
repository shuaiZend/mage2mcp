// SPDX-License-Identifier: Apache-2.0

package config

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.Server.MaxBodyBytes != DefaultMaxBodyBytes || cfg.Server.ShutdownGrace != 10*time.Second {
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

// configEnvVars is every environment variable the loader reads (plus the
// server-only MAGE2MCP_DEV_INSECURE, cleared so a developer shell cannot change
// the result of a shipped-config test).
var configEnvVars = []string{
	"MAGE2MCP_LISTEN",
	"MAGE2MCP_AUTH_ISSUER",
	"MAGE2MCP_AUTH_AUDIENCE",
	"MAGE2MCP_AUTH_JWKS_URL",
	"MAGE2MCP_AUTH_ENABLED",
	"MAGE2MCP_LOG_LEVEL",
	"MAGE2MCP_TOOLS_WRITES",
	"MAGE2MCP_DATABASE_URL",
	"MAGE2MCP_DEV_INSECURE",
	// Removed from the schema; cleared so an inherited value cannot mask a
	// regression in the unknown-key test.
	"MAGE2MCP_PUBLIC_URL",
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range configEnvVars {
		t.Setenv(key, "")
	}
}

func shippedConfigsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "..", "configs")
}

// shippedConfigEnv lists the environment each shipped configuration needs to
// validate. A file with an empty entry must validate with no environment at
// all. Every YAML file under configs/ must appear here; the test fails when a
// new example is added without declaring what it needs, so a broken example
// cannot ship again.
var shippedConfigEnv = map[string]map[string]string{
	"config.example.yaml": {},
	"config.docker.yaml": {
		"MAGE2MCP_AUTH_ISSUER":   "https://team.cloudflareaccess.com",
		"MAGE2MCP_AUTH_AUDIENCE": "access-application-aud",
		"MAGE2MCP_AUTH_JWKS_URL": "https://team.cloudflareaccess.com/cdn-cgi/access/certs",
	},
	"config.production.template.yaml": {
		"MAGE2MCP_AUTH_ISSUER":   "https://team.cloudflareaccess.com",
		"MAGE2MCP_AUTH_AUDIENCE": "access-application-aud",
		"MAGE2MCP_AUTH_JWKS_URL": "https://team.cloudflareaccess.com/cdn-cgi/access/certs",
	},
}

func TestShippedConfigsValidate(t *testing.T) {
	dir := shippedConfigsDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read configs/: %v", err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		seen[entry.Name()] = true
		env, ok := shippedConfigEnv[entry.Name()]
		if !ok {
			t.Fatalf("configs/%s has no entry in shippedConfigEnv; declare the environment it needs", entry.Name())
		}
		t.Run(entry.Name(), func(t *testing.T) {
			clearConfigEnv(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			cfg, err := Load(filepath.Join(dir, entry.Name()))
			if err != nil {
				t.Fatalf("shipped config does not validate: %v", err)
			}
			if cfg.Server.Listen == "" || cfg.Server.MaxBodyBytes <= 0 {
				t.Fatalf("shipped config is incomplete: %+v", cfg.Server)
			}
		})
	}
	for name := range shippedConfigEnv {
		if !seen[name] {
			t.Fatalf("shippedConfigEnv lists %s but configs/%s does not exist", name, name)
		}
	}
}

// TestShippedConfigsRequireDocumentedEnv proves that the templates which need
// environment values fail with a precise message when they are missing, so the
// documentation can say exactly what must be configured first.
func TestShippedConfigsRequireDocumentedEnv(t *testing.T) {
	dir := shippedConfigsDir(t)
	for name, env := range shippedConfigEnv {
		if len(env) == 0 {
			continue
		}
		t.Run(name, func(t *testing.T) {
			clearConfigEnv(t)
			_, err := Load(filepath.Join(dir, name))
			if err == nil {
				t.Fatalf("%s validates with no environment; it is a template and must fail until configured", name)
			}
			if !strings.Contains(err.Error(), "auth.issuer") {
				t.Fatalf("%s missing-env error does not name auth.issuer: %v", name, err)
			}
		})
	}
}

// TestDevelopmentExampleStartsAsShipped pins the properties that make
// `cp configs/config.example.yaml config.yaml && ./bin/mage2mcp -config config.yaml`
// work: loopback listener, authentication off, no bindings, no enabled module.
func TestDevelopmentExampleStartsAsShipped(t *testing.T) {
	clearConfigEnv(t)
	cfg, err := Load(filepath.Join(shippedConfigsDir(t), "config.example.yaml"))
	if err != nil {
		t.Fatalf("development example does not load: %v", err)
	}
	if cfg.Auth.Enabled {
		t.Fatal("development example must disable authentication; it is loopback-only")
	}
	if len(cfg.Bindings) != 0 {
		t.Fatalf("development example declares bindings, which require three enabled modules: %+v", cfg.Bindings)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Fatalf("development example listen = %q, want loopback", cfg.Server.Listen)
	}
	if cfg.Modules.Magento.Enabled || cfg.Modules.Cloudflare.Enabled || cfg.Modules.GA4.Enabled {
		t.Fatal("development example must not enable a data source")
	}
}

// TestBindingsCurrentLimitation pins the single-zone/single-property rule the
// unified adapters impose today (ADR-0018). When the adapters become
// resolver-aware, this test and the validation block in Validate are removed
// together.
func TestBindingsCurrentLimitation(t *testing.T) {
	base := func() Config {
		c := Default()
		c.Auth.Enabled = false
		c.Modules.Magento = Module{Enabled: true, Instances: []Instance{{ID: "store-us", Credential: "x"}}}
		c.Modules.Cloudflare = Module{Enabled: true, Instances: []Instance{{ID: "zone-us", Credential: "x"}}}
		c.Modules.GA4 = Module{Enabled: true, Instances: []Instance{{ID: "giztop", Credential: "x"}}}
		c.Bindings = []Binding{{ID: "b1", Magento: "store-us", Cloudflare: "zone-us", GA4: "giztop"}}
		return c
	}

	t.Run("one zone and one property", func(t *testing.T) {
		if err := base().Validate(); err != nil {
			t.Fatalf("single-instance binding rejected: %v", err)
		}
	})

	t.Run("several magento stores", func(t *testing.T) {
		cfg := base()
		cfg.Modules.Magento.Instances = append(cfg.Modules.Magento.Instances, Instance{ID: "store-eu", Credential: "x"})
		if err := cfg.Validate(); err != nil {
			t.Fatalf("multi-store Magento binding rejected: %v", err)
		}
	})

	t.Run("two cloudflare zones", func(t *testing.T) {
		cfg := base()
		cfg.Modules.Cloudflare.Instances = append(cfg.Modules.Cloudflare.Instances, Instance{ID: "zone-eu", Credential: "x"})
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "exactly one Cloudflare instance") {
			t.Fatalf("multi-zone binding error = %v, want the single-zone limitation", err)
		}
	})

	t.Run("two ga4 properties", func(t *testing.T) {
		cfg := base()
		cfg.Modules.GA4.Instances = append(cfg.Modules.GA4.Instances, Instance{ID: "ga4-eu", Credential: "x"})
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "exactly one GA4 instance") {
			t.Fatalf("multi-property binding error = %v, want the single-property limitation", err)
		}
	})

	t.Run("disabled module", func(t *testing.T) {
		cfg := base()
		cfg.Modules.GA4 = Module{Enabled: false}
		err := cfg.Validate()
		if err == nil || !strings.Contains(err.Error(), "bindings require") {
			t.Fatalf("binding with disabled module error = %v, want an enabled-module requirement", err)
		}
	})
}

// TestPublicURLIsNotAConfigKey records the removal of the dead key: a config
// that still sets it must fail loudly rather than silently ignore it.
func TestPublicURLIsNotAConfigKey(t *testing.T) {
	clearConfigEnv(t)
	path := write(t, "server:\n  listen: \"127.0.0.1:1\"\n  public_url: \"https://mcp.example.com\"\n")
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "public_url") {
		t.Fatalf("public_url accepted or error does not name it: %v", err)
	}
}

// TestAuthEnabledEnvParsing pins the exact environment semantics documented in
// docs/configuration.md: only the lower-case "true" and "1" enable; any other
// non-empty value disables authentication.
func TestAuthEnabledEnvParsing(t *testing.T) {
	cases := map[string]bool{
		"":      true, // default when the variable is unset
		"true":  true,
		"1":     true,
		"false": false,
		"0":     false,
		"TRUE":  false, // documented trap: the exact lower-case form is required
		"yes":   false,
	}
	for value, want := range cases {
		t.Run("value="+value, func(t *testing.T) {
			t.Setenv("MAGE2MCP_AUTH_ENABLED", value)
			cfg := Default()
			applyEnv(&cfg)
			if cfg.Auth.Enabled != want {
				t.Fatalf("MAGE2MCP_AUTH_ENABLED=%q -> %v, want %v", value, cfg.Auth.Enabled, want)
			}
		})
	}
}

// TestToolsWritesEnvOnlyEnables pins the asymmetric override: the variable can
// turn writes on but cannot turn a YAML true back off.
func TestToolsWritesEnvOnlyEnables(t *testing.T) {
	t.Setenv("MAGE2MCP_TOOLS_WRITES", "true")
	cfg := Default()
	applyEnv(&cfg)
	if !cfg.Tools.Writes {
		t.Fatal("MAGE2MCP_TOOLS_WRITES=true did not enable writes")
	}
	t.Setenv("MAGE2MCP_TOOLS_WRITES", "false")
	cfg = Default()
	cfg.Tools.Writes = true
	applyEnv(&cfg)
	if !cfg.Tools.Writes {
		t.Fatal("MAGE2MCP_TOOLS_WRITES=false must not disable a YAML-enabled write profile")
	}
	// The exact lower-case form is required; TRUE is ignored.
	t.Setenv("MAGE2MCP_TOOLS_WRITES", "TRUE")
	cfg = Default()
	applyEnv(&cfg)
	if cfg.Tools.Writes {
		t.Fatal("MAGE2MCP_TOOLS_WRITES=TRUE must not enable writes; use the lower-case form")
	}
}

// TestBodyCapDefaultsAndImportRelationship pins the shipped body cap and the
// floor a write-enabled deployment must respect. The cross-package derivation
// (that the floor actually admits an 8 MiB import) lives in
// internal/magento's TestImportLimitsAgreeAcrossPackages, which owns the import
// ceiling and therefore the arithmetic.
func TestBodyCapDefaultsAndImportRelationship(t *testing.T) {
	if DefaultMaxBodyBytes != 16<<20 {
		t.Fatalf("DefaultMaxBodyBytes = %d, want %d (16 MiB)", DefaultMaxBodyBytes, 16<<20)
	}
	if got := Default().Server.MaxBodyBytes; got != DefaultMaxBodyBytes {
		t.Fatalf("Default().Server.MaxBodyBytes = %d, want %d", got, DefaultMaxBodyBytes)
	}
	if MinMaxBodyBytesForImport != 12<<20 {
		t.Fatalf("MinMaxBodyBytesForImport = %d, want %d (12 MiB)", MinMaxBodyBytesForImport, 12<<20)
	}
	if MinMaxBodyBytesForImport > DefaultMaxBodyBytes {
		t.Fatalf("the import floor %d exceeds the shipped default %d",
			MinMaxBodyBytesForImport, DefaultMaxBodyBytes)
	}
}

// TestValidateBodyCapVersusWrites proves an operator who lowers the cap below
// what a maximum import needs gets a startup error when writes are enabled,
// while a read-only deployment may harden the cap freely.
func TestValidateBodyCapVersusWrites(t *testing.T) {
	base := func() Config {
		cfg := Default()
		cfg.Auth.Enabled = false
		cfg.Server.Listen = "127.0.0.1:8080"
		return cfg
	}

	t.Run("writes enabled with the default cap", func(t *testing.T) {
		cfg := base()
		cfg.Tools.Writes = true
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("writes enabled at the import floor", func(t *testing.T) {
		cfg := base()
		cfg.Tools.Writes = true
		cfg.Server.MaxBodyBytes = MinMaxBodyBytesForImport
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate at the floor: %v", err)
		}
	})

	t.Run("writes enabled below the floor", func(t *testing.T) {
		cfg := base()
		cfg.Tools.Writes = true
		cfg.Server.MaxBodyBytes = 1 << 20
		err := cfg.Validate()
		if err == nil {
			t.Fatal("a write-enabled deployment below the import floor must fail validation")
		}
		for _, want := range []string{"max_body_bytes", "tools.writes", "12582912", "16777216"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("read-only deployment may harden the cap", func(t *testing.T) {
		cfg := base()
		cfg.Tools.Writes = false
		cfg.Server.MaxBodyBytes = 64 << 10
		if err := cfg.Validate(); err != nil {
			t.Fatalf("a read-only deployment must be allowed a small cap: %v", err)
		}
	})

	t.Run("non-positive cap is still rejected", func(t *testing.T) {
		cfg := base()
		cfg.Server.MaxBodyBytes = 0
		if err := cfg.Validate(); err == nil {
			t.Fatal("max_body_bytes 0 must fail validation")
		}
	})
}

func TestValidateInstanceLimits(t *testing.T) {
	base := func() Config {
		cfg := Default()
		cfg.Auth.Enabled = false
		cfg.Modules.Magento = Module{Enabled: true, Instances: []Instance{
			{ID: "store-us", Endpoint: "https://shop.example.com", StoreCode: "default", Credential: "oauth1:a:b:c:d"},
		}}
		return cfg
	}
	cases := []struct {
		name string
		lim  *InstanceLimits
		ok   bool
	}{
		{"absent uses the module default", nil, true},
		{"typical budget", &InstanceLimits{RequestsPerSecond: 20, Burst: 40, MaxConcurrent: 8}, true},
		{"concurrency only", &InstanceLimits{MaxConcurrent: 4}, true},
		{"explicit all-zero opts out", &InstanceLimits{}, true},
		{"negative rate", &InstanceLimits{RequestsPerSecond: -1}, false},
		{"negative burst", &InstanceLimits{Burst: -1}, false},
		{"negative concurrency", &InstanceLimits{MaxConcurrent: -1}, false},
		{"burst without rate", &InstanceLimits{Burst: 10}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base()
			cfg.Modules.Magento.Instances[0].Limits = tc.lim
			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("Validate() = nil, want an error for %+v", tc.lim)
			}
		})
	}
}
