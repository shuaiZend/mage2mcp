# Testing strategy

**Status:** the sections below describe the **intended** strategy, and not all of it is
built. `docs/audit/regression-test-report.md` §2 is the current inventory and is the
authority on what actually runs; this document is the plan it is measured against.

Implemented today: unit tests beside the code, contract tests against `httptest`
fakes, an independent QA suite under `test/qa` (build tag `qa`, run by
`scripts/qa-independent.sh`, which fails on any skip), the Docker end-to-end smoke test
under `test/e2e`, a live functional harness under `test/live` (build tag `live`, driven
by `.github/workflows/live.yml`), and a conformance suite that replays payloads recorded
from a real store (`test/fixtures/magento`).

Not implemented: the sandbox suite (`tests/sandbox`), the manual real-environment
checklist (`tests/real`), a dedicated `tests/security` tree, load tests (`tests/load`),
fuzz targets, and per-module `contract`/`mocktest` packages. The `tests/` directory does
not exist. Those rows are marked **planned** below rather than left to read as current
evidence.
**Owner:** QA, with each module owner responsible for its own suite.
**Related:** [CI/CD](ci-cd.md), [Review process](review-process.md),
[Module boundaries](../module-boundaries.md), [Architecture](../architecture.md),
[Research baseline](research-baseline.md), [Threat model](../security/threat-model.md).

The purpose of this strategy is to make the evidence behind every claim explicit. A
green test suite is only meaningful when the reader knows which environment produced
it, what it asserted, and what it did not cover.

## 1. Testing pyramid

| Level | What belongs here | Location | Runs on | Network | Target |
|---|---|---|---|---|---|
| Unit | Pure functions, DTO mapping, error mapping tables, validation, cache-key building, time and currency conversion | `_test.go` beside the code | Every PR | None | Seconds |
| Contract | Behaviour every implementation of a public interface must satisfy | `_test.go` against `httptest` fakes **(no separate `contract`/`mocktest` packages)** | Every PR | None | Seconds |
| Integration, mock upstream | Client behavior against an `httptest` server: status codes, pagination, error envelopes, rate limits, malformed bodies, timeouts | `internal/<module>/*_test.go`, `test/fixtures/magento/` | Every PR | Loopback only | Under 60 s per module |
| Sandbox **(planned)** | The real vendor API against a dedicated non-production account | `tests/sandbox`, build tag `sandbox` | Nightly or manual | Yes | Minutes |
| Real environment **(planned)** | Storefront GraphQL verification, configurable rendering, MSI effect, WAF rule effect | `tests/real`, manual checklist | Before a release | Yes | Hours |
| Security | Auth, RBAC, confirmation, SSRF, injection, redaction, tenancy isolation | `_test.go` and `test/qa` **(no dedicated `tests/security` tree)** | PR subset, nightly full | None | Under 2 min subset |
| Race | Any test touching shared state, caches, sessions, or jobs | All packages | Every PR | None | 2x runtime |
| Fuzz **(planned)** | Config parsing, import payloads, strict JSON, identifiers, cursors | `*_fuzz_test.go` | Nightly, 10 min per target | None | 10 min |
| Load **(planned)** | Concurrency limits, queue depth, quota behavior, latency, memory | `tests/load`, build tag `load` | Weekly, mocks only | Loopback | 10–20 min |

Security, race, fuzz, and load are cross-cutting concerns, not layers: they apply to
several levels. A security test can be a unit test; a load test is always integration.

The pyramid is weighted toward the bottom. A test that can be a unit test is not
written as an integration test, and an integration test that can use a mock server is
not run against a sandbox. Sandbox and real-environment runs exist to catch what mocks
cannot: vendor behavior drift, real pagination, real quotas, and real rendering.

## 2. Mock server strategy

Every module ships a fake HTTP server built on `net/http/httptest`. The server is the
module's contract with its vendor, expressed as executable fixtures.

- **One fake per module.** `internal/<module>/mocktest` exposes a server that can be
  configured per scenario: success, pagination, `429` with `Retry-After`, `500`, `401`,
  malformed JSON, truncated body, HTML error page, duplicate keys, slow response.
- **Fixtures on disk.** `test/fixtures/magento/` holds response bodies recorded from a
  real store, replayed by `internal/magento/conformance_test.go`. They carry no
  `meta.json`: the replay serves each body as a 200 and the test asserts the parsed
  result, which is what those fixtures exist to pin. Fakes that need status codes and
  delays build them inline in the test.
