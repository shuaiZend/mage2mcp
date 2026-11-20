// SPDX-License-Identifier: Apache-2.0

package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
	"github.com/shuaiZend/mage2mcp/internal/security/confirm"
)

// TestConfirmStoreRejectsDifferentToolAndInstance is the SEC-03 regression test
// against the real store: a confirmation approved for one tool on one instance
// is not consumable by another tool or another instance, the rejected attempts
// do not change the row, and the matching call still wins exactly once.
func TestConfirmStoreRejectsDifferentToolAndInstance(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	svc, err := confirm.NewService(NewConfirmStore(store), confirm.DefaultTTL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	args := []byte(`{"instance":"store-us","order_id":42}`)
	req, err := svc.Require(ctx, "magento.order.hold", "store-us", "write", confirm.Summarize(args), "agent-1", args)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	hash, err := confirm.CanonicalHash(args)
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	cs := NewConfirmStore(store)
	now := time.Now().UTC()
	state := func() string {
		got, err := cs.Get(ctx, req.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		return string(got.State)
	}

	// Same arguments, different tool: the approval must not be usable.
	if err := cs.Consume(ctx, req.ID, "agent-1", "magento.order.unhold", "store-us", hash, now); errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("cross-tool consume err = %v, want confirmation_param_mismatch", err)
	}
	if got := state(); got != "approved" {
		t.Fatalf("state after cross-tool attempt = %q, want approved", got)
	}

	// Same tool and arguments, different instance.
	if err := cs.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-eu", hash, now); errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("cross-instance consume err = %v, want confirmation_param_mismatch", err)
	}
	if got := state(); got != "approved" {
		t.Fatalf("state after cross-instance attempt = %q, want approved", got)
	}

	// The matching tool and instance consume the approval.
	if err := cs.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-us", hash, now); err != nil {
		t.Fatalf("matching consume: %v", err)
	}
	if got := state(); got != "consumed" {
		t.Fatalf("state after matching consume = %q, want consumed", got)
	}

	// A replay is rejected.
	if err := cs.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-us", hash, now); errs.CodeOf(err) != errs.CodeConfirmationUsed {
		t.Fatalf("replay err = %v, want confirmation_used", err)
	}
}

// TestConfirmStoreSingleUseUnderConcurrency re-establishes the atomicity that
// the binding change must not weaken: one approved row, many concurrent
// consumers, exactly one winner.
func TestConfirmStoreSingleUseUnderConcurrency(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	svc, err := confirm.NewService(NewConfirmStore(store), confirm.DefaultTTL)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	args := []byte(`{"instance":"store-us","order_id":43}`)
	req, err := svc.Require(ctx, "magento.order.hold", "store-us", "write", confirm.Summarize(args), "agent-1", args)
	if err != nil {
		t.Fatalf("Require: %v", err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	hash, err := confirm.CanonicalHash(args)
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	cs := NewConfirmStore(store)
	const consumers = 16
	var ok, refused atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := cs.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-us", hash, time.Now().UTC()); err == nil {
				ok.Add(1)
			} else {
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != consumers-1 {
		t.Fatalf("concurrent consume: %d successes, %d refusals; want 1 and %d", ok.Load(), refused.Load(), consumers-1)
	}
}
