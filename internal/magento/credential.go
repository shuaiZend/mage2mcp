// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"fmt"
	"strings"
)

// ParseCredential builds a Signer from the configured credential form:
// bearer:<token> or oauth1:<key>:<secret>:<token>:<token-secret>.
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
	return nil, fmt.Errorf("unsupported credential form; use bearer:<token> or oauth1:<key>:<secret>:<token>:<token-secret>")
}

// InstanceSettings is the subset of instance configuration the module needs.
type InstanceSettings struct {
	ID         string
	Endpoint   string
	StoreCode  string
	Credential string
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
		client, err := NewClient(ClientConfig{
			BaseURL: inst.Endpoint, StoreCode: inst.StoreCode, Signer: signer,
		})
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
