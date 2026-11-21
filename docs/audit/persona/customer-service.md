# 客服岗位场景测试（v0.11.6）

我的日常任务：顾客投诉进来，我查订单、看物流、办退款、改地址、取消订单。
我只有一条底线：**我看到的必须就是顾客看到的那回事，退款金额一分都不能错**。
工具报错我能解释给顾客；工具说成功但实际没生效、或者悄悄多退/少退了钱，我会直接变成客诉和账差。

## 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | v0.11.6（`platform.info` 实测） |
| 实例 id | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store `pl`（PLN） |
| 付款方式（本店 demo 数据） | `checkmo`（线下汇款，无在线扣款） |
| 数据快照 | `2026-11-21T19:32:42Z`（下文所有金额/件数都绑定这个时刻） |

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
go run -tags live ./test/live -mode=raw -tool=magento.order.get -args='{"instance":"demo","order_id":31}'
```

> 下文命令均用上面的规范形式；我实测时把它预编译成 `/tmp/mcpraw` 并包了一层 `/tmp/mcp.sh`
> （行为完全一致：同一 MCP 端点、同一 approval 命令、同样的 `-mode=raw`）。
> 每条结论都用**不经过 MCP 的 REST 直连**独立复核：
> `curl -H "Authorization: Bearer $(cat /tmp/magento_token)" https://demo-magento-2.auroracreation.com/rest/pl/V1/...`

### 数据卫生（便于回归复现）

本轮我在 demo 站留下了这些可核对的数据（其他岗位 agent 同时在动同一批订单，见文末「数据漂移」）：

| 对象 | 用途 |
|---|---|
| order 8 / 26 / 31、invoice 21 / 24 / 27 | 退款与发货场景的干净样本 |
| creditmemo 17（order 8，54.00）、19（order 26，22.00）、20（order 15，34.00） | 退款金额核对 |
| order 27 | 取消场景 |
| customer 1 的 address 9 | 改地址场景（`ul. Nowa 12/3 / Wroclaw / 00-001` → `ul. Zmieniona 99 / Gdansk / 80-001`） |

---

## 一、五个客诉动作的核对结果

### 1) 顾客说「我没收到货」→ 查订单状态与物流 ❌ 看不到「哪件发了」

顾客原话：「我买了两件，只收到一个包。」我用订单号 `000000031` 查单（`order.search` 支持
`increment_id`，这点很好用），然后看发货记录：

```bash
# 按顾客报的订单号查单
... -tool=magento.order.search -args='{"instance":"demo","increment_id":"000000031"}'
# 看发货记录
... -tool=magento.shipment.list -args='{"instance":"demo","order_id":31}'
```

MCP 给我的全部信息：

```json
{"order": {"entity_id": 31, "increment_id": "000000031", "status": "processing", "state": "processing",
  "grand_total": 41.5, "subtotal": 31.5, "shipping_amount": 10, "total_qty_ordered": 2,
  "items": [
    {"item_id": 37, "sku": "24-MB01", "name": "Joust Duffle Bag", "qty_ordered": 1, "price": 29, "row_total": 29},
    {"item_id": 38, "sku": "24-MB04", "name": "Strive Shoulder Pack", "qty_ordered": 1, "price": 2.5, "row_total": 2.5}]}}
```

```json
{"shipments": [{"entity_id": 19, "increment_id": "000000019", "order_id": 31, "total_qty": 1,
  "tracks": [{"title": "DPD", "track_number": "DPD-556677", "carrier_code": "custom"}]}],
 "total": 1, "truncated": false}
```

同一时刻 REST 直连的真相：

```text
order 31: status processing
  [{'sku': '24-MB01', 'qty_ordered': 1, 'qty_invoiced': 1, 'qty_shipped': 1, 'qty_refunded': 0},
   {'sku': '24-MB04', 'qty_ordered': 1, 'qty_invoiced': 1, 'qty_shipped': 0, 'qty_refunded': 0}]
shipment 19 items: [{'sku': '24-MB01', 'qty': 1}]
```

