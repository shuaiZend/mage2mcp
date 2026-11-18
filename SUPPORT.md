# Support

mage2mcp is a community-maintained open source project. This document explains where to
ask questions, what the maintainers can and cannot help with, and what to include when
you report a problem.

The project is in Phase 0 (design). There is no runnable server yet, so most current
questions are about the design, the API mappings, and the security model.

## Where to ask

| You need | Channel |
|---|---|
| Usage questions, design questions, "how should this work?" | [GitHub Discussions](https://github.com/shuaiZend/mage2mcp/discussions) |
| Bug reports | [GitHub Issues](https://github.com/shuaiZend/mage2mcp/issues) |
| Feature proposals | [GitHub Issues](https://github.com/shuaiZend/mage2mcp/issues) |
| Vulnerability reports | [GitHub Security Advisories](https://github.com/shuaiZend/mage2mcp/security/advisories) — private, never a public issue |
| Code of conduct concerns | `conduct@mage2mcp.dev` (TO BE CONFIGURED) or the maintainer's GitHub account |

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
- Deployment shape: container or local binary, PostgreSQL and Redis versions, and whether
  a reverse proxy sits in front of the gateway.
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

## Troubleshooting (Phase 1 and later)

There is nothing to troubleshoot yet. When the first runnable build lands, the operator
guide will document the following, and this section will link to it:

- **Configuration validation.** The gateway will validate YAML and environment
  configuration at startup, reject unknown keys, and disable an invalid instance with a
  field-level error that names the offending key without echoing the secret value. A
  validation-only mode is planned so configuration can be checked before deployment.
- **Health endpoints.** `/healthz` reports process liveness, `/readyz` reports whether at
  least one module is ready and the database is reachable, and `/livez` provides an
  optional deeper check. A module that fails its startup health check is disabled while
  the other modules keep running.
- **Logs.** Structured JSON logs through `slog`, with one event per tool call carrying
  the tool, instance, identity, permission, outcome, duration, and parameter hash.
  Parameters are never logged verbatim, and the log level is configurable.
- **Metrics.** A Prometheus endpoint is planned for tool calls and latency, upstream
  requests and quota, job state, and authentication failures.

Until those exist, [the architecture document](docs/architecture.md) and the rest of
[`docs/`](docs/) are the reference for intended behavior. If a design document is
ambiguous, open a Discussion; that feedback improves the design before code exists.

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
