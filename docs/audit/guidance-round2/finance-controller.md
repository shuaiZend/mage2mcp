# 财务报表与读语义验证（v0.11.9）

任务：验证 `reports` prompt 与 instructions 的 **READS 段五条断言**是否与真实数据一致。
我的身份是财务——这五条是我判断「这个数能不能入账」的全部依据，错一条就会算错账。

**只读**：全程没有建单、开票、退款、改价。所有写操作都是并发 agent 做的，我只观测。

---

## 0. 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | **v0.11.9**（`platform.info` 实测：`{"name":"mage2mcp","version":"v0.11.9"}`） |
| 实例 | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store `pl`，`Europe/Warsaw`，PLN |
| 主快照 | `2026-11-22T19:01:19Z`（UTC）／`20:01:19 +0100`（Warsaw）／`2026-11-23T03:01 +0800`（HKT） |
| 订单数 | 主快照 8 单（全部由并发 agent 在本轮创建） |

### 0.1 必须先说的环境事实：店铺被重置过

我第一次拉「近 30 天」时是 **0 单**。查 REST 确认当时库里只有 2022 年的两条 demo 数据：

```bash
curl -sS -G ".../rest/V1/orders" -H "Authorization: Bearer $T" \
  --data-urlencode 'searchCriteria[pageSize]=200' \
  --data-urlencode 'fields=items[entity_id,increment_id,created_at,state,status,grand_total],total_count'
```

```text
total_count: 2
2 000000002 2022-08-26 12:18:58 closed closed 39.64 39.64
1 000000001 2022-08-26 12:18:58 processing processing 36.39 None
```

上一轮（v0.11.6 岗位测试）留下的 2026-11-21 那批订单全部消失 —— **demo 站被重置回初始 demo 数据**。
20 秒后（18:57:25Z）并发 agent 开始下单，到主快照时累积到 8 单。

**这直接影响结论的读法**：
- 任何「近 30 天报表」的数字都只代表**当前这一瞬间**，5 分钟内就会变；
- 我所有结论都基于「同一时刻 MCP 与 REST 对齐」，而不是「重跑得到同一个数」；
- 2022 年那两单反而很有价值：其中一单**已全额退款**（39.64 / 39.64），正好用来验「revenue 不扣退款」。

---

## 1. `prompts/get reports` 全文

```bash
/tmp/mcpcall.sh prompts/get '{"name":"reports"}'
```

（共 2457 字符，完整摘录关键段落）

```text
# Sales reports

## Why the numbers disagree

- **`revenue` is gross**: every order's `grand_total`, canceled included, refunds not deducted,
  tax and shipping included. Not GA4 revenue, not net sales.
- **`top_skus.revenue` is a line subtotal**: it sums item `row_total` (no shipping, tax or
  discounts), so the list sums to `subtotal`, never to `revenue`.
- **`refunded_amount` is document level** (`total_refunded`, shipping refunds and adjustments
  included); `refunded_items_amount` is the line share only.
- **A day is a store-timezone calendar day**: 23:30 UTC is the next day for a store an hour
  ahead. A date-only `from`/`to` is a day there; `window_local` shows the bounds.

## Recipe: monthly revenue report
1. `magento.store.configs` — confirm timezone and currency once.
2. `magento.sales.summary` — one call per window; capped at 366 days and 1000 orders (oldest
   first). Split long ranges.
3. `magento.sales.analysis` — the same window for tax, discounts, refunds, status/payment mix,
   coupons, top SKUs. Both bucket by the same timezone, so their daily rows must agree; if not,
   report it rather than pick one.
4. Quote `revenue` with its caveat ("canceled included, refunds not deducted") plus
   `refunded_amount`; never call it net revenue.

## Truncation is reported, not hidden
`sales.analysis` reports `truncated`, `excluded_orders`, `total_available` and `paging`;
`orders.geography` reports `truncated` and `unscanned_orders`. Both keep the **oldest** orders when
capped: quote them as "sample, oldest N".
```

### 1.1 instructions 的 READS 段（原文）

从 `initialize` 的 `instructions` 字段取出（共 2474 字符）：

