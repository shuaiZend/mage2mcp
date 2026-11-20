# Security Policy

mage2mcp is a gateway that holds credentials for Magento, Cloudflare, and Google
Analytics 4, and that can perform write operations on those systems. Security is a
first-class design constraint, not a feature added after release. This document explains
which versions receive fixes, how to report a vulnerability privately, what to expect
from the maintainers, and what is in scope.

**Current release:** v0.11.1 (pre-1.0), tagged 2026-11-20. It is the latest
pre-release and the only version that receives security fixes. The gateway is
usable, but pre-1.0 interfaces may change through an ADR and a changelog entry,
and it has not been validated against a live Magento instance (see
[Verification status](#verification-status)).

## Supported versions

While the project is pre-1.0, security fixes are provided on the latest minor
release only. There are no long-term support branches and no backports to older
minors.

| Version | Security fixes | Notes |
|---|---|---|
| v0.11.x | Yes | Latest pre-release; fixes land in a patch on this minor |
| v0.10.0 and older | No | Upgrade to v0.11.x |
| Versions before v0.1.0 | Not applicable | No artifacts were published |

Pre-1.0 policy:

- Minor releases may contain breaking changes. Read the release notes before upgrading.
- Security fixes are not backported. If you cannot upgrade, you are responsible for
  carrying the fix yourself.
- Once 1.0 is released, this table will be replaced with a supported-version policy that
  names the maintained release lines and their support windows.

## Verification status

No live Magento, Cloudflare, or GA4 instance was available for the security
audit. Magento behaviour is verified against the REST contract and `httptest`
fake servers (`CONTRACT`); there is no automated live-store integration test.
Treat the first production use as a verification period: run `test/e2e` against
a Magento 2.4.9 sandbox and review the [known limitations](docs/audit/known-limitations.md)
before enabling writes. Cloudflare and GA4 live API behaviour is likewise
unverified.

## Reporting a vulnerability

**Do not open a public issue, pull request, or discussion for a vulnerability.**

Use GitHub Security Advisories:

1. Open the repository's **Security** tab.
2. Select **Advisories**, then **Report a vulnerability**.
3. Provide the details listed below.

This creates a private advisory visible only to the reporter and the maintainers, and it
gives us a private place to collaborate on a fix and a coordinated disclosure date.

**GitHub Security Advisories is the only supported private channel.** Do not send
vulnerability details to the conduct address, to a public issue, or to any address not
listed in this document. An email fallback may be added later; it will be published here
with a PGP fingerprint before it is used.

If you are unsure whether something is a vulnerability, report it privately anyway. A
report that turns out to be a hardening suggestion is still useful and costs little.

## What to include in a report

A good report lets the maintainer reproduce the issue without guessing.

- **Affected version or commit.** Release tag, container image digest, or commit SHA. If
  you built from source, include the Go version and the exact revision.
- **Affected component.** For example: MCP endpoint, Cloudflare Access JWT validation,
  RBAC, a specific tool handler, the confirmation mechanism, the import pipeline, the
  audit log, configuration loading, or a container image.
- **Reproduction steps.** A minimal sequence of requests or a proof-of-concept. Redact
  real tokens, account identifiers, and customer data; use placeholders.
- **Impact.** What an attacker gains: which data, which operations, which trust boundary
  is crossed. State the preconditions, such as a valid token with a particular scope.
- **Suggested fix or mitigation.** Optional, but helpful. If you have a patch, attach it
  to the advisory rather than opening a public pull request.
- **Disclosure status.** Whether the issue is already public, known to the upstream
  vendor, or under active exploitation.
- **Credit preference.** The name or handle to credit, or a request to remain anonymous.
- **Contact method.** How to reach you if the advisory thread is not enough.

Please do not include live credentials or personal data. If a secret is required to
demonstrate the issue, describe its shape instead of sending the value, and rotate it if
it was exposed.

## Response targets

These are targets, not guarantees. The project is maintained on a best-effort basis and
does not offer a service-level agreement.

Severity follows the project's review definitions: **Critical** means exploitable data
loss or credential exposure, **High** a missing control on a trust boundary, **Medium** a
weak or ambiguous control, **Low** hardening. The same definitions are used in
[review-process.md](docs/development/review-process.md).

| Stage | Target |
|---|---|
| Acknowledgement of the report | Within 3 business days |
| Initial triage and severity assessment | Within 7 business days |
| Fix or documented mitigation plan for High and Critical issues | Within 30 days |

Lower-severity issues are handled on a best-effort basis and may be batched into a
regular release. If a fix takes longer than the target, the maintainer will say so in the
advisory and give a revised estimate rather than let the report go silent.

## Disclosure policy

The project follows coordinated disclosure.

- **Default embargo: 90 days** from the date the report is acknowledged. The maintainer
  and the reporter agree on the disclosure date within that window.
- **Shorter embargo** may apply when a vulnerability is being actively exploited, is
  already public, or when the reporter can demonstrate imminent risk. The maintainer will
  coordinate with the reporter before publishing.
- **Longer embargo** may be agreed when a fix requires an upstream change or a coordinated
  release across components. Extensions are recorded in the advisory.
- **Credit.** Reporters are credited in the advisory and release notes by name or handle,
  unless they ask to remain anonymous.
- **Public disclosure.** After the fix is released, the advisory is published with the
  affected versions, impact, fix version, and credit. Please do not publish details
  before the agreed date; if you believe the embargo is being abused, raise it in the
  advisory thread.

## Scope

### In scope

- The gateway service: MCP endpoint, Streamable HTTP handling, request parsing, and
  protocol behavior.
- Authentication and authorization: Cloudflare Access JWT validation, token audience and
  expiry checks, RBAC scopes, and per-instance grants.
- Tool handlers: input validation, output validation, parameter handling, and upstream
  request construction.
- The confirmation mechanism: parameter hashing, binding, replay resistance, and
  enforcement of the read-only default.
- The import pipeline: CSV and JSON parsing, size limits, and file handling.
- Configuration loading: schema validation, secret handling, and precedence between YAML
  and environment variables.
- Audit logging: completeness, integrity, and redaction of secrets and sensitive values.
- Container images and release artifacts published by the project.
- Vulnerabilities in third-party dependencies that are reachable and exploitable through
  the gateway.

### Out of scope

- **Vulnerabilities in Magento, Cloudflare, or Google products themselves.** Report them
  to the respective vendor. If the gateway makes an upstream issue worse, that part is in
  scope and we will help route the report.
- **Social engineering, phishing, and physical attacks** against maintainers or users.
- **Denial of service through enormous but legitimate queries.** A single authorized
  caller requesting a very large report is a hardening and cost-control question, not a
  vulnerability. Reports about missing limits, unbounded pagination, or runaway cost are
  still welcome as hardening reports and will be treated as security-relevant quality
  issues.
- **Automated scanner output without impact analysis.** A raw scanner finding with no
  explanation of reachability or impact may be closed with a request for more detail.
- **Issues that require a compromised host, a compromised dependency at build time, or
  physical access** to the machine running the gateway, unless the gateway materially
  increases the impact.
- **Missing security headers or TLS configuration on infrastructure the project does not
  operate.** The project does not host a public service.

## Safe harbor

The maintainer will not pursue legal action against researchers who act in good faith
and follow this policy. Good-faith research means:

- Testing only against your own deployment, your own upstream accounts, and data you are
  authorized to access.
- Avoiding privacy violations, data destruction, and service disruption.
- Using the minimum access necessary to demonstrate the issue, and not exfiltrating data
  beyond what the proof requires.
- Reporting promptly and privately, and giving the project a reasonable opportunity to
  fix the issue before publication.
- Complying with applicable law.

If a third party initiates legal action against you for research conducted under this
policy, the maintainer will make it clear that the research was considered authorized
under this policy. This statement does not waive the rights of any third party, and it
does not authorize testing of systems the project does not own.

## Advisories, CVEs, and release integrity

- **Advisories.** Confirmed vulnerabilities are published as GitHub Security Advisories
  after a fix is available. Each advisory states affected versions, fixed version,
  severity, impact, workarounds, and credit.
- **CVE identifiers.** The maintainer requests a CVE through GitHub, which is a CVE
  Numbering Authority, for vulnerabilities that warrant one. Not every advisory receives
  a CVE; a GitHub advisory is the canonical record either way.
- **Release notes.** Security fixes are called out in release notes and link to the
  advisory. Upgrade instructions are included when a configuration change is required.
- **SBOM and checksums.** Every release ships with an SPDX 2.3 JSON Software Bill of
  Materials and SHA-256 checksums for its artifacts. The format and publication process
  are defined in the [license policy](docs/development/license-policy.md).
- **Signing.** Artifacts and container images are signed with cosign (keyless)
  for every tagged release. Verify the `.bundle` and `checksums.txt` before
  deploying; if a signature or checksum is missing from a release, treat the
  artifact as unverified and report it.
- **Dependency response.** Dependabot or an equivalent update process will open pull
  requests for vulnerable dependencies. A dependency advisory that affects the gateway is
  handled through the same triage process as a first-party vulnerability.

## Related documents

- [Threat model](docs/security/threat-model.md)
- [Security review process](docs/security/security-review.md)
- [Contributing guide](CONTRIBUTING.md)
- [Support](SUPPORT.md)
