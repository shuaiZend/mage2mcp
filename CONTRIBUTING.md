# Contributing to mage2mcp

Thank you for considering a contribution. mage2mcp is a standalone MCP gateway for
Magento 2, Cloudflare, and Google Analytics 4, and it is currently in Phase 0 (design).
This document explains how the project is developed, what is expected from a
contribution, and which rules apply before and after 1.0.

By participating, you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md).

## Project status: what we accept now

Phase 0 is for design, not code. During this phase we welcome:

- Review comments on design documents in [`docs/`](docs/).
- Threat-model feedback and abuse-case analysis.
- Corrections to verified facts, endpoint mappings, and compatibility claims.
- Documentation fixes, including the Chinese README.

Feature pull requests are not accepted until the Phase 0 design is approved. The
development, testing, and review rules below are the contract for code contributions
from Phase 1 onward; they are documented now so that the expectations are public before
the first line of Go is merged.

## Licensing and sign-off

The project is licensed under the Apache License 2.0. By contributing, you agree that
your contribution is licensed under the same terms.

Every commit must carry a Developer Certificate of Origin (DCO) sign-off:

```text
Signed-off-by: Your Name <you@example.com>
```

Use `git commit -s` to add it automatically. The sign-off certifies that you have the
right to submit the contribution under the project license. The full text is at
<https://developercertificate.org/>.

There is no Contributor License Agreement (CLA). Copyright remains with the
contributors; the project is not assigned to a company.

Commits without a valid sign-off cannot be merged. If you forgot a sign-off, amend the
commit with
`git commit --amend -s` (on an unpublished branch) or add a follow-up commit that is
itself signed off.

## Development environment

### Required tooling

| Tool | Version | Purpose |
|---|---|---|
| Go | 1.26 or newer | Build and test the gateway |
| Docker with Compose | Current release (tested with 29.8 / Compose v5.5) | PostgreSQL, Redis, and local runs |
| PostgreSQL | 16 or newer | Configuration metadata, audit log, job state |
| Redis | 7 or newer | Asynq queues |
| Node.js | 22 or newer | Documentation linting only (CI uses 22) |
| git | Current release (tested with 2.39) | Version control |

R2 and ClickHouse are optional. They are only needed for request-level Cloudflare log
analysis in Phase 3 and are not required for local development.

The official MCP Go SDK v1.8.0 declares `go 1.25.0`, and the planned direct
dependencies require Go 1.26, so **Go 1.26 is the hard minimum** (see
[ADR-0013](docs/adr/0013-go-toolchain-floor.md)). Go 1.27.x is the current toolchain;
Run `scripts/local-ci.sh` before pushing: it executes gofmt, vet, race tests,
golangci-lint, documentation checks, and actionlint/shellcheck when installed.
GitHub CI is manual and release-time only, so the local run is the contract.

CI (when dispatched) tests Go 1.27.x. An older local Go installation can still build the project when toolchain
auto-download is enabled:

```bash
# Check the active toolchain.
go version

# Auto-download is the default since Go 1.21; re-enable it if it was disabled.
go env -w GOTOOLCHAIN=auto
```

With `GOTOOLCHAIN=auto`, a local Go 1.21.4 installation downloads and runs the toolchain
required by `go.mod`. That works, but installing Go 1.27.x directly is faster and avoids
repeated downloads. Do not commit `go.work` files or local toolchain overrides.

### Network and module proxy

`proxy.golang.org` is not reachable from every network. If module downloads fail with a
timeout, set a regional proxy for your machine only:

```bash
go env -w GOPROXY=https://goproxy.cn,direct
```

CI uses the default proxy configuration. Do not add proxy settings to the repository,
to `go.env` committed files, or to CI configuration; they are machine-local preferences.

### Local services

Phase 1 will add a Compose file for PostgreSQL, Redis, and the gateway, together with
Makefile targets for the common tasks. Until that lands, run the dependencies you need
yourself and point the service at them with environment variables. The configuration
keys and defaults are defined in
[`docs/architecture.md`](docs/architecture.md) and will be documented in an operator
guide before the first release.

Never commit `.env` files, connection strings with passwords, API tokens, service
account JSON, or TLS private keys. The `.gitignore` already excludes the common cases,
but the rule is yours to follow.

