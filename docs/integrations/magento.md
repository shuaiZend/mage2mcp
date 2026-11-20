# Magento 2 integration

**Module:** `internal/magento` — **Primary target:** Magento 2.4.9 (Open Source and Adobe Commerce,
self-hosted or PaaS). **Status:** as-built for v0.11.2; the
gateway is a REST admin client only (ADR-0022). No live Magento instance was
reachable, so every behavioural claim is `CONTRACT` (REST contract plus
`httptest` fakes); vendor facts are `[V-docs]` or `[U]`. Nothing here is
`INTEGRATION_TESTED`.
**Baseline:** [research-baseline.md](../development/research-baseline.md) (F6–F12, correction 2).

## 1. Scope and non-goals

| Area | Scope | Tag |
|---|---|---|
| Product types | Simple, Configurable, Virtual, Downloadable, Bundle | `[V-docs]` |
| Lifecycle depth | Simple and Configurable: full create / update / query / delete | `[V-docs]` |
| Lifecycle depth | Virtual, Downloadable, Bundle: create / update / query of the type-specific payload, no dedicated lifecycle automation | `[V-docs]` |
| Catalog structure | Attribute sets, attributes, attribute options, attribute groups, categories | `[V-docs]` |
| Configurable | Parent, children, options, links, variant attributes, website and store scoping, media, categories, prices, MSI inventory, bulk import, validation, relationship sync, partial-failure recovery | `[V-docs]` |
| Inventory | MSI sources, stocks, source-items, bulk assign/unassign/transfer, salable quantity | `[V-docs]` |
| Sales | Read-only orders, invoices, shipments, credit memos, aggregate analytics | `[V-docs]` |
| Verification | REST admin read-back (`configurable.children`, `product.get`); storefront GraphQL is not used (ADR-0022) | `CONTRACT` |

