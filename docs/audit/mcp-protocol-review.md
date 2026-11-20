# MCP protocol and tool-surface review

| Field | Value |
|---|---|
| Document | MCP protocol and tool-surface review (`MCP-*` findings) |
| Owner | MCP engineer (Agent 4) |
| Audited commit | `66786a5d10c1f78d00e08a5b42d01fcc2da37a3f` (`main`, tag `v0.11.0`) |
| Audit date | 2026-11-20 |
| Working tree | dirty during the audit: other workstreams landed uncommitted remediation across `cmd/`, `internal/mcp/server`, `internal/security`, `internal/storage`, `internal/jobs`, and `internal/magento`. All evidence below is pinned to `66786a5`; §8 records the delta and does **not** count uncommitted work as closed. |
| Method | Registry-derived inventory harness, in-memory MCP client, call-level probes, source inspection, real test runs |
| Verification | `go test -count=1 ./internal/mcp/... ./internal/magento/...` → **pass** (see §7) |

---

## Executive summary

The MCP layer is structurally stronger than most of this repository's surface area:
the registry validates names, titles, descriptions, permissions, and timeouts; every
tool registers through one typed-error wrapper; every tool returns `structuredContent`
plus a text fallback; all 112 tools have a closed output schema; and every write tool
is confirmation-gated. Those are real controls, not documentation claims.

The defects are where the contract is declared and then not consumed:

1. **The declared permission is not the authorized permission (P0, duplicate of
   `SEC-01`/`ARCH-01`).** `Options.Authorize` has no scope argument and production
   passes the literal `rbac.ScopeRead`. All 112 tools are authorized at `read`; the
   55 write/destructive tools are reachable by a viewer. Confirmation still requires a
   second identity, so this is not an unauthenticated write path — but the scope
   ladder is decorative.
2. **The write gate is incomplete (P1).** With a database configured and
   `tools.writes: false`, `magento.import.start`, `.cancel`, and `.resume` are still
   registered as `write` tools (proved: 49 tools, 3 non-read). The README's "write
   tools stay disabled until `tools.writes: true`" is false for exactly the tools that
   can mutate the catalog asynchronously.
3. **The inventory is not trustworthy (P2).** `platform.info` is outside the registry;
   its `tools` array omits itself; `writes_enabled` is always `false` in production
   because `cmd/server` never sets `Options.WriteTools`; `instances` is always empty
   because `configuredInstances` is never wired; and `protocol_versions` hardcodes two
   of the five versions the SDK negotiates.
4. **Schema enforcement is not mechanical (P2).** The registry never compiles an
   input schema. `ga4.analytics.traffic` ships without `additionalProperties: false`
   and accepts unknown fields (probe: `bogus` reached the handler). 169 required
   property occurrences have no description; 23 required strings have no pattern/enum/length.
5. **The agent-facing output contract is thin (P2).** All 112 output schemas exist, but
   **0 of 1103 output properties have a description**. No result states the instance
   it operated on; money is `float64` in outputs while inputs are decimal strings;
   pagination is inconsistent; there is no result-size cap.
6. **Write-side safety disclosure is incomplete (P2).** 47 of 55 write descriptions
   contain no idempotency/retry statement; 5 do not mention confirmation at all; the
   three asynchronous import write tools are outside the write-description contract
   test entirely.
7. **The error envelope is not one envelope (P2).** Text content double-prefixes the
   code (`not_found: not_found: ...`), `message` embeds the code/op, middleware errors
   omit `retryable`/`op`, denied errors drop `details`, and untyped storage errors
   reach the agent verbatim (`storage: get job: ERROR: relation "jobs" does not exist
   (SQLSTATE 42P01)`).
8. **Discoverability is unresolved at 112 tools (P2).** No server `instructions`, no
   grouping metadata, one prompt, no resources, and several exact or near duplicates
   (`magento.order.summary` is byte-for-byte the same handler as
   `magento.sales.summary`). The three proposed workflow tools are assessed in §6;
   all three are recommended **against** for v0.11 in their proposed write form.

### Finding counts (audit commit)

| Severity | Count | Findings |
|---|---:|---|
| **P0** | 1 | `MCP-01` — same defect as `SEC-01`/`ARCH-01`; recorded here because the MCP contract owns the scope metadata, counted **once** in the consolidated index under `SEC-01` |
| **P1** | 1 | `MCP-03` |
| **P2** | 9 | `MCP-02`, `MCP-04`, `MCP-05`, `MCP-06`, `MCP-07`, `MCP-08`, `MCP-09`, `MCP-11`, `MCP-12` |
| **P3** | 5 | `MCP-10`, `MCP-13`, `MCP-14`, `MCP-15`, `MCP-16` |
| **Total** | **16** | |

**De-duplicated against the other domain reports:** 0 MCP-owned P0, 1 P1, 9 P2,
5 P3. `MCP-01` is `SEC-01`/`ARCH-01`; `MCP-03` corrects the security report's
precondition that writes only exist with `tools.writes: true`; `MCP-14` overlaps the
`DOC-*` inventory findings; `MCP-15` overlaps `SEC-10`/`DOC-*`.

---

## 1. Method and reproducible inventory

### 1.1 What was measured

A transient harness built the registry exactly as `cmd/server.registerModules` does
for the maximal configuration (Magento + writes + durable import queue + Cloudflare +
GA4 + unified), registered `platform.info` through `server.NewMCP`, connected an
in-memory MCP client, and read `tools/list`. It then printed each tool's declared
permission, timeout, and confirmation flag from `registry.Lookup`. The harness source
is in Appendix A; it is not committed.

```console
$ go run ./.audit-tmp/compact
registry=111 tools_list=112
cloudflare.analytics.traffic    read    60s    false
...
```

The full harness additionally dumped input/output schemas and made call-level probes
(§7.2). The exact command and its output are recorded there.

### 1.2 The README claim: verified, with a qualification

The README says "112 tools (101 Magento)" (`README.md:15`, `:36`). That is **true for
the maximal configuration**, and false as a description of a default deployment:

| Configuration | Registry tools | `tools/list` | Magento | Non-Magento |
|---|---:|---:|---:|---:|
| Read-only, all modules (the default; `tools.writes: false`, no database) | 53 | **54** | 43 | 10 + platform |
| Read-only + database queue (still `tools.writes: false`) | 59 | **60** | 49 | 10 + platform |
| Writes enabled, no database | 105 | **106** | 95 | 10 + platform |
| Full: writes + database + all modules | 111 | **112** | **101** | 10 + platform |

The conditional Magento tools are 52 write/destructive tools (`tools.writes: true`)
and 6 asynchronous import tools (`database.url` set); 43 Magento reads are always
present. `platform.info` is the 112th tool and is not in the registry.

Reproducible cross-checks:

```console
$ wc -l internal/magento/testdata/tool-names.txt
101 internal/magento/testdata/tool-names.txt
$ go test -count=1 -run TestMagentoToolContract -v ./internal/magento/
--- PASS: TestMagentoToolContract
$ go test -count=1 -run TestImplementedToolsDocsMatchRegistry -v ./internal/magento/
--- PASS: TestImplementedToolsDocsMatchRegistry
```

The Magento golden file pins 101 names. The other 11 tools are not pinned by any
golden file or test; `docs/implemented-tools.md` lists them but the test only compares
Magento names (`internal/magento/tool_contract_test.go:61-104`).

### 1.3 Verified breakdown

| Module | Tools | Read | Write | Destructive | Confirmation | Timeouts |
|---|---:|---:|---:|---:|---:|---|
| Platform | 1 | 1 (annotation only) | 0 | 0 | 0 | none |
| Magento 2 | 101 | 46 | 46 | 9 | 55 | 30s/60s/120s |
| Cloudflare | 3 | 3 | 0 | 0 | 0 | 60s |
| Google Analytics 4 | 6 | 6 | 0 | 0 | 0 | 30s/60s |
| Unified analytics | 1 | 1 | 0 | 0 | 0 | 60s |
| **Total** | **112** | **56 + platform** | **46** | **9** | **55** | 30s:44, 60s:60, 120s:7, none:1 |

Every write/destructive tool is `RequiresConfirmation: true`. No tool has an empty
title or description; all names are unique; all 112 have an object-root input schema, but one root is open
(MCP-04); all 112 have an output schema.

### 1.3.1 Namespace breakdown

| Module | Namespaces (tool count) |
|---|---|
| Magento (101) | `product` 22 + `products` 2, `inventory` 11, `order` 10 + `orders` 1, `customer` 9, `import` 8, `category` 7 + `categories` 1, `configurable` 7, `attribute` 1 + `attributes` 5, `invoice` 5, `creditmemo` 4, `shipment` 4, `sales` 2, `store` 1, `tax` 1 |
| Cloudflare (3) | `analytics` 1, `security` 2 |
| GA4 (6) | `analytics` 2, `ecommerce` 2, `property` 2 |
| Unified (1) | `traffic` 1 |
| Platform (1) | `info` 1 |

