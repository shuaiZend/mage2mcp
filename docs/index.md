# Documentation

This is the entry point for mage2mcp documentation. The project is in **Phase 0
(design)**: the documents below are the proposed baseline pending maintainer approval, not
descriptions of a running system.

## Verification legend

Every capability claim is tagged:

| Tag | Meaning |
|---|---|
| `[V-live]` | Verified against a live system during Phase 0 |
| `[V-docs]` | Verified against official vendor documentation or released source |
| `[U]` | Not yet verified; the verification step is stated |

The consolidated evidence is in [development/research-baseline.md](development/research-baseline.md).

## Start here

| Document | Read it for |
|---|---|
| [Architecture](architecture.md) | System context, containers, request lifecycle, configuration, deployment |
| [Module boundaries](module-boundaries.md) | Ownership, dependency rules, Go contracts |
| [MCP tool catalog](mcp-tools.md) | Tool naming, schemas, permissions, catalog |
| [Database design](database-design.md) | Schema, indexes, retention, migrations |
| [Unified analytics](unified-analytics.md) | Cross-source metric semantics and join rules |
| [Development plan](development-plan.md) | Phases, exit criteria, traceability matrix |

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
| [Security review](security/security-review.md) | Control matrix, authn/authz design, residual risk |

## Development

| Document | Scope |
|---|---|
| [Agent workflow](development/agent-workflow.md) | Roles, ownership, concurrency, review, conflict handling |
| [Review process](development/review-process.md) | PR checklist, reviewer duties, blocking rules |
| [CI/CD](development/ci-cd.md) | Workflows, gates, release process |
| [Testing strategy](development/testing-strategy.md) | Test layers, coverage targets, environments |
| [License policy](development/license-policy.md) | License choice, dependency matrix, SBOM |
| [Risk register](development/risk-register.md) | Open risks and mitigations |
| [Reviews](reviews/phase-0-report.md) | Phase 0 report and independent review records |
| [Research baseline](development/research-baseline.md) | Verified facts and sources |

## Decision records

Architecture decisions live in [adr/](adr/). Every significant technical choice
has one; a change to an accepted decision requires a new ADR that supersedes it.

## Evidence

Sanitized records of Phase 0 live verification:

- [Magento evidence](evidence/magento.md)
- [Cloudflare evidence](evidence/cloudflare.md)
- [GA4 evidence](evidence/ga4.md)
