# MCP tool contract

**Status:** as-built for v0.11.3
(2026-11-20). This document defines how tools are named, validated, authorized,
confirmed, and versioned. The authoritative list of registered tools is
[implemented-tools.md](implemented-tools.md); this page does not duplicate it.

**Verification boundary.** Magento tools are tested against the REST contract and
`httptest` fakes (`CONTRACT`). No live Magento integration test exists, and
nothing here is `INTEGRATION_TESTED`.

## 1. Principles

1. **No universal escape hatch.** There is no `execute_api`, `execute_sql`, or
   raw GraphQL tool. Every tool maps to a typed service method.
2. **One tool, one intent.** A tool that does two unrelated things is split.
3. **Schemas are strict.** `additionalProperties: false`, required fields listed,
   enums for fixed values, patterns for identifiers.
4. **Permissions are declared, not inferred.** A tool without a permission is a
   registration error.
5. **Read-only by default.** Write tools are registered only when
   `tools.writes: true`; every write requires two-step human approval.
6. **Descriptions are static.** Tool descriptions are compiled constants and never
   interpolate upstream content.
7. **Every write is gated.** A write either uses the middleware operation gate or
   is explicitly declared self-ledgered (the five money tools).

## 2. Naming

```text
<module>.<domain>.<action>
```

- `module`: `magento`, `cloudflare`, `ga4`, `unified`, `platform`.
- `domain`: the resource or analysis area (`product`, `configurable`,
  `attribute`, `inventory`, `order`, `import`, `category`, `media`, `analytics`,
  `security`, `ecommerce`, `traffic`, `property`, `job`).
- `action`: a lower_snake_case verb (`get`, `search`, `create`, `update`,
  `delete`, `list`, `assign`, `summary`).

Examples: `magento.product.get`, `magento.configurable.add_child`,
`cloudflare.security.events`, `ga4.ecommerce.products`,
`unified.traffic.compare`, `platform.info`.

Rules enforced by `internal/mcp/registry`:

- The name matches `^(magento|cloudflare|ga4|unified|platform)\.[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`
  and the MCP 1–128 character `[A-Za-z0-9_.-]` rule.
- No version suffix; no instance-specific name. Instances are a parameter.
- Names are stable once released; a rename requires a deprecation cycle.

### Naming register: the recorded exceptions

The grammar above is a guideline, and a few shipped names do not follow it. They are
listed here rather than renamed: every tool name is a public contract, so a rename is
a breaking change for every client and needs a deprecation cycle, not a cleanup pass.
This register is the record MCP-10 asked for.

| Exception | Names | Why it stays |
|---|---|---|
| Singular/plural split | `magento.attribute.sets` next to `magento.attributes.*`; `magento.categories.flat` next to `magento.category.*`; `magento.products.get`/`.bulk_update` next to `magento.product.*`; `magento.orders.geography` next to `magento.order.*` | The plural forms read as collection operations and the singular ones as single-resource operations. Both readings are defensible; changing either breaks clients. |
| Four segments | `magento.attributes.options.create`, `.bulk_create`, `.delete`; `magento.product.media.*`; `magento.product.options.*`; `magento.product.links.*`; `magento.product.tier_prices.*`; `magento.configurable.*` | These address a sub-resource of a resource, and a three-segment name would have to flatten the distinction (for example `magento.media.upload` would lose which product it belongs to). |
| Verb placement | `magento.product.links.add`/`.remove`/`.bulk` versus `magento.product.links` | The bare form lists, the suffixed forms mutate. Consistent with `product.options` / `product.options.create`. |

A future major version may normalise these; until then this table is the contract.

## 3. Tool definition fields

| Field | Required | Rule enforced by the registry |
|---|---|---|
| `Name` | yes | §2 |
| `Title` | yes | 1–60 characters |
| `Description` | yes | 1–1024 characters, static |
| `InputSchema` | yes | JSON Schema object, `additionalProperties: false` |
| `OutputSchema` | inferred | `toolerr.Add` infers it from the handler's output type when not set |
| `Capability` | yes | Non-empty; it is metadata and is **not** used for gating |
| `Permission` | yes | `read`, `write`, `destructive`, or `admin` |
| `Timeout` | yes | > 0 and ≤ 120 s |
| `RequiresConfirmation` | write/destructive | Destructive tools must set it |
| `Annotations` | yes | `readOnlyHint` must match the permission; destructive tools must set `destructiveHint` |

A read tool must set `readOnlyHint`, must not be destructive, and must not require
confirmation. A destructive tool must require confirmation and set
`destructiveHint`. The registry rejects a tool that violates the contract at
registration time; `internal/mcp/registry/registry_test.go` and
`internal/magento/write_tools_test.go` pin it.