- **Request assertions.** The fake asserts the method, path, query, and body shape, so
  a wrong URL or a missing store code fails the test instead of silently returning
  plausible data.
- **No account required.** `go test ./...` must pass with no network and no vendor
  credentials. A contributor without a Magento, Cloudflare, or GA4 account can run the
  entire default suite.
- **Fakes for services, mock servers for clients.** `internal/<module>/faketest`
  implements the public service interfaces for `unified`, `mcp`, and `jobs`. Mock
  servers are for testing the module's own HTTP client. `internal/unified` uses fakes
  only and never opens a socket.
- **Recording rules.** Record from a sandbox account, never from production, then
  scrub, then commit. A fixture with a real hostname, token, email, customer name, or
  account ID is a defect.
- **Scrubbing.** Replace tokens, cookies, emails, names, addresses, phone numbers,
  order IDs, IPs, zone IDs, property IDs, and account IDs with stable synthetic values.
  A script performs the replacement; a CI check greps fixtures for the patterns and
  fails the build.
- **Determinism.** Fixtures carry fixed timestamps and fixed cursors. Tests never call
  `time.Now()` in an assertion and never depend on map iteration order.

## 3. Environment matrix and evidence labeling

Every test claim carries exactly one label: **mock**, **sandbox**, or **real**.

| Label | What it means | Credentials | Who may produce it | Evidence |
|---|---|---|---|---|
| mock | `httptest` fake with committed fixtures | None | Anyone | Test output, CI run |
| sandbox | Real vendor API, dedicated non-production account | Sandbox secrets, never in PR CI | Maintainer or QA | Run log, redacted request IDs, date |
| real | A production-like environment the maintainer controls | Maintainer only | Maintainer | Checklist with date, environment, and what was not verified |

Rules:

1. Mock success is never reported as real-environment success. "Verified" with no label
   means mock, and the reviewer treats it as mock.
2. HTTP 200 is not functional success for a Magento configurable product. A 200 from
   `POST /V1/products`, `POST /V1/configurable-products/{sku}/options`, or
   `POST /V1/configurable-products/{sku}/child` proves only that the payload was
   accepted. Functional success requires three checks: `GET /V1/products/{sku}` shows
   the parent and its extension attributes, the child SKUs are attached with the
   expected options, and a **storefront GraphQL** query returns the configurable
   product with its variant options and prices. The same rule applies to MSI: a 200
   from `POST /V1/inventory/source-items` does not prove salable quantity; verify it
   through the stock API or the storefront.
3. A sandbox claim names the environment, the date, the account type, and what was not
   verified. A real claim additionally names the maintainer.
4. Evidence for sandbox and real runs is stored in `docs/evidence/` in redacted form:
   request shape, response shape, status, and hashes. Never a full upstream body.
5. A PR that changes vendor-facing behavior and has only mock evidence says so
   explicitly. The reviewer decides whether that is acceptable for the change.

The PR template already carries a verification table with Unit, Integration (mock),
Sandbox, and Real environment rows. An empty row means not verified, not "passed".

## 4. Coverage targets

| Scope | Target | Measurement | Phase |
|---|---|---|---|
| `internal/security` (auth, RBAC, confirmation, audit) | 90% statements | `go test -coverprofile` per package | 1 |
| `internal/core`, `internal/mcp` | 80% statements | Same, aggregated by a script | 1 |
| Overall repository | 70% statements | Total statements across packages | 1 warning, 6 blocking |
| Generated code and `faketest` packages | Excluded | An explicit, reviewed exclusion list | 1 |

Coverage is measured with `go test -race -covermode=atomic -coverprofile` and reported
per package in the PR. The CI job prints the summary to the step summary today and will
fail below threshold from Phase 6.

Coverage is not a quality metric on its own. It measures which lines executed, not
whether anything was asserted: a test can call a function, ignore the result, and raise
coverage. A fully covered authorization check can still be wrong for every adversarial
input. Security controls need adversarial cases, not line hits.

How this strategy avoids gaming:

- A test that only calls a function and asserts no error is a review finding.
- `t.Skip` never counts as coverage; skipping a security test blocks the release.
- Exclusions are a reviewed list, not an inline comment added by the author.
- Reviewers require both success and failure cases for each new branch.
- Coverage is reported per package, so a well-tested module cannot hide an untested one.
- A coverage drop in a touched package is a Medium finding even when the total rises.

## 5. Per-module test plans

### 5.1 Magento