```text
READS: inventory qty is legacy and can be 0 while is_in_stock is true; judge availability by
is_in_stock, per-source numbers by inventory.source_items. captured means this call asked to
capture and the invoice came back paid, not that money moved. Report revenue sums grand_total for
every order in the window, canceled included, refunds not deducted. product price is the global
base price; store-scope price or special_price decides what a shopper pays, so read the order
line. created_at is UTC.
```

---

## 2. 照 prompt 配方出一份「近 30 天」销售报表

按配方第 1 步先确认店铺口径：

```bash
/tmp/mcpraw -mode=raw -tool=magento.store.configs -args='{"instance":"demo"}'
```

```json
{"stores":[{"id":1,"code":"pl","timezone":"Europe/Warsaw",
 "base_currency_code":"PLN","default_display_currency_code":"PLN"}]}
```

窗口 `2026-10-24 → 2026-11-23`（date-only，按 prompt 是「店铺时区的两天」）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.summary \
  -args='{"instance":"demo","from":"2026-10-24","to":"2026-11-23"}'
```

```json
{"summary":{"window":"2026-10-23T22:00:00Z/2026-11-22T23:00:00Z","timezone":"Europe/Warsaw",
 "days":[{"date":"2026-11-22","orders":8,"revenue":929,"items":14}],
 "total_orders":8,"total_revenue":929,"total_items":14,"currency":"PLN",
 "mixed_currency":false,"truncated":false,"total_available":8,"excluded_orders":0,
 "scanned_orders":8}}
```

`window` 已经换算成 UTC，且**跨了夏令时**（10-24 是 +02:00，11-23 是 +01:00）：

| 本地边界 | 换算 UTC | 实测 `window` |
|---|---|---|
| 2026-10-24 00:00 +02:00 | 2026-10-23T22:00:00Z | ✓ 一致 |
| 2026-11-23 00:00 +01:00 | 2026-11-22T23:00:00Z | ✓ 一致 |

→ **prompt 的「A date-only from/to is a day there」成立**，而且跨 DST 的两个偏移都算对了。

### 2.1 同一时刻的 REST 逐单对账（主快照 19:01:19Z）

```bash
curl -sS -G ".../rest/V1/orders" -H "Authorization: Bearer $T" \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][field]=created_at' \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][value]=2026-10-23 22:00:00' \
  --data-urlencode 'searchCriteria[filterGroups][0][filters][0][conditionType]=gteq' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][field]=created_at' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][value]=2026-11-22 23:00:00' \
  --data-urlencode 'searchCriteria[filterGroups][1][filters][0][conditionType]=lt' \
  --data-urlencode 'fields=items[entity_id,increment_id,created_at,state,status,grand_total,subtotal,shipping_amount,tax_amount,total_qty_ordered,total_refunded,subtotal_refunded,shipping_refunded],total_count'
```

| 订单 | 创建时间（UTC） | state | grand_total | subtotal | 运费 | 税 | 件数 | total_refunded |
|---|---|---|---|---|---|---|---|---|
| 000000003 | 2026-11-22 18:57:25 | new | 115.00 | 100.00 | 15.00 | 0 | 3 | — |
| 000000004 | 2026-11-22 18:58:22 | closed | 86.00 | 76.00 | 10.00 | 0 | 2 | 86.00 |
| 000000005 | 2026-11-22 18:58:58 | complete | 200.00 | 190.00 | 10.00 | 0 | 2 | — |
| 000000006 | 2026-11-22 18:59:58 | complete | 100.00 | 90.00 | 10.00 | 0 | 2 | 90.00 |
| 000000007 | 2026-11-22 19:00:05 | **canceled** | 100.00 | 95.00 | 5.00 | 0 | 1 | — |
| 000000008 | 2026-11-22 19:00:38 | **canceled** | 100.00 | 95.00 | 5.00 | 0 | 1 | — |
| 000000009 | 2026-11-22 19:00:53 | processing | 128.00 | 118.00 | 10.00 | 0 | 2 | 62.00 |
| 000000010 | 2026-11-22 19:01:12 | processing | 100.00 | 95.00 | 5.00 | 0 | 1 | — |
| **合计** | | | **929.00** | 859.00 | 70.00 | 0 | 14 | **238.00** |

三方差：`summary.total_revenue = 929.00`，`analysis.revenue = 929.00`，REST `sum(grand_total) = 929.00`
—— **完全相等，没有分差**。`total_items = items_qty = 14 = sum(total_qty_ordered)`。

> 快照是「稳定快照」：MCP 两次调用前后各拉一次 REST，`total_count` 都是 8，才采用。
> 中间有过一次 5→6 单的竞态（summary 601 / analysis 701），我丢弃重取了，没有拿竞态数据下结论。

---

## 3. READS 段五条断言逐条实测

### 断言 1：`inventory qty` 是 legacy，可能是 0 而 `is_in_stock=true`；判断可售看 `is_in_stock`，看单源数量用 `inventory.source_items`

**结论：实测成立。**

```bash
/tmp/mcpraw -mode=raw -tool=magento.inventory.status_batch \
  -args='{"instance":"demo","skus":["WJ01","WJ02","24-MB01","24-MB04","24-UG06"]}'