**Authorization metadata coverage:** a declared permission exists for 111 of 112
tools (`platform.info` has none because it bypasses the registry), but the declared
permission is **not** used for authorization: all 112 calls are authorized at
`read` (MCP-01). This is the direct answer to the "declared and actually enforced"
check: declared for 99%, enforced for 0%.

---

## 2. Verified tool inventory

Columns: the declared registry scope (`none` = not in the registry), whether the tool
mutates Magento, the declared timeout (metadata only — see MCP-05), and whether the
server requires confirmation. Read the timeout column with MCP-05 in mind.

### Platform (1 tools)

| Tool | Scope | Write? | Timeout | Confirmation |
|---|---|---|---|---|
| `platform.info` | **none** | no | **none** | no |

### Magento 2 (101 tools)

| Tool | Scope | Write? | Timeout | Confirmation |
|---|---|---|---|---|
| `magento.attribute.sets` | read | no | 30s | no |
| `magento.attributes.list` | read | no | 30s | no |
| `magento.attributes.options` | read | no | 30s | no |
| `magento.attributes.options.bulk_create` | write | yes | 120s | yes |
| `magento.attributes.options.create` | write | yes | 60s | yes |
| `magento.attributes.options.delete` | destructive | yes | 60s | yes |
| `magento.categories.flat` | read | no | 30s | no |
| `magento.category.assign_product` | write | yes | 60s | yes |
| `magento.category.create` | write | yes | 60s | yes |
| `magento.category.delete` | destructive | yes | 60s | yes |
| `magento.category.get` | read | no | 30s | no |
| `magento.category.products` | read | no | 30s | no |
| `magento.category.tree` | read | no | 30s | no |
| `magento.category.update` | write | yes | 60s | yes |
| `magento.configurable.add_child` | write | yes | 60s | yes |
| `magento.configurable.children` | read | no | 30s | no |
| `magento.configurable.create` | write | yes | 60s | yes |
| `magento.configurable.create_variations` | write | yes | 120s | yes |
| `magento.configurable.remove_child` | write | yes | 60s | yes |
| `magento.configurable.set_options` | write | yes | 120s | yes |
| `magento.configurable.update` | write | yes | 60s | yes |
| `magento.creditmemo.create` | write | yes | 60s | yes |
| `magento.creditmemo.email` | write | yes | 60s | yes |
| `magento.creditmemo.get` | read | no | 30s | no |
| `magento.creditmemo.list` | read | no | 30s | no |
| `magento.customer.addresses` | read | no | 30s | no |
| `magento.customer.addresses.create` | write | yes | 60s | yes |
| `magento.customer.addresses.delete` | destructive | yes | 60s | yes |
| `magento.customer.addresses.update` | write | yes | 60s | yes |
| `magento.customer.create` | write | yes | 60s | yes |
| `magento.customer.get` | read | no | 30s | no |
| `magento.customer.groups` | read | no | 30s | no |
| `magento.customer.search` | read | no | 30s | no |
| `magento.customer.update` | write | yes | 60s | yes |
| `magento.import.batch` | read | no | 30s | no |
| `magento.import.batches` | read | no | 30s | no |
| `magento.import.cancel` | write | yes | 60s | yes |
| `magento.import.execute` | write | yes | 60s | yes |
| `magento.import.resume` | write | yes | 60s | yes |
| `magento.import.start` | write | yes | 60s | yes |
| `magento.import.status` | read | no | 30s | no |
| `magento.import.validate` | read | no | 30s | no |
| `magento.inventory.assign` | write | yes | 60s | yes |
| `magento.inventory.source_create` | write | yes | 60s | yes |
| `magento.inventory.source_items` | read | no | 30s | no |
| `magento.inventory.sources` | read | no | 30s | no |
| `magento.inventory.status` | read | no | 30s | no |
| `magento.inventory.status_batch` | read | no | 60s | no |
| `magento.inventory.stock_source_link` | write | yes | 60s | yes |
| `magento.inventory.stock_source_unlink` | destructive | yes | 60s | yes |
| `magento.inventory.stocks` | read | no | 30s | no |
| `magento.inventory.transfer` | destructive | yes | 60s | yes |
| `magento.inventory.unassign` | write | yes | 60s | yes |
| `magento.invoice.capture` | write | yes | 60s | yes |
| `magento.invoice.create` | write | yes | 60s | yes |
| `magento.invoice.email` | write | yes | 60s | yes |
| `magento.invoice.get` | read | no | 30s | no |
| `magento.invoice.list` | read | no | 30s | no |
| `magento.order.cancel` | destructive | yes | 60s | yes |
| `magento.order.comment` | write | yes | 60s | yes |
| `magento.order.create` | write | yes | 60s | yes |
| `magento.order.email` | write | yes | 60s | yes |
| `magento.order.get` | read | no | 30s | no |
| `magento.order.hold` | write | yes | 60s | yes |
| `magento.order.search` | read | no | 30s | no |
| `magento.order.status_history` | read | no | 30s | no |
| `magento.order.summary` | read | no | 30s | no |
| `magento.order.unhold` | write | yes | 60s | yes |
| `magento.orders.geography` | read | no | 120s | no |
| `magento.product.create` | write | yes | 60s | yes |
| `magento.product.delete` | destructive | yes | 60s | yes |
| `magento.product.get` | read | no | 30s | no |
| `magento.product.links` | read | no | 30s | no |
| `magento.product.links.add` | write | yes | 60s | yes |
| `magento.product.links.bulk` | write | yes | 120s | yes |
| `magento.product.links.remove` | write | yes | 60s | yes |
| `magento.product.media` | read | no | 30s | no |
| `magento.product.media.delete` | destructive | yes | 60s | yes |
| `magento.product.media.get` | read | no | 30s | no |
| `magento.product.media.types` | read | no | 30s | no |
| `magento.product.media.update` | write | yes | 60s | yes |
| `magento.product.media.upload` | write | yes | 60s | yes |
| `magento.product.options` | read | no | 30s | no |
| `magento.product.options.create` | write | yes | 60s | yes |
| `magento.product.options.delete` | destructive | yes | 60s | yes |
| `magento.product.options.update` | write | yes | 60s | yes |
| `magento.product.search` | read | no | 30s | no |
| `magento.product.seo` | read | no | 30s | no |
| `magento.product.tier_prices` | read | no | 30s | no |
| `magento.product.tier_prices.set` | write | yes | 60s | yes |
| `magento.product.update` | write | yes | 60s | yes |
| `magento.products.bulk_update` | write | yes | 120s | yes |
| `magento.products.get` | read | no | 30s | no |
| `magento.sales.analysis` | read | no | 120s | no |
| `magento.sales.summary` | read | no | 30s | no |
| `magento.shipment.create` | write | yes | 60s | yes |
| `magento.shipment.email` | write | yes | 60s | yes |
| `magento.shipment.get` | read | no | 30s | no |
| `magento.shipment.list` | read | no | 30s | no |
| `magento.store.configs` | read | no | 30s | no |
| `magento.tax.classes` | read | no | 30s | no |

### Cloudflare (3 tools)

| Tool | Scope | Write? | Timeout | Confirmation |
|---|---|---|---|---|
| `cloudflare.analytics.traffic` | read | no | 60s | no |
| `cloudflare.security.events` | read | no | 60s | no |
| `cloudflare.security.summary` | read | no | 60s | no |

### Google Analytics 4 (6 tools)

| Tool | Scope | Write? | Timeout | Confirmation |
|---|---|---|---|---|
| `ga4.analytics.realtime` | read | no | 60s | no |
| `ga4.analytics.traffic` | read | no | 60s | no |
| `ga4.ecommerce.overview` | read | no | 60s | no |
| `ga4.ecommerce.products` | read | no | 60s | no |
| `ga4.property.capabilities` | read | no | 60s | no |
| `ga4.property.metadata` | read | no | 30s | no |

### Unified analytics (1 tools)

| Tool | Scope | Write? | Timeout | Confirmation |
|---|---|---|---|---|
| `unified.traffic.compare` | read | no | 60s | no |

---

## 3. Findings

### MCP-01: The declared tool permission is never used for authorization

- **Severity**: P0
- **State**: CONFIRMED at `66786a5`; an uncommitted candidate fix exists in the working
  tree (§8) and is **not** counted as closed here.
- **Location**: `internal/mcp/server/server.go:36` (the `Authorize` callback has no
  scope parameter), `:95` (call site); `cmd/server/main.go:236` (literal
  `rbac.ScopeRead`), `:101-104` (only `viewer`/`analyst` read grants);
  `internal/mcp/registry/registry.go:144-169` (permission validated but not consumed
  by the call path).
