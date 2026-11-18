// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"fmt"
	"sort"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Resolver maps an instance id to its store-bound service (ADR-0018).
//
// One configured store keeps the empty-instance fast path. With several stores
// the caller must name one: an unknown id is not_found and an ambiguous empty
// id is invalid_argument, never a guess.
type Resolver struct {
	services map[string]*Service
}

// NewResolver validates and indexes the instance services.
func NewResolver(services map[string]*Service) (*Resolver, error) {
	if len(services) == 0 {
		return nil, fmt.Errorf("magento: resolver needs at least one instance")
	}
	index := make(map[string]*Service, len(services))
	for id, svc := range services {
		if id == "" {
			return nil, fmt.Errorf("magento: instance id is required")
		}
		if svc == nil {
			return nil, fmt.Errorf("magento: instance %q has no service", id)
		}
		index[id] = svc
	}
	return &Resolver{services: index}, nil
}

// Resolve returns the service for an instance.
func (r *Resolver) Resolve(instance string) (*Service, error) {
	if r == nil || len(r.services) == 0 {
		return nil, errs.New(errs.CodeInternal, "Magento resolver is not configured")
	}
	if instance == "" {
		if len(r.services) == 1 {
			for _, svc := range r.services {
				return svc, nil
			}
		}
		return nil, errs.New(errs.CodeInvalidArgument,
			"instance is required when several Magento stores are configured")
	}
	svc, ok := r.services[instance]
	if !ok {
		return nil, errs.New(errs.CodeNotFound, "unknown Magento instance").WithDetail("instance", instance)
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
