#!/usr/bin/env bash
# Classify a CI event so the expensive jobs can be skipped on documentation-only
# changes without ever skipping them by accident.
#
# Outputs (to $GITHUB_OUTPUT when set, otherwise to stdout so it can be tested
# locally):
#   go=true|false          whether the repository has a Go module
#   run_heavy=true|false   whether the Go, integration, Docker, and e2e jobs apply
#
# Fail-safe rules:
#   * schedule, workflow_dispatch, and any unknown event run everything;
#   * a missing base/before commit, or a diff that cannot be computed, runs
#     everything;
#   * a path that is not on the documentation allowlist runs everything.
#
# Environment:
#   EVENT_NAME  github.event_name
#   BASE_SHA    github.event.pull_request.base.sha   (pull_request only)
#   BEFORE_SHA  github.event.before                  (push only)
#   HEAD_SHA    github.sha
set -euo pipefail

event="${EVENT_NAME:-}"
base_sha="${BASE_SHA:-}"
before_sha="${BEFORE_SHA:-}"
head_sha="${HEAD_SHA:-}"
zero_sha="0000000000000000000000000000000000000000"

emit() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    echo "$1=$2" >> "$GITHUB_OUTPUT"
  else
    echo "$1=$2"
  fi
}

if [ -f go.mod ]; then
  has_go=true
else
  has_go=false
fi
emit go "$has_go"

changed=""
case "$event" in
  pull_request)
    if [ -n "$base_sha" ] && git cat-file -e "${base_sha}^{commit}" 2>/dev/null; then
      changed="$(git diff --name-only "${base_sha}...${head_sha}" || true)"
    fi
    ;;
  push)
    if [ -n "$before_sha" ] && [ "$before_sha" != "$zero_sha" ] \
      && git cat-file -e "${before_sha}^{commit}" 2>/dev/null; then
      changed="$(git diff --name-only "$before_sha" "$head_sha" || true)"
    fi
    ;;
esac

docs_only=true
if [ -z "$changed" ]; then
  docs_only=false
else
  while IFS= read -r path; do
    [ -z "$path" ] && continue
    case "$path" in
      *.md) ;;
      docs/*) ;;
      LICENSE|NOTICE|CODEOWNERS) ;;
      .markdownlint-cli2.jsonc) ;;
      .markdown-link-check.json|.markdown-link-check.external.json) ;;
      .github/ISSUE_TEMPLATE/*) ;;
      *)
        docs_only=false
        break
        ;;
    esac
  done <<< "$changed"
fi

if [ "$docs_only" = "true" ]; then
  emit run_heavy false
  echo "Documentation-only change: Go, integration, Docker, and e2e are skipped."
else
  emit run_heavy true
fi