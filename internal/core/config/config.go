// SPDX-License-Identifier: Apache-2.0

// Package config loads and validates the gateway configuration.
//
// YAML defines the structure; ${VAR} references and a small set of explicit
// environment overrides supply deployment-specific values. Unknown keys are
// rejected so a typo cannot silently disable a control.
package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the root configuration document.
type Config struct {
	Server   Server    `yaml:"server"`
	Auth     Auth      `yaml:"auth"`
	Log      Log       `yaml:"log"`
	Database Database  `yaml:"database"`
	Tools    Tools     `yaml:"tools"`
	Modules  Modules   `yaml:"datasources"`
	Bindings []Binding `yaml:"bindings"`
}

// Binding is a declared Magento/Cloudflare/GA4 triple used by unified tools.
type Binding struct {
	ID         string `yaml:"id"`
	Magento    string `yaml:"magento"`
	Cloudflare string `yaml:"cloudflare"`
	GA4        string `yaml:"ga4"`
}

// Tools controls optional tool surfaces. Writes stay disabled until the
// confirmation middleware ships: a write tool without approval is unsafe.
type Tools struct {
	Writes bool `yaml:"writes"`
}

// Database configures the optional PostgreSQL store. An empty URL keeps the
// gateway on in-memory audit and job state (development only).
type Database struct {
	URL      string `yaml:"url"`
	MaxConns int32  `yaml:"max_conns"`
}

// Server holds listener settings.
type Server struct {
	Listen        string        `yaml:"listen"`
	PublicURL     string        `yaml:"public_url"`
	ShutdownGrace time.Duration `yaml:"shutdown_grace"`
	MaxBodyBytes  int64         `yaml:"max_body_bytes"`
}

// Auth holds Cloudflare Access verification settings.
type Auth struct {
	Enabled  bool   `yaml:"enabled"`
	Issuer   string `yaml:"issuer"`
	Audience string `yaml:"audience"`
	JWKSURL  string `yaml:"jwks_url"`
}

// Log holds logging settings.
type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Modules groups the data source configurations.
type Modules struct {
	Magento    Module `yaml:"magento"`
	Cloudflare Module `yaml:"cloudflare"`
	GA4        Module `yaml:"ga4"`
}

// Module is one data source's configuration.
type Module struct {
	Enabled   bool       `yaml:"enabled"`
	Instances []Instance `yaml:"instances"`
}

// Instance is one configured upstream.
type Instance struct {
	ID         string `yaml:"id"`
	Endpoint   string `yaml:"endpoint"`
	StoreCode  string `yaml:"store_code"`
	AccountID  string `yaml:"account_id"`
	ZoneID     string `yaml:"zone_id"`
	PropertyID string `yaml:"property_id"`
	Credential string `yaml:"credential"`
}

// Default returns the safe defaults: loopback listener, auth required, info logs.
func Default() Config {
	return Config{
		Server: Server{
			Listen:        "127.0.0.1:8080",
			ShutdownGrace: 10 * time.Second,
			MaxBodyBytes:  1 << 20,
		},
		Auth:     Auth{Enabled: true},
		Log:      Log{Level: "info", Format: "json"},
		Database: Database{MaxConns: 10},
		Tools:    Tools{Writes: false},
	}
}

