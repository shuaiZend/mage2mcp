#!/usr/bin/env bash
# Independent QA suite runner (audit deliverable).
#
# This is NOT the developers' test suite. It drives the real gateway, the real
# PostgreSQL store, the real job queue, the real confirmation service and the real
# operation ledger against fake Magento upstreams, and it fails on ANY skip: a
# skipped test is not a passing test.
#
# Usage:
#   MAGE2MCP_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/db?sslmode=disable' \
#     ./scripts/qa-independent.sh
#
# The database must be disposable: the suite creates rows and never drops them.
set -euo pipefail

cd "$(dirname "$0")/.."

: "${MAGE2MCP_TEST_DATABASE_URL:?set MAGE2MCP_TEST_DATABASE_URL to a disposable PostgreSQL database}"

log="$(mktemp -t qa-independent.XXXXXX)"
trap 'rm -f "$log"' EXIT

echo "== independent QA suite: go test -count=1 -race -tags qa ./test/qa/... =="
set +e
go test -count=1 -race -tags qa -v ./test/qa/... 2>&1 | tee "$log"
status="${PIPESTATUS[0]}"
set -e

if grep -q -- '--- SKIP' "$log"; then
  echo "" >&2
  echo "qa-independent: FAIL - the suite skipped at least one test:" >&2
  grep -- '--- SKIP' "$log" >&2
  exit 1
fi

if grep -q -- '--- FAIL' "$log"; then
  echo "" >&2
  echo "qa-independent: FAIL - at least one test failed:" >&2
  grep -- '--- FAIL' "$log" >&2
  exit 1
fi

if [ "$status" -ne 0 ]; then
  echo "qa-independent: FAIL - go test exited ${status} (build or runtime error)" >&2
  exit "$status"
fi

passed="$(grep -c -- '--- PASS' "$log" || true)"
echo ""
echo "qa-independent: PASS - ${passed} test(s) passed, 0 skipped, 0 failed"