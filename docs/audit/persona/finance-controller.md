# 财务岗位场景测试（v0.11.6）

我的日常任务：月底对账。拉销售汇总核对总额 → 对一笔订单开票 → 部分退款 → 核对贷项通知单与发票的
对应关系 → 查税额类与税率确认报表口径 → 拦超额退款。

我的判断标准只有两条：**每个数字都要能追溯到一张具体单据**；**差一分钱也要查到底**。
所以本文所有金额都同时给出 MCP 输出与 Magento REST 直连（`/rest/V1/...`，不经过网关）的独立复核。

## 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | v0.11.6（容器 `ghcr.io/shuaizend/mage2mcp:v0.11.6`） |
| 实例 id | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store `pl`（PLN，`Europe/Warsaw`） |
| 管理员令牌 | `/tmp/magento_token` |
| 审批 | `ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'` |

> 我把 live 测试工具预编译成 `/tmp/mcpraw` 来跑（`go build -tags live -o /tmp/mcpraw ./test/live`），
> 下文命令等价于 `go run -tags live ./test/live -mode=raw ...`。

```bash
export MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo
export MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"
```

**数据会漂移**：本轮多个岗位 agent 并发下单，同一窗口 20 分钟内从 20 单涨到 34 单。本文数字全部
绑定快照时间，跨时间的差异是漂移不是缺陷。

| 快照 | 时间（UTC） | 窗口 | 订单数 | `total_revenue` |
|---|---|---|---|---|
| S0 | `2026-11-21T19:24:46Z` | `2026-11-20 → 2026-11-22` | 25 | 1531.18 |
| S1 | `2026-11-21T19:32:45Z` | `2026-11-20 → 2026-11-22` | 34 | 2195.76 |

我自建的测试单据（不受他人影响，本文所有逐项核对都基于它们）：

| 单据 | 单号 | 说明 |
|---|---|---|
| 订单 | `000000025`（entity 25） | 24-MB01 ×1 + 24-UG06 ×3，运费 20 |
| 订单 | `000000029`（entity 29） | 同上，用于"分两次开票"场景 |
| 发票 | `000000023`（entity 23） | 订单 25 全额开票 |
| 贷项通知单 | `000000021`、`000000022` | 订单 25 的两次部分退款 |
| 发票 | `000000025`、`000000026` | 订单 29 分两次开票 |
| 贷项通知单 | `000000023`、`000000024` | 订单 29 的两次部分退款 |

---

## 一、拉销售汇总并核对总额

### 1.1 总额、单数、件数：与订单明细**逐笔**一致 ✅

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.summary \
  -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22"}'
```

```json
{"summary":{"window":"2026-11-20T00:00:00Z/2026-11-22T00:00:00Z",
 "days":[{"date":"2026-11-21","orders":34,"revenue":2195.76,"items":67}],
 "total_orders":34,"total_revenue":2195.76,"total_items":67,"currency":"PLN",
 "mixed_currency":false,"truncated":false,"total_available":34,
 "excluded_orders":0,"scanned_orders":34}}
```

REST 直连逐单复核（同一窗口）：

```bash
T=$(cat /tmp/magento_token)
curl -sS -G "https://demo-magento-2.auroracreation.com/rest/V1/orders" -H "Authorization: Bearer $T" \
  --data-urlencode 'searchCriteria[pageSize]=100' \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][field]=created_at' \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][value]=2026-11-20 00:00:00' \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][conditionType]=gteq' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][field]=created_at' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][value]=2026-11-22 00:00:00' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][conditionType]=lt' \
  --data-urlencode 'fields=items[entity_id,increment_id,grand_total,total_qty_ordered],total_count'
```

在 S0 快照上逐单相加：`n=25  sum(grand_total)=1531.18  sum(total_qty_ordered)=48`，
与 `total_orders=25 / total_revenue=1531.18 / total_items=48` **完全相等，没有分差**。
`total_available` 与 `scanned_orders` 都等于实际单数，`truncated=false` 是诚实的。

### P-FIN-01: 销售汇总的 revenue 含已取消订单，且完全不扣退款

- **级别**: P1
- **我的工作流**: 我要拿 `sales.summary` 的 `total_revenue` 当"本期确认收入"入账。
- **发生了什么**: 同一窗口里既有已取消订单，也有已全额退款的订单，`total_revenue` 一个都不扣。

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.analysis \
  -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22"}'
```

```json
"by_status":[{"key":"closed","orders":11,"revenue":476.18},
             {"key":"complete","orders":11,"revenue":835.5},
             {"key":"pending","orders":6,"revenue":336},
             {"key":"canceled","orders":4,"revenue":261},
             {"key":"processing","orders":2,"revenue":287.08000000000004}]
```

S0 快照上的复核（REST 直连逐单）：

```text
sum grand_total      = 1531.18      ← sales.summary 的 total_revenue
sum total_refunded   =  845.18
canceled grand_total =  261.00
net (gross-refund)   =  686.00
net excl canceled    =  425.00
```

- **我期望什么**: 工具描述只说了 `"Revenue is order grand_total (tax and shipping included)"`，
  没说**包含已取消订单**、也没说**不扣退款**。财务拿这个数当收入，25 单的窗口里虚增
  261.00（取消）+ 845.18（已退）= 最多 1106.18，是总收入的 72%。至少要在输出里给出
  `canceled_revenue` / `refunded_total`，或者让调用方按 status 过滤（`sales.summary` 没有 status 参数，
  `order.search` 有——两个工具的口径对不上）。
- **状态**: CONFIRMED

### P-FIN-02: 金额字段带二进制浮点残差，没有按货币精度收口

- **级别**: P2
- **我的工作流**: 把金额写进总账、和银行流水比对，通常按字符串/两位小数比较。
- **发生了什么**:

```json
"total_revenue": 2195.76                                  ← S1，干净
"subtotal": 1946.3600000000001,                           ← sales.analysis
"by_status":[{"key":"processing","revenue":287.08000000000004}]
"aov": 64.58117647058825
```

  同一个 `magento.sales.summary` 在不同快照上也吐过 `1804.3600000000001`、
  `1754.3600000000001`（`magento.order.summary` 别名同样）。