- **Evidence**: `installPolicy` calls `opts.Authorize(ctx, tool, instance)` with no
  required scope. The production closure hard-codes the weakest rung:
  `policy.Authorize(id, tool, instance, rbac.ScopeRead)`. `registry.Tool.Permission`
  is consumed by registry validation and by the confirmation record
  (`server.go:148`), never by authorization. A caller whose token carries `viewer`
  therefore passes every tool call at `read`, including `magento.product.delete`,
  `magento.invoice.capture`, `magento.order.create`, and `magento.import.start`.
- **Trigger**: authentication enabled, `tools.writes: true`, and any valid token with
  the `viewer` or `analyst` role — i.e. a normal read-only agent token.
- **Impact**: privilege escalation across the entire tool surface. The only remaining
  gate is confirmation by a second identity, whose own role is not checked
  (`SEC-04`). The `read < write < destructive < admin` ladder has no effect at the
  call site.
- **Proposed fix**: give `Authorize` a `requiredScope` parameter; resolve it from
  `registry.Lookup(tool).Permission` in `installPolicy`; fail closed for an unknown
  tool; ship a default policy that actually distinguishes the rungs.
- **Test requirement**: a `viewer` is denied `magento.invoice.capture` and allowed
  `magento.product.get`; an unregistered tool is denied without consulting the
  authorizer.
- **Verified by**: (pending independent review)
- **Cross-ref**: `SEC-01`, `ARCH-01`. Counted once in the consolidated index.

### MCP-02: `platform.info` is not a trustworthy inventory

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:63` (registered outside the loop),
  `:197-218` (definition; no `Permission`, `Capability`, or `Timeout`), `:205-209`
  (`tools` is registry names only), `:213` (hard-coded protocol versions), `:216`
  (`WritesEnabled: opts.WriteTools`); `cmd/server/main.go:108-126` (options never set
  `WriteTools` or `Instances`), `:194` (`configuredInstances` exists but is unused).
- **Evidence**: the maximal production configuration (writes + database) registers 101
  registry tools (55 write) plus `platform.info`. A probe using the production options returned:
  `registry_total=101 registry_write_tools=55 platform_tools_listed=101
  platform_writes_enabled=false platform_instances=map[] protocols=[2025-11-25
  2026-07-28]`. `tools/list` returns 102 tools; `platform.info.tools` returns 101 and
  omits `platform.info` itself. `configuredInstances` is referenced only by
  `cmd/server/main_test.go`.
- **Trigger**: any client calls `platform.info` (agents are expected to call it first).
- **Impact**: an agent is told writes are disabled while 55 write tools are callable;
  a multi-instance operator sees no instances; the tool index omits the tool that
  produced it. Inventory truth is exactly what this audit was asked to establish, and
  the self-description contradicts `tools/list`.
- **Proposed fix**: register `platform.info` through `registry` (or a single catalog
  builder); pass `WriteTools` and `Instances` from `cmd/server`; include the tool
  itself; derive `protocol_versions` from `mcp.SupportedProtocolVersions()`.
- **Test requirement**: `platform.info.tools` equals the `tools/list` names;
  `writes_enabled` reflects registration; `instances` matches configuration;
  `protocol_versions` equals the SDK list.
- **Verified by**: (pending independent review)
- **Cross-ref**: `SEC-16` (registry bypass), `docs/mcp-conformance.md` P1.

### MCP-03: Write tools are registered when `tools.writes` is false

- **Severity**: P1
- **State**: CONFIRMED
- **Location**: `cmd/server/main.go:272-281` (`RegisterWritesWithResolver` is gated by
  `cfg.Tools.Writes`, `RegisterImportJobs` is gated only by `importQueue != nil`);
  `internal/magento/import_jobs.go:115`, `:173`, `:244` (`import.start`, `.cancel`,
  `.resume` use `writeToolMetadata` → `PermissionWrite` + confirmation).
- **Evidence**: a probe registered the read tools plus the import-job tools, with
  writes disabled, and got `reads_plus_importjobs_total=49 non_read=3`:
  `magento.import.cancel (write)`, `magento.import.resume (write)`,
  `magento.import.start (write)`. The same three tools are the only path that can
  enqueue a catalog-mutating import. The README states "Write tools stay disabled
  until `tools.writes: true`" (`README.md:176`); the security report repeats that
  precondition (`security-audit-report.md:71`).
- **Trigger**: `database.url` configured (the documented production setup for audit and
  approvals) and `tools.writes: false` (the default, `config.Default()`).
- **Impact**: a deployment that deliberately disabled writes exposes three write
  tools, one of which queues an import that the worker executes against Magento.
  Confirmation still applies, but the documented write gate does not. Combined with
  MCP-01, a viewer can initiate the write and MCP-02 reports writes as disabled.
- **Proposed fix**: gate the three import write tools on `cfg.Tools.Writes`; split
  `RegisterImportJobs` into a read half and a write half (or take a `writes bool`);
  add the gate to the startup log/`platform.info`.
- **Test requirement**: a config with a database queue and `tools.writes: false`
  registers zero tools whose `Permission != read`.
- **Verified by**: (pending independent review)
- **Cross-ref**: corrects the precondition in `SEC-01`/`security-audit-report.md:71`.

### MCP-04: The registry does not enforce the input/output schema contract

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/registry/registry.go:127-171` (`Validate` checks name,
  title, description, capability, timeout, permission, and annotations; it never
  parses or inspects `InputSchema`/`OutputSchema`); `internal/mcp/toolerr/toolerr.go:22-31`
  (schemas are inferred only when nil), `:68-111` (`compile` silently returns `nil` on
  a parse/resolve error, disabling validation).
- **Evidence**: 111 of 112 input schemas have `additionalProperties: false` at the
  root and at every nested object. `ga4.analytics.traffic` does not
  (`internal/ga4/tools.go:115-151`); a probe sent `{"instance":"ga","metrics":
  ["not_a_metric"],"bogus":"accepted?"}` and reached the handler
  (`validation_failed` on the metric, not `invalid_argument` on `bogus`). The design
  catalog requires `additionalProperties: false` at every level
  (`docs/mcp-tools.md:23`, §4) and the conformance matrix marks it "Designed"
  (`docs/mcp-conformance.md:23`), not enforced.
- **Trigger**: a client sends an unknown field to `ga4.analytics.traffic`; or a future
  tool ships an open/malformed schema.
- **Impact**: the "every tool is closed" claim is false for one tool today and is not
  mechanically protected. Unknown arguments are silently ignored, which hides caller
  mistakes; a malformed schema disables validation entirely instead of failing
  startup. Agents cannot rely on strict rejection.
- **Proposed fix**: compile and validate every schema in `Registry.Register`; require
  an object root, `additionalProperties: false` on every object node, and a non-nil
  output schema; fail startup on a parse error rather than returning `nil`.
- **Test requirement**: registry rejects an open schema and a malformed schema;
  `ga4.analytics.traffic` rejects an unknown field.
- **Verified by**: (pending independent review)

### MCP-05: The declared timeout is dead metadata

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/registry/registry.go:139` (timeout is only range-checked);
  no consumer exists (`grep -rn '\.Timeout' internal cmd` finds only client
  configuration and the registry validation). `internal/magento/client.go:80-82` and
  `internal/ga4/client.go:94-96` default to 30s per upstream request;
  `internal/cloudflare/tools.go:191,213,246` set their own 60s handler contexts.
- **Evidence**: 60 tools advertise 60s and 7 advertise 120s, but the registry never
  turns `registry.Tool.Timeout` into a context deadline. A 120s tool such as
  `magento.sales.analysis` or `magento.products.bulk_update` is bounded only by the
  30s per-request client timeout; a multi-request loop can run far longer than the
  advertised 120s. Cloudflare is the only module that enforces a handler deadline, and
  it duplicates the constant rather than reading the registry.
- **Trigger**: a slow Magento/GA4 call or a bulk tool that makes one request per item.
- **Impact**: the timeout column in the inventory (and in `docs/implemented-tools.md`)
  is not a bound. Agents and operators cannot reason about worst-case latency; a
  write can be cut at 30s while its tool description promises 120s, and a bulk tool
  can exceed its declared budget without being stopped.
- **Proposed fix**: derive a context deadline from `registry.Tool.Timeout` in
  `installPolicy` (or a shared tool wrapper), keep the per-request client timeout
  below it, and remove the ad-hoc Cloudflare constant.
- **Test requirement**: a tool declaring 30s receives a 30s deadline; a tool declaring
  120s receives 120s; the deadline is observable from the handler.
- **Verified by**: (pending independent review)

### MCP-06: Write descriptions do not disclose idempotency, mutation, or confirmation consistently

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/write_tools.go:375-384` (write metadata),
  `:43-70` (the only descriptions with an idempotency sentence);
  `internal/magento/import_jobs.go:195-229` (import write descriptions);
  `internal/magento/write_tools_test.go:106-110` (description contract applies only to
  `RegisterWrites`).