- ✅ **运单号看得到**：`shipment.get` / `shipment.list` 都带 `tracks`，我能直接报给顾客。
- ❌ **发的是哪一件看不到**：`order.get` 只有 `qty_ordered`，没有 `qty_shipped`；`shipment.list`
  只有 `total_qty: 1`，没有 `items`（上游 shipment 19 明确有 `items: [{sku: 24-MB01, qty: 1}]`）。
  所以「DPD-556677 里装的是哪个包、还有哪件没发」我只能靠猜——猜错就是二次客诉。
- ❌ **退了几件也看不到**：order 26 上游 `qty_refunded=1`，`order.get` 里没有任何对应字段，
  我会误以为顾客手上还是 2 件。

→ 见 **P-CS-04**。

### 2) 顾客要退其中一件 → 办部分退款，核对金额与件数 ❌ 金额多退了运费

样本 order 26（干净、两件不同 SKU，顾客要退 `downolable` 1 件）：

```bash
... -tool=magento.order.get -args='{"instance":"demo","order_id":26}'
# 开票（退款前提）
... -tool=magento.invoice.create -args='{"instance":"demo","order_id":26,"items":[{"order_item_id":30,"qty":1},{"order_item_id":31,"qty":2}]}'
# 只退 downolable 1 件（17.00 PLN）
... -tool=magento.creditmemo.create -args='{"instance":"demo","order_id":26,"items":[{"order_item_id":31,"qty":1}]}'
```

```json
{"creditmemo": {"entity_id": 19, "increment_id": "000000019", "order_id": 26, "state": 2,
  "grand_total": 22, "subtotal": 17, "discount_amount": 0,
  "shipping_amount": 5, "tax_amount": 0, "adjustment": 0,
  "total_qty": 1, "order_currency_code": "PLN"},
 "invoice_id": 24, "operation_id": "fd4c7432-..."}
```

**我要退的是 17.00，工具退给顾客的是 22.00**——多出来的 5.00 是整单运费，而这单的另外 2 件商品
（24-MB01 ×1 已发、downolable ×1）顾客都还留着（上游：24-MB01 `qty_shipped=1`；downolable
`qty_ordered=2, qty_refunded=1`）。

- 件数**是对的**：`total_qty: 1`；上游 creditmemo 19 的 `items: [{sku: downolable, qty: 1, row_total: 17}]`。
- 金额**不对**：`grand_total: 22` = 17 商品 + 5 全部运费。
- 这不是 MCP 算错，是它把 Magento 的默认口径原样带了出来：我用 REST 直连退 order 12 的 1 件
  17.00（请求里**没有任何运费字段**），拿到的 creditmemo 16 也是 `grand_total 27 = 17 + 10 运费`。
  问题在于 **`creditmemo.create` 没有 `shipping_amount` 参数**（schema 里只有
  `adjustment_positive` / `adjustment_negative`），既不给我控制权，也不在响应/确认里提醒我
  「本单含运费 5.00」，而它自己内部记账只算了商品行：

```bash
ssh ubuntu@49.51.250.83 'cd /opt/mage2mcp && sudo docker compose exec -T postgres psql -U mage2mcp -d mage2mcp \
 -c "select operation, entity_id, amount_minor, currency, state, upstream_ref from operations where operation='"'"'magento.creditmemo.create'"'"' and entity_id in ('"'"'21'"'"','"'"'24'"'"') order by created_at;"'
```

```text
 operation                 | entity_id | amount_minor | currency | state     | upstream_ref
---------------------------+-----------+--------------+----------+-----------+----------------------
 magento.creditmemo.create | 21        |         3400 | PLN      | succeeded | creditmemo:000000017   ← order 8，实际退 54.00
 magento.creditmemo.create | 24        |         1700 | PLN      | succeeded | creditmemo:000000019   ← order 26，实际退 22.00
```

