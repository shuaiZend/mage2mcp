// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"sort"
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
	// Deduplicated reports that this upload matched an existing entry by content
	// hash, so no new entry was created.
	Deduplicated bool `json:"deduplicated,omitempty"`
	// DemotedFrom lists the entries that lost a role to this one. Magento moves
	// `image` silently, so the caller is told which base image was replaced.
	DemotedFrom []int `json:"demoted_from,omitempty"`
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
	// Content-hash dedupe: a repeat of the same bytes with the same roles must not
	// create a second gallery entry. The cache is best-effort, so a hit is confirmed
	// against the live gallery before it is reused.
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	key := mediaKey(sku, hash, in.Types)
	if cached, ok := s.fingerprintFor(key); ok {
		if entry, found := s.galleryEntry(ctx, sku, cached.entryID); found {
			entry.DemotedFrom = s.clearPreviousRoleHolders(ctx, sku, entry.ID, in.Types, s.gallerySnapshot(ctx, sku))
			entry.Deduplicated = true
			return &entry, nil
		}
		s.forgetFingerprint(key)
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
	// The gallery is read at most once and reused: it resolves the created entry
	// after a scalar response and finds the previous holders of a role.
	var gallery []MediaEntry
	var galleryErr error
	if len(raw) > 0 || len(in.Types) > 0 {
		gallery, galleryErr = s.ProductMedia(ctx, sku)
	}
	if len(raw) > 0 {
		id, scalar, err := decodeMediaEntryID(raw)
		if err != nil {
			return nil, err
		}
		if scalar {
			// Resolve the gallery entry so the caller sees the stored file,
			// label, position, and roles rather than an echo of the request.
			created = MediaEntry{ID: id, MediaType: "image", Label: in.Label, Position: in.Position, Types: in.Types}
			for _, entry := range gallery {
				if entry.ID == id {
					created = entry
					break
				}
			}
		} else if err := json.Unmarshal(raw, &created); err != nil {
			return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
		}
	}
	_ = content
	// Assigning `image` to a second entry silently moves the role: Magento does not
	// report that the previous holder was demoted, so the operator loses a base
	// image without being told. Clear the role on the previous holders explicitly
	// and hand the caller their ids.
	if galleryErr == nil {
		created.DemotedFrom = s.clearPreviousRoleHolders(ctx, sku, created.ID, in.Types, gallery)
	}
	if created.ID > 0 {
		s.rememberFingerprint(key, mediaFingerprint{entryID: created.ID, file: created.File})
	}
	return &created, nil
}

// galleryEntry returns one gallery entry by id, reporting whether it still exists.
func (s *Service) galleryEntry(ctx context.Context, sku string, entryID int) (MediaEntry, bool) {
	entries, err := s.ProductMedia(ctx, sku)
	if err != nil {
		return MediaEntry{}, false
	}
	for _, entry := range entries {
		if entry.ID == entryID {
			return entry, true
		}
	}
	return MediaEntry{}, false
}

// gallerySnapshot returns the gallery, or nil when it cannot be read.
func (s *Service) gallerySnapshot(ctx context.Context, sku string) []MediaEntry {
	entries, err := s.ProductMedia(ctx, sku)
	if err != nil {
		return nil
	}
	return entries
}

// clearPreviousRoleHolders removes the roles just granted to entryID from every
// other entry of the product, so the demotion is explicit and reported instead of
// silent. Failures are not fatal: the upload succeeded, and the caller learns that
// the cleanup did not complete from the returned slice being partial.
func (s *Service) clearPreviousRoleHolders(ctx context.Context, sku string, entryID int, roles []string, entries []MediaEntry) []int {
	if len(roles) == 0 || len(entries) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		wanted[role] = struct{}{}
	}
	var demoted []int
	for _, entry := range entries {
		if entry.ID == entryID {
			continue
		}
		kept := make([]string, 0, len(entry.Types))
		var dropped bool
		for _, role := range entry.Types {
			if _, clash := wanted[role]; clash {
				dropped = true
				continue
			}
			kept = append(kept, role)
		}
		if !dropped {
			continue
		}
		if _, err := s.UpdateMedia(ctx, sku, entry.ID, MediaUpdateInput{Types: kept}); err != nil {
			continue
		}
		demoted = append(demoted, entry.ID)
	}
	return demoted
}

// decodeMediaEntryID extracts the new gallery entry id from a
// POST /products/{sku}/media response. Magento returns it as a JSON integer
// (ProductAttributeMediaGalleryManagementInterface::create(): int); some builds
// and proxies return it as a JSON string, so both scalar shapes are accepted.
// scalar is false when the body is not a scalar, in which case the caller
// decodes the full media entry instead.
func decodeMediaEntryID(raw []byte) (id int, scalar bool, err error) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return mediaEntryIDFromText(number.String())
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && strings.TrimSpace(text) != "" {
		return mediaEntryIDFromText(text)
	}
	return 0, false, nil
}

