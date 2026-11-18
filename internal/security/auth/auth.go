// SPDX-License-Identifier: Apache-2.0

// Package auth defines the authenticated caller identity and the HTTP
// middleware that establishes it.
//
// Identity is the only authorization input: a verifier produces it, the
// middleware stores it on the request context, and the policy engine consumes
// it. Nothing downstream re-parses the raw token, and the token is never logged.
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

// Stable rejection reasons. They are safe to expose and to aggregate.
const (
	reasonMissingToken           = "missing_token"
	reasonMalformedAuthorization = "malformed_authorization"
	reasonDuplicateAuthorization = "duplicate_authorization"
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
	// Roles are the verified role names granted to the caller.
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

// Verifier turns a raw bearer token into an identity. It returns an
// *errs.Error with code CodeUnauthenticated when the token is not acceptable.
type Verifier interface {
	Verify(ctx context.Context, rawToken string) (*Identity, error)
}

// Middleware extracts a single Bearer token from the Authorization header,
// verifies it, and stores the identity on the request context. A missing,
// malformed, or unverifiable token produces 401 with
// {"error":"unauthenticated","reason":"<stable_reason>"}. The token itself is
// never logged or echoed.
func Middleware(v Verifier, logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, reason := bearerToken(r)
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