order 8 同样：退 1 件 34.00 → creditmemo 17 实退 **54.00**（34 + 20 运费），账本记 **34.00**。
一次少记 20.00，一次少记 5.00。→ 见 **P-CS-01**。

**可用的绕法**（已验证，但得我自己算运费）：`adjustment_negative` 等于运费即可把运费抵掉：

```bash
... -tool=magento.creditmemo.create -args='{"instance":"demo","order_id":15,"items":[{"order_item_id":18,"qty":1}],"adjustment_negative":10}'
```

```json
{"creditmemo": {"entity_id": 20, "grand_total": 34, "subtotal": 34, "shipping_amount": 10, "adjustment": -10, "total_qty": 1}}
```

（`grand_total` 34 正确；但响应里仍写着 `shipping_amount: 10`，只看这一个字段会以为运费退了。）

**其余退款行为是对的**（逐条核过）：

| 检查 | 命令 | 结果 |
|---|---|---|
| 重复点「退款」不会重复退 | 同参数再调 `creditmemo.create`（无 idempotency_key） | `"replayed": true`，creditmemo 仍只有 1 条，上游 `total_refunded` 仍 54.00 ✅ |
| 超退被本地拦住 | `items:[{"order_item_id":10,"qty":99}]` | `invalid_argument: order item 10 has 3 refundable units, cannot refund 99`，上游零写入 ✅ |
| 无发票不能退 | order 27（已取消无发票） | `invalid_argument: order 27 has no invoice, so there is nothing to refund` ✅ |
| 拿别人的 item 退 | order 26 传 item 32 | `invalid_argument: order item 32 is not part of the order` ✅ |
| 回读金额与上游一致 | `creditmemo.get 19` vs `GET /V1/creditmemo/19` | 都是 22.00 ✅ |

### 3) 顾客要改收货地址 → 改地址并回读确认 ❌ 改不了这单，也回读不了

```bash
... -tool=magento.customer.addresses.update -args='{"instance":"demo","customer_id":1,"address_id":9,"street":["ul. Zmieniona 99"],"city":"Gdansk","postcode":"80-001"}'
```

```json
{"address": {"id": 9, "country_id": "PL", "region_id": 997, "default_billing": false, "default_shipping": false}}
```

回读（`customer.addresses`）：

```json
{"addresses": [ ... {"id": 9, "country_id": "PL", "region": "dolnośląskie", "region_id": 997,
  "default_billing": false, "default_shipping": false} ]}
```

**街道、城市、邮编、电话、收件人全都不在返回里**，我没有任何办法向顾客复述「您的新地址是 X」。
上游确实改了（REST 直连 `street: ["ul. Zmieniona 99"], city: Gdansk, postcode: 80-001`，
`telephone/firstname/lastname` 没有被这次部分更新抹掉——这点是好的）。

更关键的是：**顾客要改的是「这一单」的收货地址，而 MCP 没有任何工具能改已下单订单的地址**
（`magento.order.*` 只有 get/search/summary/geography/comment/cancel/hold/unhold/email/create，
没有一个能改地址）。
我改地址簿成功了，但同一时刻 order 31 的收货地址仍然是旧的：

```text
address 9 (地址簿): street ['ul. Zmieniona 99'] city Gdansk postcode 80-001
order 31 shipping: street ['ul. Nowa 12/3'] city Wroclaw postcode 00-001
```

工具返回 `{"address": {...}}` 这个「成功」非常容易让我误以为顾客的订单已经改到新地址。
→ 见 **P-CS-05**。

### 4) 顾客问「我的信息你们存了什么」→ 查客户资料，确认没有多余 PII ✅ 没有多余 PII（但也答不了顾客）

```bash
... -tool=magento.customer.get -args='{"instance":"demo","customer_id":1}'
... -tool=magento.customer.addresses -args='{"instance":"demo","customer_id":1}'
... -tool=magento.customer.search -args='{"instance":"demo","email":"roni_cost@example.com"}'
```