```

```json
{"items":[
  {"sku":"WJ01","status":{"sku":"WJ01","qty":0,"is_in_stock":true,"manage_stock":true}},
  {"sku":"WJ02","status":{"sku":"WJ02","qty":0,"is_in_stock":true,"manage_stock":true}},
  {"sku":"24-MB01","status":{"sku":"24-MB01","qty":100,"is_in_stock":true,...}}]}
```

扫 25 个 SKU，**12 个商品 `qty=0` 且 `is_in_stock=true`**（WJ01–WJ12，全部是可配置父商品）：

```text
qty==0 AND is_in_stock==true: ['WJ01','WJ02','WJ03','WJ04','WJ05','WJ06','WJ07','WJ08','WJ09','WJ10','WJ11','WJ12']
```

「legacy」这个词也是准确的：`inventory.status` 打的是 CatalogInventory 老接口

```text
internal/magento/inventory_read.go:56
  raw, err := s.client.do(ctx, http.MethodGet, "/stockItems/"+url.PathEscape(sku), nil, nil)
```

单源数量确实要另取（父商品 0，子商品 100）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.inventory.source_items \
  -args='{"instance":"demo","skus":["WJ01","WJ01-S-Blue","24-MB01"]}'
```

```json
{"items":[
 {"sku":"24-MB01","source_code":"default","quantity":100,"status":1},
 {"sku":"WJ01","source_code":"default","quantity":0,"status":1},
 {"sku":"WJ01-S-Blue","source_code":"default","quantity":100,"status":1}]}
```

按 `is_in_stock` 判断是对的：WJ01 前台确实在售。

```bash
curl -sS "https://demo-magento-2.auroracreation.com/pl/stellar-solar-jacket.html" | grep -o 'W magazynie'
# W magazynie      ← 有货
# data-price-amount="75"  data-price-type="finalPrice"
```

**精度提示（不是偏差）**：本店 simple 商品的 `qty` 就是真实源库存（100），只有 configurable
父商品是 0。指令的说法没错，但一个只扫到 simple 商品的 agent 会以为 `qty` 一直没用——反过来
一个只看 `qty` 的 agent 会把 WJ01 判成缺货。这条断言值得保留原文。

### 断言 2：`captured` 表示「本次调用请求了捕获且发票回来是 paid」，不代表钱到账

**结论：无法直接实测（我只读，不能创建发票）；代码与现有数据双向印证，与指令一致。**

生产库里只有两张 2022 年的发票，都是离线支付 `checkmo`，且都 `state=2`：

```bash
curl -sS ".../rest/V1/orders/1" -H "Authorization: Bearer $T" | python3 -c '...'
# state processing  payment checkmo  total_paid 36.39  total_invoiced 36.39  total_due 0
```

```bash
/tmp/mcpraw -mode=raw -tool=magento.invoice.get -args='{"instance":"demo","invoice_id":1}'
```

```json
{"invoice":{"entity_id":1,"increment_id":"000000001","order_id":1,"state":2,
 "grand_total":36.39,"subtotal":29,"shipping_amount":5,"tax_amount":2.39,"total_qty":1}}
```

