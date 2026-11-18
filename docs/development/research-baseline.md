# Phase 0 research baseline

This document is the shared, verified starting point for all Phase 0 design work.
Every Phase 0 document must be consistent with it. When a document contradicts
this baseline, the baseline wins until it is amended with new evidence.

**Baseline date:** 2026-11-18
**Verification legend:**

| Tag | Meaning |
|---|---|
| `[V-live]` | Confirmed against a live system (API call, running binary, real response) during Phase 0 |
| `[V-docs]` | Confirmed against official vendor documentation or released source code |
| `[U]` | Not yet verified; must state what would verify it and how |

Rules:

1. Every capability claim in an integration document carries one of these tags.
2. `[U]` claims must never be presented as working features in README or tool descriptions.
3. Vendor documentation can be wrong or version-specific. Where the live system can
   answer a question, the live system is the source of truth.
4. No credentials, tokens, customer data, or full upstream response bodies may appear
   in any document or evidence file.

## Verified facts

| ID | Fact | Evidence | Tag |
|---|---|---|---|
| F1 | Official MCP Go SDK latest stable is **v1.8.0**; its `go.mod` requires **go 1.25.0**; licensed Apache-2.0 with MIT-licensed portions remaining | `proxy.golang.org` equivalent via goproxy.cn: `@latest`, `v1.8.0.mod`, `LICENSE` | `[V-live]` |
| F2 | SDK exports `mcp.NewServer`, `mcp.AddTool[In,Out]` (automatic JSON Schema 2020-12 validation of inputs and outputs), `mcp.NewStreamableHTTPHandler`, `auth.RequireBearerToken`, `auth.TokenVerifier`, `Server.AddReceivingMiddleware` | SDK v1.8.0 source: `mcp/tool.go`, `mcp/streamable.go`, `mcp/server.go`, `auth/auth.go` | `[V-live]` |
| F3 | SDK supports protocol version **2026-07-28** and negotiates down to 2025-11-25 | SDK v1.8.0 `mcp/shared.go`, `mcp/server.go` | `[V-live]` |
| F4 | Local workstation Go is 1.21.4, but `GOTOOLCHAIN=auto` successfully downloads and runs go1.25.0; latest available toolchain is go1.27.4 | Local `go run` probe; toolchain version list | `[V-live]` |
| F5 | `proxy.golang.org` is unreachable from the local workstation; `goproxy.cn` works. CI runners are expected to use the default proxy | Network probes | `[V-live]` |
| F6 | Magento **2.4.9** exists in the current release line (alongside 2.4.8-p5, 2.4.7-p10) | Adobe Commerce release versions page | `[V-docs]` |
| F7 | Configurable product endpoints confirmed: `POST /V1/products`, `POST /V1/configurable-products/{sku}/options`, `POST /V1/configurable-products/{sku}/child`, `GET /V1/products/{sku}`, `GET /V1/eav/attribute-sets/list`, `GET /V1/products/attribute-sets/{setId}/attributes`, `GET /V1/categories` | AdobeDocs/commerce-webapi tutorial sources | `[V-docs]` |
| F8 | Magento GraphQL has **no product write mutations**; product mutations cover reviews, compare lists, wishlist, and stock/price alerts only. Product management must use REST admin APIs | AdobeDocs/commerce-webapi `graphql/schema/products/mutations/` | `[V-docs]` |
| F9 | Bulk/async status endpoints: `GET /V1/bulk/{bulkUuid}/status`, `GET /V1/bulk/{bulkUuid}/operation-status/{status}`, `GET /V1/bulk/{bulkUuid}/detailed-status`; asynchronous APIs require a configured message queue | Adobe Commerce bulk/async documentation | `[V-docs]` |
| F10 | MSI endpoints confirmed: `POST /V1/inventory/bulk-product-source-assign`, `POST /V1/inventory/bulk-product-source-unassign`, `POST /V1/inventory/bulk-product-source-transfer`, `POST /V1/inventory/bulk-partial-source-transfer`, `POST /V1/inventory/source-items`, `GET /V1/inventory/source-items`, `POST /V1/inventory/source-items-delete` | Adobe Commerce inventory documentation | `[V-docs]` |
| F11 | Magento import endpoints: `POST /V1/import/csv` (base64 payload) and `POST /V1/import/json`; validation strategies `validation-stop-on-errors` / `validation-skip-errors` with `allowedErrorCount` | Adobe Commerce import documentation | `[V-docs]` |
| F12 | Magento REST authentication differs by deployment: self-hosted/PaaS uses admin or integration tokens; Adobe Commerce as a Cloud Service uses Adobe IMS OAuth and does not support traditional token generation | Adobe Commerce authentication documentation | `[V-docs]` |
| F13 | Cloudflare Logpush is available on Free, Pro, and Business (self-service, usage-based), not Enterprise-only; 25 GB/month included per account, then $0.03/GB to internal destinations (R2, Pipelines) and $0.10/GB to external destinations; no historical backfill; logs are lost if a job fails | Cloudflare Logpush overview and pricing docs | `[V-docs]` |
| F14 | An HTTP Requests Logpush record is roughly 1.5–3 kB uncompressed; 1M requests/day ≈ 1.5–3 GB/day ≈ 45–90 GB/month | Cloudflare Logpush sizing docs | `[V-docs]` |
| F15 | Bot Score, bot tags, and verified-bot fields in `http_requests` logs require Bot Management (Enterprise add-on); they are unavailable on Pro | Cloudflare `http_requests` dataset field documentation | `[V-docs]` |
| F16 | Cloudflare GraphQL Analytics exposes 70+ datasets; default limit 300 queries per 5-minute window; a zone-scoped query may include up to 10 zones, an account-scoped query exactly 1 account | Cloudflare GraphQL limits docs | `[V-docs]` |
| F17 | Security Events are backed by the `firewallEventsAdaptive` dataset; retention is up to 31 days across plans; results may be sampled | Cloudflare WAF Security Events documentation | `[V-docs]` |
| F18 | GA4 Data API returns aggregate data only. Compatibility is enforced **per field pair**, not per scope: live probes accepted `ecommercePurchases` + `eventCount` and `itemId` + `sessions`, while `itemId` + `screenPageViews` was rejected. `itemSku` does not exist in the schema; use `itemId`. Raw event data requires the BigQuery export. The GA4 skills library's blanket rules are contradicted by these results | Live `runReport` compatibility probes; see [evidence/ga4.md](../evidence/ga4.md) | `[V-live]` |
| F19 | Target GA4 property `properties/354542598` ("Giztop - GA4") is readable, time zone **America/Los_Angeles**, currency **USD** | Live `runReport` | `[V-live]` |
| F20 | Live GA4 quotas observed: 200,000 tokens/day, 40,000 tokens/hour, 10 concurrent requests, 14,000 tokens/project/hour, 10 server errors/project/hour, 120 potentially thresholded requests/hour | Live `runReport` with `returnPropertyQuota` | `[V-live]` |
| F21 | The same property served 116,184 sessions on 2026-11-17 but returned 0 `ecommercePurchases` and 0 `purchaseRevenue` for the last 7 days. Ecommerce tracking must be probed, never assumed | Live `runReport` | `[V-live]` |
| F22 | Google API and documentation domains (`*.googleapis.com`, `developers.google.com`) are unreachable from the local workstation; GA4 verification is only possible through the configured GA4 MCP server | Network probes | `[V-live]` |
| F23 | Candidate dependency licenses: pgx, asynq, chi, golang-jwt, jsonschema-go, testify = MIT; go-redis = BSD-2-Clause; prometheus/client_golang = Apache-2.0; all actively maintained and not archived | GitHub repository metadata | `[V-live]` |
| F24 | Local tooling: gh CLI authenticated with `repo` + `workflow` scopes; Docker 29.8 with Compose v5.5; Node 26 / npm 11; git 2.39 | Local probes | `[V-live]` |