```json
{"customer": {"id": 1, "group_id": 1, "website_id": 1, "store_id": 1,
  "created_in": "Default Store View", "created_at": "2022-08-26 12:18:51", "updated_at": "2026-11-21 18:43:25"}}
```

- ✅ **没有多余 PII**：上游 `GET /V1/customers/1` 实际有 `email / firstname / lastname / dob /
  gender / street / city / postcode / telephone`，MCP 的投影一个都没带出来；`customer.search`
  用邮箱命中，但返回里不含邮箱。`order.get` 也不带顾客邮箱/电话。这一条是干净的。
- ❌ 副作用：**我没法核对顾客身份**（不能问「请确认一下您的邮箱是 X 吗」），也看不到地址内容，
  与场景 3 是同一个根因。另外 `customer.search` 只接受完整合法邮箱，顾客只报片段
  （`"email":"roni"`）时直接 `invalid_argument: email is not a valid address`。
  → 见 **P-CS-09**。

### 5) 顾客要取消刚下的单 → 取消并确认真的取消了 ✅（金额维度缺失）

```bash
... -tool=magento.order.create  ...            # 新建 order 27（pending，2 件 24-MB01，68.00）
... -tool=magento.order.cancel -args='{"instance":"demo","order_id":27}'
```

```json
{"order_id": 27, "canceled": true, "operation_id": "5ea9203a-..."}
```

回读 `order.get 27`：`"status": "canceled", "state": "canceled", "updated_at": "2026-11-21 19:29:06"` ✅
重复取消：同参数再调 → `{"order_id": 27, "canceled": true, "operation_id": "5ea9203a-...", "replayed": true}` ✅
上游复核：`status canceled, total_canceled 68, items[0].qty_canceled 2` ✅

- ✅ 真的取消了，且重复调用不会二次取消。
- ✅ 已开票订单（order 26）取消被正确拒绝，信息可操作：
  `conflict: Magento declined the cancellation and the order state did not change ... verify the order before acting`，
  details 里带 `order_state: processing`。
- ⚠️ `order.get` 不显示 `total_canceled` / `qty_canceled`：我能看到「取消了」，但看不到
  「取消了多少钱、几件」。顾客追问「那 68 块什么时候退」时，我拿不出数字。→ 见 **P-CS-06**。

---

## 二、发现清单

### P-CS-01: 部分退款会把「全部未退运费」一起退掉，工具既不可控也不提示，内部记账还只算商品行

- **级别**: P1
- **我的工作流**: 顾客退其中一件，我按「这件多少钱」办部分退款，然后告诉顾客退款金额。
- **发生了什么**:
  - order 26（subtotal 63 / shipping 5 / grand_total 68），退 1 件 17.00 →
    creditmemo 19 `grand_total: 22`（17 商品 + 5 全部运费），`total_qty: 1` 正确。
  - order 8（subtotal 136 / shipping 20 / grand_total 156），退 1 件 34.00 →
    creditmemo 17 `grand_total: 54`（34 + 20 全部运费）。
  - 这不是 MCP 自己算的，是 Magento 的默认口径原样透传：REST 直连、只传 items 退 order 12 的
    1 件 17.00，得到 creditmemo 16 `grand_total: 27`（17 + 10 运费）。
  - `creditmemo.create` 的 schema 没有 `shipping_amount` 参数（只有
    `adjustment_positive` / `adjustment_negative`），响应里也只有 `shipping_amount` 字段本身，
    没有一句「本次退款含运费 5.00」。
  - 工具自己的授权金额只算商品行：`operations.amount_minor` 为 1700（实际退 2200）、
    3400（实际退 5400）。
- **我期望什么**: 要么暴露 `shipping_amount`（Magento 的 `CreditmemoInterface` 本来就有），
  要么默认不退运费、由我显式加；至少要在响应和审批摘要里明写「退款 = 商品 17.00 + 运费 5.00 =
  22.00」。现在等于工具替我做了一个我没同意、也没看见的金额决定。