→ **发票 state=2（paid）、订单 total_paid=36.39，但 checkmo 是线下付款，钱没有「到账」这回事**。
这半边（paid ≠ 钱到账）有实测支撑。

另外半边（`captured` 必须同时满足「本次调用请求了捕获」）只能看代码：

```text
internal/magento/invoice_write.go:139
  Captured:    in.Capture && invoice.State == invoiceStatePaid,
```

注释还直接引用了上一轮我报的 U-14（`capture=false` 曾报 `captured=true`）。所以指令的措辞
（"this call asked to capture **and** the invoice came back paid"）与实现逐字对应。

**能力缺口（不是断言错误）**：`invoice.get` / `invoice.list` 的输出里**没有 `captured` 字段**
——capture 意图是每次调用的入参，不落库。所以财务回读一张历史发票时，无法知道当初是否请求过捕获。
指令说的是 "this call"，措辞没错；但如果要审计历史，没有任何字段可用。建议在
`invoice.create` 的响应之外，至少把 capture 意图写进订单注释或账本 `args_hash` 之外的可读字段。

### 断言 3：`revenue` = 窗口内每张订单的 `grand_total` 之和，含 canceled，不扣退款

**结论：实测成立，两个方向都用数字证死。**

用主快照（8 单，其中 2 单 canceled、3 单有退款）：

| 假设 | 应有金额 | 实测 `revenue` | 判定 |
|---|---|---|---|
| 全部 `grand_total` 相加（指令口径） | 929.00 | **929.00** | ✓ 成立 |
| 若**扣掉退款** 238.00 | 691.00 | 929.00 | ✗ 不成立 → 确认「不扣退款」 |
| 若**排除 canceled** 200.00 | 729.00 | 929.00 | ✗ 不成立 → 确认「含 canceled」 |

`analysis` 的 `by_status` 也印证了 canceled 是**加进去**的，而不是减出来的：

```json
"by_status":[{"key":"canceled","orders":2,"revenue":200},
             {"key":"complete","orders":2,"revenue":300},
             {"key":"processing","orders":2,"revenue":228},
             {"key":"closed","orders":1,"revenue":86},
             {"key":"pending","orders":1,"revenue":115}]
```

`200+300+228+86+115 = 929` ✓，且 `by_status[canceled].revenue = 200 = REST 两单 canceled 之和` ✓。
这正是 `reports` prompt 说的「canceled revenue is `by_status[canceled].revenue`, never a subtraction」。

**「不扣退款」的第二个独立证据（2022 年那单，全额退款）**：

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.summary -args='{"instance":"demo","from":"2022-08-01","to":"2022-09-01"}'
# → total_orders 2, total_revenue 76.03
```

```text
REST: 订单 1 gt=36.39（未退）  订单 2 gt=39.64（total_refunded=39.64，已全额退）
      36.39 + 39.64 = 76.03 = summary.total_revenue
```

如果 revenue 扣退款，这里应该是 36.39。实测 76.03 → **退款一分没扣**。

> 财务含义：这个 `revenue` 不能当「净销售额」入账。25 单窗口里 canceled 占 200/929 = 21.5%，
> 已退款 238/929 = 25.6%，两者叠加最多 47%。`reports` prompt 第 4 步要求「Quote revenue with
> its caveat」，这条必须照做，否则报表直接高估近一半。

### 断言 4：`product price` 是全局基础价；店铺作用域价或 `special_price` 才决定顾客实付

**结论：实测成立，且找到了一个差 25% 的实物证据（24-WB05）。**

先确认「店铺作用域价格」在本店是否存在差异：pl 与 en 两个 store view 各取 300 个商品比价。

```bash
curl -sS -G ".../rest/pl/V1/products" -H "Authorization: Bearer $T" \
  --data-urlencode 'searchCriteria[pageSize]=300' --data-urlencode 'fields=items[sku,price]'
