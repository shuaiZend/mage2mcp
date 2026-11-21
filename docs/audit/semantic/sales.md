# 语义层测试 B：销售与资金链路（sales）

| 项目 | 值 |
|---|---|
| 被测版本 | 生产 v0.11.5（`http://127.0.0.1:18113/mcp`，实例 `demo`，店铺 `https://demo-magento-2.auroracreation.com`，store `pl`，货币 PLN） |
| 测试日期 | 2026-11-22（HKT） |
| 范围 | `magento.order.*`、`magento.invoice.*`、`magento.shipment.*`、`magento.creditmemo.*`、`magento.sales.summary`、`magento.orders.geography`、`magento.tax_classes` |
| 方法 | 通过网关真实调用 + 用同一管理员令牌直接调用 Magento REST 做对照；只记录，不改代码 |
| 环境说明 | demo 站在测试期间至少两次重置：此前创建的订单/发票/贷项通知单被清空。文中金额与输出均为**当时**的真实返回；重置后无法复读的实体已在证据中注明 |

所有调用使用仓库自带 harness：

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18113/mcp MCP_INSTANCE=demo \
MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
go run -tags live ./test/live -mode=raw -tool=<tool> -args='{...}'
```

## 结论摘要

| ID | 级别 | 工具 | 一句话 | 状态 |
|---|---|---|---|---|
| SEM-SALES-01 | **P0** | `magento.creditmemo.create` | 部分退款请求被放大成**整张发票全额退款**（qty 1/2 → 退 83.00 而非 44.00） | CONFIRMED |
| SEM-SALES-02 | **P1** | `magento.order.cancel` | 已开票订单取消：Magento 返回 200+`false`，工具却报 `canceled:true`，订单仍是 processing | CONFIRMED |
| SEM-SALES-03 | **P1** | `magento.order.create` | 地址模型无 `region_id`，PL 店铺下单在 place_order 必然 400；错误被泛化，调用方无法自查 | CONFIRMED |
| SEM-SALES-04 | P2 | 全部写工具 | Magento 400 的 `message`/`parameters` 被丢弃，只剩 `validation_failed` + “verify store_code and field values” | CONFIRMED |
| SEM-SALES-05 | P2 | `magento.invoice.create` | `capture=false` 在离线支付方式下仍生成**已支付**发票（state 2），描述却写“open invoice” | CONFIRMED |
| SEM-SALES-06 | P2 | `magento.sales.summary` / `magento.orders.geography` | `revenue` 把已取消订单计入（234 含取消的 39），无状态过滤 | SUSPECTED |
| SEM-SALES-07 | P3 | 资金类写工具 | `idempotency_key` 永久全局占用：失败操作的同 key 永久 `idempotency_key_reuse`，无 TTL | CONFIRMED |
| SEM-SALES-08 | P3 | `magento.creditmemo.create` | 输出 `total_qty: 0`，而贷项通知单实际有 2 件（Magento 响应无该字段，零值外泄） | CONFIRMED |
| SEM-SALES-09 | P3 | 全部写工具 | 路由脱敏把动作段也替换：`op: magento POST /order/{id}/{id}`，无法区分 invoice/ship/refund | CONFIRMED |

---

## SEM-SALES-01: 部分退款被放大为整张发票全额退款

- **严重级别**: P0
- **工具**: `magento.creditmemo.create`
- **现象**: 对一张 2 件、grand_total 83.00 的发票请求“退 1 件”，工具成功返回，但贷项通知单是**整张发票**：

```text
$ ... -tool=magento.creditmemo.create -args='{"instance":"demo","order_id":3,"invoice_id":3,
    "items":[{"order_item_id":5,"qty":1}],"idempotency_key":"sem-t54-cm-2"}'
is_error=false code=
{
  "creditmemo": {"entity_id": 2, "order_id": 3, "state": 2,
    "grand_total": 83, "subtotal": 68, "shipping_amount": 15, "total_qty": 0},
  "invoice_id": 3, "operation_id": "716a7582-..."
}
```

  直接读贷项通知单，明细是 **qty 2**：

```text
$ curl ... /rest/pl/V1/creditmemo/2
cm2 grand 83 subtotal 68 shipping 15
items: [(5, 2, 68)]
```

  对照：同一店铺、同样 2 件发票（grand 78.00），直接以**正确报文**请求退 1 件，得到真正的部分退款 44.00（34.00 商品 + 10.00 运费）：

```text
$ curl -X POST .../V1/order/4/refund -d '{"items":[{"order_item_id":6,"qty":1}],
    "notify":false,"appendComment":false,"arguments":{}}'
