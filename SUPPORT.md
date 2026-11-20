# Support

mage2mcp is a community-maintained open source project. This document explains where to
ask questions, what the maintainers can and cannot help with, and what to include when
you report a problem.

**Current release:** v0.11.1 (pre-1.0), tagged 2026-11-20. The gateway runs as
a server and worker pair with PostgreSQL; the quickstart, configuration
reference, deployment guide, and known limitations are the place to start. The
project has not been validated against a live Magento instance (see
[SECURITY.md](SECURITY.md#verification-status)); treat the first production use
as a verification period.

## Where to ask

| You need | Channel |
|---|---|
| Usage questions, design questions, "how should this work?" | [GitHub Discussions](https://github.com/shuaiZend/mage2mcp/discussions) |
| Bug reports | [GitHub Issues](https://github.com/shuaiZend/mage2mcp/issues) |
| Feature proposals | [GitHub Issues](https://github.com/shuaiZend/mage2mcp/issues) |
| Vulnerability reports | [GitHub Security Advisories](https://github.com/shuaiZend/mage2mcp/security/advisories) — private, never a public issue |
| Code of conduct concerns | `shuai@shuangyulin.cn` or the maintainer's GitHub account |

Search existing discussions and issues before opening a new one. Duplicate questions
split the answer and waste reviewer time.

### GitHub Discussions

Use Discussions for open-ended questions: whether a design fits your deployment, how a
tool is expected to behave, how to map a Magento or Cloudflare concept into the gateway,
or how to approach a contribution. Discussions are also the right place to propose a new
data source module before writing an issue or an ADR.

### GitHub Issues

Use Issues for something actionable: a reproducible defect, a documentation error, or a
concrete feature proposal with a use case. An issue should be closeable by a change in
the repository.

Do not use Issues for:

- Vulnerability reports. Use Security Advisories; a public issue can expose users before
  a fix exists.
- Support questions that need a conversation. Use Discussions.

### Security Advisories

Report vulnerabilities privately through the repository's Security tab, as described in
[SECURITY.md](SECURITY.md). Never include exploit details, live credentials, or customer
data in a public issue.

## What maintainers can and cannot support

The project is maintained on a best-effort basis by volunteers and contributors. There is
no service-level agreement, no guaranteed response time, and no on-call rotation.

### What we can do

- Answer design and usage questions in Discussions, as time allows.
- Triage bug reports and confirm or reject them with a reason.
- Review pull requests that follow [CONTRIBUTING.md](CONTRIBUTING.md).
- Fix security issues according to the targets in [SECURITY.md](SECURITY.md).
- Correct documentation, including the compatibility matrix and the Chinese README.
- Help route a report about Magento, Cloudflare, or Google to the right upstream channel
  when the gateway is involved.

### What we cannot do

- **Guarantee response times or resolution.** No SLA applies to community support.
- **Respond to production incidents.** The project does not operate your deployment and
  cannot join an incident bridge, page an on-call engineer, or provide emergency fixes.
- **Support Magento, Cloudflare, or Google Analytics accounts.** Account provisioning,
  billing, quotas, plan entitlements, and vendor-side outages are the vendor's
  responsibility. Logpush billing and Bot Management entitlement are Cloudflare matters.
- **Provision or troubleshoot your credentials.** Creating Magento integration tokens,
  Cloudflare API tokens, GA4 service accounts, or Cloudflare Access applications is your
  responsibility as the operator.
- **Debug your infrastructure.** Kubernetes, reverse proxies, TLS, DNS, firewalls, and
  database operations are outside the project's scope, except where the gateway's own
  configuration is the cause.
- **Provide custom development, consulting, or commercial support.** The project does not
  sell support.
- **Give legal, privacy, or compliance advice.** You are responsible for how your
  deployment handles personal data and for the terms of the upstream APIs you use.

## Before you ask

A little preparation gets a much better answer.

1. Read the [README](README.md), the documentation map, and
   [`docs/development/research-baseline.md`](docs/development/research-baseline.md). Many
   questions are already answered there, including why some capabilities are out of
   scope.
2. Search Discussions and Issues for the error message or the concept.
3. Reduce the problem to the smallest reproducible case.
4. Collect the facts listed below, with secrets removed.

### What to include in a bug report

- mage2mcp version or commit SHA, and the Go version if you built from source.
- Deployment shape: container or local binary, PostgreSQL version (Redis is not
  used), and whether a reverse proxy or Cloudflare Tunnel sits in front of the
  gateway.
- The upstream involved: Magento version and edition, Cloudflare plan and dataset, or GA4
  property type. Do not include account IDs, zone IDs, property IDs, or tokens.
- The exact tool name and a redacted input. Replace tokens, hostnames, SKUs, order
  numbers, and customer identifiers with placeholders.
- What you expected and what happened, including the full error message and a redacted
  log excerpt with timestamps.
- Whether the behavior is reproducible, and any recent configuration change.

Never paste credentials, JWTs, service account JSON, customer data, or full upstream
response bodies into an issue. If a maintainer needs a sensitive value, they will ask for
a redacted form or a private channel.

## Troubleshooting

These are the as-built behaviours to check first. The full reference is
[docs/configuration.md](docs/configuration.md); deployment details are in
[docs/operations/deployment.md](docs/operations/deployment.md).

- **Configuration validation.** The gateway validates YAML and environment
  configuration at startup, rejects unknown keys, and reports every problem it
  finds. There is no validation-only mode; `-healthcheck` probes a running
  server and does not load `-config`.
- **Health endpoints.** `/healthz` reports process liveness. `/readyz` reports
  that the process can serve MCP and the registry tool count; `platform.info` is
  served outside the registry, so a fresh development start reports `"tools":0`.
  There is no `/livez`.
- **Metrics.** `/metrics` is a Prometheus endpoint on the same listener. It is
  not authenticated by the gateway; restrict the listener or put it behind your
  edge/Access policy.
- **Authentication.** A non-loopback listener refuses to start with
  authentication disabled unless `-dev-insecure` or
  `MAGE2MCP_DEV_INSECURE=true` is set. Never use that combination in production.
- **Writes.** `tools.writes: true` registers the write tools, but approvals need
  `database.url` (the `mage2mcp-admin` CLI reads the shared store). Without
  PostgreSQL, writes are registered but cannot be approved, and the operation
  ledger is disabled.
- **Async imports.** The `magento.import.start/status/cancel/batches/batch/resume`
  tools are registered only when `database.url` is set.
- **Unified bindings.** A `bindings` block requires Magento, Cloudflare, and GA4
  to be enabled, with exactly one Cloudflare instance and one GA4 instance.
  Other shapes are rejected at startup.
- **Magento verification.** No live Magento instance was available for the
  audit. If a Magento call behaves differently from the contract, that is a
  reportable defect; include the request shape and the upstream message.
- **Logs.** Structured JSON through `slog`, with secret-shaped attributes
  redacted. Parameters and response bodies are not logged.

If a document and the code disagree, the code wins and the documentation is the
defect; report it. The [known limitations](docs/audit/known-limitations.md) list
the accepted gaps.

## Response expectations

- Discussions and Issues are read on a best-effort basis. Expect days, not hours.
- Security reports follow the targets in [SECURITY.md](SECURITY.md): acknowledgement
  within 3 business days, triage within 7, and a fix or mitigation plan for High and
  Critical issues within 30 days. Those are targets, not guarantees.
- Pull request review follows the process in [CONTRIBUTING.md](CONTRIBUTING.md) and is best effort.

## Related documents

- [README](README.md)
- [Contributing](CONTRIBUTING.md)
- [Security policy](SECURITY.md)
- [Code of conduct](CODE_OF_CONDUCT.md)
