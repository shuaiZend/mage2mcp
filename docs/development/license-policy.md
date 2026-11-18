# License and supply-chain policy

**Status:** Phase 0 policy baseline — adopted with [ADR-0008](../adr/0008-license.md).
**Owner:** Maintainer (license steward). **Last full audit:** 2026-11-18.
**Next scheduled audit:** 2027-02-18.

This document records the license decision for mage2mcp, the evidence behind it, the
dependency inventory it relies on, and the rules that keep the project compliant as
dependencies change. It is engineering due diligence, not legal advice; edge cases go to
professional counsel as described in section 8.

## 1. Purpose and scope

| Scope area | What is covered | Where it is enforced |
|---|---|---|
| Project source code | Every file in this repository authored by the project | `LICENSE`, `NOTICE`, SPDX headers, DCO |
| Go dependencies | Direct and transitive modules in the resolved module graph | Section 6 inventory, dependency review, module-graph scan |
| Container images | Base image layers and packages, plus the static binary inside them | Section 9, image SBOM |
| CI actions and tools | Every `uses:` reference and every `go install`ed binary in `.github/workflows/` | Section 10, SHA pinning, review |
| Documentation and release artifacts | Markdown, binaries, SPDX SBOM, checksums, cosign bundles, release notes | `LICENSE`, sections 9, 11, and 12 |

Out of scope: the licenses of Magento, Cloudflare, and Google Analytics. The project calls
their public APIs without redistributing them, so their terms of service govern that use.

## 2. License decision summary

| Item | Value |
|---|---|
| Project license | Apache License, Version 2.0 |
| SPDX identifier | `Apache-2.0` |
| Copyright line | `Copyright 2026 The mage2mcp Authors` |
| License text | [LICENSE](../../LICENSE) — unmodified Apache-2.0 |
| Attribution file | [NOTICE](../../NOTICE) — project attribution plus third-party pointers |
| Contribution model | DCO 1.1 sign-off, no CLA (section 11) |
| Dependency rule | Allowlist only (section 8) |

The sections below justify the decision, prove dependency compatibility, and define
maintenance.

### 2.1 Evaluation dimensions

The six dimensions from the project brief, for the three licenses seriously considered.
"Compatible" means compatible with this project's goals, not a legal conclusion.

| Dimension | Apache-2.0 | MIT | GPL-3.0 |
|---|---|---|---|
| Commercial use, including hosted service | Permitted; no source obligation for network use | Permitted; no source obligation | Permitted, but any distributed derivative must be GPL-3.0; GPL-3.0 has no network clause, so a purely hosted service triggers nothing |
| Patent grant | Express grant from each contributor, terminated by a patent suit (Apache-2.0 section 3) | No express grant; only whatever implied license local law recognizes | Express contributor grant (GPLv3 section 11), but no dedicated retaliation clause aimed at a licensee who sues |
| Derivative works | Permitted under any license; must keep notices, include the license, and state changed files | Permitted under any license; must keep the copyright and permission notice | Must be licensed GPL-3.0 when distributed; source must be offered |
| Enterprise internal deployment | No obligation beyond internal use; no distribution, no notice duty | Same, with even less process | Internal use without distribution is unencumbered, but legal teams treat copyleft as a procurement blocker |
| Third-party integration | Links freely with MIT, BSD, ISC, and Apache-2.0 code; combinable into GPLv3 works, not GPLv2-only works | Links freely with everything permissive; same GPLv3-only direction as Apache-2.0 | Forces the combined distributed work to GPL-3.0, which most Magento and Cloudflare integrators cannot accept |
| Community contribution | Corporate-friendly; sign-off plus an implicit patent grant; the NOTICE file records attribution | Simplest possible; some companies still ask who grants the patent license | Attracts copyleft-motivated contributors and repels commercial ones; incompatible with the intended adopter profile |

## 3. Why Apache-2.0

1. **Explicit patent grant and retaliation.** Section 3 grants every downstream user a
   patent license from every contributor and terminates it for anyone who sues alleging
   the work infringes. For a gateway that reads and writes commerce data, that is the
   term enterprises ask about, and the retaliation clause stops a patent-asserting
   contributor from forking the project and suing its users.
