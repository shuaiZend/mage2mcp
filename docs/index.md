# Documentation

This is the entry point for mage2mcp documentation. The documents below
describe the shipped v0.11.1 gateway; design catalogues are kept alongside the
implemented surface and are marked where they are not as-built.

## Latest release

[**v0.11.1**](https://github.com/shuaiZend/mage2mcp/releases/tag/v0.11.1) (2026-11-20) is
the current pre-1.0 release: the production-readiness remediation. Read
the [v0.11.1 release notes](releases/v0.11.1.md), the
[Releases page](https://github.com/shuaiZend/mage2mcp/releases) for artifacts and
signatures, and [CHANGELOG.md](../CHANGELOG.md) for the full history.

## Status labels

Capability claims in this documentation use the audit vocabulary:

| Label | Meaning |
|---|---|
| **TESTED (CONTRACT)** | A test exercises the behaviour against a fake upstream or an in-process client. |
| **TESTED (PostgreSQL)** | A test exercises the behaviour against a real PostgreSQL instance. |
| **IMPLEMENTED** | Code exists; no direct test or only partial coverage is stated. |
| **PLANNED** | Designed, not implemented; the residual risk is in [known limitations](audit/known-limitations.md). |
| **DEPRECATED** | Superseded; the old behaviour is no longer supported. |

`INTEGRATION_TESTED` is not used: no live Magento integration test exists. The
integration documents additionally use source tags (`[V-live]`, `[V-docs]`,
`[U]`) for the provenance of vendor facts; those tags do not imply that the
gateway has an integration test.

The consolidated evidence is in [development/research-baseline.md](development/research-baseline.md)
and the release audit is in [audit/](audit/).

## Start here

| Document | Read it for |
|---|---|
| [Architecture](architecture.md) | System context, containers, request lifecycle, configuration, deployment |
| [Module boundaries](module-boundaries.md) | Ownership, dependency rules, Go contracts |
| [MCP tool catalog](mcp-tools.md) | Tool naming, schemas, permissions, catalog |
| [MCP conformance](mcp-conformance.md) | Best-practice matrix and the tests that prove it |
| [Implemented tools](implemented-tools.md) | The tools registered in code today |
| [Magento write fields](magento-fields.md) | Field requirements and SEO guidance for Magento writes |
| [Write safety](write-safety.md) | Catalog/URL warnings and the `write_safety` prompt |
| [Database design](database-design.md) | Schema, indexes, retention, migrations |
| [Unified analytics](unified-analytics.md) | Cross-source metric semantics and join rules |
| [Development plan](development-plan.md) | Phases, exit criteria, traceability matrix |
| [Configuration](configuration.md) | Every configuration key, default, and environment override |
| [Compatibility](compatibility.md) | Supported components, interface stability promises, version support |
| [Production acceptance](operations/production-acceptance.md) | Read-only acceptance against a live Magento instance and the fixes it produced |
| [Actions bump review](development/actions-bump-review.md) | Review of the GitHub Actions major-version bump (PR #46) |
| [Product copy run](development/product-copy-run.md) | End-to-end product copy on the demo and the 11 MCP defects it found |
| [v0.11.1 release notes](releases/v0.11.1.md) | Remediation highlights, breaking changes, upgrade and verification checklist |
| [v0.11.0 release notes](releases/v0.11.0.md) | Highlights, breaking changes, upgrade and verification checklist |
| [v0.10.0 release notes](releases/v0.10.0.md) | Previous release notes |
| [v0.9.0 release notes](releases/v0.9.0.md) | Previous release notes |
| [v0.8.1 release notes](releases/v0.8.1.md) | Previous release notes |
| [v0.8.0 release notes](releases/v0.8.0.md) | Previous release notes |
| [v0.7.6 release notes](releases/v0.7.6.md) | Previous release notes |
| [v0.7.5 release notes](releases/v0.7.5.md) | Previous release notes |
| [v0.7.4 release notes](releases/v0.7.4.md) | Previous release notes |
| [v0.7.3 release notes](releases/v0.7.3.md) | Previous release notes |
| [v0.7.2 release notes](releases/v0.7.2.md) | Previous release notes |
| [v0.7.1 release notes](releases/v0.7.1.md) | Previous release notes |
| [v0.7.0 release notes](releases/v0.7.0.md) | Previous release notes |
| [v0.6.0 release notes](releases/v0.6.0.md) | Previous release notes |
| [v0.5.0 release notes](releases/v0.5.0.md) | Previous release notes |
| [v0.4.0 release notes](releases/v0.4.0.md) | Previous release notes |
| [v0.3.0 release notes](releases/v0.3.0.md) | Previous release notes |
| [v0.2.0 release notes](releases/v0.2.0.md) | Previous release notes |
| [v0.1.0 release notes](releases/v0.1.0.md) | Previous release notes |

## Integrations

| Document | Scope |
|---|---|
| [Magento 2](integrations/magento.md) | REST/GraphQL mapping, EAV, configurable lifecycle, MSI, import |
| [Cloudflare](integrations/cloudflare.md) | GraphQL analytics, security events, Logpush, plan matrix |
| [Google Analytics 4](integrations/ga4.md) | Data API, field catalog, compatibility validation, quotas |

## Security

| Document | Scope |
|---|---|
| [Threat model](security/threat-model.md) | Assets, trust boundaries, STRIDE, attack trees, mitigations |
| [Security review](security/security-review.md) | As-built control matrix, status labels, residual risk |

## Development

| Document | Scope |
|---|---|
| [Agent workflow](development/agent-workflow.md) | Roles, ownership, concurrency, review, conflict handling |
| [Review process](development/review-process.md) | PR checklist, reviewer duties, blocking rules |
| [CI/CD](development/ci-cd.md) | Workflows, gates, release process |
| [Testing strategy](development/testing-strategy.md) | Test layers, coverage targets, environments |
| [License policy](development/license-policy.md) | License choice, dependency matrix, SBOM |
| [Risk register](development/risk-register.md) | Open risks and mitigations |
| [Handoff](development/handoff.md) | Current state, next steps, known limitations |
| [Reviews](reviews/phase-0-report.md) | Phase 0 report (historical) and independent review records |
| [Research baseline](development/research-baseline.md) | Verified facts and sources |

## Module guides

| Module | Scope |
|---|---|
| [Magento](../internal/magento/README.md) | REST client, product/configurable/inventory/import |
| [Cloudflare](../internal/cloudflare/README.md) | Traffic, security summary and events |
| [GA4](../internal/ga4/README.md) | Reports, metadata, capabilities, realtime |
| [Unified](../internal/unified/README.md) | Cross-source comparison semantics |

## Operations

| Document | Scope |
|---|---|
| [Deployment](operations/deployment.md) | Components, Compose, Cloudflare Tunnel, configuration, capacity |
| [Backup and restore](operations/backup-restore.md) | What to back up, restore checklist, retention |
| [Upgrade](operations/upgrade.md) | Versioning, procedure, rollback, compatibility |
| [Monitoring](operations/monitoring.md) | Health, metrics, logs, audit queries, alerts |
| [Release checklist](release-checklist.md) | Pre-tag checks, publishing, signature verification, rollback |

## Decision records

Architecture decisions live in [adr/](adr/). Every significant technical choice
has one; a change to an accepted decision requires a new ADR that supersedes it.

## Evidence

Sanitized verification records and the release audit:

- [Magento evidence](evidence/magento.md)
- [Cloudflare evidence](evidence/cloudflare.md)
- [GA4 evidence](evidence/ga4.md)
- [Production acceptance](operations/production-acceptance.md) (read-only, live store)
- [Magento write verification](development/magento-write-verification.md) (disposable demo)
- [Known limitations](audit/known-limitations.md)
- [Documentation consistency audit](audit/documentation-consistency.md)

## Project policies

| Document | Scope |
|---|---|
| [CONTRIBUTING.md](../CONTRIBUTING.md) | Development environment, DCO, branch and commit rules, review gates |
| [SECURITY.md](../SECURITY.md) | Supported versions, private vulnerability reporting, disclosure timeline |
| [SUPPORT.md](../SUPPORT.md) | Where to ask questions and what support is in scope |
| [CODE_OF_CONDUCT.md](../CODE_OF_CONDUCT.md) | Community expectations and enforcement |
| [CHANGELOG.md](../CHANGELOG.md) | Release history in Keep a Changelog form |
| [LICENSE](../LICENSE) and [NOTICE](../NOTICE) | Apache-2.0 terms and third-party attributions |
