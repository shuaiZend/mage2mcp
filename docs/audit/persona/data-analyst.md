# 数据分析师岗位场景测试（v0.11.6）

我的日常任务：从 MCP 拉销售数据做周报/月报——销售汇总、地域分布、商品表现、渠道口径对比，
然后把数字贴进报表给老板看。我最怕的不是报错，是**看起来对但其实少了一块**。

## 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | v0.11.6 |
| 实例 id | `demo` |
| 店铺 | `pl`（`Europe/Warsaw` 时区，PLN，另有 store view `en`） |
| 数据快照 S1 | `2026-11-21T19:29:37Z`，窗口 `2026-01-01 → 2027-01-01`，25 单 |

⚠️ **本轮测试期间其他岗位 agent 在并发下单**，同一窗口的订单数在 20 分钟内从 20 → 22 → 25 → 26 变化。
所以本文所有数字都绑定快照时间；跨时间的差异是数据漂移，不是缺陷（我在 S1 里用 REST 独立复核过）。

独立复核用的 REST 直连（不经过 MCP）：

```bash
TOKEN=$(cat /tmp/magento_token)
curl -g -sS "https://demo-magento-2.auroracreation.com/rest/pl/V1/orders?\
searchCriteria[pageSize]=200&\
searchCriteria[filter_groups][0][filters][0][field]=created_at&\
searchCriteria[filter_groups][0][filters][0][value]=2026-01-01%2000:00:00&\
searchCriteria[filter_groups][0][filters][0][condition_type]=gteq&\
searchCriteria[filter_groups][1][filters][0][field]=created_at&\
searchCriteria[filter_groups][1][filters][0][value]=2027-01-01%2000:00:00&\
searchCriteria[filter_groups][1][filters][0][condition_type]=lt" \
  -H "Authorization: Bearer $TOKEN"
```

---

## 一、五个报表动作的核对结果

### 1) 销售汇总：总额与订单数 ✅ 对得上

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  go run -tags live ./test/live -mode=raw -tool=magento.sales.summary \
  -args='{"instance":"demo","from":"2026-01-01","to":"2027-01-01"}'
```

```json
{"summary":{"window":"2026-01-01T00:00:00Z/2027-01-01T00:00:00Z",
 "days":[{"date":"2026-11-21","orders":25,"revenue":1531.18,"items":48}],
 "total_orders":25,"total_revenue":1531.18,"total_items":48,"currency":"PLN",
 "mixed_currency":false,"truncated":false,"total_available":25,
 "scanned_orders":25,"excluded_orders":0}}
```

REST 独立复核（同一分钟）：`n=25 grand=1531.18 qty=48` —— 完全一致。
`items=48` 也等于 `sum(total_qty_ordered)`，没有把 configurable 子行重复计入。

### 2) 地域分布：与订单数据一致 ✅

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  go run -tags live ./test/live -mode=raw -tool=magento.orders.geography \
  -args='{"instance":"demo","from":"2026-01-01","to":"2027-01-01"}'
```

```json
{"geography":{"orders_scanned":25,"total_available":25,"unscanned_orders":0,
 "duplicates_skipped":0,"truncated":false,"total_orders":25,"total_revenue":1531.18,
 "countries":[{"country_id":"PL","orders":23,"revenue":1463.18},
              {"country_id":"UNKNOWN","orders":2,"revenue":68}]}}
```

REST 逐单统计国家：`PL 23 / 1463.18`、`UNKNOWN 2 / 68.0` —— 一致。
`UNKNOWN` 的 2 单是 `shipping_assignments[0].shipping.address` 为 `null` 的订单（order 13/14）。
**这个桶是诚实的**（没有静默丢掉），但和字段契约不符，见 P-DA-10。

### 3) 商品表现：截断诚实，但「少了一块」不是截断造成的 ❌

```bash
... -tool=magento.sales.analysis -args='{"instance":"demo","from":"2026-01-01","to":"2027-01-01"}'
```

```json
"revenue": 1531.18, "subtotal": 1331.18, "shipping": 200,
"top_skus": [ {"sku":"24-MB01", "qty":…, "revenue":…, "orders":…}, … ]  // 共 12 条
```