**Non-goals.** Adobe Commerce as a Cloud Service (SaaS / IMS OAuth) is out of scope for v0.x: IMS
OAuth instead of admin or integration tokens, the `/async` segment moves after `V1`, and `POST /V1/import/csv` is unsupported `[V-docs]` ([auth](https://developer.adobe.com/commerce/webapi/rest/authentication/),
[async](https://developer.adobe.com/commerce/webapi/rest/use-rest/asynchronous-web-endpoints/), [import](https://developer.adobe.com/commerce/webapi/rest/modules/import/)); see section 13. GraphQL writes are out: product mutations cover reviews, compare lists, wishlist, and
stock/price alerts only `[V-docs]` (baseline F8). Also excluded: Magento plugins, modules, patches, direct database
access, checkout, carts, customers, payment, promotions, CMS, B2B, and per-variant price deltas (no REST
endpoint in 2.4.9 `[V-docs]`; see U4).

**Success definition.** HTTP 200 is not correctness. A configurable product is
correct when the admin read-back returns the parent, the expected children, the
declared option values, and the expected stock status; `magento.configurable.create`
verifies the child links by reading them back and reports `orphan_skus` for any
child it could not link. Storefront rendering is the operator's verification, not
the gateway's (ADR-0022).

## 2. Authentication and connection model

### 2.1 Credential types and gateway modes

| Deployment | Credential | Lifetime | Gateway mode | Notes | Tag |
|---|---|---|---|---|---|
| Self-hosted / PaaS | Admin token | 4 hours | `admin_token` | `credential: admin:<username>:<password>`; inherits the admin ACL role; prefer a dedicated API-only admin, never a human administrator | `[V-docs]` |
| Self-hosted / PaaS | Integration access token | Indefinite until revoked | `integration_bearer` | Bearer use disabled by default since 2.4.x; needs `oauth/consumer/enable_integration_as_bearer=1` | `[V-docs]` |
| Self-hosted / PaaS | Integration OAuth 1.0a (4 credentials) | n/a | `integration_oauth1` | HMAC-signed requests, no config toggle; **production default** | `[V-docs]` |
| Adobe Commerce as a Cloud Service | Adobe IMS OAuth | IMS-managed | none | Out of scope for v0.x | `[V-docs]` |

Source: [token-based authentication](https://developer.adobe.com/commerce/webapi/get-started/authentication/gs-authentication-token/). `POST /V1/integration/admin/token` exists in the 2.4.9 admin schema `[V-docs]`; with 2FA enabled the plain endpoint is insufficient and a
TFA provider route is required `[V-docs]` ([generate-local](https://developer.adobe.com/commerce/webapi/rest/quick-reference/generate-local/)). With the `admin:` credential form the gateway mints an admin token lazily on the first
request and again when Magento answers 401, caches it for 4 hours minus 5 minutes of skew, and never persists
the password, logs the token, or uses session cookies. The token request has a 15 s deadline, does not follow
redirects, and never echoes the upstream body.

**Admin credentials are more powerful than an integration token.** The token inherits the full ACL role of the
admin account, including sales, customer, and configuration access that no integration needs. Configure a
dedicated API-only admin (System > Permissions > All Users) with a least-privilege role for the manifest in
section 2.2, never a human administrator account; rotate the password when the gateway is decommissioned. When
the store requires 2FA, the plain token endpoint fails and `bearer:` or `oauth1:` remains the supported path.

### 2.2 Least privilege

**The tool surface is not the security boundary; the credential is.** A tool
allowlist decides what the gateway *calls*; it does not bound what the integration
credential *can do* if the gateway, a dependency, or a leaked secret is used
outside the registry. The ACL is part of the deployment contract. The gateway
does **not** probe the credential's ACL at startup; the operator configures and
reviews the integration's Available APIs.

Integration ACL is a tree chosen in **System > Extensions > Integrations > Available APIs** `[V-docs]` ([authentication](https://developer.adobe.com/commerce/webapi/get-started/authentication/)). Resource IDs below come from the 2.4.9 `acl.xml` files and from the `<resources>` element of the `webapi.xml` route that consumes them; `Magento\Framework\Acl` extends `Laminas\Permissions\Acl\Acl`, which inherits an allow from a parent resource to its children, so granting a parent grants every descendant `[V-docs]` ([Catalog acl.xml](https://github.com/magento/magento2/blob/2.4.9/app/code/Magento/Catalog/etc/acl.xml), [Backend acl.xml](https://github.com/magento/magento2/blob/2.4.9/app/code/Magento/Backend/etc/acl.xml), [Sales acl.xml](https://github.com/magento/magento2/blob/2.4.9/app/code/Magento/Sales/etc/acl.xml), [InventoryApi acl.xml](https://github.com/magento/inventory/blob/1.2.2/InventoryApi/etc/acl.xml)).

**Read-only manifest.** It contains only resources that no core 2.4.9 REST write route consumes. Catalog, source-item and
sales-document reads are absent because their resources also gate writes (see the write manifest), so those read
tools are unavailable in this mode. Store codes are explicit configuration, not
discovered from a storefront GraphQL query.

| Capability | Resource ID | Routes that consume it | Status |
|---|---|---|---|
| Orders, order items, order comments (read) | `Magento_Sales::actions_view` | `GET /V1/orders`, `/V1/orders/{id}`, `/V1/orders/items`, `/V1/orders/{id}/comments` | `[V-docs]` read-only |
| Transactions (read) | `Magento_Sales::transactions_fetch` | `GET /V1/transactions[/{id}]` | `[V-docs]` read-only |
| Stocks (read) | `Magento_InventoryApi::stock` | `GET /V1/inventory/stocks[/{stockId}]` | `[V-docs]` read-only |
| Salable quantity, resolver (read) | `Magento_InventorySalesApi::stock` | `GET /V1/inventory/get-product-salable-quantity/...`, `/is-product-salable/...`, `/stock-resolver/...` | `[V-docs]` read-only |
| Store codes | none | configured `store_code` per instance; no storefront GraphQL | `CONTRACT` |

**Write-enabled manifest.** Least privilege for the full tool set: every row is required by an enabled tool,
and resources for disabled tool groups are removed rather than left granted.

| Capability | Resource ID | Routes that consume it | Status |
|---|---|---|---|
| Products, websites, configurable options/children/variation, bulk MSI assign/transfer | `Magento_Catalog::products` | `POST/PUT/DELETE /V1/products...`, `/V1/products/{sku}/websites`, `/V1/configurable-products/...`, `/V1/inventory/bulk-product-source-*` | `[V-docs]` |
| Attributes and options | `Magento_Catalog::attributes_attributes` | `/V1/products/attributes...`, `/{code}/options...`, `GET /V1/products/media/types/{set}` | `[V-docs]` |
| Attribute sets, groups, membership | `Magento_Catalog::sets` | `/V1/products/attribute-sets...` | `[V-docs]` |
| Categories and category products | `Magento_Catalog::categories` | `/V1/categories...`, `/V1/categories/{id}/products...` | `[V-docs]` |
| Media, tier/base/special/cost prices, links, custom options | `Magento_Catalog::catalog` | `/V1/products/{sku}/media...`, `/V1/products/tier-prices*`, `/base-prices`, `/special-price`, `/cost`, `/{sku}/links`, `/V1/products/options` | `[V-docs]` |
| MSI sources and source items | `Magento_InventoryApi::source` | `GET /V1/inventory/sources`, `GET/POST /V1/inventory/source-items`, `/source-items-delete` | `[V-docs]` |
| Source create/update | `Magento_InventoryApi::source_edit` | `POST/PUT /V1/inventory/sources[/{code}]` | `[V-docs]` |
| Stock-source links | `Magento_InventoryApi::stock_source_link` | `GET/POST /V1/inventory/stock-source-links`, `/stock-source-links-delete` | `[V-docs]` |
| Stocks create/update/delete (only if stock tools are enabled) | `Magento_InventoryApi::stock_edit`, `Magento_InventoryApi::stock_delete` | `POST/PUT /V1/inventory/stocks`, `DELETE /V1/inventory/stocks/{id}` | `[V-docs]` |
| Salable quantity, resolver (read) | `Magento_InventorySalesApi::stock` | `GET /V1/inventory/get-product-salable-quantity/...`, `/is-product-salable/...`, `/stock-resolver/...` | `[V-docs]` |
| Order reads for verification | `Magento_Sales::actions_view` | `GET /V1/orders...` | `[V-docs]` |
| Store, website and group ids | `Magento_Backend::store` | `GET /V1/store/storeViews`, `/storeGroups`, `/websites`, `/storeConfigs` | `[V-docs]` (residual, below) |

`Magento_Catalog::catalog` is the parent of the four catalog children, so granting it also grants
`Magento_Catalog::products`, including `DELETE /V1/products/{sku}`. Media and price writes cannot be granted
without product delete, which is why write mode is inherently destructive and why every write is confirmation-gated
(section 14). The old `Magento_InventoryApi::source_items` id does not exist in 1.2.2: source-item reads and writes
are both `Magento_InventoryApi::source`.

Not granted in either manifest: `Magento_Config::config` and its section children (`Magento_Catalog::config_catalog`,
`Magento_Sales::config_sales`, …) — store configuration is out of scope and core 2.4.9 has no REST write route for
it; `Magento_Sales::sales_invoice`, `Magento_Sales::shipment`, `Magento_Sales::sales_creditmemo` — they gate
capture/refund/create as well as reads, so those read tools stay disabled unless the operator explicitly grants the
resources and accepts the extra write capability; `Magento_Catalog::catalog_inventory` — legacy stock path, only
with the legacy tools.

**Startup credential probe.** After the schema fetch and before any tool is advertised, the gateway probes the
credential once per instance (again after a 401 re-mint or credential rotation), with a 10 s budget and no retry on
5xx. A denial is HTTP 401 or 403 carrying `The consumer isn't authorized to access <resource>`: 2.4.9 maps the ACL
`AuthorizationException` to 401 in `Magento\Framework\Webapi\ErrorProcessor::getHttpCodeForLocalizedException`
`[V-docs]`, so the finding's 403 is the right semantics but not the literal status on every build; a proxy or
extension may surface 403. Both are accepted, and the message must name the probed resource.

A probe whose route is absent from the instance schema is skipped and recorded, never treated as a failure, because capability detection has already removed the tools that needed it. The probe is read-only in effect. ACL is checked in `Magento\Webapi\Controller\Rest\RequestValidator::validate()`
before the service and its input parameters are resolved `[V-docs]`, so a denied request never parses a body or
reaches a repository; every probe body is deliberately invalid (`type_id: __mage2mcp_probe__`, empty payloads) and
targets the reserved SKU `__mage2mcp_probe__`, which is read first — a denial already proves the catalog ACL is
absent, a 404 allows the write probe, and a 200 aborts as `probe_sku_conflict` rather than risk a write. Even a
wrongly authorized call therefore fails validation instead of creating, updating, or deleting anything.

| Mode | Probe | Pass | Fail |
|---|---|---|---|
| `read_only` | `PUT /V1/products/__mage2mcp_probe__` (needs `Magento_Catalog::products`) | denial naming that resource | any other status → instance disabled: `credential_scope_exceeded: read-only mode requires Magento_Catalog::products to be absent; probe was authorized (status 404)` |
| `read_only` | `POST /V1/inventory/source-items` with `{"sourceItems":[]}` (needs `Magento_InventoryApi::source`) | denial | any 2xx/4xx → `credential_scope_exceeded` for source-item writes |
| `write` | one invalid-body write route per required resource: `PUT /V1/products/__mage2mcp_probe__`, `POST /V1/products/attributes`, `POST /V1/products/attribute-sets`, `POST /V1/categories`, `POST /V1/products/__mage2mcp_probe__/media`, `POST /V1/inventory/source-items`, `POST /V1/inventory/sources`, `POST /V1/inventory/stock-source-links`, and `POST /V1/inventory/stocks` when stock tools are enabled | non-denial (400 or 404) | denial → instance disabled: `credential_scope_insufficient: Magento_Catalog::catalog missing (media probe denied)` |

Two resources are reported but cannot be probed. `Magento_Backend::store` has no REST write route in 2.4.9 (only
`GET /V1/store/*`), yet it is the `ADMIN_RESOURCE` of the admin store-view/website/group controllers, so a write
instance that resolves stores can also change them in the admin UI; `Magento_Config::config` has no core REST route
at all. Both are recorded as `acl_unverifiable` in the startup report and must be confirmed in the integration's
Available APIs tree (probe U10). `[U]`

### 2.3 Store code

REST base is `{base_url}/rest/{store_code}/V1/...`; `store_code` accepts `default`, `all`, or a store view code `[V-docs]`. Documented fallback `[V-docs]`: create without a
store code writes globally; update without one updates the default store only; `all` updates every store scope
lacking its own value; an explicit code updates only that view. `store_code` is therefore explicit configuration, never
inferred from a tool call, and `all` is allowed only for product and CMS operations.

### 2.4 Base URL and TLS

The base URL is operator-supplied and validated when the client is built:
`https` only (plain `http` solely for `localhost`/`127.0.0.1` in development); no
userinfo, query, or fragment. Redirects are refused. TLS verification is Go's
default; `InsecureSkipVerify` is not exposed and there is no custom CA bundle.
There is **no IP allowlist or DNS-pinning guard** (PLANNED); the operator is
trusted to configure a real Magento host.

## 3. Endpoint map

Paths are relative to `{base_url}/rest/{store_code}`. "Idempotent" describes the HTTP method, not Magento internals. Risk: `low` read, `medium` additive write, `high` destructive or storefront-visible, `critical`
irreversible or cross-store. `[V-docs]` = path and method confirmed in the 2.4.9 admin OpenAPI schema
or released `webapi.xml`; `[U]` = unconfirmed, probe stated.

### 3.1 Products and prices

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Create product | POST | `/V1/products` | W | No | High | `[V-docs]` | `type_id` selects the product type |
| Update / get / delete product | PUT, GET, DELETE | `/V1/products/{sku}` | R/W | Yes | Critical (delete) | `[V-docs]` | PUT is a merge (5.3); delete cascades to links, media, stock |
| Search / list types | GET | `/V1/products`, `/V1/products/types` | R | Yes | Low | `[V-docs]` | `searchCriteria[...]`; always set `pageSize` |
| Websites | POST, DELETE | `/V1/products/{sku}/websites[/{websiteId}]` | W | Yes | Medium | `[V-docs]` | Body `{productWebsiteLink:{sku,website_id}}` |
| Related/upsell links | POST, PUT, GET, DELETE | `/V1/products/{sku}/links[/{type}[/{linkedSku}]]` | R/W | No (POST) | Medium | `[V-docs]` | `link_type` in body |
| Tier prices | POST, PUT | `/V1/products/tier-prices`, `/tier-prices-information`, `/tier-prices-delete` | R/W | No (POST) | High | `[V-docs]` | PUT replaces the whole set; POST used as read |
| Base / special / cost prices | POST | `/V1/products/base-prices`, `/special-price`, `/cost` | W | Yes | High | `[V-docs]` | Bulk by SKU |

### 3.2 Configurable products

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Options | POST, GET, PUT, DELETE | `/V1/configurable-products/{sku}/options`, `/options/all`, `/options/{id}` | R/W | No (POST) | High | `[V-docs]` | `{attribute_id, label, position, values[{value_index}]}` |
| Add / list child link | POST, GET | `/V1/configurable-products/{sku}/child`, `/children` | R/W | No (POST) | High | `[V-docs]` | `{childSku}`; returns boolean |
| **Delete child link** | DELETE | `/V1/configurable-products/{sku}/children/{childSku}` | W | Yes | High | `[V-docs]` | Unlinks; the child product survives |
| Generate variation | PUT | `/V1/configurable-products/variation` | W | No | High | `[V-docs]` | `{product, options[]}`; creates children and links |

Routes per [ConfigurableProduct webapi.xml](https://github.com/magento/magento2/blob/2.4.9/app/code/Magento/ConfigurableProduct/etc/webapi.xml).

### 3.3 EAV: sets, attributes, options, groups

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Attribute sets | GET | `/V1/eav/attribute-sets/list`, `/V1/products/attribute-sets/sets/list` | R | Yes | Low | `[V-docs]` | Both routes exist in 2.4.9 |
| Set CRUD, membership, assignment | GET, POST, PUT, DELETE | `/V1/products/attribute-sets[/{attributeSetId}[/attributes[/{attributeCode}]]]`, `/attribute-sets/attributes` | R/W | No (POST) | Medium | `[V-docs]` | Membership read drives dynamic mapping |
| Attribute groups | GET, POST, PUT, DELETE | `/V1/products/attribute-sets/groups[/{groupId}]` | R/W | No (POST) | Medium | `[V-docs]` | Delete fails while attributes remain |
| Attributes | GET, POST, PUT, DELETE | `/V1/products/attributes[/{attributeCode}]` | R/W | No (POST) | Critical (delete) | `[V-docs]` | Returns `frontend_input`, `scope`, `options`; delete drops the column |
| Attribute options | GET, POST, PUT, DELETE | `/V1/products/attributes/{attributeCode}/options[/{optionId}]` | R/W | No (POST) | High (delete) | `[V-docs]` | `value` + `label`; `store_labels[]` per store |

### 3.4 Categories and media

#### Authentication: bearer first

Magento 2.4.7 (and some hosted builds) reject OAuth 1.0a with
`Signature method HMAC-SHA1 is not supported`. Use a bearer token from
`POST /rest/{store_code}/V1/integration/admin/token` (admin credentials) or a
pre-issued integration token, and configure either `credential: admin:<username>:<password>`
or `credential: bearer:<token>`. The admin form mints the token on the first
request, refreshes it before the assumed 4-hour lifetime, and re-mints it once
when Magento answers 401; the password is never logged or persisted. The
`bearer:` form stays available for operators who do not want to store admin
credentials, and `oauth1:` is unchanged. The gateway surfaces Magento's message
in `details.upstream_message`; a token-endpoint rejection is reported as
`unauthenticated` without echoing the upstream body.

Admin credentials are strictly more powerful than an integration token: the
minted token inherits the whole admin ACL role. Use a dedicated API-only admin
with a least-privilege role (section 2.2), never a human administrator, and
treat the credential as production-grade secret material. With 2FA enabled on
the store the plain token endpoint is refused, so fall back to `bearer:` or
`oauth1:`.

#### Deployment variance

Endpoint availability differs between Magento builds and hosting setups. The
gateway keeps the tool contract stable and adapts the read path:

| Read | Primary route | Fallback |
|---|---|---|
| Tier prices | `GET /V1/products/tier-prices?skus=` | `GET /V1/products/{sku}` (`tier_prices`) |
| Product links | `GET /V1/products/{sku}/links?linkType=` | `GET /V1/products/{sku}` (`product_links`) |
| Order history | `GET /V1/orders/{id}/comments` | accepts the `items` envelope or a bare array |
| Invoice/shipment/credit-memo lists | collection endpoints with an `order_id` filter | — |

Verified against `www.giztop.com` on 2026-11-20; see
[production acceptance](../operations/production-acceptance.md).

#### Variant images

Magento has no configurable-product media association endpoint: the
`ConfigurableProduct` routes are children, options, and variations only
(verified against `app/code/Magento/ConfigurableProduct/etc/webapi.xml`). A
variant's image is the **child product's own gallery**, managed with the normal
product media tools against the child SKU:

1. `magento.product.media.upload` with the child SKU and base64 content;
2. `magento.product.media.update` to set `image`/`small_image`/`thumbnail` roles
   and the position;
3. optionally upload the same file to the parent for its gallery.

The gateway intentionally does not invent a parent-child media link: Magento's
storefront reads the selected child's gallery, so keeping the image on the child
is the supported path. `magento.product.media.get` reads one entry back.

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Category tree / list / get | GET | `/V1/categories`, `/V1/categories/list`, `/V1/categories/{categoryId}` | R | Yes | Low | `[V-docs]` | Tree is large; cache it |
| Category CRUD and move | POST, PUT, DELETE | `/V1/categories[/{categoryId}]`, `/{categoryId}/move` | W | No (POST) | High (delete) | `[V-docs]` | `custom_attributes[]` for SEO fields; move rewrites paths |
| Category products | GET, POST, PUT, DELETE | `/V1/categories/{categoryId}/products[/{sku}]` | R/W | Yes | Medium | `[V-docs]` | |
| Media types | GET | `/V1/products/media/types/{attributeSetName}` | R | Yes | Low | `[V-docs]` | Roles per set; never hardcode |
| Create / list / get media | POST, GET | `/V1/products/{sku}/media[/{entryId}]` | R/W | No (POST) | Medium | `[V-docs]` | Base64 content; returns `entryId` |
| Update / delete media | PUT, DELETE | `/V1/products/{sku}/media/{entryId}` | W | Yes | High (delete) | `[V-docs]` | Roles, label, position; delete removes the file |

### 3.5 MSI inventory and stock status

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Sources | GET, POST, PUT | `/V1/inventory/sources[/{sourceCode}]` | R/W | No (POST) | High | `[V-docs]` | |
| Stocks | GET, POST, PUT, DELETE | `/V1/inventory/stocks[/{stockId}]` | R/W | No (POST) | Critical (delete) | `[V-docs]` | Default stock cannot be deleted |
| Stock-source links | GET, POST | `/V1/inventory/stock-source-links`, `/stock-source-links-delete` | R/W | No | High | `[V-docs]` | POST-as-delete |
| Source items | GET | `/V1/inventory/source-items` | R | Yes | Low | `[V-docs]` | searchCriteria on `sku`, `source_code` |
| Upsert / delete source items | POST | `/V1/inventory/source-items`, `/source-items-delete` | W | Yes | High | `[V-docs]` | Quantity **replaces**, it does not add |
| Assign / unassign source items | POST | `/V1/inventory/source-items`, `/source-items-delete` | W | Yes | High | `[V-docs]` | The gateway uses these per-SKU endpoints; it does not call the bulk transfer routes |
| Transfer between sources | — | two source-item upserts (read, re-read, write, read back) | W | No | High | `CONTRACT` | Returns `transfer_id` + outcome enum; see §8 |
| Salable quantity / resolver / legacy | GET | `/V1/inventory/get-product-salable-quantity/{sku}/{stockId}`, `/is-product-salable/{sku}/{stockId}`, `/stock-resolver/{type}/{code}`, `/V1/stockStatuses/{sku}` | R | Yes | Low | `[V-docs]` | Post-index truth; `stockStatuses` is the legacy CatalogInventory surface |

Routes per [Inventory Management](https://developer.adobe.com/commerce/webapi/rest/inventory/) and [magento/inventory](https://github.com/magento/inventory/tree/1.2.2) `webapi.xml` files.

### 3.6 Store configuration and sales

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Store views / groups / websites / config | GET | `/V1/store/storeViews`, `/storeGroups`, `/websites`, `/storeConfigs` | R | Yes | Low | `[V-docs]` | Resolve codes and ids; never hardcode |
| Orders | GET, POST | `/V1/orders`, `/V1/orders/{id}`, `/V1/orders/items`; creation via the cart flow | R/W | No (create) | Critical | `[V-docs]` | Heavy payload; writes are confirmation- and ledger-gated, see 10 |
| Order state | POST | `/V1/orders/{id}/comments`, `/hold`, `/unhold`, `/cancel`, `/emails` | W | No | High | `[V-docs]` | Confirmation-gated |
| Invoices | GET, POST | `/V1/invoices`, `/V1/invoices/{id}`; create via `POST /V1/order/{orderId}/invoice`; `/V1/invoices/{id}/capture`, `/email` | R/W | No (create) | Critical | `[V-docs]` | The workflow route honours `capture`; the repository route does not, so the gateway does not use it |
| Shipments / credit memos | GET, POST | `/V1/shipments`, `/V1/creditmemos` (+ `/{id}`, `/email`); create via the order document routes | R/W | No (create) | Critical | `[V-docs]` | Confirmation- and ledger-gated |

### 3.7 Async, bulk status, import, schema (design reference; not used)

| Capability | Method | Path | Read/Write | Idempotent | Risk | Verification tag | Notes |
|---|---|---|---|---|---|---|---|
| Async product create | POST | `/async/V1/products` | W | No | High | `[V-docs]` | Declared in `WebapiAsync/etc/webapi_async.xml` |
| Async bulk update | PUT | `/async/bulk/V1/products/bySku` | W | Conditional | High | `[V-docs]` | Declared in `Catalog/etc/webapi_async.xml` |
| Async update by SKU | PUT | `/async/V1/products/{sku}` | W | Conditional | High | `[V-docs]` | **Not registered** in 2.4.9 `webapi_async.xml` (F26); use `PUT /async/bulk/V1/products/bySku` |
| Bulk status | GET | `/V1/bulk/{bulkUuid}/status`, `/detailed-status`, `/operation-status/{status}`, `/V1/bulk` | R | Yes | Low | `[V-docs]` | Poll `bulk_uuid` |
| Import | POST | `/V1/import/csv`, `/V1/import/json` | W | No | Critical | `[V-docs]` | **Adobe Commerce only**; base64 CSV |
| Schema / async schema | GET | `/rest/{store}/schema?services=all`, `/rest/{store}/async/schema` | R | Yes | Low | `[V-docs]` | Source of truth for capabilities |

This table is vendor reference only. **The gateway does not use Magento async,
bulk-status, or import endpoints.** Imports run through the gateway's own
PostgreSQL job queue and write through the normal REST tools; the async path was
not implemented. The `/V1/import/*` endpoints are Adobe Commerce-only and are not
called.

## 4. The Magento EAV model for integrators

Products are EAV rows: `catalog_product_entity` holds SKU and type, and attribute values live in typed tables scoped by store and
website.

**Scope.** `scope` is `global`, `website`, or `store` `[V-docs]` (`scope` on
`catalog-data-product-attribute-interface`). Only `global` attributes can drive a configurable product. A store-scoped value is an override; removing it
reverts to the default, which is why a store-scoped write can look like a reset. A product references exactly
one attribute set, which decides which attributes exist; the attribute group only decides admin form placement.
Two products in different sets therefore have different attribute surfaces, and a field's presence is never
transitive.

**`frontend_input` is the type system:** `text`, `textarea`, `select`, `multiselect`, `boolean`,
`price`, `date`, `media_image`, `gallery`, `swatch_visual`, `swatch_text` `[V-docs]`. There is **no
`is_configurable` flag** — the term appears nowhere in the 2.4.9 REST schema, and
`Catalog/etc/db_schema.xml` has no such column `[V-docs]`. Configurability is derived: the attribute
is in the product's set, has a select-like input, is global, and has options with `value_index` values.

**Option ids vs values.** Select options have a stable integer `option_id`, while the stored product
value is the option `value`, exposed as `value_index` in GraphQL and as `value` in REST `[V-docs]`. Configurable payloads use `values[].value_index`
(integer), never the label and never the option id. Labels are localizable per store through `store_labels[]` (`{store_id, label}`), so
matching variants by label is always wrong.

**Why dynamic mapping is mandatory.** Attribute codes, option ids, labels, sets, media roles, and
store codes are instance-specific. The gateway resolves them at runtime from `GET /V1/products/attribute-sets/sets/list`, `.../{setId}/attributes`, `GET /V1/products/attributes/{attributeCode}/options`, `GET /V1/products/media/types/{attributeSetName}`, and `GET /V1/store/storeViews`,
caching per instance with a TTL and invalidating on its own attribute writes. Tool inputs use logical names
(`color`) resolved to `attribute_id` + `value_index` at call time; an unresolved name yields `validation_failed` listing the valid options instead of a
Magento exception.

## 5. Configurable product lifecycle

The gateway does not run a persisted multi-state machine with per-transition
idempotency keys or compensation. It runs a **staged workflow** and reports what
it did:

```text
precheck -> children -> parent -> links (read-back verified) -> stock
```

| Stage | Behaviour | On failure |
|---|---|---|
| `precheck` | Resolve the attribute set and variant attributes, reject a pre-existing SKU on a fresh run, validate the child matrix before any write | `conflict`/`validation_failed`; zero writes |
| `children` | Create only the missing children; on resume, reuse existing children | Stage-tagged error; children created so far remain |
| `parent` | Create the configurable parent (or reuse it on resume) | Stage-tagged error; children remain |
| `links` | Link each child, then read the children back to verify the association | `orphan_skus` names children that exist but are not linked |
| `stock` | Set the parent stock status from the children and report `parent_in_stock` | Reported; the links remain |

The result always carries:

- `operation_id` — pass it back to resume the same create;
- `outcome` — `completed` or `partial`;
- `stages` / `completed_stages` / `failed_stage`;
- `orphan_skus` — children that exist but are not linked (reconcile them).

**Resume.** A resumed run with `operation_id` reuses existing children and the
parent, adds missing links, and repairs an orphan left by a failed link stage. It
writes no duplicate SKU. A fresh run (no `operation_id`) is **not** idempotent and
refuses a pre-existing SKU in precheck.

**No compensation.** Nothing is deleted. A partial run leaves the completed
writes in place and reports the orphans; an operator or a resumed run finishes
the job.

**Vendor payload shape** (still true): `attribute_id` is a string,
`value_index` an integer, and child attribute values are strings `[V-docs]`
(`configurable-product-data-option-interface`,
`configurable-product-data-option-value-interface`). Parent visibility 4 and
child visibility 1 are the conventional shape, but the tool accepts the schema's
enums.

## 6. Validation rules table

| Rule | Why it matters | Enforcement point | Error surfaced to the agent |
|---|---|---|---|
| SKU `^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`, unique | Global uniqueness; late failure wastes writes | Pre-flight before any write | `validation_failed: sku already exists (PHONE-01-BLK-128)` |
| Configurable attribute select-like and global | Options cannot be created otherwise | Schema cache | `validation_failed: 'color' is frontend_input 'text'` |
| Option value exists | Dangling values break the storefront | Schema cache | `validation_failed: value_index 99 not in 'color' (valid: 12, 13)` |
| Child covers every option | Incomplete combinations cannot be selected | Matrix expansion | `validation_failed: PHONE-01-BLK-128 missing 'storage'` |
| Parent and child share a website | Otherwise variants are invisible | Pre-flight on website ids | `validation_failed: child websites [1] not subset of parent [1,2]` |
| Parent visibility 4, child 1 | Prevents duplicate listings | Payload normalization | `validation_failed: configurable parent visibility must be 4` |
| `type_id` matches payload shape | Type-specific payloads are otherwise ignored | Tool input schema | `invalid_argument: type_id 'bundle' requires bundle_options` |
| Quantity non-negative; source exists | A bad value or code rejects the batch | Tool schema, `GET /V1/inventory/sources` | `not_found: source 'wh2' does not exist` |
| Store code valid | A typo writes to the wrong scope | Operator configuration; the gateway does not validate it at startup | Upstream 400/404 naming the store code |
| Media MIME allowed and size bounded | Magento validates both | Gateway pre-check: `image/jpeg`, `image/png`, `image/webp`, `image/gif`, ≤ 5 MiB decoded | `validation_failed: mime_type must be one of image/jpeg, image/png, image/webp, image/gif` |
| Import format is CSV or JSON and within the decoded cap | XLSX/gzip/archive payloads would bypass the CSV/JSON validator | Format allowlist and 8 MiB decoded / 5,000 row caps (section 9) | `unsupported_format`; `validation_failed` naming the limit |
| Category exists | Assignment fails late otherwise | `GET /V1/categories/{id}` | `not_found: category 42 does not exist` |
| Destructive call confirmed | Deletes are irreversible | Tool layer | `confirmation_required` with an opaque `confirmation_id`; approval by a different human identity |
| Admin read-back verifies the links | HTTP 200 is not correctness | Final `links` stage reads `configurable.children` back | `validation_failed: child PHONE-01-BLK-256 exists but is not linked` (reported as `orphan_skus`) |

## 7. Media handling

Uploads use the base64 content contract, not multipart and not URL fetch:

```json
{"entry": {"media_type": "image", "label": "Phone 01 front", "position": 1, "disabled": false,
  "types": ["image", "small_image", "thumbnail"],
  "content": {"base64_encoded_data": "<base64>", "type": "image/jpeg",
              "name": "phone-01-front.jpg"}}}
```

`content` requires `base64_encoded_data`, `type`, and `name` `[V-docs]` (`framework-data-image-content-interface`); the endpoint returns the new `entryId` as an integer.

**MIME and size.** `ImageContentValidator` accepts `image/jpg`, `image/jpeg`, `image/gif`, and
`image/png`, requires the declared MIME to equal the MIME sniffed from the decoded bytes, and rejects names containing
`\ / ? * : " ; < > ( ) | { }` `[V-docs]` ([ImageContentValidator.php](https://github.com/magento/magento2/blob/2.4.9/lib/internal/Magento/Framework/Api/ImageContentValidator.php)). It performs **no size check**; the ceiling is PHP `post_max_size` and the web server body limit, which are
deployment-specific `[U]` (probe U2). Gateway defaults: 2 MiB per image, 10 MiB per request, 20 images per
product.

**Roles.** Valid roles come from `GET /V1/products/media/types/{attributeSetName}` `[V-docs]`; the
gateway never hardcodes them. Assigning `image` to a second entry silently moves the role, so the gateway clears the
role on the previous holder first.

**Deduplication and failure.** Decoded bytes are hashed (SHA-256) and remembered as
`sku → hash → roles → entryId`. A repeat with the same hash and roles is skipped and returns the recorded entry with
`deduplicated: true`; a repeat with different roles updates the existing entry instead of creating a second one. The
record is a **bounded in-process cache**, not a durable one: a hit is verified against the live gallery before it is
reused, so a stale entry costs a redundant upload rather than a wrong answer, and a restart simply forgets the
fingerprints. Magento creates entry and file together, so a failed upload leaves no partial entry.

**No crash resume across restarts.** If the process dies between creating an entry and assigning its roles, the entry
exists without them and a later retry uploads again. Making that resume would need the fingerprint to be durable,
which this layer deliberately does not depend on; recover by listing the gallery (`magento.product.media`) and setting
the roles with `magento.product.media.update`.

**No external URL fetching.** It would turn a product write into an SSRF primitive and make the write
non-reproducible. Any future URL-import tool must reuse the section 2.4 host validation and stay disabled by
default.

## 8. MSI inventory model

MSI separates write from read (CQRS): `SourceItem` is the write entity, aggregated
stock is produced by reindexing, and `GET` should use aggregated data while
`POST`/`PUT` use source items `[V-docs]` ([Inventory Management](https://developer.adobe.com/commerce/webapi/rest/inventory/)).

| Concept | Meaning | Endpoint |
|---|---|---|
| Source | A physical location holding stock | `/V1/inventory/sources` |
| Stock | A sales channel (currently websites) mapped to sources | `/V1/inventory/stocks` |
| Stock-source link | Priority-ordered membership of a source in a stock | `/V1/inventory/stock-source-links` |
| Source item | Quantity of one SKU at one source | `/V1/inventory/source-items` |
| Salable quantity | Aggregate sellable quantity minus reservations | `/V1/inventory/get-product-salable-quantity/{sku}/{stockId}` |

**Salable quantity is not the sum of source quantities.** Reservations from carts
and checkout reduce it and clear at shipment `[V-docs]`. A SKU with 10 units and
3 reservations reports 7. Every tool states which number it returns: source
quantity (physical), salable quantity (sellable now), or storefront
`stock_status` (binary).

**Assignment and transfer.** `magento.inventory.assign` and `.unassign` write
source items per SKU (1–100 rows). `magento.inventory.transfer` is a **per-SKU**
operation, not a bulk endpoint: it reads both source rows, re-reads them
immediately before writing, upserts both in one batch, then reads them back. The
result is a `TransferResult`:

| Field | Meaning |
|---|---|
| `transfer_id` | stable id for this attempt (also in the error details) |
| `outcome` | `applied`, `conflict`, `verification_mismatch`, `partial`, `not_applied`, `needs_verification` |
| `from_before` / `to_before` | quantities observed before the write |
| `from_after` / `to_after` | quantities observed after the write (omitted when not observed) |
| `intended_from` / `intended_to` | the post-state the transfer wrote |
| `from_applied` / `to_applied` | whether each row reached its intended value |
| `applied_rows` | `both`, `from`, `to`, `none`, or `unknown` |

Only `applied` is success; every other outcome is returned with a typed error
carrying the same facts. `needs_verification` means the post-state is unknown and
the gateway does not compensate on its own. The input `quantity` is an integer;
fractional MSI quantities are refused (a fractional read-back is
`needs_verification`). The operation is not transactional.

**Eventual consistency.** Source-item writes are synchronous, but salable
quantity and storefront availability depend on indexers and, with a message
queue, on consumers `[U]` for the delay. The gateway does not poll Magento for
30 seconds and does not use GraphQL `stock_status`; a read-back of the source
items is the verification it performs. Indexer lag is an operator-visible
condition, not something the gateway can prove.

## 9. Bulk import

Pipeline: `normalize -> validate -> preview -> confirm -> execute -> status ->
report`.

| Stage | Behaviour | Failure handling |
|---|---|---|
| Normalize | CSV or JSON to the canonical row model; headers map to attribute codes, never positionally | Unmapped header → `validation_failed` naming it; any other format → `unsupported_format` |
| Validate | Section 6 plus cross-row checks: duplicate SKUs, parent/child references, option values, website and category ids | Report all errors, not the first |
| Preview | Diff of creates, updates, unchanged, and per-row warnings | `magento.import.validate`; never writes |
| Confirm | The write tools use the standard two-step confirmation | Missing approval → `confirmation_required` |
| Execute | Bounded synchronous batch (`import.execute`) or a queued job (`import.start`) | Per-row report; earlier writes are not rolled back |
| Status | `magento.import.status` reads the PostgreSQL job; `.batches`/`.batch` read persisted batch history | Unknown job → `not_found` |
| Report | Row-level result: sku, action, status, error; failed rows persisted for resume | Produced even on abort |

**Format policy (as built).** Only CSV and JSON are accepted. There is no XLSX
converter, no gzip, and no archive support; those formats return
`unsupported_format`. Limits are enforced on the decoded bytes:

| Input | Accepted | Bound | Rejection |
|---|---|---|---|
| CSV (UTF-8) | yes | 8 MiB decoded, 64 columns, 5,000 rows | `validation_failed` naming the limit |
| JSON | yes | 8 MiB decoded, 5,000 rows | `validation_failed` / `invalid_argument` |
| XLSX, XLS, OLE2, gzip, zip, any archive | no | — | `unsupported_format` |

The base64 form of an 8 MiB payload is 11,184,812 bytes. The request body cap
must admit that plus the JSON envelope: the default is 16 MiB, the minimum for a
write-enabled deployment is 12 MiB, and a lower cap fails configuration
validation at startup. `magento.import.start` also needs `database.url`.

**Async execution.** The gateway's async path is its own PostgreSQL job queue,
not Magento `/async` or `/V1/import/*`. The worker claims the job with a lease
and fencing token, executes the rows through the normal REST tools, persists the
batch and every failed row, and records progress. `magento.import.resume` reads
the persisted failures (not the 100-row report cap) and queues a new job with
only those rows; repeated resumes of the same source job return the same new job.
Partial success is normal: the report distinguishes created, updated, skipped,
and failed rows.

**Confirmation and audit.** Imports are writes: they require the two-step
confirmation, the operation ledger gate, and an audit record. Payload bodies are
not stored in audit; the batch stores the payload needed for resume.

## 10. Orders and sales analytics

Sales data is read and written. Reads are projected and aggregate; writes
(orders, invoices, shipments, credit memos, comments, holds, cancellation) are
confirmation-gated and ledger-gated. The money tools accept an optional
`idempotency_key` and return `operation_unknown` when the outcome is ambiguous;
they never retry automatically. `magento.invoice.create` uses
`POST /V1/order/{orderId}/invoice` (the workflow route) because the repository
route `POST /V1/invoices` has no capture field and would silently ignore
`capture=true`. See §12 and the [threat model](../security/threat-model.md) §8.12.

**Acceptable queries.** `GET /V1/orders` filtered on `created_at`, `status`, `store_id`,
`customer_group_id`, or `increment_id`, with an explicit `pageSize` and sort `[V-docs]` ([search](https://developer.adobe.com/commerce/webapi/rest/use-rest/performing-searches/)). Aggregation happens in the gateway over the smallest
field set that answers the question: `entity_id`, `increment_id`, `created_at`, `status`, `store_id`, `grand_total`, `base_grand_total`, `total_qty_ordered` (order grain) and `qty_ordered` (order-line grain), `customer_group_id`.

**Why not pull full order tables.** An order embeds addresses, payment data, item options, and
extension attributes; a month of orders is hundreds of megabytes and personal data the gateway has no reason to
hold. Responses are projected, and raw order payloads never reach the model. Item analysis uses `GET /V1/orders/items` with the
same discipline.

**Pagination and limits.** `pageSize` is always sent: core collections have no default page size, so
an unbounded search can materialize the whole collection `[V-docs]` ([Collection.php](https://github.com/magento/magento2/blob/2.4.9/lib/internal/Magento/Framework/Data/Collection.php)). Caps: 200 per page for `order.search`, 50 for `products.get`, 20,000 orders per
`sales.analysis` call (10,000 default), and 1,000 orders per `order.summary`
window; truncation is reported rather than silently paginating forever. An out-of-range
`currentPage` returns an empty set, which must be distinguished from "no orders". `filter_groups` are ANDed, filters within a group
are ORed, and OR across groups is unsupported `[V-docs]`; only top-level attributes are searchable. Date filters use
`from`/`to` with `Y-m-d H:i:s` in the store time zone; the gateway converts UTC inputs and states the zone in the response
metadata.

**Outputs.** Revenue, order count, AOV, and units per period, plus per-store and per-status
breakdowns, each carrying the filters, store code, time zone, and truncation flag. No customer identifiers in
aggregates.

## 11. Error mapping

Magento returns `{code, message, parameters}`; `trace` appears only in developer mode `[V-docs]` ([response format](https://developer.adobe.com/commerce/webapi/get-started/gs-web-api-response/)). Statuses are coarse: 400 covers validation
and business rules, 404 covers both unknown routes and missing entities, 500 covers everything unexpected.

| Magento signal | HTTP | Representative message | Gateway code | Agent-facing message |
|---|---|---|---|---|
| `InputException`, `ValidatorException` | 400 | `Invalid product data: %1` | `validation_failed` | Field-level reason with the attribute |
| `CouldNotSaveException` | 400 | `The product couldn't be saved.` | `validation_failed` | Generic failure plus correlation id |
| Malformed JSON or wrong types | 400 | `Decoding error: ...` | `invalid_argument` | Names the parameter and expected type |
| `NoSuchEntityException` | 404 | `The product that was requested doesn't exist.` | `not_found` | Echoes the requested SKU |
| Unknown route | 404 | `Request does not match any route.` | `not_found` | Endpoint unsupported here; run capability check |
| Missing or expired token | 401 | `The consumer isn't authorized to access %resources.` | `permission_denied` | Re-authentication required |
| Insufficient ACL | 401 (403 via proxy) | `The consumer isn't authorized to access %resources.` | `permission_denied` | Same message as an expired token; the operator reviews the integration's ACL |
| `AlreadyExistsException` | 400/409 | `The product with the "%1" SKU already exists.` | `conflict` | SKU conflict with existing entity id |
| Duplicate URL key | 400 | `URL key for specified store already exists.` | `conflict` | Suggests a different `url_key` |
| Concurrent edit | 400 | `The order ... cannot be ...` | `conflict` | Refresh and retry |
| Throttling | 429 | (deployment-specific) | `rate_limited` | Retry-after hint |
| 5xx, reset, empty body | 500/502/503 | `Internal Error` | `upstream_unavailable` | Instance unhealthy; no payload echo |
| Client deadline | n/a | n/a | `upstream_timeout` | May have completed; verify before retry |
| Ambiguous write outcome | timeout, 5xx, reset after send | n/a | `operation_unknown` | Poll the operation; never retry |
| Idempotency key reused with different args | n/a | n/a | `idempotency_key_reuse` | Use a new key for a different request |

Translation rules: never forward raw messages (they embed class names, SQL fragments, and paths), `trace`, `parameters`, or
headers; distinguish entity 404 from route 404 by the message, because the recovery differs; map 400s mentioning
authorization or permission to `permission_denied`, since retrying with different data will not help; log endpoint, status,
duration, and a request hash, never the token, body, or customer data.

## 12. Timeouts, retries, pagination

Magento publishes **no universal REST rate limit**. Real limits come from
PHP-FPM workers, nginx, the PaaS WAF, and database capacity. The gateway does
**not** implement a per-instance concurrency semaphore, token bucket, rate
limiter, retry loop, or circuit breaker. Those are **PLANNED** (SEC-11,
PERF-05); limit concurrency at the agent or edge.

| Control | As built | Notes |
|---|---|---|
| Per-request timeout | 30 s default (`magento.DefaultTimeout`) | A write timeout becomes `operation_unknown`, not `failed` |
| Tool timeout | 30 s read, 60 s write, 120 s bulk/analysis | Enforced by the MCP middleware |
| Automatic retries | none | `retryable` is a hint for the caller; the gateway does not retry |
| Non-idempotent write retry | never automatic | A timed-out POST may have succeeded; the ledger/reconciler decides |
| Circuit breaker | none | PLANNED |
| Page size / row caps | per tool (200 order search, 50 product batch, 20,000 sales analysis, 1,000 order summary) | Bound memory and response size |
| Connection pool | tuned `http.Transport` (100 idle, 16 per host, 32 max per host) | Magento only; Cloudflare/GA4 use the standard client |

A write whose outcome is ambiguous is recorded as `unknown` and returned as
`operation_unknown`; the worker's reconciler resolves it. The caller must poll,
never retry.

## 13. Compatibility and versioning

| Topic | Position | Tag |
|---|---|---|
| Primary target / range | 2.4.9 primary; 2.4.x supported; 2.4.4+ assumed for Swagger behaviour | `[V-docs]` (F6) |
| Schema as source of truth | `GET /rest/{store}/schema?services=all` returns the instance's own Swagger; the gateway does **not** fetch or diff it at startup | `[V-docs]` |
| Async capability | Not used; the gateway has its own PostgreSQL job queue | `CONTRACT` |
| Swagger UI | Disabled in production mode since 2.4.4; `/swagger` may be exposed by default | `[V-docs]` |
| GraphQL | No Magento GraphQL client; storefront verification is not implemented (ADR-0022) | `CONTRACT` |
| Import endpoints | Adobe Commerce only; absent in Magento Open Source | `[V-docs]` |
| Legacy stock APIs | `CatalogInventory` endpoints superseded by MSI but still present | `[V-docs]` |
| 2FA on admin users | Plain admin token endpoint insufficient; TFA route required | `[V-docs]` |
| SaaS / IMS OAuth | Out of scope: different auth, different async path, no CSV import | `[V-docs]` |

**Capability detection is not implemented.** The gateway does not fetch the
schema at startup, does not diff it, and does not hide a tool whose endpoint is
missing; a missing route surfaces as `not_found`/`unsupported_capability` from the
call. A startup probe is **PLANNED**.

**Behavioural differences, not just availability:** 2.4.4+ hides Swagger UI
outside developer mode (the schema endpoint is unaffected); MSI is the inventory
authority from 2.3 onward, so writing `stock_item` through the product payload is
a compatibility path; async and bulk require a message queue, which the gateway
does not use; SaaS reorders the async path and drops CSV import, and is out of
scope for v0.x.

## 14. Security considerations

| Control | Requirement |
|---|---|
| Token storage | Memory or OS keyring; never in logs, errors, audit rows, or tool output; config references an env var or file path, never a committed literal |
| Least privilege | One integration per environment, restricted to the manifest for its mode (section 2.2). The credential, not the tool surface, is the boundary. The gateway does **not** probe the ACL at startup; review the integration's Available APIs against section 2.2 |
| Read-only deployments | Read-only manifest only; catalog, source-item and sales-document reads require the write manifest because 2.4.9 grants no read-only resource for them, and an instance with those resources is never described as read-only |
| Store configuration | `Magento_Config::config` and its section children are never granted; core 2.4.9 has no REST write route for store config, and store-view administration remains the residual of `Magento_Backend::store` (section 2.2) |
| Import formats | CSV and JSON only, 8 MiB decoded and 5,000 rows; XLSX, XLS, gzip, and archives are rejected with `unsupported_format` (section 9) |
| Admin credentials | Opt-in through the `admin:<username>:<password>` credential form; the password stays in process memory, is never logged, persisted, or echoed in errors, and mints a token that is cached for 4 h − 5 min and re-minted once on 401. Admin ACL exceeds an integration's, so use a dedicated API-only admin with the least-privilege role, never a human administrator |
| Credential logging | Method, path, status, duration, body hash only; `Authorization`, cookies, `Set-Cookie` redacted at the transport layer |
| Write confirmation | `delete`, `replace`, and bulk deletes require the two-step confirmation protocol (ADR-0014): opaque `confirmation_id`, approval by a different human identity, parameter-hash binding, single use. No `confirm` boolean exists |
| Audit fields | Subject hash, tool, instance, scope, decision, phase, state, error code, request id; the canonical argument hash is stored in the confirmation and operation ledger, not in the audit row; no bodies |
| Admin session abuse | No session cookies, no `/swagger`, no admin UI routes, no admin user creation |
| Response leakage | Messages translated (section 11); `trace` and `parameters` dropped |
| Replay / minimisation | Writes are gated by the operation ledger with fencing and idempotency keys; the money tools accept an explicit `idempotency_key` and return `operation_unknown` on ambiguity. Order reads project fields and aggregates exclude customer identifiers |

Threat-model detail belongs in `docs/security/`; this is the Magento-specific subset.

## 15. Verification log

| What | When | Against | Result | Tag |
|---|---|---|---|---|
| Endpoint paths and methods | 2026-11-18 | 2.4.9 admin OpenAPI schema (`admin-schema-2.4.9.yaml`, 493 paths) | Matches section 3 | `[V-docs]` |
| Configurable routes | 2026-11-18 | `ConfigurableProduct/etc/webapi.xml` @ 2.4.9 | 9 routes, including child DELETE | `[V-docs]` |
| MSI routes | 2026-11-18 | `magento/inventory` 1.2.2 `webapi.xml` files | Confirmed | `[V-docs]` |
| Import endpoints | 2026-11-18 | Import API page and Open Source `ImportExport` module | Commerce-only | `[V-docs]` |
| Auth and token lifetimes | 2026-11-18 | Token-based authentication page | Confirmed | `[V-docs]` |
| Async route registration | 2026-11-18 | `webapi_async.xml` @ 2.4.9 | Two core routes | `[V-docs]` |
| ACL manifests | 2026-11-19 | 2.4.9 `acl.xml` files and `webapi.xml` route resources | Matches section 2.2 | `[V-docs]` |
| ACL denial status and probe safety | 2026-11-19 | `RequestValidator.php`, `ErrorProcessor.php` @ 2.4.9 | Denial is 401 before body parsing; probe accepts 401/403 | `[V-docs]` |
| Format policy | 2026-11-19 | Threat model §5.8 and Import API page | CSV/JSON only, 8 MiB decoded, 5,000 rows; body-cap floor 12 MiB | `CONTRACT` |
| Live behaviour of any endpoint | — | No instance available | Not run | `[U]` |

The probe suite that closes the `[U]` items is in [docs/evidence/magento.md](../evidence/magento.md). No live verification was possible in this session because
no credentials were provided; every behavioural claim above is documentation-derived and must be re-confirmed
against a staging instance before the module is described as working.

## 16. Open questions and decision record

| ID | Question | Resolution | Blocking? |
|---|---|---|---|
| U1 | Does `POST /V1/products` accept `configurable_product_links` at create time, validated against existing children? | Create parent, link children, read back `configurable.children` | No |
| U2 | Real maximum media payload size (`post_max_size`, `client_max_body_size`) | Upload increasing sizes until 400/413 | No |
| U3 | Exact 404 payload for a missing product vs an unknown route | Two probes, compare `message` | No |
| U4 | No REST endpoint for per-variant price deltas in 2.4.9; confirm no supported write path | Schema search for `super`; CSV round-trip | Yes for price-differentiated variants |
| U5 | ~~Is `PUT /async/V1/products/{sku}` registered?~~ Resolved: not registered in 2.4.9 (F26) | `GET /rest/default/async/schema` for other installs | Closed |
| U6 | Default `pageSize` per endpoint when omitted | `GET /V1/products` without `pageSize` | No |
| U7 | Delay between source-item write and salable/stock status, with and without consumers | Timed staging probe against the REST source-item read-back | Yes for the inventory budget |
| U8 | ~~Exact ACL resource ids for MSI source items and store config~~ Resolved: source items are `Magento_InventoryApi::source`; store config reads are `Magento_Backend::store` (section 2.2) | Integration ACL tree | Closed |
| U9 | Does 2.4.9 return 409 or 400 for a duplicate SKU? | Duplicate create probe | No |
| U10 | Can store-view write scope be detected over REST? | No REST write route is gated by `Magento_Backend::store`; compare the integration's Available APIs tree with section 2.2 at startup review | No (residual, reported as `acl_unverifiable`) |
| U11 | Real maximum media payload accepted by the target's PHP/nginx limits | Upload increasing sizes until 400/413 | No |

**Decision record.** [ADR-0003](../adr/0003-magento-client-design.md) records
REST-only access with no Magento GraphQL client; the configurable lifecycle as a
staged workflow with an operation id for resume and orphan reporting; explicit
store code configuration; the gateway's PostgreSQL job queue for imports (not
Magento async); CSV/JSON import with an 8 MiB decoded cap; and SaaS/IMS OAuth out
of scope for v0.x.