- **影响**: 每一笔「只退其中一件」的退款都多退一次运费；账本记录的退款额与实际打款额不一致，
  财务对账时必然出现差额。
- **状态**: CONFIRMED（金额、上游 creditmemo、账本三方都核对过）

### P-CS-02: 只要给退款/发货写备注，请求必定 400；而且错误信息把真正的上游原因吞掉了

- **级别**: P1
- **我的工作流**: 办退款时写一句备注（「顾客退回 1 件，包装完好」），或者发货时写「DPD 寄出」。
- **发生了什么**:

```bash
... -tool=magento.creditmemo.create -args='{"instance":"demo","order_id":12,"items":[{"order_item_id":15,"qty":1}],"comment":"Customer returns 1x downolable","notify":false}'
```

```json
{"code": "validation_failed",
 "details": {"store_code": "pl", "upstream_status": "400"},
 "message": "Magento rejected the request as invalid; verify store_code and field values",
 "op": "magento POST /order/{id}/refund"}
```

```bash
... -tool=magento.shipment.create -args='{"instance":"demo","order_id":10,"items":[{"order_item_id":12,"qty":2}],"comment":"Wysyłka kurierem DPD","tracks":[{"carrier_code":"custom","title":"DPD","track_number":"DPD-TEST-0001"}]}'
```

```json
{"code": "validation_failed",
 "details": {"store_code": "pl", "upstream_status": "400"},
 "message": "Magento rejected the request as invalid; verify store_code and field values",
 "op": "magento POST /order/{id}/{id}"}
```

  去掉 `comment` 后同样两个调用立刻成功（creditmemo 19 / shipment 13），所以是备注触发的。
  用 REST 直连把根因隔离出来：

```text
# MCP 发出的 comment 结构（is_visible_on_front 是布尔）
POST /rest/pl/V1/order/31/refund {"items":[{"order_item_id":38,"qty":1}],"comment":{"comment":"probe","is_visible_on_front":false}}
HTTP 400 {"message":"Error occurred during \"is_visible_on_front\" processing. The \"\" value's type is invalid. The \"int\" type was expected. Verify and try again."}

# 同一个字段给 int 就成功
POST /rest/pl/V1/order/31/refund {"items":[{"order_item_id":38,"qty":1}],"comment":{"comment":"probe int","is_visible_on_front":0}}
HTTP 200 "25"
```

  即 `internal/magento/order_documents_write.go` 的 `documentComment.IsVisibleOnFront` 是 `bool`，
  序列化成 `false`，而 Magento 要求 `int`。
  第二个问题：Magento 已经明确说了「`is_visible_on_front` 类型错」，但这句话没到我跟前——
  `internal/magento/client.go` 的 `upstreamTextIsSafe` 用 `upstreamQuotedRE = "[^"]*"` 把任何含
  双引号的句子整段丢掉，而 Magento 恰好用双引号包住**字段名**。同一个过滤对不含引号的错误是放行的
  （例如 `invoice.create` 报的
  `Invoice Document Validation Error(s):The invoice can't be created without products...` 就带出来了）。

- **我期望什么**: 备注能写进去（这是客服留痕的基本要求）；失败时给出「哪个字段、什么类型不对」。
  字段名不是机密，把它当敏感信息屏蔽，代价是我只能把 400 原样转给顾客或反复试错。
- **影响**: 客服/运营只要写备注就办不了退款和发货；400 又不可定位，只能盲试。
- **状态**: CONFIRMED（两种工具都复现；REST 隔离证明根因在 `is_visible_on_front` 的类型）

### P-CS-03: 退款审批环节看不到金额——审批人只能看到一个 item 数量

- **级别**: P2
- **我的工作流**: 退款要另一个同事审批，我希望他看到「即将退 22.00 PLN」再点同意。
- **发生了什么**: `confirmation_required` 只回两个字段，没有金额：

