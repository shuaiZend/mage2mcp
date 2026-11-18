# CI/CD

**Status:** Phase 0 baseline. The workflows in `.github/workflows/` are live. The Go
jobs are defined, reviewed, and merged, but skipped until `go.mod` lands.
**Owner:** Maintainer (Platform). Security-relevant steps require Security review.
**Related:** [Agent workflow](agent-workflow.md), [Review process](review-process.md),
[Testing strategy](testing-strategy.md), [Development plan](../development-plan.md),
[Threat model](../security/threat-model.md),
[ADR-0007](../adr/0007-multi-agent-development.md).

This document describes what CI proves, what it does not yet prove, and how the
pipeline may change. It is normative: a workflow that contradicts it is a bug in one
of the two, fixed in the same PR. Deployment automation, self-hosted runners, and
multi-provider CI are out of scope.

## 1. Goals and principles

| Principle | What it means here | How it is enforced |
|---|---|---|
| Fast feedback | A docs-only PR gets a verdict in under 3 minutes; a Go PR in under 10 | Job split, caching, concurrency cancellation (Section 6) |
| No skipped security check | Every gate is enforced or explicitly listed as not yet enforced; never quietly removed | Section 4 records a status per gate; blocking gates never use `continue-on-error` |
| Reproducible builds | The same commit and toolchain produce the same artifact | `-trimpath`, `CGO_ENABLED=0`, pinned toolchain and tool versions (Section 9) |
| Least-privilege tokens | `contents: read` by default; write scopes per job with a stated reason | Section 7 |
| No secrets in logs | PR workflows receive no secrets; release secrets are environment-scoped; `set -x` is banned | Section 7 |
| Fail closed | A missing tool, module, or unknown state fails the job instead of passing it; a skipped job is never treated as a pass | Section 3 |
| Auditable change | Every bypass, waiver, and ruleset change leaves a record in `docs/reviews/` | Sections 8 and 10 |

## 2. Workflow inventory

Only the check named `docs` is required today; the Phase 1 list is in Section 3.1.
Other workflows fail their runs, but a red run does not block a merge until its check
joins the ruleset.

### 2.1 `ci.yml`

| Attribute | Value |
|---|---|
| Triggers | `push` to `main`, `pull_request`, weekly schedule, `workflow_dispatch`; one concurrent run per ref, superseded runs cancelled |
| Jobs | `detect`, `docs`, `go` (matrix 1.25.x and 1.27.x), `external-links` |
| What each job proves | `docs`: documentation obeys `.markdownlint-cli2.jsonc` and every relative link resolves. `go`: code is gofmt-clean, vet-clean, builds, and passes the suite under `-race` on both toolchains, with coverage uploaded. `external-links`: external URLs are alive, advisory only. `detect`: whether the Go jobs apply |
| Runtime and failure | `docs` 1–2 min warm and blocking; `go` 3–5 min per matrix entry and blocking once required; `external-links` 3–6 min with `continue-on-error`, never blocking; `detect` fails only on checkout or shell errors |

### 2.2 `security.yml`

| Attribute | Value |
|---|---|
| Triggers | `push` to `main`, `pull_request`, weekly schedule, `workflow_dispatch` |
| Jobs | `detect`, `gitleaks`, `govulncheck`, `gosec`, `scorecard` |
| What each job proves | `gitleaks`: no credential shape is committed anywhere in history. `govulncheck`: no known vulnerability is reachable from the code. `gosec`: no high-severity, high-confidence finding, with all findings uploaded as SARIF. `scorecard`: the supply-chain posture is scored and published |
| Runtime and failure | `gitleaks` under 1 min, fails on a finding; `govulncheck` 1–2 min, fails on a reachable vulnerability; `gosec` 1–2 min, report-only first pass then a `-severity high -confidence high` blocking pass; `scorecard` about 1 min. SARIF uploads run with `if: always()` |

### 2.3 `dependency-review.yml`

| Attribute | Value |
|---|---|
| Triggers | `pull_request` only |
| Jobs | `dependency-review` |
| What it proves | The dependency diff introduces no advisory of High severity or above and no license on the deny list (GPL-2.0, GPL-3.0, AGPL-3.0, LGPL-2.1, LGPL-3.0, SSPL-1.0) |
| Runtime and failure | Under 1 min; fails the check and comments a summary on the PR. It never runs on `push`, so an administrator-bypass merge is not reviewed by this job |

