// SPDX-License-Identifier: Apache-2.0

// Package auth defines the authenticated caller identity and the HTTP
// middleware that establishes it.
//
// Identity is the only authorization input: a verifier produces it, the
// middleware stores it on the request context, and the policy engine consumes
// it. Nothing downstream re-parses the raw token, and the token is never logged.
//
// Two credential sources are accepted, both verified by the same Verifier:
// Cloudflare Access forwards a signed JWT in Cf-Access-Jwt-Assertion, and a
// direct OIDC client sends Authorization: Bearer. The gateway verifies the
// assertion's signature and claims; it does NOT enforce that the origin is
// reachable only through Cloudflare. Origin lockdown (a Tunnel, or authenticated
// origin pull) is a deployment control, and without it any client that can
// reach the listener can present its own Authorization header.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	errs "github.com/shuaiZend/mage2mcp/internal/core/errors"
)

// Identity classes. A service identity never inherits a human's roles.
const (
	ClassHuman   = "human"
	ClassService = "service"
)

// AssertionHeader is the header Cloudflare Access uses to forward a signed JWT
// to the origin.
const AssertionHeader = "Cf-Access-Jwt-Assertion"

// Stable rejection reasons. They are safe to expose and to aggregate.
const (
	reasonMissingToken           = "missing_token"
	reasonMalformedAuthorization = "malformed_authorization"
	reasonDuplicateAuthorization = "duplicate_authorization"
	reasonMalformedAssertion     = "malformed_assertion"
	reasonDuplicateAssertion     = "duplicate_assertion"
	reasonInvalidIdentity        = "invalid_identity"
	reasonInvalidToken           = "invalid_token"
	reasonNoVerifier             = "no_verifier"
)

// Identity is a verified caller. It is immutable once placed on a context.
type Identity struct {
	// Subject is the verified `sub` claim; it is never empty.
	Subject string
	// Class is ClassHuman or ClassService.
	Class string
	// Roles are the role names from the verified token's `roles` claim. The IdP
	// is the role authority; the policy decides what each name grants, and a name
	// with no grant grants nothing.
	Roles []string
}

type identityKey struct{}

// WithIdentity returns a context carrying a copy of id. A nil identity is
// ignored and ctx is returned unchanged.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	if ctx == nil || id == nil {
		return ctx
	}
	cp := *id
	cp.Roles = append([]string(nil), id.Roles...)
	return context.WithValue(ctx, identityKey{}, &cp)
}

// IdentityFromContext returns the identity stored by WithIdentity. Callers must
// treat the returned value as read-only.
func IdentityFromContext(ctx context.Context) (*Identity, bool) {
	if ctx == nil {
		return nil, false
	}
	id, ok := ctx.Value(identityKey{}).(*Identity)
	if !ok || id == nil {
		return nil, false
	}
	return id, true
}

// Verifier turns a raw credential (a Bearer token or a Cloudflare Access
// assertion) into an identity. It returns an *errs.Error with code
// CodeUnauthenticated when the credential is not acceptable.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (*Identity, error)
}

// Middleware extracts exactly one credential, verifies it, and stores the
// identity on the request context. A missing, malformed, or unverifiable
// credential produces 401 with
// {"error":"unauthenticated","reason":"<stable_reason>"}. The credential itself
// is never logged or echoed.
//
// Credential precedence. Cf-Access-Jwt-Assertion is authoritative when present:
// its value is verified and the Authorization header is not consulted, so a
// caller cannot downgrade to a bearer token by supplying a bad assertion. When
// the assertion header is absent, Authorization: Bearer is used unchanged. A
// duplicate header of either kind is rejected before any verification, so an
// ambiguous request never reaches the verifier.
//
// Both sources go through the same Verifier and therefore the same algorithm
// allowlist, signature check, issuer, audience, expiry, and role parsing. There
// is no second, weaker verifier for the assertion path.
//
// The middleware does not check that the request came from Cloudflare. That is
// a deployment control (a Tunnel or authenticated origin pull); see the package
// comment.
func Middleware(v Verifier, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, reason := credential(r)
			if reason != "" {
				reject(w, logger, r, reason)
				return
			}
			if v == nil {
				reject(w, logger, r, reasonNoVerifier)
				return
			}
			id, err := v.Verify(r.Context(), raw)
			if err != nil {
				reject(w, logger, r, failureReason(err))
				return
			}
			if id == nil || id.Subject == "" {
				reject(w, logger, r, reasonInvalidIdentity)
				return
			}
			next.ServeHTTP(w, r.WithContext(WithIdentity(r.Context(), id)))
		})
	}
}

// credential selects the one credential this request may authenticate with.
//
// The Access assertion wins when present; the Authorization header is then
// ignored. Both headers are still checked for duplication first, so a request
// that presents two credentials of the same kind is rejected rather than
// silently resolved.
func credential(r *http.Request) (string, string) {
	assertions := r.Header.Values(AssertionHeader)
	authorizations := r.Header.Values("Authorization")
	switch {
	case len(assertions) > 1:
		return "", reasonDuplicateAssertion
	case len(authorizations) > 1:
		return "", reasonDuplicateAuthorization
	}
	if len(assertions) == 1 {
		return assertionToken(assertions[0])
	}
	return bearerToken(r)
}

// assertionToken validates the shape of one Cf-Access-Jwt-Assertion value. It
// deliberately does not verify the JWT: that is the Verifier's job, so the
// assertion and bearer paths cannot drift apart.
func assertionToken(value string) (string, string) {
	token := strings.TrimSpace(value)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", reasonMalformedAssertion
	}
	return token, ""
}

// bearerToken returns the credential and, on failure, the rejection reason.
func bearerToken(r *http.Request) (string, string) {
	headers := r.Header.Values("Authorization")
	switch {
	case len(headers) == 0:
		return "", reasonMissingToken
	case len(headers) > 1:
		return "", reasonDuplicateAuthorization
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") || parts[1] == "" {
		return "", reasonMalformedAuthorization
	}
	return parts[1], ""
}

// failureReason reads the verifier's stable reason, defaulting to a generic one
// so an unexpected error cannot leak detail to the client.
func failureReason(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		if reason := strings.TrimSpace(e.Details["reason"]); reason != "" {
			return reason
		}
	}
	return reasonInvalidToken
}

func reject(w http.ResponseWriter, logger *slog.Logger, r *http.Request, reason string) {
	// Debug, not Warn: a hostile client must not be able to amplify log volume.
	logger.DebugContext(r.Context(), "authentication failed", "reason", reason, "remote_addr", r.RemoteAddr)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthenticated", "reason": reason})
}
