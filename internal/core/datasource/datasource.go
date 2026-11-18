// SPDX-License-Identifier: Apache-2.0

// Package datasource defines the contract every integration module implements.
package datasource

import "context"

// Capability is a declared, versioned unit of functionality. The registry
// registers a tool only when at least one enabled instance supports it.
type Capability struct {
	ID          string
	Version     string
	ReadOnly    bool
	Description string
}

// DataSource is intentionally small. Modules must not be forced to implement
// methods that do not apply to them.
type DataSource interface {
	Name() string
	HealthCheck(ctx context.Context, instanceID string) error
	Capabilities(ctx context.Context, instanceID string) ([]Capability, error)
}
