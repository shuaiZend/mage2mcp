# Magento evidence log

**Scope:** read-only probes that verify the endpoint map in
[docs/integrations/magento.md](../integrations/magento.md) against a live Magento 2.4.x instance.
**Date of record:** 2026-11-18. **Result:** not executed — no credentials or reachable instance were
provided, so every behavioural row is `[U]`; structural rows verified from official documentation or
released source are tagged `[V-docs]`.

## 1. Environment and redaction rules

| Item | Value |
|---|---|
| Target | Magento 2.4.x, ideally 2.4.9 (baseline F6) |
| Base URL | `MAGENTO_BASE_URL`, e.g. `https://staging.example.com` (no `/rest`, no trailing slash) |
| Store code | `MAGENTO_STORE_CODE`, default `default` |
| Credential | `MAGENTO_ACCESS_TOKEN`: integration or admin bearer token, provisioned out of band |

Rules, in order of importance: never print, paste, or commit `MAGENTO_ACCESS_TOKEN`, cookies, or
`Authorization` headers (the suite never mints a token: `POST /V1/integration/admin/token` is a POST
and is out of scope here); never record customer data or person-linked order data, and truncate ids
and SKUs to counts or `SKU-<redacted>`; never paste full response bodies — record endpoint, status,
structural facts (field names, counts, enum values), and a timestamp, inspecting through `jq 'keys'`
or `jq '.items | length'`. Admin UI screenshots are not evidence.

## 2. Probe suite

All commands are read-only, bounded by `--max-time`, and print only a status code or a structural
projection.

```bash
set -u
: "${MAGENTO_BASE_URL:?set MAGENTO_BASE_URL}"
: "${MAGENTO_ACCESS_TOKEN:?set MAGENTO_ACCESS_TOKEN}"
STORE="${MAGENTO_STORE_CODE:-default}"
API="${MAGENTO_BASE_URL}/rest/${STORE}"
AUTH="Authorization: Bearer ${MAGENTO_ACCESS_TOKEN}"
JSON="Accept: application/json"
get() { curl -sS --max-time 60 -H "$AUTH" -H "$JSON" "$@"; }

# P1  Authentication and store visibility
get -o /dev/null -w '%{http_code}\n' "$API/V1/store/storeViews"

# P2/P3  Full REST schema (capability source of truth) and async schema
get "$API/schema?services=all" -o schema.json && jq -r '.paths | keys[]' schema.json | sort > paths.actual
wc -l paths.actual
get -o async-schema.json -w '%{http_code}\n' "$API/async/schema"

# P4  Configurable surface (replace SKU-CONFIG with a known configurable SKU)
get -o /dev/null -w '%{http_code}\n' "$API/V1/configurable-products/SKU-CONFIG/children"
get "$API/V1/configurable-products/SKU-CONFIG/options/all" | jq 'length'

# P5  Attribute sets, then one attribute's type, scope, and options
get "$API/V1/products/attribute-sets/sets/list?searchCriteria[pageSize]=100" | jq '.items | length'
get "$API/V1/products/attributes/color" | jq '{frontend_input, scope, options: (.options | length)}'
get -o /dev/null -w '%{http_code}\n' "$API/V1/products/attributes/color/options"

# P6  Media roles for an attribute set
get "$API/V1/products/media/types/default" | jq 'length'

# P7  MSI topology and one source-item page
get "$API/V1/inventory/sources" | jq '.items | length'
get "$API/V1/inventory/stocks" | jq '.items | length'
get -o /dev/null -w '%{http_code}\n' "$API/V1/inventory/source-items?searchCriteria[pageSize]=1"

# P8  Salable quantity and legacy stock status for one SKU on the default stock
get "$API/V1/inventory/get-product-salable-quantity/SKU-SIMPLE/1" | jq '.'
get -o /dev/null -w '%{http_code}\n' "$API/V1/stockStatuses/SKU-SIMPLE"

# P9  Sales read (one order, one page) and tier price read (POST-as-read, no mutation)
get "$API/V1/orders?searchCriteria[pageSize]=1&searchCriteria[sortOrders][0][field]=created_at" \
  | jq '{count: (.items | length), total: .total_count}'
get -H 'Content-Type: application/json' -X POST -d '{"skus":["SKU-SIMPLE"]}' \
  -o /dev/null -w '%{http_code}\n' "$API/V1/products/tier-prices-information"

# P10 Import endpoint presence (Commerce only; absent on Open Source)
jq -r '.paths | keys[] | select(startswith("/V1/import/"))' schema.json

# P11 Storefront truth via GraphQL over GET (no POST, no mutation)
curl -sS --max-time 30 -H "$JSON" -G "$MAGENTO_BASE_URL/graphql" \
  --data-urlencode 'query={products(filter:{sku:{eq:"SKU-CONFIG"}}){items{sku stock_status ... on ConfigurableProduct{configurable_options{attribute_code values{value_index label}} variants{product{sku} attributes{code value_index}}}}}}' \
  | jq '.data.products.items[0] | {sku, stock_status, options: (.configurable_options | length), variants: (.variants | length)}'
```

