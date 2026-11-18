# ADR-0007: Multi-agent concurrent development model

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

The project is developed with multiple AI agents working concurrently on
different modules, plus a human maintainer. Agents are fast but can produce
inconsistent styles, duplicate abstractions, and conflicting edits. Public
review standards still apply: no self-approval, no unreviewed merges, no
untested claims.

## Problem

How do we let several agents work in parallel without corrupting shared files,
drifting interfaces, or bypassing review?

## Options considered

1. **One agent, sequential.** Consistent but slow; the human becomes the
   bottleneck.
2. **Free-for-all parallel agents on `main`.** Fast, high conflict risk, no
   review trail.
3. **Interface-first, ownership-partitioned agents with worktrees and
   independent review.** Slower to start, predictable and auditable.
4. **Full CI-gated microservice split per agent.** Overkill for the project size.

## Decision

Adopt option 3.

- The Architect freezes shared contracts (`core` interfaces, DTO rules, error
  taxonomy, tool naming) before module work starts.
- Each agent owns a disjoint set of paths, recorded in CODEOWNERS and in the
  ownership table.
- Each agent works on its own branch and, where supported, its own Git worktree.
- Agents never edit another agent's owned paths; shared files are edited only by
  the lead.
- A developer agent may not approve its own work. Review comes from a different
  agent or the maintainer, and the review record is committed under
  `docs/reviews/`.
- Interface or architecture changes require an ADR and Architect review before
  code is written.
- Conflicts are resolved by pausing the interface, comparing proposals, writing
  an ADR, and updating the contract — never by overwriting code.

## Rationale

The failure mode of parallel agents is not merge conflicts; it is semantic
divergence (three different error models, two pagination schemes, inconsistent
tool naming). Freezing contracts first and partitioning ownership makes
divergence visible early and cheap to fix. Independent review preserves the
open-source quality bar even when the only human is the maintainer.

## Consequences

### Positive

- Parallel work without shared-file races.
- Consistent public interfaces across modules.
- A review trail that satisfies open-source governance expectations.

### Negative

- Up-front contract work delays first code.
- An incorrect contract is expensive to change later; mitigated by ADRs.

### Security impact

Independent review is a security control, not a formality: it is the only check
on an agent that misunderstands a boundary. Security-owned paths require a
Security reviewer.

### Maintenance cost

Low process overhead per PR; higher up-front design cost.

## Follow-up

- Phase 0: publish the ownership table and review matrix.
- Phase 1: enforce ownership through CODEOWNERS and required checks.
- Revisit when a second human maintainer joins.

## References

- [Agent workflow](../development/agent-workflow.md)
- [Review process](../development/review-process.md)
