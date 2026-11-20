# Current state and handoff

This page records what exists today and what the next contributor or agent
should do next. It is updated at the end of each development session.

## What works

- MCP gateway over Streamable HTTP (official Go SDK), tool registry enforcing
  the tool contract, structured content with a text fallback, health and
  Prometheus metrics.
- Cloudflare Access JWT verification (RS256/ES256, JWKS, issuer/audience/expiry),
  default-deny RBAC, instance-aware authorization and audit, two-step human
  confirmation for writes.
- Magento 2: product read/write, configurable create/add-child, MSI assignment,
  bounded import validation and execution, multi-store routing (reads and writes).
- Cloudflare: traffic, security summary, security events; multi-zone routing.
- GA4: traffic, metadata, capability probe, realtime, ecommerce overview, item
  performance; multi-property routing.
- Unified: Cloudflare requests vs GA4 sessions comparison with declared semantics.
- PostgreSQL migrations, durable audit, a PostgreSQL-backed job queue/worker
  (no Redis, ADR-0019), the operation ledger with fencing/UNKNOWN/reconciler,
  persisted import batches, and the admin approval CLI.
- Verification: unit/contract/PostgreSQL/fake-Magento e2e tests via
  `scripts/local-ci.sh`; GitHub CI is a manual pre-release workflow. No live
  Magento integration test exists.

## Next steps, in order

1. **Live Magento validation.** Run `test/e2e` and the read-only acceptance
   against a real Magento 2.4.9 sandbox; no live instance was available for the
   audit.
2. **Rate limiting and quotas.** Add a per-subject/per-instance limiter and
   Cloudflare/GA4 quota accounting (SEC-11, PERF-05).
3. **Tamper-evident audit.** Add a hash chain/signed checkpoint, retention
   partitions, and close the REL-16-R audit gap for the five self-ledgered money
   tools.
4. **Multi-instance unified bindings.** Make the unified adapters resolver-aware
   and remove the single-zone/single-property validation (ADR-0018).
5. **Session identity binding.** Bind the MCP session to the verified identity
   (SEC-15).
6. **Raw Cloudflare logs.** Implement the R2/ClickHouse path and
   `cloudflare.logs.*` tools (ADR-0004, PLANNED).
7. **Maintenance drills.** Dependency upgrade run, security-response tabletop,
   contributor onboarding from a clean clone.

## Known limitations

- Import execution has a bounded synchronous path (8 MiB / 5000 rows) and an
  asynchronous job path when `database.url` is set; without PostgreSQL the
  `magento.import.start/status/cancel/batches/batch/resume` tools are not
  registered.
- Unified analytics currently covers one scenario and its bindings are
  single-instance: a binding requires exactly one Cloudflare instance and
  exactly one GA4 instance (Magento may have several). Multi-zone or
  multi-property bindings are rejected at configuration validation until the
  adapters become resolver-aware (ADR-0018).
- Cloudflare Logpush inspection and raw-log querying are not implemented.
- GA4 BigQuery export is a documented boundary, not a feature.
- v0.11.2 is published as a pre-release (signed artifacts, SBOM, multi-arch GHCR
  image) and deployed on the test host. The release-workflow fixes from v0.0.1
  (allowed-action `owner/repo@*` patterns and GHCR login) remain in place.