2. **Trademark clause.** Section 6 withholds trademark rights, so a fork cannot present
   itself as mage2mcp, without a separate trademark policy.
3. **Enterprise acceptance.** Apache-2.0 is on the default approved list of essentially
   every corporate open-source program office, removing a procurement step from each
   self-hosted deployment.
4. **Compatibility with the permissive dependency set.** Every module in the resolved
   graph is MIT, BSD, ISC, Apache-2.0, or dual MIT/Apache-2.0 (section 6); the only
   obligations added are notice retention and NOTICE propagation.
5. **The NOTICE mechanism.** Section 4(d) gives attribution a defined home that travels
   with the binary, instead of a `THANKS` file nobody propagates.

### 3.1 What Apache-2.0 costs

| Cost | Detail | Mitigation in this project |
|---|---|---|
| Patent termination (section 3) | A licensee who files a patent suit alleging the work infringes loses the patent license for the whole work, not just the disputed part. Some patent-holding integrators refuse Apache-2.0 for this reason, and we cannot waive the clause. | Documented in the README license section; integrators that cannot accept it are out of scope |
| NOTICE obligations (section 4(d)) | Redistributors must reproduce the attribution notices. Today `NOTICE` is a pointer file, and the Prometheus modules ship real NOTICE files that must be reproduced (section 9.3). | Release NOTICE is generated from the SBOM before tagging; the gap is an open question |
| GPLv2-only incompatibility | Apache-2.0 code cannot be combined into a GPLv2-only work, because its patent, notice, and termination terms are additional restrictions under GPLv2 section 6. A GPLv2-only Magento extension cannot merge this code. | Accepted; documented in section 5 and in the README |
| No protection against SaaS competitors | A competitor may host a modified fork without publishing changes. | Accepted: the defensible asset is integration quality and operational hardening, not source secrecy. Relicensing would need contributor consent and a new ADR |

## 4. Why not MIT

MIT is shorter, universally understood, and compatible with the same dependency set. It was
rejected for one reason: no express patent grant, so any patent license is whatever a court
implies from the distribution, which varies by jurisdiction and gives contributors and
users no written assurance. For a gateway that reads product, order, inventory, and traffic
data and writes back to a commerce system, the plausible patent surface is larger than for
a utility library. The Apache-2.0 grant plus retaliation turns an unstated risk into a
stated, symmetric term at the cost of a NOTICE file, and it denies contributors the trick
of contributing code and later asserting a patent against its users.

## 5. Why not GPL-3.0

GPL-3.0 was rejected because copyleft conflicts with the adoption goal:

- **Downstream self-hosters and integrators.** Any distributed derivative, including a
  patched image handed to a client, must be GPL-3.0 with source available. Magento
  agencies and Cloudflare integrators ship such images routinely and will not accept it.
- **Commercial adoption.** Many enterprises prohibit GPL-3.0 in products and procurement
  without legal review, adding friction to exactly the deployment model targeted here.
- **No network clause benefit.** GPL-3.0 does not require source disclosure for hosted use,
  so it would not prevent the SaaS competition Apache-2.0 also permits; the copyleft cost
  buys nothing.

### 5.1 The Apache-2.0 and GPL-3.0 compatibility nuance

The compatibility runs in one direction, and the direction matters:

- **Apache-2.0 code may be combined into a GPL-3.0 work.** The Free Software Foundation
  treats Apache-2.0 as GPLv3-compatible and GPLv3 section 7 accommodates its terms, so a
  GPLv3 project can absorb mage2mcp code.
- **GPL-3.0 code may not be relicensed as Apache-2.0**, so this project cannot take GPLv3
  code into its own tree. **Apache-2.0 is not GPLv2-compatible at all**: its patent, notice,
  and termination terms are additional restrictions under GPLv2 section 6, in either direction.

So the denylist in section 8 governs *inbound* code while the outbound direction stays
permissive: GPLv3 users are welcome downstream, GPLv2-only users are not served.

## 6. Dependency license inventory

### 6.1 Method

- Versions were resolved, not guessed: the nine direct dependencies were pinned in a
  scratch module and `go list -m all` produced the build list on 2026-11-18.