| Scenario | Level | Assertion |
|---|---|---|
| Product create, read, update, delete | Mock and sandbox | DTO round-trip; a missing SKU maps to `not_found`; a repeated delete is idempotent or returns `not_found` |
| Configurable lifecycle: create parent, set options, add children, verify | Mock and real | Variant count and option labels match; storefront GraphQL returns options with prices; removing a child updates the option set |
| EAV: attribute sets, attributes, options | Mock | Types map correctly; select and multiselect options are preserved; an unknown type maps to `unknown` |
| MSI: sources, stocks, source items, bulk assign, unassign, transfer | Mock and sandbox | Salable quantity reflects the assignment; a partial bulk failure returns per-item results |
| Media: list, upload, delete | Mock | Content-type allowlist, size cap, and stable entry ID |
| Import: validate, start, status, cancel, partial failure | Mock and sandbox | `validation-skip-errors` returns per-row results; `allowedErrorCount` is honored; a resumed import does not duplicate rows |
| Pagination and cursors | Mock | Cursors are opaque, signed, and instance-bound; a tampered cursor is rejected |
| Error mapping | Mock | `400`, `401`, `403`, `404`, `409`, `429`, and `5xx` map to the documented codes |

### 5.2 Cloudflare

| Scenario | Level | Assertion |
|---|---|---|
| GraphQL template rendering | Unit | The query matches its fixture; no user input is concatenated; dataset and fields come from an allowlist |
| Sampling metadata | Mock | Sampling and row-count metadata appear in the result quality; unsampled data is never reported as complete |
| Security events (`firewallEventsAdaptive`) | Mock | Pagination works; the 31-day boundary is handled; the sampling flag is surfaced |
| Quota and budget | Mock | `429` with `Retry-After` is respected; the budget counter decrements; the 300-per-5-minute limit is enforced |
| Read-only assertion | Unit and review | No create, update, or delete method exists on the client; a test enumerates methods and fails on a mutating verb |
| Logpush metadata | Mock | Job list and get only; dataset list; no job mutation path |

### 5.3 GA4

| Scenario | Level | Assertion |
|---|---|---|
| Compatibility validator | Unit | Incompatible dimension and metric pairs are rejected before the API call with a documented reason; valid pairs pass |
| Quota handling | Mock | Quota errors map to `rate_limited` with retry guidance; concurrency is capped at 4 per property; retries are bounded |
| Ecommerce capability probe | Sandbox or real | A property with zero purchases is reported as "no ecommerce data", not as zero revenue |
| Metadata | Mock | Dimensions and metrics are cached; an unknown field is rejected |
| Result metadata | Unit | `TimeZone`, `Currency`, `Freshness`, and `Quality` are always present; absence is a contract violation |
| Time zone and currency | Unit | The property time zone applies to date ranges; money stays a decimal string |

### 5.4 MCP

| Scenario | Level | Assertion |
|---|---|---|
| Protocol negotiation | Integration | `2026-07-28` is accepted; down-negotiation to `2025-11-25` works; an unsupported version is rejected |
| Tool schema validation | Unit | Every tool has an input schema with `additionalProperties: false`, required fields, a timeout, a permission, and accurate annotations |
| Authentication failures | Integration | Missing, expired, wrong-audience, and forged JWTs return 401 before MCP routing |
| Session binding | Integration | A session ID presented with another subject's token is rejected and invalidated |
| Registry behavior | Unit | A disabled module removes its tools and emits `list_changed`; an unmapped tool is denied |
| Strict JSON | Unit and fuzz | Duplicate keys, prototype keys, deep nesting, and oversized bodies are rejected with stable codes |

### 5.5 Unified

| Scenario | Level | Assertion |
|---|---|---|
| Join correctness | Unit with fakes | Sessions and orders join on date and instance with no fan-out duplication; a missing side is reported as partial |
| Time zone and currency | Unit | The property time zone converts to UTC boundaries; a currency mismatch is rejected, never silently converted |
| SKU mapping | Unit | An unmapped SKU is reported, not dropped; mappings are instance-scoped |
| Quality flags | Unit | Every result carries sources, formula, freshness, and quality |
| No network | Unit | `internal/unified` tests use fakes only and open no socket |

## 6. Security test plan