- **Evidence**: 55 write/destructive tools. Only 5 descriptions use the word
  "idempotent" (`configurable.add_child`, `configurable.create_variations`,
  `inventory.assign`, `product.create`, `product.update`); 8 disclose any
  retry/duplicate behaviour; **47 of 55 have no idempotency/retry statement at all**.
  Five write descriptions do not mention confirmation even though the server requires
  it (`configurable.add_child`, `configurable.create`, `inventory.assign`,
  `product.create`, `product.update`). The three asynchronous import write tools are
  registered outside `RegisterWrites` and therefore outside its contract test; their
  descriptions omit "Magento" and the instance-resolution sentence, and
  `import.cancel`/`import.resume` omit the write-operation sentence. The objective
  requires every write tool to state whether it is idempotent and what it mutates.
- **Trigger**: an agent retries after a timeout, a partial failure, or a 5xx — exactly
  the cases where the error taxonomy says "retryable" but the tool description says
  nothing.
- **Impact**: duplicate orders, invoices, captures, shipments, credit memos, links, or
  imports are plausible. The confirmation gate forces a new approval, which mitigates
  the worst case, but the agent has no guidance and the description is the only
  interface text it sees before acting.
- **Proposed fix**: require an idempotency sentence and a mutation sentence in every
  write description; apply the write-description test to `RegisterImportJobs`; set
  `idempotentHint` per tool rather than uniformly `false`.
- **Test requirement**: a table-driven test asserts every write description contains
  an idempotency statement, a mutation statement, and a confirmation statement.
- **Verified by**: (pending independent review)

### MCP-07: Input-schema quality is inconsistent and sometimes ambiguous

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: representative examples — `internal/magento/write_tools.go:602-603`
  (`product.delete` schema), `:666-676` (`removeChild`/`unassign`),
  `internal/magento/tools.go:291-295` (`salesSummarySchema`),
  `internal/ga4/extra_tools.go:246` (`limit` tag), `internal/unified/tools.go:24-28`
  (`compareInput`), `internal/magento/tools.go:1980` (`categories.flat` reuses
  `storeConfigsSchema`), `:1788` (`tax.classes` reuses `customerGroupsSchema`).
- **Evidence**:
  - **169 required property occurrences have no description**; 270 of 587 property occurrences
    are undocumented; 70 of 112 tools have at least one undocumented property.
    `magento.product.create` leaves required `attribute_set_id`, `name`, and `type_id`
    undocumented; `magento.order.create` leaves the entire billing/shipping address
    undocumented; `magento.inventory.transfer` leaves `quantity`, `from_source`, and
    `to_source` undocumented.
  - **Unit/type ambiguity**: `qty` (number) is used by order/invoice/shipment/
    creditmemo items, while `quantity` (integer) is used by inventory/product/tier
    prices; neither consistently states the unit. `price` is a decimal **string** in
    product create/update but a **number** in tier prices and custom options. Outputs
    use `float64` for money (`internal/magento/product.go:39`,
    `internal/magento/order.go:61-66`) while inputs use strings; product outputs carry
    no currency at all.
  - **23 required strings have no pattern/enum/maxLength/format**: all six GA4
    `instance` fields, Cloudflare `since`/`until`, Magento `sales.*`/`orders.geography`
    `from`/`to`, and unified `binding`/`start`/`end`. GA4 ecommerce `limit` is
    documented "1-200" but has no `minimum`/`maximum`; `unified.traffic.compare` has
    no property descriptions and no RFC 3339 pattern (the handler validates later).
  - **Four import tools lack `instance`** (`import.status`, `.cancel`, `.resume`,
    `.batch`), so they cannot be instance-scoped (cross-ref `SEC-02`/`ARCH-03`).
- **Trigger**: an agent constructs a write or a paginated read from the schema alone.
- **Impact**: wrong quantity/price units, failed validation round trips, silent
  defaults, and cross-tenant job access. The schema is the only contract the model
  sees; missing descriptions are missing instructions.
- **Proposed fix**: add a schema lint that requires a description on every required
  field and a pattern/enum/maxLength on every identifier; introduce shared
  `money`/`quantity`/`instance`/`timestamp` definitions with explicit units and
  currency; add `instance` to the four job tools; stop sharing schema constants
  between unrelated tools.
- **Test requirement**: a schema-lint test over all 112 tools; per-tool golden schema
  snapshots.
- **Verified by**: (pending independent review)

### MCP-08: Output schemas exist but are not self-describing, and results have no common envelope

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/toolerr/toolerr.go:27-31` (output schema inferred from
  the Go type), `:56-63` (structured content plus JSON text); representative outputs
  `internal/magento/tools.go:63-65` (`productGetOutput`), `:81-84`
  (`productSearchOutput`); `internal/magento/product.go:31-55` (no currency).
- **Evidence**: all 112 tools have an output schema (all roots
  `additionalProperties: false`), but **0 of 1103 output properties carry a
  description**. No Magento output states the `instance` it operated on, a request id,
  a time range, or a quality/truncation field; only the analytics tools carry
  `time_zone`/`currency`/`quality`. List shape is inconsistent: only
  `product.search`, `category.products`, `customer.search`, and `order.search` return
  `total`; `products.get`, `product.media`, `inventory.sources`, `import.batches`, and
  `customer.addresses` return a bare array with no total or truncation flag. There is
  no result-size cap or `resource_link`; `import.batch` returns every failed row and
  `product.search` can return 200 full products. Success text is serialized JSON, but
  error text is prose (`code: message`), so the text fallback is not uniform.
- **Trigger**: an agent interprets `grand_total`, `qty`, `sampled`, `quality`,
  `total_available`, or a list without a `total`; or a large result fills the context.
- **Impact**: misinterpretation of money/quantity fields; no way to verify which
  instance a write hit; context blowups on large imports/searches; clients cannot
  uniformly parse the text fallback.
- **Proposed fix**: add `jsonschema` descriptions to output structs; introduce a common
  envelope `{instance, data, meta:{truncated,total,quality}}` (or document the
  per-tool shape); cap inline results and return `resource_link`/`truncated`; return
  the JSON envelope as text on errors too.
- **Test requirement**: output-schema description coverage; a result-size test; a
  text-fallback test that parses both success and error content as JSON.
- **Verified by**: (pending independent review)

### MCP-09: One logical error, several envelope shapes, and untyped errors leak internals

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/toolerr/toolerr.go:113-139`;
  `internal/mcp/server/server.go:177-195`; `internal/core/errors/errors.go:93-101`;
  `internal/storage/jobqueue.go:232`; `internal/ga4/report.go:114`.
- **Evidence**:
  - `errs.Error.Error()` already formats `code: message` (or `op: code: message`).
    `toolerr.errorResult` sets `message = err.Error()` and text
    `code + ": " + message`, so the text is `not_found: not_found: unknown Magento
    instance` (probe). `server.toolErrorResult`/`deniedResult` do the same.
  - Handler errors include `retryable` (when true), `op`, and `details`; middleware
    `confirmation_required` errors include `details` but not `retryable`/`op`;
    `deniedResult` includes only `code` and `message` and drops `details` (including
    the denial reason).
  - Untyped storage errors reach the agent with database internals: a probe returned
    `{"code":"internal","message":"storage: get job: ERROR: relation \"jobs\" does not
    exist (SQLSTATE 42P01)"}`. GA4's metric-parse failure is also untyped
    (`fmt.Errorf` at `ga4/report.go:114`).
  - The conformance matrix says invalid arguments are protocol errors
    (`docs/mcp-conformance.md:28` T7); in code they are tool errors with
    `isError: true` and `code: invalid_argument` (probe). The doc and code disagree.
- **Trigger**: any tool error, especially a storage failure or a confirmation/denial.
- **Impact**: agents that branch on text see duplicated codes; retryability is
  inconsistent across error sources; database internals and SQLSTATE leak; the
  documented protocol-error/tool-error split is not what the server does.
- **Proposed fix**: one envelope builder for handler, middleware, and denial errors;
  `message` without the code/op prefix; always include `code`, `message`, `details`,
  `retryable`, and `op` (empty when unset); map storage errors to
  `upstream_unavailable`/`internal` with safe text; fix or reclassify T7.
- **Test requirement**: an envelope-shape table covering handler, middleware,
  denial, and storage errors; a test asserting no SQLSTATE/relation name appears in
  any tool result.
- **Verified by**: (pending independent review)
- **Cross-ref**: `SEC-10` covers the upstream `upstream_message`/path leak; this
  finding is the envelope shape and the untyped-error path.

