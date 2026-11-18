# mage2mcp

**Open source, modular, multi-source AI integration platform — a Magento 2 MCP gateway.**

mage2mcp exposes Magento 2, Cloudflare, and Google Analytics 4 data and
operations to AI agents through the [Model Context Protocol](https://modelcontextprotocol.io).
It runs as a standalone Go service; it does not require installing a plugin in
Magento and never connects directly to the Magento database.

> **Status: Phase 0 — design.** No runnable server exists yet. The architecture,
> module boundaries, API mappings, and security model are being written and
> reviewed in public.

## Why

AI agents need reliable, auditable access to commerce and analytics systems.
Most integrations today are either vendor-hosted black boxes or one-off scripts
that bypass the business logic and permissions of the systems they touch.
mage2mcp takes a different position:

- **Standalone gateway** — the MCP server is a separate deployment; Magento stays untouched.
- **Official APIs only** — Magento REST/GraphQL, Cloudflare API, GA4 Data API.
- **Read-only by default** — write operations are explicit, permissioned, and audited.
- **Modular by construction** — each data source is an independent module behind a shared contract.
- **Built for public review** — threat model, ADRs, SBOM, and CI gates from day one.

## Planned modules

| Module | Scope |
|---|---|
| Magento 2 | Products (simple, configurable, virtual, downloadable, bundle), EAV, MSI inventory, media, categories, orders, bulk import |
| Cloudflare | GraphQL analytics, security events, WAF/bot insights, Logpush-based log access |
| Google Analytics 4 | Traffic, acquisition, events, ecommerce, realtime |
| Unified analytics | Cross-source analysis with explicit metric semantics |

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

## Contributing

The project is not accepting feature pull requests until the Phase 0 design is
approved. Design review comments and threat-model feedback are welcome via
GitHub issues.
