# GitHub Actions bump review: PR #46

**Status:** review complete (2026-11-20). Verdict: **merge, after the one-line comment
fix in §4 F1**. No functional incompatibility was found; the release-only pins need a
validation run at the next release (§4 F3).
**Owner:** Maintainer (Platform). CI and supply-chain changes also require Security
review.
**Related:** [CI/CD](ci-cd.md) (§7.2 action pinning policy), [Review
process](review-process.md) (dependency-PR review duty), [Risk register](risk-register.md)
R14, [PR #46](https://github.com/shuaiZend/mage2mcp/pull/46).

PR #46 (`chore(ci): bump the github-actions group across 1 directory with 17 updates`,
head `aa370c108169ac737f9fea1d25f8e97df3d54c01`, base `0ad79b6`) replaces 17 action pins
in five workflow files. It changes no workflow logic, no `with:` inputs, no permissions
and no triggers. This document records the review of every pin: what the new major
changes, whether this repository's usage survives it, and what must happen before and
after the merge.

## 1. Verdict per action

Runtime is the action's own runtime (`runs.using`), not the Node version the `docs` job
installs for the project.

| Action | From → to | Runtime | Breaking surface that matters here | Verdict |
|---|---|---|---|---|
| `actions/checkout` | v4 → v7.0.1 | node24 | Fork-PR checkout is blocked under `pull_request_target`/`workflow_run`; credentials persist to a separate file (v6). Neither trigger is used and no step reads `.git/config` credentials | Safe |
| `actions/setup-node` | v4 → v7.1.0 | node24 | ESM bundle; the dummy `NODE_AUTH_TOKEN` export is gone. No npm publish step exists; `cache: npm` and `node-version: "22"` unchanged | Safe |
| `actions/setup-go` | v5 → v7.0.0 | node24 | ESM bundle, `@actions/cache` 6.2.0. `go-version` and `cache: true` unchanged | Safe |
| `actions/upload-artifact` | v4 → v7.0.2 | node24 | ESM; new optional `archive` (default `true`) and `overwrite` (default `false`) inputs. Directory uploads still zip; `if-no-files-found` and `retention-days` kept | Safe |
| `actions/download-artifact` | v4 → v8.0.2 | node24 | Digest mismatch now fails by default (`digest-mismatch: error`); non-zipped downloads are no longer auto-extracted; the v5 path change only affected downloads **by ID**. We download one named, zipped artifact | Safe, release-only |
| `actions/dependency-review-action` | v4 → v5.0.0 | node24 | Runner floor 2.327.1; inputs unchanged (`fail-on-severity`, `allow-licenses`, `comment-summary-in-pr` all still declared) | Safe — **comment says `# v4`**, see F1 |
| `github/codeql-action/upload-sarif` | v3 → v4.39.3 | node24 | v4 line; minimum CodeQL bundle 2.20.7 applies to `init`/`analyze`, not to `upload-sarif`; `sarif_file` unchanged | Safe (executed green in the PR run) |
| `ossf/scorecard-action` | v2.4.0 → v2.5.0 | docker | Minor; stops echoing the token; `results_file`/`results_format`/`publish_results` unchanged | Safe, schedule-only |
| `anchore/sbom-action` | v0 → v0.24.3 | node24 | 0.x minor chain; `path`/`format`/`output-file` unchanged | Safe (executed green in the PR run) |
| `sigstore/cosign-installer` | v3 → v4.1.3 | composite | v4 installs **cosign v3** by default (`v3.1.4`); v3 requires `--bundle` on `sign-blob`. The workflow already passes `--bundle`; image `cosign sign` is unchanged | Safe, release-only; see F2 |
| `docker/setup-qemu-action` | v3 → v4.4.0 | node24 | Runtime only; no inputs used | Safe, release-only |
| `docker/setup-buildx-action` | v3 → v4.4.1 | node24 | Removed deprecated inputs/outputs; none are used | Safe, release-only |
| `docker/metadata-action` | v5 → v6.2.0 | node24 | ESM; list inputs now keep `#` inside values. The `tags:` block has no `#` and the expressions are evaluated by GitHub before the action runs | Safe, release-only |
| `docker/login-action` | v3 → v4.8.0 | node24 | Runtime only; `registry`/`username`/`password` unchanged | Safe, release-only |
| `docker/build-push-action` | v6 → v7.4.0 | node24 | Removed deprecated `DOCKER_BUILD_NO_SUMMARY`/`DOCKER_BUILD_EXPORT_RETENTION_DAYS` envs (not set); `provenance`, `load`, `sbom`, `cache-from`/`cache-to` kept | Safe, release-only |
| `actions/attest-build-provenance` | v2 → v4.2.2 | composite | v4 is a wrapper over `actions/attest`; `subject-path` and the `id-token`/`attestations` permissions are unchanged | Safe, release-only |
| `softprops/action-gh-release` | v2 → v3.0.4 | node24 | Runtime moved from Node 20 to Node 24 only; `files`/`generate_release_notes` unchanged | Safe, release-only |