| Family | Cases | Assertion | Phase |
|---|---|---|---|
| JWT forgery | `alg: none`, HS256 with the public key, wrong `kid`, `kid` as a URL, wrong `iss` or `aud`, expired by 61 s, `nbf` +10 min, 9 KiB token, duplicate headers | 401 with the expected reason; no outbound JWKS fetch for a `kid` injection | 1 |
| SSRF | 50-URL corpus: metadata IP, decimal and octal forms, IPv6-mapped, `nip.io`, redirect chain, `file://`, DNS rebinding harness | Zero connections to the listener; only an allowlisted host is reached | 2 |
| Injection | 60 malformed JSON payloads, a 40-case prompt-injection corpus, CSV formula injection | Stable error codes; no write without confirmation; no outbound request | 1–2 |
| Oversized input | 200 MB body, 100 000 CSV columns, deep nesting, 1 000-element array | Rejected at 413 or by schema before allocation | 1 |
| Replay | Confirmation nonce reuse, session replay, token replay within its lifetime | The second use is rejected and audited | 1 |
| Confirmation tampering | Modified parameter hash, a nonce from another subject, an expired nonce | `confirmation_required`; nothing executes | 1 |
| Cross-instance access | Every tool called with a foreign instance and foreign IDs | 403 or 404 with no foreign data in body or headers | 1 |
| Race | 200 concurrent streams with distinct subjects, concurrent cache writes, concurrent job transitions | `-race` clean; no interleaved chunks; no cross-instance value | 1–2 |
| Fuzzing | Config parser, strict JSON, import CSV and JSON, SKU and identifier parser, cursor decoder | No panic, bounded memory, stable error codes | 6 |

A failing security test is never quarantined silently. Quarantine requires an issue and
a reviewed PR; for auth, RBAC, confirmation, or audit it also blocks the release.

## 7. Performance and load testing

| Metric | Budget | How it is measured |
|---|---|---|
| Tool latency | p95 under 500 ms for a cached read, under 2 s for a single upstream read, 30 s hard timeout | Load harness against a mock upstream |
| Throughput | 50 requests per second sustained at 64 in-flight | Mock upstream, 10-minute run |
| Job latency | p95 start under 5 s; queue depth bounded | Worker load test |
| Memory | Under 256 MiB RSS at 64 concurrent sessions | 30-minute soak |
| Goroutines and file descriptors | Bounded under 500 slow connections and 50 idle streams | Soak with slowloris clients |
| Upstream calls per tool | 1 plus pagination; never N+1 | Instrumented mock server |

Agent concurrency is simulated with N virtual agents, each with a distinct JWT and
instance grant, running a scripted tool sequence (list, read, report) with 100–500 ms
of think time and a 10 percent error injection rate. The harness is a Go program in
`tests/load` using the MCP client, not a browser. It records per-instance quota
consumption so a noisy agent is visible.

Never load-tested: production Magento, a production Cloudflare zone, a production GA4
property, or any vendor API without written approval. Sandbox load is capped at 10
percent of the documented quota and must be announced to the account owner. The default
load target is the mock, and a load test that requires a vendor is a manual, scheduled
activity.

## 8. CI integration

| Suite | PR | Nightly | Weekly | Command |
|---|---|---|---|---|
| Unit, contract, mock integration | Yes | Yes | Yes | `go test ./...` |
| Race detector | Yes | Yes | Yes | `go test -race ./...` |
| Security fast subset | Yes | Yes | Yes | `go test ./tests/security/... -short` |
| Security full | No | Yes | Yes | `go test ./tests/security/...` |
| Fuzz | No | Yes, 10 min per target | Yes | `go test -fuzz=FuzzX -fuzztime=10m ./internal/...` |
| Load | No | No | Yes, mocks only | `go test ./tests/load/... -tags=load` |
| Sandbox | No | Opt-in | Manual | `go test ./tests/sandbox/... -tags=sandbox` |
| Docs and internal links | Yes | Yes | Yes | `npm run lint:md`, `npm run lint:links` |

Local commands are the same as CI, which is the point: a contributor reproduces a
failure without guessing. Sandbox runs need `MAGE2MCP_SANDBOX=1` plus credentials and
are never part of the default suite.

Skipping policy: a test is excluded only by a build tag that removes it from the
default run, or by a `t.Skip` that names a tracking issue. Both need reviewer approval.
A tagged suite that is invoked without its required environment fails loudly; it must
not silently skip, because a silent skip is indistinguishable from a pass. Skipping a
security test blocks the release.

### 8.1 Documentation-only fast path

