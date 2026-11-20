# Magento 2 write verification (2026-11-20)

## Target and method

- **Instance**: `https://demo-magento-2.auroracreation.com` (standard Magento 2
  demo, resets every six hours; store view code `pl`; timezone Europe/Warsaw).
- **Build under test**: released **v0.8.1** (`mage2mcp-server`/`admin` binaries and
  the `ghcr.io/shuaizend/mage2mcp:v0.8.1` image). **No rebuild or redeploy of the
  project stack** — a temporary test container was used.
- **Writes enabled** (`tools.writes: true`), confirmations approved by a separate
  identity (`demo-reviewer`) through `mage2mcp-admin`.
- **Credentials**: the provided OAuth 1.0a integration was rejected by this
  Magento (see W7); an admin bearer token from
  `POST /rest/V1/integration/admin/token` was used instead (valid ~1 hour).

## Results

| Tool | Result | Notes |
|---|---|---|
| `magento.product.create` | PASS | confirmation → approve → create, `product.get` verifies |
| `magento.product.update` | PASS | price/status/SEO fields |
| `magento.product.delete` | PASS | destructive confirmation, `get` returns not_found |
| `magento.category.create` | PASS (retry) | first attempt 400 with the same name as a previous run; retry with a new name succeeded (W9) |
| `magento.category.assign_product` | PASS | |
| `magento.inventory.assign` | PASS | requires `status` in each item (W11) |
| `magento.products.bulk_update` | PASS | |
| `magento.import.execute` | PARTIAL | returns a correct failed-row report, but the row failed with an opaque `validation_failed` (W4/W12) |
| `magento.customer.create` | PASS | |
| `magento.customer.addresses.create` | FAIL | `404` — no customer-address route on this Magento (W6) |
| `magento.configurable.create` | PASS | parent + variant + child link |
| `magento.product.media.upload` | **FAIL (upstream success)** | image appears in the gallery, tool returns `upstream_unavailable` (W1) |
| `magento.product.media.update` | FAIL | `400` — payload lacks `media_type`; Magento: "no MediaGalleryEntryConverter for this type" (W3) |
| `magento.product.media.delete` | PASS | |
| `magento.attributes.options.create` | **FAIL (upstream success)** | option id `"216"` returned as a JSON scalar; tool expects an object (W2) |
| `magento.order.create` | FAIL | cart/items/shipping succeeded; `place_order` route `404` (W5) |

## Defects

| ID | Severity | Tool / area | Evidence | Suspected cause | Proposed fix |
|---|---|---|---|---|---|
| W1 | high | `product.media.upload` | Magento returns a JSON string (`"3422"`) for the new entry id; the tool unmarshals it into a `MediaEntry` and reports `upstream_unavailable`, while the image is already in the gallery | response parser assumes an object | accept scalar/string responses and return the id (optionally fetch the gallery entry); never report failure after a successful write |
| W2 | high | `attributes.options.create` | Magento returns `"216"`; tool reports `upstream_unavailable: unreadable attribute-option payload` | same as W1 | accept the scalar id and return `{label, value}` |
| W3 | high | `product.media.update` | `PUT /products/{sku}/media/{id}` → 400 "no MediaGalleryEntryConverter for this type" | update payload omits `media_type` (and possibly `file`/`content`) | send a complete entry (`id`, `media_type`, `file` or content, label, position, types) or read-modify-write |
| W4 | high | all writes | Magento 400 messages (e.g. URL-key collision, media converter, import validation) are discarded; clients only see `validation_failed` + `upstream_status` | `statusError` drops the upstream body | include a sanitized, truncated `upstream_message` in `details` |
| W5 | high | `order.create` | `POST /carts/{id}/order` and `/carts/mine/order` both 404 on this demo; tool reports `not_found` at `place_order` and leaves an abandoned cart | route absent on the target; no preflight | detect unsupported place-order routes before creating a cart, and/or document the requirement; return actionable guidance |
| W6 | medium | `customer.addresses.create` | `POST /customers/{id}/addresses`, `GET /customers/{id}/addresses`, `POST /addresses` all 404 | route unavailable on this Magento | fall back to `PUT /customers/{id}` with an `addresses` array, or fail early with a capability error |
| W7 | high | credentials | OAuth 1.0a `HMAC-SHA1` rejected: "Signature method HMAC-SHA1 is not supported"; admin bearer token works | Magento 2.4.7 demo removed/disabled OAuth 1.0a; no token auto-acquisition | document bearer-first; optionally support the admin token endpoint with explicit opt-in, and refresh short-lived tokens |
| W8 | medium | instance config | store code `default` returns an opaque 400; valid codes are `pl`/`en` | no store-code discovery/validation; upstream message dropped (W4) | validate store code against `store/configs` at startup, or include valid codes in the error |
| W9 | medium | `category.create` | duplicate name (same URL key) → 400; no `url_key` input | category payload has no url_key; message hidden (W4) | expose `url_key` or auto-suffix duplicates; surface the upstream message |
| W10 | low | `category.tree` | returns a flat list under `roots` | key/shape mismatch with the tool name | rename to `categories` or return a nested tree |
| W11 | low | `inventory.assign` | schema requires `status` per item | stricter than Magento | default `status` to 1 when omitted |
| W12 | medium | `import.execute` | minimal CSV row fails with opaque `validation_failed`; direct `POST /products` with the same fields succeeds | import payload adds fields that this Magento rejects, or required fields are missing; message hidden (W4) | capture the upstream message, reproduce with a real row, and align the import payload |

## Follow-up

Fix W1–W6 and W9 first (correctness and misleading failures), then W7/W8
(credential/store discovery), then W10–W12. W1/W2 are the most dangerous because
they report failure after a successful write.

## Fix status (v0.9.0)

| ID | Status | Evidence |
|---|---|---|
| W1 | fixed | `media.upload` accepts the scalar entry id, resolves the gallery entry, and returns it; demo upload + update re-verified |
| W2 | fixed | `attributes.options.create` accepts the scalar option id and returns `{label, value}`; demo returned `217` |
| W3 | fixed | `media.update` reads the current entry and sends `media_type`/`file`; demo update succeeded |
| W4 | fixed | upstream 400/422 messages are included as `details.upstream_message` (sanitized, ≤300 chars); import rows include it |
| W5 | improved | missing place-order routes now return `unsupported_capability` with stage/cart id and a best-effort cart cleanup; the demo does not expose cart deletion either, so cleanup reports `failed: not_found` |
| W6 | improved | missing customer-address routes now return `unsupported_capability` with guidance instead of a bare 404 |
| W7 | documented | the demo rejects OAuth 1.0a (`HMAC-SHA1` unsupported); bearer tokens from the admin token endpoint work. The gateway surfaces Magento's message; automatic token acquisition is not implemented |
| W8 | improved | 400 errors include `store_code` and a hint to verify it; upstream messages are surfaced |
| W9 | fixed | `category.create` accepts `url_key` and sends it through `custom_attributes`; demo create succeeded |
| W10 | fixed | `category.tree` now returns the list under `categories` (breaking output rename, pre-1.0) |
| W11 | fixed | `inventory.assign` defaults `status` to 1 when omitted |
| W12 | fixed | imports succeed; duplicate URL keys are surfaced with Magento's message (include `url_key` in the CSV to avoid collisions) |