"release-only" means the pin is only reached by `.github/workflows/release.yml` (tag
push), so it is not exercised by pull-request CI. See §5.

## 2. Method

1. Read the full diff (`gh pr diff 46`, 336 lines, five files, no non-workflow files).
2. Resolved **every** new SHA against the upstream tag with
   `git ls-remote --tags https://github.com/<repo>.git 'refs/tags/<tag>^{}'` and compared
   it with the version in the trailing comment: **17/17 match** the comment. A moved or
   mistyped pin is the one failure mode a dependabot diff cannot be trusted on.
3. Fetched `action.yml`/`action.yaml` at each pinned SHA and compared the declared inputs
   with the inputs the workflows actually pass. Every input used by this repository is
   still declared, so no `with:` key silently becomes a no-op.
4. Read the release notes/changelog for each major transition (upload-artifact v5/v6/v7,
   download-artifact v5–v8, checkout v7, setup-node v7, setup-go v7, codeql v4 line,
   cosign-installer v4, the docker action majors, attest v3/v4, gh-release v3).
5. Parsed the five post-PR workflows as YAML and re-ran the `actions-pinned` gate's
   regular expression against them: every `uses:` is a 40-character SHA, no tag pin.
6. Inspected the PR's own runs at `aa370c1` job by job and step by step to record which
   pins actually executed (§6).

## 3. Breaking-change analysis

### 3.1 Node 20 → Node 24 action runtime

Fourteen of the seventeen pins move the action runtime to `node24`. That requires
Actions Runner **v2.327.1 or newer**. Every job in this repository uses
`runs-on: ubuntu-latest` (GitHub-hosted), which is always at or above that floor; there
are no self-hosted runners in the workflows. The project's own Node version is
unaffected: the `docs` and `external-links` jobs still request `node-version: "22"`,
which `setup-node@v7` installs as before. The composite actions
(`attest-build-provenance`, `cosign-installer`) are unaffected by the runtime change.

**Verdict:** no action needed, no workflow change required.

### 3.2 `actions/checkout` v4 → v7.0.1

v6 moved persisted credentials to a separate file; v7 blocks checking out fork pull
requests under `pull_request_target` and `workflow_run` (a new
`allow-unsafe-pr-checkout` input defaults to `false`). This repository uses
`pull_request`, `push` (branches and tags), `schedule` and `workflow_dispatch` only —
`pull_request_target` is explicitly banned by [CI/CD §7.1](ci-cd.md). The `release`
`verify`/`publish` jobs and the `scorecard` job already pass
`persist-credentials: false`, and no step reads credentials out of `.git/config`; the
only post-checkout `git fetch` (`release.yml` verify) targets the public repository and
already runs without persisted credentials.

**Verdict:** safe. The fork-PR restriction is a no-op here and stays a no-op as long as
§7.1 holds.

### 3.3 Artifact pipeline: `upload-artifact` v7.0.2 + `download-artifact` v8.0.2

The pair is bumped together, which is required: `download-artifact@v8` needs
`upload-artifact@v4` or newer. Behaviour that changed:

- `upload-artifact@v7` archives by default (`archive: 'true'`), exactly as v4 did, so the
  directory uploads (`path: dist`, `path: coverage.out`) still produce one zip.
- `download-artifact@v8` no longer tries to unzip every file; it checks `Content-Type`
  first. Because the `release-dist` artifact is a zip, the existing
  `name: release-dist` / `path: dist` download still extracts into `dist/`.
- `digest-mismatch` now defaults to `error` instead of warning. A corrupted upload fails
  the publish job instead of shipping. That is the desired direction for a release.
- The v5 breaking change ("inconsistent path behaviour") applied only to downloads **by
  artifact ID**; both call sites download by name.

**Verdict:** safe. `release.yml:95` is the only consumer and it is release-only.

### 3.4 `sigstore/cosign-installer` v3 → v4.1.3

