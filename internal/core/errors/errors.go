// SPDX-License-Identifier: Apache-2.0

// Package errs defines the closed error taxonomy shared by every module.
//
// An error crossing a tool boundary is always one of the codes below. Upstream
// payloads, credentials, and stack traces never cross that boundary; they are
// attached as an unexported cause for logs only.
package errs

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Code is a stable, agent-facing failure class.
type Code string

// The closed failure taxonomy. Adding a code requires updating the tool
// catalog and the conformance tests.
const (
	CodeInvalidArgument       Code = "invalid_argument"
	CodeValidationFailed      Code = "validation_failed"
	CodeUnauthenticated       Code = "unauthenticated"
	CodePermissionDenied      Code = "permission_denied"
	CodeNotFound              Code = "not_found"
	CodeConflict              Code = "conflict"
	CodeRateLimited           Code = "rate_limited"
	CodeUpstreamTimeout       Code = "upstream_timeout"
	CodeUpstreamUnavailable   Code = "upstream_unavailable"
	CodeUnsupportedCapability Code = "unsupported_capability"
	CodeConfirmationRequired  Code = "confirmation_required"
	CodeInternal              Code = "internal"

	CodeIllegalTransition           Code = "illegal_transition"
	CodeIdempotencyKeyReuse         Code = "idempotency_key_reuse"
	CodeArtifactExpired             Code = "artifact_expired"
	CodeConfirmationMismatch        Code = "confirmation_param_mismatch"
	CodeConfirmationUsed            Code = "confirmation_used"
	CodeConfirmationExpired         Code = "confirmation_expired"
	CodeEcommerceNotTracked         Code = "ecommerce_not_tracked"
	CodeRequiresLogpush             Code = "requires_logpush"
	CodeInsufficientData            Code = "insufficient_data"
	CodeMappingIncomplete           Code = "mapping_incomplete"
	CodeCurrencyMismatch            Code = "currency_mismatch"
	CodeIncompleteWindow            Code = "incomplete_window"
	CodeUnsupportedFormat           Code = "unsupported_format"
	CodeCredentialScopeExceeded     Code = "credential_scope_exceeded"     //nolint:gosec // G101: error code, not a credential
	CodeCredentialScopeInsufficient Code = "credential_scope_insufficient" //nolint:gosec // G101: error code, not a credential
	CodeProbeSKUConflict            Code = "probe_sku_conflict"
)

// valid is the closed set. Adding a code requires updating the tool catalog and
// the conformance tests.
var valid = map[Code]struct{}{
	CodeInvalidArgument: {}, CodeValidationFailed: {}, CodeUnauthenticated: {},
	CodePermissionDenied: {}, CodeNotFound: {}, CodeConflict: {}, CodeRateLimited: {},
	CodeUpstreamTimeout: {}, CodeUpstreamUnavailable: {}, CodeUnsupportedCapability: {},
	CodeConfirmationRequired: {}, CodeInternal: {}, CodeIllegalTransition: {},
	CodeIdempotencyKeyReuse: {}, CodeArtifactExpired: {}, CodeConfirmationMismatch: {},
	CodeConfirmationUsed: {}, CodeConfirmationExpired: {}, CodeEcommerceNotTracked: {},
	CodeRequiresLogpush: {}, CodeInsufficientData: {}, CodeMappingIncomplete: {},
	CodeCurrencyMismatch: {}, CodeIncompleteWindow: {}, CodeUnsupportedFormat: {},
	CodeCredentialScopeExceeded: {}, CodeCredentialScopeInsufficient: {}, CodeProbeSKUConflict: {},
}

const (
	maxDetails    = 10
	maxDetailSize = 1024
)

// Error is the only error type that crosses the tool boundary.
type Error struct {
	Code      Code
	Message   string
	Retryable bool
	Upstream  string
	Op        string
	Details   map[string]string
	cause     error
}

// New builds an error with a safe message.
func New(code Code, message string) *Error {
	if _, ok := valid[code]; !ok {
		code = CodeInternal
		message = "internal error"
	}
	return &Error{Code: code, Message: message}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Op != "" {
		return fmt.Sprintf("%s: %s: %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is/As for logs and tests. It is never
// serialized to an agent.
func (e *Error) Unwrap() error { return e.cause }

// WithCause attaches an internal cause and returns the error.
func (e *Error) WithCause(err error) *Error {
	e.cause = err
	return e
}

// WithOp sets the operation label and returns the error.
func (e *Error) WithOp(op string) *Error {
	e.Op = op
	return e
}

// WithDetail attaches a bounded, redacted detail and returns the error.
func (e *Error) WithDetail(key, value string) *Error {
	if len(e.Details) >= maxDetails {
		return e
	}
	if len(key)+len(value) > maxDetailSize {
		return e
	}
	if e.Details == nil {
		e.Details = make(map[string]string, 4)
	}
	e.Details[key] = value
	return e
}

// RetryableError reports whether the error is safe to retry. A non-idempotent
// write must never be retried, which is the caller's responsibility to combine
// with the tool annotation.
func RetryableError(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable
	}
	return false
}

// CodeOf returns the taxonomy code, or CodeInternal for an unknown error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// SortedDetails returns the bounded details as sorted "key=value" strings.
func (e *Error) SortedDetails() []string {
	if e == nil || len(e.Details) == 0 {
		return nil
	}
	keys := make([]string, 0, len(e.Details))
	for k := range e.Details {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+strings.TrimSpace(e.Details[k]))
	}
	return out
}