## Corrections to the project brief

These points in the original brief are superseded by the evidence above. Documents
must follow the corrected position and record the change in the relevant ADR.

1. **Cloudflare Logpush is not Enterprise-only.** The risk is cost, dataset
   entitlement, and irrecoverable loss during job failure — not availability.
2. **Magento GraphQL cannot manage products.** All product writes go through REST.
   GraphQL is used for storefront read verification (for example confirming that a
   configurable product actually renders variant choices).
3. **The target GA4 property has no ecommerce data today.** Unified ecommerce
   scenarios can be structurally validated but not numerically validated against
   this property.
4. **Go 1.25+ is a hard requirement**, driven by the official MCP SDK. The local
   Go 1.21.4 install works only because toolchain auto-download is enabled.

## Document conventions

- English only for technical documents. README is bilingual (English + zh-CN).
- One sentence per line is not required, but keep paragraphs short and scannable.
- Use tables for endpoint, dataset, metric, and control matrices.
- Use `mermaid` for diagrams that benefit from it; keep every diagram readable at
  100 characters wide.
- Cite sources inline as markdown links to the official documentation.
- Never invent endpoint paths, field names, or quota numbers. If unsure, mark `[U]`
  and describe the verification step.
- Each integration document ends with a **Verification log** section listing what was
  checked, when, against which environment, and the result.
- Each design document ends with **Open questions** and **Decision record** sections
  that link to ADRs.
