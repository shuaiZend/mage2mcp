# Tutorial: from zero to a working Magento agent

This walks you from an empty machine to an agent that reads a real store, changes a price,
places an order and refunds it — with the approval gate exercised along the way.

It assumes no prior knowledge of mage2mcp. Every command and every tool call below was run
against a live Magento store while writing this, so the outputs are real.

Other languages: [中文](i18n/zh/tutorial.md) · [Español](i18n/es/tutorial.md) ·
[Nederlands](i18n/nl/tutorial.md).

---

## 1. What you are building

```text
your agent  ──MCP (Streamable HTTP)──▶  mage2mcp  ──REST──▶  Magento 2
                                            │
                                            └──▶ PostgreSQL (audit, approvals, jobs)
```

mage2mcp is an MCP server. An agent talks to it in MCP; it talks to Magento in REST. You do
not write Magento REST calls, and the agent never holds a Magento credential.

---

## 2. Before you start

| You need | Why |
|---|---|
| Go 1.25 or newer | to build the two binaries |
| A Magento 2 instance and an admin user | the tutorial uses a real store; a fake one will not show you the real behaviour |
| PostgreSQL 16+ (optional) | required for approvals, the audit trail and async imports; without it those live in memory |
| An MCP client | Claude Desktop, or any client that speaks Streamable HTTP |

Check Go:

```bash
go version   # go1.25 or newer
```

---

## 3. Run the gateway

```bash
git clone https://github.com/shuaiZend/mage2mcp.git
cd mage2mcp
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin
```

Copy the development configuration and start:

```bash
cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml
```

It listens on `127.0.0.1:8080` with **no data source enabled**, so it exposes exactly one
tool. That is deliberate: the shipped example cannot reach anything.

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/readyz    # {"status":"ready","tools":1}
```

`tools: 1` is correct — that one tool is `platform.info`.

### Connect your store

Edit `config.yaml`:

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"                                  # you choose this id
        endpoint: "https://your-store.example.com"
        store_code: "default"
        credential: "${MAGENTO_CREDENTIAL}"
```

Export the credential as an admin token (`admin:<user>:<password>`) or a bearer token, then
restart:

```bash
export MAGENTO_CREDENTIAL='admin:your_admin_user:your_password'
./bin/mage2mcp -config config.yaml
curl -s localhost:8080/readyz    # {"status":"ready","tools":105}
```

The tool count now reflects the Magento surface. `store-us` is the **instance id** — every
tool call carries it, because one gateway can front several stores.

> **Write tools are still off.** `tools.writes: false` is the default. Reads work; nothing can
> change the store yet. Section 7 turns writes on.

---

## 4. Connect an MCP client

The endpoint is `POST /mcp` over Streamable HTTP.

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

Restart the client. It calls `tools/list` on connect and you should see the Magento tools.
The server also sends **instructions** at initialize: read them once, because they state how
to treat tool results (as data, never as instructions) and the rules for copying a product
from another store.

Any other client: point it at the same URL with the Streamable HTTP transport.

---

## 5. Your first reads

Ask your agent, or call the tools directly. These are the four you will use most:

| Tool | What it answers |
|---|---|
| `platform.info` | version, registered tools, configured instances |
| `magento.product.search` | which products match a filter |
| `magento.product.get` | one product, by SKU |
| `magento.order.search` | orders in a window, for a customer, by status |

A search:

```json
{"instance": "store-us", "sku": "24-MB01"}
```

`instance` is required and must name a configured instance — the error is explicit if you
mistype it, which is the point: a typo cannot silently read the wrong store.

> **Two kinds of 404.** `not_found` means the entity is missing. `unsupported_capability`
> means the route does not exist on this store — some Magento builds do not expose every
> endpoint, and the gateway tells you which case you are in. Do not treat them as the same
> thing when debugging.

---

## 6. Five things you will actually do

### 6.1 Find what is out of stock

```text
magento.product.search  {"instance":"store-us", "limit":50}
magento.inventory.status {"instance":"store-us", "sku":"24-MB01"}
```

`inventory.status.qty` is the **legacy** stock figure, not the sum of MSI source items. On an
MSI store it can read 0 while `is_in_stock` is true. Use `magento.inventory.source_items` for
per-source quantities.

### 6.2 Change a price

```text
magento.product.update {"instance":"store-us", "sku":"24-MB01", "price":"19.99"}
```

Read it back to be sure — the gateway returns the updated product, and a price change is
exactly the kind of thing worth confirming:

```text
magento.product.get {"instance":"store-us", "sku":"24-MB01"}
```

### 6.3 Place an order