- **我期望什么**: 金额字段应统一按货币最小单位（PLN 分）round 到 2 位小数再输出。现在
  `1946.3600000000001` 与账上 `1946.36` 字符串比较不相等，任何 `if amount == expected` 的对账脚本都会
  误判为差异；而 `aov` 输出 12 位小数对一个"平均客单价"字段没有意义。
- **状态**: CONFIRMED

### P-FIN-03: 销售汇总没有任何税额/退款/折扣字段，财务无法用它出报表

- **级别**: P1
- **我的工作流**: 财务月报要四列：含税收入、税额、运费、退款。`sales.summary` 只有一列 `revenue`。
- **发生了什么**: `sales.summary` 的完整输出字段就是
  `window / days[date,orders,revenue,items] / total_orders / total_revenue / total_items / currency /
  mixed_currency / truncated / total_available / excluded_orders / scanned_orders`——**没有 tax**。
  要拿税额只能换工具 `magento.sales.analysis`（有 `tax` / `discount` / `shipping` / `refunded_amount`），
  但那个工具的 `refunded_amount` 口径又有问题（见 P-FIN-04），且两个工具的单数/金额口径不同
  （见 P-FIN-06）。
- **我期望什么**: 财务最常用的就是"这个窗口的税是多少"。工具描述明确说 revenue "tax included"，
  却连一个 `tax` 汇总字段都不给，逼着财务换工具再拼一次数，拼的时候还要自己保证口径一致。
- **状态**: CONFIRMED

### P-FIN-04: `sales.analysis.refunded_amount` 只是"商品行退款小计"，不含运费与调整费，差 160.00

- **级别**: P1
- **我的工作流**: 算净收入 = `revenue - refunded_amount`。
- **发生了什么**: S0 快照：

```json
"revenue": 1531.18, "refunded_amount": 685.18, "refunded_qty": 26
```

  REST 直连逐单核对（`fields=items[entity_id,subtotal_refunded,shipping_refunded,adjustment_positive,total_refunded]`）：

```text
 id  inc          state        gt      ref   subref  shipref  adjP
 15  000000015    complete   78.00   34.00   34.00   10.00  10.00
 ...（其余略）
TOTALS {'gt': 1531.18, 'ref': 845.18, 'subref': 685.18, 'shipref': 170.0}
```

  即：`total_refunded = subtotal_refunded(685.18) + shipping_refunded(170.00) − adjustment_positive(10.00)
  = 845.18`。而 `refunded_amount` 输出的是 685.18（`sales_analysis.go:368` 累加的是
  `item.amount_refunded`，只到商品行），比真实退款总额少 **160.00**（170.00 运费 − 10.00 调整费）。

  如果财务按 `revenue - refunded_amount` 算净收入：`1531.18 - 685.18 = 846.00`，
  正确值 `1531.18 - 845.18 = 686.00`，**差 160.00**。

- **我期望什么**: 字段名 `refunded_amount` 的 jsonschema 描述是
  `"Amount refunded across the window"`——声称的是"退款总额"，实际只是商品小计。
  要么改名 `refunded_subtotal`，要么把运费与调整费算进去，并把口径写进描述。
- **状态**: CONFIRMED

### P-FIN-05: `discount` 符号为负，与 `Order.DiscountAmount` 的"正数"声明相反

- **级别**: P3
- **我的工作流**: 按 `revenue = subtotal + shipping + tax - discount` 验算。
- **发生了什么**:

```json
"subtotal": 1946.3600000000001, "discount": -45.6, "shipping": 295, "tax": 0,
"revenue": 2195.76
```

  恒等式实际是 `revenue = subtotal + shipping + tax + discount`（1946.36 + 295 − 45.6 = 2195.76）。

- **我期望什么**: 订单对象里 `DiscountAmount` 的 jsonschema 是
  `"Total discount applied, as a positive number."`；汇总里同一概念变成负数。
  同一个词在两个工具里符号相反，对账脚本很容易把折扣加两遍（差 91.20）。
- **状态**: CONFIRMED

### P-FIN-06: 日拆分口径不一致：`sales.summary` 按 UTC 日期，`sales.analysis` 按店铺时区

- **级别**: P2
- **我的工作流**: 日报/月报要对齐"哪一天的收入"。
- **发生了什么**: 同一窗口，两个工具都返回"按天"数据，但日期桶不同源：

  - `sales.summary`（`internal/magento/order.go:213`）用 `orderDate(order.CreatedAt)`，
    直接取 `created_at` 字符串前 10 位（Magento 存的是 UTC），输出里**没有时区字段**；
  - `sales.analysis`（`internal/magento/sales_analysis.go:313`）用
    `orderLocalTime(order.CreatedAt, loc)` 转成 `Europe/Warsaw` 再取日期，输出 `"timezone":"Europe/Warsaw"`。

  本店 UTC+1，因此 `23:00–24:00 UTC` 的订单在 `sales.summary` 记在 D 日，在 `sales.analysis` 记在 D+1 日。
  当前数据（订单都集中在 `17:00–19:31 UTC`）不跨这条线，所以两边暂时都是 `2026-11-21`，
  **未能实测出分歧**——这是代码层推断。

- **我期望什么**: 两个报表工具对"一天"的定义必须一致，或者 `sales.summary` 也要有 `timezone` 字段
  并说明按 UTC 分桶。否则月底跨期核对时，日报会差一天的货。
- **状态**: SUSPECTED（代码证据确凿，缺一条 23:00 UTC 之后的订单来实测）

---

## 二、开票 + 部分退款，逐项核对金额

### 2.1 建单与开票（全额）

建单（带显式幂等键，避免与其他岗位的同参数订单撞键）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.order.create \
  -args='{"instance":"demo","customer_id":3,
   "items":[{"sku":"24-MB01","qty":1},{"sku":"24-UG06","qty":3}],
   "billing_address":{...PL/mazowieckie/00-001...},
   "shipping_address":{...},
   "shipping_carrier_code":"flatrate","shipping_method_code":"flatrate",
   "payment_method":"checkmo","idempotency_key":"fin-order-2-20261122"}'