```json
{"code": "confirmation_required",
 "details": {"confirmation_id": "79d6d10e-2ed0-4d9d-b07a-b450c40ae865", "expires_at": "2026-11-21T19:35:10Z"},
 "message": "this operation requires approval by a different human"}
```

  审批侧看到的摘要（`confirmation_tokens.summary`，我查的是同一次待批记录）：

```text
 tool                      | instance_id | scope | summary
---------------------------+-------------+-------+---------------------------------------------
 magento.creditmemo.create | demo        | write | order_id=26 instance=demo items=[{"order_item_id":31,"qty":1}]
```

  摘要完全由入参拼出来（`internal/security/confirm/confirm.go: Summarize`），没有任何金额。
  而工具其实**已经算出了金额**（账本 `amount_minor: 1700`），只是没放进摘要。

- **我期望什么**: 审批摘要带 `amount=22.00 PLN`（并说明含运费 5.00）。审批的核心就是核对金额，
  现在审批人对钱一无所知，等于走形式。
- **影响**: 多退、错退在审批环节没有任何拦截能力；与 P-CS-01 叠加后，审批人批准的是「1 件」，
  实际出去的是「1 件 + 全部运费」。
- **状态**: CONFIRMED

### P-CS-04: `order.get` 缺 `qty_shipped/qty_invoiced/qty_refunded`，`shipment.get/list` 缺 `items`——「哪件发了/退了几件」答不出来

- **级别**: P2
- **我的工作流**: 顾客问「我两件里哪件发了、哪件还没发」，或者「我退的那件你们收到没有」。
- **发生了什么**:
  - order 31（两件不同 SKU，只发了 24-MB01）：`order.get` 只有 `qty_ordered: 1 / 1`；
    `shipment.list` 只有 `total_qty: 1`。上游 shipment 19 明确是
    `items: [{sku: 24-MB01, qty: 1}]`，order 31 上游是 `qty_shipped: 1 / 0`。
  - order 26：上游 `qty_refunded: 1`（downolable），`order.get` 里没有任何退款件数字段，
    我会以为顾客手上还是 2 件。
  - `shipment.get 13` / `shipment.list 10` 都不含 `items`（上游 `/V1/shipment/13` 有）。
- **我期望什么**: `order.get` 的每个 item 带 `qty_invoiced/qty_shipped/qty_refunded`（Magento
  的 order item 本来就有），`shipment` 带 `items`。这两处一加，客诉对话立刻可答。
- **影响**: 我给出的物流答复只能靠推测；「已发/未发」「退了几件」这类最高频问题全部答不准。
- **状态**: CONFIRMED

### P-CS-05: 改地址只能改地址簿，改完还回读不到内容；已下单订单的收货地址没有任何工具能改

- **级别**: P2
- **我的工作流**: 顾客说「我搬家了，把这单寄到新地址」，我改地址、回读给顾客确认。
- **发生了什么**:
  - `customer.addresses.update` 返回 `{"address": {"id": 9, "country_id": "PL", "region_id": 997,
    "default_billing": false, "default_shipping": false}}` —— 没有 street/city/postcode/telephone。
  - `customer.addresses` 回读同样没有这些字段（只有 country/region）。
  - 上游确实改了：`street: ['ul. Zmieniona 99'], city: Gdansk, postcode: 80-001`。
  - 已下单的 order 31 收货地址仍是旧的 `['ul. Nowa 12/3'], Wroclaw, 00-001`；
    工具清单里没有任何 `order.addresses.*` / `order.update` 能改订单地址。
- **我期望什么**: 要么提供改订单收货地址的能力，要么在 `customer.addresses.update` 的响应/描述里
  明确写「此操作只改地址簿，不影响已下单订单」；并且至少把改后的地址字段回读给我，让我能对顾客复述。
- **影响**: 我会向顾客承诺「地址已改」，而包裹仍按旧地址发出——这是最典型的客诉升级场景。
- **状态**: CONFIRMED

### P-CS-06: 取消订单后看不到「取消了多少钱/几件」

