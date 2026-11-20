#!/usr/bin/env bash
# Local pre-push verification. GitHub CI only runs on demand before a release.
set -euo pipefail

cd "$(dirname "$0")/.."

echo "== gofmt =="
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
  echo "These files need gofmt:"
  echo "$unformatted"
  exit 1
fi

echo "== go mod verify / vet =="
go mod verify
go vet ./...

echo "== go test -race (excluding e2e) =="
packages=()
while IFS= read -r pkg; do packages+=("$pkg"); done < <(go list ./... | grep -v '/test/e2e/')
# -json so skipped tests are visible. Without it `go test` prints only "ok" for a
# package whose tests all skipped, and the durability suite skips itself when
# MAGE2MCP_TEST_DATABASE_URL is unset: the gate used to report success while 11 of
# 13 storage tests never ran (QA-01).
test_log="$(mktemp)"
trap 'rm -f "$test_log"' EXIT
set +e
go test -race -count=1 -json "${packages[@]}" >"$test_log" 2>&1
test_status=$?
set -e
# Anything that is not a per-test JSON record is a build error or a panic.
grep -v '^{' "$test_log" || true
extract_tests() {
  grep -o "\"Test\":\"[^\"]*\",\"Action\":\"$1\"" "$test_log" |
    sed 's/.*"Test":"\([^"]*\)".*/  \1/' | sort -u
}
if [ "$test_status" -ne 0 ]; then
  echo "" >&2
  echo "local CI failed: go test exited $test_status" >&2
  extract_tests fail >&2 || true
  exit "$test_status"
fi
skipped="$(grep -c '\"Action\":\"skip\"' "$test_log" || true)"
if [ "${skipped:-0}" -gt 0 ]; then
  echo "" >&2
  echo "local CI failed: $skipped test(s) were skipped, so this gate did not cover them." >&2
  echo "Set MAGE2MCP_TEST_DATABASE_URL to a reachable PostgreSQL, for example:" >&2
  echo "  export MAGE2MCP_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/mage2mcp?sslmode=disable'" >&2
  extract_tests skip >&2 || true
  exit 1
fi
echo "go test: no failures, no skips"

echo "== golangci-lint =="
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run

echo "== docs =="
npm ci
npm run lint:md
npm run lint:links
npm run test:contracts

if command -v actionlint >/dev/null 2>&1; then
  echo "== actionlint =="
  actionlint
fi

if command -v shellcheck >/dev/null 2>&1; then
  echo "== shellcheck =="
  shellcheck scripts/*.sh .github/scripts/*.sh
fi

echo "local CI passed"