curl -sS -G ".../rest/en/V1/products" -H "Authorization: Bearer $T" ...（同上）
```

```text
products compared: 300   store-scope price differences pl vs en: 0
```

→ 本店**没有** pl/en 之间的店铺作用域价差（至少前 300 个 SKU 没有）。但 `special_price` 通道存在：

```text
24-MB04  price=32  special_price=32.000000   （相等，无效果）
24-WB05  price=32  special_price=24.000000   ← 差 8.00（25%）
24-WB06  price=33  special_price=33.000000
24-UG07  price=12  special_price=12.000000
24-WG09  price=43  special_price=43.000000
24-WG01  price=49  special_price=49.000000
```

MCP 侧（`product.get`）：类型化字段 `price` 就是全局基础价 32，`special_price` 只出现在
`attributes` 字典里，**没有类型化字段**：

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.get -args='{"instance":"demo","sku":"24-WB05"}'
```

```text
typed price: 32
attributes.special_price: 24.000000
attributes.special_from_date: 2022-08-26 00:00:00
has typed special_price key: False
```

顾客实际看到的价（前台，只读 GET）：

```bash
curl -sS "https://demo-magento-2.auroracreation.com/pl/savvy-shoulder-tote.html"
```

```html
<span class="price-label">Cena promocyjna</span>
<span id="product-price-10" data-price-amount="24" data-price-type="finalPrice" class="price-wrapper">
  <span class="price">24,00 zł</span></span>
<meta itemprop="price" content="24" />
<span class="old-price">...（划线原价 32）</span>
```

→ **全局价 32，顾客实付 24.00**。指令说的「read the order line」是对的：报收入只能用订单行金额。

**精度提示**：本店当前**没有任何订单行**包含 24-WB05，所以「order line 与 product price 不一致」
这一情形没有订单证据，只有前台价证据。而且 `special_price` 藏在 `attributes` 里，一个只读
`price` 字段的 agent 会把 24-WB05 高估 8.00（33%）。建议在 `product.get` 的输出描述里点明
「`price` 不含 special_price，实付看 `attributes.special_price` 或订单行」。

### 断言 5：`created_at` 是 UTC

**结论：实测成立，而且能精确到秒。**

三个时钟同时打印，再取最新订单的 `created_at`：

```bash
date -u +'NOW_UTC=%Y-%m-%d %H:%M:%S'
TZ=Europe/Warsaw date +'NOW_WARSAW=%Y-%m-%d %H:%M:%S %z'
TZ=Asia/Hong_Kong date +'NOW_HKT=%Y-%m-%d %H:%M:%S %z'
```

```text
NOW_UTC    = 2026-11-22 19:00:27
NOW_WARSAW = 2026-11-22 20:00:27 +0100
NOW_HKT    = 2026-11-23 03:00:27 +0800

订单 7  created_at = 2026-11-22 19:00:05   （state=canceled）
订单 6  created_at = 2026-11-22 18:59:58   updated_at = 2026-11-22 19:00:27
```

判据：
- 若 `created_at` 是 UTC → 订单 7 比 UTC 现在早 **22 秒**（并发 agent 刚下的单，合理）；
- 若 `created_at` 是 Warsaw 本地时间 → 它应该读作 `20:00:05`（早 22 秒）或 `20:xx`，
  绝不可能是 `19:00:05`（那意味着「1 小时 22 分前」）；
- 实测 `19:00:05` = **UTC 现在 − 22 秒** → **UTC 确认**。

第二重证据：订单 6 的 `updated_at = 19:00:27` 与我打印的 `NOW_UTC` **同一秒**（当时另一个 agent
刚改过它的状态）。本地时间会差 1 小时。

**但这里有一处文档自相矛盾（见第 5 节 D-1）**：订单对象的字段描述写的是
`"Store time, format YYYY-MM-DD HH:MM:SS, no timezone suffix."`
（`internal/magento/order.go:88`）。"Store time" 会被读成「店铺本地时间」。实测值是 UTC。
一个照字段描述做账的财务会把每笔订单的时间往前推 1 小时（欧洲冬令时），跨月边界时直接错期。

---

## 4. 时区分桶：summary 与 analysis 的 `by_day` 是否同一天