- **级别**: P3
- **我的工作流**: 取消后向顾客确认「68.00 已作废，无需付款」。
- **发生了什么**: order 27 取消后 `order.get` 只有 `"status": "canceled"`；上游有
  `total_canceled: 68`、`items[0].qty_canceled: 2`，MCP 都没有对应字段。
- **我期望什么**: 至少带 `total_canceled` 或 item 级 `qty_canceled`，让我能给出金额口径。
- **影响**: 顾客追问「取消了多少、要不要付款」时只能靠订单总额推断（部分取消时会推断错）。
- **状态**: CONFIRMED

### P-CS-07: `invoice.create` 在 `capture=false` 时仍返回 `captured: true`

- **级别**: P3
- **我的工作流**: 顾客问「你们收到我的钱了吗」时，我可能会看开票/收款状态。
- **发生了什么**: 我每次开票都没有传 `capture`（默认 false），核对到的响应全是 `"captured": true`：

```json
{"invoice": {"entity_id": 24, "order_id": 26, "state": 2, "grand_total": 68, "total_qty": 3},
 "captured": true, "operation_id": "d94bac3d-..."}
```

  本店付款方式是 `checkmo`（线下汇款），上游 `total_offline_refunded` 也证明是线下流程。
  工具自己的描述是「`capture`: Capture the payment; false only records the invoice」，
  而实现（`internal/magento/invoice_write.go`）是 `Captured: invoice.State == invoiceStatePaid`
  —— 对线下付款方式，Magento 开票即 paid，与是否 capture 无关。

- **我期望什么**: `captured` 只应在真的走了 capture 时才是 true；否则用
  `capture_attempted` / `invoice_state` 之类诚实的字段。
- **影响**: 客服/财务会把「已开票」误读成「已收款」。
- **状态**: CONFIRMED（`captured: true` 与 `capture=false` + 线下付款方式并存）

### P-CS-08: 发货失败时错误里的操作标签是 `magento POST /order/{id}/{id}`

- **级别**: P3
- **我的工作流**: 把错误原文转给技术支持时，希望它说清是哪条上游路由。
- **发生了什么**: `shipment.create` 失败的 envelope 里 `"op": "magento POST /order/{id}/{id}"`；
  同一位置的 `creditmemo.create` 是 `"op": "magento POST /order/{id}/refund"`（可读）。
  原因：`internal/magento/client.go` 的 `routeWords` 白名单里没有 `ship`，
  `redactedRoute` 把它当标识符替换成了 `{id}`。
- **我期望什么**: 至少保留路径段的语义（`/order/{id}/ship`），别把动作名也打码。
- **影响**: 报障时无法定位；日志里同一形状的错误难以区分（发货 vs 其它 /order/{id}/xxx）。
- **状态**: CONFIRMED

### P-CS-09: 顾客身份与地址内容不可回读，`customer.search` 只支持完整邮箱

- **级别**: P3
- **我的工作流**: 接电话时核对顾客身份（「请确认一下您的邮箱/收货地址」）。
- **发生了什么**: `customer.get` / `customer.search` 不返回 email、姓名；`customer.addresses`
  不返回 street/city/postcode/telephone；`customer.search` 传 `"email":"roni"` 直接
  `invalid_argument: email is not a valid address`。
- **我期望什么**: 这是 PII 最小化的必然取舍（本岗位认同「不要多存多露」），但客服场景需要
  **可核对的等价物**：例如脱敏邮箱（`r***@example.com`）、地址后四位、或一个「按邮箱/电话匹配」
  的确认接口。至少在文档里说明客服无法回读，避免我向顾客承诺「我念给您听」。
- **影响**: 身份核对只能靠顾客自述，冒充风险与误改地址风险都在客服这一环。
- **状态**: CONFIRMED（PII 侧是 NOT-A-DEFECT：确实没有多余 PII）

### P-CS-10: `creditmemo.email` 返回 `sent: true`，但投递无法核实

