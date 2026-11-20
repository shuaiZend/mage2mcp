// SPDX-License-Identifier: Apache-2.0

// Package observability provides the gateway's structured logging and
// Prometheus metrics.
//
// Logs are emitted through log/slog and pass through a redaction filter that
// removes secrets before anything is written. Metrics live on a private
// registry, so the exposed endpoint contains only gateway instruments and the
// standard Go and process collectors.
package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// redacted replaces any attribute value that may contain a secret.
const redacted = "[REDACTED]"

// secretKeyFragments are matched, case-insensitively, as substrings of an
// attribute key. A key containing any fragment is redacted. This list must stay
// identical to internal/security/confirm's copy: an approval summary and a log
// line must agree on what counts as a credential-bearing key.
var secretKeyFragments = []string{
	"token",
	"secret",
	"password",
	"authorization",
	"cookie",
	"api_key",
	"apikey",
	"credential",
	"private",
	"pwd",
	"session",
	"jwt",
	"bearer",
}

// NewLogger returns a slog.Logger writing JSON (format "json") or text
// (format "text") at the given level ("debug", "info", "warn", or "error").
//
// An unknown level falls back to info, an unknown format falls back to JSON,
// and a nil writer defaults to os.Stderr.
//
// Every attribute whose key matches a secret pattern is replaced with
// "[REDACTED]" before it is written, at any depth of slog.Group. A string
// value that looks like a bearer token ("Bearer ...") is redacted as well.
func NewLogger(level, format string, w io.Writer) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}
	opts := &slog.HandlerOptions{
		Level:       parseLevel(level),
		ReplaceAttr: redactAttr,
	}
	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(w, opts)
	} else {
		handler = slog.NewJSONHandler(w, opts)
	}
	return slog.New(handler)
}

// parseLevel maps the configured level name to a slog level. Unknown names
// fall back to info so a bad configuration cannot silence logging.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// redactAttr implements slog.HandlerOptions.ReplaceAttr.
//
// ReplaceAttr is called for every non-group attribute, including attributes
// nested inside slog.Group values, so checking the key alone covers nested
// secrets. The groups argument is unused.
func redactAttr(_ []string, a slog.Attr) slog.Attr {
	// A boolean can never be a credential, and redacting one hides operational
	// state an operator needs: `authorization_enabled=false` used to be printed
	// as "[REDACTED]" because its key contains "authorization".
	if a.Value.Kind() == slog.KindBool {
		return a
	}
	if isSecretKey(a.Key) {
		return slog.String(a.Key, redacted)
	}
	if a.Value.Kind() == slog.KindString && looksLikeBearer(a.Value.String()) {
		return slog.String(a.Key, redacted)
	}
	return a
}

// isSecretKey reports whether key contains any known secret fragment.
// Hyphens are normalized to underscores so header-style keys such as
// "X-Api-Key" match the "api_key" fragment.
func isSecretKey(key string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, fragment := range secretKeyFragments {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}

// looksLikeBearer reports whether value begins with the "Bearer" scheme and
// carries a non-empty token.
func looksLikeBearer(value string) bool {
	const scheme = "bearer "
	trimmed := strings.TrimSpace(value)
	if len(trimmed) <= len(scheme) {
		return false
	}
	return strings.EqualFold(trimmed[:len(scheme)], scheme)
}
