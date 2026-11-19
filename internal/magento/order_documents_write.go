// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

const (
	maxDocumentItems    = 100
	maxDocumentComment  = 1024
	maxShipmentTracks   = 20
	maxTrackNumberBytes = 128
	maxTrackTitleBytes  = 255
)

var carrierCodeRE = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// DocumentLine is one order item and the quantity to process.
type DocumentLine struct {
	OrderItemID int     `json:"order_item_id"`
	Qty         float64 `json:"qty"`
}

// ShipmentTrackInput is one tracking number to attach to a shipment.
type ShipmentTrackInput struct {
	TrackNumber string `json:"track_number"`
	Title       string `json:"title"`
	CarrierCode string `json:"carrier_code"`
}

// ShipmentCreateInput is one shipment creation request.
type ShipmentCreateInput struct {
	OrderID int                  `json:"order_id"`
	Items   []DocumentLine       `json:"items"`
	Tracks  []ShipmentTrackInput `json:"tracks,omitempty"`
	Comment string               `json:"comment,omitempty"`
	Notify  bool                 `json:"notify"`
}

// CreditmemoCreateInput is one credit memo creation request.
type CreditmemoCreateInput struct {
	OrderID            int            `json:"order_id"`
	Items              []DocumentLine `json:"items"`
	AdjustmentPositive float64        `json:"adjustment_positive,omitempty"`
	AdjustmentNegative float64        `json:"adjustment_negative,omitempty"`
	Comment            string         `json:"comment,omitempty"`
	Notify             bool           `json:"notify"`
}

// CreateShipment ships order items with POST /shipments.
func (s *Service) CreateShipment(ctx context.Context, in ShipmentCreateInput) (*Shipment, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateShipmentCreate(in); err != nil {
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	tracks := make([]shipmentTrackBody, 0, len(in.Tracks))
	for _, track := range in.Tracks {
		tracks = append(tracks, shipmentTrackBody(track))
	}
	body := shipmentCreateBody{Entity: shipmentEntityBody{
		OrderID: in.OrderID, Items: items, Tracks: tracks,
		Comment: documentComment{Comment: in.Comment, IsVisibleOnFront: false},
		Notify:  in.Notify,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/shipments", nil, body)
	if err != nil {
		return nil, err
	}
	var shipment Shipment
	if err := json.Unmarshal(raw, &shipment); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable shipment payload").WithCause(err)
	}
	return &shipment, nil
}

// CreateCreditmemo refunds order items with POST /creditmemos.
func (s *Service) CreateCreditmemo(ctx context.Context, in CreditmemoCreateInput) (*Creditmemo, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if err := validateCreditmemoCreate(in); err != nil {
		return nil, err
	}
	items := make([]invoiceItemBody, 0, len(in.Items))
	for _, line := range in.Items {
		items = append(items, invoiceItemBody(line))
	}
	body := creditmemoCreateBody{Entity: creditmemoEntityBody{
		OrderID: in.OrderID, Items: items,
		AdjustmentPositive: in.AdjustmentPositive, AdjustmentNegative: in.AdjustmentNegative,
		Comment: documentComment{Comment: in.Comment, IsVisibleOnFront: false},
		Notify:  in.Notify,
	}}
	raw, err := s.client.do(ctx, http.MethodPost, "/creditmemos", nil, body)
	if err != nil {
		return nil, err
	}
	var creditmemo Creditmemo
	if err := json.Unmarshal(raw, &creditmemo); err != nil {
		return nil, errs.New(errs.CodeUpstreamUnavailable, "Magento returned an unreadable creditmemo payload").WithCause(err)
	}
	return &creditmemo, nil
}

type documentComment struct {
	Comment          string `json:"comment,omitempty"`
	IsVisibleOnFront bool   `json:"is_visible_on_front"`
}

type shipmentCreateBody struct {
	Entity shipmentEntityBody `json:"entity"`
}

type shipmentEntityBody struct {
	OrderID int                 `json:"order_id"`
	Items   []invoiceItemBody   `json:"items"`
	Tracks  []shipmentTrackBody `json:"tracks,omitempty"`
	Comment documentComment     `json:"comment"`
	Notify  bool                `json:"notify"`
}

type shipmentTrackBody struct {
	TrackNumber string `json:"track_number"`
	Title       string `json:"title,omitempty"`
	CarrierCode string `json:"carrier_code"`
}

type creditmemoCreateBody struct {
	Entity creditmemoEntityBody `json:"entity"`
}

type creditmemoEntityBody struct {
	OrderID            int               `json:"order_id"`
	Items              []invoiceItemBody `json:"items"`
	AdjustmentPositive float64           `json:"adjustment_positive,omitempty"`
	AdjustmentNegative float64           `json:"adjustment_negative,omitempty"`
	Comment            documentComment   `json:"comment"`
	Notify             bool              `json:"notify"`
}

func validateDocumentLines(orderID int, lines []DocumentLine) error {
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	if len(lines) == 0 {
		return invalidf("at least one order item is required")
	}
	if len(lines) > maxDocumentItems {
		return invalidf("at most %d order items are allowed per call", maxDocumentItems)
	}
	seen := make(map[int]struct{}, len(lines))
	for _, line := range lines {
		if line.OrderItemID <= 0 {
			return invalidf("order_item_id must be a positive integer")
		}
		if line.Qty <= 0 {
			return invalidf("quantity must be greater than zero")
		}
		if _, duplicate := seen[line.OrderItemID]; duplicate {
			return invalidf("order item %d is duplicated", line.OrderItemID)
		}
		seen[line.OrderItemID] = struct{}{}
	}
	return nil
}

func validateDocumentComment(comment string) error {
	if len(comment) > maxDocumentComment || strings.ContainsFunc(comment, unicode.IsControl) {
		return invalidf("comment must be at most %d bytes and contain no control characters", maxDocumentComment)
	}
	return nil
}

func validateShipmentCreate(in ShipmentCreateInput) error {
	if err := validateDocumentLines(in.OrderID, in.Items); err != nil {
		return err
	}
	if err := validateDocumentComment(in.Comment); err != nil {
		return err
	}
	if len(in.Tracks) > maxShipmentTracks {
		return invalidf("at most %d tracking numbers are allowed per shipment", maxShipmentTracks)
	}
	for _, track := range in.Tracks {
		if track.TrackNumber == "" || len(track.TrackNumber) > maxTrackNumberBytes ||
			strings.ContainsFunc(track.TrackNumber, unicode.IsControl) {
			return invalidf("track_number must be 1-%d bytes without control characters", maxTrackNumberBytes)
		}
		if len(track.Title) > maxTrackTitleBytes {
			return invalidf("track title must be at most %d bytes", maxTrackTitleBytes)
		}
		if !carrierCodeRE.MatchString(track.CarrierCode) {
			return invalidf("carrier_code must match %s", carrierCodeRE.String())
		}
	}
	return nil
}

func validateCreditmemoCreate(in CreditmemoCreateInput) error {
	if err := validateDocumentLines(in.OrderID, in.Items); err != nil {
		return err
	}
	if err := validateDocumentComment(in.Comment); err != nil {
		return err
	}
	if in.AdjustmentPositive < 0 || in.AdjustmentNegative < 0 {
		return invalidf("adjustments must not be negative")
	}
	return nil
}