"3"   HTTP 200
cm3 grand 44 subtotal 34 shipping 10
items: [(6, 1, 34)]
```

  此前另一次同样现象（重置前）：发票 grand 89.40（2 件 + 运费 35.00），请求 qty 1，返回贷项 `grand_total 89.4 / total_qty 0`。

- **期望**: 工具描述与参数契约：`items` 1–100 行，按 `qty` 退款；本地 `checkRefundable` 已确认 1 ≤ 2，说明工具“认为”自己在退 1 件。
- **证据**:
  1. `internal/magento/order_documents_write.go` 把请求体包成 `{"entity": {...}}` 发往 `POST /order/{id}/refund`；该路由（`RefundOrderInterface`）的参数在**顶层**（`items`/`notify`/`appendComment`/`arguments`），`entity` 包裹层被忽略 → Magento 收到空 `items` → 默认退整张发票。
  2. 上面的正/误报文对照（83.00 vs 44.00，qty 2 vs qty 1）是决定性证据。
- **影响**: 任何“部分退款”都会变成全额退款：商户实际多退钱（本例多退 39.00 = 83.00 − 44.00），且订单 `total_refunded` 与发票余额被一次性耗尽；后续真正的部分退款会因“0 refundable”被拒。属于资金损失类缺陷，必须在发布前修复。
- **状态**: **CONFIRMED**（网关与直连对照均已复现；demo 重置前的一次 89.40 同现象可佐证）

---

## SEM-SALES-02: 已开票订单取消“静默假成功”

- **严重级别**: P1
- **工具**: `magento.order.cancel`
- **现象**: 对一张已开票（invoice state 2，total_paid 78.00）且已部分退款的订单执行取消，工具报成功：

```text
$ ... -tool=magento.order.cancel -args='{"instance":"demo","order_id":4,"idempotency_key":"sem-t54-cancel-1"}'
is_error=false code=
{"order_id": 4, "canceled": true, "operation_id": "c1025ccb-..."}
```

  但订单状态没有变化：

```text
$ curl ... /V1/orders/4
state = processing
total_paid = 78   total_refunded = 44   total_invoiced = 78
invoices: inv 4 state 2 grand 78
```

  直接调用同一路由，Magento 的响应是 **HTTP 200 + body `false`**：

```text
$ curl -X POST .../V1/orders/4/cancel -w '\nHTTP %{http_code}'
false
HTTP 200
```

- **期望**: 已开票/已付款订单不可取消（Magento 语义），工具应返回 typed conflict，或至少校验返回的布尔值后报失败。
- **证据**: 网关把任何 2xx 当成功（`classifyOperationError(nil) = applied`），未解析 `false` 响应体；台账随即 `run.Succeed("order:4")`，留下“已成功取消”的永久记录。同一工具对**未开票**订单行为正确：新建订单首次取消 `canceled:true` 且状态变 `canceled`，再次取消返回 `conflict` + `order_state: canceled`。
- **影响**: 调用方相信订单已取消（可能据此停止发货、释放库存、通知客户），实际订单仍 processing、钱已收；台账记录与事实相反。属于状态机“静默成功”，是资金/履约链路的高风险缺陷。
- **状态**: **CONFIRMED**

---

## SEM-SALES-03: `order.create` 在 PL 店铺必然失败——地址模型缺少 `region_id`

- **严重级别**: P1
- **工具**: `magento.order.create`
- **现象**: 按工具 schema 提供的完整地址（`country_id: PL`、城市、邮编，含 `region` 文本）下单，在 `place_order` 阶段失败：

```text
$ ... -tool=magento.order.create -args='{...,"billing_address":{...,"country_id":"PL"},
    "shipping_address":{...,"country_id":"PL"},...}'
