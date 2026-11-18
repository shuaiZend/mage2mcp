# ADR-0008: Apache-2.0 as the project license

- **Status:** Accepted
- **Date:** 2026-11-18
- **Deciders:** maintainer, Architect
- **Supersedes:** none

## Context

mage2mcp is a standalone Go MCP gateway for Magento 2, Cloudflare, and Google Analytics 4.
It is meant to be self-hosted by merchants, by Magento agencies for their clients, and by
enterprise platform teams, and it must be acceptable to the open-source program office of
each of them. The planned dependency set is permissive: the MCP Go SDK, pgx, asynq, chi,
golang-jwt, go-redis, prometheus/client_golang, yaml.v3, and testify, plus their transitive
modules, are MIT, BSD, ISC, or Apache-2.0 (see [license policy](../development/license-policy.md)).

## Problem

Which license lets the project grant patent assurance to adopters, stay compatible with its
dependency set, and remain deployable by commercial integrators, while keeping contribution
and redistribution obligations simple enough to enforce with a small maintainer team?

## Options considered

1. **Apache-2.0.** Permissive, with an express patent grant, patent retaliation, a trademark
   clause, and a NOTICE mechanism.
2. **MIT.** Permissive and minimal, with no express patent grant.
3. **GPL-3.0.** Copyleft; distributed derivatives must be GPL-3.0 with source available.
4. **Dual license**, for example Apache-2.0 plus a commercial license. Rejected early: it
   adds a commercial contract, pricing, and enforcement surface that a Phase 0 project
   cannot support, and it does not improve the licensing position of self-hosters.

## Decision

Adopt option 1: **Apache License, Version 2.0**, SPDX `Apache-2.0`, with the copyright line
`Copyright 2026 The mage2mcp Authors`, an unmodified `LICENSE`, a `NOTICE` file, DCO sign-off
instead of a CLA, and an allowlist dependency policy.

## Rationale

- **Patent grant and retaliation.** Section 3 gives every user a patent license from every
  contributor and withdraws it from anyone who sues alleging the work infringes. A gateway
  that reads and writes commerce data has a larger plausible patent surface than a utility
  library, and enterprise reviewers ask for this term by name.
- **Trademark clause.** Section 6 keeps forks from trading on the project name without a
  separate trademark policy.
- **Enterprise acceptance.** Apache-2.0 is on the default approved list of essentially every
  corporate open-source program office, so self-hosted deployment needs no license exception.
- **Dependency compatibility.** Every module in the resolved graph is MIT, BSD-2-Clause,
  BSD-3-Clause, ISC, Apache-2.0, or dual MIT/Apache-2.0; the added obligations are notice
  retention and NOTICE propagation only.
- **Outbound compatibility.** Apache-2.0 code can be combined into GPLv3 works, so copyleft
  downstream projects can reuse it, while GPLv3 code cannot flow back in.
- **NOTICE mechanism.** Section 4(d) gives third-party attribution a defined home that travels
  with the binary instead of an informal credits file.

MIT was rejected because it grants no express patent license and gives contributors no patent
peace. GPL-3.0 was rejected because it forces every distributed derivative, including an image
a Magento agency hands to a client, under copyleft, which blocks the intended adopters; it also
does not prevent hosted competition, since GPL-3.0 has no network clause.

## Consequences

### Positive

- Patent assurance and trademark protection are explicit and travel with every copy.
- No procurement exception is needed for enterprise self-hosting.
- The permissive dependency set needs no relicensing or isolation layer.
- Downstream GPLv3 projects can reuse the code.

### Negative

- Section 3 termination is unacceptable to some patent-holding integrators and cannot be waived.
- Section 4(d) adds a NOTICE obligation; the release must reproduce the notices of the four
  Prometheus modules and `yaml/go-yaml`, which the current `NOTICE` stub does not yet do.
- Apache-2.0 cannot be combined into a GPLv2-only work, so GPLv2-only Magento extensions cannot
  merge the code.
- A competitor may host a modified fork without publishing changes. Accepted: the defensible
  asset is integration quality and operational hardening, not source secrecy.
- Relicensing later would require contributor consent, which DCO-based contribution makes slow.

### Security impact

No direct runtime effect. The patent and trademark clauses reduce legal risk for adopters, and
the NOTICE and SBOM process makes the shipped third-party surface auditable. The license review
gate also blocks copyleft or unlicensed code from entering the supply chain, which is a
provenance control, not a legal formality.

### Maintenance cost

Low: one license file, one NOTICE file, a per-release notices bundle generated from the SBOM,
and a quarterly dependency license audit. The recurring cost is the audit, not the license.

## Follow-up

- Generate the release `THIRD-PARTY-NOTICES.md` from the SBOM before the first release.
- Switch dependency review from a deny list to an allowlist, and add Commons Clause and
  no-license handling.
- Resolve the open items in the license policy: MIT-0 approval, the `gitleaks-action` EULA, the
  Go 1.26 floor, and the archived `gopkg.in/yaml.v3`.
- Revisit only if a material adopter segment cannot accept Apache-2.0; that requires a new ADR.

## References

- [License and supply-chain policy](../development/license-policy.md)
- [Research baseline](../development/research-baseline.md), facts F1, F16, F23
- [Architecture](../architecture.md)
- [LICENSE](../../LICENSE), [NOTICE](../../NOTICE)
- [Contributing](../../CONTRIBUTING.md)
- [CI/CD](../development/ci-cd.md) — release SBOM and signing process