```

```json
{"order":{"entity_id":29,"increment_id":"000000029","grand_total":74.68,"subtotal":54.68,
 "shipping_amount":20,"tax_amount":0,"total_qty_ordered":4,
 "items":[{"item_id":34,"sku":"24-MB01","qty_ordered":1,"price":29,"row_total":29},
          {"item_id":35,"sku":"24-UG06","qty_ordered":3,"price":8.56,"row_total":25.68}]}}
```

开票：

```bash
/tmp/mcpraw -mode=raw -tool=magento.invoice.create \
  -args='{"instance":"demo","order_id":25,
   "items":[{"order_item_id":28,"qty":1},{"order_item_id":29,"qty":3}],
   "capture":true}'
```

```json
{"invoice":{"entity_id":23,"increment_id":"000000023","order_id":25,"state":2,
 "grand_total":74.68,"subtotal":54.68,"shipping_amount":20,"tax_amount":0,
 "total_qty":4,"order_currency_code":"PLN"},"captured":true}
```

**逐项核对（订单 25 ↔ 发票 23，REST 直连 `GET /V1/invoices/23` 复核）**：

| 项目 | 订单 000000025 | 发票 000000023 | 差 |
|---|---|---|---|
| subtotal | 54.68 | 54.68 | 0.00 |
| shipping | 20.00 | 20.00 | 0.00 |
| tax | 0.00 | 0.00 | 0.00 |
| **grand_total** | **74.68** | **74.68** | **0.00** |
| total_qty | 4 | 4 | 0 |

发票行（REST 原始）：

```text
{'order_item_id': 28, 'sku': '24-MB01', 'qty': 1, 'price': 29,   'row_total': 29}
{'order_item_id': 29, 'sku': '24-UG06', 'qty': 3, 'price': 8.56, 'row_total': 25.68}
```

发票行金额与订单行**逐行相等**，`state=2`（paid）与 `capture:true` 一致。✅

### 2.2 部分退款（订单 25）

第一次（退 1 件 24-UG06）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":1}],
         "idempotency_key":"fin-cm-25-a-20261122"}'
```

```json
{"creditmemo":{"entity_id":21,"increment_id":"000000021","order_id":25,"state":2,
 "grand_total":28.56,"subtotal":8.56,"shipping_amount":20,"tax_amount":0,
 "adjustment":0,"total_qty":1,"order_currency_code":"PLN"},
 "invoice_id":23}
```

第二次（退剩余 3 件）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,
         "items":[{"order_item_id":28,"qty":1},{"order_item_id":29,"qty":2}],
         "idempotency_key":"fin-cm-25-b-20261122"}'
```

```json
{"creditmemo":{"entity_id":22,"increment_id":"000000022","grand_total":46.12,
 "subtotal":46.12,"shipping_amount":0,"tax_amount":0,"total_qty":3},"invoice_id":23}
```

**逐项核对（订单 ↔ 两张贷项通知单）**：

| 项目 | 订单 000000025 | 贷项 000000021 | 贷项 000000022 | 贷项合计 | 差 |
|---|---|---|---|---|---|
| subtotal | 54.68 | 8.56 | 46.12 | 54.68 | 0.00 |
| shipping | 20.00 | **20.00** | 0.00 | 20.00 | 0.00 |
| tax | 0.00 | 0.00 | 0.00 | 0.00 | 0.00 |
| **grand_total** | **74.68** | **28.56** | **46.12** | **74.68** | **0.00** |
| total_qty | 4 | 1 | 3 | 4 | 0 |

行级追溯：

| 订单行 | order_item_id | qty | 订单 row_total | 发票 23 row_total | 贷项 row_total |
|---|---|---|---|---|---|
| 24-MB01 | 28 | 1 | 29.00 | 29.00 | 29.00（CM22） |
| 24-UG06 | 29 | 3 | 25.68 | 25.68 | 8.56（CM21）+ 17.12（CM22）= 25.68 |

**退款总额 74.68 = 订单总额 74.68，一分不差**，订单最终 `state=closed`、
`total_refunded=74.68`、`subtotal_refunded=54.68`、`shipping_refunded=20`。

**但运费的归属是错的**——见 P-FIN-07。

### P-FIN-07: 第一次部分退款就把运费全额退掉，且没有任何参数能控制

- **级别**: P1
- **我的工作流**: 客户退 3 件里的 1 件水壶（8.56），只退商品款，运费不退。
- **发生了什么**: 只退 1 件、8.56 的贷项通知单，`shipping_amount` 直接是 20.00，
  `grand_total=28.56`。第二张（退剩余 3 件）`shipping_amount=0`。

```json
{"creditmemo":{"increment_id":"000000021","grand_total":28.56,
 "subtotal":8.56,"shipping_amount":20,...}}
{"creditmemo":{"increment_id":"000000022","grand_total":46.12,
 "subtotal":46.12,"shipping_amount":0,...}}
```

  同样的事在分次开票上重演：订单 29 只开 1 件水壶的发票，`shipping_amount` 就是全部 20.00
  （发票 `grand_total=28.56` 而 `subtotal=8.56`）；第二张发票运费 0。

- **我期望什么**: `magento.creditmemo.create` 的入参只有
  `order_id / invoice_id / items / adjustment_positive / adjustment_negative / comment / notify`，
  **没有 `shipping_amount`**；`refundOrderBody`（`internal/magento/order_documents_write.go:389`）
  也没有运费字段。结果是 Magento 默认把运费全退在第一次部分退款上：
  - 客户只退 1 件却拿回全部运费，店铺直接损失 20.00（本单 26.8% 的订单金额）；
  - 跨期看，运费退款落在错误的会计期间（第一次部分退款的月份）；
  - 财务无法把运费退款按退货比例分摊。
  财务必须能指定退多少运费（包括 0）。
- **状态**: CONFIRMED

### P-FIN-08: 带 comment 的贷项通知单 100% 失败（bool vs int），且失败原因被吞掉

- **级别**: P1
- **我的工作流**: 退款时写备注（RMA 号、退款原因），这是财务留痕的硬要求。
- **发生了什么**: 只要传 `comment`，必然 `validation_failed`：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":1}],
         "comment":"finance partial refund test"}'
```