is_error=true code=validation_failed
{"code":"validation_failed","details":{"cart_cleanup":"failed: not_found","stage":"place_order",
 "store_code":"pl","upstream_status":"400"},
 "message":"Magento write failed at stage place_order: Magento rejected the request as invalid;
 verify store_code and field values; completed writes are not rolled back and require compensation"}
```

  直连同一流程，Magento 给出真正原因：

```text
$ curl -X PUT .../V1/carts/13/order -d '{"paymentMethod":{"method":"checkmo"}}'
{"message":"Proszę sprawdzić adres dostawy. %1",
 "parameters":["\"regionId\" is required. Enter and try again."]}
HTTP 400
```

  补上 `region_id: 997`（PL dolnośląskie）后同一报文立即成功：

```text
$ curl -X POST .../carts/{id}/shipping-information -d '{...,"region_id":997}'
shipping 200
$ curl -X PUT .../carts/{id}/order -d '{"paymentMethod":{"method":"checkmo"}}'
5
PUT 200
```

- **期望**: 工具应能表达 Magento 地址模型（含 `region_id`），或在 schema/描述中说明“region 下拉国家必须提供 region_id”；失败时应带出上游原因。
- **证据**: `order.createSchema` 的地址对象只有 `region`（字符串），无 `region_id`/`regionId`；`addressPayload` 同样没有该字段。因此 PL（以及所有 region 为下拉的国家）的订单无法通过该工具创建。另有附带问题：失败后 `cart_cleanup: failed: not_found`——`DELETE /V1/carts/{id}` 在该店铺也是路由不存在，失败尝试会遗留购物车（本次测试中直接观察到 cleanup 失败且店铺中残留多个购物车）。
- **影响**: 该店铺（以及同类国家）的“下单”能力实际不可用；agent 反复重试只会得到同一条泛化错误，并不断遗留购物车。对生产可用性是一级缺陷（业务主流程失败），但不是资金损失。
- **状态**: **CONFIRMED**（网关 + 直连双向复现；修复方向：地址模型增加 `region_id`，失败时透出上游原因）

---

## SEM-SALES-04: 400 的上游原因被丢弃，调用方无法自查

- **严重级别**: P2
- **工具**: 所有写工具（示例：`magento.order.create`、`magento.shipment.create`）
- **现象**: Magento 的 400 响应体含 `message` + `parameters`（字段级原因），工具输出只有：

```text
{"code":"validation_failed","details":{"store_code":"pl","upstream_status":"400"},
 "message":"Magento rejected the request as invalid; verify store_code and field values",
 "op":"magento POST /order/{id}/{id}"}
```

- **期望**: 至少给出可操作的原因（如 `regionId is required`、`item already shipped`），或把上游 message 作为受限字段返回。
- **证据**: 三个独立场景（下单缺 regionId、对已发货订单再发货、取消已开票订单）都只得到同一句泛化文案；`upstream_message` 细节不再出现在返回中（t18 的统一错误信封将其剥离）。同时 `op` 字段被脱敏成 `/order/{id}/{id}`，连“哪个动作”都看不出来（见 SEM-SALES-09）。
- **影响**: agent 无法自我纠正，只能盲目重试或升级人工；排障成本高。建议在 `details` 中返回**受限、去参数化**的上游 message（或错误码映射表），而不是全部丢弃。
- **状态**: **CONFIRMED**

---

## SEM-SALES-05: `capture=false` 仍生成“已支付”发票

- **严重级别**: P2
- **工具**: `magento.invoice.create`
- **现象**: 传 `capture:false` 开票，返回 `captured: true`，发票 state 2（Paid），订单转为 processing：

```text
$ ... -tool=magento.invoice.create -args='{"instance":"demo","order_id":5,
    "items":[{"order_item_id":5,"qty":2}],"capture":false,"idempotency_key":"sem-t54-inv-1"}'
is_error=false code=
{"invoice": {"entity_id": 5, "state": 2, "grand_total": 89.4, ...},
 "captured": true, "operation_id": "d95f7504-..."}