### MCP-10: Tool naming is internally inconsistent and violates the documented grammar

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/tools.go:435-437` (`attribute.sets` vs
  `attributes.list`), `:1933` (`categories.flat`), `:1273` (`products.get`), `:1808`
  (`orders.geography`); `internal/magento/write_tools.go:283-299` (mixed verb
  placement).
- **Evidence**:
  - **Singular/plural namespace splits**: `magento.attribute.sets` (1) vs
    `magento.attributes.*` (5); `magento.category.*` (7) vs `magento.categories.flat`
    (1); `magento.product.*` (22) vs `magento.products.get`/`.bulk_update` (2);
    `magento.order.*` (10) vs `magento.orders.geography` (1).
  - **18 four-segment names** violate the documented
    `<module>.<domain>.<action>` grammar (`docs/mcp-tools.md:34-44`):
    `magento.attributes.options.{create,bulk_create,delete}`,
    `magento.customer.addresses.{create,update,delete}`,
    `magento.product.links.{add,remove,bulk}`,
    `magento.product.media.{get,update,upload,delete,types}`,
    `magento.product.options.{create,update,delete}`,
    `magento.product.tier_prices.set`.
  - **27 noun-final names** have no action verb: `attribute.sets`,
    `attributes.options`, `categories.flat`, `category.tree`, `category.products`,
    `configurable.children`, `customer.addresses`, `customer.groups`, `import.batch`,
    `import.batches`, `import.status`, `inventory.source_items`, `inventory.sources`,
    `inventory.status`, `inventory.status_batch`, `inventory.stocks`,
    `order.status_history`, `orders.geography`, `product.links`, `product.media`,
    `product.media.types`, `product.options`, `product.seo`, `product.tier_prices`,
    `sales.analysis`, `store.configs`, `tax.classes`.
  - **Collection naming is mixed**: `.list` is used by `attributes.list`,
    `creditmemo.list`, `invoice.list`, `shipment.list`; bare nouns are used by
    `attribute.sets`, `attributes.options`, `customer.addresses`, `customer.groups`,
    `inventory.sources`, `inventory.stocks`, `product.media`, `product.options`,
    `store.configs`, `tax.classes`, `import.batches`.
  - **Verb placement is mixed**: `magento.inventory.source_create` vs
    `magento.product.create`; `magento.inventory.stock_source_link` vs
    `magento.product.links.add`; `magento.attributes.options.bulk_create` vs
    `magento.product.links.bulk`.
- **Trigger**: an agent or operator searches the tool list for a resource or action.
- **Impact**: the 112-tool surface is harder to search; `attribute.sets` is invisible
  to a search for "attributes"; the design catalog's naming grammar is not the
  registry's grammar.
- **Proposed fix**: do not mass-rename released tools. Add a documented exception
  list, additive aliases with the §9 deprecation note, and generate names from a
  single table so future names follow one grammar.
- **Test requirement**: a naming-lint test that enforces the chosen grammar and the
  documented exception list.
- **Verified by**: (pending independent review)

### MCP-11: Exact and near-duplicate tools inflate the surface

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/magento/tools.go:257,268-271,391-417` (`sales.summary`),
  `:1609-1645` (`order.summary`); `internal/magento/write_tools.go:283`
  (`import.execute`), `:289-299` (single vs bulk link/option/update tools);
  `internal/magento/import_jobs.go:115` (`import.start`).
- **Evidence**:
  - **Exact duplicate**: `magento.order.summary` and `magento.sales.summary` use the
    same `salesSummarySchema`, the same `salesSummaryOutput`, and the same
    `svc.SalesSummary` handler; the `order.summary` description says "This is the
    design-catalog name for the same data as magento.sales.summary; both stay
    available" (`tools.go:1612-1615`). Neither is marked deprecated under the
    documented §9 mechanism.
  - **Bulk supersets**: `magento.product.links.bulk` (`action: add|remove`,
    `linked_skus` 1-50) subsumes `magento.product.links.add`/`.remove`;
    `magento.attributes.options.bulk_create` (1-50 labels) subsumes
    `.options.create`; `magento.products.bulk_update` (1-50 SKUs) subsumes
    `magento.product.update` for status/visibility/attributes;
    `magento.inventory.status_batch` (1-25 SKUs) subsumes `magento.inventory.status`.
  - **Same data, two shapes**: `magento.category.tree` (nested) and
    `magento.categories.flat` (flattened) both read `GET /V1/categories`.
  - **Sync vs async import**: `magento.import.execute` and `magento.import.start`
    both execute a bounded import; one blocks, one queues. The descriptions do not
    say which to choose.
  - **Aggregate overlap**: `magento.sales.analysis` already returns order count,
    revenue, `items_qty`, AOV, and a daily trend, which are the entire outputs of
    `magento.sales.summary`/`order.summary`; `magento.orders.geography` adds only a
    country breakdown.
- **Trigger**: an agent chooses a tool from the list.
- **Impact**: the advertised surface is larger than the distinct capability set;
  agents pick a redundant tool, and maintainers must keep duplicate implementations
  in sync. `magento.order.summary` is the clearest example: two names, one handler.
- **Proposed fix**: apply the documented §9 deprecation path (`meta.quality.notes:
  ["deprecated: use <new tool>"]`) to `order.summary` and the single-item bulk
  subsets; document the sync/async import choice and the tree/flat choice in the
  descriptions; remove aliases no earlier than the next minor.
- **Test requirement**: a duplicate-detection test that fails when two tools share a
  handler/schema/output without a deprecation note.
- **Verified by**: (pending independent review)

### MCP-12: 112 tools have no server-level selection guidance

- **Severity**: P2
- **State**: CONFIRMED
- **Location**: `internal/mcp/server/server.go:62` (`mcp.NewServer(..., nil)` — no
  `Instructions`), `:269-288` (the only prompt, `write_safety`); no `AddResource`
  calls anywhere in `internal/`.
- **Evidence**: the server exposes 112 tools (55 write/destructive), one prompt, and
  no resources or `Instructions` field. There is no grouping metadata (`_meta`), no
  module-scoped tool set, and no workflow tool. `platform.info` is the only index, and
  it is wrong (MCP-02). Naming and duplicates (MCP-10/11) make selection harder.
- **Trigger**: an agent receives `tools/list` and must choose among 112 names.
- **Impact**: wrong tool selection, wasted context on near-duplicates, and writes
  attempted with the wrong intent. At this size, descriptions alone are not a
  selection strategy.
- **Proposed fix**: add `mcp.ServerOptions.Instructions` with a short module map and a
  "choose this, not that" table; add prompts for the common multi-step workflows
  (order fulfillment, configurable creation, inventory sync); consider module-scoped
  tool sets; fix `platform.info`.
- **Test requirement**: `instructions` is present and non-empty; the prompt list is
  tested; a selection smoke test (scripted agent task) picks the expected tool for
  the three workflows in §6.
- **Verified by**: (pending independent review)