## 4. Input schemas

- Root type is `object`; `additionalProperties: false` at every level.
- Identifiers use `pattern` and length caps: Magento SKUs `[A-Za-z0-9._:-]`,
  attribute codes `[a-z0-9_]`, instance ids
  `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`.
- Dates are RFC 3339 or `YYYY-MM-DD`; date-only windows are converted and the
  response states the time zone where it matters.
- Pagination is `{ "cursor": "<offset>", "limit": N }` for Magento list tools.
  The cursor is a zero-based row offset that must be a multiple of `limit`; the
  cap is per tool (for example 200 for product/order search, 100 for some reads).
- Enums cover every fixed choice; free-form strings are length-bounded.
- No field may contain a URL, SQL fragment, or GraphQL document.
- Defaults are expressed in the schema, not hidden in the handler.
- The request body cap is `server.max_body_bytes` (default 16 MiB, validated to
  admit a maximum import; a write-enabled deployment must use at least 12 MiB).
  The body is rejected with `413` above the cap.

## 5. Result shape

There is **no common `{data, meta}` envelope**. Each tool returns its own typed
JSON object. `internal/mcp/toolerr` puts that JSON in `structuredContent` and, for
backward compatibility, the same serialized JSON in a `TextContent` block. A
result is never silently truncated; the tool's output schema declares its fields.

There is no 8 KiB inline cap and no `resource_link` path. Large results are
returned inline up to the tool's own row caps; the resource-link design is
**PLANNED** (ADR-0017).

Some results carry explicit quality fields where they matter: GA4 reports include
`time_zone` and `currency`; Cloudflare security results include `sampled` and
`retention_days`; `magento.inventory.transfer` returns an outcome enum and
before/after quantities. There is no universal `meta.source` or
`meta.retrieved_at` field.

## 6. Errors

Invalid arguments are rejected before the handler runs: the SDK's schema check
and `toolerr`'s compiled validator both produce a tool error with
`invalid_argument` (the SDK may reject malformed JSON-RPC as a protocol error).
Execution failures are tool errors (`isError: true`) with a structured envelope:

```json
{
  "code": "operation_unknown",
  "message": "the mutation may have been applied; poll the operation",
  "details": { "operation_id": "..." },
  "retryable": true
}
```

`retryable` is present only when true; `op` and `details` are optional. The
closed code set is in [module-boundaries.md](module-boundaries.md) §4.2. The codes
that matter to agents:

- `confirmation_required` — create/approve the confirmation, then resubmit with
  `confirmation_id`.
- `operation_unknown` — the mutation may have been applied; **poll, never retry**.
- `idempotency_key_reuse` — the key was reused with different arguments.
- `unsupported_capability` — the instance cannot perform the operation.
- `rate_limited`, `upstream_timeout`, `upstream_unavailable` — upstream signals;
  check `retryable`.

## 7. Permissions and scopes

| Scope | Meaning | Shipped examples |
|---|---|---|
| `read` | No side effects | product/order reads, analytics reports |
| `write` | Creates or updates; requires confirmation | product create/update, imports, document creation |
| `destructive` | Deletes or overwrites; requires confirmation | product/category/option/media delete, order cancel, inventory transfer, source unlink |
| `admin` | Reserved for gateway/credential metadata | none of the shipped tools use it |

The required scope is read from the registry and enforced by RBAC
(`internal/mcp/server.authorizeCall`); an unregistered tool is denied before the
authorizer is consulted. The default policy grants `viewer`/`analyst` read,
`operator` write, and `admin` admin. A tool that is a write but is declared
`read` is a defect.

## 8. Confirmation

Every tool with `RequiresConfirmation` follows the two-step protocol:

1. The first call without `confirmation_id` validates the arguments against the
   schema, computes `sha256(canonical(args))`, and creates a pending
   `confirmation_tokens` row bound to the tool, the instance, and the hash.
2. The response is `confirmation_required` with an opaque id, a summary, and an
   expiry. The requester never receives an approval secret.
3. A different human approves it through `mage2mcp-admin` (which needs
   `database.url`). Self-approval is rejected; the TTL is 300 s by default, 900 s
   maximum.
4. The requester resubmits the same arguments plus `confirmation_id`. The gateway
   verifies state, hash, identity, tool, instance, and TTL, then consumes it in a
   compare-and-set. For gated writes the consume is atomic with the ledger row.

A confirmation for one tool or instance cannot be replayed against another. The
approval CLI's `-as` identity is self-asserted (SEC-04, accepted): the trail is
only as trustworthy as the operator and database access controls.