v4 exists to install cosign v3; its default `cosign-release` input is `v3.1.4`. The
installer's own release notes flag the one breaking change that affects callers:
`cosign sign-blob` requires `--bundle` from v3 on. `release.yml:108` already passes
`--bundle "${f}.bundle"`. The image signature path (`cosign sign --yes
ghcr.io/...@<digest>`, `release.yml:148`) is unchanged in v3: the keyless bundle is still
written as a registry annotation, so the signature stays verifiable with the existing
`cosign verify` recipe.

Two notes rather than blockers:

- cosign v3 changed the bundle format. The `.bundle` files attached to the release are
  consumed by our own release checklist, not by third parties, but the first v3-signed
  release should be verified once with `cosign verify-blob --bundle` before the notes
  advertise it.
- `cosign-release` is not pinned in the workflow, so the installed binary follows the
  action's default. [CI/CD §7.2](ci-cd.md) already calls an unpinned `cosign-release` a
  supply-chain risk; see F2.

**Verdict:** safe for the current commands; pin the binary version and verify one
signature (F2, F3).

### 3.5 `actions/dependency-review-action` v4 → v5.0.0

v5 is the node24 line of the same action. `action.yml` at the pinned SHA still declares
`fail-on-severity`, `allow-licenses` and `comment-summary-in-pr`, so the allowlist gate
and the `on-failure` comment behaviour are unchanged. The job already has
`pull-requests: write`, which `comment-summary-in-pr` requires.

**One defect in the diff:** `dependency-review.yml:16` pins the v5.0.0 commit but the
trailing comment still says `# v4`. The `actions-pinned` gate only validates that a SHA
is present, so CI cannot catch this; the comment is the only human-readable record of
which version is deployed. F1 fixes it.

**Verdict:** safe, with the comment fixed in the same change.

### 3.6 `github/codeql-action/upload-sarif` v3 → v4.39.3

v4 is the current major line of the CodeQL Action. The changelog's breaking entries
(minimum CodeQL bundle 2.20.7 in 4.39.0, GHES-version discontinuations) apply to
`init`/`analyze`; this repository only uses `upload-sarif`, which takes a SARIF file and
uploads it. `sarif_file` is still declared, and both jobs that use it (`gosec`,
`scorecard`) already grant `security-events: write`.

The PR's own Security run executed the `gosec` → `Upload SARIF` step on
`codeql-action@33d7b85` and it succeeded (§6), so this pin is empirically validated on
the path we use. `scorecard` is schedule-only and therefore skipped in PR CI, but it
calls the same action with the same input shape.

**Verdict:** safe.

### 3.7 docker action majors (`setup-qemu` v4, `setup-buildx` v4, `metadata` v6, `login` v4, `build-push` v7)

All five are the same maintenance release: node24 runtime, ESM bundle, dependency
refresh. The only removals are deprecated inputs/outputs in `setup-buildx@v4` and the
deprecated `DOCKER_BUILD_NO_SUMMARY`/`DOCKER_BUILD_EXPORT_RETENTION_DAYS` environment
variables in `build-push@v7`; this repository passes no inputs to `setup-qemu` or
`setup-buildx` and sets neither variable. The `metadata@v6` list-parsing change preserves
`#` inside values, and the `tags:` block contains no `#`.

`build-push@v7` still declares every input used by `release.yml:136` (`context`, `push`,
`build-args`, `platforms`, `tags`, `labels`, `provenance`) and by the buildx-cache steps
added by the CI-speed work (`load`, `sbom`, `cache-from`, `cache-to`), so the cache
plumbing survives the major bump.

**Verdict:** safe, release-only.

### 3.8 `actions/attest-build-provenance` v2 → v4.2.2

v3 moved the runtime to node24 and v4 turned the action into a thin wrapper over
`actions/attest`. The interface used here (`subject-path: "dist/*"`) and the required
permissions (`id-token: write`, `attestations: write`, both present on the `publish`
job) are unchanged. The provenance predicate is still generated by
`attest-build-provenance/predicate`, which the wrapper pins internally.

**Verdict:** safe, release-only.

### 3.9 `softprops/action-gh-release` v2 → v3.0.4

v3.0.0 is a runtime-only major: Node 20 → Node 24, floating major tag kept on `v3`.
`files: dist/*` and `generate_release_notes: true` are unchanged, and the job has
`contents: write`.

**Verdict:** safe, release-only.

### 3.10 `ossf/scorecard-action` v2.4.0 → v2.5.0

Minor bump inside v2. It no longer prints the token while validating options; the
`publish_results: true` OIDC path is unchanged. The action runs as
`docker://ghcr.io/ossf/scorecard-action:v2.5.0`, which matches the pinned SHA's version.