### MCP-13: Capability metadata is inaccurate and unconsumed

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/tools.go:123-128` (`toolMetadata` defaults every read
  tool to `magento.product.read`), with per-tool overrides throughout the file
  (e.g. `:512`, `:802`, `:1049`, `:1837`); `internal/mcp/registry/registry.go:137`
  (only non-empty is checked).
- **Evidence**: 19 tools have a capability whose first two segments do not match the
  tool namespace; the substantive mislabels are `magento.order.get`,
  `magento.order.search`, `magento.order.summary`, `magento.sales.summary`, and
  `magento.tax.classes` → `magento.product.read`; `magento.creditmemo.email`,
  `magento.invoice.email`, `magento.shipment.email` → `magento.order.write`;
  `magento.configurable.remove_child` → `magento.product.write`. `Capability` is never
  read by authorization, discovery, or metrics (`grep` finds only registry validation
  and the declarations).
- **Trigger**: a future capability-based policy, UI grouping, or docs generation that
  trusts the field.
- **Impact**: misleading metadata; if the capability model is ever consumed, it would
  grant or deny the wrong tools. The docs describe a capability model the code does
  not use.
- **Proposed fix**: derive the capability from the namespace/table, or remove the
  field until it has a consumer; add a test that capability and namespace agree.
- **Test requirement**: capability/namespace consistency test.
- **Verified by**: (pending independent review)

### MCP-14: The inventory documentation drifts from the registry, and the checker validates docs against docs

- **Severity**: P3
- **State**: DOC-ONLY (the code is the authority; the documents are wrong)
- **Location**: `docs/implemented-tools.md:10-16` (universal claims), `:22`
  (`platform.info` 30s/read), `:158-162` ("Not implemented yet");
  `docs/mcp-tools.md:227-228` vs `:445` (`platform.job.get`/`.cancel` defined and
  "not planned" in the same document); `docs/mcp-conformance.md:28` (T7), `:63` (P1);
  `scripts/check-contracts.mjs:103-105` (requires `platform.job.get` when
  `import.start` is in the docs).
- **Evidence**:
  - The name list in `docs/implemented-tools.md` is accurate (112 rows, no missing or
    extra names), but the metadata is not: `platform.info` is documented `read`/`30s`
    though it has no registry scope and no timeout; `import.start`, `.cancel`, and
    `.resume` are documented `30s` but are registered with `60s` (the
    `writeToolMetadata` default). Diff of the table against the registry: 5
    mismatches, 0 name mismatches.
  - `docs/implemented-tools.md:10-16` claims every tool is `additionalProperties:
    false` (false for `ga4.analytics.traffic`), every call is authorized at its
    declared scope (false, MCP-01), and `instance` does not select credentials (false;
    `RegisterWithResolver`/`RegisterWritesWithResolver` route reads and writes).
  - The "Not implemented yet" section lists customer addresses, order creation,
    invoice capture, category/attribute tools, and GA4 ecommerce reports — all are
    registered. This is a direct contradiction of the table above it.
  - `docs/mcp-tools.md` defines `platform.job.get`/`.cancel` in §7.5a and calls them
    "Not planned" in the same document; the contract checker passes because the
    former exists in the docs, while no Go code registers either tool.
  - `docs/mcp-conformance.md:63` says "the server does not hardcode a version"; it
    does (`server.go:213`). `:28` says invalid arguments are protocol errors; the
    code returns tool errors (MCP-09).
  - The docs test (`tool_contract_test.go:61-104`) compares only Magento names; it
    does not check scope, timeout, confirmation, or the Cloudflare/GA4/unified names.
- **Trigger**: an operator or agent reads the inventory before enabling writes.
- **Impact**: the documents that are supposed to be the inventory of record are wrong
  in the direction that matters (write metadata, implementation status, protocol
  behavior); the checker gives false assurance because it validates the design doc
  against itself.
- **Proposed fix**: generate the inventory table from the registry in CI (name,
  module, scope, write, timeout, confirmation, description hash); extend the docs
  test to all modules and metadata; delete the stale "Not implemented yet" section;
  reconcile `platform.job.*` (implement or remove); fix T7/P1.
- **Test requirement**: a generated-inventory check that fails on any drift; a
  docs-only check for the removed claims.
- **Verified by**: (pending independent review)
- **Cross-ref**: `DOC-*`, `SEC-16`.

### MCP-15: No provenance envelope; upstream text reaches the agent unmarked

- **Severity**: P3
- **State**: CONFIRMED (code gap); the threat model's claim is DOC-ONLY
- **Location**: `docs/security/threat-model.md:257` (cites `internal/redact/untrusted.go`,
  `internal/mcp/envelope.go`, `internal/registry/descriptions.go` — none exist);
  `internal/magento/client.go:183-190` (`upstream_message` detail);
  `internal/mcp/toolerr/toolerr.go:56-63` (raw structured content).
- **Evidence**: no tool result carries a `provenance`/`trust` marker or a constant
  untrusted-content notice. Magento's `upstream_message` (bounded, control-stripped,
  300 runes) and GA4's error text are returned in `details`, so upstream text reaches
  the model with the same authority as gateway-authored text. The threat model lists
  ingress neutralisation, provenance tagging, and a redaction package as mitigations;
  `grep` finds none of the three packages, and no Go file contains "provenance".
- **Trigger**: an upstream validation message, product name, or error string contains
  instructions; the agent reads the tool result.
- **Impact**: prompt injection from upstream text is not structurally separated from
  instructions; the document claims a control that does not exist, so a reviewer may
  accept the risk as mitigated.
- **Proposed fix**: implement the provenance envelope or remove the claim; mark
  upstream-derived strings as untrusted data and cap/neutralise them; add a test that
  a hostile upstream message is returned as data, not as an instruction.
- **Test requirement**: a fake upstream returns `IGNORE PREVIOUS INSTRUCTIONS`; the
  tool result marks it untrusted and no control flow changes.
- **Verified by**: (pending independent review)
- **Cross-ref**: `SEC-10` (upstream text leak), `DOC-*`.

### MCP-16: Annotation hints are inaccurate or missing

- **Severity**: P3
- **State**: CONFIRMED
- **Location**: `internal/magento/tools.go:127` (`ReadOnlyHint: true,
  IdempotentHint: true`, no `OpenWorldHint`); `internal/ga4/tools.go:54,61` and
  `internal/ga4/extra_tools.go:63,72,211,259` (read-only, no `IdempotentHint`);
  `internal/mcp/registry/registry.go:144-169` (only read/destructive consistency is
  checked).
- **Evidence**: 101 Magento tools leave `openWorldHint` nil, which the MCP
  specification interprets as **true**; Magento is a closed, configured instance, so
  the accurate value is `false`. The six GA4 read tools leave `idempotentHint` false;
  the other 50 read tools set it true. `platform.info` is read-only but has
  `idempotentHint` false. All 55 write tools set `idempotentHint` false, including
  set-semantics writes (`product.tier_prices.set`, `inventory.assign`,
  `product.update`) that are idempotent at the HTTP level. The registry does not
  validate `openWorldHint` or `idempotentHint` at all.
- **Trigger**: a client uses annotations to decide whether to prompt, cache, or retry.
- **Impact**: clients may treat every Magento read as an open-world call (extra
  prompting) or retry an actually-idempotent write unnecessarily; the annotation
  matrix in `docs/mcp-conformance.md:27` is not enforced.
- **Proposed fix**: set `OpenWorldHint=false` for Magento/Cloudflare/unified,
  `IdempotentHint=true` for all reads, and per-tool hints for writes; validate the
  hints in `registry.Validate` against the declared permission.
- **Test requirement**: an annotation matrix test comparing scope, read-only,
  destructive, idempotent, and open-world hints per tool.
- **Verified by**: (pending independent review)

---

## 4. Naming-inconsistency register

This is the complete list of inconsistencies observed in the 101 Magento names. The
other modules are consistent (`cloudflare.<domain>.<action>`,
`ga4.<domain>.<action>`, `unified.<domain>.<action>`).

### 4.1 Singular vs plural namespaces

| Resource | Singular namespace | Plural namespace |
|---|---|---|
| attribute | `magento.attribute.sets` | `magento.attributes.list`, `.options`, `.options.create`, `.options.bulk_create`, `.options.delete` |
| category | `magento.category.{get,tree,products,create,update,delete,assign_product}` | `magento.categories.flat` |
| product | `magento.product.{get,search,create,update,delete,links*,media*,options*,seo,tier_prices*}` | `magento.products.get`, `magento.products.bulk_update` |
| order | `magento.order.{get,search,summary,status_history,create,cancel,comment,email,hold,unhold}` | `magento.orders.geography` |

### 4.2 Names with no action segment (noun-final)

`magento.attribute.sets`, `magento.attributes.options`, `magento.categories.flat`,
`magento.category.tree`, `magento.category.products`, `magento.configurable.children`,
`magento.customer.addresses`, `magento.customer.groups`, `magento.import.batch`,
`magento.import.batches`, `magento.import.status`, `magento.inventory.source_items`,
`magento.inventory.sources`, `magento.inventory.status`,
`magento.inventory.status_batch`, `magento.inventory.stocks`,
`magento.order.status_history`, `magento.orders.geography`, `magento.product.links`,
`magento.product.media`, `magento.product.media.types`, `magento.product.options`,
`magento.product.seo`, `magento.product.tier_prices`, `magento.sales.analysis`,
`magento.store.configs`, `magento.tax.classes`.

### 4.3 Four-segment names (violate `<module>.<domain>.<action>`)

`magento.attributes.options.{create,bulk_create,delete}`,
`magento.customer.addresses.{create,update,delete}`,
`magento.product.links.{add,remove,bulk}`,
`magento.product.media.{get,update,upload,delete,types}`,
`magento.product.options.{create,update,delete}`,
`magento.product.tier_prices.set` — 18 names in total.

### 4.4 Collection naming: `.list` vs bare noun

| Uses `.list` | Uses a bare noun |
|---|---|
| `magento.attributes.list`, `magento.creditmemo.list`, `magento.invoice.list`, `magento.shipment.list` | `magento.attribute.sets`, `magento.attributes.options`, `magento.customer.addresses`, `magento.customer.groups`, `magento.inventory.sources`, `magento.inventory.stocks`, `magento.product.media`, `magento.product.options`, `magento.store.configs`, `magento.tax.classes`, `magento.import.batches` |

### 4.5 Verb placement

| Pattern | Examples |
|---|---|
| `domain.verb` | `magento.product.create`, `magento.category.update`, `magento.inventory.assign`, `magento.order.cancel` |
| `domain.noun_verb` | `magento.inventory.source_create`, `magento.inventory.stock_source_link`, `magento.inventory.stock_source_unlink` |
| `domain.subdomain.verb` | `magento.product.links.add`, `magento.product.media.update`, `magento.attributes.options.create` |
| `domain.subdomain.noun_verb` | `magento.attributes.options.bulk_create`, `magento.products.bulk_update`, `magento.product.links.bulk` |

No mass rename is recommended: names are a released interface. Add an exception list,
additive aliases, and the §9 deprecation note; generate future names from one table.

---

## 5. Duplicate and overlapping functionality register

| Overlap | Tools | Evidence | Recommendation |
|---|---|---|---|
| **Exact duplicate** | `magento.order.summary`, `magento.sales.summary` | Same `salesSummarySchema`, `salesSummaryOutput`, and `svc.SalesSummary` handler; the `order.summary` description admits it (`tools.go:1612-1615`) | Deprecate `order.summary` with `meta.quality.notes`; keep `sales.summary` |
| **Bulk superset** | `magento.product.links.add`/`.remove` vs `.bulk` | `.bulk` takes `action: add\|remove` and 1-50 `linked_skus` | Document single as a convenience alias; deprecate eventually |
| **Bulk superset** | `magento.attributes.options.create` vs `.bulk_create` | `.bulk_create` takes 1-50 labels | Same |
| **Bulk superset** | `magento.product.update` vs `magento.products.bulk_update` | Bulk applies status/visibility/attributes to 1-50 SKUs | Same; note bulk excludes name/price |
| **Batch superset** | `magento.inventory.status` vs `.status_batch` | Batch takes 1-25 SKUs | Same |
| **Batch vs single** | `magento.product.get` vs `magento.products.get` | One SKU vs 1-50 SKUs | Keep; document the distinction |
| **Same data, two shapes** | `magento.category.tree` vs `magento.categories.flat` | Both read `GET /V1/categories`; nested vs flattened | Keep both; document when to use each |
| **Sync vs async** | `magento.import.execute` vs `magento.import.start` | Both execute a bounded import; one blocks, one queues | Document the choice in both descriptions |
| **Aggregate overlap** | `magento.sales.analysis` vs `sales.summary`/`order.summary`/`orders.geography` | `sales.analysis` returns orders, revenue, items_qty, AOV, and a daily trend; geography adds a country breakdown | Keep analysis as the drill-down; deprecate the two summaries; document geography |

The documented deprecation mechanism (`docs/mcp-tools.md:375-386`) is not used by any
tool. A duplicate-detection test does not exist.

---

## 6. Discoverability and the three proposed workflow tools

### 6.1 Is the surface tractable?

Partially. The namespace prefixes are a real grouping mechanism, the descriptions are
static and mostly informative, and the `write_safety` prompt covers catalog-structure
writes. But at 112 tools (55 write/destructive), with four singular/plural splits,
27 noun-final names, 18 four-segment names, exact duplicates, and no server
`Instructions`, selection is not tractable from `tools/list` alone. `platform.info`
is the intended index and it is wrong (MCP-02). The highest-leverage fixes are the
inventory truth fix and a short `Instructions`/prompt layer, not more tools.

### 6.2 `magento.configurable.create_complete` — **recommend against**

Current code already implements the complete flow in `magento.configurable.create`:
children are created, the parent is created with `configurable_product_options`, each
child is linked, and parent stock is set (`internal/magento/configurable.go:114-153`).
`magento.configurable.create_variations` adds children to an existing parent,
`magento.configurable.set_options` declares options, and `magento.configurable.add_child`
links one child. A `create_complete` would be a second name for the same compound,
non-atomic write (the existing tool reports `stage`/`completed_skus` on failure and
has no compensation). Adding it doubles the write surface and the confirmation
surface without adding capability. If the real gap is attribute-option preflight,
add a read-only preflight or extend the existing tool; document the sequence in a
prompt. **Do not ship in v0.11.**

### 6.3 `magento.inventory.reconcile` — **recommend against as a write tool**

There is no reconciliation code today. The current inventory tools are
`inventory.assign` (set semantics, 100 items), `inventory.transfer` (read-modify-write
across two sources, explicitly "not transactional, so a failure can leave one row
updated"), `inventory.unassign`, `inventory.source_items`, and `inventory.status`.
A write reconcile would be a read-compute-write loop with an unbounded blast radius,
no idempotency ledger (`ARCH-04`; `grep -rn idempotency_keys --include=*.go` returns
zero), and an approval surface that shows only the tool title (`SEC-07`). The safe
shape is a **read-only** `magento.inventory.reconcile_plan` returning a diff
(`sku`, `source`, `current`, `desired`) that an operator reviews, followed by
per-item `inventory.assign` confirmations. **Do not ship a write reconcile in
v0.11.**

### 6.4 `magento.order.fulfillment` — **recommend against as a single write tool**

The business need is real (invoice, shipment, tracking, customer notification), and
the current surface requires three or four separate confirmed calls. But a single
fulfillment tool would combine `invoice.capture` (charges the payment method),
`shipment.create` (creates tracking), and `order.comment`/email into a non-atomic
compound write with no idempotency ledger. A partial failure can capture money
without creating the shipment, and the approval summary cannot show which order or
amount is being approved (`SEC-07`). The safe shape is a **read-only**
`magento.order.fulfillment_status` (order state + invoice/shipment state + tracking)
and/or a `order_fulfillment` prompt that sequences the existing confirmed writes.
**Do not ship the compound write in v0.11.**

| Proposed tool | Current code | Real need | Recommendation |
|---|---|---|---|
| `magento.configurable.create_complete` | `configurable.create` already does the full flow | Low; duplicate | **Against** |
| `magento.inventory.reconcile` | No reconcile; transfer non-transactional | Medium; unsafe as a write | **Against**; read-only plan if requested |
| `magento.order.fulfillment` | Separate invoice/shipment/comment tools | High; unsafe as one write | **Against**; read-only status + prompt |

---

## 7. Verification

### 7.1 Required test command

Run at `66786a5` and again on the dirty working tree after the in-flight
remediation changes; both runs pass:

```console
$ go test -count=1 ./internal/mcp/... ./internal/magento/...
ok      github.com/shuaiZend/mage2mcp/internal/mcp/registry    0.140s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/server    0.160s
ok      github.com/shuaiZend/mage2mcp/internal/mcp/toolerr    0.135s
ok      github.com/shuaiZend/mage2mcp/internal/magento    0.592s
```

```console
$ go vet ./internal/mcp/... ./internal/magento/...
(no output; exit 0)
```

The Magento contract and docs tests also pass:

```console
$ go test -count=1 -run 'TestMagentoToolContract|TestImplementedToolsDocsMatchRegistry' -v ./internal/magento/
=== RUN   TestMagentoToolContract
--- PASS: TestMagentoToolContract (0.06s)
=== RUN   TestImplementedToolsDocsMatchRegistry
--- PASS: TestImplementedToolsDocsMatchRegistry (0.02s)
PASS
```

That pass is itself evidence for MCP-14: the docs test compares names only, so the
stale metadata and the "Not implemented yet" contradiction do not fail it.

### 7.2 Call-level probes

Run against the maximal registry with an in-memory MCP client and a memory
confirmation store. Outputs are abbreviated to the envelope:

```console
PROBE platform.info
  is_error=false tools=111 protocol_versions=["2025-11-25","2026-07-28"]