// mediaEntryIDFromText converts a scalar media id to a positive integer.
func mediaEntryIDFromText(text string) (int, bool, error) {
	id, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || id <= 0 {
		e := errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media id")
		if err != nil {
			e = e.WithCause(err)
		}
		return 0, false, e
	}
	return id, true, nil
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
	current, err := s.GetMedia(ctx, sku, entryID)
	if err != nil {
		return nil, err
	}
	entry := mediaUpdateEntry{
		ID: entryID, MediaType: current.MediaType, File: current.File,
		Label: in.Label, Position: in.Position, Types: in.Types, Disabled: in.Disabled,
	}
	body := mediaUpdateBody{Entry: entry}
	raw, err := s.client.do(ctx, http.MethodPut, "/products/"+url.PathEscape(sku)+"/media/"+strconv.Itoa(entryID), nil, body)
	if err != nil {
		return nil, err
	}
	updated := *current
	if in.Label != nil {
		updated.Label = *in.Label
	}
	if in.Position != nil {
		updated.Position = *in.Position
	}
	if in.Types != nil {
		updated.Types = in.Types
	}
	if in.Disabled != nil {
		updated.Disabled = *in.Disabled
	}
	if len(raw) > 0 {
		var acknowledged bool
		if err := json.Unmarshal(raw, &acknowledged); err != nil {
			var entry MediaEntry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
			}
			updated = entry
		}
	}
	return &updated, nil
}

type mediaUpdateBody struct {
	Entry mediaUpdateEntry `json:"entry"`
}

type mediaUpdateEntry struct {
	ID        int      `json:"id"`
	MediaType string   `json:"media_type"`
	File      string   `json:"file"`
	Label     *string  `json:"label,omitempty"`
	Position  *int     `json:"position,omitempty"`
	Types     []string `json:"types,omitempty"`
	Disabled  *bool    `json:"disabled,omitempty"`
}

// GetMedia returns one gallery entry with GET /products/{sku}/media/{id}.
func (s *Service) GetMedia(ctx context.Context, sku string, entryID int) (*MediaEntry, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if err := validateSKU(sku); err != nil {
		return nil, err
	}
	if entryID <= 0 {
		return nil, invalidf("media_id must be a positive integer")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/"+url.PathEscape(sku)+"/media/"+strconv.Itoa(entryID), nil, nil)
	if err != nil {
		return nil, err
	}
	var entry MediaEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media payload").WithCause(err)
	}
	return &entry, nil
}

// MediaType is one gallery role available for an attribute set.
type MediaType struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// MediaTypes lists the media roles for an attribute set with
// GET /products/media/types/{attributeSetName}.
func (s *Service) MediaTypes(ctx context.Context, attributeSetName string) ([]MediaType, error) {
	if s == nil || s.client == nil {
		return nil, errs.New(errs.CodeInternal, "Magento media service is not configured")
	}
	if trimmed := strings.TrimSpace(attributeSetName); trimmed == "" || len(attributeSetName) > 255 {
		return nil, invalidf("attribute_set_name is required and must be at most 255 bytes")
	}
	raw, err := s.client.do(ctx, http.MethodGet, "/products/media/types/"+url.PathEscape(attributeSetName), nil, nil)
	if err != nil {
		return nil, err
	}
	var types []MediaType
	if err := json.Unmarshal(raw, &types); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable media-type payload").WithCause(err)
	}
	if types == nil {
		types = []MediaType{}
	}
	return types, nil
}

// mediaFingerprint is the cached identity of an uploaded gallery entry.
type mediaFingerprint struct {
	entryID int
	file    string
}

// maxMediaFingerprints bounds the dedupe cache. It is an optimisation, not a
// record: eviction only costs a redundant upload.
const maxMediaFingerprints = 1024

// mediaKey identifies one upload by content and roles, so a repeat with the same
// bytes and the same roles can be skipped and a repeat with different roles can be
// applied to the existing entry.
func mediaKey(sku, hash string, roles []string) string {
	sorted := append([]string(nil), roles...)
	sort.Strings(sorted)
	return sku + "|" + hash + "|" + strings.Join(sorted, ",")
}

// fingerprintFor returns the cached entry for this upload, if any.
func (s *Service) fingerprintFor(key string) (mediaFingerprint, bool) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	entry, ok := s.mediaByKey[key]
	return entry, ok
}

// rememberFingerprint records a successful upload.
func (s *Service) rememberFingerprint(key string, entry mediaFingerprint) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	if s.mediaByKey == nil {
		s.mediaByKey = make(map[string]mediaFingerprint, 16)
	}
	if _, exists := s.mediaByKey[key]; !exists {
		s.mediaOrder = append(s.mediaOrder, key)
		if len(s.mediaOrder) > maxMediaFingerprints {
			oldest := s.mediaOrder[0]
			s.mediaOrder = s.mediaOrder[1:]
			delete(s.mediaByKey, oldest)
		}
	}
	s.mediaByKey[key] = entry
}

// forgetFingerprint drops a cache entry whose gallery entry is gone.
func (s *Service) forgetFingerprint(key string) {
	s.mediaMu.Lock()
	defer s.mediaMu.Unlock()
	delete(s.mediaByKey, key)
}