### 2.4 `release.yml`

| Attribute | Value |
|---|---|
| Triggers | Push of any tag matching `v*` |
| Jobs | `release`, one sequential job on the `release` environment, maintainer as required reviewer |
| What it proves | The tagged commit builds five target binaries with a pinned toolchain; an SPDX SBOM and SHA-256 checksums are produced; every artifact is signed keyless with cosign; a multi-arch image is pushed to GHCR when a `Dockerfile` exists |
| Runtime and failure | 8–12 min, dominated by the multi-arch build; stops at the first failing step. GHCR is pushed before the GitHub release is created, so a late failure can leave an image without a release. There is no test step: the tag must be cut from a commit whose CI is green, verified by the release checklist, not by the workflow |

## 3. The `detect` gate

`detect` exists because Phase 0 has no `go.mod`. Without it, the Go jobs would be
permanently red, and a permanently red job teaches everyone to ignore CI. With it, the
job definitions stay merged, valid, and ready for the day the module appears.

| Repository state | `detect` | Go, govulncheck, gosec | Merge effect |
|---|---|---|---|
| No `go.mod` (today) | `go=false` | Skipped | Go gates absent; only `docs` is required |
| `go.mod` present | `go=true` | Run, not required | Gates visible; a red run does not block |
| Phase 1 ruleset applied | `go=true` | Run and required | A red run blocks; a skipped run would not |

The third row is the danger. GitHub treats a required check whose latest conclusion is
`skipped` as satisfying the requirement, not as a failure. If `go.mod` were deleted,
renamed, or moved while `go (1.25.x)` and `go (1.27.x)` were required, both checks would
report `skipped` and the PR could merge with no Go verification. Therefore:

1. A skipped job is never described as a passing job; the correct phrasing is
   "skipped, not applicable because `go.mod` is absent".
2. A check joins the required list only when the job runs unconditionally on every PR.
   The PR that adds the Go checks also replaces `detect` with a required `guard` job that
   fails when `go.mod` is absent; `detect` is deleted one release later. Any PR editing
   `.github/workflows/` states which jobs are skipped and why.

### 3.1 Required-check change in Phase 1

Phase 1 adds `go (1.25.x)`, `go (1.27.x)`, `govulncheck`, and `gosec` to the required
list. This is a repository ruleset update, not a code change: the job names already
exist and are already reported, so only the ruleset changes.

| Check name | Today | Phase 1 | Reported by |
|---|---|---|---|
| `docs` | Required | Required | `ci.yml` |
| `go (1.25.x)` | Skipped, not required | Required | `ci.yml` matrix |
| `go (1.27.x)` | Skipped, not required | Required | `ci.yml` matrix |
| `govulncheck` | Skipped, not required | Required | `security.yml` |
| `gosec` | Skipped, not required | Required | `security.yml` |
| `guard` | Does not exist | Required | `ci.yml` |
| `gitleaks` | Runs, not required | Recommended | `security.yml` |
| `dependency-review` | Runs, not required | Recommended | `dependency-review.yml` |

Job names are part of the interface. Renaming `go (1.25.x)` leaves the old name in the
required list with no job to report it, blocking every PR. A rename and a ruleset update
land in the same window, recorded in `docs/reviews/`.

## 4. Quality gates

Status: **Enforced** (runs and blocks), **Gated** (job exists, skipped until `go.mod`),
**Partial**, **Not enforced**.