```

  直连 Magento 用同样的 `capture:false` 也是 state 2、订单 `total_paid 39`：

```text
$ curl -X POST .../V1/order/6/invoice -d '{"capture":false,"items":[...]}'
"6"
$ curl .../V1/invoices/6
state 2  grand 39
$ curl .../V1/orders/6
state processing  total_paid 39
```

- **期望**: 工具描述写“capture=false records an open invoice and charges nothing”。对离线支付方式（checkmo）Magento 的行为是“无款可收 → 直接标记已付”，因此 `captured` 字段本身是**诚实的**（true），但描述里的“open invoice”不成立。
- **证据**: 网关与直连一致；差异只在文档语义。对在线支付网关（真实扣款）行为可能不同，本环境无法验证。
- **影响**: 依赖“capture=false 先开票后收款”的 agent 会误判订单已收款；但由于 `captured` 字段正确，影响可控。建议把描述改为“capture=false 跳过支付网关扣款；离线支付方式下发票仍会立即标记为已支付，请以 captured 字段为准”。
- **状态**: **CONFIRMED**（文档与行为不符；字段本身正确）

---

## SEM-SALES-06: `sales.summary` / `orders.geography` 把已取消订单计入 revenue

- **严重级别**: P2
- **工具**: `magento.sales.summary`、`magento.orders.geography`
- **现象**: 窗口内 3 张订单（117 + 78 + 39），其中 39 的订单已被取消，但汇总仍计 234：

```text
$ ... -tool=magento.sales.summary -args='{"instance":"demo","from":"2026-11-21","to":"2026-11-22"}'
{"summary":{"total_orders":3,"total_revenue":234,"total_items":6,"currency":"PLN",
 "total_available":3,"scanned_orders":3,"excluded_orders":0}}
$ ... -tool=magento.orders.geography -args='{"instance":"demo","from":"2026-11-21","to":"2026-11-22"}'
{"geography":{"orders_scanned":3,"total_revenue":234,"countries":[{"country_id":"PL","orders":3,"revenue":234}]}}
$ ... -tool=magento.order.search -args='{"instance":"demo","created_from":"2026-11-21","created_to":"2026-11-22"}'
3 processing 117 / 4 processing 78 / 5 canceled 39  → sum 234
```

- **期望**: 字段名为 `revenue`，但工具没有状态过滤参数；取消订单是否应计入需要明确。若按“毛销售额”口径则应改名为 `gross_order_total` 或在描述中写明包含取消单。
- **证据**: 234 = 117 + 78 + 39，取消单 39 被计入；`excluded_orders: 0` 只反映分页截断，不反映状态过滤。
- **影响**: 用该指标做经营判断会高估收入（本例高估 39/234 ≈ 16.7%）。属于口径问题，非计算错误；建议明确口径或增加 `status`/`exclude_canceled` 参数。
- **状态**: **SUSPECTED**（行为已确认；“是否缺陷”取决于产品口径，需产品/文档决策）

---

## SEM-SALES-07: `idempotency_key` 永久全局占用，无 TTL

- **严重级别**: P3
- **工具**: 资金类写工具（`order.create`、`invoice.create`、`creditmemo.create`、`invoice.capture`、`order.cancel`）
- **现象**: 复用上一轮测试用过的 key（`sem-sales-order-1`），即使参数已不同，永久返回：

```text
is_error=true code=idempotency_key_reuse
{"code":"idempotency_key_reuse","details":{"operation_id":"abb7b2a3-...","state":"failed"},
 "message":"idempotency key was already used with different arguments"}