- `sum(top_skus.qty) = 48` = `items_qty` ✅
- `sum(top_skus.revenue) = 1331.18` = `subtotal`，**比 `revenue` 少 200 = 运费** ❌
- 字段描述写的是 `"revenue": "Revenue for this window"`，没有任何地方说这是不含运费的行小计

也就是说：商品表现表加起来 1331.18，销售汇总说 1531.18，差 200 谁也说不清（见 P-DA-01）。
截断本身：`top_skus` 上限 50 条、`refunded_skus` 上限 10 条、`top_coupons` 上限 10 条，
当前 12/10/0 条**没有触发**，且 `refunded_skus` 求和 = `refunded_amount` = 639.06 仍然对得上；
但上限一到就是静默丢行（见 P-DA-06）。

### 4) 两个口径对比：summary vs geography vs analysis ✅ 三者一致

快照 S1，窗口 `2026-01-01 → 2027-01-01`：

| 口径 | 订单数 | 营收 | 件数 |
|---|---|---|---|
| `magento.sales.summary` | 25 | 1531.18 | 48 |
| `magento.orders.geography` | 25 | 1531.18 | — |
| `magento.sales.analysis` | 25 | 1531.18 | 48 |
| REST 直连（基准） | 25 | 1531.18 | 48 |
| `magento.order.summary`（deprecated 别名） | 25 | 1531.18 | 48 |

内部一致性也全部通过：
`sum(by_status.orders)=25`、`sum(by_status.revenue)=1531.18`、
`sum(by_country.revenue)=1531.18`、`new_revenue+repeat_revenue=1531.18`、
`sum(refunded_skus.amount)=refunded_amount=639.06`、`sum(aov_bands.orders)=25`。
——**同一个窗口、同一个口径下，四个工具 + REST 完全可复现。这部分做得很好。**

真正对不上的地方不在总额，在**按天分桶**和**截断保留哪一端**（P-DA-03、P-DA-04）。

### 5) 分页：没有重复也没有遗漏 ✅

```bash
# limit=1 逐页翻，cursor 必须传字符串
... -tool=magento.order.search -args='{"instance":"demo","created_from":"2020-01-01","created_to":"2027-01-01","limit":1,"cursor":"0"}'
```

| limit | 页数 | 行数 | 去重后 | 重复 | 遗漏 |
|---|---|---|---|---|---|
| 1 | 24 | 23 | 23 | 0 | 0 |
| 5 | 5 | 23 | 23 | 0 | 0 |
| 7 | 4 | 23 | 23 | 0 | 0 |

再加一轮**慢速翻页**（每页 sleep 0.25s，期间其他 agent 正在下单，数据从 24 涨到 26）：
`attempt 0: pages=25 first=1 last=25 dup=[] missing=[]`、`attempt 1: pages=26 first=1 last=26 dup=[] missing=[]`
—— 没有重复、没有跳过。`total` 字段随数据增长正确变化。
但 `order.search` 的输出只有 `{orders, total}`，**没有任何分页元数据**（对比 analysis 有
`paging.method/order_by/snapshot_consistent/duplicates_skipped/caveat`），见 P-DA-13。

---

## 二、发现清单

### P-DA-01: `top_skus.revenue` 是行小计口径，商品报表与销售汇总差一个运费，且无任何标注

- **级别**: P2
- **我的工作流**: 拉商品表现做「Top SKU 贡献占比」，把各 SKU revenue 加起来当营收分母
- **发生了什么**: 快照 S1，窗口 `2026-01-01 → 2027-01-01`：

  ```json
  "revenue": 1531.18, "subtotal": 1331.18, "shipping": 200, "tax": 0, "discount": 0
  "top_skus": 12 条, sum(revenue)=1331.18, sum(qty)=48
  ```

  差额 200 正好是 `shipping`。代码里 `stat.Revenue += item.RowTotal`（行小计），
  而 `analysis.revenue` 是 `grand_total`。字段描述只有一句 `"revenue": "Revenue for this window"`。
- **我期望什么**: 要么给行小计口径一个明确名字（`line_revenue` / `subtotal_revenue`），
  要么在描述里写清「不含运费与税，求和等于 subtotal 而非 revenue」。
  现在这张表拿去和汇总对账，永远差一截，而且差值随运费率变化，看起来像丢单。
- **状态**: CONFIRMED（数据 + 代码双向确认）