| Gate | Command or mechanism | Enforcing workflow | Status in Phase 0 | Phase |
|---|---|---|---|---|
| gofmt | `gofmt -l .` | `ci.yml` `go` | Gated | 1 |
| go vet | `go vet ./...` | `ci.yml` `go` | Gated | 1 |
| Build | `go build ./...` | `ci.yml` `go` | Gated | 1 |
| golangci-lint | `golangci-lint run` | none | Not enforced: no config, no job | 1 |
| Tests | `go test ./...` | `ci.yml` `go` | Gated | 1 |
| Race detector | `go test -race` | `ci.yml` `go` | Gated | 1 |
| govulncheck | `govulncheck ./...` | `security.yml` | Gated | 1 |
| gosec | high severity, high confidence | `security.yml` | Gated | 1 |
| License audit | dependency review deny list | `dependency-review.yml` | Partial: npm manifest only, no Go module graph | 1 |
| SBOM | syft SPDX JSON | `release.yml` | Partial: release only, never on a PR | 1, enforced in 6 |
| Contract tests | `go test ./tests/contract/...` | none | Not enforced: no tests exist | 2 |
| Security tests | `go test ./test/security/...` | none | Not enforced: no tests exist | 1–2 |
| Independent review | One approval, review record on bypass | GitHub ruleset | Enforced by process | 0 |
| Docs | markdownlint and internal links | `ci.yml` `docs` | Enforced and required; red on `main` until referenced Phase 0 documents land | 0 |

"Gated" is not "enforced": a failing Go job is a signal a human must honor, not a merge
block. The docs gate is red because `README.md` and `docs/index.md` link to Phase 0
documents still being written, and a required check that fails on `main` blocks all PRs.

## 5. Gap analysis and remediation

| Gap | Why it is deferred | Phase | Acceptance criterion |
|---|---|---|---|
| No `.golangci.yml` and no lint job | No Go code to lint; a config without code cannot be validated | 1 | `golangci-lint run` passes on both toolchains with errcheck, govet, staticcheck, ineffassign, unused, and gosec enabled; the check is required |
| staticcheck not integrated | It ships through golangci-lint | 1 | staticcheck enabled with no blanket exclusions; every `//nolint` names a rule, a reason, and a tracking issue |
| No Go license audit | dependency review reads the diff, not the resolved module graph | 1 | A scan over the module graph fails on any license outside the allowlist in the license policy document |
| SBOM only at release | Per-PR SBOMs are noisy before dependencies exist | 1, enforced in 6 | Every PR generates an SBOM and posts added and removed components; a new component without license review fails |
| No container image scan | No Dockerfile until Phase 1 | 1, enforced in 6 | Trivy fails on unfixed HIGH or CRITICAL findings; the released digest is scanned before it is pushed |
| CodeQL not in a workflow | Default setup may be enabled in repository settings; this cannot be verified from the repository contents (`[U]`) | 1 | CodeQL runs on push and PR for Go, the check is required, and every alert is triaged or dismissed with a reason |
| No coverage threshold | No code exists; a threshold of zero is noise | 1 warning, 6 blocking | A script fails the job below core 80 percent, security 90 percent, overall 70 percent; generated code is excluded |
| No benchmark regression gate | No benchmarks exist | 6 | Hot-path benchmarks run on PR with benchstat; a regression above 10 percent fails or is waived with a record |
| No DCO or import-boundary check | `CONTRIBUTING.md` promises DCO "once CI is in place"; the boundary check needs the package layout | 1 | Every commit carries a `Signed-off-by` matching its author, and a `go list -deps` check fails on any forbidden module edge |
| No `go mod verify` or `go.sum` drift check | Threat model T-33 requires both; neither exists | 1 | CI fails on a modified `go.sum` without a `go.mod` change, and on any `go mod verify` failure |
| Actions pinned by tag, not SHA | All workflows use tags such as `@v4` | 1 | Every `uses:` carries a 40-character SHA with a version comment; a lint job rejects a tag reference |
| No image signature, provenance, fuzz, or load gate | Release signs blobs only; parsers do not exist yet | 6–7 | `cosign verify` and `gh attestation verify` succeed for the digest; fuzz runs 10 minutes nightly and load runs weekly against mocks |

## 6. Caching and performance

| Cache | Mechanism | Key | Saves | State |
|---|---|---|---|---|
| Go modules and build cache | `actions/setup-go`, `cache: true` | Hash of `go.sum` | 30–60 s per job | Configured |
| npm packages | `actions/setup-node`, `cache: npm` | Hash of `package-lock.json` | 15–25 s | Configured |
| Docker layers and buildx | `cache-from` and `cache-to` of type `gha` | Dockerfile plus `go.sum` | 3–6 min per release | Not configured; `docker/setup-buildx-action` is also missing, so the multi-arch push cannot work |

Runtime budgets to be measured in Phase 1:

