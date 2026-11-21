// SPDX-License-Identifier: Apache-2.0

package magento

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/storage"
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
	Status             string `json:"status,omitempty" jsonschema:"Current status."`
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

// CancelOrderInput is one order cancellation request.
type CancelOrderInput struct {
	OrderID int `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	// IdempotencyKey makes the call replayable: repeating it with the same
	// arguments returns the recorded cancellation.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Instance is the configured instance id recorded in the operation ledger.
	Instance string `json:"instance,omitempty" jsonschema:"Configured instance id the result came from."`
}

// CancelOrderResult is the recorded outcome of one cancellation.
type CancelOrderResult struct {
	OrderID     int    `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
	Canceled    bool   `json:"canceled" jsonschema:"True when the order is now canceled."`
	OperationID string `json:"operation_id,omitempty" jsonschema:"Ledger operation id, for polling an unknown outcome."`
	Replayed    bool   `json:"replayed,omitempty" jsonschema:"True when a previous result was replayed for the same idempotency_key, so nothing new happened."`
	Reconciled  bool   `json:"reconciled,omitempty" jsonschema:"True when the result was confirmed by reading the store back rather than by its response."`
}

// CancelOrder cancels an order with POST /orders/{id}/cancel.
//
// Cancellation is irreversible, so the order is read first: an order that is
// already canceled, complete or closed is a typed conflict and no request is
// sent. The operation is recorded in the ledger before the POST. An ambiguous
// outcome (timeout, transport failure, 5xx) is resolved by reading the order
// back: a canceled order is a success, an order still open is a definite
// failure, and an unreadable order stays unknown for a human.
func (s *Service) CancelOrder(ctx context.Context, in CancelOrderInput) (*CancelOrderResult, error) {
	if err := s.requireOrderService(); err != nil {
		return nil, err
	}
	if in.OrderID <= 0 {
		return nil, invalidf("order_id must be a positive integer")
	}
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) > 256 {
		return nil, invalidf("idempotency_key must be at most 256 characters")
	}
	order, err := s.GetOrder(ctx, in.OrderID)
	if err != nil {
		return nil, err
	}
	amount, amountErr := moneyFromAmount(order.GrandTotal, order.Currency)
	ops, err := configuredOperations()
	if err != nil {
		return nil, err
	}
	request := OperationRequest{
		Tool: "magento.order.cancel", Instance: s.operationInstanceID(in.Instance),
		Subject: callerSubject(ctx), IdempotencyKey: in.IdempotencyKey,
		EntityType: storage.EntityOrder, EntityID: strconv.Itoa(in.OrderID),
		Args: cancelArgs{OrderID: in.OrderID},
	}
	if amountErr == nil {
		request.Amount = &amount
	}
	run, begun, err := ops.Begin(ctx, request)
	if err != nil {
		return nil, err
	}
	if begun != nil && begun.Decision == storage.DecisionReplay {
		var recorded CancelOrderResult
		if err := Replay(begun, &recorded); err != nil {
			return nil, err
		}
		recorded.Replayed = true
		return &recorded, nil
	}
	// Pre-flight on a fresh attempt: a repeat of a successful cancellation replays
	// above instead of being rejected here as a conflict.
	switch order.State {
	case "canceled":
		_ = run.Fail(ctx, string(errs.CodeConflict))
		return nil, errs.New(errs.CodeConflict,
			"the order is already canceled; no cancel request was sent").
			WithDetail("order_id", strconv.Itoa(in.OrderID)).
			WithDetail("order_state", order.State).
			WithDetail("operation_id", run.ID())
	case "complete", "closed":
		_ = run.Fail(ctx, string(errs.CodeConflict))
		return nil, errs.New(errs.CodeConflict,
			"the order can no longer be canceled in its current state").
			WithDetail("order_id", strconv.Itoa(in.OrderID)).
			WithDetail("order_state", order.State).
			WithDetail("operation_id", run.ID())
	}
	raw, postErr := s.client.do(ctx, http.MethodPost, "/orders/"+strconv.Itoa(in.OrderID)+"/cancel", nil, nil)
	switch classifyOperationError(postErr) {
	case operationNotApplied:
		_ = run.Fail(ctx, string(errs.CodeOf(postErr)))
		return nil, postErr
	case operationAmbiguous:
		upstreamCode := string(errs.CodeOf(postErr))
		// A single read is not evidence: the cancellation may still be in flight.
		// Require two agreeing reads before calling it a definite failure.
		notCanceled, lastState := 0, ""
		for attempt := 1; attempt <= reconcileReads; attempt++ {
			if attempt > 1 {
				if err := sleepContext(ctx, reconcileBackoff); err != nil {
					break
				}
			}
			after, readErr := s.GetOrder(ctx, in.OrderID)
			if readErr != nil {
				continue
			}
			if after.State == "canceled" {
				result := &CancelOrderResult{OrderID: in.OrderID, Canceled: true, OperationID: run.ID(), Reconciled: true}
				if err := run.Succeed(ctx, orderRef(*after), result); err != nil {
					return nil, err
				}
				return result, nil
			}
			notCanceled++
			lastState = after.State
			if notCanceled >= 2 {
				break
			}
		}
		if notCanceled >= 2 {
			if err := run.Fail(ctx, reconciledNotApplied); err != nil {
				return nil, err
			}
			failure := errs.New(errs.CodeOf(postErr),
				"the order was not canceled: two independent reads show it still open, so it is safe to retry with a new idempotency key").
				WithDetail("operation_id", run.ID()).
				WithDetail("order_state", lastState)
			failure.Retryable = true
			return nil, failure
		}
		_ = run.Unknown(ctx, upstreamCode)
		return nil, operationUnknownError(run.ID(), "order cancellation", upstreamCode)
	}
	// Magento answers 200 with a JSON boolean. `false` means the order was NOT
	// canceled (for example because it is already invoiced), so trusting the
	// status code alone would report a cancellation that never happened.
	var canceled bool
	if err := json.Unmarshal(raw, &canceled); err != nil {
		_ = run.Unknown(ctx, string(errs.CodeUpstreamUnavailable))
		return nil, operationUnknownError(run.ID(), "order cancellation", string(errs.CodeUpstreamUnavailable))
	}
	if !canceled {
		_ = run.Fail(ctx, string(errs.CodeConflict))
		return nil, errs.New(errs.CodeConflict,
			"Magento declined the cancellation and the order state did not change (an invoiced or otherwise non-cancelable order); verify the order before acting").
			WithDetail("order_id", strconv.Itoa(in.OrderID)).
			WithDetail("order_state", order.State).
			WithDetail("operation_id", run.ID())
	}
	result := &CancelOrderResult{OrderID: in.OrderID, Canceled: true, OperationID: run.ID()}
	if err := run.Succeed(ctx, "order:"+strconv.Itoa(in.OrderID), result); err != nil {
		return nil, err
	}
	return result, nil
}

// cancelArgs is the canonical argument set hashed for replay detection.
type cancelArgs struct {
	OrderID int `json:"order_id" jsonschema:"Magento order entity id, not the increment id."`
}
