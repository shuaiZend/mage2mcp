# Development plan

**Status:** Phase 0 baseline. Phase 1 starts only after maintainer approval of
this plan and the Phase 0 report.

## 1. Principles

- **Design before code.** Shared contracts, data model, and security boundaries
  are frozen before modules are implemented.
- **Vertical slices, not horizontal layers.** Each phase delivers tools an agent
  can actually call, with tests and documentation.
- **No phase is "done" without evidence.** Mock tests, sandbox tests, and real
  environment tests are reported separately and never conflated.
- **Security work is continuous.** Threat model, adversarial tests, and supply
  chain checks are part of each phase, not a final gate.
- **Public by default.** Every artifact is written as if a stranger will review
  it tomorrow, because they will.

## 2. Phase overview

| Phase | Objective | Primary deliverables | Exit criteria |
|---|---|---|---|
| 0 | Design baseline | Architecture, boundaries, API mappings, threat model, ADRs, governance, CI | Maintainer approval of the Phase 0 report; docs CI green |
| 1 | Foundation | Go module, config, logging, storage, Redis, MCP server skeleton, Cloudflare Access JWT, RBAC, audit, Docker, health, metrics | An authenticated agent can list tools; a read-only stub tool works end to end; unit + integration tests; CI gates active |
| 2 | Magento | REST/GraphQL clients, EAV discovery, product services, configurable lifecycle, MSI, media, categories, orders, async import, artifact store | Configurable product created and verified through storefront GraphQL on a real 2.4.9 instance; an import resumes from row N after a forced failure **without re-uploading the payload**, with the artifact store configured |
| 3 | Cloudflare | GraphQL analytics templates, security events, Logpush metadata, optional R2/ClickHouse ingestion | Traffic and security tools return correct, quality-annotated data on a real zone; read-only guarantee tested |
| 4 | GA4 | Data API client, compatibility validator, capability probe, ecommerce, realtime | Reports work on a real property; incompatible combinations rejected with actionable errors; quota handling tested |
| 5 | Unified analytics | Cross-source models, joins, SKU mapping, eight required scenarios | Each scenario returns a result with explicit sources, formulas, and quality flags; semantics tests pass |
| 6 | Hardening | Full test matrix, race, fuzz, load, security tests, dependency and license audit, SBOM, container scan, external review | Zero open Critical/High; coverage targets met; load test within budget |
| 7 | Release | README, docs, release workflow, images, SBOM, checksums, version policy | v0.1.0 tagged, signed, published, installable from the documented quickstart |

## 3. Phase 0 traceability

Every research item required by the project brief maps to a committed document.

| # | Research item | Document |
|---|---|---|
| 1 | Git repository check | [Phase 0 report](reviews/phase-0-report.md) |
| 2 | Go MCP SDK research | [ADR-0002](adr/0002-mcp-transport.md), [mcp-tools](mcp-tools.md) |
| 3 | Magento 2.4.9 API research | [integrations/magento](integrations/magento.md), [ADR-0003](adr/0003-magento-client-design.md) |
| 4 | Configurable product API research | [integrations/magento](integrations/magento.md) |
| 5 | Cloudflare API research | [integrations/cloudflare](integrations/cloudflare.md), [ADR-0004](adr/0004-cloudflare-log-storage.md) |
| 6 | Cloudflare Pro capability analysis | [integrations/cloudflare](integrations/cloudflare.md) |
| 7 | Logpush availability analysis | [integrations/cloudflare](integrations/cloudflare.md), [ADR-0004](adr/0004-cloudflare-log-storage.md) |
| 8 | GA4 API research | [integrations/ga4](integrations/ga4.md), [ADR-0005](adr/0005-ga4-integration.md) |
| 9 | Module architecture | [architecture](architecture.md), [module-boundaries](module-boundaries.md), [ADR-0001](adr/0001-modular-architecture.md) |
| 10 | Database design | [database-design](database-design.md), [ADR-0009](adr/0009-data-stores.md) |
| 11 | Security threat model | [threat-model](security/threat-model.md), [security-review](security/security-review.md), [ADR-0006](adr/0006-authentication.md) |
| 12 | License evaluation | [license-policy](development/license-policy.md), [ADR-0008](adr/0008-license.md) |
| 13 | Multi-agent collaboration | [agent-workflow](development/agent-workflow.md), [ADR-0007](adr/0007-multi-agent-development.md) |
| 14 | CI/CD and review | [ci-cd](development/ci-cd.md), [review-process](development/review-process.md) |
| 15 | Storage and extension | [database-design](database-design.md), [ADR-0004](adr/0004-cloudflare-log-storage.md), [ADR-0009](adr/0009-data-stores.md) |