- **级别**: P3
- **我的工作流**: 退款后把退款单发给顾客，顾客说没收到时我要能查。
- **发生了什么**:

```bash
... -tool=magento.creditmemo.email -args='{"instance":"demo","document_id":19}'
```

```json
{"document_id": 19, "kind": "creditmemo", "sent": true}
```

  连续两次调用都是 `sent: true`，没有任何消息 id / 队列状态 / 失败原因可查。

- **我期望什么**: `sent` 应说明它代表什么（Magento 接受请求 ≠ 已投递），或提供可查询的投递记录。
- **影响**: 「我们发过邮件」无法举证，容易变成「你们根本没发」的争执。
- **状态**: SUSPECTED（`sent: true` 的语义取决于 Magento 邮件队列，本店无法进一步验证投递）

---

## 三、汇总

| 编号 | 一句话 | 级别 | 状态 |
|---|---|---|---|
| P-CS-01 | 部分退款连带退全额运费，无参数无提示，账本只记商品金额 | P1 | CONFIRMED |
| P-CS-02 | 带 comment 的退款/发货必定 400，且上游原因被安全过滤吞掉 | P1 | CONFIRMED |
| P-CS-03 | 退款审批摘要里没有金额 | P2 | CONFIRMED |
| P-CS-04 | `order.get` 缺已发/已退件数，`shipment` 缺 `items` | P2 | CONFIRMED |
| P-CS-05 | 改地址改不了订单、也回读不到内容 | P2 | CONFIRMED |
| P-CS-06 | 取消后看不到取消金额/件数 | P3 | CONFIRMED |
| P-CS-07 | `capture=false` 却报 `captured: true` | P3 | CONFIRMED |
| P-CS-08 | 发货错误的 op 标签是 `/order/{id}/{id}` | P3 | CONFIRMED |
| P-CS-09 | 顾客身份/地址不可回读；邮箱只支持精确匹配 | P3 | CONFIRMED |
| P-CS-10 | `creditmemo.email` 的 `sent: true` 不可核实 | P3 | SUSPECTED |

**做对了、值得保留的**：退款幂等重放（`replayed: true`，不重复退款）、超退本地拦截、
「无发票 / 跨单 item」的错误文案、取消的幂等与「已开票不可取消」的 `conflict`、
运单号可见、PII 投影确实没有多余字段、以及**所有回读金额与 REST 直连逐分对齐**
（22.00 / 54.00 / 34.00 三笔全部一致）。

**给修复阶段的提示（本轮不改代码）**：

1. P-CS-02 是两行级别的类型修正（`IsVisibleOnFront` → `int`），但它挡住的是「退款+备注」整条主流程，
   建议优先。
2. P-CS-01 需要产品决策（暴露 `shipping_amount` / 默认不退运费 / 至少在响应与审批摘要里显式列明），
   决策前至少要把「含运费」写进响应文本，否则金额口径永远对不上。
3. P-CS-03 的金额工具内部已经算好（账本 `amount_minor`），把它塞进 `confirm.Summarize` 即可；
   顺带能缓解 P-CS-01 的信息不对称。

## 四、数据漂移说明（不是缺陷）

本轮多个岗位 agent 并发操作同一 demo 站，我实测期间观察到：

- order 4 在我第一次 `order.get` 时是 `processing`，几分钟后再开票报
  `The invoice can't be created without products ... gdy zamówienie ma status complete`
  —— 已被其他 agent 推进到 `complete`。
- order 8 的 `qty_shipped` 从 0 变成 3（不是我发的）。
- 账本里出现了别的 agent 的 creditmemo（`000000021` / `000000022`，对应 invoice 23），说明同一实例
  上还有人在跑退款用例，读 `creditmemo.list` 时要按 `order_id` 过滤。

因此本文所有金额/件数结论都绑定快照 `2026-11-21T19:32:42Z`，并且**同时**给出 MCP 与 REST 直连
两侧的读数；回归阶段请按「同一时刻两侧是否对齐」判断，而不是重跑得到同一个总额。
