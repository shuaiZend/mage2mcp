// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// MediaEntry is one product media entry (image, video, or other file).
type MediaEntry struct {
	ID        int      `json:"id"`
	MediaType string   `json:"media_type"`
	Label     string   `json:"label"`
	Position  int      `json:"position"`
	Disabled  bool     `json:"disabled"`
	Types     []string `json:"types"`
	File      string   `json:"file"`
}

// ProductMedia lists the media gallery of one product.
func (s *Service) ProductMedia(ctx context.Context, sku string) ([]MediaEntry, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku)+"/media", nil, nil)
	if err != nil {
		return nil, err
	}
	var entries []MediaEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
	}
	if entries == nil {
		entries = []MediaEntry{}
	}
	return entries, nil
}