```

  该记录是**上一轮**（demo 重置前）失败操作留下的；demo 数据已清空，台账行仍存在并继续阻断该 key。

- **期望**: 幂等键按 `(instance, tool, key)` 唯一是设计（§6.3）；但失败行是否应永久保留、是否需要 TTL/清理，应在文档中说明。
- **证据**: 台账在网关 PostgreSQL 中，与 demo 店铺数据生命周期无关；`state: failed` 的行没有过期时间字段被工具使用。
- **影响**: 调用方（尤其自动化 agent）若复用可预测的 key（如 `order-1`），会被永久拒绝且无法自行恢复；建议文档要求 key 使用 UUID，并考虑对失败行设置保留期。
- **状态**: **CONFIRMED**（设计后果；非缺陷，但需文档说明）

---

## SEM-SALES-08: 贷项通知单输出 `total_qty: 0` 与事实不符

- **严重级别**: P3
- **工具**: `magento.creditmemo.create` / `magento.creditmemo.list`
- **现象**: 实际含 2 件商品的贷项通知单，工具输出 `total_qty: 0`：

```text
{"creditmemo":{"entity_id":2,"grand_total":83,"subtotal":68,"shipping_amount":15,"total_qty":0}}
直连 /V1/creditmemo/2 → items: [(5, 2, 68)]，响应中无 total_qty 字段
```

- **期望**: 缺失字段应省略（`omitempty`）或置 `null`，而不是输出 0；`total_qty: 0` 会被读成“没有退任何商品”。
- **证据**: Magento 的 creditmemo REST 响应不含 `total_qty`，Go 零值 0 被序列化。
- **影响**: 与 SEM-SALES-01 叠加时会误导调用方以为“没有退商品”，实际退了 2 件。属于输出诚实性问题。
- **状态**: **CONFIRMED**

---

## SEM-SALES-09: 路由脱敏把动作段也替换成 `{id}`

- **严重级别**: P3
- **工具**: 所有写工具的错误 `op` 字段
- **现象**: 对 `/order/1/ship` 的失败，`op` 显示为：

```text
"op": "magento POST /order/{id}/{id}"
```

- **期望**: 动作段（`invoice`/`ship`/`refund`/`capture`）应保留，只有标识符被脱敏。
- **证据**: `internal/magento/client.go` 的 `routeWords` 允许表包含 `invoices`（复数）但**不含** `invoice`、`ship`、`refund`，因此这三个动作被当作标识符替换。
- **影响**: 排障时无法从错误中判断失败动作；对资金类操作尤其不利。修复只需在允许表补三个词。
- **状态**: **CONFIRMED**

---

## NOT-A-DEFECT（已验证正确，记录以免重复排查）

| 行为 | 证据 |
|---|---|
| `invoice.create` 同 key 重放 | 第二次调用 `replayed: true`、`operation_id` 相同、发票内容逐字段相同，未产生第二张发票 |
| `creditmemo.create` 同 key 重放 | 第二次调用 `replayed: true`、`operation_id` 相同、内容相同（注意：重放的是 SEM-SALES-01 的错误结果，重放机制本身正确） |
| 已支付发票再次 capture | `conflict` + `invoice_state: 2` + `no capture request was sent`，未重复扣款 |
| 已取消订单再次取消 | `conflict` + `order_state: canceled`，未重复请求 |
| 超额退款 | 本地拒绝：`invalid_argument: order item 6 has 1 refundable units, cannot refund 2`，无上游调用 |
| 金额计算 | 发票 83.00 = 68.00 + 15.00；部分退款 44.00 = 34.00 + 10.00；订单 117.00 = 102.00 + 15.00；全部为 2 位小数、PLN |
| `sales.summary` 与 `orders.geography` 一致 | 同一窗口均为 3 单 / 234.00 / 6 件；`mixed_currency:false`、`truncated:false`、`unscanned_orders:0` |
| `magento.order.summary` 弃用标记 | 返回 `deprecated:true` + `use: magento.sales.summary`，诚实 |
| `magento.tax.classes` | 返回 Taxable Goods(2)/Retail Customer(3)，与店铺配置一致 |
| `order.status_history` | 无评论的订单返回空数组（未伪造记录） |

## 未覆盖 / 环境限制

1. **`operation_unknown` 语义未实测**：demo 站无故障注入，无法制造超时/5xx 来观察 UNKNOWN 落库与返回。代码层已有对应测试，但本环境无法端到端验证“ambiguous → unknown 而非 failed”。
2. **多行部分成功未实测**：发票/发货/退款的多行“一行失败其余成功”场景，因 demo 频繁重置与 SEM-SALES-01 的整单退款行为，未能稳定构造；单行路径已覆盖。
3. **邮件类工具未调用**（`order.email`、`invoice.email`、`shipment.email`、`creditmemo.email`）：避免向 demo 客户真实发信；未验证返回值语义。
4. **在线支付网关的 capture 语义**：demo 仅启用 checkmo（离线），`capture=true/false` 的真实扣款差异无法验证。
5. **demo 重置**：测试期间订单 3–7、发票 5–7、贷项 5 被清空，部分实体无法二次复读；文中金额均为当时真实输出。
