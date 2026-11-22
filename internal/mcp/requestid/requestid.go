// SPDX-License-Identifier: Apache-2.0

// Package requestid carries the per-call request id on the context.
//
// The middleware assigns one id per MCP call and writes it to every audit row it
// creates, so one call can be followed across intent and outcome. Tools that
// drive their own operation ledger (the money tools) write their outcome row
// themselves, from inside internal/magento, which cannot import
// internal/mcp/server: that would be an import cycle. This package is the shared
// leaf both sides can import, so a self-ledgered row carries the same id as the
// middleware rows of the same call.
package requestid

import "context"

// key is unexported so no other package can collide with it or read the value
// without going through From.
type key struct{}

// With returns a context carrying id. An empty id is ignored, so a caller that
// has no id cannot erase one that is already there.
func With(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, key{}, id)
}

// From returns the request id on ctx, or "" when there is none.
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(key{}).(string); ok {
		return id
	}
	return ""
}