**Verdict:** safe, schedule-only.

### 3.11 `anchore/sbom-action` v0 → v0.24.3

0.x chain, so a "minor" bump can still change behaviour; the changelog for this range is
dependency and node24 updates. `path`, `format` and `output-file` are still declared, and
the PR's Release Dry Run executed the `Generate SBOM` step on this pin successfully
(§6). Note that the trailing comment changed from `# v0` to `# v0.24.3`, which is more
precise than the rest of the file and therefore inconsistent with it; harmless, and the
convention question is left to F4.

**Verdict:** safe.

## 4. Required follow-ups

Ordered by when they must happen. F1 belongs in the merge; F2/F3 are before or right
after it; F4/F5 are optional.

**F1 — fix the version comment (must land with the merge).**
`.github/workflows/dependency-review.yml:16`: change the trailing `# v4` to `# v5`. The
pinned commit `a1d282b36b6f3519aa1f3fc636f609c47dddb294` is the `v5.0.0` tag (verified
against upstream), so the current comment misstates the deployed major version. The
`actions-pinned` gate cannot detect this.

**F2 — pin the cosign binary.**
`.github/workflows/release.yml:102`: give the installer an explicit
`with: cosign-release: v3.1.4` (the version `cosign-installer@v4.1.3` already defaults
to). [CI/CD §7.2](ci-cd.md) lists an unpinned `cosign-release` as a supply-chain risk,
and the major bump is the moment the cosign binary changed under us.

**F3 — validate the release-only pins before the release is announced.**
`download-artifact@v8`, `cosign-installer@v4`, the five docker majors,
`attest-build-provenance@v4` and `action-gh-release@v3` are reachable only from
`release.yml` on a `v*` tag, so no pull-request run proves them. Push a throwaway
pre-release tag (for example `v0.9.0-rc.1`) and confirm: the `publish` job downloads
`release-dist` into `dist/`, `sha256sum ./*` sees the files, `cosign sign-blob` produces
one `.bundle` per file, the multi-arch image is pushed and signed, the provenance
attestation is created, and the GitHub release is published. If any step fails, re-pin
that single action to its previous SHA — the bumps are independent, so a rollback is one
line.

**F4 — decide the version-comment convention (optional).**
`anchore/sbom-action` now carries `# v0.24.3` while every other pin carries a major
(`# v7.0.1`, `# v7.1.0`, `# v4.39.3`, `# v2.5.0`, …). Both forms are honest; pick one and
apply it consistently, or state the rule in [CI/CD §7.2](ci-cd.md).

**F5 — trailing newline (optional, pre-existing).**
`.github/workflows/release.yml` has no newline at end of file. The PR preserves that
state; it does not introduce it. Worth fixing while the file is open.

## 5. Conflicts with the in-flight workflow work

The CI-speed workstream is editing the same three files in the same regions. This is a
textual conflict, not a semantic one: the bump touches only `uses:` lines, and the
CI-speed change touches the jobs around them.

| File | Overlap | Effect |
|---|---|---|
| `ci.yml` | `detect` (checkout gains `fetch-depth: 0`), `docs` (checkout + setup-node), `go` (matrix/checkout/setup-go), `upload-artifact` step, `dco`, `actions-pinned`, `docker`, `e2e`, `external-links` | PR #46's hunks no longer apply cleanly: the CI work rewrote the surrounding context, moved line numbers and added a `gate` job |
| `security.yml` | `detect`, `gosec`, `scorecard` | Same: `run_heavy` gating, timeouts and job-level `concurrency` were inserted around the bumped `uses:` lines |
| `release-dry-run.yml` | `dry-run` job | Same: `timeout-minutes` and job-level `concurrency` inserted before the bumped steps |
| `release.yml` | none | Applies cleanly |
| `dependency-review.yml` | none | Applies cleanly |

**e2e job.** PR #46 bumps `actions/checkout` and `actions/setup-go` inside `e2e`; the
CI-speed work adds a `run_heavy` condition, a `timeout-minutes` and a job-level
`concurrency` group. There is no functional conflict: `setup-go@v7` keeps
`go-version: "1.27.x"` and `cache: true`, and `e2e` needs neither credentials nor
artifacts, so `checkout@v7`'s credential-file change is invisible to it. Expect one
conflict block and resolve it by taking both sides.