`.github/scripts/classify-changes.sh` classifies every run before the heavy jobs start
and exports `go` and `run_heavy`. A pull request or push whose changed paths are all on
the documentation allowlist (Markdown, `docs/`, the issue and pull-request templates,
and the documentation lint and link-check configs) runs only `docs`, `dco`,
`actions-pinned`, and `gitleaks`. The Go matrix, storage integration, Docker smoke
test, e2e, `govulncheck`, and `gosec` jobs report `skipped` for that reason.

The filter is fail-safe by construction: scheduled and manually dispatched runs, a push
whose before-commit is unknown (force push, new branch), and any path outside the
allowlist all run the full pipeline. A skipped job is never treated as a pass — the
`ci-gate` job fails the run when any job that did run failed or was cancelled.

Caching is part of the budget, not an optimisation detail: `actions/setup-go` restores
the module and build caches keyed by `go.sum`, `golangci-lint` runs once (on the primary
toolchain) instead of once per matrix entry, and the `docker` job and the release dry
run share a single GitHub Actions layer cache scope (`mage2mcp-docker`), so the
module-download layer is built once per dependency change rather than once per
workflow.

## 9. Test data management

- **Synthetic only.** No production data, ever, including "just one order to debug".
- **Generated with fixed seeds.** Committed fixtures are the record of what was used.
- **Reserved identifiers.** SKUs such as `SKU-TEST-0001`, emails at `example.invalid`,
  IPs from the RFC 5737 ranges (`192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24`),
  and fixed UUIDs.
- **Scrub before commit.** A script replaces tokens, emails, names, addresses, and
  account identifiers; a CI check fails the build when a pattern survives.
- **Cleanup.** Sandbox tests create resources with a unique run prefix and delete them
  in `t.Cleanup`. A janitor job lists leftovers nightly. Never delete data you did not
  create.
- **Determinism.** Inject the clock through a `Now func() time.Time` or an equivalent
  interface and freeze it to a fixed instant. No sleeps, no reliance on wall time, no
  unordered comparison.
- **Fixture immutability.** A recorded fixture changes only with a stated reason in the
  PR, because a silent fixture change hides a behavior change.

## 10. Release acceptance criteria

| Criterion | Evidence |
|---|---|
| Required checks green on the tagged commit | CI run URL |
| Unit, contract, and mock integration pass on both Go versions | CI |
| Race detector clean | CI |
| Full security suite green | Nightly run |
| govulncheck and gosec clean | CI |
| License audit and SBOM present | Release artifacts |
| Coverage targets met | Coverage report |
| Real-environment verification for the release scope | Checklist with date, environment, and maintainer |
| No open Critical or High finding | Issue tracker |
| Load test within budget for a change to a hot path | Load report |
| Docs updated and links green | CI |
| Review record for every bypass merge in the release | `docs/reviews/` |

A patch release may carry mock-only evidence for a vendor-facing change only when the
change fixes behavior already verified in a real environment, and the notes say which
behaviors were not re-verified.

## 11. Open questions and decision record

| Question | Owner | Resolution point |
|---|---|---|
| Ownership and rotation of sandbox accounts | QA | Phase 1 |
| Testcontainers for PostgreSQL and Redis, or Compose | QA | Phase 1 |
| Magento compatibility matrix across 2.4.7, 2.4.8, and 2.4.9 | Magento owner | Phase 2 |
| Publish coverage and load reports as a public dashboard | Platform | Phase 6 |

| Decision | Record |
|---|---|
| Mock-first testing with fakes for cross-module work | [Module boundaries](../module-boundaries.md), Section 8 |
| Evidence labels and the no-mock-as-real rule | [Agent workflow](agent-workflow.md), Section 8 |
| Security test corpus and severity model | [Threat model](../security/threat-model.md) |
| Which suites block a merge | [CI/CD](ci-cd.md), Section 4 |

## Tool contract tests

`internal/magento/tool_contract_test.go` pins the public Magento tool names in
`testdata/tool-names.txt` and checks that `docs/implemented-tools.md` lists
exactly the registered tools. Renaming a tool now requires updating the golden
file and the changelog in the same change, which makes an accidental breaking
rename fail CI.

## End-to-end smoke test

`make e2e` (`test/e2e/run.sh`) starts PostgreSQL from Compose plus a fake Magento
REST API, then runs the server and worker on loopback with writes enabled. It
exercises `tools/list`, `magento.product.create` through the two-step
confirmation (approved by a different identity), `magento.product.seo`,
`magento.import.validate`, and an asynchronous `magento.import.start` executed by
the worker, asserting `created: 1` in the job report. The CI `e2e` job runs the
same script; it is not a required gate because it depends on Docker.