**结论：默认口径下实测同一天（修复成立）；但存在一个 prompt 没写的覆盖参数，会让两者必然不一致。**

### 4.1 默认口径：一致 ✓

主快照（19:01:19Z，8 单）：

```text
sales.summary  → days: [{"date":"2026-11-22","orders":8,"revenue":929,"items":14}]
                 timezone: "Europe/Warsaw"
sales.analysis → by_day: [{"date":"2026-11-22","orders":8,"revenue":929}]
                 timezone: "Europe/Warsaw"
                 window_local: {from:"2026-10-24T00:00:00+02:00", to:"2026-11-23T00:00:00+01:00",
                                first_date:"2026-10-24", last_date:"2026-11-22"}
```

两个工具给出**同一天（2026-11-22）、同额（929）**。2022 年窗口也一样：两边都是 `2022-08-26`。
上一轮（v0.11.6）这里是「summary 按 UTC、analysis 按店铺时区」的 P2 缺陷（我的 P-FIN-06 / DA-03），
**本轮已修复**：

```text
internal/magento/order.go:389  summaryDay(...) → reportDay(createdAt, loc)
  // "The bucketing itself is reportDay — the same rule sales.analysis uses — so the two
  //  reports cannot disagree about which day an order belongs to."
```

而且 `sales.summary` 现在也输出了 `timezone` 字段（上一轮没有），`sales.analysis` 输出了
`window_local`。

### 4.2 边界情形：无法实测（数据里没有 23:00–24:00 UTC 的订单）

prompt 举的例子是「23:30 UTC 对早一小时的店是第二天」。要实测需要一张 `23:00–24:00Z` 的订单，
本店当前 8 单集中在 `18:57–19:01Z`，没有该时段订单。当前 UTC 是 19:01，等 4 小时不现实。
**标注：无法验证（缺数据）**，但代码证据（`reportDay` + `summaryDay` 共用同一规则）支持该断言。

### 4.3 偏差：`sales.analysis` 的 `timezone` 参数会让两边**必然**不一致

`reports` prompt 说：

> Both bucket by the same timezone, so their daily rows must agree; if not, report it rather than pick one.

但 `sales.analysis` 的 schema 里有一个 prompt 完全没提的参数：

```bash
/tmp/mcpraw -mode=schema -tool=magento.sales.analysis
# timezone -> "IANA zone for day/hour buckets; defaults to the store view timezone."
```

实测（2022 年窗口，同一批 2 单）：

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.analysis \
  -args='{"instance":"demo","from":"2022-08-01","to":"2022-09-01","timezone":"Pacific/Kiritimati"}'
```

```text
timezone: Pacific/Kiritimati
by_day: [{"date":"2022-08-27","orders":2,"revenue":76.03}]
window_local: {from:"2022-08-01T00:00:00+14:00", ..., first_date:"2022-08-01", last_date:"2022-08-31"}

