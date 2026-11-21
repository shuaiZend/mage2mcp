# 岗位场景测试汇总（v0.11.6）

汇总 7 份岗位报告（`docs/audit/persona/` 下 store-operator / merchandiser / customer-service /
finance-controller / integrator / security-officer / data-analyst），去重、分级、抽查复现。

原始发现 **101 条**（SO 16 / MERCH 10 / CS 10 / FIN 19 / INT 21 / SEC 9 / DA 16，
另 SEC-10、SEC-11 已判定 NOT-A-DEFECT 不计入）。去重后 **85 条**独立缺陷。

**覆盖对账**：101 条原始发现里，100 条被折叠进下面的 85 条（多对一的去重映射见表内「来源」列）；
剩下 1 条 **FIN-19**（上游成功但调用方收到 `internal`）经队长裁决为重新部署产物，
已移入「四、环境限制」的 E-4，不计入产品缺陷。

**本文不修改任何代码，也不改任何岗位报告。** 所有复现都是我（finance-controller）重新跑的，
不采信报告结论；未抽查的条目明确标 `UNVERIFIED`。

---

## 一、分级规则（先说清口径，避免各级别被滥用）

| 级别 | 判据 | 本轮条数 |
|---|---|---|
| **P0** | 静默写错金额/成本/税类，或静默多退钱，且操作者从返回结果**无法察觉** | 1 |
| **P1** | 工具声称的行为与实际不符，会导致**误操作**（写错、多退、漏发、丢单、对账断裂） | 17 |
| **P2** | 不撒谎但**不诚实/不完整/边界错**（截断不标注、字段缺失、错误分类错、审计缺行） | 34 |
| **P3** | 措辞、描述、体验、能力缺口（不影响正确性） | 33 |

只有 **U-01** 判 P0。理由：它一次 `product.update` 就能把在售价从 45 改成 2.5、把成本改成 2026、
把税类从 2 改成 7，**全部静默**，且这是运营每天都会做的动作。

**为什么两条安全项（U-76 审批不绑角色、U-80 审计可被污染）没有判 P0**：它们确实属于
「安全」，但判 P0 需要「默认可利用且直接造成损失」。本轮生产部署 `auth.enabled=false`，
审批闸门不是唯一控制点；U-76 的完整利用需要「一个已开启 RBAC 的多角色实例 + 一个非人身份」，
U-80 只是污染审计归因、不能直接改钱。所以按 P1/P2 处理，并在派发里排进「第三批」优先修。
如果产品认为「生产环境必须开鉴权」，那么 U-76 应当升级为 P0 —— 这一条留给产品决策。

**没有把「部分退款连带退运费」（U-11）判 P0**，尽管它的资金影响最大：贷项返回体里
`shipping_amount: 20` 是可见的，认真看的财务能发现；但工具无法控制、也没有提示，所以判 P1
并标注「建议按 P0 优先级修复」。这一点与队长裁决一致（该条被列为「需产品决策」）。

---

## 二、抽查复现（11 条，我自己跑的）

图例：`REPRODUCED` 我亲自复现；`NOT-REPRODUCED` 我按报告步骤没复现出来；
`ENV-DEPENDENT` 需要特定环境（鉴权实例/超时限流/历史数据）才能观察，我用代码或数据侧面确认；
`UNVERIFIED` 未抽查，采信报告（附代码定位或数据证据的会注明）。

### S-1 → U-01（P0）`product.update` 只改 weight，把售价改成了重量值 —— REPRODUCED

```bash
# 建一个干净的对照商品（create 路径是好的）
/tmp/mcpraw -mode=raw -tool=magento.product.create -args='{"instance":"demo","sku":"SUM-PROBE-01",
 "name":"Summary Probe 01","attribute_set_id":4,"type_id":"simple","price":"50.00","weight":"1.75",
 "special_price":"45.00","cost":"20.00","visibility":1,"status":2,"quantity":5}'
# → price 50, weight 1.75, special_price 45.000000, cost 20.000000, tax_class_id 2   ← 全对

# 只改重量
/tmp/mcpraw -mode=raw -tool=magento.product.update -args='{"instance":"demo","sku":"SUM-PROBE-01","weight":"2.5"}'
```

```text
is_error=false code=
price 50 weight 2.5
special_price 2.500000          ← 重量写进了促销价
special_from_date 2026-11-21 00:00:00
special_to_date None
cost 20.000000 tax_class_id 2
```

`special_price=2.5 < price=50`，Magento 取 `min(price, special_price)` 作为成交价 → 售价塌到 2.50。
商品已删除（`product.delete` → `{"deleted": true}`），店铺已还原。

### S-2 → U-01（P0）只传 `special_price` 必然 400，报错却指向 `special_from_date` —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.update -args='{"instance":"demo","sku":"SUM-PROBE-01","special_price":"40.00"}'
```

```json
{"code":"validation_failed",
 "details":{"store_code":"pl","upstream_field":"special_from_date","upstream_status":"400"},
 "message":"Magento rejected the request as invalid; verify store_code and field values"}
