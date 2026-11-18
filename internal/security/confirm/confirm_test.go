// SPDX-License-Identifier: Apache-2.0

package confirm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(NewMemoryStore(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestCanonicalHashIgnoresKeyOrderAndConfirmationID(t *testing.T) {
	a, err := CanonicalHash(json.RawMessage(`{"b":2,"a":1,"confirmation_id":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := CanonicalHash(json.RawMessage(`{ "a" : 1, "b" : 2 }`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("hash depends on key order or confirmation_id: %s != %s", a, b)
	}
	c, err := CanonicalHash(json.RawMessage(`{"a":1,"b":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if a == c {
		t.Fatal("hash does not bind argument values")
	}
}

func TestCanonicalHashRejectsInvalidJSON(t *testing.T) {
	if _, err := CanonicalHash(json.RawMessage(`{`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestFullLifecycle(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	args := json.RawMessage(`{"instance":"store-us","sku":"A-1"}`)
	req, err := svc.Require(ctx, "magento.product.update", "store-us", "write", "Update product A-1", "agent-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if req.State != StatePending {
		t.Fatalf("state = %s", req.State)
	}
	pending, err := svc.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if err := svc.Approve(ctx, req.ID, "agent-1"); err == nil {
		t.Fatal("self-approval must be rejected")
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", args); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", args); errs.CodeOf(err) != errs.CodeConfirmationUsed {
		t.Fatalf("replay = %v, want confirmation_used", err)
	}
}

func TestConsumeRequiresApproval(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	args := json.RawMessage(`{"a":1}`)
	req, err := svc.Require(ctx, "tool.x", "", "write", "", "agent-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", args); errs.CodeOf(err) != errs.CodeConfirmationRequired {
		t.Fatalf("err = %v, want confirmation_required", err)
	}
}

func TestConsumeRejectsChangedArguments(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	req, err := svc.Require(ctx, "tool.x", "", "write", "", "agent-1", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", json.RawMessage(`{"a":2}`)); errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("err = %v, want confirmation_param_mismatch", err)
	}
}

func TestConsumeRejectsWrongRequester(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	args := json.RawMessage(`{"a":1}`)
	req, err := svc.Require(ctx, "tool.x", "", "write", "", "agent-1", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-2", args); errs.CodeOf(err) != errs.CodePermissionDenied {
		t.Fatalf("err = %v, want permission_denied", err)
	}
}

func TestExpiry(t *testing.T) {
	svc := newTestService(t)
	svc.now = func() time.Time { return time.Now().UTC().Add(-2 * time.Minute) }
	ctx := context.Background()
	args := json.RawMessage(`{"a":1}`)
	req, err := svc.Require(ctx, "tool.x", "", "write", "", "agent-1", args)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().UTC() }
	if err := svc.Approve(ctx, req.ID, "human-1"); errs.CodeOf(err) != errs.CodeConfirmationExpired {
		t.Fatalf("err = %v, want confirmation_expired", err)
	}
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(nil, time.Minute); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewService(NewMemoryStore(), MaxTTL+time.Second); err == nil {
		t.Fatal("excessive ttl accepted")
	}
}

func TestRequestIDIsUUIDv4Shape(t *testing.T) {
	svc := newTestService(t)
	req, err := svc.Require(context.Background(), "tool.x", "", "write", "", "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(req.ID, "-")
	if len(parts) != 5 {
		t.Fatalf("id is not UUID-shaped: %s", req.ID)
	}
}