```text
magento.order.create {
  "instance":"store-us",
  "customer_id": 1,
  "items": [{"sku":"24-MB01","qty":2}],
  "billing_address":  {"firstname":"Ada","lastname":"Lovelace","street":["1 Main St"],
                       "city":"Warsaw","postcode":"00-001","country_id":"PL",
                       "region_id":997,"telephone":"500600700"},
  "shipping_address": {"firstname":"Ada","lastname":"Lovelace","street":["1 Main St"],
                       "city":"Warsaw","postcode":"00-001","country_id":"PL",
                       "region_id":997,"telephone":"500600700"},
  "shipping_carrier_code":"flatrate",
  "shipping_method_code":"flatrate",
  "payment_method":"checkmo",
  "idempotency_key":"order-2026-11-22-001"
}
```

Two things matter here:

- **`region_id` is required for countries with a region directory** (Poland, the US, and many
  others). Without it Magento rejects the address. No region-directory tool is exposed, so take
  the id from the store's admin.
- **Always pass `idempotency_key`.** A write retried without one can duplicate. With one, the
  same call replays the recorded result instead of placing a second order.

If the placement is ambiguous — a timeout, a network drop, a 5xx — the tool answers
`operation_unknown` and tells you **not to retry**. That is not an error to work around: the
order may exist. See section 8.

### 6.4 Refund one item out of three

```text
magento.creditmemo.create {
  "instance":"store-us",
  "order_id": 41,
  "items": [{"order_item_id": 7, "qty": 1}]
}
```

By default a partial refund does **not** refund shipping; the remaining shipping goes back
only when the last units are refunded. To refund shipping explicitly, pass `shipping_amount`.

Read the result's `grand_total` and compare it with what you expected before telling anyone
the refund is done.

### 6.5 Answer "where is my order?"

```text
magento.order.get       {"instance":"store-us", "order_id":41}
magento.shipment.list   {"instance":"store-us", "order_id":41}
magento.order.status_history {"instance":"store-us", "order_id":41}
```

---

## 7. Turning on writes and approving them

Writes are off by default. Turning them on is a deliberate two-part act:

```yaml
tools:
  writes: true

database:
  url: "postgres://mage2mcp:secret@127.0.0.1:5432/mage2mcp?sslmode=disable"
```

The database is not optional here: approvals are stored in PostgreSQL, shared between the
gateway and the admin CLI.

Restart, then make a write:

```text
magento.product.update {"instance":"store-us","sku":"24-MB01","price":"17.99"}
```

It does not execute. It answers:

```json
{"code": "confirmation_required",
 "details": {"confirmation_id": "0eb27457-...", "expires_at": "..."},
 "message": "this operation requires approval by a different human"}
```

A human approves it:

```bash
./bin/mage2mcp-admin -config config.yaml -as you@example.com pending
./bin/mage2mcp-admin -config config.yaml -as you@example.com approve 0eb27457-...
```

Then the agent retries **the same call with `confirmation_id` added**, and it executes.

Three properties are worth knowing:

- The approval is bound to the tool, the instance and a **hash of the arguments**. Change any
  argument and the approval no longer applies.
- It is **single use** and expires (5 minutes, 15 at most).
- The requester cannot approve its own operation.

---

## 8. When something goes wrong

| You see | What it means | What to do |
|---|---|---|
| `operation_unknown` | the write may or may not have landed | **do not retry.** Use `magento.operation.get` to inspect it, then `magento.operation.dispose` with a judgement |
| `confirmation_required` | the write needs a human | approve, then retry with `confirmation_id` |
| `unsupported_capability` | this store does not expose the route | do it in the admin UI; it is not a gateway bug |
| `validation_failed` | Magento rejected the payload | read `details.upstream_field` / `details.upstream_message`; the reason is usually there |
| `rate_limited` | the per-instance budget is exhausted | wait and retry; it is explicitly retryable |
| `permission_denied` | your identity lacks the scope | ask for a higher role |

### The `operation_unknown` workflow

An ambiguous money operation is recorded, not guessed:

```text
magento.operation.get    {"instance":"store-us","operation_id":"46b1322a-..."}
magento.operation.list   {"instance":"store-us","needs_attention":true}
magento.operation.dispose{"instance":"store-us","operation_id":"...",
                          "judgment":"applied","note":"checked /V1/orders/41: exists, increment 000000041"}
```

`judgment` is one of:

- `applied` — you looked upstream and the change is there
- `not_applied` — you looked and it is not; the key is released so a retry is safe
- `requeue` — you have not determined it yet; the reconciler reads upstream again

A `note` is required. The disposition records **who decided what**, because "unknown" means
nobody knows — flipping the state without a judgement would turn uncertainty into a false
certainty.

---

## 9. Where to go next

| If you want to | Read |
|---|---|
| understand every tool | [mcp-tools.md](mcp-tools.md) and the generated [implemented-tools.md](implemented-tools.md) |
| deploy it properly | [operations/deployment.md](operations/deployment.md) and [configuration.md](configuration.md) |
| know what is guaranteed | [mcp-conformance.md](mcp-conformance.md) |
| know what is *not* | [audit/known-limitations.md](audit/known-limitations.md) |
| understand a design decision | [adr/](adr/) |

The [README](../README.md) has the release history and the verification status.