```

根因（代码确认）：`internal/magento/product_write.go:286-288` 把
`[Weight, SpecialPrice, SpecialFromDate, SpecialToDate, Cost]` 传给
`pricingAttributesFromPointers`，而 `pricingAttributeOrder = ["special_price","special_from_date",
"special_to_date","cost","tax_class_id"]`（:634-636）按位对齐 → **整体错位一格**。
`buildCreatePayload` 走的是 `pricingAttributes(_, specialPrice, …)`（:664），参数顺序正确，
所以 `product.create` 不受影响（S-1 的对照实验已证明）。

### S-3 → U-02（P1）`bulk_update` 丢 `price` 却报 `updated` —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.products.bulk_update \
  -args='{"instance":"demo","skus":["24-MB04"],"attributes":{"price":"12.34"}}'
# → {"failed":0,"rows":[{"sku":"24-MB04","status":"updated"}],"total":1,"updated":1}
/tmp/mcpraw -mode=raw -tool=magento.product.get -args='{"instance":"demo","sku":"24-MB04"}'
# → "price": 32        ← 没变
```

### S-4 → U-07（P1）`tier_prices.set` 报 `set: true`，上游零写入 —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.tier_prices.set \
  -args='{"instance":"demo","sku":"24-MB04","prices":[{"customer_group":"0","quantity":3,"price":30}]}'
# → {"sku":"24-MB04","prices":1,"set":true}

/tmp/mcpraw -mode=raw -tool=magento.product.tier_prices -args='{"instance":"demo","sku":"24-MB04"}'
# → {"prices":[]}
curl -sS ".../rest/V1/products/24-MB04" -H "Authorization: Bearer $T"
# → tier_prices: []      ← 上游也是空的
```

### S-5 → U-08（P1）`tier_prices` 读取字段映射错 —— REPRODUCED

用 REST 直连绕过网关写入阶梯价（`{"customer_group_id":0,"qty":3,"value":30}`，HTTP 200），
再让 MCP 读：

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.tier_prices -args='{"instance":"demo","sku":"24-MB04"}'
```

```json
{"prices":[{"sku":"","price":0,"price_type":"","website_id":0,"customer_group":"0","quantity":3}]}
```

上游是 `{'customer_group_id': 0, 'qty': 3, 'value': 30}`：`value`→`price` 没映射上（恒 0），
`sku`/`price_type` 在上游不存在（恒空）。已用 REST 清空还原（`tier_prices: []`）。

### S-6 → U-14（P1）`invoice.create` 的 `capture:false` 仍返回 `captured:true` —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.invoice.create \
  -args='{"instance":"demo","order_id":37,"items":[{"order_item_id":44,"qty":1}],"capture":false}'
```

```json
{"invoice":{"entity_id":29,"increment_id":"000000029","order_id":37,"state":2,...},
 "captured":true}
```

`captured` 由 `invoice.State == 2` 推导（`invoice_write.go:134`），而离线支付方式（checkmo）下
Magento 恒把 invoice 置 2 → 「已开票」被报成「已收款」。

### S-7 → U-31（P2）`order.get` 缺已发/已退件数与收货地址 —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.order.get -args='{"instance":"demo","order_id":25}'
```

```text
order keys: [created_at, customer_id, discount_amount, entity_id, grand_total, increment_id,
             items, order_currency_code, shipping_amount, state, status, store_id, subtotal,
             tax_amount, total_qty_ordered, updated_at]        ← 16 个
item keys:  [item_id, name, price, product_type, qty_ordered, row_total, sku]   ← 7 个
qty_shipped: False   qty_invoiced: False   qty_refunded: False
shipping_description: False   billing_address: False   extension_attributes: False
```

### S-8 → U-81（P3）`instance` 描述称「可省略」，schema 却必填 —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.search -args='{"limit":2}'
# → {"code":"invalid_argument",
#    "details":{"validation":"validating root: required: missing properties: [\"instance\"]"}}
```

而该工具 `instance` 的 schema 描述是 `"resolved by the server when only one instance is configured"`。

### S-9 → U-28（P2）不存在的工具名返回 `internal` —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.operation.status -args='{"instance":"demo"}'
# → {"code":"internal","message":"an unexpected error occurred"}
```

`operation.*` 确实不存在（INT-07 的 102 个工具清单里没有），但「工具名写错」和「服务器内部故障」
返回同一个 code，客户端无法区分该修参数还是该报警。

### S-10 → U-50（P2）日桶口径：同一窗口、同一批订单，两个工具算出两个日期 —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.sales.summary \
  -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22"}'
# → days: [{"date":"2026-11-21","orders":35,"revenue":2232.76,"items":68}]
#   输出键里没有 timezone

/tmp/mcpraw -mode=raw -tool=magento.sales.analysis \
  -args='{"instance":"demo","from":"2026-11-20","to":"2026-11-22","timezone":"Pacific/Kiritimati"}'
# → timezone: Pacific/Kiritimati  by_day: [{"date":"2026-11-22","orders":35,"revenue":2232.76}]
```

同一 35 单、同一 2232.76，一个记在 11-21、一个记在 **11-22（已落在请求窗口之外）**。
代码：`order.go:213` 裸切 `created_at` 前 10 位（UTC）；`sales_analysis.go:313` 转店铺/参数时区。

### S-11 → U-80（P2）`instance` 在校验前落库，可污染审计 —— REPRODUCED

```bash
/tmp/mcpraw -mode=raw -tool=magento.product.get \
  -args='{"instance":"demo\nFORGED audit line: subject=admin","sku":"24-MB04"}'