```json
{"code":"validation_failed",
 "details":{"store_code":"pl","upstream_status":"400"},
 "message":"Magento rejected the request as invalid; verify store_code and field values",
 "op":"magento POST /order/{id}/refund"}
```

  去掉 `comment` 用同样的行/数量重试，仍然失败（幂等键问题，见 P-FIN-09）；换新幂等键后成功。
  同样的请求直接打到 Magento 才能看到真正原因：

```bash
curl -sS -X POST "https://demo-magento-2.auroracreation.com/rest/V1/order/25/refund" \
  -H "Authorization: Bearer $T" -H 'Content-Type: application/json' \
  -d '{"items":[{"order_item_id":29,"qty":1}],"appendComment":true,"notify":false,
       "comment":{"comment":"probe: finance partial refund test","is_visible_on_front":false}}'
```

```json
{"message":"Error occurred during \"is_visible_on_front\" processing. The \"\" value's type is invalid. The \"int\" type was expected. Verify and try again."}
HTTP 400
```

  根因：`documentComment.IsVisibleOnFront` 是 `bool`
  （`internal/magento/order_documents_write.go:360-363`），JSON 序列化成 `false`，
  而 Magento 的 `CreditmemoCommentInterface::setIsVisibleOnFront` 要 `int`。

- **我期望什么**: (1) 发 `0/1` 而不是 `true/false`；(2) 上游 400 的 `message` 必须透出
  ——现在只给 `upstream_status: 400` 和一句"verify store_code and field values"，财务看到这句话
  根本不知道是字段类型错，会去怀疑 store code。**这条错误信息对财务完全不可追溯**。
- **状态**: CONFIRMED（`shipment.create` 用的是同一个 `documentComment` 结构体
  `order_documents_write.go:99`，预计同样失败；发货场景不在本岗位范围，未实测）

### P-FIN-09: 一次失败会毒化派生幂等键，同参数重试直接被拒

- **级别**: P2
- **我的工作流**: P-FIN-08 失败后，我去掉 comment 重试同一笔退款。
- **发生了什么**:

```bash
# 第一次：带 comment，失败
# 第二次：去掉 comment，同样的 order/items，不传 idempotency_key
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":1}]}'
```

```json
{"code":"validation_failed",
 "details":{"operation_id":"8b13be42-fc5c-46a5-b493-ba2ecb060908","state":"failed"},
 "message":"the operation previously failed and was not retried; start a new operation to try again"}
```

  原因：`creditmemoArgs`（`order_documents_write.go:243`）参与哈希的只有
  `order_id / invoice_id / items / adjustment_*`，**`comment` 不在里面**。所以"带 comment"和
  "不带 comment"被视为同一个操作，第二次拿到的是第一次失败的回放。

- **我期望什么**: 要么把 `comment` 纳入哈希（它确实是不同的请求），要么在错误里直接给出
  "换一个 `idempotency_key` 重试"的可执行指引。现在财务只能靠猜——而且 `comment` 差异不影响
  哈希这件事本身也是 P-FIN-10。
- **状态**: CONFIRMED

### P-FIN-10: `adjustment_positive` 会吃掉"可退金额"，而本地校验只看数量，最后一件永远退不掉

- **级别**: P1
- **我的工作流**: 退最后 1 件水壶（8.56）。
- **发生了什么**: 订单 29 上我先退过 2 件水壶（CM23，37.12）和 1 件包（CM24，29.00 + 调整费 2.00），
  订单 `total_refunded=68.12`，剩余可退金额 `74.68 − 68.12 = 6.56`；但按数量算，
  24-UG06 还有 1 件可退（`qty_invoiced 3 − qty_refunded 2 = 1`）。

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":29,"invoice_id":26,"items":[{"order_item_id":35,"qty":1}],
         "idempotency_key":"fin-cm-29-nocomment-final"}'
```

```json
{"code":"validation_failed",
 "details":{"store_code":"pl","upstream_status":"400"},
 "message":"Magento rejected the request as invalid; verify store_code and field values"}
```

  REST 直连同一请求才拿到真正原因：

```json
{"message":"Creditmemo Document Validation Error(s):\nWiększość pieniędzy do zwrotu to 6.56."}
HTTP 400
```

  即"最多可退 6.56"，而 1 件水壶要退 8.56 —— 差 2.00，正是之前那张 CM24 的
  `adjustment_positive: 2` 吃掉的额度。

- **我期望什么**: `checkRefundable`（`order_documents_write.go:300`）只校验
  `qty_invoiced − qty_refunded`，不校验"剩余可退金额"。所以它放行了一个必然被上游拒绝的请求，
  又把上游那句"最多可退 6.56"扔掉。财务只看到"Magento rejected the request as invalid"，
  永远查不到这 2.00 卡在哪。**这是典型的"差一分钱查不到底"**：
  数量上还可退 1 件，金额上只差 2.00，既不能退也不能平。
- **状态**: CONFIRMED

---

## 三、贷项通知单与发票的对应关系

### 3.1 发票解析：单发票自动、多发票必须显式指定 ✅

订单 29 有两张发票（25：1 件水壶；26：1 件包 + 2 件水壶）。不指定 `invoice_id`：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":29,"items":[{"order_item_id":35,"qty":1}],
         "idempotency_key":"fin-cm-29-noidx"}'
```

```json
{"code":"invalid_argument",
 "message":"order 29 has 2 invoices; pass invoice_id to choose the one to refund"}
```

错误信息明确、可编程。单发票的订单 25 则自动解析成 `invoice_id=23`，不需要传。✅

### P-FIN-11: 贷项通知单在 Magento 侧根本没有 `invoice_id`，网关返回的对应关系是本地推导值

- **级别**: P1
- **我的工作流**: 审计凭证要求"每张贷项通知单能指回它冲销的发票"。
- **发生了什么**: `creditmemo.create` 的返回里带着 `"invoice_id": 23`：

```json
{"creditmemo":{"entity_id":21,"increment_id":"000000021","order_id":25,...},
 "invoice_id":23}
```

  但直接读 Magento 的贷项通知单：

