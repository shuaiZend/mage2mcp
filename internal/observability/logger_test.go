// SPDX-License-Identifier: Apache-2.0

package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerJSONRedactsSecrets(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("debug", "json", &buf)

	logger.Info("tool call",
		"token", "tok-123",
		"password", "hunter2",
		"authorization", "Bearer abc.def",
		"X-Api-Key", "header-key-456",
		"tool", "magento.product.get",
	)

	out := buf.String()
	for _, secret := range []string{"tok-123", "hunter2", "abc.def", "header-key-456"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked secret %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, redacted) {
		t.Errorf("log output does not contain %q: %s", redacted, out)
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON log: %v\n%s", err, out)
	}
	for _, key := range []string{"token", "password", "authorization", "X-Api-Key"} {
		if got := record[key]; got != redacted {
			t.Errorf("%s = %v, want %q", key, got, redacted)
		}
	}
	if got := record["tool"]; got != "magento.product.get" {
		t.Errorf("tool = %v, want magento.product.get", got)
	}
	if got := record["msg"]; got != "tool call" {
		t.Errorf("msg = %v, want %q", got, "tool call")
	}
}

func TestNewLoggerRedactsNestedGroups(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)

	logger.Info("upstream",
		slog.Group("request",
			slog.String("access_token", "nested-token"),
			slog.Group("headers",
				slog.String("Cookie", "session=abc"),
				slog.String("user_agent", "test-agent"),
			),
		),
	)

	out := buf.String()
	for _, secret := range []string{"nested-token", "session=abc"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked nested secret %q: %s", secret, out)
		}
	}

	var record map[string]any
	if err := json.Unmarshal(buf.Bytes(), &record); err != nil {
		t.Fatalf("decode JSON log: %v\n%s", err, out)
	}
	request, ok := record["request"].(map[string]any)
	if !ok {
		t.Fatalf("request group missing or wrong type: %v", record["request"])
	}
	if got := request["access_token"]; got != redacted {
		t.Errorf("request.access_token = %v, want %q", got, redacted)
	}
	headers, ok := request["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers group missing or wrong type: %v", request["headers"])
	}
	if got := headers["Cookie"]; got != redacted {
		t.Errorf("request.headers.Cookie = %v, want %q", got, redacted)
	}
	if got := headers["user_agent"]; got != "test-agent" {
		t.Errorf("request.headers.user_agent = %v, want test-agent", got)
	}
}

func TestNewLoggerRedactsBearerValues(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("info", "json", &buf)

	logger.Info("call",
		"upstream_auth", "Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
		"lowercase_auth", "bearer lower-token",
		"basic_auth", "Basic dXNlcjpwYXNz",
		"note", "no credentials here",
	)

	out := buf.String()
	for _, secret := range []string{"eyJhbGciOiJIUzI1NiJ9", "lower-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output leaked bearer token %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "Basic dXNlcjpwYXNz") {
		t.Errorf("non-bearer value was unexpectedly redacted: %s", out)
	}
	if !strings.Contains(out, "no credentials here") {
		t.Errorf("plain note was unexpectedly redacted: %s", out)
	}
}

func TestNewLoggerTextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("warn", "text", &buf)

	logger.Warn("upstream failed", "api_key", "key-123", "status", 503)

	out := buf.String()
	if !strings.Contains(out, "level=WARN") {
		t.Errorf("text output missing level: %s", out)
	}
	if !strings.Contains(out, "upstream failed") {
		t.Errorf("text output missing message: %s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Errorf("text output missing %q: %s", redacted, out)
	}
	if strings.Contains(out, "key-123") {
		t.Errorf("text output leaked secret: %s", out)
	}
	if !strings.Contains(out, "status=503") {
		t.Errorf("text output missing non-secret attribute: %s", out)
	}
}

func TestNewLoggerUnknownLevelFallsBackToInfo(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger("verbose", "json", &buf)

	ctx := context.Background()
	if logger.Enabled(ctx, slog.LevelDebug) {
		t.Error("unknown level enabled debug logging; want info fallback")
	}
	if !logger.Enabled(ctx, slog.LevelInfo) {
		t.Error("unknown level disabled info logging; want info fallback")
	}
	logger.Debug("filtered")
	if buf.Len() != 0 {
		t.Errorf("debug record written at info fallback level: %s", buf.String())
	}
}

func TestNewLoggerNilWriterDoesNotPanic(t *testing.T) {
	logger := NewLogger("error", "json", nil)
	if logger == nil {
		t.Fatal("NewLogger returned nil")
	}
	// Filtered, so nothing is written to the default os.Stderr.
	logger.Info("filtered")
}

func TestNewLoggerLevelNames(t *testing.T) {
	tests := []struct {
		level string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"bogus", slog.LevelInfo},
	}
	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			logger := NewLogger(tt.level, "json", &bytes.Buffer{})
			if got := logger.Enabled(context.Background(), tt.want); !got {
				t.Errorf("level %q does not enable %v", tt.level, tt.want)
			}
		})
	}
}
