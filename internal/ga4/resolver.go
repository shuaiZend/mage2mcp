// SPDX-License-Identifier: Apache-2.0

package ga4

import (
	"fmt"
	"sort"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/mcp/registry"
)

// serviceLookup resolves an instance id to its service at call time.
type serviceLookup func(instance string) (*Service, error)

// singleLookup binds a lookup to exactly one service; the instance argument is
// validated by the schema but does not select a different property.
func singleLookup(svc *Service) serviceLookup {
	return func(string) (*Service, error) { return svc, nil }
}

// lookup exposes the resolver as a lookup function.
func (r *Resolver) lookup() serviceLookup {
	return func(instance string) (*Service, error) { return r.Resolve(instance) }
}

// RegisterWithResolver registers all GA4 tools with per-call instance routing.
func RegisterWithResolver(reg *registry.Registry, resolver *Resolver) error {
	if reg == nil || resolver == nil {
		return fmt.Errorf("ga4: RegisterWithResolver needs a registry and resolver")
	}
	lookup := resolver.lookup()
	if err := registerTools(reg, lookup); err != nil {
		return err
	}
	return registerExtraTools(reg, lookup)
}

// Resolver maps an instance id to its service (ADR-0018).
//
// With exactly one configured instance an empty instance resolves to it, which
// keeps single-instance deployments and their tool calls unchanged. With
// several instances the caller must name one; an unknown id is not_found and an
// ambiguous empty id is invalid_argument.
type Resolver struct {
	services map[string]*Service
}

// NewResolver validates and indexes the instance services.
func NewResolver(services map[string]*Service) (*Resolver, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("ga4: resolver needs at least one instance")
	}
	index := make(map[string]*Service, len(services))
	for id, svc := range services {
		if id == "" {
			return nil, fmt.Errorf("ga4: instance id is required")
		}
		if svc == nil {
			return nil, fmt.Errorf("ga4: instance %q has no service", id)
		}
		index[id] = svc
	}
	return &Resolver{services: index}, nil
}

// Resolve returns the service for an instance.
func (r *Resolver) Resolve(instance string) (*Service, error) {
	if r == nil || len(r.services) == 0 {
		return nil, errs.New(errs.CodeInternal, "GA4 resolver is not configured")
	}
	if instance == "" {
		if len(r.services) == 1 {
			for _, svc := range r.services {
				return svc, nil
			}
		}
		return nil, errs.New(errs.CodeInvalidArgument,
			"instance is required when several GA4 properties are configured")
	}
	svc, ok := r.services[instance]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "unknown GA4 instance").WithDetail("instance", instance)
	}
	return svc, nil
}

// Instances returns the configured instance ids, sorted.
func (r *Resolver) Instances() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.services))
	for id := range r.services {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