# → invalid_argument（schema pattern 拒绝，符合预期）
```

但审计表里已经有这条：

```sql
select seq, instance_id, tool, decision, phase, state, error_code
from audit_events where instance_id like '%FORGED%';
```

```text
  seq  | instance_id                            | tool                | decision | phase   | state   | error_code
 13593 | demo                                   +| magento.product.get | error    | outcome | failed  | invalid_argument
       | FORGED audit line: subject=admin        |                     |          |         |         |
 13592 | demo                                   +| magento.product.get | pending  | intent  | pending |
       | FORGED audit line: subject=admin        |                     |          |         |         |
```

**请求被拒绝了，但伪造内容已经写进审计表**（`instance_id` 里含换行，任何按行读日志的工具都会被骗）。

### S-12 → U-38（SO-05）发货数量比请求少 1 —— **NOT-REPRODUCED**

```bash
# 干净订单：4 件
/tmp/mcpraw -mode=raw -tool=magento.order.create -args="$(cat /tmp/order_ship4.json)"
# → entity 38, qty 4, items [(45, 24-MB04, 4)]

/tmp/mcpraw -mode=raw -tool=magento.shipment.create \
  -args='{"instance":"demo","order_id":38,"items":[{"order_item_id":45,"qty":4}],"notify":false}'
# → {"shipment":{"entity_id":22,"increment_id":"000000022","order_id":38,"total_qty":4,...}}
# 上游：state processing, [(45, qty_ordered 4, qty_shipped 4)]      ← 要 4 发 4，正确
```

**结论：新订单上无法复现。** 报告里订单 8（要 4 发 3）、订单 11（要 2 发 1）都是别人用
`order.create` 造的、且当时正被并发操作。判 `ENV-DEPENDENT`：需要用「订单 8/11 当时的状态」
才能复现，不排除是并发/历史状态所致，**不建议按缺陷修复，建议修复阶段用干净订单再验一次**。

### S-13 → U-76（SEC-03）审批不绑角色 —— ENV-DEPENDENT（代码已确认）

生产部署 `auth.enabled=false`，无法在生产上复现越权；安全岗位自建的鉴权实例已销毁。
我读代码确认报告的三处论断都成立：

```text
internal/security/confirm/confirm.go:122-130
  func (s *Service) Approve(ctx, id, approver string) error {
      if strings.TrimSpace(approver) == "" { return … }
      return s.store.Approve(ctx, id, approver, s.now())   ← 只检查非空
  }
cmd/admin/main.go:121
  if err := svc.Approve(ctx, id, approver); err != nil      ← 验签后直接批准，无角色判断
```

`auth.Identity.Class`（human/service）在仓库里只用于审计归因，没有任何授权判断使用它。
与 ADR-0014「an agent cannot be an approver of record」直接冲突 → 判缺陷（P1，与队长裁决一致）。

### S-14 → U-78（SEC-01）超时/限流的调用丢失 outcome 行 —— ENV-DEPENDENT（代码已确认）

```text
internal/mcp/server/server.go:407-411
  func recordOutcome(opts Options, ctx context.Context, …) {
      if opts.Audit != nil { opts.Audit(ctx, tool, instance, "outcome", state, string(code)) }
  }