### P-DA-02: `new_orders` / `repeat_orders` 叫「首次/回头客户」，实际是「窗口内单笔/多笔」

- **级别**: P2
- **我的工作流**: 算新客占比、复购率，直接取 `new_orders` / `repeat_orders`
- **发生了什么**: 字段描述是
  `"new_orders": "Orders from first-time customers"`、`"repeat_orders": "Orders from returning customers"`。
  实现只统计窗口内的订单数（`agg.orders > 1` 才算 repeat），完全不看窗口之前的订单。
  复现：customer 1 在 2022 年有 2 单、2026-11-21 有 15 单；取只含它一单的窗口：

  ```bash
  ... -tool=magento.sales.analysis \
    -args='{"instance":"demo","from":"2026-11-21T17:31:00Z","to":"2026-11-21T17:32:00Z"}'
  ```

  ```json
  {"orders":1,"revenue":117,"customers":1,"guest_orders":0,
   "new_orders":1,"new_revenue":117,"repeat_orders":0,"repeat_customers":0}
  ```

  一个 2022 年就有订单、历史共 17 单的老客户被报成「first-time customer」。
  另外 `customers` 只数注册客户（guest 被排除），但 `new_orders+repeat_orders` 也排除了 guest，
  所以有 guest 的店 `new+repeat ≠ orders`，也没写。
- **我期望什么**: 描述改成「窗口内仅下单一次的客户订单」；或者真按窗口前是否有单来算。
  现在这两个字段在月报里会系统性高估新客（跨月窗口越窄，老客户越容易被算成新客）。
- **状态**: CONFIRMED（数据 + 代码）

### P-DA-03: 按天分桶一个用 UTC、一个用店铺时区，`summary` 连时区字段都没有

- **级别**: P2
- **我的工作流**: 用 `sales.summary` 出日销趋势，再用 `sales.analysis` 的 `by_day` 交叉验证
- **发生了什么**: `sales.summary` 的日桶是 `created_at[:10]` 裸切字符串（UTC 日），
  `sales.analysis` 的日桶按 `timezone`（默认店铺 `Europe/Warsaw`）换算。
  同一窗口两个工具给出不同日期：

  ```bash
  # summary：日桶 2026-11-21，且输出里没有 timezone 字段
  ... -tool=magento.sales.summary -args='{"instance":"demo","from":"2026-11-21","to":"2026-11-22"}'
  # {"window":"2026-11-21T00:00:00Z/2026-11-22T00:00:00Z",
  #  "days":[{"date":"2026-11-21","orders":25,"revenue":1531.18,"items":48}], ...}

  # analysis 同一窗口，时区一换，日期就跨天（甚至跨出窗口右边界）
  ... -tool=magento.sales.analysis \
    -args='{"instance":"demo","from":"2026-11-21","to":"2026-11-22","timezone":"Pacific/Kiritimati"}'
  # {"from":"2026-11-21T00:00:00Z","to":"2026-11-22T00:00:00Z",
  #  "timezone":"Pacific/Kiritimati","orders":25,"revenue":1531.18,
  #  "by_day":[{"date":"2026-11-22","orders":25,"revenue":1531.18}]}
  ```

  `by_day` 的日期可以落在 `[from, to)` 之外（上例落在 `to` 当天）。
  对默认时区同理：一笔 `23:30Z` 的订单，summary 记 11-21，analysis 记 11-22（Warsaw 00:30）。
  当前 demo 的订单都集中在 17:31–19:29Z，所以碰巧不跨天，差异没暴露。
- **我期望什么**: `sales.summary` 也要有 `timezone` 入参和输出字段（或明确写死「日桶为 UTC 日」）；
  `by_day` 的日期键要和请求窗口同一个时区基。否则两张日销表对不上，且错的是「差一天」这种最难发现的方式。
- **状态**: CONFIRMED（机制 + 时区参数实证；具体跨天订单当前数据集未命中）

### P-DA-04: 截断时一个保留最旧、一个保留最新，且 geography 不说保留的是哪一端