- SPDX identifiers come from the GitHub license API on 2026-11-18, cross-checked against
  each repository `LICENSE`. Where the API returned `NOASSERTION` the text was read
  directly: `klauspost/compress`, `munnerz/goautoneg`, and `pmezard/go-difflib` are
  BSD-3-Clause; `go-check/check` is BSD-2-Clause; `go-yaml/yaml` v3 and the MCP SDK are
  dual-licensed.
- Maintenance is a supply-chain signal, not a license verdict: **Active** = push within 12
  months, **Slow** = 12–24, **Dormant** = over 24, **Archived** = archived. "Test-only"
  means reachable only from a dependency's tests; module-graph membership does not prove
  linkage, so build info and the SBOM are authoritative (section 12).

### 6.2 Inventory

55 modules verified: 9 direct, 46 transitive. Distribution: MIT 24, BSD-3-Clause 14,
Apache-2.0 11, BSD-2-Clause 3, ISC 1, MIT-0 1, dual MIT + Apache-2.0 1.

| Module | Version | SPDX license | Repository maintained? | Transitive? | Compatibility verdict | Checked on |
|---|---|---|---|---|---|---|
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0 (MIT portions) | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/jackc/pgx/v5` | v5.13.0 | MIT | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/hibiken/asynq` | v0.26.0 | MIT | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/go-chi/chi/v5` | v5.5.0 | MIT | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/golang-jwt/jwt/v5` | v5.3.1 | MIT | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/redis/go-redis/v9` | v9.23.0 | BSD-2-Clause | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/prometheus/client_golang` | v1.25.0 | Apache-2.0 | Active | No (direct) | Compatible; NOTICE to propagate | 2026-11-18 |
| `gopkg.in/yaml.v3` | v3.0.1 | MIT + Apache-2.0 | Archived | No (direct) | Compatible; repo archived, migrate | 2026-11-18 |
| `github.com/stretchr/testify` | v1.12.1 | MIT | Active | No (direct) | Compatible | 2026-11-18 |
| `github.com/beorn7/perks` | v1.0.1 | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/bsm/ginkgo/v2` | v2.12.0 | MIT | Dormant | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/bsm/gomega` | v1.27.10 | MIT | Dormant | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/cespare/xxhash/v2` | v2.3.0 | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/davecgh/go-spew` | v1.1.1 | ISC | Dormant | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/dgryski/go-rendezvous` | v0.0.0-20200823014737-9f7001d12a5f | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/google/go-cmp` | v0.7.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `github.com/google/jsonschema-go` | v0.4.3 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/jackc/pgpassfile` | v1.0.0 | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/jackc/pgservicefile` | v0.0.0-20240606120523-5a60cdf6a761 | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/jackc/puddle/v2` | v2.2.3 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/jpillora/backoff` | v1.0.0 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/json-iterator/go` | v1.1.12 | MIT | Archived | Yes | Compatible; repo archived | 2026-11-18 |
| `github.com/klauspost/compress` | v1.20.1 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `github.com/klauspost/cpuid/v2` | v2.4.0 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/kr/pretty` | v0.3.0 | MIT | Dormant | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/kylelemons/godebug` | v1.1.0 | Apache-2.0 | Dormant | Yes | Compatible; NOTICE check required | 2026-11-18 |
| `github.com/modern-go/concurrent` | v0.0.0-20180306012644-bacd9c7ef1dd | Apache-2.0 | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/modern-go/reflect2` | v1.0.2 | Apache-2.0 | Slow | Yes | Compatible | 2026-11-18 |
| `github.com/munnerz/goautoneg` | v0.0.0-20191010083416-a7dc8b61c822 | BSD-3-Clause | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/mwitkow/go-conntrack` | v0.0.0-20190716064945-2f068394615f | Apache-2.0 | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/pmezard/go-difflib` | v1.0.0 | BSD-3-Clause | Dormant | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/prometheus/client_model` | v0.6.3 | Apache-2.0 | Active | Yes | Compatible; NOTICE to propagate | 2026-11-18 |
| `github.com/prometheus/common` | v0.72.0 | Apache-2.0 | Active | Yes | Compatible; NOTICE to propagate | 2026-11-18 |
| `github.com/prometheus/procfs` | v0.22.0 | Apache-2.0 | Active | Yes | Compatible; NOTICE to propagate | 2026-11-18 |
| `github.com/robfig/cron/v3` | v3.0.1 | MIT | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/segmentio/asm` | v1.1.3 | MIT-0 | Active | Yes | Compatible; MIT-0 allowlisted 2026-11-18 | 2026-11-18 |
| `github.com/segmentio/encoding` | v0.5.4 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/spf13/cast` | v1.10.0 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `github.com/stretchr/objx` | v0.5.3 | MIT | Active | Yes (test-only) | Compatible | 2026-11-18 |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 | BSD-3-Clause | Dormant | Yes | Compatible | 2026-11-18 |
| `github.com/zeebo/xxh3` | v1.1.0 | BSD-2-Clause | Active | Yes | Compatible | 2026-11-18 |
| `go.uber.org/atomic` | v1.12.0 | MIT | Active | Yes | Compatible | 2026-11-18 |
| `go.uber.org/goleak` | v1.3.0 | MIT | Active | Yes (test-only) | Compatible | 2026-11-18 |
| `go.yaml.in/yaml/v2` | v2.4.4 | Apache-2.0 (MIT portions) | Active | Yes | Compatible; NOTICE to propagate | 2026-11-18 |
| `go.yaml.in/yaml/v3` | v3.0.5 | Apache-2.0 (MIT portions) | Active | Yes | Compatible; NOTICE to propagate | 2026-11-18 |
| `golang.org/x/net` | v0.59.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/oauth2` | v0.37.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/sync` | v0.22.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/text` | v0.42.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/time` | v0.15.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `golang.org/x/tools` | v0.42.0 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `google.golang.org/protobuf` | v1.36.12 | BSD-3-Clause | Active | Yes | Compatible | 2026-11-18 |
| `gopkg.in/check.v1` | v1.0.0-20201130134442-10cb98267c6c | BSD-2-Clause | Dormant | Yes (test-only) | Compatible | 2026-11-18 |

No module in the resolved graph is under a denylisted license. Two findings affect the
same review without being license failures: two archived repositories. The Apache-2.0 NOTICE obligations are handled in section 9.3.

### 6.3 Findings that affect the dependency set

| Finding | Evidence | Consequence |
|---|---|---|
| `gopkg.in/yaml.v3` is archived | Repository archived, last push 2025-04-01 | Keep only while the replacement is validated; `go.yaml.in/yaml/v3` v3.0.5 is already in the graph via `prometheus/common` |
| `github.com/json-iterator/go` is archived | Repository archived, last push 2024-05-27 | Pulled by `prometheus/common`; not replaceable by us, tracked as an upstream risk |
| `github.com/segmentio/asm` is MIT-0 | Repository `LICENSE` read on 2026-11-18 | Permissive and compatible, but not an allowlisted identifier; pre-approve the variant per section 8.3 |
| Two direct modules require Go 1.26 | `prometheus/client_golang` v1.25.0 and `redis/go-redis/v9` v9.23.0 both declare `go 1.26.0` | **Resolved:** the project floor is raised to 1.26 and the CI matrix is 1.26.x/1.27.x ([ADR-0013](../adr/0013-go-toolchain-floor.md)); no older pins |

## 7. MCP Go SDK licensing nuance

`github.com/modelcontextprotocol/go-sdk` v1.8.0 is not uniformly Apache-2.0 today. Its
`LICENSE` opens with a transition statement, then contains both the Apache-2.0 and the MIT
text. New code and specification contributions are Apache-2.0, as are contributions whose
authors granted relicensing consent; contributions from authors who have not consented
remain MIT, and documentation contributions excluding specifications are CC-BY-4.0. The
GitHub license API therefore reports `NOASSERTION`, which is correct: there is no single
identifier. What this means for us:

| Topic | Consequence |
|---|---|
| SPDX expression | The SBOM records `Apache-2.0 AND MIT` for this module, not `Apache-2.0` alone |
| NOTICE | The SDK repository has no `NOTICE` file (checked 2026-11-18), so section 4(d) adds no propagation duty. The transition statement is still reproduced verbatim in the notices bundle because it qualifies the grant |
| Redistribution | MIT portions require the copyright and permission notice in the materials shipped with the binary; Apache-2.0 portions require a copy of the license. Bundling the SDK `LICENSE` unmodified satisfies both |
| Patent position | MIT portions carry no express patent grant, so the assurance in section 3 covers only the Apache-2.0 portions. Residual risk, not a blocker |
| Relicensing and docs | Never describe the SDK as Apache-2.0-only in a file we author. CC-BY-4.0 covers SDK documentation we do not redistribute; excerpts copied into `docs/` need attribution |

The project `NOTICE` already states the dual position correctly; this section is the
evidence behind that sentence.

## 8. Policy for new dependencies

### 8.1 Allowlist

A dependency may be added only if its license, read from its own `LICENSE`, is on this list.

| SPDX identifier | Status | Conditions |
|---|---|---|
| `MIT`, `ISC` | Allowed | Keep the copyright and permission notice |
| `BSD-2-Clause` | Allowed | Keep the copyright notice and disclaimer |
| `BSD-3-Clause` | Allowed | Keep the copyright notice, disclaimer, and no-endorsement clause |
| `Apache-2.0` | Allowed | Include the license text; propagate any `NOTICE` into the release NOTICE |
| `MPL-2.0` | Allowed with review, not in the automated allowlist | File-level copyleft: MPL-covered files stay MPL; publish modifications to them; never paste MPL code into files we license Apache-2.0. Because the CI allowlist omits it, adding one requires a policy update and Security review |
| `MIT-0` | Allowed | Permissive, no attribution required; OSI-approved. Approved 2026-11-18 for `segmentio/asm` and future use; keep the identifier distinct from `MIT` in the SBOM |
| `CC-BY-4.0` | Allowed for documentation and data | Appears in the MCP Go SDK composite license (documentation). Never acceptable for executable code the project must defend |
| `LicenseRef-scancode-google-patent-license-golang` | Allowed | The additional Go patent grant attached to `golang.org/x/*`; compatible with Apache-2.0 |

### 8.2 Denylist

Any of these blocks a pull request. The workflow uses an allowlist rather than a denylist
so an unknown license fails closed; this denylist records the identifiers that must never
be approved and is enforced by that allowlist plus review.

| Identifier or condition | Why it is denied |
|---|---|
| `GPL-2.0` | Copyleft; incompatible with Apache-2.0 in either direction |
| `GPL-3.0` | Copyleft; forces downstream distribution under GPL-3.0 |
| `AGPL-3.0` | Copyleft with a network clause; unacceptable for a hosted gateway |
| `LGPL-2.1`, `LGPL-3.0` | Weak copyleft with relinking and source obligations a static Go binary cannot satisfy cleanly |
| `SSPL-1.0` | Not OSI-approved; service-offer obligations are incompatible with the deployment model |
| Commons Clause | Not an open-source license; forbids selling the software, which breaks commercial redistribution |
| No license, or an unresolved `NOASSERTION` | Absence of a grant means no right to redistribute |

### 8.3 Review process for anything unusual

| Case | Rule |
|---|---|
| `MIT-0` | Allowed (section 8.1). Attribution-free; the identifier stays distinct from `MIT` in the SBOM |
| `CC0-1.0`, `Unlicense`, `CC-BY-4.0` | Public-domain dedications and attribution-only licenses, allowed for fixtures, data, and documentation but never for code the project must defend; maintainer approval, a recorded patent-risk note, and attribution in the file or notices bundle |
| Dual license (for example `MIT OR Apache-2.0`) | Choose the allowlisted branch, record the choice in the inventory, keep that branch's notices |
| Custom terms, EULA, "source available" | Denied by default. An exception needs an ADR, a Security review, and a written statement of the obligation accepted |
| Unknown or unreadable license, or any exception | Denied until resolved; an exception is recorded with a reviewer, a date, and a revisit condition |

A dependency whose terms are ambiguous, or material to the project's licensing position, is
referred to professional counsel before merge; the maintainer records referral and answer.

## 9. Container and artifact licensing

### 9.1 Base image

The repository has no `Dockerfile` yet, so this is a requirement on the first one.

| Option | License position | Verdict |
|---|---|---|
| `scratch` with a static binary, or `gcr.io/distroless/static-debian12:nonroot` | Scratch has no OS layer to review; distroless is Apache-2.0 and carries `ca-certificates` and `tzdata` whose package licenses must be read from the image SBOM | Preferred; distroless when TLS roots and time zones are needed |
| `debian:*-slim`, `alpine` | Dozens of package licenses, including GPL-2.0 and GPL-3.0 components; in Alpine, musl is MIT but BusyBox is GPL-2.0. Aggregation in one image is not linking, so the Apache-2.0 code is unaffected, but a naive deny-list scan over the image SBOM will fail | Debian only with a reviewed image SBOM; Alpine avoided, and an exception must document the aggregation argument |

The binary is built with `CGO_ENABLED=0`, so the Go runtime and standard library
(BSD-3-Clause) are the only code it carries beyond the module graph. Base image package
licenses are `[U]` until an image SBOM exists; verify with `syft <image> -o spdx-json` and
read every package license in the result.

### 9.2 Artifact set per release

| Artifact | License content | Control |
|---|---|---|
| `mage2mcp_<os>_<arch>` binaries, `mage2mcp.sbom.spdx.json` | Embedded module code and Go runtime (BSD-3-Clause); SPDX 2.3 JSON with one package per module | Both signed with cosign; build info lists every module; SBOM attached to the release |
| `checksums.txt`, `<artifact>.bundle` | None | SHA-256 over every artifact; keyless cosign bundle per artifact |
| Container image, `THIRD-PARTY-NOTICES.md` (planned) | Base layers plus binary; full attribution bundle | Image digest pinned in the release notes, image signature a Phase 1 gate; notices generated from the SBOM (9.3) |

### 9.3 What the release NOTICE must contain

1. The project attribution block from [NOTICE](../../NOTICE), unchanged, and a copy of the
   Apache-2.0 license text.
2. The verbatim `NOTICE` files of `prometheus/client_golang`, `prometheus/common`,
   `prometheus/client_model`, `prometheus/procfs`, and `yaml/go-yaml` (covering
   `go.yaml.in/yaml/v2` and `v3`) — the only modules in the graph that ship one — plus the
   copyright and permission notices for every MIT, BSD-2-Clause, BSD-3-Clause, and ISC
   module, grouped by license.
3. The MCP Go SDK `LICENSE` in full, including its transition statement (section 7), and a
   statement that image base layers carry their own licenses, pointing at the image SBOM.

The repository `NOTICE` is a pointer file, sufficient for the source repository but not for
a binary release; the generated bundle is the release-time artifact.

## 10. CI/CD and GitHub Actions

### 10.1 Action inventory

Verified 2026-11-18 against each action's repository license metadata. Every action below
is currently referenced by a floating major tag and must move to a SHA pin (10.3).

| Action | Reference | SPDX license | Note |
|---|---|---|---|
| `actions/checkout`, `actions/setup-go`, `actions/setup-node`, `actions/upload-artifact` | `@v4`, `@v5`, `@v4`, `@v4` | MIT | Pin by SHA in Phase 1 |
| `actions/dependency-review-action`, `softprops/action-gh-release`, `github/codeql-action/upload-sarif` | `@v4`, `@v2`, `@v3` | MIT | Dependency review enforces section 8.2 |
| `anchore/sbom-action`, `sigstore/cosign-installer`, `docker/build-push-action`, `ossf/scorecard-action` | `@v0`, `@v3`, `@v6`, `@v2.4.0` | Apache-2.0 | Pin by SHA in Phase 1 |
| `gitleaks` CLI (`github.com/zricethezav/gitleaks/v8`) | `v8.30.1` | MIT | Compliant; installed with `go install` |

`go install`ed tools are dependencies too: `govulncheck` (`golang.org/x/vuln`, BSD-3-Clause)
and `gosec` (`github.com/securego/gosec/v2`, Apache-2.0) are pinned to exact versions.

### 10.2 Secret scanning tool

The `gitleaks/gitleaks-action@v2` action was rejected during Phase 0: its `LICENSE.txt` is a
Gitleaks LLC EULA requiring a paid key for organization-owned repositories and forbidding
modification. CI now installs the MIT-licensed `gitleaks` CLI
(`github.com/zricethezav/gitleaks/v8@v8.30.1`) with `go install` and runs it directly, so only
allowlisted code executes in the pipeline.

### 10.3 Pinning and review of actions

- Every `uses:` reference is pinned to a 40-character commit SHA with a version comment;
  tags are mutable and are a supply-chain risk, as [CI/CD](ci-cd.md) section 7.2 already
  requires. The current tag pins are a known Phase 1 gap.
- A new action is a dependency: check its license against section 8.1, review its
  permissions, and add it to 10.1 in the same pull request. Dependabot refreshes pins
  weekly (npm monthly); a major bump is a reviewed pull request that re-triggers the check.

### 10.4 Dependency review consistency

[`.github/workflows/dependency-review.yml`](../../.github/workflows/dependency-review.yml)
denies `GPL-2.0, GPL-3.0, AGPL-3.0, LGPL-2.1, LGPL-3.0, SSPL-1.0`. The policy denylist
adds Commons Clause and "no license", which that configuration cannot express. Switch from
`deny-licenses` to `allow-licenses` with the section 8.1 list so the check fails closed on
unknown, missing, and Commons-Clause-adjacent terms; the action accepts one mode, not both.
Keep the deny list only as a second line of defence, and handle `CC-BY-4.0` explicitly,
because the action cannot tell code from documentation.

The action reads the dependency diff in a pull request; it does not read the resolved
module graph, inspect `go install`ed tools, or run on `push`. The module-graph license scan
that closes that gap is a Phase 1 gate in [CI/CD](ci-cd.md) section 5.
The earlier `1.25.x` matrix conflict is resolved: the project floor is Go 1.26 and the
matrix is `1.26.x`/`1.27.x`, per [ADR-0013](../adr/0013-go-toolchain-floor.md).

## 11. Contribution licensing

- **DCO, not CLA.** Every commit carries `Signed-off-by: Name <email>` certifying the
  Developer Certificate of Origin 1.1. There is no copyright assignment and no CLA, matching
  [CONTRIBUTING.md](../../CONTRIBUTING.md); a commit without a matching sign-off is not
  merged, and squash merges preserve the sign-off line. Inbound equals outbound: a
  contribution is licensed under Apache-2.0, and no contribution is accepted under
  different terms without an ADR.
- **SPDX headers.** Every new Go file starts with
  `// SPDX-License-Identifier: Apache-2.0` before the package clause and any build tag.
  Generated files add a `// Code generated ... DO NOT EDIT.` line; existing files receive
  the header when next modified.
- **Third-party code.** Accepted only if its license is allowlisted, the origin and version
  are stated in the pull request, the copyright line is preserved, and the module or
  snippet is added to the inventory. Code of unknown provenance, including model output
  that reproduces a known project, is rejected.
- **Documentation** is licensed under Apache-2.0 with the rest of the repository. Whether
  `docs/` should move to CC-BY-4.0 is open (section 14).

## 12. SBOM and third-party notices process

| Step | When | How |
|---|---|---|
| Generate the source SBOM | Every `v*` tag, in `release.yml` | `syft dir:. -o spdx-json=dist/mage2mcp.sbom.spdx.json`, currently via `anchore/sbom-action` |
| Verify linkage | Same run | `go version -m dist/mage2mcp_linux_amd64` lists the modules actually linked; compare with the SBOM package list |
| Generate notices, publish, sign | Same run, before publication | Render section 9.3 from the SBOM; publish SBOM, checksums, bundles, and notices as release assets; keyless cosign over each artifact; image attestation is a Phase 1 gate |
| Review a diff | On every dependency pull request | Compare package name, version, and `LicenseConcluded` with the previous SBOM; a new package without an inventory row, or a changed license on an existing module, blocks the pull request |
| Regenerate locally | On demand | `syft dir:. -o spdx-json` and `go list -m all`, which is what section 6 uses |

The SBOM is the machine-readable record, this document the human-readable one; a license in
the SBOM that is missing here means this table is wrong until proven otherwise. Format choice
is tracked in [CI/CD](ci-cd.md) section 12.

## 13. Audit cadence and ownership

| Trigger | Action | Owner |
|---|---|---|
| Quarterly (next 2027-02-18) | Re-run section 6 over the resolved graph; refresh versions, licenses, maintenance status | Maintainer |
| New dependency, or a new or changed CI action | Section 8 review before merge; inventory and 10.1 rows added in the same PR | Reviewer plus maintainer |
| Major upgrade of a direct dependency, or an upstream license or repository change | Re-check license, `NOTICE`, and transitive additions; re-audit dependents; update inventory and notices | Maintainer |
| Before each minor release | Confirm SBOM, notices bundle, and inventory agree | Maintainer |
| Any exception, EULA, or ambiguous term | ADR plus Security review; counsel referral recorded | Maintainer |

The maintainer (`@shuaiZend` in [CODEOWNERS](../../CODEOWNERS)) is the license steward, and
Security reviews any change to the release path, the signing setup, or a license exception.

## 14. Open questions

| Question | Owner | Resolution point |
|---|---|---|
| ~~Add `MIT-0` to the allowlist?~~ Resolved: allowed in section 8.1 | Maintainer | Closed 2026-11-18 |
| ~~Replace `gitleaks-action`?~~ Resolved: MIT CLI v8.30.1, action removed | Maintainer plus Security | Closed 2026-11-18 |
| ~~Raise the Go floor to 1.26?~~ Resolved: floor is 1.26 ([ADR-0013](../adr/0013-go-toolchain-floor.md)) | Maintainer | Closed 2026-11-18 |
| Migrate off archived `gopkg.in/yaml.v3` to `go.yaml.in/yaml/v3`? Decided yes; `go.yaml.in/yaml/v3` is already in the graph | Maintainer | Phase 1, with `go.mod` |
| ~~Switch dependency review to `allow-licenses`?~~ Resolved: workflow uses `allow-licenses` | Maintainer | Closed 2026-11-18 |
| Move `docs/` to CC-BY-4.0, or keep one license for the repository? | Maintainer | Post-1.0 |
| Must Apache-2.0 notices also appear in container image labels? | Maintainer | First image build |
| Is a counsel review of the MCP SDK's MIT portions warranted before 1.0? | Maintainer | Before 1.0 |

## 15. Verification log

| Check | Environment | Date | Result |
|---|---|---|---|
| MCP SDK version, `go.mod`, dual license text | `goproxy.cn` and `raw.githubusercontent.com`, tag `v1.8.0` | 2026-11-18 | v1.8.0, go 1.25.0, Apache-2.0 with MIT portions confirmed |
| Resolved module graph for the nine direct dependencies | Local Go toolchain with `goproxy.cn`, `go list -m all` | 2026-11-18 | 55 modules; Go 1.26.11 selected by two `go 1.26.0` floors |
| SPDX license and maintenance status per module | GitHub REST API metadata (`license`, `archived`, `pushed_at`) plus `LICENSE` reads for `NOASSERTION` cases | 2026-11-18 | 55 of 55 identified; no denylisted license; 2 archived (`gopkg.in/yaml.v3`, `json-iterator/go`), 18 dormant, 1 slow, 34 active |
| NOTICE files in Apache-2.0 dependencies | Raw repository files | 2026-11-18 | Present in the four Prometheus modules and `yaml/go-yaml`; absent in the MCP SDK |
| CI action licenses | GitHub REST API per action repository | 2026-11-18 | 12 permissive; the one proprietary action was removed and replaced by the MIT gitleaks CLI |
| Dependency review configuration | `.github/workflows/dependency-review.yml` | 2026-11-18 | Allowlist mode; identifiers match section 8.1 |
| Base image package licenses | No image exists | 2026-11-18 | `[U]`; verify with `syft <image> -o spdx-json` at the first build |

## 16. Decision record

| Decision | Record |
|---|---|
| Project license is Apache-2.0 | [ADR-0008](../adr/0008-license.md) |
| DCO instead of a CLA | [CONTRIBUTING.md](../../CONTRIBUTING.md) |
| Allowlist dependency policy and SBOM per release | This document |
| SBOM format, signing format, provenance | [CI/CD](ci-cd.md) section 12, threat model T-33 and T-35 |
