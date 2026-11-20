// SPDX-License-Identifier: Apache-2.0

package confirm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.product.update", "store-us", args); err != nil {
		t.Fatal(err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.product.update", "store-us", args); errs.CodeOf(err) != errs.CodeConfirmationUsed {
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
	if err := svc.Consume(ctx, req.ID, "agent-1", "tool.x", "", args); errs.CodeOf(err) != errs.CodeConfirmationRequired {
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
	if err := svc.Consume(ctx, req.ID, "agent-1", "tool.x", "", json.RawMessage(`{"a":2}`)); errs.CodeOf(err) != errs.CodeConfirmationMismatch {
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
	if err := svc.Consume(ctx, req.ID, "agent-2", "tool.x", "", args); errs.CodeOf(err) != errs.CodePermissionDenied {
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

// holdArgs is the byte-identical argument set shared by the order hold/unhold
// tools, which is the pair that made the missing tool binding exploitable.
var holdArgs = json.RawMessage(`{"instance":"store-us","order_id":42}`)

// TestConsumeRejectsDifferentTool is the SEC-03 regression test: an approval
// issued for magento.order.hold must not be consumable by magento.order.unhold,
// even though the arguments are byte-identical.
func TestConsumeRejectsDifferentTool(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	req, err := svc.Require(ctx, "magento.order.hold", "store-us", "write", "Hold order 42", "agent-1", holdArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	err = svc.Consume(ctx, req.ID, "agent-1", "magento.order.unhold", "store-us", holdArgs)
	if errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("cross-tool consume err = %v, want confirmation_param_mismatch", err)
	}
	// The rejected attempt must not burn the approval: the tool it was issued
	// for can still use it.
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-us", holdArgs); err != nil {
		t.Fatalf("legitimate consume after a rejected cross-tool attempt: %v", err)
	}
}

// TestConsumeRejectsDifferentInstance proves the instance is part of the
// binding, not just metadata.
func TestConsumeRejectsDifferentInstance(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	req, err := svc.Require(ctx, "magento.product.delete", "store-us", "destructive", "Delete A-1", "agent-1", holdArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	err = svc.Consume(ctx, req.ID, "agent-1", "magento.product.delete", "store-eu", holdArgs)
	if errs.CodeOf(err) != errs.CodeConfirmationMismatch {
		t.Fatalf("cross-instance consume err = %v, want confirmation_param_mismatch", err)
	}
	if err := svc.Consume(ctx, req.ID, "agent-1", "magento.product.delete", "store-us", holdArgs); err != nil {
		t.Fatalf("legitimate consume after a rejected cross-instance attempt: %v", err)
	}
}

// TestConsumeSingleUseUnderConcurrency re-establishes the guarantee that made
// the store trustworthy: one approval, one execution.
func TestConsumeSingleUseUnderConcurrency(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	req, err := svc.Require(ctx, "magento.order.hold", "store-us", "write", "Hold order 42", "agent-1", holdArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "human-1"); err != nil {
		t.Fatal(err)
	}
	const n = 20
	var wg sync.WaitGroup
	var ok, refused atomic.Int32
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := svc.Consume(ctx, req.ID, "agent-1", "magento.order.hold", "store-us", holdArgs); err == nil {
				ok.Add(1)
			} else {
				refused.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if ok.Load() != 1 || refused.Load() != n-1 {
		t.Fatalf("concurrent Consume: %d successes, %d refusals; want 1 and %d", ok.Load(), refused.Load(), n-1)
	}
}

// TestApproveRejectsSelfApproval pins the self-approval rule independently of
// the lifecycle test.
func TestApproveRejectsSelfApproval(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	req, err := svc.Require(ctx, "magento.order.hold", "store-us", "write", "Hold order 42", "agent-1", holdArgs)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, req.ID, "agent-1"); errs.CodeOf(err) != errs.CodePermissionDenied {
		t.Fatalf("self-approval err = %v, want permission_denied", err)
	}
}

// TestPendingShowsInstanceAndSummary proves the approval surface carries the
// tenant and a readable argument summary.
func TestPendingShowsInstanceAndSummary(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	if _, err := svc.Require(ctx, "magento.order.hold", "store-us", "write",
		Summarize(holdArgs), "agent-1", holdArgs); err != nil {
		t.Fatal(err)
	}
	pending, err := svc.Pending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, %v", pending, err)
	}
	if pending[0].Instance != "store-us" {
		t.Fatalf("instance = %q, want store-us", pending[0].Instance)
	}
	if !strings.Contains(pending[0].Summary, "order_id=42") {
		t.Fatalf("summary = %q, want it to contain order_id=42", pending[0].Summary)
	}
	var buf bytes.Buffer
	if err := WritePending(&buf, pending); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"INSTANCE", "store-us", "magento.order.hold", "order_id=42"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pending table missing %q:\n%s", want, out)
		}
	}
}

func TestSummarizeBoundsAndRedacts(t *testing.T) {
	args := json.RawMessage(`{
		"z": 1,
		"a": "plain",
		"access_token": "should-not-appear",
		"authorization": "Bearer abcdefghijklmnop",
		"password": "hunter2",
		"confirmation_id": "dropped",
		"payload": "` + strings.Repeat("A", 5000) + `",
		"line": "one\ntwo\tthree"
	}`)
	got := Summarize(args)
	for _, forbidden := range []string{"should-not-appear", "hunter2", "Bearer", "dropped", strings.Repeat("A", 200)} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, got)
		}
	}
	for _, want := range []string{"access_token=[REDACTED]", "authorization=[REDACTED]", "password=[REDACTED]", "<5000 chars>", "line=one two three"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
	if len([]rune(got)) > maxSummaryLen+1 {
		t.Fatalf("summary is not bounded: %d runes", len([]rune(got)))
	}
	// Sorted keys make the summary deterministic.
	if !strings.HasPrefix(got, "a=plain") {
		t.Fatalf("summary is not key-sorted: %s", got)
	}
	// A JWT under an innocent key is still redacted.
	if jwt := Summarize(json.RawMessage(`{"blob":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2lnbmF0dXJlLXNpZ25hdHVyZS1zaWduYXR1cmU"}`)); !strings.Contains(jwt, "[REDACTED]") {
		t.Fatalf("JWT-shaped value not redacted: %s", jwt)
	}
}