- **级别**: P2
- **我的工作流**: 用 `max_orders` 控成本跑一遍，看「样本」里的地域/商品结构
- **发生了什么**: 同一窗口同一上限，两个工具截出来的样本完全不同：

  ```bash
  ... -tool=magento.sales.analysis  -args='{"instance":"demo","from":"2026-01-01","to":"2027-01-01","max_orders":1}'
  # {"orders":1,"revenue":117,"total_available":25,"excluded_orders":24,"truncated":true}
  #   → 117 = 最早的订单（entity 3, 17:31:24Z）

  ... -tool=magento.orders.geography -args='{"instance":"demo","from":"2026-01-01","to":"2027-01-01","max_orders":1}'
  # {"orders_scanned":1,"total_available":25,"unscanned_orders":24,"truncated":true,
  #  "total_revenue":68,"countries":[{"country_id":"PL","orders":1,"revenue":68}]}
  #   → 68 = 最新的订单（entity 27, 19:28:58Z）
  ```

  analysis 输出 `paging.order_by = "created_at asc, entity_id asc"`，还能知道保留的是最旧；
  geography 输出里**只有 truncated/unscanned，没有任何排序信息**。
  `sales.summary` 的代码注释写 "scans at most maxSummaryOrders orders (newest first)"，
  但 `orderSearchCriteria` 根本没设 `sortOrders`，实测是 entity_id 升序（最旧优先），注释与实际相反。
- **我期望什么**: 三个工具统一截断方向，并在输出里声明（geography 补 `paging.order_by`）。
  现在的后果是：`truncated=true` 的两张表根本不可比，而地域分布按最新 N 单算，会系统性偏向最近的促销活动。
- **状态**: CONFIRMED（数据 + 代码）

### P-DA-05: `sales.summary` 有 366 天窗口上限但工具描述没写，`sales.analysis` 却没有任何上限

- **级别**: P3
- **我的工作流**: 拉近两年销售汇总
- **发生了什么**:

  ```bash
  ... -tool=magento.sales.summary -args='{"instance":"demo","from":"2020-01-01","to":"2027-01-01"}'
  # {"code":"invalid_argument","message":"sales window must be at most 366 days"}
  ```

  工具描述只说 "over a bounded window"，没给数字。同样的窗口 `sales.analysis` 能跑
  （3 年窗口正常返回 `{"orders":25,"revenue":1531.18}`），因为它只受 20000 单限制。
  两个工具对「能拉多长」的策略不一致，且只在报错时才告诉我。
- **我期望什么**: 描述里写明 366 天；或者两个工具用同一套窗口策略。
- **状态**: CONFIRMED

### P-DA-06: `top_skus`(50) / `refunded_skus`(10) / `top_coupons`(10) 静默截断，没有「共 N 条」标记

- **级别**: P3
- **我的工作流**: 把商品表现表当完整清单导出，算长尾占比
- **发生了什么**: 三个列表在代码里 `if len(out) > limit { out = out[:limit] }`，输出里没有任何
  `total_skus` / `truncated` / `of` 字段。当前数据 `top_skus=12`、`refunded_skus=10`（正好卡在上限）、
  `top_coupons=0`，且 `sum(refunded_skus.amount)=639.06 = refunded_amount` 仍能对上，
  所以**这次没丢**；但下一个退款 SKU 出现后，列表就会少一行而金额仍显示 639.06+。
- **我期望什么**: 给列表加一个 `*_total` 计数（哪怕只是 `skus_returned: 50 / skus_total: 137`）。
  现在的唯一发现方式是「自己把列表加一遍和汇总比」，这不该是我的工作。
- **状态**: SUSPECTED（代码确认；当前数据未触发上限）

### P-DA-07: `top_coupons` 描述说「按营收排序」，实际按订单数排序，且没有 revenue 字段

- **级别**: P3
- **我的工作流**: 找「最赚钱的优惠券」
- **发生了什么**: 输出 schema 描述 `"top_coupons": "Coupons by revenue, highest first."`，
  但 `CouponStat` 只有 `code` / `orders`，`topCoupons()` 排序键是 `Orders`，降序。
  窗口内 `coupon_orders=0`，本次无法用数据证伪，仅代码级。
- **我期望什么**: 描述改成「by order count」，或补 revenue 并按 revenue 排序。
- **状态**: SUSPECTED（代码确认；无优惠券数据可复现）

### P-DA-08: `aov_bands` 标签硬编码美元符号，店铺是 PLN

