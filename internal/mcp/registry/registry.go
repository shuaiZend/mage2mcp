// SPDX-License-Identifier: Apache-2.0

// Package registry holds the tool catalog and enforces the tool contract:
// naming, required metadata, scope/annotation consistency, and bounded timeouts.
package registry

import (
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

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
}

// Permissions are the only valid scopes.
const (
	PermissionRead        = "read"
	PermissionWrite       = "write"
	PermissionDestructive = "destructive"
	PermissionAdmin       = "admin"
)

var (
	nameRE = regexp.MustCompile(`^(magento|cloudflare|ga4|unified|platform)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
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
	mu    sync.RWMutex
	tools map[string]Tool
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register validates and adds a tool. It returns an error rather than panicking
// so startup can report every invalid tool at once.
func (r *Registry) Register(t Tool) error {
	if err := Validate(t); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("registry: duplicate tool %q", t.Name)
	}
	r.tools[t.Name] = t
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