### Documentation tooling

Node.js is needed only for documentation linting and link checking. There is no
JavaScript runtime dependency in the gateway. The documentation checks are live in CI
(`docs` job): `npm ci`, `npm run lint:md`, and `npm run lint:links`. Run the same
commands locally before opening a pull request.

## Branches and commits

### Branches

Never push directly to `main`. Every change goes through a branch and a pull request.

Use one of these prefixes, followed by a short lowercase slug:

| Prefix | Use |
|---|---|
| `feat/` | New capability or tool |
| `fix/` | Bug fix |
| `docs/` | Documentation only |
| `test/` | Test-only change |
| `refactor/` | Internal restructuring without behavior change |
| `chore/` | Build, CI, dependency, or tooling change |

Examples: `feat/magento-configurable-products`, `fix/confirmation-hash-mismatch`,
`docs/cloudflare-logpush-limits`, `test/ga4-quota-backoff`.

Keep branches short-lived. Rebase is not used on published branches: bring your branch
up to date by merging `main` into it.

### Commits

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
<type>(<scope>): <description>

<body>

Signed-off-by: Your Name <you@example.com>
```

Allowed types: `feat`, `fix`, `docs`, `test`, `refactor`, `perf`, `build`, `ci`,
`chore`, `revert`. Use a scope that names the area, for example `magento`, `cloudflare`,
`ga4`, `unified`, `auth`, `rbac`, `config`, `audit`, or `docs`.

Rules:

- The description is imperative, lowercase, and has no trailing period.
- The body explains *why*, not *what*; the diff already shows the what.
- One logical change per commit. Do not mix refactoring with behavior changes.
- Reference issues and ADRs in the body, for example `Refs: #42` or `ADR: docs/adr/0007-...`.
- Breaking changes use `!` after the type or scope and a `BREAKING CHANGE:` footer.

### Force pushes

Do not force-push. This includes `--force-with-lease`. Published history is shared
history: reviewers may have based work on your branch, and CI results are tied to the
commits it tested. If review requires a correction, add a commit. Maintainers squash
the pull request on merge, so a tidy final history does not require rewriting the branch.

## Module ownership

Ownership is recorded in [CODEOWNERS](CODEOWNERS) and in the table below; keep both in
sync. The project currently has a single maintainer, so the same person may hold several
roles; the review rules still apply and become meaningful as the team grows.

| Area | Path | Owner | Required review |
|---|---|---|---|
| Core contracts, models, config schema | `internal/core/` | Architect | Maintainer |
| Authentication, RBAC, audit, secrets | `internal/security/` | Security owner | Architect + Security |
| MCP server, registry, transport, tools | `internal/mcp/` | Architect | Maintainer |
| Magento 2 module | `internal/magento/` | Magento owner | Magento owner |
| Cloudflare module | `internal/cloudflare/` | Cloudflare owner | Cloudflare owner |
| GA4 module | `internal/ga4/` | GA4 owner | GA4 owner |
| Unified analytics | `internal/unified/` | Unified owner | Architect + Unified owner |
| Jobs and queues | `internal/jobs/` | Architect | Maintainer |
| Storage and migrations | `internal/storage/` | Architect | Maintainer |
| Observability | `internal/observability/` | Architect | Maintainer |
| Process wiring | `cmd/` | Architect | Maintainer |
| Cross-cutting tests | `tests/` | QA | QA |
| Documentation | `docs/`, `README.md`, `README.zh-CN.md` | Docs owner | Docs owner |

The following are **core interfaces** and require an ADR plus Architect review before
implementation:

- The data source contract and capability declaration.
- The tool registration API, risk classes, and confirmation model.
- The authentication and authorization model.
- The audit record schema.
- The configuration schema and instance model.
- The database schema for configuration, audit, and job state.
- Metric semantics in unified analytics.
- Public tool names and input/output schemas.

Changes to a public tool name or schema are interface changes even after 1.0. Before
1.0 they require an ADR; after 1.0 they require an ADR and a deprecation path.

## Testing expectations

Tests are part of the change, not a follow-up. A pull request that adds behavior without
tests will not be merged unless the maintainer explicitly agrees that the behavior is
not testable in isolation.

