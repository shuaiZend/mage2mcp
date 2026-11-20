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
go test -race -count=1 "${packages[@]}"

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