对照 sales.summary 同窗口 → days: [{"date":"2022-08-26",...}]
```

**同窗口、同两单、同 76.03，日期差一天**（`2022-08-26` vs `2022-08-27`）。
按 prompt 的字面要求，agent 这时应该「report it rather than pick one」—— 但它报的会是一个
**自己制造的假不一致**：只要不传 `timezone`，两边就一致。

`timezone` 参数本身是合理功能（分析师想看某个时区的日报），问题是 **prompt 没提它**，
而 prompt 又给了一条「两边必须一致，不一致就上报」的硬指令。这两条放在一起，会诱导 agent
误报缺陷。建议 prompt 补一句：「`sales.analysis` 接受 `timezone` 覆盖；一旦覆盖，与
`sales.summary`（永远用店铺时区）的 `by_day` 不再可比，不要把差异当缺陷上报」。

---

## 5. `reports` prompt 逐条核对（含偏差）

| # | prompt 的断言 | 实测 | 证据 |
|---|---|---|---|
| 1 | `revenue` is gross：每单 `grand_total`，含 canceled，不扣退款，含税与运费 | **成立** | 929 = 全部 8 单之和；扣退款应为 691、排除 canceled 应为 729，实测 929 |
| 2 | `top_skus.revenue` 是行小计，列表合计等于 `subtotal`，永远不等于 `revenue` | **成立** | `sum(top_skus.revenue)=859.00 = subtotal`，`revenue=929`；另有显式字段 `sku_revenue_basis: "line_subtotal"` |
| 3 | `refunded_amount` 是单据级（含运费退款与调整费）；`refunded_items_amount` 只是行份额 | **成立** | `refunded_amount=238.00 = REST sum(total_refunded)`；`refunded_items_amount=225.00 = sum(subtotal_refunded)`；差 13.00 正是运费退款 |
| 4 | 一天是店铺时区的日历日；date-only 的 from/to 是「那边的两天」；`window_local` 给边界 | **成立（但 `window_local` 只有 analysis 有）** | date-only 窗口换算成 `22:00Z/23:00Z` 且跨 DST 两个偏移都对；analysis 有 `window_local`，summary 只有 UTC `window` + `timezone` |
| 5 | summary「capped at 366 days」 | **成立** | 400 天窗口 → `invalid_argument: "sales window must be at most 366 days"`；恰好 366 天通过 |
| 6 | summary「capped at 1000 orders (oldest first)」 | **无法验证** | 窗口内最多 8 单，触发不了上限 |
| 7 | analysis 与 summary「daily rows must agree」 | **默认成立；传 `timezone` 后必然不一致** | 见 4.3；prompt 未提 `timezone` 参数 |
| 8 | `order.summary` 是 deprecated 别名 | **成立** | 返回 `{"deprecated":true,"use":"magento.sales.summary"}`，revenue 与 summary 相同（76.03），也带 `timezone` |
| 9 | analysis 报 `truncated`/`excluded_orders`/`total_available`/`paging` | **成立** | 四个字段都在；`paging.caveat` 说明 offset 分页非快照一致 |
| 10 | geography 报 `truncated` 与 `unscanned_orders` | **成立** | geography 输出含 `truncated`、`unscanned_orders`、`total_available`、`paging`、`duplicates_skipped` |
| 11 | geography 的 `UNKNOWN` = 没有收货地址 | **无法验证** | 窗口内 8 单全部 `country_id=PL`，没有缺地址订单 |
| 12 | 截断时两者都保留「最旧的」 | **无法验证** | 数据量不足，触发不了上限 |
| 13 | 「Not with: sales.summary — it has no refund or cancellation fields」 | **成立** | summary 的键只有 window/timezone/days/total_*/currency/mixed/truncated/total_available/excluded/scanned |
| 14 | `refunded_skus` 上限 10，`refunded_skus_total` 给真实数量 | **成立** | `refunded_skus_total=4`，`len(refunded_skus)=4`（未触发上限，但字段存在且一致） |

### 偏差汇总（agent 会因此算错账/误报的地方）

| 编号 | 级别 | 偏差 | 影响 |
|---|---|---|---|
| **D-1** | **P1** | 订单字段 `created_at` 的描述写 `"Store time, ... no timezone suffix"`，而 instructions 说 UTC；实测是 UTC | 财务照字段描述做账会把每笔订单错位 1 小时（欧洲冬令时），跨月/跨日边界直接错期 |
| **D-2** | **P1** | `reports` prompt 说「两边 bucket 同一时区，日行必须一致，不一致就上报」，但 `sales.analysis` 有未提及的 `timezone` 参数，覆盖后必然不一致 | agent 会把自己制造的差异当缺陷上报（我实测：2022-08-26 vs 2022-08-27），浪费修复轮次，或更糟——挑一个数当真 |
| D-3 | P2 | prompt 说 `window_local` 给边界，但只有 `analysis` 有；`summary` 只有 UTC `window` + `timezone` | 按 prompt 在 summary 上找 `window_local` 会找不到；要自己换算 |
| D-4 | P2 | `orders.geography` 既没有 `timezone` 也没有 `window_local`，只有 UTC 的 from/to | 只看 geography 的 agent 不知道窗口按哪个时区取整 |
| D-5 | P3 | `product.get` 的类型化 `price` 不含 `special_price`，`special_price` 只在 `attributes` 字典里 | 用 `price` 算价的 agent 会把 24-WB05 高估 8.00（33%）；instructions 已提示「read the order line」，但 product 侧没有类型化字段可用 |
| D-6 | P3 | `invoice.get`/`list` 不暴露 `captured`（capture 意图不落库） | 财务无法审计「这张发票当初是否请求过捕获」；instructions 的措辞 "this call" 本身没错 |

---

## 6. 五条 READS 断言的最终判定

| # | 断言 | 判定 |
|---|---|---|
| 1 | `inventory qty` 是 legacy，可能 0 而 `is_in_stock=true`；可售看 `is_in_stock`，单源看 `source_items` | **实测成立** |
| 2 | `captured` = 本次请求捕获且发票回来 paid，不代表钱到账 | **无法直接验证（只读约束）**；代码与离线发票数据一致，判「成立（间接）」 |
| 3 | `revenue` 含 canceled、不扣退款 | **实测成立**（929 vs 691 / 729 双反证；2022 全额退款单也计入） |
| 4 | `product price` 是全局基础价，实付由店铺作用域价或 `special_price` 决定 | **实测成立**（24-WB05：全局 32，前台 24.00） |
| 5 | `created_at` 是 UTC | **实测成立**（created_at 19:00:05 vs NOW_UTC 19:00:27；Warsaw 为 20:00:27） |

**五条断言的语义全部正确**，没有一条是假的。真正会让人算错账的不是断言内容，而是
**D-1（字段描述与指令冲突）** 和 **D-2（prompt 漏了 timezone 参数）**。

---

## 7. 数据卫生与复现说明

- **只读**：本次全部是 `get`/`list`/`search`/`summary`/`analysis`/`geography`/`status` 类调用，
  没有一次写操作；没有创建订单、发票、退款，也没有改价改库存。
- **快照绑定**：主快照 `2026-11-22T19:01:19Z`（8 单 / 929.00 PLN）。文中所有数字都标了时刻。
- **竞态处理**：数据由并发 agent 持续写入（我观测到 2→8 单、以及一次 5→6 单的竞态）。
  凡涉及「MCP 与 REST 相等」的结论，都在前后各拉一次 REST、`total_count` 一致时才采用；
  出现过一次 summary 601 / analysis 701 的竞态数据，已丢弃重取，未用于结论。
- **店铺重置**：本轮开始时 demo 站只有 2022 年的 2 条 demo 订单（上一轮 v0.11.6 的 2026-11-21
  那批已消失）。这解释了「近 30 天报表为什么一开始是 0 单」，也提醒回归阶段不能拿旧快照比数。

### 复现命令

```bash
# 会话
/tmp/mcpcall.sh prompts/get '{"name":"reports"}'