| Job | Cold | Warm | Budget |
|---|---|---|---|
| `docs` and `detect` | 2m20s | 1m30s | 3 min |
| `go` per matrix entry | 4m30s | 3m | 6 min |
| `gitleaks` | 40s | 30s | 2 min |
| `govulncheck` and `gosec` | 3m30s | 2m30s | 5 min |
| `dependency-review` | 40s | 40s | 2 min |
| `release` | 12m | 8m | 15 min |

Keeping PR feedback under 10 minutes without dropping checks:

- Workflows run in parallel; within `ci.yml`, `docs` does not wait for `go`.
- The matrix uses `fail-fast: false` so one toolchain failure does not hide the other.
- `concurrency` cancels superseded runs on the same ref, so stale runs do not queue.
- Only `gitleaks` clones full history; expensive suites are scheduled, not per-PR.

What we will not do to save time: drop `-race`, drop the second `gosec` pass, or wrap a
failing check in retries. If the budget is exceeded, a suite moves to nightly with a
record in `docs/reviews/`, and stays required only while it runs on every PR.

## 7. Permissions and security of CI

| Workflow | Job | Permissions | Reason |
|---|---|---|---|
| `ci.yml` | all | `contents: read` | Checkout and read-only checks |
| `security.yml` | `gitleaks` | `contents: read`, `security-events: write` | Report to code scanning |
| `security.yml` | `govulncheck` | `contents: read` | Module download and scan |
| `security.yml` | `gosec` | `contents: read`, `security-events: write` | SARIF upload |
| `security.yml` | `scorecard` | `contents: read`, `security-events: write`, `id-token: write`, `actions: read` | OIDC publication |
| `dependency-review.yml` | `dependency-review` | `contents: read`, `pull-requests: write` | PR summary comment |
| `release.yml` | `release` | `contents: write`, `packages: write`, `id-token: write` | Release assets, GHCR, keyless signing |

`security.yml` currently grants `security-events: write` workflow-wide, including to
`gitleaks` and `govulncheck`; Phase 1 scopes it per job. No workflow may add a
permission outside this table without updating the table and passing Security review.

### 7.1 `pull_request_target` is banned

- It runs in the base repository context with a write-capable token and secrets, while
  the code under review is attacker-controlled; checking out the PR head under that
  trigger is arbitrary code execution with those privileges.
- No workflow here needs it: docs, Go, and security checks need read access only. The
  alternative is `pull_request` plus a `workflow_run` follow-up that never checks out
  untrusted code. An exception requires an ADR and Security review.

### 7.2 Action pinning policy

Actions are pinned to a full 40-character commit SHA with a version comment, and
Dependabot refreshes the pins weekly. Tools use an exact version, never `latest`;
`gosec v2.30.0` and `govulncheck v1.10.0` already do. npm devDependencies are exact with
a committed lockfile, currently 0.24.1 and 3.15.0. The current tag pinning (`@v4`,
`@v5`, `@v0`) plus an unpinned `cosign-release` is a supply-chain risk, not a style
preference: a tag can be moved to a different commit. SHA pinning is a Phase 1 gate.

### 7.3 Fork pull requests

- `GITHUB_TOKEN` is read-only for fork PRs regardless of the `permissions` block, so
  SARIF uploads and PR comments must be guarded by a
  `head.repo.full_name == github.repository` condition or they break external
  contributors.
- No secret and no `release` environment is available to a fork PR. A first-time
  contributor's run needs maintainer approval, which approves executing code, not the
  change itself. A change that needs a credential is tested by the maintainer on an
  upstream branch, never by giving a fork a secret.

### 7.4 Secret exposure rules

1. No repository or environment secret is exposed to a `pull_request` workflow;
   environment secrets exist only on `release`, only for the job that needs them.
2. `set -x` is banned; shell steps use `set -euo pipefail`. No `env`, `printenv`,
   `go env`, or configuration dump is uploaded or written to a step summary.
3. `GITHUB_TOKEN` is the only token in CI; a leaked secret is rotated, not deleted.

### 7.5 Artifact retention

