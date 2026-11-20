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

// Tools controls optional tool surfaces. Write tools are registered only when
// Writes is true; every write still requires two-step human confirmation.
type Tools struct {
	Writes bool `yaml:"writes"`
}

// Database configures the optional PostgreSQL store. An empty URL keeps the
// gateway on in-memory audit and job state (development only).
type Database struct {
	URL      string `yaml:"url"`
	MaxConns int32  `yaml:"max_conns"`
}

// Request-body limits.
//
// DefaultMaxBodyBytes bounds one MCP request body. It is sized for the largest
// supported operation — a maximum-size product import — rather than for the
// median call: an 8 MiB decoded import encodes to 11,184,812 bytes of base64
// (internal/magento.MaxImportBytes), and the JSON envelope adds ~200 bytes, so
// 12 MiB would be the bare minimum. 16 MiB leaves 33% headroom for envelope
// growth and keeps the bound a round power of two.
//
// This is a deliberate trade, not an oversight. The endpoint is authenticated
// (auth.enabled defaults to true, and a non-loopback listener with auth disabled
// is refused), every import write additionally needs a two-step human
// confirmation, and the cap is a hard ceiling: the handler reads at most this
// many bytes and answers 413 "request body exceeds N bytes" beyond it. Peak
// memory for one in-flight maximum import is roughly 16 MiB (body) + 11.2 MiB
// (base64 string) + 8 MiB (decoded) + ~7 MB (parsed rows) ≈ 42 MB; a deployment
// that never imports (tools.writes false, or no import tools) can lower this
// value, and a write-enabled deployment that lowers it below
// MinMaxBodyBytesForImport fails validation at startup instead of failing at
// runtime.
const (
	// DefaultMaxBodyBytes is the shipped server.max_body_bytes default.
	DefaultMaxBodyBytes int64 = 16 << 20
	// MinMaxBodyBytesForImport is the smallest body cap that still admits a
	// maximum-size import (8 MiB decoded -> 11,184,812 bytes base64 plus the
	// envelope). internal/magento's TestImportLimitsAgreeAcrossPackages fails if
	// this stops covering magento.ImportHTTPBodyBytes(magento.MaxImportBytes),
	// so raise both together.
	MinMaxBodyBytesForImport int64 = 12 << 20
)

// Server holds listener settings.
//
// There is deliberately no public_url field: the server never consumed one, and
// a configuration key that cannot change behaviour is worse than no key. Use
// MAGE2MCP_LISTEN and the Cloudflare Tunnel hostname instead.
type Server struct {
	Listen        string        `yaml:"listen"`
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
	Credential Secret `yaml:"credential"`
	// Limits is the outbound request budget for this instance. Nil means the
	// module default; the Magento module applies a generous default so an
	// unconfigured deployment is still protected from a retry storm.
	Limits *InstanceLimits `yaml:"limits"`
}

// InstanceLimits bounds how hard the gateway may drive one upstream instance.
//
// It is per instance because two stores have independent capacity. A zero
// field disables that dimension: no rate limit, no concurrency cap.
type InstanceLimits struct {
	// RequestsPerSecond is the sustained request rate.
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	// Burst is how many requests may be issued back-to-back before the
	// sustained rate applies.
	Burst int `yaml:"burst"`
	// MaxConcurrent bounds in-flight requests for the instance.
	MaxConcurrent int `yaml:"max_concurrent"`
}