# 店铺口径
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.store.configs -args='{"instance":"demo"}'

# 报表（默认口径）
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.sales.summary  -args='{"instance":"demo","from":"2026-10-24","to":"2026-11-23"}'
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.sales.analysis -args='{"instance":"demo","from":"2026-10-24","to":"2026-11-23"}'

# 时区覆盖（D-2 复现）
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.sales.analysis \
  -args='{"instance":"demo","from":"2022-08-01","to":"2022-09-01","timezone":"Pacific/Kiritimati"}'

# 断言 1：库存
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.inventory.status_batch -args='{"instance":"demo","skus":["WJ01","24-MB01"]}'
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.inventory.source_items -args='{"instance":"demo","skus":["WJ01","WJ01-S-Blue"]}'

# 断言 4：全局价 vs 实付
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  /tmp/mcpraw -mode=raw -tool=magento.product.get -args='{"instance":"demo","sku":"24-WB05"}'
curl -sS "https://demo-magento-2.auroracreation.com/pl/savvy-shoulder-tote.html" | grep -o 'data-price-amount="[0-9.]*"'

# 断言 5：UTC
date -u +'NOW_UTC=%Y-%m-%d %H:%M:%S'; TZ=Europe/Warsaw date +'NOW_WARSAW=%Y-%m-%d %H:%M:%S %z'

# REST 逐单对账（见 2.1 的完整 curl）
```

**只记录，未修改任何代码。**