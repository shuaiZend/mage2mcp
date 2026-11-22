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

# A deleted test cannot fail, so the suite stays green without it (one agent
# rewrote a test file and dropped three tests another agent had appended). This
# check compares test names with origin/main and fails on a removal. It runs
# before the compile-and-test steps because it is fast and catches the loss
# early. It reads the working tree, not just HEAD: a test deleted but not yet
# committed is exactly the case a pre-push gate has to catch. `git stash create`
# snapshots the tracked working tree without touching it or creating a ref, and
# prints nothing on a clean tree, so the fallback is plain HEAD.
echo "== test inventory =="
snapshot="$(git stash create || true)"
if [ -n "$snapshot" ]; then
  bash scripts/check-test-inventory.sh --head "$snapshot"
else
  bash scripts/check-test-inventory.sh
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
  # `go test -json` writes "Action" before "Test", so matching
  # "Test":...,"Action":... never fires: a failing run printed only the exit code
  # and no test names. Match the action first, then pull the name out on its own
  # (the same shape the skip check below uses).
  grep "\"Action\":\"$1\"" "$test_log" | grep -o '"Test":"[^"]*"' |
    sed -e 's/^"Test":"//' -e 's/"$//' | sort -u | sed 's/^/  /'
}
if [ "$test_status" -ne 0 ]; then
  echo "" >&2
  echo "local CI failed: go test exited $test_status" >&2
  extract_tests fail >&2 || true
  exit "$test_status"
fi
# `go test -json` emits a package-level skip record for every package with no
# test files, and a test-level record only when a test actually skipped.
# Counting both made this gate fail on every run since b0bb42c (the package-level
# records are normal, not skipped tests). Package-level records are reported as
# information; only a record carrying "Test" is a skipped test.
package_skips="$(grep '"Action":"skip"' "$test_log" | grep -vc '"Test":' || true)"
if [ "${package_skips:-0}" -gt 0 ]; then
  echo "note: ${package_skips} package(s) have no test files (not a failure):"
  grep '"Action":"skip"' "$test_log" | grep -v '"Test":' |
    sed -E 's/.*"Package":"([^"]*)".*/  \1/' | sort -u
fi

# Tests whose skip is correct under the default build tag. The list is explicit
# on purpose: a blanket "a skip is fine" would remove this gate's meaning.
#   TestHarnessRequiresProbeTag — docs/audit/perf-probe runs only with
#     `-tags probe`; the package documents the harness as historical evidence,
#     so skipping under the default build is correct.
exempt_skips=(TestHarnessRequiresProbeTag)
# `go test -json` writes "Action" before "Test", so pull the name out on its own.
skipped_tests="$(grep '"Action":"skip"' "$test_log" | grep -o '"Test":"[^"]*"' |
  sed -e 's/^"Test":"//' -e 's/"$//' | sort -u || true)"
unexpected=""
while IFS= read -r name; do
  if [ -z "$name" ]; then
    continue
  fi
  exempt=false
  for allowed in "${exempt_skips[@]}"; do
    if [ "$name" = "$allowed" ]; then
      exempt=true
      break
    fi
  done
  if [ "$exempt" = false ]; then
    unexpected="${unexpected}  ${name}"$'\n'
  fi
done <<< "$skipped_tests"
if [ -n "$unexpected" ]; then
  echo "" >&2
  echo "local CI failed: these tests were skipped, so this gate did not cover them:" >&2
  printf '%s' "$unexpected" >&2
  echo "Set MAGE2MCP_TEST_DATABASE_URL to a reachable PostgreSQL, for example:" >&2
  echo "  export MAGE2MCP_TEST_DATABASE_URL='postgres://user:pass@127.0.0.1:5432/mage2mcp?sslmode=disable'" >&2
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