```bash
curl -sS "https://demo-magento-2.auroracreation.com/rest/V1/creditmemo/21" -H "Authorization: Bearer $T"
```

```text
entity_id = 21
increment_id = 000000021
order_id = 25
invoice_id = None        ← Magento 里就是 null
total_qty  = None
```

  `magento.creditmemo.get` / `magento.creditmemo.list` 的输出里**没有任何 invoice 字段**：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.get -args='{"instance":"demo","creditmemo_id":21}'
```

```json
{"creditmemo":{"entity_id":21,"increment_id":"000000021","order_id":25,"state":2,
 "grand_total":28.56,"subtotal":8.56,"shipping_amount":20,"tax_amount":0,
 "adjustment":0,"total_qty":1,"order_currency_code":"PLN"}}
```

- **我期望什么**: `create` 返回的 `invoice_id` 是网关自己解析出来的（`refundInvoiceID`），
  不是单据上的字段，回读就没了。财务拿这个 `invoice_id` 去做"贷项↔发票"勾稽，
  换一个接口/一次导出就断链。要么在输出里标明它是 `resolved_invoice_id`（推导值），
  要么在 `creditmemo.get`/`list` 里也给出同样的解析结果。
- **状态**: CONFIRMED

### P-FIN-12: 不校验发票级数量——可以对"只开了 1 件"的发票冲销 2 件

- **级别**: P1
- **我的工作流**: 指定 `invoice_id=25` 冲销该发票上的 1 件水壶。
- **发生了什么**: 我传 `qty:2`（发票 25 上只有 1 件），网关照做：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":29,"invoice_id":25,
         "items":[{"order_item_id":35,"qty":2}],"idempotency_key":"fin-cm-29-crossinv"}'
```

```json
{"creditmemo":{"entity_id":23,"increment_id":"000000023","order_id":29,
 "grand_total":37.12,"subtotal":17.12,"shipping_amount":20,"total_qty":2},
 "invoice_id":25}
```

  发票 25 只开了 1 件水壶（`subtotal 8.56`、`shipping 20`、`grand_total 28.56`），
  这张贷项通知单却冲销了 **2 件 + 20 运费 = 37.12**，比它声称对应的发票总额（28.56）还多 8.56。

- **我期望什么**: `checkRefundable` 用的是**订单级** `qty_invoiced − qty_refunded`（订单上 3 件都已开票），
  所以本地校验通过。但既然接口让调用方指定 `invoice_id`，就必须校验该发票上的可退数量，
  否则"贷项通知单冲销哪张发票"这个凭证关系是假的：凭证金额可以大于它所冲销的发票金额。
- **状态**: CONFIRMED

### P-FIN-13: 同一张贷项通知单的 `total_qty` 在 create/get 与 list 之间不一致

- **级别**: P3
- **我的工作流**: 用 `total_qty` 做件数勾稽。
- **发生了什么**: Magento 里 `total_qty` 是 `null`，网关在 `create` 时用请求数量补齐
  （`order_documents_write.go:222-229`），`get` 也给了 1；但 `list` 里这个字段直接消失：

```json
// creditmemo.get 21
"total_qty": 1

// creditmemo.list order 25
{"creditmemos":[{"entity_id":21,"increment_id":"000000021","order_id":25,"state":2,
  "grand_total":28.56,"subtotal":8.56,"discount_amount":0,"shipping_amount":20,
  "tax_amount":0,"adjustment":0,"order_currency_code":"PLN",
  "created_at":"2026-11-21 19:29:01","updated_at":"2026-11-21 19:29:01"}],
 "total":1,"truncated":false}
```

- **我期望什么**: 同一张单据在三个接口里字段应当一致。要么都合成、要么都省略并说明
  `total_qty` 只在 create 的响应里由请求推导。
- **状态**: CONFIRMED

---

## 四、税额类与税率，报表口径

### 4.1 税类能查，但只有两个类目

```bash
/tmp/mcpraw -mode=raw -tool=magento.tax.classes -args='{"instance":"demo"}'
```

```json
{"classes":[{"class_id":2,"class_name":"Taxable Goods","class_type":"PRODUCT"},
            {"class_id":3,"class_name":"Retail Customer","class_type":"CUSTOMER"}]}
```

商品确实挂在 `tax_class_id=2`（Taxable Goods）上：

```bash
curl -sS -G "https://demo-magento-2.auroracreation.com/rest/V1/products/24-MB01" \
  -H "Authorization: Bearer $T" --data-urlencode 'fields=custom_attributes[attribute_code,value]'
```

```text
{'attribute_code': 'tax_class_id', 'value': '2'}
```

### P-FIN-14: 税率与税规则完全查不到，财务无法证明"税额为 0"是对的

- **级别**: P1
- **我的工作流**: 确认报表里的 `tax=0` 是"该店不征税"还是"税规则没配好"。
- **发生了什么**: MCP 只有 `magento.tax.classes`，**没有任何查税率/税规则的具**。直接打 Magento：

```bash
curl -sS -G ".../rest/V1/taxRates" -H "Authorization: Bearer $T"
curl -sS -G ".../rest/V1/taxRules" -H "Authorization: Bearer $T"
```

```json
{"message":"Wniosek nie odpowiada żadnej ścieżce."}
```

  波兰语 404 = **路由不存在**（按 README 的判据，这是店铺不暴露的路由，不是网关缺陷）。
  结果：所有订单 `tax_amount=0`（S0 快照 25 单 `sum(tax)=0`），`sales.analysis` 的 `tax` 也恒为 0，
  而 `sales.summary` 连 `tax` 字段都没有（P-FIN-03）。

- **我期望什么**: 财务要回答"这个 PLN 收入里含多少税"，现在**三个渠道都拿不到税率**：
  没有 MCP 工具、没有 REST 路由、报表只有含税总额。商品明明是 `Taxable Goods`（税类 2），
  订单税额却是 0——这既可能是"税率未配置"，也可能是"价格含税且税率为 0%"，从 MCP 侧无法区分。
  至少要在 `sales.summary` 给出 `tax` 汇总，并让 `tax.classes` 的输出带上"该类目当前无有效税率"
  这类可判定的信息。