### What to test

- **Unit tests.** Table-driven, deterministic, no network, no real credentials, no
  sleeps. Inject clocks and rate limiters instead of depending on wall time.
- **Integration tests with mock upstreams.** Use `httptest` servers that reproduce the
  upstream API's status codes, pagination, error envelopes, and rate-limit responses.
  Fixtures must be recorded or hand-written, scrubbed of tokens, customer data, and
  account identifiers, and committed with the test.
- **Contract tests.** Where an upstream schema is central to a tool, add a test that
  fails when the expected response shape changes, so upstream drift is visible.
- **Race detector.** All tests must pass under `go test -race ./...`. Do not skip the
  race detector for code that touches shared state, caches, or background jobs.
- **Security tests.** At minimum: missing, expired, malformed, and wrong-audience JWTs;
  RBAC denial for each risk class; confirmation hash mismatch and replay; input
  validation at schema boundaries; secret redaction in logs and tool output; rejection of
  caller-supplied upstream hosts or URLs; and correct behavior on `429` and `5xx`
  responses.
- **Failure injection.** Timeouts, partial pages, malformed JSON, and upstream errors
  must produce a typed error, not a panic and not a silent empty result.
- **Fuzz tests.** Add them for configuration parsing, import payload handling, and any
  parser that consumes untrusted input.

Coverage is reported but not gamed. Critical paths — authentication, authorization,
confirmation, audit, and write execution — require direct tests rather than incidental
coverage.

### Quality gates

Every pull request must pass the gates below. The docs and DCO jobs run in CI today; the
Go jobs activate as soon as `go.mod` exists. Changelog and documentation review are checked
by reviewers.

| Gate | Check | Blocking |
|---|---|---|
| Formatting | `gofmt -l .` produces no output | Yes |
| Vet | `go vet ./...` | Yes |
| Build | `go build ./...` | Yes |
| Unit and integration tests | `go test -race ./...` on Go 1.26.x and 1.27.x | Yes |
| Security tests | Auth, RBAC, confirmation, redaction suites | Yes |
| Secret scan | gitleaks over the full history | Yes |
| Dependency review | No High severity advisories and no denied licenses | Yes |
| Vulnerability scan | `govulncheck ./...` when the Go module exists | Yes |
| Static analysis | gosec with no High severity findings | Yes |
| Documentation | Behavior changes update the relevant docs | Yes |
| Documentation lint | Markdown lint and internal link check | Yes |
| Changelog | User-visible change has a changelog entry | Yes |
| DCO | Every commit has a valid sign-off | Yes |
| ADR | Required for core interface changes | Yes |

Live upstream tests are optional, run only when the required environment variables are
present, and must never be required for a normal `go test ./...` run. They must use
dedicated test accounts, not production credentials.

## Pull request process

1. **Open an issue or discussion first** for anything larger than a typo or a small
   documentation fix. Agree on the approach before writing code.
2. **Open the pull request as a draft** while work is in progress. Fill in the pull
   request template: summary, type of change, affected modules, checklist, and a
   verification table that states whether each level was tested with a mock, a sandbox,
   or a real environment.
3. **Keep the change focused.** Split refactors from behavior changes and split unrelated
   fixes into separate pull requests. Small pull requests are reviewed faster.
4. **Disclose substantial AI-generated contributions** in the description. You are
   responsible for every line you submit, including its license provenance and tests.
5. **Request review** from the module owner listed above. If you are unsure, request a
   maintainer review and they will route it.
6. **No self-approval.** An author cannot approve their own pull request, even when they
   are a maintainer. A second maintainer must approve; while the project has only one
   maintainer, an independent reviewer from outside the author's work must approve
   instead.
7. **Resolve all conversations.** Unresolved review threads block merge.
8. **Critical and High security findings block merge.** A finding from review, a
   security test, or a scanner must be fixed, or explicitly accepted by the maintainer
   with a written rationale and a tracking issue. Medium and Low findings need a
   follow-up issue at minimum.
9. **Maintainers merge.** Do not merge your own pull request. Squash merge is the
   default, and the DCO sign-off is preserved in the squashed commit message.