- **级别**: P3
- **我的工作流**: 直接把客单价分布表贴进波兰店月报
- **发生了什么**:

  ```json
  "aov_bands":[{"label":"<$100","orders":22},{"label":"$100-300","orders":3},
               {"label":"$300-600","orders":0},{"label":"$600-1k","orders":0},
               {"label":"$1k-2k","orders":0},{"label":">$2k","orders":0}]
  ```

  同一响应里 `currency` 是 `PLN`。字段描述还是 `"label": "Label shown to the shopper"`
  （这是前台展示用文案，被复制到了报表字段上）。
- **我期望什么**: 标签跟随币种（`<100 PLN`），或干脆给 `lower`/`upper` 数值让客户端自己格式化。
- **状态**: CONFIRMED

### P-DA-09: `ship_latency` 没有单位，且三处字段描述是复制粘贴错位

- **级别**: P3
- **我的工作流**: 报「平均发货时长」
- **发生了什么**: `ship_latency` 的字段描述只有 "Time from order to shipment."，
  实际值是**天**（小数）。快照 `2026-11-21T19:26:57Z`（当时 10 张发货单）输出
  `{"avg":0.0003298611111111111,"median":0.00037037037037037035,
  "p90":0.0004050925925925926,"max":0.0004050925925925926}`；
  我用 REST 拉了同一批 10 张发货单，逐单算 order→shipment 延迟
  （6s/20s/27s/32s/32s/32s/33s/34s/34s/35s，合计 285s，均值 28.5s = 0.00032986 天），
  **与工具输出逐位吻合**——计算是对的，只是没说单位，一个 `0.00033` 摆在那里
  没人知道是 28 秒还是 28 小时。快照 S1 时新到货的发货单把 `avg` 推到 0.0055、
  `max` 0.0572（≈82 分钟），也符合实际。
  同批错位描述：`ship_latency.max` = "Upper bound of the band."、
  `shipping_methods[].method` = "How the scan was paged: offset or cursor."
- **我期望什么**: 描述写 "Time from order to shipment, in days"。
- **状态**: CONFIRMED（数值已用 REST 复核正确，仅单位/描述缺失）

### P-DA-10: `country_id` 契约是 ISO 3166-1 alpha-2，实际会返回 `UNKNOWN` 哨兵

- **级别**: P3
- **我的工作流**: 按国家码 join 汇率表/地图
- **发生了什么**: 字段描述 `"country_id": "ISO 3166-1 alpha-2 country code, uppercase."`，
  实测返回 `{"country_id":"UNKNOWN","orders":2,"revenue":68}`（这两单的
  `shipping_assignments[0].shipping.address` 是 `null`）。`sales.analysis.by_country` 用同样的哨兵。
- **我期望什么**: 桶本身很诚实（我赞成不丢），但要在描述里说明存在 `UNKNOWN` 哨兵值，
  否则下游按 ISO 校验/映射会直接报错或落空。
- **状态**: CONFIRMED

### P-DA-11: `order.search` 的 `cursor` 声明为 string，传整数直接 400

- **级别**: P3
- **我的工作流**: 翻页时自然传 `"cursor": 0`
- **发生了什么**:

  ```bash
  ... -tool=magento.order.search -args='{"instance":"demo","limit":5,"cursor":0}'
  # {"code":"invalid_argument","message":"arguments do not match the tool schema",
  #  "details":{"validation":"validating root: validating /properties/cursor: type: 0 has type \"integer\", want \"string\""}}
  ```

  schema 里 `"cursor": {"type":"string","pattern":"^(0|[1-9][0-9]{0,8})$"}`，注释写
  "Zero-based row offset; must be a multiple of limit"——是个偏移量，却要求字符串；
  同族参数 `max_orders` / `limit` / `store_id` 都是 integer。传 `"cursor":"0"` 才成功。
- **我期望什么**: 类型统一成 integer（或把错误信息改成「cursor 需为字符串形式的偏移量」）。
- **状态**: CONFIRMED

### P-DA-12: `sales.summary` 恰好 1000 单时会误报 `truncated=true`