| Artifact | Producer | Retention | Contents |
|---|---|---|---|
| `coverage-*` | `ci.yml` `go` | 14 days, set explicitly | Coverage profile only |
| `gosec.sarif`, `results.sarif` | `security.yml` | Code scanning retention | Static findings, posture score |
| `dist/*` | `release.yml` | Lifetime of the release | Binaries, SBOM, checksums, signature bundles |

## 8. Branch protection and merge policy

| Setting | Value | Rationale |
|---|---|---|
| Require a pull request | Yes | No direct pushes, including by the maintainer |
| Required approvals | 1 | The minimum meaningful independent review |
| Dismiss stale approvals on new commits | Yes | An approval applies to a commit, not a branch |
| Require review from CODEOWNERS | Yes | Ownership routing is enforced, not advisory |
| Require conversation resolution | Yes | An unresolved thread is an unaddressed finding |
| Required status checks | `docs` today; four more in Phase 1 | Section 3.1 |
| Require branches to be up to date | Yes | The tested merge result is the merged result |
| Require linear history | Yes | Squash merge only; no merge commits |
| Force push and deletion | Both blocked | Published history is shared; `main` cannot disappear |
| Tag ruleset | `v*` protected; creation and deletion restricted | A tag is a release identity |
| Bypass | Repository administrator only | Temporary, single maintainer |

Merge method is squash only. The PR description carries the rationale the squashed
commit would lose, and the DCO sign-off is preserved in the squashed message.

### 8.1 Administrator bypass

The ruleset lets the repository administrator bypass the approval requirement, because
there is exactly one maintainer and no second human reviewer. It is a documented
exception, not a silent capability.

- Every bypass merge requires a committed record at
  `docs/reviews/<pr-number>-review.md` with the contents defined in the
  [review process](review-process.md), and the PR description names it.
- The bypass is removed when a second maintainer joins, or 90 days after `v0.1.0`,
  whichever comes first. The `release` environment permits self-review for the same
  reason; when a second maintainer joins, "prevent self-review" is enabled.

### 8.2 Changing the ruleset

1. Open a PR that changes this section and names the exact rule being changed.
2. Obtain review. If merged by bypass, write the review record first.
3. The maintainer applies the change in Settings, Rules, `main`. Agents cannot: no
   workflow holds administration permission and no administrator token exists in CI.
4. Verify with a throwaway PR that the new check is required and that a skipped job
   does not silently satisfy it, then record the change and the verification.

## 9. Release process

### 9.1 Version policy for v0.x

| Change | Bump | Example |
|---|---|---|
| Breaking change to a tool name, schema, or public interface | MINOR | 0.3.0 to 0.4.0 |
| New backward-compatible capability or module | MINOR | 0.2.0 to 0.3.0 |
| Bug fix with no interface change | PATCH | 0.3.0 to 0.3.1 |
| Pre-release candidate | Suffix | 0.4.0-rc.1 |

`1.0.0` waits for the Phase 6 exit criteria and 30 days with no open Critical or High
finding. Pre-1.0, a breaking change is legal but needs an ADR and a changelog entry; it
is never shipped silently in a patch.

### 9.2 Release steps

| Step | Who | Evidence |
|---|---|---|
| 1. Release issue with version, scope, and linked PRs; confirm `main` is green on every required check | Maintainer | Issue and CI run URL |
| 2. Update `CHANGELOG.md` and both READMEs if behavior changed | Maintainer | Commit |
| 3. Create and push a signed annotated tag on a `main` commit | Maintainer | `git tag -s v0.4.0` |
| 4. Approve the `release` environment deployment, then verify artifacts, SBOM, checksums, signatures, image digest, and notes | Maintainer | Environment log and release checklist |

Build matrix: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
`windows/amd64`, with `CGO_ENABLED=0`, `-trimpath`, and
`-ldflags "-s -w -X main.version=<tag>"`. Darwin binaries are cross-compiled and not
smoke-tested in CI; the notes say so. Artifacts: one binary per target, the SPDX SBOM,
`checksums.txt`, a `.bundle` per signed artifact, and the GHCR image. The image carries
the version tag and `latest`; `latest` moves only for a stable release.

