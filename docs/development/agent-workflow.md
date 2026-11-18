# Multi-agent development workflow

**Status:** Phase 0 baseline. Applies to every agent and human contributor.

The project is developed by a maintainer working with multiple AI agents in
parallel. This document defines who owns what, how agents work concurrently
without corrupting shared files, and how review preserves the open-source quality
bar. The decision record is [ADR-0007](../adr/0007-multi-agent-development.md).

## 1. Roles

| Role | Responsibility | Does not |
|---|---|---|
| Architect | Contracts, module boundaries, ADRs, integration review | Implement every module |
| Magento engineer | Magento client, EAV, products, configurable lifecycle, MSI, media, import | Touch Cloudflare or GA4 code |
| Cloudflare engineer | API client, GraphQL templates, security events, Logpush metadata | Modify Cloudflare configuration |
| GA4 engineer | Data API client, report builder, compatibility validator | Modify GA4 properties |
| Security engineer | AuthN/AuthZ, secrets, threat model, security tests, audits | Approve its own security changes |
| MCP engineer | Server, transport, registry, tool schemas, protocol tests | Bypass module services |
| Unified analyst | Cross-source models, joins, metric semantics, SKU mapping | Import module internals |
| QA engineer | Independent tests, regression, performance, review | Write the feature under test |
| Documentation engineer | README, guides, references, ADR formatting | Change design decisions |

One person or agent may hold several roles in a small team, but the same change
must never be authored and approved by the same agent.

## 2. Ownership

| Path | Owner | Required reviewer |
|---|---|---|
| `internal/core/`, `cmd/` | Architect | Maintainer |
| `internal/security/` | Security | Architect + Security |
| `internal/mcp/` | MCP | Architect |
| `internal/magento/` | Magento | Architect (contract changes) |
| `internal/cloudflare/` | Cloudflare | Architect (contract changes) |
| `internal/ga4/` | GA4 | Architect (contract changes) |
| `internal/unified/` | Unified | Architect |
| `internal/storage/`, `internal/jobs/` | Architect | Maintainer |
| `tests/` | QA | Module owner |
| `docs/` | Docs | Architect for design docs |
| `.github/workflows/` | Maintainer | Security for auth/secret steps |

CODEOWNERS encodes this routing. Editing a path you do not own requires the
owner's approval in the PR, not a silent override.

## 3. Interface-first sequence

1. Architect writes or updates the contract in `docs/module-boundaries.md`.
2. Contract tests and fake implementations are specified.
3. DTOs and error mappings are agreed.
4. Input/output schemas for affected tools are specified.
5. Modules are implemented in parallel against the contract.
6. QA tests independently against the contract.
7. Architect reviews integration; Security reviews boundary changes.
8. Maintainer merges.

No module may block on another module's implementation: fakes make parallel work
possible.

## 4. Branches and worktrees

- `main` is protected: PR required, status checks required, linear history, no
  force push, no deletion.
- Branch naming: `feat/<scope>`, `fix/<scope>`, `docs/<scope>`, `test/<scope>`,
  `chore/<scope>`.
- One agent per branch. Where the platform supports it, one Git worktree per
  agent, so uncommitted work never collides.
- Commits use Conventional Commits and must be signed off (`git commit -s`) per
  the DCO.
- Squash merge only. The PR description carries the rationale that would
  otherwise be lost.
- Agents never push to `main`, never force-push a shared branch, and never rebase
  another agent's branch.

## 5. Review matrix

| Change type | Author | Required reviewers |
|---|---|---|
| Documentation only | Docs | Maintainer or Architect |
| Module feature (no contract change) | Module owner | Another agent + Maintainer |
| Public interface or DTO change | Module owner | Architect + Maintainer |
| Auth, RBAC, secrets, audit | Security | Architect + Maintainer |
| CI/CD, release, supply chain | Maintainer | Security |
| Dependency addition | Any | Maintainer + license check |
| Security fix | Security | Maintainer (private review if embargoed) |

Reviewer duties:

- Read the actual diff and tests, not the author's summary.
- Verify the change matches the contract and the ADR.
- Check error paths, cancellation, resource cleanup, and concurrency.
- Confirm no secrets, credentials, or customer data were introduced.
- Confirm documentation and changelog are updated.
- Record findings as review comments with severity (`Critical`, `High`, `Medium`,
  `Low`). Unresolved `Critical` or `High` blocks merge.

Self-approval is prohibited. An agent that authored a change may not be the
reviewer of record.

## 6. Review record

Every PR that is merged with an administrative bypass (for example, the solo
maintainer merging their own design PR) must include a committed review record at
`docs/reviews/<pr-number>-review.md` containing:

- the reviewed commit range,
- the reviewer identity (agent name and model, or human),
- the checklist used,
- findings and their resolution,
- an explicit statement of what was **not** verified.

This is how a one-person project keeps an auditable review trail. The bypass is
temporary: it is removed when a second maintainer joins.

## 7. Conflict resolution

When two agents disagree about an interface, a data model, or an architectural
choice:

1. Stop changes to the contested surface.
2. Each side writes a short proposal: problem, options, recommendation, impact.
3. Architect compares against project constraints and security requirements.
4. Record the decision in an ADR.
5. Update the contract and tests in the same PR.
6. Resume module work against the updated contract.

Overwriting another agent's code to "resolve" a disagreement is a process
violation and is reverted.

## 8. Agent behavior constraints

Every agent must:

1. Read the architecture and boundary documents before writing code.
2. Never change a public interface without an ADR.
3. Never edit another agent's owned paths.
4. Never approve its own work.
5. Never skip tests or disable a check to make CI pass.
6. Never invent an endpoint, field name, or SDK method.
7. Never introduce a dependency with an incompatible license.
8. Never write credentials, tokens, or customer data into code, fixtures, or docs.
9. Never present mock-verified behavior as real-environment verified.
10. Never present "it compiles" as "it works".
11. Update documentation with every behavior change.
12. Mark unverified functionality explicitly as unverified.

## 9. Phase 0 execution record

| Wave | Agents | Files |
|---|---|---|
| 0 | Lead | Repository, license, governance, CI, architecture, boundaries, ADRs |
| 1 | Magento, Cloudflare, GA4, Security, Docs | Integration designs, threat model, READMEs, governance prose |
| 2 | Data, MCP, Platform, License | Database design, tool catalog, CI/CD docs, license policy |
| 3 | Unified, Lead | Unified analytics, development plan, risk register |
| 4 | Independent reviewers | Architecture, security, documentation review |
| 5 | Lead | Integration, review records, PR, maintainer report |

Each wave commits separately. Reviewers in Wave 4 did not author the documents
they review.

## 10. Onboarding a new agent

Provide, in this order:

1. This document and [module-boundaries.md](../module-boundaries.md).
2. The owning module's integration design.
3. The relevant ADRs.
4. The verification baseline and the rules for tagging claims.
5. The exact paths the agent owns and the paths it must not touch.

An agent that has not read items 1–4 must not be given write access to a module.
