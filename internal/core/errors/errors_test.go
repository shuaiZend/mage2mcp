// SPDX-License-Identifier: Apache-2.0

package errs

import (
	"errors"
	"fmt"
	"testing"
)

func TestNewKeepsKnownCode(t *testing.T) {
	err := New(CodeNotFound, "product not found")
	if err.Code != CodeNotFound || err.Message != "product not found" {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func TestNewCoercesUnknownCode(t *testing.T) {
	err := New(Code("made_up"), "leak")
	if err.Code != CodeInternal || err.Message != "internal error" {
		t.Fatalf("unknown code not coerced: %+v", err)
	}
}

func TestDetailsAreBounded(t *testing.T) {
	err := New(CodeInvalidArgument, "bad")
	for i := 0; i < 50; i++ {
		_ = err.WithDetail(fmt.Sprintf("k%d", i), "v")
	}
	if len(err.Details) > maxDetails {
		t.Fatalf("details = %d, want <= %d", len(err.Details), maxDetails)
	}
}

func TestWithCauseIsUnwrappableButNotSerialized(t *testing.T) {
	cause := errors.New("dial tcp: secret-address")
	err := New(CodeUpstreamUnavailable, "upstream unavailable").WithCause(cause)
	if !errors.Is(err, cause) {
		t.Fatal("cause not unwrappable")
	}
	if err.Message == cause.Error() {
		t.Fatal("cause leaked into the agent-facing message")
	}
}

func TestCodeOfAndRetryable(t *testing.T) {
	base := New(CodeRateLimited, "slow down")
	base.Retryable = true
	wrapped := fmt.Errorf("outer: %w", base)
	if CodeOf(wrapped) != CodeRateLimited || !RetryableError(wrapped) {
		t.Fatal("wrapped error not detected")
	}
	if CodeOf(errors.New("plain")) != CodeInternal {
		t.Fatal("plain error should map to internal")
	}
}

func TestSortedDetailsIsDeterministic(t *testing.T) {
	err := New(CodeValidationFailed, "bad").WithDetail("b", "2").WithDetail("a", "1")
	got := err.SortedDetails()
	if len(got) != 2 || got[0] != "a=1" || got[1] != "b=2" {
		t.Fatalf("unexpected details order: %v", got)
	}
}