```bash
sha256sum -c checksums.txt
cosign verify-blob --bundle mage2mcp_linux_amd64.bundle mage2mcp_linux_amd64
cosign verify ghcr.io/shuaizend/mage2mcp:v0.4.0 \
  --certificate-identity-regexp '^https://github.com/shuaiZend/mage2mcp/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Signing is keyless through the workflow's OIDC identity: no long-lived signing key
exists. The SBOM is SPDX JSON; that format decision belongs in an ADR before the first
release. Release notes are generated from merged PR titles and then curated by the
maintainer: security fixes link their advisory, breaking changes state the migration
path, and behavior verified only against mocks is labeled as such.

### 9.3 No agent publishes a release

An agent may prepare the release PR, update the changelog, draft notes, and run
verification. An agent may not create or push a tag, approve the `release` environment,
run `gh release create`, edit a published release, push to GHCR, or sign with a release
identity. Publication is a human action with a human identity, because it is where
reviewed code becomes a supported artifact. A release performed by an agent is invalid.

### 9.4 Rollback

A published version is never reused and a release is never silently deleted. If a
release is broken: warn in the notes, publish a patch, re-point `latest` to the previous
known-good digest, and open an advisory if the defect is security-relevant. A tag may be
deleted only before the release job completes, recorded in the release issue.

## 10. Failure playbook

### 10.1 Flaky tests

- A flaky test is quarantined, not retried. No workflow may add a retry wrapper to turn
  a flaky failure green.
- Quarantine means a `t.Skip` naming a tracking issue, for example
  `t.Skip("flaky: #123")`, merged in a reviewed PR. The issue carries the `flaky` label,
  the failure rate, and the suspected cause; fix or delete within 14 days.
- A quarantined test guarding a security control (auth, RBAC, confirmation, audit)
  blocks the release until fixed.

### 10.2 Other failures

| Situation | Immediate action | Prohibited | Follow-up |
|---|---|---|---|
| Dependency or registry outage | Re-run once, then wait or pin to the last known-good version | Disabling the check; merging code on red | Open an issue; a docs-only merge may be waived and recorded after an hour |
| Compromised action | Disable the workflow, remove the action, rotate reachable secrets, audit runs since the compromise, invalidate caches | Running it again "to see" | Re-release affected artifacts; advisory if one shipped |
| Leaked secret | Rotate first, then remove it from logs and artifacts, then investigate access | Deleting the commit and calling it fixed | Incident note in `docs/reviews/`; scanner rule if the shape was new |
| Broken release | Stop, warn in the notes, publish a patch, re-point `latest` | Deleting the tag after publication; reusing the version | Release issue with timeline and root cause |
| Scanner false positive | Annotate with a justification and a tracking issue | A blanket rule exclusion or repository-wide disable | Security review of the annotation |

## 11. Cost and maintenance

| Item | Current | Notes |
|---|---|---|
| Actions minutes | Free for standard runners on a public repository | Roughly 400–600 minutes per month at 30 PRs, two weekly schedules, and two releases |
| Artifact storage | Two coverage profiles per Go PR run, 14-day retention | Well under 100 MB at any time |
| Release assets | Five binaries, SBOM, checksums, signature bundles | 60–120 MB per release, stored with the release; code scanning is free for public repositories |

Dependency update cadence: Go modules, GitHub Actions, and Docker base images weekly;
npm docs tooling monthly. A major bump is a normal reviewed PR, and an action major bump
is reviewed for permission changes.

The Maintainer owns CI and is the only person who changes the ruleset. Security reviews
anything touching authentication, secrets, signing, or permissions; Platform implements
workflow changes. No agent merges a workflow change without a review record on bypass.

## 12. Open questions and decision record

| Question | Owner | Resolution point |
|---|---|---|
| CodeQL default setup versus an advanced workflow | Maintainer | Phase 1, verified in repository settings |
| Require two approvals once a second maintainer joins | Maintainer | When the second maintainer joins |
| Replace the hand-written release build with GoReleaser | Platform | Phase 7, before the first public release |

| Decision | Record |
|---|---|
| Multi-agent development, review records, independent review | [ADR-0007](../adr/0007-multi-agent-development.md) |
| SHA pinning, `go mod verify`, SBOM, provenance | [Threat model](../security/threat-model.md), T-33 and T-35 |
| CI provider change, self-hosted runners, a new workflow permission, or the release signing format | ADR required |
