// SPDX-License-Identifier: Apache-2.0

//go:build !probe

// Package perfprobe holds the reproducible measurement harness behind
// docs/audit/performance-review.md.
//
// The measurements were taken against commit
// 66786a5d10c1f78d00e08a5b42d01fcc2da37a3f (the audit baseline). The harness
// drives the real internal/magento client against a fake Magento REST server,
// so it encodes the gateway behaviour of that commit. The remediation changed
// some of that behaviour on purpose (for example configurable-product creation
// now performs a SKU precheck that the fake does not model), which means the
// harness is historical evidence rather than a live gate.
//
// Run it explicitly with:
//
//	go test -tags probe ./docs/audit/perf-probe/
//
// It is expected to need updating against the fake before it passes on the
// current tree; that work is tracked as deferred in
// docs/audit/known-limitations.md. Under the default build tag this file keeps
// the package loadable so `go test ./...` stays green.
package perfprobe

import "testing"

// TestHarnessRequiresProbeTag documents why the measurements are not part of
// the default test run.
func TestHarnessRequiresProbeTag(t *testing.T) {
	t.Skip("audit measurement harness for commit 66786a5; run with -tags probe")
}