// Load reads a YAML file, expands ${VAR} references, applies environment
// overrides, and validates the result.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path) // #nosec G304 -- the config path is an operator-supplied CLI argument
	if err != nil {
		return Config{}, fmt.Errorf("config: read %s: %w", path, err)
	}
	expanded := os.ExpandEnv(string(raw))
	dec := yaml.NewDecoder(bytes.NewReader([]byte(expanded)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
	}
	applyEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyEnv(c *Config) {
	if v := os.Getenv("MAGE2MCP_LISTEN"); v != "" {
		c.Server.Listen = v
	}
	if v := os.Getenv("MAGE2MCP_PUBLIC_URL"); v != "" {
		c.Server.PublicURL = v
	}
	if v := os.Getenv("MAGE2MCP_AUTH_ISSUER"); v != "" {
		c.Auth.Issuer = v
	}
	if v := os.Getenv("MAGE2MCP_AUTH_AUDIENCE"); v != "" {
		c.Auth.Audience = v
	}
	if v := os.Getenv("MAGE2MCP_AUTH_JWKS_URL"); v != "" {
		c.Auth.JWKSURL = v
	}
	if v := os.Getenv("MAGE2MCP_AUTH_ENABLED"); v != "" {
		c.Auth.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("MAGE2MCP_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("MAGE2MCP_TOOLS_WRITES"); v == "true" || v == "1" {
		c.Tools.Writes = true
	}
	if v := os.Getenv("MAGE2MCP_DATABASE_URL"); v != "" {
		c.Database.URL = v
	}
}

// Validate rejects an unsafe or incomplete configuration.
func (c Config) Validate() error {
	var problems []string
	if c.Server.Listen == "" {
		problems = append(problems, "server.listen is required")
	}
	if c.Server.ShutdownGrace < 0 {
		problems = append(problems, "server.shutdown_grace must not be negative")
	}
	if c.Server.MaxBodyBytes <= 0 {
		problems = append(problems, "server.max_body_bytes must be positive")
	}
	if c.Auth.Enabled {
		if !strings.HasPrefix(c.Auth.Issuer, "https://") {
			problems = append(problems, "auth.issuer must be an https URL")
		}
		if c.Auth.Audience == "" {
			problems = append(problems, "auth.audience is required when auth is enabled")
		}
		if !strings.HasPrefix(c.Auth.JWKSURL, "https://") {
			problems = append(problems, "auth.jwks_url must be an https URL")
		}
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, "log.level must be debug, info, warn, or error")
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		problems = append(problems, "log.format must be json or text")
	}
	if c.Database.URL != "" {
		if !strings.HasPrefix(c.Database.URL, "postgres://") && !strings.HasPrefix(c.Database.URL, "postgresql://") {
			problems = append(problems, "database.url must be a postgres:// or postgresql:// URL")
		}
		if c.Database.MaxConns <= 0 {
			problems = append(problems, "database.max_conns must be positive")
		}
	}
	for name, m := range map[string]Module{
		"magento": c.Modules.Magento, "cloudflare": c.Modules.Cloudflare, "ga4": c.Modules.GA4,
	} {
		if !m.Enabled {
			continue
		}
		if len(m.Instances) == 0 {
			problems = append(problems, name+": enabled module needs at least one instance")
		}
		seen := map[string]bool{}
		for i, inst := range m.Instances {
			if inst.ID == "" {
				problems = append(problems, fmt.Sprintf("%s.instances[%d].id is required", name, i))
				continue
			}
			if seen[inst.ID] {
				problems = append(problems, fmt.Sprintf("%s: duplicate instance id %q", name, inst.ID))
			}
			seen[inst.ID] = true
			if inst.Credential == "" {
				problems = append(problems, fmt.Sprintf("%s instance %q has no credential reference", name, inst.ID))
			}
		}
	}
	seenBindings := map[string]bool{}
	instanceIDs := map[string]string{}
	owner := map[string]string{}
	for name, m := range map[string]Module{
		"magento": c.Modules.Magento, "cloudflare": c.Modules.Cloudflare, "ga4": c.Modules.GA4,
	} {
		for _, inst := range m.Instances {
			if inst.ID == "" {
				continue
			}
			// Instance ids are global: audit rows, jobs, and bindings refer to
			// them without a module qualifier, so a duplicate would be ambiguous.
			if previous, exists := owner[inst.ID]; exists && previous != name {
				problems = append(problems, fmt.Sprintf("instance id %q is used by both %s and %s", inst.ID, previous, name))
			}
			owner[inst.ID] = name
			instanceIDs[inst.ID] = inst.ID
		}
	}
	for i, b := range c.Bindings {
		if b.ID == "" {
			problems = append(problems, fmt.Sprintf("bindings[%d].id is required", i))
			continue
		}
		if seenBindings[b.ID] {
			problems = append(problems, fmt.Sprintf("duplicate binding id %q", b.ID))
		}
		seenBindings[b.ID] = true
		for field, id := range map[string]string{"magento": b.Magento, "cloudflare": b.Cloudflare, "ga4": b.GA4} {
			if id == "" {
				problems = append(problems, fmt.Sprintf("binding %q: %s instance is required", b.ID, field))
				continue
			}
			if _, ok := instanceIDs[id]; !ok {
				problems = append(problems, fmt.Sprintf("binding %q references unknown instance %q", b.ID, id))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}