Review turnaround is best effort; see [SUPPORT.md](SUPPORT.md). A pull request with no
activity for 60 days may be closed with a note, and can be reopened when work resumes.

## Documentation requirements

- **Every behavior change updates documentation in the same pull request.** A new tool,
  a changed input schema, a new configuration key, or a changed default is a behavior
  change.
- **New architecture decisions require an ADR** in [`docs/adr/`](docs/adr/). Use the
  project template: context, decision, consequences, alternatives considered, status.
  ADRs are numbered and immutable once accepted; supersede rather than rewrite.
- **Keep the two READMEs in sync.** `README.md` is the source of truth and
  `README.zh-CN.md` is a full translation, not a summary. A change to one requires the
  corresponding change to the other.
- **Follow the documentation conventions** in
  [`docs/development/research-baseline.md`](docs/development/research-baseline.md):
  tables for matrices, mermaid diagrams readable at 100 columns, inline links to
  official vendor documentation, and no invented endpoint paths, field names, or quota
  numbers.
- **Mark unverified claims.** If a behavior is not verified against a live system or
  released source, mark it as unverified and state what would verify it. Do not describe
  planned behavior as working behavior.
- **Link to documents by relative path** so links work on GitHub and in the repository.
  Documentation lives in `docs/`, not in the repository root, except for the community
  files listed in the README.

## Proposing a new data source module

A data source module is a Go package that exposes one upstream system through the shared
contract. Adding one is a design change, not a plugin drop-in. The sequence is:

1. **Open a design issue.** Describe the upstream system, its official API documentation,
   the authentication model, rate limits, pagination, data sensitivity, and the tools you
   intend to expose. State explicitly what will *not* be supported.
2. **Get an ADR if the contract changes.** If the module needs a new capability kind, a
   new risk class, or a new auth mechanism, write an ADR before implementation.
3. **Create the module** in `internal/<name>` with its client, services, DTOs, errors,
   configuration, tests, and module README. Implement `core/datasource.DataSource` and
   declare capabilities. The module must not import another module's internals; the
   wiring belongs in `cmd/server`.
4. **Declare capabilities.** State read versus write, resource types, required upstream
   scopes, risk classes, rate limits, pagination behavior, and retention assumptions.
   Capabilities that are not declared must not be reachable.
5. **Register tools explicitly.** Every tool has a namespaced name, a JSON Schema input
   with `additionalProperties: false`, an output schema, a permission, a timeout, and a
   risk class. A generic passthrough tool is not acceptable. High-risk tools must
   support confirmation bound to a parameter hash.
6. **Add configuration.** Define the instance configuration schema, the secret
   references, and validation errors that name the offending field without echoing the
   secret.
7. **Test it.** Unit tests, mock-upstream integration tests, failure-mode tests, and the
   security tests listed above. Add a fake implementation and contract tests so other
   modules can be tested without this source. Include at least one test proving that a
   caller cannot redirect the module to a different host or account.
8. **Document it.** Add `docs/integrations/<name>.md` with a verification log, update
   [`docs/mcp-tools.md`](docs/mcp-tools.md), the threat model, the compatibility matrix,
   and both READMEs. If the module introduces a new trust boundary, update
   `docs/security/threat-model.md` and request a security review. Register the module in
   [`docs/index.md`](docs/index.md), and add its path to [CODEOWNERS](CODEOWNERS) and the
   table above.

Modules that wrap an upstream with a private, undocumented, or unstable API will not be
accepted. The same applies to modules that require a plugin inside the upstream system or
direct database access.

## Security

Never commit credentials, tokens, customer data, or full upstream response bodies. Use
environment variables or a secret manager, and redact secrets in logs, errors, and test
fixtures. If you accidentally commit a secret, rotate it immediately and tell the
maintainer; deleting the commit is not sufficient because the value may already be
cached or cloned.

Do not report vulnerabilities in public issues or pull requests. Follow
[SECURITY.md](SECURITY.md) and use GitHub Security Advisories for private reporting.

## Questions

- Usage and design questions: GitHub Discussions.
- Bugs and feature proposals: GitHub Issues.
- Vulnerabilities: GitHub Security Advisories, never a public issue.
- Community expectations: [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
- Support scope: [SUPPORT.md](SUPPORT.md).
