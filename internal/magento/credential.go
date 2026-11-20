// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"fmt"
	"strings"
)

// ParseCredential builds a Signer from the configured credential form:
// bearer:<token>, oauth1:<key>:<secret>:<token>:<token-secret>, or
// admin:<username>:<password>.
//
// The admin form returns an unbound AdminTokenSigner: NewResolverFromSettings
// binds it to the instance endpoint and store code, then installs the 401-retry
// HTTP client. A password may itself contain colons; the username may not.
func ParseCredential(credential string) (Signer, error) {
	if strings.HasPrefix(credential, "bearer:") {
		return &BearerSigner{Token: strings.TrimPrefix(credential, "bearer:")}, nil
	}
	if strings.HasPrefix(credential, "oauth1:") {
		parts := strings.Split(strings.TrimPrefix(credential, "oauth1:"), ":")
		if len(parts) != 4 {
			return nil, fmt.Errorf("oauth1 credential must be oauth1:<key>:<secret>:<token>:<token-secret>")
		}
		return NewOAuth1Signer(parts[0], parts[1], parts[2], parts[3])
	}
	if strings.HasPrefix(credential, "admin:") {
		parts := strings.SplitN(strings.TrimPrefix(credential, "admin:"), ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("admin credential must be admin:<username>:<password>")
		}
		return NewAdminTokenSigner(parts[0], parts[1])
	}
	return nil, fmt.Errorf("unsupported credential form; use bearer:<token>, oauth1:<key>:<secret>:<token>:<token-secret>, or admin:<username>:<password>")
}

// InstanceSettings is the subset of instance configuration the module needs.
type InstanceSettings struct {
	ID         string
	Endpoint   string
	StoreCode  string
	Credential string
	// Limits bounds this instance's outbound requests. The zero value means
	// DefaultLimits: an unconfigured deployment is still protected.
	Limits Limits
}

// NewResolverFromSettings builds one service per configured store and returns a
// resolver over them. It is shared by the server and the worker.
func NewResolverFromSettings(settings []InstanceSettings) (*Resolver, error) {
	services := make(map[string]*Service, len(settings))
	for _, inst := range settings {
		signer, err := ParseCredential(inst.Credential)
		if err != nil {
			return nil, fmt.Errorf("magento %s: %w", inst.ID, err)
		}
		// The caller decides the budget; a zero Limits means unlimited. The
		// configuration layer supplies DefaultLimits when an instance does not
		// mention limits, so "not configured" and "explicitly unlimited" stay
		// distinguishable here.
		cfg := ClientConfig{BaseURL: inst.Endpoint, StoreCode: inst.StoreCode, Signer: signer, Limits: inst.Limits}
		if admin, ok := signer.(*AdminTokenSigner); ok {
			// The admin form mints its token lazily, so it needs the instance
			// it belongs to and the client that retries once on a 401.
			if err := admin.Bind(inst.Endpoint, inst.StoreCode); err != nil {
				return nil, fmt.Errorf("magento %s: %w", inst.ID, err)
			}
			cfg.HTTPClient = admin.HTTPClient()
		}
		client, err := NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("magento %s: %w", inst.ID, err)
		}
		svc, err := NewService(client)
		if err != nil {
			return nil, err
		}
		services[inst.ID] = svc
	}
	return NewResolver(services)
}
