// SPDX-License-Identifier: Apache-2.0

// Package registry holds the tool catalog and enforces the tool contract:
// naming, required metadata, scope/annotation consistency, and bounded timeouts.
package registry

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterFunc installs the tool on an MCP server. Typed handlers live at the
// call site so the registry never depends on module internals.
type RegisterFunc func(*mcp.Server) error

// Tool is the metadata every tool must declare before it can be registered.
type Tool struct {
	Name                 string
	Title                string
	Description          string
	Capability           string
	Permission           string
	Timeout              time.Duration
	RequiresConfirmation bool
	Annotations          mcp.ToolAnnotations
	Register             RegisterFunc
	// ValidateInput, when set, checks raw arguments against the tool's JSON
	// Schema before a confirmation is issued.
	ValidateInput func(args []byte) error
}

// Permissions are the only valid scopes.
const (
	PermissionRead        = "read"
	PermissionWrite       = "write"
	PermissionDestructive = "destructive"
	PermissionAdmin       = "admin"
)

var (
	// nameRE enforces module.domain.action for the data modules. platform.info
	// is the one two-segment server-owned tool; it is a first-class registry
	// entry (not a side map) and is validated by the same metadata contract.
	nameRE = regexp.MustCompile(`^((magento|cloudflare|ga4|unified)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+|platform\.[a-z][a-z0-9_]*)$`)
	// mcpNameRE is the specification rule: 1-128 characters from [A-Za-z0-9_.-].
	mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

const (
	maxTitle       = 60
	maxDescription = 1024
	maxTimeout     = 120 * time.Second
)

// Registry is safe for concurrent use.
type Registry struct {
	mu            sync.RWMutex
	tools         map[string]Tool
	writesEnabled bool
}

// New returns an empty registry. Writes are enabled by default so module tests
// and development embeddings keep their historical behaviour; the server entry
// point calls SetWritesEnabled(cfg.Tools.Writes) before registering modules.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool), writesEnabled: true}
}

// SetWritesEnabled is the registration-time write gate. It must be called
// before tools are registered; Register rejects every non-read tool while the
// flag is false, so a module that forgets its own check cannot leak a write.
func (r *Registry) SetWritesEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writesEnabled = enabled
}

// WritesEnabled reports the registration gate flag.
func (r *Registry) WritesEnabled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.writesEnabled
}

// HasWrites reports whether any registered tool mutates data. platform.info
// uses this to report writes_enabled truthfully instead of trusting a config
// flag that may not match the registry.
func (r *Registry) HasWrites() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.tools {
		if t.Permission != PermissionRead {
			return true
		}
	}
	return false
}

// Register validates and adds a tool. It returns an error rather than panicking
// so startup can report every invalid tool at once.
func (r *Registry) Register(t Tool) error {
	if err := Validate(t); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.writesEnabled && t.Permission != PermissionRead {
		return fmt.Errorf("registry: %s tool %q is disabled by tools.writes", t.Permission, t.Name)
	}
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("registry: duplicate tool %q", t.Name)
	}
	r.tools[t.Name] = t
	return nil
}

// NewInputValidator compiles a JSON Schema and returns a validator for raw
// arguments. It is used to reject malformed write requests before asking a human
// to approve them.
func NewInputValidator(schemaJSON string) (func(args []byte) error, error) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		return nil, fmt.Errorf("registry: parse input schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("registry: resolve input schema: %w", err)
	}
	return func(args []byte) error {
		var instance any
		if len(args) == 0 {
			instance = map[string]any{}
		} else if err := json.Unmarshal(args, &instance); err != nil {
			return fmt.Errorf("arguments are not a JSON object: %w", err)
		}
		return resolved.Validate(instance)
	}, nil
}

// SetInputValidator compiles schemaJSON and attaches the validator to a
// registered tool.
func (r *Registry) SetInputValidator(name, schemaJSON string) error {
	validator, err := NewInputValidator(schemaJSON)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tool, ok := r.tools[name]
	if !ok {
		return fmt.Errorf("registry: tool %q is not registered", name)
	}
	tool.ValidateInput = validator
	r.tools[name] = tool
	return nil
}

// Validate enforces the tool contract.
func Validate(t Tool) error {
	switch {
	case !nameRE.MatchString(t.Name):
		return fmt.Errorf("registry: tool name %q does not match module.domain.action", t.Name)
	case !mcpNameRE.MatchString(t.Name):
		return fmt.Errorf("registry: tool name %q violates the MCP character rule", t.Name)
	case t.Title == "" || len(t.Title) > maxTitle:
		return fmt.Errorf("registry: tool %q title must be 1-%d characters", t.Name, maxTitle)
	case t.Description == "" || len(t.Description) > maxDescription:
		return fmt.Errorf("registry: tool %q description must be 1-%d characters", t.Name, maxDescription)
	case t.Capability == "":
		return fmt.Errorf("registry: tool %q has no capability", t.Name)
	case t.Timeout <= 0 || t.Timeout > maxTimeout:
		return fmt.Errorf("registry: tool %q timeout must be within (0, %s]", t.Name, maxTimeout)
	case t.Register == nil:
		return fmt.Errorf("registry: tool %q has no register function", t.Name)
	}
	switch t.Permission {
	case PermissionRead:
		if !t.Annotations.ReadOnlyHint {
			return fmt.Errorf("registry: read tool %q must set readOnlyHint", t.Name)
		}
		if t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint {
			return fmt.Errorf("registry: read tool %q must not be destructive", t.Name)
		}
		if t.RequiresConfirmation {
			return fmt.Errorf("registry: read tool %q must not require confirmation", t.Name)
		}
	case PermissionWrite, PermissionDestructive, PermissionAdmin:
		if t.Annotations.ReadOnlyHint {
			return fmt.Errorf("registry: %s tool %q must not set readOnlyHint", t.Permission, t.Name)
		}
		if t.Permission == PermissionDestructive {
			if !t.RequiresConfirmation {
				return fmt.Errorf("registry: destructive tool %q must require confirmation", t.Name)
			}
			if t.Annotations.DestructiveHint == nil || !*t.Annotations.DestructiveHint {
				return fmt.Errorf("registry: destructive tool %q must set destructiveHint", t.Name)
			}
		}
	default:
		return fmt.Errorf("registry: tool %q has unknown permission %q", t.Name, t.Permission)
	}
	return nil
}

// Lookup returns a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns tools sorted by name.
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Len reports the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}