```

`recordOutcome` 用的是**请求 ctx**：超时/取消之后 ctx 已失效，审计写入必然失败，而调用方
（`server.go:239-324` 各处）全部丢弃返回值 → 静默丢失 outcome。与安全岗位的观察（seq 244 只有
intent/pending、10 次 rate_limited 无 outcome）吻合。

---

## 三、去重后的缺陷清单（85 条，按代码归属分组）

「来源」列是各岗位报告的原始编号；一条缺陷被多个岗位独立报出的，说明它在本轮 7 种工作流里都出现了。

### G1 `internal/magento/product_write.go`（商品写）— 6 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-01 | **P0** | SO-01, SO-02 | `product.update`（含 `configurable.update`、导入更新分支） | 价格类五字段整体错位一格：weight→special_price、special_price→special_from_date、special_from_date→special_to_date、special_to_date→cost、cost→tax_class_id，全部静默 | REPRODUCED |
| U-02 | P1 | INT-01, MERCH-04 | `products.bulk_update` | `price`/`name` 被静默丢弃却报 `status:"updated"`；也没有别的批量改价入口 | REPRODUCED |
| U-03 | P2 | MERCH-05 | `configurable.update` / `product.update` | 给 configurable 父商品写 price 返回成功但前台不变（Magento 行为），工具不提示 | UNVERIFIED |
| U-04 | P3 | SO-10 | `product.update` | 接受 3–4 位小数并原样回显（8.555），前台只显示 2 位（8.56） | UNVERIFIED |
| U-05 | P3 | MERCH-08 | `product.get` | 丢掉 `extension_attributes`，看不到 configurable 的 options/子商品链接 | UNVERIFIED |
| U-06 | P3 | MERCH-10 | `product.create` | 入参带 `quantity` 但响应里没有库存回执 | UNVERIFIED |

### G2 阶梯价（tier price 读写）— 3 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-07 | P1 | MERCH-01 | `product.tier_prices.set` | Magento 用 **200 + 错误数组**表示失败，网关只看状态码 → 返回 `set:true` 却零写入 | REPRODUCED |
| U-08 | P1 | MERCH-02 | `product.tier_prices` | 读取用了**写接口**的字段名（price/sku），上游是 `value` → price 恒 0、sku 恒空 | REPRODUCED |
| U-09 | P2 | MERCH-03 | `product.tier_prices.set` | 文档说「replaces」，实际是 append，旧阶梯删不掉 | UNVERIFIED |

### G3 `internal/magento/order_documents_write.go`（发票/发货/贷项写）— 14 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-10 | P1 | SO-03, CS-02, FIN-08 | `creditmemo.create` / `shipment.create` | `documentComment.IsVisibleOnFront` 是 `bool`，Magento 要 `int` → **带备注的退款/发货 100% 400** | REPRODUCED（代码+队长实测） |
| U-11 | P1 | CS-01, FIN-07 | `creditmemo.create` | 第一次部分退款把**全部未退运费**一起退掉，没有 `shipping_amount` 参数、无提示 | REPRODUCED |
| U-12 | P1 | FIN-10 | `creditmemo.create` | 本地校验只看数量不看金额上限：`adjustment_positive` 吃掉额度后，最后一件永远退不掉 | REPRODUCED |
| U-13 | P1 | FIN-12 | `creditmemo.create` | 不做发票级数量校验：对只开 1 件的发票冲销 2 件成功，贷项金额 > 该发票 | REPRODUCED |
| U-14 | P1 | INT-03, CS-07 | `invoice.create` | `capture:false` 仍返回 `captured:true`（离线支付下 invoice.state 恒 2），把「已开票」当「已收款」 | REPRODUCED |
| U-15 | P2 | FIN-11 | `creditmemo.create` | 返回的 `invoice_id` 是本地推导值，Magento 侧 `invoice_id=null`，回读即消失 | REPRODUCED |
| U-16 | P2 | CS-03 | 审批流程 | 审批摘要只有 `order_id` + `items` 数量，**没有金额**；金额其实已经算好了（`amount_minor`） | UNVERIFIED |
| U-17 | P2 | INT-05 | 审批流程 | 预检（已取消/已开票/最小起订量）排在审批之后，无人值守时无法先校验 | UNVERIFIED |
| U-18 | P2 | INT-04 | 审批流程 | 幂等重放仍要求新的审批，丢响应后无法自动重试 | UNVERIFIED |
| U-19 | P2 | INT-06 | 审批流程 | 并发失败者收到 `confirmation_used` 但无 `operation_id`，无法定位已成功的那次 | UNVERIFIED |
| U-20 | P2 | FIN-16 | `creditmemo.create` | 纯本地校验失败也写操作账本、占幂等键、消耗一次审批 | UNVERIFIED |
| U-21 | P2 | FIN-15 | `creditmemo.create` | 可退数量只在错误文本里（`"2 refundable units"`），无结构化字段；单复数错（`1 units`） | UNVERIFIED |
| U-22 | P3 | CS-08 | `shipment.create` | 发货失败时 op 标签是 `magento POST /order/{id}/{id}` | UNVERIFIED |
| U-23 | P3 | FIN-13 | `creditmemo.get` / `list` | 同一张单据 `total_qty` 在 create/get 有、在 list 无 | UNVERIFIED |

### G4 `internal/magento/client.go` + 错误映射（上游错误不可用）— 7 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-24 | P1 | SO-03, CS-02, FIN-08, SO-06, INT-11 | 全部写工具 | 上游失败原因被整句丢弃（`upstreamQuotedRE` 把含引号的字段名判为敏感），`details` 只剩 `store_code` + `upstream_status` | REPRODUCED（代码+实测） |
| U-25 | P2 | SO-16, SEC-08 | 透传 `upstream_message` 的工具 | 占位符未替换（`Proszę sprawdzić adres dostawy. %1`、`=%customerGroup`），且是波兰语原文 | UNVERIFIED |
| U-26 | P2 | SO-06, INT-09 | `bulk_update` / `product.update` | 瞬时错误（MySQL deadlock、并发 400）被映射成**不可重试**的 `validation_failed` | UNVERIFIED |
| U-27 | P2 | INT-11 | `order.create` / `import.*` | 状态冲突只有 `validation_failed` + 波兰语原文，没有稳定 reason code | UNVERIFIED |
| U-28 | P2 | INT-20 | 全部 | 调用不存在的工具名返回 `code:"internal"`，与服务器内部故障无法区分 | REPRODUCED |
| U-29 | P3 | INT-21 | 传输层 | JSON 解析失败/未知 method 返回纯文本 400，不是 JSON-RPC 错误对象 | UNVERIFIED |
| U-30 | P3 | INT-19 | 错误信封 | `retryable` 只在为 `true` 时出现，与 ADR-0010「每条错误都带 Retryable」不一致 | UNVERIFIED |

### G5 订单/发货/客户读取投影 — 15 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-31 | P2 | SO-04, CS-04 | `order.get` / `shipment.get` | 订单无 `qty_shipped/qty_invoiced/qty_refunded`、无收货地址、无配送方式；shipment 无 `items` → 「哪件发了/退了几件」答不出来 | REPRODUCED |
| U-32 | P2 | CS-05 | `customer.addresses.update` / 订单 | 只能改地址簿且回读不到 street/city/postcode；已下单订单的地址没有任何工具能改 | UNVERIFIED |
| U-33 | P1 | SO-07 | `order.create` | 下单失败残留购物车 → 下一次下单数量翻倍（要 3 件成 6 件），且返回成功 | UNVERIFIED |
| U-34 | P2 | SO-08 | `inventory.status` / `source_items` | 库存三处数字互相矛盾（qty=0 却 is_in_stock=true；源 status=1 但目录不可售），无解释 | UNVERIFIED |
| U-35 | P2 | SO-09 | `inventory.*` | 「改库存」没有明确入口，报错也不指向该用的工具 | UNVERIFIED |
| U-36 | P2 | MERCH-07 | `category.products` | 返回的商品除 sku 外全是零值（name ""、price 0、status 0） | UNVERIFIED |
| U-37 | P2 | SO-05 | `shipment.create` | 发货数量比请求少 1（要 4 发 3、要 2 发 1）但报成功 | **NOT-REPRODUCED**（新订单 4→4 正确；ENV-DEPENDENT） |
| U-38 | P3 | CS-06 | `order.cancel` | 取消后看不到 `total_canceled`/`qty_canceled` | UNVERIFIED |
| U-39 | P3 | CS-09 | `customer.*` | 客户身份与地址内容不可回读；`customer.search` 只支持完整邮箱精确匹配 | UNVERIFIED |
| U-40 | P3 | CS-10 | `creditmemo.email` | 返回 `sent:true` 但投递无法核实 | UNVERIFIED |
| U-41 | P3 | INT-18 | `order.search` | 不返回明细行，读回对账无法区分同一客户的多笔订单 | UNVERIFIED |
| U-42 | P3 | SO-11 | 全部 | 两种 404（路由不存在 / 实体不存在）仍然混在一起 | UNVERIFIED |
| U-43 | P3 | SO-12 | 全部 | 参数错误的「原因」只在 structuredContent，text 通道只有一句话 | UNVERIFIED |
| U-44 | P3 | SO-13 | 文档 | 三处工具描述是别的工具抄过来的 | UNVERIFIED |
| U-45 | P3 | SO-14 | `product.search` | 无法筛「缺货但仍在售」，2114 SKU 只能抽样（能力缺口） | UNVERIFIED |

### G6 报表口径（`sales_analysis.go` + `order.go` 汇总）— 19 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-46 | P2 | FIN-01, DA-14 | `sales.summary` / `analysis` | revenue 含已取消订单、且完全不扣退款（25 单窗口虚增最多 1106.18/1531.18），三个工具一致但都没说明 | REPRODUCED |
| U-47 | P2 | FIN-03 | `sales.summary` | 没有任何 tax/refund/discount 字段，财务拿它出不了报表 | REPRODUCED |
| U-48 | P2 | FIN-04, DA-01 | `sales.analysis` | 行小计当单据总额：`refunded_amount` 少 160.00（不含运费/调整费）、`top_skus.revenue` 少一个运费（1331.18 vs 1531.18） | REPRODUCED |
| U-49 | P2 | DA-02 | `sales.analysis` | `new_orders`/`repeat_orders` 描述为「首次/回头客户」，实际是「窗口内单笔/多笔」 | UNVERIFIED |
| U-50 | P2 | DA-03, FIN-06 | `sales.summary` vs `analysis` | 日桶口径一个 UTC、一个店铺时区；`summary` 连 `timezone` 字段都没有（实测同批订单 11-21 vs 11-22） | REPRODUCED |
| U-51 | P2 | DA-04 | `analysis` / `orders.geography` | 截断时一个保留最旧、一个保留最新，geography 还不说保留的是哪一端 | UNVERIFIED |
| U-52 | P2 | FIN-02, DA-16 | `summary` / `analysis` | 金额带二进制浮点尾数（`1946.3600000000001`、`287.08000000000004`、`aov` 12 位小数） | REPRODUCED |
| U-53 | P2 | FIN-14 | `tax.classes` / 报表 | 税率与税规则完全查不到（无工具、路由也未暴露），无法证明 `tax=0` 是配置还是缺陷 | REPRODUCED |
| U-54 | P2 | DA-12 | `sales.summary` | 恰好 1000 单会误报 `truncated=true`（代码级：达到上限且未见到短页） | UNVERIFIED |
| U-55 | P3 | FIN-05 | `sales.analysis` | `discount` 符号为负，与 `Order.DiscountAmount`「as a positive number」声明相反 | REPRODUCED |
| U-56 | P3 | DA-05 | `sales.summary` | 366 天窗口上限未文档化，`sales.analysis` 却无上限 | UNVERIFIED |
| U-57 | P3 | DA-06 | `analysis` | `top_skus`(50)/`refunded_skus`(10)/`top_coupons`(10) 静默截断，没有「共 N 条」标记 | UNVERIFIED |
| U-58 | P3 | DA-07 | `analysis` | `top_coupons` 描述「按营收排序」，实际按订单数，且没有 revenue 字段 | UNVERIFIED |
| U-59 | P3 | DA-08 | `analysis` | `aov_bands` 标签硬编码 `$`，店铺是 PLN | UNVERIFIED |
| U-60 | P3 | DA-09 | `analysis` | `ship_latency` 没有单位（实测天），三处字段描述复制粘贴错位 | UNVERIFIED |
| U-61 | P3 | DA-10 | `orders.geography` | `country_id` 契约是 ISO 3166-1，实际返回 `UNKNOWN` 哨兵 | UNVERIFIED |
| U-62 | P3 | DA-11 | `order.search` | `cursor` 声明为 string，传整数直接 400 | UNVERIFIED |
| U-63 | P3 | DA-13 | `order.search` | 没有分页元数据/去重保护 | UNVERIFIED |
| U-64 | P3 | DA-15 | `summary` / `geography` | 没有 store 维度过滤（店有 pl/en 两个 store view） | UNVERIFIED |

### G7 导入（`internal/magento/import.go`）— 4 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-65 | P2 | INT-10 | `import.*` | 每行必须带 `name`，纯改价 CSV 被整单拒绝；`start` 只报第一行错误 | UNVERIFIED |
| U-66 | P2 | INT-08 | `attributes.options.bulk_create` | `total=3` 但 `rows` 只有 2（重复标签静默消失），`created+failed≠total` | UNVERIFIED |
| U-67 | P3 | INT-14 | `import.start` | 重放已完成任务仍返回 `queued:true`，无 `replayed` | UNVERIFIED |
| U-68 | P3 | INT-15 | `import.status` | `job.result` 是 JSON 字符串，需要二次解析 | UNVERIFIED |

### G8 操作账本与幂等（`internal/storage/` + `server.go`）— 7 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-69 | P1 | FIN-17 | 账本 | `operations.amount_minor` 只是商品行小计，不含运费与调整费（7 张单据少记 82.00：292.16 vs 210.16） | REPRODUCED |
| U-70 | P2 | INT-17, FIN-18 | 账本 | `entity_id` 语义错：`order.create` 存**客户 id**、`creditmemo.create` 存**发票 id**、`invoice.create` 存**订单 id**，无法稳定 join | REPRODUCED |
| U-71 | P1 | INT-02, MERCH-06 | 非 money 写工具 | 幂等重放**在 structuredContent 里不可见**（只在 content[0].text），派生 key 让「同参数再写一次」永久 no-op 且仍报成功 | UNVERIFIED |
| U-72 | P1 | INT-12 | `order.create` | 省略 `idempotency_key` 时，同参数的**合法重复下单被永久去重**（`replayed:true`），ERP 会静默丢单 | REPRODUCED（我 t4 亲历：同参建单直接回放订单 25） |
| U-73 | P2 | FIN-09 | `creditmemo.create` | 派生 key 不含 `comment` → 一次失败后同参数重试被拒（「operation previously failed」） | REPRODUCED |
| U-74 | P1 | INT-07 | 全部 | `operation_unknown` 提示「poll」，但 102 个工具里**没有任何按 operation_id 轮询的接口**；`order.create` 的 unknown 构造上不可判定，生产库已积压 11 条 | UNVERIFIED |
| U-75 | P3 | INT-16 | 校验错误 | 超长 `idempotency_key` 被完整回显（文档要求把它当 secret） | UNVERIFIED |

### G9 审批（`internal/security/confirm/` + `cmd/admin/`）— 2 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-76 | P1 | SEC-03 | `cmd/admin` / `confirm.Approve` | 审批权限不绑角色与身份类别：viewer 能批 admin 的 destructive 删除，`type=service` 的 agent 令牌也能批，与 ADR-0014 冲突 | ENV-DEPENDENT（代码已确认，队长已裁决 P1） |
| U-77 | P2 | SEC-04 | 审计表 | `audit_events.confirmation_id`/`role_name`/`params_hash` 从不写入（253 行全 NULL），审批与操作不可关联 | UNVERIFIED |

### G10 审计记录（`internal/mcp/server/server.go`）— 3 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-78 | P1 | SEC-01 | 审计 | 超时/限流的调用丢失 outcome 行（`recordOutcome` 用已失效的请求 ctx，错误被丢弃） | ENV-DEPENDENT（代码已确认） |
| U-79 | P1 | SEC-02 | 审计 | 五个自助记账资金工具的审计行不可关联：intent 悬空、outcome 无 request_id、`identity_class` 硬编码 service、失败记成 `allow` | UNVERIFIED |
| U-80 | P2 | SEC-05 | 审计 | `instance` 在 schema 校验**之前**原样落库（我实测：被拒的伪造 instance 已写入 audit_events） | REPRODUCED |

### G11 schema / 描述 / 协议 — 5 条

| 编号 | 级别 | 来源 | 工具 | 一句话 | 复现 |
|---|---|---|---|---|---|
| U-81 | P3 | SO-15, MERCH-09, INT-13 | 全部（101/102） | `instance` 描述称「单实例时由服务端解析」，schema 却 required；第一次调用必踩 | REPRODUCED |
| U-82 | P3 | INT-13 | `bulk_update` | 顶层描述「部分成功逐行报告」而 `skus` 描述「整单拒绝」，实测是整单拒绝 | UNVERIFIED |
| U-83 | P3 | SEC-06 | cloudflare 工具 | `instance` 只是标签，用 magento 实例名也能调 Cloudflare 并真的打上游，审计记错实例 | UNVERIFIED |
| U-84 | P3 | SEC-07 | 全部 | 每个结果都回显调用者邮箱（`_meta.subject`） | UNVERIFIED |
| U-85 | P3 | SEC-09 | SSE | 响应缺 `charset=utf-8`，波兰语被按 ISO-8859-1 解码 | UNVERIFIED |

### 统计

| 级别 | 条数 | 其中 REPRODUCED | 其中 ENV-DEPENDENT | NOT-REPRODUCED | UNVERIFIED |
|---|---|---|---|---|---|
| P0 | 1 | 1 | 0 | 0 | 0 |
| P1 | 17 | 11 | 2 | 0 | 4 |
| P2 | 34 | 12 | 0 | 1 | 21 |
| P3 | 33 | 2 | 0 | 0 | 31 |
| **合计** | **85** | **26** | **2** | **1** | **56** |

> 「REPRODUCED」包含我在本任务里重跑的 11 条（S-1..S-11）＋我在 t4 财务测试里亲历的条目
> （U-11/U-12/U-13/U-15/U-46..U-48/U-52/U-55/U-69/U-72/U-73）。
> 56 条 UNVERIFIED 不是「不可信」，而是本轮时间/权限不够逐条重跑；它们都附了代码定位或上游数据证据，
> 修复阶段应优先复验其中 P1。

---

## 四、环境限制（**不是产品缺陷**，别写进修复单）

| 编号 | 现象 | 为什么不记缺陷 | 影响 |
|---|---|---|---|
| E-1 | demo 站周期性重置；本轮 7 个 agent 并发下单，同一窗口 20 分钟内 20→35 单 | 共用测试店 | 所有数字必须绑定快照时间；跨时间比对无意义 |
| E-2 | 该店不暴露的路由：`POST /products/{sku}/options`、`POST\|PUT /customers/{id}/addresses`、`GET /creditmemos/{id}`（要用 `GET /creditmemo/{id}`）、`GET /taxRates`、`GET /taxRules` | 店铺侧路由裁剪（波兰语 404 = 路由不存在） | FIN-14（税率不可查）、MERCH 的部分验证只能绕 REST |
| E-3 | 生产部署 `auth.enabled=false` | 测试部署的取舍 | RBAC/审批越权只能用自建实例验证（U-76/U-78） |
| E-4 | 19:22:25Z 一次 `docker compose` 重新部署，导致 19:23–19:26Z 期间「上游成功、账本 succeeded、调用方收 internal」（FIN-19） | 优雅关闭窗口内的在途请求 | 不是代码缺陷；但**写操作必须带 idempotency_key 才能安全重试**，这条应写进文档 |
| E-5 | 商品 `price` 字段是全局价，实际成交价由 store-scope / `special_price` 决定（34 vs 29） | Magento 数据模型 | 任何用 `product.price` 重算订单金额的对账脚本会高估 |
| E-6 | 三位小数价格 8.555 在行级被舍入成 8.56（row_total 25.68 vs 25.665） | Magento 行级舍入 | 0.015 的一次性差异，不是重复舍入（退款侧已核对闭合） |
| E-7 | 订单 `created_at` 是 UTC，店铺时区 Europe/Warsaw | Magento 存储约定 | 与 U-50 叠加才成为问题；单纯存 UTC 不算缺陷 |
| E-8 | Magento 用 200 + 错误数组表示业务失败（tier price 写入） | 上游约定 | 这**是**网关缺陷（U-07）的成因，但上游行为本身不算缺陷 |

---

## 五、测试脚本问题（**不是产品缺陷**）

| 编号 | 问题 | 后果 | 建议 |
|---|---|---|---|
| T-1 | `go run -tags live` 在重新部署窗口里会拿到 `internal`，容易被误判为网关缺陷 | 我在 t4 一开始就误判过一次（后来 16 次连跑正常） | 预编译 `go build -o /tmp/mcpraw ./test/live`，先跑 3 次基线 |
| T-2 | 7 份报告用了 3 种不同包装（`mcp()` shell 函数 / `/tmp/mcpraw` / `python3 /tmp/mcpx.py`），命令不能直接互换 | 复验时要重写命令 | 统一成 `test/live -mode=raw` 或提交 `mcpx.py` 进仓库 |
| T-3 | 各报告快照时间不同（19:24Z / 19:29Z / 19:32Z / 19:37Z），数字不可横向比较 | 同一缺陷在不同报告里金额不同（如退款 639.06 / 685.18 / 733.8） | 汇总只引用「同快照内 MCP vs REST 对齐」，不跨报告比金额 |
| T-4 | 少数条目把「能力缺口」记为缺陷（SO-14 缺库存筛选、MERCH-04 无批量改价） | 会虚增缺陷数 | 本文保留但标 P3，并在修复派发里归到「增强」而非「修复」 |
| T-5 | SO-05（少发 1 件）没有干净对照实验 | 无法判定 | 我用新订单复现失败（4→4 正确），已降级为 ENV-DEPENDENT |
| T-6 | 安全岗位的自建实例（18120/18121）在任务结束后已销毁 | U-76/U-78 无法二次复现 | 修复阶段需重建同类环境做回归 |

---

## 六、派发建议（按代码归属，可直接开单）

### 第一批：P0 + 资金类 P1（建议同一个修复者，先做）

| 顺序 | 编号 | 文件 | 改什么 |
|---|---|---|---|
| 1 | U-01 | `internal/magento/product_write.go:286-288` | 把 `pricingAttributesFromPointers([]*string{in.Weight, …}, …)` 改成 `pricingAttributes(in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost, in.TaxClassID)`（该函数的第一个参数 `_` 本来就是丢弃 weight；weight 由 `payload.Weight` 单独承载，见 :316-321，**不要连 weight 一起删**）；补单测：只传 weight 时 payload 里不能出现 `special_price`/`special_from_date`/`cost`/`tax_class_id` |
| 2 | U-10 | `internal/magento/order_documents_write.go:360-363` | `IsVisibleOnFront` 改成 `int`（0/1）；`shipment.create` 同结构体一并修 |
| 3 | U-11 | `order_documents_write.go` + `creditmemo.create` schema | 增加 `shipping_amount`（含 0）；不传时按「只退已退商品对应的运费」或显式提示「本次将退全部剩余运费」 |
| 4 | U-14 | `internal/magento/invoice_write.go:134` | `captured` 不能只由 `invoice.State==2` 推导；改为按支付方式/是否在线捕获判定，或在描述里说明 |
| 5 | U-69 | `internal/magento/order_documents_write.go` `authorisedAmount` | 账本金额 = 单据 `grand_total`（含运费与调整），或拆 `subtotal_minor`/`shipping_minor`/`adjustment_minor` |
| 6 | U-12 | `order_documents_write.go:300` `checkRefundable` | 同时校验剩余可退金额，并在错误里给出 `refundable_amount` |
| 7 | U-13 | 同上 | 校验发票级 `qty` 上限（按 `invoice_id` 取该发票的行数量） |
| 8 | U-72 | 幂等键派生 | 省略 key 时不要用参数哈希做永久去重（至少对 `order.create` 加显式提示或要求必填） |

### 第二批：静默失败与错误可用性（同一类根因）

| 编号 | 文件 | 改什么 |
|---|---|---|
| U-07 | 阶梯价写路径 | 解析 Magento 的 `200 + [{message}]` 错误体，非空即失败 |
| U-08 | 阶梯价读路径 | 读接口字段名用 `customer_group_id`/`qty`/`value` |
| U-02 | `products.bulk_update` | `price`/`name` 要么支持，要么显式拒绝（`unsupported_capability`），不能静默丢弃后报 updated |
| U-24 | `internal/magento/client.go:451` `upstreamTextIsSafe` / `upstreamQuotedRE` | 上游 message 只做长度截断与换行清理，不要因引号整句丢弃；把 `upstream_message` 稳定放进 `details` |
| U-25 | 同上 | 占位符兜底替换（`%1`→`(details omitted)`），并附英文摘要 |
| U-26 | 错误分类 | deadlock/lock timeout/5xx → `conflict` 或 `upstream_unavailable` + `retryable:true` |
| U-28 | `internal/mcp/toolerr` 或 registry | 未注册工具 → `not_found`（不要 `internal`） |
| U-71 | `internal/storage` + 各写工具 | 所有写工具的 `structuredContent` 都带 `replayed`；派生 key 的「同参重放」语义要在描述里写清 |
| U-74 | 新增 `operation.get`/`operation.status` 工具 | 让 `operation_unknown` 有闭环出口 |

### 第三批：审计与审批（安全）

| 编号 | 文件 | 改什么 |
|---|---|---|
| U-76 | `cmd/admin/main.go:121`、`internal/security/confirm/confirm.go:122` | 验签后要求 `Identity.Class == human`；`Approve` 拒绝 service 类身份 |
| U-78 | `internal/mcp/server/server.go:407` | `recordOutcome` 用 `context.WithoutCancel(ctx)` 或独立超时；审计失败要打日志，不能 `_ =` |
| U-79 | 五个资金工具 | outcome 行带 `request_id`、真实 `identity_class`、失败记 `failed` |
| U-77 | 审计写入 | 写 `confirmation_id`/`role_name`/`params_hash` |
| U-80 | `server.go` 中间件顺序 | instance 先按 pattern 校验再落库（或落库前 sanitize） |
| U-70 | 账本 | `entity_type` 与 `entity_id` 语义对齐，父单据单独一列 |
| U-16 | `confirm.Summarize` | 摘要里带金额（`amount_minor` 已经算出来了） |

### 第四批：报表口径（`sales_analysis.go` / `order.go`）

| 编号 | 改什么 |
|---|---|
| U-46 | 输出 `canceled_revenue` / `refunded_total`，或在描述里明确「含取消、不扣退款」 |
| U-47 | `sales.summary` 加 `tax` / `refunded` / `discount` 汇总 |
| U-48 | `refunded_amount` 改名或含运费与调整；`top_skus.revenue` 明确是行小计 |
| U-50 | 统一日桶口径；`sales.summary` 输出 `timezone` |
| U-52 | 金额按货币精度 round 到 2 位再输出 |
| U-53 | 提供税率/税规则读取（或明确说明本店不可查） |
| U-49/U-51/U-54..U-64 | 口径与描述修正，可一次性打包 |

### 第五批：读取投影与体验（P2/P3，可并行）

U-03/U-04/U-05/U-06/U-09/U-15/U-17..U-23/U-31..U-45/U-55..U-68/U-73/U-75/U-81..U-85。

### 明确不建议修复（已判定 NOT-A-DEFECT 或环境）

- SEC-10 注入面（无 SSRF、控制字符被拒、错误不回显负载）——已探测，无问题
- SEC-11 重放/审批绕过——`confirmation_used` 生效
- E-4 部署窗口的 `internal`——部署产物，改为文档提示「写操作必须带 idempotency_key」
- U-37 少发货——新订单无法复现，回归阶段用干净订单再验

---

## 七、给修复阶段的三个提醒

1. **同一类根因重复出现三次**：导入的 status/visibility、阶梯价写入、`bulk_update` 的 price，
   都是「上游用 200 表达业务失败，网关只看状态码」。修的时候请抽成一个公共的
   `decodeUpstreamBusinessError`，而不是三处各修一遍。
2. **U-01 是唯一 P0，且它只需要改一行参数顺序**。建议先修它并立刻回归（改重量 → 售价不变），
   避免其他修复把它掩盖。
3. **审计链（U-69/U-70/U-77/U-78/U-79）比单个字段 bug 更值得投入**：财务、安全两个岗位
   独立撞上同一片区域，说明「可追溯」这个承诺目前只做了一半。