**security workflow.** PR #46 bumps `checkout`, `setup-go` and both
`codeql-action/upload-sarif` steps; the CI-speed work adds `run_heavy` gating,
timeouts, `concurrency`, and `fetch-depth: 0` on `detect`. No functional conflict: the
`gosec` SARIF upload is guarded by `if: always() && (...)`, which still runs when the
job runs, and `upload-sarif@v4` needs the same `security-events: write` the job already
grants. On a docs-only PR `gosec` is skipped and no SARIF is uploaded, which is the
intended behaviour of the classifier.

**Recommended integration order** (either is correct, the first is cheapest):

1. Land PR #46 first — it is `MERGEABLE` but `BEHIND`, so update the branch and merge;
   its checks were already green at `aa370c1`. Then rebase the CI-speed work and add the
   two pins in F6.
2. If the CI-speed work must land first, ask dependabot to rebase (`@dependabot rebase`)
   so it re-applies the 17 SHA replacements against the new file layout, then add F6 by
   hand.

**F6 — version skew introduced by the CI-speed work (needs a decision either way).**
That work adds `docker/setup-buildx-action@v3` and `docker/build-push-action@v6` to
`ci.yml` (currently lines 214 and 219) and to `release-dry-run.yml` (currently lines 66
and 68). PR #46 does not know about them, so after both land, `release.yml` would run
buildx v4 / build-push v7 while `ci.yml` and `release-dry-run.yml` stay on v3 / v6 — the
same action at two majors in one pipeline, sharing one `mage2mcp-docker` cache scope.
Bump those four lines to the PR's pins (`docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069`,
`docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc`) so all three
workflows share one major. The inputs those steps pass (`load`, `provenance`, `sbom`,
`cache-from`, `cache-to`) are all declared by `build-push@v7`.

## 6. Evidence

**Pins resolved against upstream tags** (17/17 match the trailing comment):

```text
OK actions/checkout v7.0.1 3d3c42e5…
OK actions/setup-node v7.1.0 949feb24…
OK actions/setup-go v7.0.0 b7ad1dad…
OK actions/upload-artifact v7.0.2 cf430e03…
OK actions/download-artifact v8.0.2 9000827c…
OK sigstore/cosign-installer v4.1.3 756fe2d2…
OK docker/setup-qemu-action v4.4.0 99012661…
OK docker/setup-buildx-action v4.4.1 f87e5991…
OK docker/metadata-action v6.2.0 dc802804…
OK docker/login-action v4.8.0 8aa5831f…
OK docker/build-push-action v7.4.0 c3c9e263…
OK actions/attest-build-provenance v4.2.2 4d101475…
OK softprops/action-gh-release v3.0.4 8a0cd651…
OK github/codeql-action v4.39.3 33d7b85d…
OK ossf/scorecard-action v2.5.0 fa7117db…
OK anchore/sbom-action v0.24.3 66cbf4bc…
OK actions/dependency-review-action v5.0.0 a1d282b3…
```

**Workflow content at the PR head** (`aa370c1`): all five files parse as YAML; the
`actions-pinned` regular expression (`uses:[[:space:]]+[^#]+@(v[0-9]|main|master)`)
finds no match, so the SHA-pinning gate stays green.

**PR #46's own runs at `aa370c1`** — all successful:

| Run | Workflow | Result | Pins exercised |
|---|---|---|---|
| 44484250167 | CI | success | `checkout@v7.0.1`, `setup-node@v7.1.0`, `setup-go@v7.0.0`, `upload-artifact@v7.0.2` |
| 44484250094 | Security | success | `checkout`, `setup-go`, `codeql-action/upload-sarif@v4.39.3` (`Upload SARIF` step green) |
| 44484250240 | Release Dry Run | success | `checkout`, `setup-go`, `sbom-action@v0.24.3`, `upload-artifact@v7.0.2` |
| 44484250107 | Dependency Review | success | `dependency-review-action@v5.0.0` |

Jobs observed green in the CI run: `detect`, `docs` (with `npm ci`), `dco`,
`actions-pinned`, `go (1.26.x)`, `go (1.27.x)`, `integration`, `docker`, `e2e`;
`external-links` and `scorecard` skipped by their triggers, as designed.

**Not exercised by any pull-request run** (release-only, see F3): `download-artifact@v8.0.2`,
`cosign-installer@v4.1.3`, `setup-qemu@v4.4.0`, `setup-buildx@v4.4.1`, `metadata@v6.2.0`,
`login@v4.8.0`, `build-push@v7.4.0`, `attest-build-provenance@v4.2.2`,
`action-gh-release@v3.0.4`.