- **状态**: CONFIRMED

### 4.2 价格口径：目录价 34，订单行价 29，差 5.00 可追溯到 `special_price`

财务拿价目表核对订单行价时必须能解释每一个差额，所以我把这两条也查到了底：

```bash
curl -sS -G ".../rest/V1/products/24-MB01" -H "Authorization: Bearer $T" \
  --data-urlencode 'fields=custom_attributes[attribute_code,value]'
```

```text
price = 34                      ← 商品接口的 price 字段
{'attribute_code': 'special_price',      'value': '29.000000'}
{'attribute_code': 'special_from_date',  'value': '2026-11-20 00:00:00'}
{'attribute_code': 'special_to_date',    'value': '2026-11-30 00:00:00'}
```

订单行价 29.00 = 有效期内的 `special_price`，可追溯 ✅。
但 `magento.product.get` 的**类型化字段只有 `price=34`**，`special_price` 只出现在 `attributes` 字典里：

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.get -args='{"instance":"demo","sku":"24-MB01"}'
```

```text
price 34
  special_price     = None | in attributes: 29.000000
  special_from_date = None | in attributes: 2026-11-20 00:00:00
  special_to_date   = None | in attributes: 2026-11-30 00:00:00
  tax_class_id      = None | in attributes: 2
```

- **级别**: P3（口径提示，不是缺陷：这是 Magento 的数据模型）
- **风险**: 任何用 `product.price` 重算订单金额的对账脚本会**每件高估 5.00**（本单 1 件即 5.00）。
  报告在此明确记录：订单行价必须以**订单行**为准，不能反查商品价目。

### 4.3 三位小数价格的舍入：8.555 → 8.56

`24-UG06` 目录价 `8.555`（三位小数），订单行价 `8.56`，`row_total = 3 × 8.56 = 25.68`；
若按目录价重算 `3 × 8.555 = 25.665`，**差 0.015**。

退款侧没有二次流失：贷项 21（1 件，8.56）+ 贷项 22（2 件，17.12）= 25.68 = 订单行 `row_total` ✅。
所以这 0.015 是"单位价格在行级被舍入到 2 位小数"造成的一次性差异，**不是重复舍入**。
记录在此，供财务按目录价重算时对账使用（P3，口径说明，非缺陷）。

---

## 五、尝试超额退款

### 5.1 数量超额：拒绝并给出可退数量 ✅

订单 25 的 24-UG06 已退 1 件（共 3 件），再退 3 件：

```bash
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":3}],
         "idempotency_key":"fin-cm-25-over-1"}'
```

```json
{"code":"invalid_argument",
 "message":"order item 29 has 2 refundable units, cannot refund 3"}
```

换另一行（24-MB01 共 1 件）退 2 件：

```json
{"code":"invalid_argument",
 "message":"order item 28 has 1 refundable units, cannot refund 2"}
```

拒绝发生在**上游写入之前**（本地 `checkRefundable`），Magento 侧无任何贷项通知单产生——这点是对的。

### P-FIN-15: 可退数量只存在于错误文本里，没有结构化字段；单复数也不对

- **级别**: P2
- **我的工作流**: 客服/前端要显示"最多可退 N 件"，我得从错误里取这个数。
- **发生了什么**: 错误详情只有 `{"code","message"}`，没有 `details`：

```json
{"code":"invalid_argument","message":"order item 29 has 2 refundable units, cannot refund 3"}
```

  只能正则解析人类可读文本；而且 1 件的场景输出 `"1 refundable units"`（复数错误）。
  超额退款被拒后，同参数重试会变成另一个错误（见下），连这句话都拿不到了。

- **我期望什么**: 结构化输出 `{"order_item_id":29,"refundable_qty":2,"requested_qty":3}`，
  让程序能直接渲染"最多可退 2 件"。文本保留作为兜底。
- **状态**: CONFIRMED

### P-FIN-16: 被本地拒绝的超额退款也算"操作失败"，重试拿到的是另一种错误

- **级别**: P2
- **我的工作流**: 客户改口说"那退 2 件吧"，我用同样的参数再试一次（业务上就是同一笔退款）。
- **发生了什么**: 同参数重试：

```json
{"code":"invalid_argument",
 "details":{"operation_id":"7e37ea25-f6ee-48fa-8cb1-6d8eb00468ec","state":"failed"},
 "message":"the operation previously failed and was not retried; start a new operation to try again"}
```

  一次**根本没到上游**的入参校验失败，被记成一条"失败操作"，并且污染了派生幂等键。
  另外这类必然被本地拒绝的请求，仍然要先走一遍人工审批（`confirmation_required` → approve）才被拒，
  审批额度被消耗在一次注定失败的调用上。

- **我期望什么**: 纯本地参数校验失败不应写入操作账本、不应占用幂等键；审批也不应在参数自检之前发生。
- **状态**: CONFIRMED

---

## 六、审计账本（追溯性）：`operations` 里的金额与单据金额不一致

财务要"每个数字能追溯到具体单据"，所以我直接查了网关自己的操作账本（Postgres）：

```bash
ssh -o BatchMode=yes ubuntu@49.51.250.83 \
 "sudo docker exec mage2mcp-postgres-1 psql -U mage2mcp -d mage2mcp -c \
  \"select operation, entity_type, entity_id, amount_minor, state, upstream_ref
    from operations where entity_type in ('invoice','creditmemo') and state='succeeded'
    order by created_at desc\""
```

```text
         operation         | entity_type | entity_id | amount_minor |  state    |     upstream_ref
---------------------------+-------------+-----------+--------------+-----------+----------------------
 magento.creditmemo.create | creditmemo  | 26        |         2900 | succeeded | creditmemo:000000024
 magento.invoice.create    | invoice     | 29        |         4612 | succeeded | invoice:000000026
 magento.creditmemo.create | creditmemo  | 25        |         1712 | succeeded | creditmemo:000000023
 magento.invoice.create    | invoice     | 29        |          856 | succeeded | invoice:000000025
 magento.creditmemo.create | creditmemo  | 23        |         4612 | succeeded | creditmemo:000000022
 magento.creditmemo.create | creditmemo  | 23        |          856 | succeeded | creditmemo:000000021
 magento.invoice.create    | invoice     | 25        |         5468 | succeeded | invoice:000000023