## 4. Phase 1 work breakdown

Phase 1 is the first code phase and the one that determines whether the rest can
be parallelized safely.

| Workstream | Tasks | Owner |
|---|---|---|
| Module and toolchain | `go.mod` (go 1.26.0), directory skeleton, Makefile, `.golangci.yml`, tool version pins | Architect |
| Core | Config schema and validation, error package, neutral models, capability model, `DataSource` contract | Architect |
| Storage | PostgreSQL schema, migrations, repositories, Redis adapters, Asynq wiring | Architect |
| Security | JWT verifier with JWKS cache, RBAC policy engine, audit writer, confirmation service, secret resolution | Security |
| MCP | Server construction, Streamable HTTP handler, registry, request pipeline, health and metrics endpoints | MCP |
| Observability | `slog` setup with redaction, Prometheus metrics, health checks | Architect |
| Deployment | Dockerfile (multi-stage, distroless), Compose file, `.env.example`, graceful shutdown | Platform |
| Import artifacts | Object-store adapter for payloads, 24 h retention, resume and expiry semantics (Q8 resolved) | Magento + Architect |
| Testing | Test harness, fake data source, contract test skeleton, CI activation (go jobs) | QA |
| Documentation | Module README template, configuration reference, quickstart draft | Docs |

Phase 1 exit criteria, in measurable terms:

1. `go build ./...`, `go vet ./...`, `golangci-lint run`, `go test -race ./...`
   pass on both supported Go versions.
2. An agent authenticated with a valid Cloudflare Access JWT can initialize a
   session and list tools; an invalid token is rejected with 401.
3. A read-only stub tool for each module returns typed data from a fake
   implementation, with audit rows written.
4. A disabled module does not prevent startup; `/readyz` reports degraded.
5. Docker image builds for amd64 and arm64, runs as non-root, and exposes health
   and metrics on the documented ports.
6. CI enforces docs, build, vet, lint, test, race, govulncheck, gosec, and
   dependency review on every PR.

## 5. Phase 2–5 sequencing

```text
Phase 1 (foundation)
   ├── Phase 2 (Magento) ──┐
   ├── Phase 3 (Cloudflare)├── Phase 5 (Unified) ── Phase 6 ── Phase 7
   └── Phase 4 (GA4) ──────┘
```

Phases 2, 3, and 4 are independent after Phase 1 and may run concurrently with
separate owners. Phase 5 starts when at least two of the three source modules
have stable read APIs. Phase 6 starts only when all Phase 2–5 exit criteria are
met.

## 6. Definition of Done (every phase)

- Code reviewed by someone other than the author.
- Unit tests for normal, error, boundary, timeout, rate-limit, and retry paths.
- Integration tests against mock servers; sandbox tests where credentials exist.
- Race detector clean for concurrency-touching code.
- No new `Critical`/`High` security findings.
- Threat model and security review updated if a trust boundary changed.
- Documentation updated: module README, tool reference, configuration reference.
- Changelog entry for user-visible change.
- ADR written if an architectural decision was made.

## 7. Release plan

| Version | Content | Compatibility promise |
|---|---|---|
| v0.1.0 | Phase 1–2: Magento read + configurable write, MCP gateway, auth | Pre-1.0: breaking changes allowed with an ADR and changelog entry |
| v0.2.0 | Phase 3: Cloudflare analytics and security | Same |
| v0.3.0 | Phase 4: GA4 | Same |
| v0.4.0 | Phase 5: unified analytics | Same |
| v1.0.0 | Phase 6–7 complete: stable public interfaces, documented upgrade path | Semantic Versioning; tool names and schemas stable |

Release artifacts: multi-arch container image, static binaries, SPDX SBOM,
SHA-256 checksums, cosign signature, release notes generated from the changelog.
No agent may publish a release; the `release` environment requires maintainer
approval.

## 8. Success metrics

- Time from `git clone` to a working local MCP server with fake data sources:
  under 10 minutes.
- Time to add a new data source module by an outside contributor: under one week,
  touching no existing module.
- PR feedback under 10 minutes.
- Zero credential or customer-data leaks in repository history.
- Zero unresolved Critical/High security findings at release.

## 9. Out of scope

- A Magento plugin of any kind.
- Direct Magento database access.
- Write access to Cloudflare or GA4 configuration.
- A general SQL or raw API execution tool.
- Multi-tenant SaaS operation (schema reserves room, product does not implement).
- Adobe Commerce as a Service (IMS) support in v0.x.
- User-level GA4 data or any PII export.