// Default returns the safe defaults: loopback listener, auth required, info logs.
func Default() Config {
	return Config{
		Server: Server{
			Listen:        "127.0.0.1:8080",
			ShutdownGrace: 10 * time.Second,
			MaxBodyBytes:  DefaultMaxBodyBytes,
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
		// Only the exact lower-case "true" or "1" enables authentication; any
		// other non-empty value disables it. docs/configuration.md warns that
		// the value is compared literally.
		c.Auth.Enabled = v == "true" || v == "1"
	}
	if v := os.Getenv("MAGE2MCP_LOG_LEVEL"); v != "" {
		c.Log.Level = v
	}
	if v := os.Getenv("MAGE2MCP_TOOLS_WRITES"); v == "true" || v == "1" {
		// Only the exact lower-case "true" or "1" enables writes; the variable
		// can never disable a YAML-enabled write profile.
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
	} else if c.Tools.Writes && c.Server.MaxBodyBytes < MinMaxBodyBytesForImport {
		// A write-enabled deployment ships the import tools; a body cap below the
		// import envelope would reject a documented maximum-size import at the
		// transport with a 413 long after startup. Fail at startup instead.
		problems = append(problems, fmt.Sprintf(
			"server.max_body_bytes is %d but tools.writes is enabled: a maximum-size import "+
				"(8 MiB decoded, 11184812 bytes base64) needs at least %d; raise "+
				"server.max_body_bytes to %d or more, or disable tools.writes",
			c.Server.MaxBodyBytes, MinMaxBodyBytesForImport, DefaultMaxBodyBytes))
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
			if inst.Credential.Empty() {
				problems = append(problems, fmt.Sprintf("%s instance %q has no credential reference", name, inst.ID))
			}
			if l := inst.Limits; l != nil {
				if l.RequestsPerSecond < 0 {
					problems = append(problems, fmt.Sprintf("%s instance %q: limits.requests_per_second must not be negative", name, inst.ID))
				}
				if l.Burst < 0 {
					problems = append(problems, fmt.Sprintf("%s instance %q: limits.burst must not be negative", name, inst.ID))
				}
				if l.MaxConcurrent < 0 {
					problems = append(problems, fmt.Sprintf("%s instance %q: limits.max_concurrent must not be negative", name, inst.ID))
				}
				if l.Burst > 0 && l.RequestsPerSecond <= 0 {
					problems = append(problems, fmt.Sprintf(
						"%s instance %q: limits.burst requires limits.requests_per_second to be greater than zero", name, inst.ID))
				}
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
	// Unified tools currently bind exactly one Cloudflare service and one GA4
	// service (cmd/server builds the adapters from a single service), so the
	// documented multi-instance path cannot start. Reject it here with an
	// actionable message instead of failing later in module registration; see
	// ADR-0018 and docs/configuration.md. Remove this block when the unified
	// adapters become resolver-aware.
	if len(c.Bindings) > 0 {
		if !c.Modules.Magento.Enabled || !c.Modules.Cloudflare.Enabled || !c.Modules.GA4.Enabled {
			problems = append(problems,
				"bindings require datasources.magento, datasources.cloudflare and datasources.ga4 to be enabled")
		} else if len(c.Modules.Cloudflare.Instances) != 1 || len(c.Modules.GA4.Instances) != 1 {
			problems = append(problems,
				"bindings currently require exactly one Cloudflare instance and exactly one GA4 instance (ADR-0018); "+
					"configure one zone and one property, or remove the bindings")
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("config: %s", strings.Join(problems, "; "))
	}
	return nil
}

// Secret is a credential that redacts itself in every formatting path.
//
// Instance.Credential used to be a plain string, so `fmt.Sprintf("%+v", cfg)` — a
// debug print, a panic value, a support bundle — printed every bearer token and
// admin password. The logger's key-based redaction only covers slog attributes, not
// a struct formatted with %+v, so the type has to be the defence.
//
// Call Reveal to obtain the value. There is deliberately no implicit conversion.
type Secret string

// redacted is what every formatting path returns.
const redacted = "[REDACTED]"

// String implements fmt.Stringer.
func (s Secret) String() string { return redacted }

// GoString implements fmt.GoStringer, which %#v uses.
func (s Secret) GoString() string { return redacted }

// MarshalJSON redacts the value so a config dump cannot leak it either.
func (s Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// Reveal returns the credential itself. Call it only where the value is used.
func (s Secret) Reveal() string { return string(s) }

// Empty reports whether the credential is unset.
func (s Secret) Empty() bool { return len(s) == 0 }
