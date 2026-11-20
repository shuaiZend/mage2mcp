# CI/CD

**Status:** as-built for v0.11.x plus the production-readiness remediation
(2026-11-20). Day-to-day verification is local; GitHub Actions runs on demand
before a release, not on every push or pull request.

**Owner:** Maintainer (Platform). **Related:**
[Testing strategy](testing-strategy.md), [Release checklist](../release-checklist.md),
[Threat model](../security/threat-model.md), [ADR-0007](../adr/0007-multi-agent-development.md).

## 1. The model

| Layer | What runs | When |
|---|---|---|
| Local verification | `scripts/local-ci.sh` | Before every push/PR, by the author |
| GitHub `ci.yml` | `verify`, `integration`, `docker`, optional `e2e`, `ci-gate` | Manual `workflow_dispatch` only |
| GitHub `security.yml` | `detect`, `gitleaks`, `govulncheck`, `gosec`, `scorecard` | Weekly schedule and manual dispatch |
| GitHub `dependency-review.yml` | `dependency-review` | Dependabot pull requests only |
| GitHub `release.yml` | `verify`, `publish` | Push of a `v*` tag |

There is no per-commit GitHub CI. A red local run is the normal failure signal;
the manual workflow exists to reproduce the release environment, not to gate
every commit.

## 2. `scripts/local-ci.sh`

This is the day-to-day gate. It fails on the first error and runs:

```bash
gofmt -l .
go mod verify
go vet ./...
go test -race -count=1 <all packages except /test/e2e/>
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run
npm ci
npm run lint:md
npm run lint:links
npm run test:contracts
```

If `actionlint` or `shellcheck` is installed, it also runs them. It does **not**
run the PostgreSQL integration tests, the Docker build, or the fake-Magento e2e
harness; those are in the manual workflow (and the e2e harness can be run
directly).

## 3. `.github/workflows/ci.yml` — manual pre-release

**Trigger:** `workflow_dispatch` with two inputs:

| Input | Default | Meaning |
|---|---|---|
| `run_e2e` | `true` | Run the Docker/Magento fake e2e smoke test |
| `go_version` | `1.27.x` | Go toolchain for `verify` and `integration` |

**Jobs:**

| Job | What it proves |
|---|---|
| `verify` | gofmt-clean; `go mod verify` and no `go.mod`/`go.sum` drift; `go vet`; golangci-lint v2.15.0; race tests excluding `test/e2e`; total coverage ≥ **70%**; documentation gates (`npm run lint:md`, `lint:links`, `test:contracts`); every action is SHA-pinned |
| `integration` | PostgreSQL 17 service; `go test -race -count=1 ./internal/storage/...` against `MAGE2MCP_TEST_DATABASE_URL` |
| `docker` | Builds the image, runs it with `MAGE2MCP_AUTH_ENABLED=false` / `MAGE2MCP_DEV_INSECURE=true` on a loopback port, checks `/healthz`, `/readyz`, `/metrics` (`mage2mcp_build_info`), and the non-root user |
| `e2e` | `./test/e2e/run.sh` (fake Magento + PostgreSQL), only when `run_e2e` is true |
| `ci-gate` | Fails if any non-skipped job failed or was cancelled; a skipped optional job is not a failure |

The coverage floor is enforced with `go tool cover -func` and `awk`; there is no
per-package threshold. The documentation gates are exactly the three npm scripts
above, not a separate workflow.

## 4. `.github/workflows/security.yml` — weekly and manual

**Trigger:** weekly schedule (Wednesday 04:23 UTC) and `workflow_dispatch`.
No per-commit runs.

| Job | What it proves |
|---|---|
| `detect` | Classifies the change (Go present, heavy scan needed) |
| `gitleaks` | Scans history for committed secrets (`gitleaks v8.30.1`) |
| `govulncheck` | No reachable known vulnerability (`govulncheck v1.10.0`) |
| `gosec` | Static security findings, high severity/high confidence |
| `scorecard` | OpenSSF supply-chain posture; publishes results |

`gitleaks` runs unconditionally; `govulncheck` and `gosec` run when `detect`
reports Go code and a heavy scan. The workflow has `contents: read` and
`actions: read`; only `gosec`/`scorecard` get `security-events: write`.

## 5. `.github/workflows/dependency-review.yml`

**Trigger:** `pull_request` opened/synchronized/reopened, job-level
`if: github.actor == 'dependabot[bot]'`.

`dependency-review` fails on a High-severity advisory and on a license outside
the allowlist (`0BSD`, `Apache-2.0`, `BSD-2-Clause`, `BSD-3-Clause`, `CC0-1.0`,
`CC-BY-4.0`, `ISC`, `MIT`, `MIT-0`, `Unlicense`, and the Go patent-license
reference). An unknown license fails closed. It comments a summary on failure.

## 6. `.github/workflows/release.yml` — tag push

**Trigger:** push of `v*`.

| Job | What it does |
|---|---|
| `verify` | Confirms the tag is on `main`; gofmt, `go vet`, `go mod verify`, `go test -race ./...`, `govulncheck`; builds release binaries, SPDX SBOM, third-party notices; uploads `release-dist` |
| `publish` | `release` environment (required reviewer); downloads the artifact, generates `checksums.txt`, signs each artifact keyless with cosign, attests the image, publishes the GitHub release and the multi-arch GHCR image |

The `verify` job has `contents: read` only; `publish` has `contents: write`,
`packages: write`, `id-token: write`, and `attestations: write`. A tag is never
cut by an agent; publication is a human action.

## 7. What is not automated

- No per-commit GitHub Actions. Local CI is the author's responsibility; the
  release workflow reruns the Go gates before publishing.
- No live Magento integration test. `test/e2e` uses a fake Magento; see
  [known limitations](../audit/known-limitations.md) §1.1.
- No live Cloudflare or GA4 test.
- No deployment automation; the image is published, not deployed.
- No CodeQL workflow; if repository-level default setup is enabled, it is not
  described by this file.

## 8. Verification

The job names, triggers, and steps above were read from
`.github/workflows/{ci,security,dependency-review,release}.yml` and
`scripts/local-ci.sh`. The documentation gates are the npm scripts in
`package.json`; the coverage floor is the `awk` check in `ci.yml`. No test
asserts this document, so a workflow change must update it in the same PR.