PROBE magento.product.get {"instance":"nope","sku":"ABC"}
  is_error=true {"code":"not_found","details":{"instance":"nope"},
                 "message":"not_found: unknown Magento instance"}
  text="not_found: not_found: unknown Magento instance"
PROBE magento.product.get {"instance":"store-us","sku":"ABC","bogus":1}
  is_error=true {"code":"invalid_argument",
                 "details":{"validation":"validating root: unexpected additional properties [\"bogus\"]"}}
PROBE ga4.analytics.traffic {"instance":"ga","metrics":["not_a_metric"],"bogus":"accepted?"}
  is_error=true {"code":"validation_failed","details":{"metric":"not_a_metric","reason":"unsupported_metric"}}
PROBE magento.product.delete {"instance":"store-us","sku":"ABC"}
  is_error=true {"code":"confirmation_required",
                 "details":{"confirmation_id":"...","expires_at":"..."}}
PROBE magento.import.status {"job_id":"00000000-0000-0000-0000-000000000000"}
  (with a failing queue stub)
  is_error=true {"code":"internal","message":"storage: get job: ERROR: relation \"jobs\" does not exist (SQLSTATE 42P01)"}
```

Production-wiring probe (`server.NewMCP` with the options `cmd/server` actually
passes, 101 registry tools):

```console
registry_total=101 registry_write_tools=55 platform_tools_listed=101
platform_writes_enabled=false platform_instances=map[] protocols=[2025-11-25 2026-07-28]
```

Write-gate probe (reads + import jobs, `tools.writes` off):

```console
reads_plus_importjobs_total=49 non_read=3
[magento.import.cancel (write) magento.import.resume (write) magento.import.start (write)]
```

### 7.3 What the tests do not cover

- No test asserts that a write tool is absent when `tools.writes` is false.
- No test asserts `platform.info` matches `tools/list`, `writes_enabled`, or
  `instances`.
- No test compiles the input/output schemas at registration.
- No test asserts an output-schema description exists.
- No test asserts a write description contains an idempotency statement.
- No test asserts the error envelope shape is identical across handler, middleware,
  and denial paths.
- No test pins the Cloudflare, GA4, or unified tool names.

---

## 8. Working-tree delta and cross-references

The audit was pinned to `66786a5`, but the working tree was **not** clean while the
report was written: the other workstreams were landing remediation in parallel. At
handoff, `git status --short` showed 20 tracked files modified (not committed), in
these areas:

- `cmd/server`, `cmd/admin`, `cmd/worker` — authorization, confirmation display,
  worker retry/cancel wiring;
- `internal/mcp/server/{server,server_test}.go` — the candidate `MCP-01` fix
  (scope-aware `Authorize`, fail-closed unknown tool, `serverOwnedTools`);
- `internal/security/rbac/*`, `internal/security/confirm/*`,
  `internal/storage/{confirm,jobqueue,storage}*` — scope ladder, confirmation
  binding/summary, fenced job transitions, `Cancel` row-count handling;
- `internal/jobs/*`, `internal/magento/{configurable,media}*` — retryability,
  configurable/media remediation;
- untracked `internal/jobs/probe_reliability_test.go` and
  `internal/storage/probe_reliability_test.go` (reliability probes).

I did **not** author, modify, or independently review any of those changes. The
required test command passes on the dirty tree, but every finding in this report is
recorded against `66786a5` and none of the in-flight fixes is counted as closed. Two
consequences matter for the reader:

1. `MCP-01` has a credible uncommitted candidate fix; it still needs a commit,
   independent review, and a test at the server boundary before it can be closed.
2. `REL-14` (`magento.import.cancel` false success) has an uncommitted candidate fix
   in `storage/jobqueue.go`; it is not duplicated here. The untyped DB-error path
   behind `MCP-09` remains: `GetJob` still returns `fmt.Errorf("storage: get job:
   %w", err)` on a non-`ErrNoRows` failure.

`.audit-tmp/` was this audit's transient harness and has been removed before handoff;
Appendix A is its source of record.

### Cross-reference map

| MCP finding | Overlaps | Disposition |
|---|---|---|
| MCP-01 | `SEC-01`, `ARCH-01` | Same P0; count once under `SEC-01` |
| MCP-02 | `SEC-16` | MCP-02 is the wrong inventory data; SEC-16 is the registry bypass |
| MCP-03 | `SEC-01` precondition | MCP-03 corrects the "writes only with `tools.writes`" assumption |
| MCP-07 | `SEC-02`, `ARCH-03` (missing `instance` on job tools) | Cross-referenced, not duplicated |
| MCP-09 | `SEC-10` (upstream text) | MCP-09 is envelope shape/untyped errors |
| MCP-13 | `MAG-25` (capability docs) | Different: code metadata vs docs matrix |
| MCP-14 | `DOC-*`, `SEC-16` | DOC-ONLY; docs engineer owns the fix |
| MCP-15 | `SEC-10`, `DOC-*` | MCP-15 is the missing provenance envelope |
| (not a finding) | `REL-14` (`import.cancel` false success) | Reliability owns it; not duplicated here |

---

## Appendix A: reproducible inventory harness

Transient program, run from the repository root with `go run ./.audit-tmp/compact`.
It builds the maximal registry (Magento reads + writes + import jobs, Cloudflare, GA4,
unified), registers `platform.info` through `server.NewMCP`, and prints the
`tools/list` inventory with the registry metadata. It is reproduced here so a
reviewer can recreate it without the deleted directory.

```go
// Transient audit harness: derives the registered tool inventory from code.
// Run from the repository root: go run ./.audit-tmp/compact
package main

import (
    "context"
    "fmt"
    "log/slog"
    "net/http"
    "os"
    "sort"
    "time"

    "github.com/modelcontextprotocol/go-sdk/mcp"
    "github.com/shuaiZend/mage2mcp/internal/cloudflare"
    "github.com/shuaiZend/mage2mcp/internal/ga4"
    "github.com/shuaiZend/mage2mcp/internal/jobs"
    "github.com/shuaiZend/mage2mcp/internal/magento"
    "github.com/shuaiZend/mage2mcp/internal/mcp/registry"
    "github.com/shuaiZend/mage2mcp/internal/mcp/server"
    "github.com/shuaiZend/mage2mcp/internal/security/confirm"
    "github.com/shuaiZend/mage2mcp/internal/unified"
)

type noopSigner struct{}

func (noopSigner) Sign(*http.Request) error { return nil }

type fakeQueue struct{}

func (fakeQueue) Enqueue(context.Context, jobs.Task) (string, error) { return "job-1", nil }
func (fakeQueue) Cancel(context.Context, string) error              { return nil }
func (fakeQueue) GetJob(context.Context, string) (*jobs.JobRecord, error) {
    return nil, nil
}

type trafficProvider struct{}
type sessionProvider struct{}

func (trafficProvider) Traffic(context.Context, string, time.Time, time.Time) (unified.Traffic, error) {
    return unified.Traffic{}, nil
}
func (sessionProvider) Sessions(context.Context, string, time.Time, time.Time) (unified.Sessions, error) {
    return unified.Sessions{}, nil
}

func must(err error) {
    if err != nil {
        fmt.Fprintln(os.Stderr, "FATAL:", err)
        os.Exit(1)
    }
}

func main() {
    mc, err := magento.NewClient(magento.ClientConfig{BaseURL: "https://shop.example.com", StoreCode: "default", Signer: noopSigner{}})
    must(err)
    msvc, err := magento.NewService(mc)
    must(err)
    resolver, err := magento.NewResolver(map[string]*magento.Service{"store-us": msvc})
    must(err)
    cfc, err := cloudflare.NewClient(cloudflare.Config{APIBaseURL: "https://api.cloudflare.com/client/v4", APIToken: "x", ZoneID: "z"})
    must(err)
    g, err := ga4.NewService(ga4.Config{PropertyID: "123456", TokenSource: ga4.StaticToken("x")})
    must(err)
    u, err := unified.NewService([]unified.Binding{{ID: "b1", Magento: "store-us", Cloudflare: "cf1", GA4: "ga1"}}, trafficProvider{}, sessionProvider{})
    must(err)

    reg := registry.New()
    must(magento.RegisterWithResolver(reg, resolver))
    must(magento.RegisterWritesWithResolver(reg, resolver))
    must(magento.RegisterImportJobs(reg, resolver, fakeQueue{}))
    must(cloudflare.Register(reg, cloudflare.NewService(cfc)))
    must(ga4.Register(reg, g))
    must(ga4.RegisterExtra(reg, g))
    must(unified.Register(reg, u))

    confirmSvc, err := confirm.NewService(confirm.NewMemoryStore(), confirm.DefaultTTL)
    must(err)
    logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
    srv, err := server.NewMCP(server.Options{Version: "audit", Logger: logger, Registry: reg, WriteTools: true, Confirm: confirmSvc})
    must(err)

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    st, ct := mcp.NewInMemoryTransports()
    go func() { _, _ = srv.Connect(ctx, st, nil) }()
    cs, err := mcp.NewClient(&mcp.Implementation{Name: "audit", Version: "0"}, nil).Connect(ctx, ct, nil)
    must(err)
    defer cs.Close()
    res, err := cs.ListTools(ctx, nil)
    must(err)

    meta := map[string]registry.Tool{}
    for _, t := range reg.List() {
        meta[t.Name] = t
    }
    sort.Slice(res.Tools, func(i, j int) bool { return res.Tools[i].Name < res.Tools[j].Name })
    fmt.Printf("registry=%d tools_list=%d\n", reg.Len(), len(res.Tools))
    for _, t := range res.Tools {
        m := meta[t.Name]
        scope := m.Permission
        if scope == "" {
            scope = "none"
        }
        fmt.Printf("%s\t%s\t%gs\t%v\n", t.Name, scope, m.Timeout.Seconds(), m.RequiresConfirmation)
    }
}
```

Expected first lines:

```console
registry=111 tools_list=112
cloudflare.analytics.traffic    read    60s    false
cloudflare.security.events    read    60s    false
cloudflare.security.summary    read    60s    false
...
```

---

## Appendix B: limitations

1. **No live Magento.** Every Magento tool claim is code/contract-verified against
   the fake-server test harness; no `INTEGRATION` label is claimed.
2. **No live Cloudflare or GA4.** Their schemas and error paths are inspected, not
   exercised against the real APIs.
3. **Concurrent working tree.** The security workstream's uncommitted changes were
   present during the audit; all findings are pinned to `66786a5`, and §8 records
   what changed. A re-run against the eventual commit is required before release.
4. **Transient harness.** The inventory harness is not committed; Appendix A is the
   source of record. The counts in §1.2 were also cross-checked against the Magento
   golden file and the docs table.
5. **Severity judgement.** The P0/P1/P2/P3 assignment follows `docs/audit/README.md`;
   MCP-01 is counted once, under `SEC-01`, to avoid inflating the P0 count.
