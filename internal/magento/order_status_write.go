// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"unicode"
)

const maxOrderCommentBytes = 1024

// AddOrderComment appends a status-history entry with
// POST /orders/{id}/comments. A non-empty status also transitions the order.
func (s *Service) AddOrderComment(ctx context.Context, orderID int, comment, status string, notify bool) error {
	if err := s.requireOrderService(); err != nil {
		return err
	}
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	if comment == "" || len(comment) > maxOrderCommentBytes || strings.ContainsFunc(comment, unicode.IsControl) {
		return invalidf("comment must be 1-%d bytes and contain no control characters", maxOrderCommentBytes)
	}
	if status != "" && !orderStatusRE.MatchString(status) {
		return invalidf("status is not a valid Magento order status")
	}
	history := orderStatusHistoryBody{
		Comment:            comment,
		Status:             status,
		IsCustomerNotified: 0,
		IsVisibleOnFront:   0,
		EntityName:         "order",
	}
	if notify {
		history.IsCustomerNotified = 1
	}
	body := orderCommentBody{StatusHistory: history}
	_, err := s.client.do(ctx, http.MethodPost, "/orders/"+strconv.Itoa(orderID)+"/comments", nil, body)
	return err
}

// HoldOrder places an order on hold with POST /orders/{id}/hold.
func (s *Service) HoldOrder(ctx context.Context, orderID int) error {
	if err := s.requireOrderService(); err != nil {
		return err
	}
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodPost, "/orders/"+strconv.Itoa(orderID)+"/hold", nil, nil)
	return err
}

// UnholdOrder releases an order hold with POST /orders/{id}/unhold.
func (s *Service) UnholdOrder(ctx context.Context, orderID int) error {
	if err := s.requireOrderService(); err != nil {
		return err
	}
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodPost, "/orders/"+strconv.Itoa(orderID)+"/unhold", nil, nil)
	return err
}

type orderCommentBody struct {
	StatusHistory orderStatusHistoryBody `json:"statusHistory"`
}

type orderStatusHistoryBody struct {
	Comment            string `json:"comment"`
	Status             string `json:"status,omitempty"`
	IsCustomerNotified int    `json:"is_customer_notified"`
	IsVisibleOnFront   int    `json:"is_visible_on_front"`
	EntityName         string `json:"entity_name"`
}

// SendOrderEmail sends the order confirmation email with
// POST /orders/{id}/emails.
func (s *Service) SendOrderEmail(ctx context.Context, orderID int) error {
	if err := s.requireOrderService(); err != nil {
		return err
	}
	if orderID <= 0 {
		return invalidf("order_id must be a positive integer")
	}
	_, err := s.client.do(ctx, http.MethodPost, "/orders/"+strconv.Itoa(orderID)+"/emails", nil, nil)
	return err
}