```

### P-FIN-17: 账本 `amount_minor` 只是"商品行小计"，不含运费与调整费，7 张单据累计少记 82.00

- **级别**: P1
- **我的工作流**: 用审计账本做退款台账，核对"账本金额 = 单据金额"。
- **发生了什么**:

| 单据 | `upstream_ref` | 单据 `grand_total` | 账本 `amount_minor` | 差 |
|---|---|---|---|---|
| 发票 | invoice:000000023 | 74.68 | 5468 → 54.68 | −20.00（运费） |
| 发票 | invoice:000000025 | 28.56 | 856 → 8.56 | −20.00（运费） |
| 发票 | invoice:000000026 | 46.12 | 4612 → 46.12 | 0.00 |
| 贷项 | creditmemo:000000021 | 28.56 | 856 → 8.56 | −20.00（运费） |
| 贷项 | creditmemo:000000022 | 46.12 | 4612 → 46.12 | 0.00 |
| 贷项 | creditmemo:000000023 | 37.12 | 1712 → 17.12 | −20.00（运费） |
| 贷项 | creditmemo:000000024 | 31.00 | 2900 → 29.00 | −2.00（adjustment_positive） |
| **合计** | | **292.16** | **210.16** | **−82.00** |

  原因：账本金额来自 `authorisedAmount(...)`，用的是订单行的 `price`（不含运费、不含调整），
  而单据 `grand_total` 含运费与调整费。

- **我期望什么**: 审计账本记的是"这次操作动了多少钱"，就必须等于单据的 `grand_total`；
  否则财务用账本汇总会比实际少 82.00（占上表 7 张单据合计 292.16 的 28.1%）。
  至少要把 `shipping_amount` 与 `adjustment_*` 计入，或另设 `subtotal_minor` / `shipping_minor` 拆开记。
- **状态**: CONFIRMED

### P-FIN-18: `entity_type=creditmemo` 但 `entity_id` 记的是**发票号**（发票侧记的是订单号）

- **级别**: P2
- **我的工作流**: 按 `entity_type + entity_id` 把账本行 join 回单据。
- **发生了什么**: 上表里贷项通知单 21 的账本行是 `entity_type=creditmemo, entity_id=23`，
  而 23 是**发票号**（订单 25 只有一张发票 23；贷项是 21）。真正能 join 上的是 `upstream_ref`
  （`creditmemo:000000021`，字符串）。发票侧又不同：`entity_type=invoice, entity_id=29` 是**订单号**。

```text
invoice:000000023  → entity_type=invoice,    entity_id=25   ← 订单 25
invoice:000000025  → entity_type=invoice,    entity_id=29   ← 订单 29
creditmemo:000000021 → entity_type=creditmemo, entity_id=23 ← 发票 23
```

- **我期望什么**: 三张表（订单/发票/贷项）的关联必须能靠 id 稳定 join。现在同一个字段
  `entity_id` 在不同 `entity_type` 下含义不同（订单号 / 发票号），财务做勾稽只能靠 `upstream_ref`
  字符串解析，`upstream_ref` 又是自由文本。建议 `entity_type` 与实际 id 语义一致
  （贷项用贷项 id，发票用发票 id），并把父单据 id 单独一列。
- **状态**: CONFIRMED

---

## 七、环境异常观察（非本岗位缺陷，但影响对账可信度）

### P-FIN-19: 工具在上游已成功、账本记 succeeded，调用方却收到 `internal`

- **级别**: P3（SUSPECTED，未能稳定复现）
- **发生了什么**: `2026-11-21T19:23:5x–19:26:2xZ` 期间，几乎所有调用都返回：

```json
{"code":"internal","message":"an unexpected error occurred"}
```

  但同一时刻的操作账本把这些调用记为成功：

```text
 2026-11-21 19:24:40.438+00 | magento.sales.summary | pending | pending
 2026-11-21 19:24:40.836+00 | magento.sales.summary | allow   | succeeded
 2026-11-21 19:24:42.005+00 | magento.sales.summary | pending | pending
 2026-11-21 19:24:42.405+00 | magento.sales.summary | allow   | succeeded
 ...（4 次调用，全部 succeeded）