- **级别**: P3
- **我的工作流**: 信 `truncated` 字段判断报表是否完整
- **发生了什么**: 代码里 `scannedAll` 只在「某页返回不足 limit」时置真，循环条件是
  `offset < 1000`。若窗口正好 1000 单：5 页 × 200 全部取满，循环因 offset 到 1000 退出，
  `scannedAll` 仍为 false → `truncated = excluded>0 || !scannedAll = true`，
  但同时 `total_available=1000, scanned_orders=1000, excluded_orders=0`。
  工具描述说 truncated 是 "whether anything was excluded"，自相矛盾。
  对照：`sales.analysis` 在恰好 20000 单时 `truncated=false`，两者行为不一致。
- **我期望什么**: `excluded_orders>0` 才置 truncated，或把两种事实分开表达。
- **状态**: SUSPECTED（代码确认；当前 25 单数据集无法触发）

### P-DA-13: `order.search` 没有任何分页元数据/去重保护

- **级别**: P3
- **我的工作流**: 用 order.search 自己拼一份订单明细清单
- **发生了什么**: 输出只有 `{"orders": [...], "total": 25}`；对比 `sales.analysis` 有
  `paging.method/order_by/snapshot_consistent/duplicates_skipped/caveat`，
  `orders.geography` 有 `duplicates_skipped`。order.search 既没设 `sortOrders`
  （`orderSearchCriteria` 里只有 filters），也不去重、不报告偏移。
  实测（limit=1 慢速翻页，期间其他 agent 在下单）25/26 页**没有重复也没有遗漏**，
  因为新订单的 entity_id 更大、追加在末尾。但这是数据巧合，不是契约保证：
  一旦有订单被删（demo 站会周期性重置）或默认排序变化，偏移翻页就会漏行且无法察觉。
- **我期望什么**: 显式 `sortOrders`（created_at + entity_id，和 analysis 一致）+ 一个
  `duplicates_skipped`/`paging` 字段，至少让「漏了一行」有迹可循。
- **状态**: SUSPECTED（未观察到实际丢失；缺的是保证与可观测性）

### P-DA-14: `canceled` 订单计入营收，退款不冲减营收——三个工具一致但都没说明

- **级别**: P3
- **我的工作流**: 用 `revenue` 当「销售额」
- **发生了什么**: 快照 S1 里 `by_status` 含 `{"key":"canceled","orders":4,"revenue":261}`，
  而 `revenue=1531.18` 包含这 261；`refunded_amount=639.06` 也是独立字段，没有从 revenue 里扣。
  summary / geography / analysis 三者口径一致（这点很好），但 summary 的描述只说
  "Revenue is order grand_total (tax and shipping included)"，没提状态过滤与退款关系。
- **我期望什么**: 描述里补一句「包含所有状态（含 canceled），退款单独报，未冲减」。
  否则「销售额」到底是 GMV 还是净销售额，只能靠猜。
- **状态**: CONFIRMED（口径一致，属文档缺口）

### P-DA-15: `sales.summary` / `orders.geography` 没有 store 维度过滤

- **级别**: P3
- **我的工作流**: 分 store view 出报表（本店有 `pl` 和 `en`）
- **发生了什么**: `store.configs` 返回两个 store view（`pl` / `en`），但 summary 与 geography
  既没有 `store_id` 入参，输出也没有分店维度，只能全站汇总；
  `sales.analysis` 有 `by_store`（当前全部 `{"key":"PL","orders":25}`）。
  `order.search` 是有 `store_id` 的，所以缺口在聚合层。
- **我期望什么**: summary/geography 至少接受 `store_id`，或输出里给 `by_store` 分桶。
- **状态**: CONFIRMED（能力缺口；当前订单全在 store 1，未造成数字错误）

### P-DA-16: 金额出现二进制浮点尾数，报表对账需要自己 round

- **级别**: P3
- **我的工作流**: 把 JSON 数字直接贴报表/参与对账
- **发生了什么**:

  ```json
  "by_status":[{"key":"processing","orders":6,"revenue":566.6800000000001}, ...]
  ```

  同一响应里 `revenue: 1531.18` 是干净的，分组维度就出尾数。
- **我期望什么**: 金额按分（或 2 位小数）序列化。现在任何「sum 分组 == 总额」的断言都得先 round，
  否则会出现 1531.1799999999998 这种「差一分钱」的对账事故。
- **状态**: CONFIRMED

---

## 三、我核对通过的部分（不是缺陷，记下来备查）