func TestSummarizeCapsItemCount(t *testing.T) {
	payload := map[string]any{}
	for i := 0; i < 40; i++ {
		payload[fmt.Sprintf("k%02d", i)] = i
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	got := Summarize(raw)
	if !strings.Contains(got, "more)") {
		t.Fatalf("summary did not abbreviate a large argument set: %s", got)
	}
}

func TestRequireBoundsSummary(t *testing.T) {
	svc := newTestService(t)
	req, err := svc.Require(context.Background(), "tool.x", "", "write",
		strings.Repeat("x", maxSummaryLen*3), "agent-1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(req.Summary)) > maxSummaryLen+1 {
		t.Fatalf("stored summary is not bounded: %d runes", len([]rune(req.Summary)))
	}
}

// TestSecretKeyFragmentsCoverCredentialNames is the table-driven guard for the
// redaction contract shared with internal/observability: every key that names a
// credential must be redacted in an approval summary, and ordinary business
// fields must stay visible.
func TestSecretKeyFragmentsCoverCredentialNames(t *testing.T) {
	secretKeys := []string{
		"api_token", "client_secret", "password", "authorization", "cookie",
		"api_key", "apikey", "credential", "private_key", "pwd", "session_id", "jwt", "bearer",
	}
	for _, key := range secretKeys {
		if !isSecretKey(key) {
			t.Errorf("key %q is not treated as secret-bearing", key)
		}
		raw, err := json.Marshal(map[string]any{key: "canary-value"})
		if err != nil {
			t.Fatal(err)
		}
		got := Summarize(raw)
		if strings.Contains(got, "canary-value") {
			t.Errorf("key %q leaked its value: %s", key, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("key %q was not redacted: %s", key, got)
		}
	}
	for _, key := range []string{"sku", "amount", "currency", "order_id", "entity_id", "instance", "qty"} {
		if isSecretKey(key) {
			t.Errorf("business key %q is wrongly treated as secret-bearing", key)
		}
	}
}

// TestBoundTextEscapesFormatCharacters proves Unicode Cf characters cannot ride
// into an approval summary. They are escaped rather than deleted, because
// deleting a bidi override silently joins the values on either side of it.
func TestBoundTextEscapesFormatCharacters(t *testing.T) {
	format := []rune{
		'\u200B', '\u200C', '\u200D', '\u200E', '\u200F',
		'\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2066', '\u2067', '\u2068', '\u2069',
		'\uFEFF',
	}
	for _, r := range format {
		raw, err := json.Marshal(map[string]any{"sku": "a" + string(r) + "b"})
		if err != nil {
			t.Fatal(err)
		}
		got := Summarize(raw)
		if strings.ContainsRune(got, r) {
			t.Errorf("U+%04X survived into the summary: %q", r, got)
		}
		if want := fmt.Sprintf(`\u%04x`, r); !strings.Contains(got, want) {
			t.Errorf("U+%04X was not escaped as %s: %q", r, want, got)
		}
	}
	// Deletion would turn "a<RLM>b" into "ab"; escaping keeps both values.
	got := Summarize(json.RawMessage(`{"sku":"a\u202Eb"}`))
	if strings.Contains(got, "ab") {
		t.Fatalf("a bidi control joined two values: %q", got)
	}
	if !strings.Contains(got, `a\u202eb`) {
		t.Fatalf("bidi control not escaped in place: %q", got)
	}
}

// TestSummarizeRendersSecurityKeysBeforePadding proves a caller cannot hide the
// fields that matter by adding many alphabetically earlier arguments.
func TestSummarizeRendersSecurityKeysBeforePadding(t *testing.T) {
	args := map[string]any{
		"amount": "10.00", "currency": "USD", "order_id": 42,
		"entity_id": 7, "sku": "A-1", "instance": "store-us",
	}
	for i := 0; i < 40; i++ {
		args[fmt.Sprintf("aaa_pad_%02d", i)] = i
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	got := Summarize(raw)
	for _, key := range prioritySummaryKeys {
		if !strings.Contains(got, key+"=") {
			t.Fatalf("security-relevant key %q missing from the summary: %s", key, got)
		}
	}
	if !strings.Contains(got, "more)") {
		t.Fatalf("padding was not abbreviated: %s", got)
	}
	firstPad := strings.Index(got, "aaa_pad_")
	if firstPad < 0 {
		t.Fatalf("no padding rendered, test cannot prove ordering: %s", got)
	}
	for _, key := range prioritySummaryKeys {
		idx := strings.Index(got, key+"=")
		if idx < 0 || idx > firstPad {
			t.Fatalf("key %q rendered after padding (idx=%d firstPad=%d): %s", key, idx, firstPad, got)
		}
	}
}
