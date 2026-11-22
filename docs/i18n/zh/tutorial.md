<!-- translated-from: docs/tutorial.md @ 0326e3c -->

# 教程：从零到可用的 Magento agent

本教程带你从一台空机器走到一个能读取真实店铺、修改价格、下单并退款的 agent，途中会
实际走一遍审批闸门。

阅读不需要任何 mage2mcp 前置知识。下面每条命令与每次工具调用，都是撰写时对真实 Magento
店铺跑过的，输出是真实的。

其他语言：[English](../../tutorial.md) · [Español](../es/tutorial.md) · [Nederlands](../nl/tutorial.md)。

---

## 1. 你在搭什么

```text
your agent  ──MCP (Streamable HTTP)──▶  mage2mcp  ──REST──▶  Magento 2
                                            │
                                            └──▶ PostgreSQL（审计、审批、任务）
```

mage2mcp 是一个 MCP 服务器。agent 用 MCP 与它对话，它用 REST 与 Magento 对话。你不必写
Magento 的 REST 调用，agent 也从不持有 Magento 凭据。

---

## 2. 开始之前

| 你需要 | 为什么 |
|---|---|
| Go 1.25 或更新 | 构建两个二进制 |
| 一个 Magento 2 实例与管理员账号 | 教程使用真实店铺；假店铺看不到真实行为 |
| PostgreSQL 16+（可选） | 审批、审计轨迹与异步导入需要它；没有则存在内存里 |
| 一个 MCP 客户端 | Claude Desktop，或任何支持 Streamable HTTP 的客户端 |

检查 Go：

```bash
go version   # go1.25 或更新
```

---

## 3. 启动网关

```bash
git clone https://github.com/shuaiZend/mage2mcp.git
cd mage2mcp
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin
```

复制开发配置并启动：

```bash
cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml
```

它监听 `127.0.0.1:8080`，**未启用任何数据源**，所以只暴露一个工具。这是有意的：随包提供的
示例配置连不到任何东西。

```bash
curl -s localhost:8080/healthz   # {"status":"ok"}
curl -s localhost:8080/readyz    # {"status":"ready","tools":1}
```

`tools: 1` 是正确的——那一个工具就是 `platform.info`。

### 接入你的店铺

编辑 `config.yaml`：

```yaml
datasources:
  magento:
    enabled: true
    instances:
      - id: "store-us"                                  # 这个 id 由你命名
        endpoint: "https://your-store.example.com"
        store_code: "default"
        credential: "${MAGENTO_CREDENTIAL}"
```

把凭据导出为管理员令牌（`admin:<user>:<password>`）或 bearer 令牌，然后重启：

```bash
export MAGENTO_CREDENTIAL='admin:your_admin_user:your_password'
./bin/mage2mcp -config config.yaml
curl -s localhost:8080/readyz    # {"status":"ready","tools":105}
```

工具数现在反映 Magento 的能力面。`store-us` 是**实例 id**——每次工具调用都携带它，因为一个
网关可以对接多个店铺。

> **写工具仍然是关闭的。** 默认 `tools.writes: false`。读操作可用，但还不能改动店铺。第 7 节
> 会打开写。

---

## 4. 接入 MCP 客户端

端点是 Streamable HTTP 的 `POST /mcp`。

Claude Desktop（`claude_desktop_config.json`）：

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

重启客户端。它会在连接时调用 `tools/list`，你应该能看到 Magento 工具。服务器还会在
initialize 时发送 **instructions**：请读一遍，因为它说明了如何对待工具结果（当作数据，
绝不当作指令）以及从别的店铺复制商品的规则。

其他客户端：用同一 URL，传输方式选 Streamable HTTP。

---

## 5. 第一次读取

让 agent 去查，或直接调用工具。最常用的四个：

| 工具 | 它回答什么 |
|---|---|
| `platform.info` | 版本、已注册工具、已配置实例 |
| `magento.product.search` | 哪些商品匹配某个过滤条件 |
| `magento.product.get` | 按 SKU 取一个商品 |
| `magento.order.search` | 某时间窗、某客户、某状态的订单 |

一次搜索：

```json
{"instance": "store-us", "sku": "24-MB01"}
```

`instance` 是必填的，且必须指向已配置的实例——写错时错误信息是明确的，这正是要点：拼错不能
静默读到别的店铺。

> **两种 404。** `not_found` 表示实体不存在；`unsupported_capability` 表示该店铺没有暴露这个
> 路由——有些 Magento 构建并不开放全部端点，网关会告诉你是哪一种。排障时不要把两者当成
> 一回事。

---

## 6. 你实际会做的五件事

### 6.1 找出缺货商品

```text
magento.product.search  {"instance":"store-us", "limit":50}
magento.inventory.status {"instance":"store-us", "sku":"24-MB01"}
```

`inventory.status.qty` 是**legacy** 库存数字，不是 MSI 源项之和。在 MSI 店铺里它可能为 0 而
`is_in_stock` 为 true。要按源查询，用 `magento.inventory.source_items`。

### 6.2 改价格

```text
magento.product.update {"instance":"store-us", "sku":"24-MB01", "price":"19.99"}
```