The tier-price read is a POST because Magento exposes it that way; it changes nothing. P11 uses GraphQL
over GET to keep the suite free of write-shaped requests; if the instance rejects GET for GraphQL, run
the same document as a POST containing only a `query` (never a `mutation`) and record that `[U]`.

## 3. Results

| Probe | Expected | Observed | Date | Status |
|---|---|---|---|---|
| P1 store views | 200, non-empty `items` | not run — credentials not provided | 2026-11-18 | `[U]` |
| P2/P3 schema and async schema | 200, Swagger 2.0 with `paths`; async schema lists `async/V1/products` | not run — credentials not provided | 2026-11-18 | `[U]` |
| P4 children / options | 200; children ≥ 1, options ≥ 1 | not run — credentials not provided | 2026-11-18 | `[U]` |
| P5/P6 attribute and media metadata | 200; `frontend_input`, `scope`, options and media roles present | not run — credentials not provided | 2026-11-18 | `[U]` |
| P7 MSI topology | 200; ≥ 1 source, ≥ 1 stock | not run — credentials not provided | 2026-11-18 | `[U]` |
| P8 salable quantity | 200; numeric quantity | not run — credentials not provided | 2026-11-18 | `[U]` |
| P9 orders and tier prices | 200; `items` length ≤ 1; array response | not run — credentials not provided | 2026-11-18 | `[U]` |
| P10 import paths | present on Commerce, absent on Open Source | schema not fetched; docs and module layout indicate Commerce-only | 2026-11-18 | `[V-docs]` |
| P11 GraphQL variants | 200; `variants` length equals child count | not run — credentials not provided | 2026-11-18 | `[U]` |
| Endpoint map (section 3) | paths and methods as listed | matches the 2.4.9 admin OpenAPI schema (493 paths) and released `webapi.xml`; configurable child DELETE and MSI routes present | 2026-11-18 | `[V-docs]` |
| Import endpoints | `POST /V1/import/csv`, `/V1/import/json` | in the 2.4.9 admin schema; Commerce-only per docs and Open Source layout | 2026-11-18 | `[V-docs]` |
| Auth and async | admin 4 h, integration indefinite, customer 1 h; async declared in `webapi_async.xml` | token page; core 2.4.9 declares `async/V1/products` (POST) and `async/bulk/V1/products/bySku` (PUT) | 2026-11-18 | `[V-docs]` |
| Media and EAV fields | jpg/jpeg/gif/png, MIME must match bytes; no `is_configurable`; no super-attribute pricing route | `ImageContentValidator` defaults; 2.4.9 schema and `Catalog/etc/db_schema.xml` | 2026-11-18 | `[V-docs]` |

## 4. Capability detection and schema diff

Run P2, then diff the instance's own schema against the endpoint map in section 3 of the integration
document. This is the only reliable way to know what a given 2.4.x build exposes, because extensions
and edition change the surface.

```bash
# expected.txt: "METHOD /V1/path", one per line, derived from the integration document section 3.
# Keep it in the repository as a fixture; the excerpt below shows the format and the critical rows.
cat > expected.txt <<'EOF'
POST /V1/products
DELETE /V1/products/{sku}
POST /V1/configurable-products/{sku}/child
DELETE /V1/configurable-products/{sku}/children/{childSku}
POST /V1/inventory/source-items
GET /V1/inventory/get-product-salable-quantity/{sku}/{stockId}
GET /V1/store/storeViews
EOF

jq -r '.paths | to_entries[] | .key as $p | .value | keys[] | select(. != "parameters")
       | "\(. | ascii_upcase) \($p)"' schema.json | sort -u > actual.txt

comm -23 expected.txt actual.txt > missing-upstream.txt   # expected but absent
comm -13 expected.txt actual.txt > extra-upstream.txt     # present but unexpected
wc -l missing-upstream.txt extra-upstream.txt
```

| Diff result | Meaning | Action |
|---|---|---|
| Expected path missing upstream | Edition or version does not expose it (e.g. import on Open Source) | Do not advertise the tool; log `capability_absent` |
| Path present, method missing | Partial support, usually an older 2.4.x | Disable the operation, not the whole capability |
| Extra upstream paths; `async/schema` lists a route | Extensions or newer features; async usable only with a running consumer | Ignore unknown paths; prefer async only after a status probe transitions |

Two schema properties matter beyond paths: `definitions` confirms payload fields (for example
`configurable_product_links` on the product extension interface), and `info.version` gives the
instance's declared version, which belongs in the capability cache key.

## 5. What this suite does not verify

- Write behaviour: no state-changing request is included, so the configurable lifecycle is
  documentation-derived until a staging instance runs a scripted create/verify/delete cycle.
- Indexer timing: P8 and P11 must run before and after a source-item write to measure the delay
  (integration document U7); without a write only the steady state is observed. Rate limiting is not
  measured either: Magento publishes no universal limit, so the gateway defaults are policy. SaaS
  instances (IMS OAuth) are out of scope and must not be probed here.