1. **四个工具 + REST 总额完全一致**：25 单 / 1531.18 PLN / 48 件（快照 S1），
   `order.summary`（deprecated 别名）数字与 `sales.summary` 相同并带 `deprecated=true, use=...`。
2. **分组求和闭合**：`by_status`、`by_country`、`by_payment`、`by_store`、`currencies`、
   `aov_bands`、`new+repeat`、`refunded_skus` 全部能加回总额/总数。
3. **items 口径正确**：`total_items=48` = `sum(total_qty_ordered)`，configurable 子行没有重复计。
4. **hour / weekday 分桶用了店铺时区**：订单 17:31–19:29Z（Warsaw +1h）→
   `hour[18]=5, hour[19]=15, hour[20]=5`，`sum(hour)=25=orders`，`weekday[5]=25`
   （2026-11-21 是周六）—— 正确。
5. **ship_latency 计算正确**：快照 `19:26:57Z` 的 avg 0.00032986 天与 REST 逐单算的
   285s/10 = 28.5s 完全吻合（只是缺单位，见 P-DA-09）。
6. **空窗口诚实**：`{"days":[],"total_orders":0,"total_revenue":0}`、`{"countries":[]}`、
   `{"top_skus":[]}` —— 不编造数字，也不报错。
7. **边界报错清晰**：`to<=from` → "to must be after from"；`timezone:"Mars/Olympus"` →
   "not a valid IANA zone"；`max_orders` 超上限 → schema 校验拒绝。
8. **分页无重复无遗漏**：limit=1/5/7 三种步长 + 慢速翻页（并发写入期间）全部 23/25/26 行不重不漏。
9. **truncated 语义诚实**：`max_orders=1` 时三个工具都给出
   `truncated=true` + `total_available` + `excluded/unscanned`，没有假装完整。
10. **`order.search` 的 `cursor` 超出末尾**返回 `{"orders":[],"total":25}`，不报错也不编造。

---

## 四、给修复阶段的建议（按我的痛点排序）

1. **口径命名**：`top_skus.revenue` → 明确是行小计（或补 `grand_total` 口径的 SKU 营收），
   并在 `analysis` 顶部给出「各分组口径」一句话说明。这是我最容易被坑的一条。
2. **时区统一**：`sales.summary` 加 `timezone` 入参/输出，日桶与请求窗口同基；
   `by_day` 日期键不得落在 `[from,to)` 之外。
3. **截断三件套**：所有列表类输出补 `*_total` / `returned` 计数；截断方向统一并在输出里声明
   （geography 补 `paging.order_by`）；summary 的 "newest first" 注释与实现对齐。
4. **新客/复购定义**：要么改实现（看窗口前订单），要么改描述（窗口内单笔/多笔）。
5. **字段描述清理**：`aov_bands.label`（"Label shown to the shopper"）、
   `ship_latency.max`（"Upper bound of the band"）、`shipping_methods.method`
   （"How the scan was paged"）三处复制粘贴错位；`ship_latency` 补单位「天」；
   `top_coupons` 排序键与描述对齐；`country_id` 说明 `UNKNOWN` 哨兵。
6. **金额序列化**按 2 位小数，避免浮点尾数进报表。

---

## 五、可复现性说明

- 本文所有 `go run -tags live` 命令都是 `docs/audit/persona/README.md` 里的标准调用形式，
  直接复制即可重跑（需 `MCP_LIVE=1` 与隧道在）。
- **数字会漂移**：测试期间其他岗位 agent 在并发下单，同一窗口 20 分钟内从 20 → 22 → 25 → 26 单。
  我给出的每条数字都标了快照时间（S1 = `2026-11-21T19:29:37Z`）。
  验证「可复现」的方式不是重跑得到同样的 1531.18，而是**同一时刻 MCP 与 REST 直连给出同样的数**
  —— 这一点在 S1 上是成立的（25 / 1531.18 / 48 / 639.06 / PL 23+UNKNOWN 2）。
- 订单状态是活的（观察期间有一单从 `pending` 变 `processing`），
  所以「by_status 分桶」必须和 REST 在同一秒采样才能对齐；S1 的 19:29:37Z（MCP）
  与 19:29:40Z（REST）状态分布一致：closed 10 / processing 6 / complete 5 / canceled 4。
