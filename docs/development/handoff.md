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
- PostgreSQL migrations, durable audit, and a PostgreSQL-backed job queue/worker
  (no Redis, ADR-0019), plus the admin approval CLI.
- CI: docs, dco, actions-pinned, go 1.26/1.27, gosec, govulncheck, gitleaks,
  dependency-review, integration (PostgreSQL), coverage floor, release dry run.

## Next steps, in order

1. **Asynchronous import.** Add a `magento:import` job to `internal/jobs`, store
   batches in `import_batches`/`import_items`, expose
   `magento.import.status/cancel/report`, and make execution resumable. The
   synchronous bounded path stays as the small-batch fast path.
2. ~~**Magento order reads.**~~ Done: `magento.order.get`, `magento.order.search`,
   and `magento.sales.summary` are implemented; the unified ecommerce scenarios
   can now build on them.
3. **Unified expansion.** Channel revenue, product funnel, and decline
   correlation using the order reads, each with explicit formulas and quality
   flags.
4. **Release v0.1.0.** Tag on `main`, approve the `release` environment, then
   verify signatures, SBOM, and checksums per `docs/release-checklist.md`.
5. **Maintenance drills.** Dependency upgrade run, security-response tabletop,
   contributor onboarding from a clean clone.

## Known limitations

- Import execution is bounded and synchronous (8 MiB / 5000 rows); large imports
  need the job path above.
- Cloudflare Logpush inspection and raw-log querying are not implemented.
- GA4 BigQuery export is a documented boundary, not a feature.
- Unified analytics currently covers one scenario.
- v0.1.0 is published as a pre-release (signed artifacts, SBOM, multi-arch GHCR
  image) and deployed on the test host. The release-workflow fixes from v0.0.1
  (allowed-action `owner/repo@*` patterns and GHCR login) remain in place.