## 9. Idempotency, replay, and `operation_unknown`

Every write is gated by the operation ledger. The five self-ledgered money tools
(`magento.invoice.create`, `magento.invoice.capture`, `magento.creditmemo.create`,
`magento.order.create`, `magento.order.cancel`) accept an optional
`idempotency_key`; the middleware derives one for the other write tools when the
caller does not supply it.

- A repeat with the same key returns the recorded result with `replayed: true`
  and does not call upstream.
- A repeat with the same key and different arguments is
  `idempotency_key_reuse`.
- An ambiguous outcome (timeout, transport failure after send, 5xx) becomes
  `operation_unknown`; the operation is `unknown` in the ledger and is resolved by
  the reconciler. A retry while `unknown` is refused.

The ledger states and transitions are documented in
[database-design.md](database-design.md) §3.11 and analysed in the
[threat model](security/threat-model.md) §8.12.

## 10. Inventory and conditional surface

The generated inventory is [implemented-tools.md](implemented-tools.md). The
count is configuration-dependent:

| Configuration | Magento | Total |
|---|---:|---:|
| Read-only, no database, no bindings | 43 | 53 |
| Read-only, no database, with bindings | 43 | 54 |
| Writes, no database, no bindings | 95 | 105 |
| Writes, no database, with bindings | 95 | 106 |
| Read-only, database, no bindings | 46 | 56 |
| Writes + database, no bindings | 101 | 111 |
| Writes + database + bindings (maximum) | 101 | 112 |

The maximum also requires single-instance Cloudflare and GA4 modules (multi-instance
bindings are rejected; see ADR-0018).

Do not build a client from this page's prose; read the inventory for names,
scopes, timeouts, and confirmation flags.

## 11. Planned, not shipped

These are designed but **not registered**:

- `cloudflare.logs.*` and the R2/ClickHouse raw-log path.
- `cloudflare.zone.list`, `analytics.status_codes`, `analytics.top`,
  `analytics.cache`, `security.bots`.
- GA4 acquisition, landing-page, event, and funnel reports.
- Unified bot-vs-human, WAF impact, product funnel, channel revenue, geography,
  SKU performance, decline correlation, and SKU mapping.
- `platform.job.get` / `platform.job.cancel` (job inspection is per feature:
  `magento.import.status`, `.batches`, `.batch`, `.cancel`).
- `magento.configurable.verify_storefront` — not planned; the gateway is REST
  admin only (ADR-0022).
- Progress notifications, cancellation propagation, `resource_link`, and
  `notifications/tools/list_changed`.

## 12. Timeouts and long-running work

| Class | Timeout | Behaviour |
|---|---|---|
| Read | 30 s | Returns `upstream_timeout` |
| Write | 60 s | The ledger records the outcome; an ambiguous failure is `unknown` |
| Bulk write / analysis | 120 s | Per-item partial-success report |
| Import start / status / cancel | 30 s | Queues or reads a PostgreSQL job |
| Reconciliation | worker task | Minute cadence; 8 attempts then escalation |

The declared timeout is applied by the MCP middleware as a real deadline. A tool
that exceeds it returns `upstream_timeout`; for a write that becomes `unknown`,
not `failed`. Long work is a job, not an inline call. There is no MCP
`progressToken` or cancellation notification.

## 13. Versioning and deprecation

- Tool names do not carry versions.
- Additive changes (a new optional field, a new enum value) are minor releases.
- Removing or renaming a field, changing a type, or changing semantics is a
  breaking change: an ADR, a minor release with both fields, a deprecation note in
  the description/result, and removal no earlier than the next minor.
- Pre-1.0, a breaking change still requires an ADR and a changelog entry.
- A deprecated tool remains registered until removal; the deprecation is recorded
  in the changelog and release notes.

## 14. Description guidelines

Descriptions are model-facing interface text and a prompt-injection surface if
they ever contain upstream data. Rules:

- Static text only, reviewed like code.
- First sentence: what it does, the data source, and whether it reads or writes.
- State the most important limitation (sampling, freshness, missing ecommerce,
  confirmation, `operation_unknown`).
- Never include examples that look like instructions to the agent.
- Never promise a capability the instance may not have.
- Keep descriptions under 1,024 characters; put detail in the output schema and
  docs.

## 15. Related

- [implemented-tools.md](implemented-tools.md) — the generated inventory.
- [module-boundaries.md](module-boundaries.md) — error taxonomy and module rules.
- [database-design.md](database-design.md) — confirmation and ledger tables.
- [security/threat-model.md](security/threat-model.md) — replay and injection analysis.
- [mcp-conformance.md](mcp-conformance.md) — transport and protocol matrix.
