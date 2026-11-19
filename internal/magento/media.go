// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

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

const (
	maxMediaBytes      = 5 << 20 // decoded payload ceiling
	maxMediaLabelBytes = 255
	maxMediaPosition   = 1000
)

var (
	mediaFilenameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	mediaTypes      = map[string]bool{"image": true, "small_image": true, "thumbnail": true, "swatch_image": true}
	mediaMIMETypes  = map[string]bool{
		"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true,
	}
)

// MediaUploadInput is one media upload request.
type MediaUploadInput struct {
	Label      string
	Position   int
	Types      []string
	Filename   string
	MIMEType   string
	DataBase64 string
}

// UploadMedia adds an image to a product gallery with
// POST /products/{sku}/media. The payload is bounded and the MIME type and
// file name are validated before any bytes are sent.
func (s *Service) UploadMedia(ctx context.Context, sku string, in MediaUploadInput) (*MediaEntry, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	content, err := validateMediaUpload(in)
	if err != nil {
		return nil, err
	}
	entry := mediaUploadEntry{
		MediaType: "image",
		Label:     in.Label,
		Position:  in.Position,
		Types:     in.Types,
	}
	entry.Content.Base64 = in.DataBase64
	entry.Content.Type = in.MIMEType
	entry.Content.Name = in.Filename
	body := mediaUploadBody{Entry: entry}
	raw, err := s.client.do(ctx, http.MethodPost, "/products/"+url.PathEscape(sku)+"/media", nil, body)
	if err != nil {
		return nil, err
	}
	var created MediaEntry
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &created); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
		}
	}
	_ = content
	return &created, nil
}

type mediaUploadBody struct {
	Entry mediaUploadEntry `json:"entry"`
}

type mediaUploadEntry struct {
	MediaType string   `json:"media_type"`
	Label     string   `json:"label,omitempty"`
	Position  int      `json:"position,omitempty"`
	Disabled  bool     `json:"disabled"`
	Types     []string `json:"types,omitempty"`
	Content   struct {
		Base64 string `json:"base64_encoded_data"`
		Type   string `json:"type"`
		Name   string `json:"name"`
	} `json:"content"`
}

// validateMediaUpload checks the upload before any bytes leave the gateway.
func validateMediaUpload(in MediaUploadInput) ([]byte, error) {
	if !mediaMIMETypes[in.MIMEType] {
		return nil, invalidf("mime_type must be one of image/jpeg, image/png, image/webp, image/gif")
	}
	if !mediaFilenameRE.MatchString(in.Filename) {
		return nil, invalidf("filename must match %s", mediaFilenameRE.String())
	}
	if len(in.Label) > maxMediaLabelBytes || strings.ContainsFunc(in.Label, unicode.IsControl) {
		return nil, invalidf("label must be at most %d bytes and contain no control characters", maxMediaLabelBytes)
	}
	if in.Position < 0 || in.Position > maxMediaPosition {
		return nil, invalidf("position must be between 0 and %d", maxMediaPosition)
	}
	seen := make(map[string]bool, len(in.Types))
	for _, mediaType := range in.Types {
		if !mediaTypes[mediaType] {
			return nil, invalidf("types must be a subset of image, small_image, thumbnail, swatch_image")
		}
		if seen[mediaType] {
			return nil, invalidf("media types must not repeat")
		}
		seen[mediaType] = true
	}
	if in.DataBase64 == "" {
		return nil, invalidf("data_base64 is required")
	}
	// Bound before decoding so an oversized body is rejected without allocating.
	if base64.StdEncoding.DecodedLen(len(in.DataBase64)) > maxMediaBytes {
		return nil, invalidf("media payload must be at most %d bytes", maxMediaBytes)
	}
	content, err := base64.StdEncoding.DecodeString(in.DataBase64)
	if err != nil {
		return nil, invalidf("data_base64 is not valid base64")
	}
	if len(content) == 0 {
		return nil, invalidf("media payload must not be empty")
	}
	return content, nil
}

// DeleteMedia removes one media entry from a product gallery with
// DELETE /products/{sku}/media/{entryId}.
func (s *Service) DeleteMedia(ctx context.Context, sku string, entryID int) error {
	if s == nil || s.client == nil {
		return errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return err
	}
	if entryID <= 0 {
		return invalidf("media_id must be a positive integer")
	}
	path := "/products/" + url.PathEscape(sku) + "/media/" + strconv.Itoa(entryID)
	_, err := s.client.do(ctx, http.MethodDelete, path, nil, nil)
	return err
}

// MediaUpdateInput is a partial media-entry update; nil fields are omitted.
type MediaUpdateInput struct {
	Label    *string
	Position *int
	Types    []string
	Disabled *bool
}

// UpdateMedia changes one gallery entry with PUT /products/{sku}/media/{id}.
func (s *Service) UpdateMedia(ctx context.Context, sku string, entryID int, in MediaUpdateInput) (*MediaEntry, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if entryID <= 0 {
		return nil, invalidf("media_id must be a positive integer")
	}
	if in.Label == nil && in.Position == nil && in.Types == nil && in.Disabled == nil {
		return nil, invalidf("at least one field must be provided to update")
	}
	if in.Label != nil {
		if len(*in.Label) > maxMediaLabelBytes || strings.ContainsFunc(*in.Label, unicode.IsControl) {
			return nil, invalidf("label must be at most %d bytes without control characters", maxMediaLabelBytes)
		}
	}
	if in.Position != nil && (*in.Position < 0 || *in.Position > maxMediaPosition) {
		return nil, invalidf("position must be between 0 and %d", maxMediaPosition)
	}
	if in.Types != nil {
		seen := make(map[string]bool, len(in.Types))
		for _, mediaType := range in.Types {
			if !mediaTypes[mediaType] {
				return nil, invalidf("types must be a subset of image, small_image, thumbnail, swatch_image")
			}
			if seen[mediaType] {
				return nil, invalidf("media types must not repeat")
			}
			seen[mediaType] = true
		}
	}
	entry := mediaUpdateEntry{ID: entryID, Label: in.Label, Position: in.Position, Types: in.Types, Disabled: in.Disabled}
	body := mediaUpdateBody{Entry: entry}
	raw, err := s.client.do(ctx, http.MethodPut, "/products/"+url.PathEscape(sku)+"/media/"+strconv.Itoa(entryID), nil, body)
	if err != nil {
		return nil, err
	}
	var updated MediaEntry
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &updated); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
		}
	}
	return &updated, nil
}

type mediaUpdateBody struct {
	Entry mediaUpdateEntry `json:"entry"`
}

type mediaUpdateEntry struct {
	ID       int      `json:"id"`
	Label    *string  `json:"label,omitempty"`
	Position *int     `json:"position,omitempty"`
	Types    []string `json:"types,omitempty"`
	Disabled *bool    `json:"disabled,omitempty"`
}