回读确认——网关会返回更新后的商品，而改价恰恰是最值得确认的一类操作：

```text
magento.product.get {"instance":"store-us", "sku":"24-MB01"}
```

### 6.3 下单

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

这里有两点要紧：

- **对有行政区目录的国家（波兰、美国等），`region_id` 是必填的。** 缺了它 Magento 会拒绝该
  地址。网关未暴露地区查询工具，请从店铺后台取该 id。
- **始终传 `idempotency_key`。** 不带 key 的重试可能产生重复。带了 key，同样的调用会重放已
  记录的结果，而不是再下一单。

如果下单结果不明确——超时、连接中断、5xx——工具会返回 `operation_unknown` 并明确告诉你
**不要重试**。这不是需要绕过的错误：订单可能已经存在。见第 8 节。

### 6.4 三件商品里退一件

```text
magento.creditmemo.create {
  "instance":"store-us",
  "order_id": 41,
  "items": [{"order_item_id": 7, "qty": 1}]
}
```

默认情况下，部分退款**不退**运费；只有退最后几件时剩余运费才一并退回。要显式退运费，传
`shipping_amount`。

在告诉任何人「退款已完成」之前，读返回的 `grand_total` 并与你的预期核对。

### 6.5 回答「我的订单到哪了？」

```text
magento.order.get       {"instance":"store-us", "order_id":41}
magento.shipment.list   {"instance":"store-us", "order_id":41}
magento.order.status_history {"instance":"store-us", "order_id":41}
```

---

## 7. 打开写操作并审批

写操作默认关闭。打开它需要刻意的两步：

```yaml
tools:
  writes: true

database:
  url: "postgres://mage2mcp:secret@127.0.0.1:5432/mage2mcp?sslmode=disable"
```

这里的数据库不是可选项：审批存在 PostgreSQL 里，由网关与管理 CLI 共享。

重启后发起一次写：

```text
magento.product.update {"instance":"store-us","sku":"24-MB01","price":"17.99"}
```

它不会执行，而是回答：

```json
{"code": "confirmation_required",
 "details": {"confirmation_id": "0eb27457-...", "expires_at": "..."},
 "message": "this operation requires approval by a different human"}
```

由人来批准：

```bash
./bin/mage2mcp-admin -config config.yaml -as you@example.com pending
./bin/mage2mcp-admin -config config.yaml -as you@example.com approve 0eb27457-...
```

然后 agent **带 `confirmation_id` 重试同一个调用**，它才会执行。

有三个性质值得知道：

- 审批绑定到工具、实例与**参数哈希**。改动任何一个参数，审批即失效。
- 审批**一次性**且有有效期（5 分钟，最长 15 分钟）。
- 发起方不能批准自己的操作。

---

## 8. 出问题时

| 你看到 | 含义 | 怎么办 |
|---|---|---|
| `operation_unknown` | 写操作可能生效也可能没有 | **不要重试。** 用 `magento.operation.get` 查它，再用 `magento.operation.dispose` 给出判断 |
| `confirmation_required` | 写操作需要人工批准 | 批准后带 `confirmation_id` 重试 |
| `unsupported_capability` | 该店铺没有暴露这个路由 | 到后台操作；这不是网关的缺陷 |
| `validation_failed` | Magento 拒绝了该报文 | 看 `details.upstream_field` / `details.upstream_message`，原因通常在那里 |
| `rate_limited` | 该实例的预算已用尽 | 等待后重试；它被明确标记为可重试 |
| `permission_denied` | 你的身份缺少该 scope | 申请更高角色 |

### `operation_unknown` 的处理流程

结果不明确的资金操作会被记录下来，而不是猜测：

```text
magento.operation.get    {"instance":"store-us","operation_id":"46b1322a-..."}
magento.operation.list   {"instance":"store-us","needs_attention":true}
magento.operation.dispose{"instance":"store-us","operation_id":"...",
                          "judgment":"applied","note":"checked /V1/orders/41: exists, increment 000000041"}
```

`judgment` 取值：

- `applied` —— 你查了上游，变更在
- `not_applied` —— 你查了，变更不在；key 会被释放，重试是安全的
- `requeue` —— 你还没查清；对账器会重新读取上游

`note` 是必填的。处置记录的是**谁做了什么判断**，因为「unknown」意味着没人知道——不写判断就
翻转状态，等于把不确定洗成虚假的确定。

---

## 9. 接下来读什么

| 如果你想 | 读 |
|---|---|
| 了解每个工具 | [mcp-tools.md](../../mcp-tools.md) 与自动生成的 [implemented-tools.md](../../implemented-tools.md) |
| 正式部署 | [operations/deployment.md](../../operations/deployment.md) 与 [configuration.md](../../configuration.md) |
| 知道哪些是保证的 | [mcp-conformance.md](../../mcp-conformance.md) |
| 知道哪些**不是** | [audit/known-limitations.md](../../audit/known-limitations.md) |
| 理解某个设计决策 | [adr/](../../adr/) |

[README](../../../README.md) 里有发布历史与验证状态。