```

  而客户端四次都拿到 `internal`。同一时刻 `magento.product.get` 在 REST 直连下完全正常。
  19:26 之后同一命令连续 12 次全部成功，`go run` 也恢复正常。

  时间上对应一次重新部署：`/opt/mage2mcp/compose.yaml` 的 mtime 是
  `2026-11-22 03:22:25`（本地）= `19:22:25Z`，容器 `Up About a minute`（19:22:40 启动）。

- **我期望什么**: 这是我最怕的一类：**账本说成功、调用方说失败**。财务看到 `internal`
  会重试，而写操作重试就可能重复退款/重复开票。即便根因是部署重启，
  也应在响应里区分"网关内部错误，操作结果未知"（类似已有的 `operation_unknown`），
  而不是笼统的 `internal`。
- **状态**: SUSPECTED（与一次重新部署时间重合，19:26 后 16 次调用未再复现）

---

## 八、汇总

### 8.1 对得上的部分（这些我确认没问题）

| 核对项 | 结果 |
|---|---|
| `sales.summary` 总额 / 单数 / 件数 vs REST 逐单相加 | 完全相等（1531.18 / 25 / 48） |
| `truncated` / `total_available` / `scanned_orders` / `excluded_orders` | 诚实（25/25/0） |
| 全额开票：订单 25 ↔ 发票 23 的 subtotal/shipping/tax/grand_total/qty | 逐项 0 差 |
| 发票行 vs 订单行（price / row_total） | 逐行相等 |
| 两次部分退款合计 vs 订单总额 | 74.68 = 74.68，0 差 |
| 行级退款分摊（1×8.56 + 2×17.12 = 25.68） | 与订单行 row_total 相等 |
| 单发票自动解析 / 多发票强制指定 | 行为正确，错误信息可编程 |
| 数量超额退款 | 本地拒绝，给出可退数量，上游零写入 |
| 三位小数价格 8.555 的退款 | 无二次舍入流失（合计 25.68） |

### 8.2 发现清单

| ID | 级别 | 一句话 | 状态 |
|---|---|---|---|
| P-FIN-01 | P1 | `sales.summary` revenue 含已取消订单、不扣退款（虚增最多 1106.18/1531.18） | CONFIRMED |
| P-FIN-02 | P2 | 金额字段带浮点残差（`1946.3600000000001`、`287.08000000000004`） | CONFIRMED |
| P-FIN-03 | P1 | `sales.summary` 无 tax / refund / discount 字段，财务出不了报表 | CONFIRMED |
| P-FIN-04 | P1 | `sales.analysis.refunded_amount` 只是商品小计，比真实退款少 160.00 | CONFIRMED |
| P-FIN-05 | P3 | `discount` 符号为负，与订单字段"as a positive number"相反 | CONFIRMED |
| P-FIN-06 | P2 | 日拆分口径：summary 按 UTC、analysis 按 Europe/Warsaw | SUSPECTED |
| P-FIN-07 | P1 | 第一次部分退款就把运费全额退掉，且无 `shipping_amount` 参数可控 | CONFIRMED |
| P-FIN-08 | P1 | 带 comment 的贷项通知单必然 400（`is_visible_on_front` bool vs int），原因被吞 | CONFIRMED |
| P-FIN-09 | P2 | 失败毒化派生幂等键（comment 不入哈希），同参数重试被拒 | CONFIRMED |
| P-FIN-10 | P1 | `adjustment_positive` 吃掉可退金额，本地只校验数量 → 最后一件永远退不掉 | CONFIRMED |
| P-FIN-11 | P1 | 贷项通知单在 Magento 侧 `invoice_id=null`，网关返回的是本地推导值 | CONFIRMED |
| P-FIN-12 | P1 | 不做发票级数量校验：可对只开 1 件的发票冲销 2 件（凭证 > 发票） | CONFIRMED |
| P-FIN-13 | P3 | 同一贷项通知单 `total_qty` 在 get 有、list 无 | CONFIRMED |
| P-FIN-14 | P1 | 税率/税规则完全不可查，无法证明 `tax=0` 是配置还是缺陷 | CONFIRMED |
| P-FIN-15 | P2 | 可退数量只在错误文本里（`"2 refundable units"`），无结构化字段；单复数错 | CONFIRMED |
| P-FIN-16 | P2 | 本地校验失败也写操作账本、占幂等键、消耗审批 | CONFIRMED |
| P-FIN-17 | P1 | 账本 `amount_minor` 不含运费/调整费，7 张单据累计少记 82.00 | CONFIRMED |
| P-FIN-18 | P2 | `entity_type=creditmemo` 但 `entity_id` 是发票号；发票侧又是订单号 | CONFIRMED |
| P-FIN-19 | P3 | 上游成功、账本 succeeded，调用方收到 `internal`（疑似部署重启） | SUSPECTED |

### 8.3 给修复阶段的优先级建议

1. **P-FIN-08**（comment 必失败）——一行 `bool → int` 的事，但它是"退款留痕"的硬门槛。
2. **P-FIN-07**（运费全退）——`shipping_amount` 缺失会让每次部分退款都多退运费。
3. **P-FIN-10 / P-FIN-12**（金额上限与发票级数量校验）——这是"差一分查不到底"的两个具体来源。
4. **P-FIN-17 / P-FIN-18**（账本金额与实体标识）——审计链断了，前面修得再好也追不回来。
5. **P-FIN-01 / P-FIN-03 / P-FIN-04**（报表口径）——财务最终交付物就是报表，口径错等于没修。
6. **P-FIN-02**（浮点残差）——修起来便宜，不修则所有对账脚本都不安全。

---

## 附：本文用到的复现命令清单

```bash
export MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo
export MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"
cd /Users/shuaizeng/Documents/mage2mcp && go build -tags live -o /tmp/mcpraw ./test/live

# 销售汇总 / 分析 / 税类
/tmp/mcpraw -mode=raw -tool=magento.sales.summary  -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22"}'
/tmp/mcpraw -mode=raw -tool=magento.sales.analysis -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22"}'
/tmp/mcpraw -mode=raw -tool=magento.tax.classes    -args='{"instance":"demo"}'

# 建单 → 开票 → 部分退款
/tmp/mcpraw -mode=raw -tool=magento.order.create -args="$(cat /tmp/order_create2.json)"
/tmp/mcpraw -mode=raw -tool=magento.invoice.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":28,"qty":1},{"order_item_id":29,"qty":3}],"capture":true}'
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":1}],"idempotency_key":"fin-cm-25-a-20261122"}'

# 单据回读
/tmp/mcpraw -mode=raw -tool=magento.invoice.get     -args='{"instance":"demo","invoice_id":23}'
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.get  -args='{"instance":"demo","creditmemo_id":21}'
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.list -args='{"instance":"demo","order_id":25}'

# 超额退款
/tmp/mcpraw -mode=raw -tool=magento.creditmemo.create \
  -args='{"instance":"demo","order_id":25,"items":[{"order_item_id":29,"qty":3}],"idempotency_key":"fin-cm-25-over-1"}'

# REST 直连复核（不经过网关）
T=$(cat /tmp/magento_token)
curl -sS "https://demo-magento-2.auroracreation.com/rest/V1/orders/25"        -H "Authorization: Bearer $T"
curl -sS "https://demo-magento-2.auroracreation.com/rest/V1/invoices/23"      -H "Authorization: Bearer $T"
curl -sS "https://demo-magento-2.auroracreation.com/rest/V1/creditmemo/21"    -H "Authorization: Bearer $T"

# 操作账本
ssh -o BatchMode=yes ubuntu@49.51.250.83 \
 "sudo docker exec mage2mcp-postgres-1 psql -U mage2mcp -d mage2mcp -c \
  \"select operation, entity_type, entity_id, amount_minor, state, upstream_ref
    from operations where entity_type in ('invoice','creditmemo') and state='succeeded'\""
```

**只记录，未修改任何代码。**
