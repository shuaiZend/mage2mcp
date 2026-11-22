#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Fail when a top-level test function disappeared between two git revisions.
#
# A deleted test leaves valid Go code that compiles and passes, so the suite
# stays green while the coverage it provided is gone. This gate compares the
# test names of two revisions and fails on a removal. See
# scripts/testinventory for the logic and its tests.
#
# Usage: scripts/check-test-inventory.sh [--base <ref>] [--head <ref>]
#                                        [--allow-removed <TestName>]... [--repo <dir>]
# Run with --help for the full text.
set -euo pipefail

# Run from the repository root so the default --repo . is the module root, no
# matter which directory the caller invoked the script from.
cd "$(dirname "$0")/.."

exec go run ./scripts/testinventory/cmd "$@"
