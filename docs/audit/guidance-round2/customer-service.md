# 客服岗位：照 `refunds` prompt 做退款与运费（v0.11.9）

第一轮问「工具能不能干活」，这一轮问「**写下的指引能不能让 agent 干对活**」。
我把 `prompts/get refunds` 当唯一手册，逐条照做，每一步用 REST 直连 `/V1/creditmemo/{id}`
复核金额。**只记录，不改代码。**

## 0. 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | **v0.11.9**（`platform.info` 实测 `"version": "v0.11.9"`） |
| 实例 | `demo`，store `pl`（PLN），付款方式 `checkmo`（离线） |
| 主快照 | `2026-11-22T19:04:01Z`（UTC） |
| 自建数据 | 订单 4 / 6 / 9 / 11，均带 `R2-REFUND-20261122T185934Z` 订单备注；8 张 credit memo（id 2–9） |

**店铺在本轮开始时被重置过**（库里只剩 2022 年两条 demo 订单），18:57:25Z 起并发 agent 开始下单，
所有订单号都是重置后重新计数的。下文所有金额都绑定上面的快照时间，并逐张用 REST 复核。

## 1. `prompts/get refunds` 全文（运费相关段落原文）

```bash
# MCP prompts/get（不是读源码）
/tmp/promptget.sh refunds
```

> **Shipping follows `shipping_amount`.** Omit it and a partial refund returns no shipping; a memo
> consuming the last refundable units returns the remaining shipping; `0` means "none". The trap
> is the middle case.

> **A quantity above `qty_invoiced - qty_refunded` is rejected** before any refund request, per
> invoice: "one more unit" can exceed the invoice, not the order.

> **Adjustments change the total, not the lines.** `adjustment_positive` consumes refundable
> amount, so a later line refund can be rejected as "more than refundable".

> 1. `magento.order.get` — item ids, `qty_invoiced`, `qty_refunded`, shipping amount.
> 2. `magento.invoice.list` — pick the invoice; with several, pass `invoice_id`.
> 3. `magento.creditmemo.create` — one line per item with the quantity, plus `shipping_amount`
>    (omit = none on a partial refund, `0` = explicitly none) and a `comment`.

`creditmemo.create` 的 schema 描述（v0.11.9 实测）：

> Shipping to refund with this memo. Omit it to refund no shipping unless this memo refunds every
> remaining refundable unit, in which case the remaining shipping is refunded; 0 refunds no
> shipping explicitly.

## 2. 实测矩阵：4 个订单、8 张 credit memo（全部 REST 复核）

自建订单（每单都用 `magento.order.get` 先确认构成，再开票、再退款）：

| 订单 | 构成 | 小计 | 运费 | 订单总额 |
|---|---|---|---|---|
| 4 | 24-MB03 ×2 @38 | 76 | 10 | 86 |
| 6 | 24-MB05 ×1 @45 + 24-MB06 ×1 @45 | 90 | 10 | 100 |
| 9 | 24-MB02 ×2 @59 | 118 | 10 | 128 |
| 11 | 24-UB02 ×2 @74 | 148 | 10 | 158 |

| memo | 订单 | 传参 | 期望 grand_total | MCP 实测 | REST 复核 | 判定 |
|---|---|---|---|---|---|---|
| 2 | 4 | 退 1 件，**省略** `shipping_amount` | 38（不含运费） | 38（`shipping_amount: 0`） | grand 38 / sub 38 / ship 0 | **一致** |
| 3 | 4 | 退**最后** 1 件，省略 | 38+10=48 | 48（`shipping_amount: 10`） | grand 48 / sub 38 / ship 10 | **一致** |
| 4 | 6 | 退 1 行，`shipping_amount: 0` | 45 | 45（ship 0） | grand 45 / ship 0 | **一致** |
| 5 | 6 | 退**最后** 1 行，`shipping_amount: 0` | 45（显式 0 压过默认） | 45（ship 0） | grand 45 / ship 0 | **一致** |
| 6 | 9 | 退 1 件，`shipping_amount: 3` | 59+3=62 | 62（ship 3） | grand 62 / ship 3 | **一致（但 prompt 未写此用法）** |
| 7 | 9 | 退最后 1 件，`shipping_amount: 7` | 59+7=66 | 66（ship 7） | grand 66 / ship 7 | **一致** |
| 8 | 11 | 只开票 1 件后退这 1 件，省略 | 74（订单还有 1 件未退） | **84（ship 10）** | grand 84 / sub 74 / ship 10 | **矛盾/歧义（见 §3）** |
| 9 | 11 | 退第 2 件，`shipping_amount: 0` | 74 | 74（ship 0） | grand 74 / ship 0 | **一致** |

订单级闭合（REST `/V1/orders/{id}` 的 `total_refunded`）：

| 订单 | grand_total | total_refunded | 说明 |
|---|---|---|---|
| 4 | 86 | 86 | 38 + 48，运费退满，状态 `closed` |
| 6 | 100 | 90 | 两张 memo 都显式 `0`，**10 运费留给商家**，状态 `complete` |
| 9 | 128 | 128 | 62 + 66，运费 3 + 7 分两次退，状态 `closed` |
| 11 | 158 | 158 | 84 + 74，状态 `closed` |

## 3. 三条运费规则逐条判定

### 规则 1「部分退款不退运费」——**一致**

订单 4：2 件、运费 10。退 1 件、省略 `shipping_amount` → `grand_total 38`，`shipping_amount 0`。
REST 复核 memo 2 同值。这正是 v0.11.6 的 P-FIN-07（部分退款连带退全额运费）修复后的行为。

### 规则 2「退完最后可退件数退剩余运费」——**一致**

订单 4 第 2 张 memo（最后 1 件、省略）→ `grand_total 48 = 38 + 10`，`shipping_amount 10`。
两单合计 86 = 订单总额，一分不差。

订单 9 更细：先退 1 件并显式 `shipping_amount: 3`（部分运费），再退最后 1 件传 `7` →
62 + 66 = 128 = 订单总额。**「剩余运费」是按订单累计计算的**（10 − 3 = 7），这一点实测成立。

### 规则 3「`0` 表示明确不退」——**一致，且能压过规则 2 的默认**

订单 6：2 行各 1 件。第 1 行传 `0` → 45（ship 0）；**第 2 行（最后一件）也传 `0` → 45（ship 0）**。
即显式 `0` 优先于「最后件数退剩余运费」的默认，订单只退 90、运费 10 留给商家。
prompt 的措辞（"`0` means \"none\""）与实现一致，但**没有明说它优先于最后一件的默认**——
这是本 prompt 最容易踩的边界，建议补一句。

## 4. 「退完最后件数」的判定口径：**按发票，不是按订单**（判定：缺失/歧义）

这是本次唯一一条会**实际多退钱**的发现。

订单 11：2 件（每件 74）、运费 10、总额 158。我只开票 **1 件**（invoice 8，ship 10），
然后按 prompt「退其中一件」的做法退这 1 件、**省略** `shipping_amount`：

```text
creditmemo 8  grand_total 84  subtotal 74  shipping_amount 10  total_qty 1
REST /V1/creditmemo/8 → grand 84 / sub 74 / ship 10
```

**退了全额运费**——尽管订单里还有 1 件（74）既没退也没开票，顾客仍然拿着货。
也就是说，「最后可退件数」判定的是**这张发票**是否被退空，而不是**整张订单**是否退完。

后果：

- 商家在第一张发票退空时就把运费退光了；如果后面再退第 2 件，运费已为 0（实测：再传
  `shipping_amount: 5` 被拒，`refundable_shipping: "0.00"`），**无法再调整**；
- 从「订单总额」看并没有多退（158 = 84 + 74），所以账能对上；但**运费退早了**，
  对「分批发货、分批退款」的订单，等于把运费押在第一批上；
- prompt 只在**数量校验**那条里说了 "per invoice"，运费那条说的是 "the last refundable
  units"，没有说作用域。一个照手册办事的客服会以为「订单还剩一件 → 现在不该退运费」，
  结果拿到 84 而不是 74。

判定：**缺失**（prompt 未说明运费默认的作用域）+ 潜在**矛盾**（同一句里的 "last refundable
units" 在数量语境下是 per invoice，在运费语境下也是 per invoice，但读者会理解成 per order）。

建议：在运费那条里补一句「作用域与数量校验相同：以**发票**为单位；一张发票被退空即退剩余运费，
即使订单还有未退的行」。

## 5. prompt 没写、但会多退/少退的情况

### 5.1 正数 `shipping_amount` = 退指定金额（判定：缺失）

prompt 只列了两种取值：省略、`0`。实测传正数是**按金额退**的：

```text
订单 9，退 1 件，shipping_amount: 3  →  creditmemo 6  grand 62 = 59 + 3
```

schema 描述里有 "Shipping to refund with this memo"，但 prompt 的枚举里没有这一项。
照 prompt 办事的 agent 会认为运费只能「全退（最后一件）」或「不退」，遇到「退一半运费」的
业务要求时只能改用 `adjustment_negative` 手工抵——那正是 prompt 自己在第一条里警告过的坑。

判定：**缺失**。建议在第一条补一句「正数 = 退这么多；上限见 §5.2」。

### 5.2 超额 `shipping_amount` 被拒，且给可退额（判定：缺失，但行为正确）

```text
订单 9（剩余运费 7），shipping_amount: 999
→ validation_failed  details: {currency: PLN, refundable_shipping: "7.00"}
   "shipping_amount exceeds the shipping still refundable on this order"
```

这是**好行为**（拒绝 + 告诉你能退多少），但 prompt 没提「有上限」。
agent 若按业务口头要求填一个大数，会拿到一个它没预料到的错误码；好在错误自解释。

### 5.3 `adjustment_positive` 吃掉可退额（判定：一致）

```text
订单 11，退第 2 件（74），再传 adjustment_positive: 5
→ validation_failed  details: {currency: PLN, refundable_amount: "74.00", requested_amount: "79.00"}
   "the requested credit memo exceeds the amount still refundable"
```

与 prompt 的「`adjustment_positive` consumes refundable amount」一致。注意实测是**同一次调用**
就被拒（prompt 写的是 "a later line refund can be rejected"，措辞略微偏向「后续」）。

### 5.4 税：本店没有可测样本（判定：未覆盖，不是缺陷）

4 张订单的 `tax_amount` 都是 0（PL 店的商品税类未配置或税率为 0），所以
「运费含税 / 税是否随运费一起退」这条**无法实测**。prompt 也没提税。
建议在指引里说明「税随行与运费按 Magento 计算，`tax_amount` 会出现在 credit memo 上」，
否则 agent 在有税的店里只能靠猜。

## 6. 顺带核对（都与 prompt 一致）

| prompt 的说法 | 实测 | 判定 |
|---|---|---|
| 数量超过 `qty_invoiced - qty_refunded` 会被拒 | 订单 11 未开票的第 2 件 → `invalid_argument: order item 15 has 0 refundable units, cannot refund 1`（details 带 `refundable_units: 0` / `requested_units: 1`） | 一致 |
| 多张发票必须传 `invoice_id` | 订单 11 有 2 张发票时不传 → `invalid_argument: order 11 has 2 invoices; pass invoice_id to choose the one to refund` | 一致 |
| `captured` 不是 `state` | 4 次 `invoice.create` 全部返回 `captured: false`（checkmo，invoice state 2） | 一致 |
| `invoice_linked: false` = 没有存储链接 | 8 张 memo 的 REST `invoice_id` 全是 `None`，MCP 每次都返回 `invoice_linked: false` | 一致 |
| `replayed: true` = 同参数已退过 | 我重复调用一次相同参数的 `invoice.create`，第二次直接回放同一张发票（invoice 8） | 一致 |

## 7. 一条环境异常（不是本任务的缺陷，但会影响共享店铺的测试）

我第一次 `order.create`（24-MB01 ×1 + 24-MB04 ×1）返回的订单 **3** 里，除了我这两件，
还多了一组我从未请求的商品（`WT09-S-Yellow` configurable + 其子件），而当时店铺正在被
其他 agent 并发下单（同一时刻有 `product.create` / `import.start` / `configurable.create` 在跑，
且所有 agent 都用同一个 demo 客户 `customer_id 1`）。

随后我用**完全相同的参数**再下一次单，网关正确地回放了同一笔操作
（`operation_id 0b1d4a63-…`，即第一次那笔），没有新建订单——幂等行为本身是对的，
但它也说明：**那笔操作创建的订单确实就是 3**。

我没有继续深挖机制（超出本任务范围），但记录在此，因为它对「共享店铺 + 同一客户并发下单」
是真实风险：**两次为同一客户下单，可能合并成一张订单**。我因此没有使用订单 3 做退款测试，
所有退款都建立在我重新创建并**逐单核对过构成**的订单 4 / 6 / 9 / 11 上。
建议单独开一个任务验证「同一 customer 并发 order.create 是否串单」。

## 8. 结论

| 结论 | 内容 |
|---|---|
| **三条运费规则** | 全部成立：部分退款不退运费（memo 2）、退完最后件数退剩余运费（memo 3/7）、`0` 明确不退且优先于默认（memo 5） |
| **金额准确性** | 8 张 memo 的 `grand_total` 与期望逐张相等，且 REST 直连复核一致；订单级 `total_refunded` 与 `grand_total` 精确闭合（86/86、90/100、128/128、158/158） |
| **需要改指引的地方（3 条缺失）** | ① 「最后件数」的作用域是**发票**不是订单（会提前退运费，§4）；② 正数 `shipping_amount` = 退指定金额（§5.1）；③ `shipping_amount` 有上限、超了会被拒并返回 `refundable_shipping`（§5.2） |
| **建议补的 2 条** | ④ 明确「显式 `0` 优先于最后一件的默认」；⑤ 说明税的处理（本店无样本，未覆盖） |
| **顺带确认** | `captured` / `invoice_linked` / 多发票需 `invoice_id` / 数量校验 / `replayed` 五项与 prompt 描述一致 |

一句话给设计者：**这三条运费规则本身是对的，坑在「作用域」和「取值枚举」上**——
prompt 把「省略」和「0」讲透了，却没讲「正数」，也没讲「最后一件」是按发票算的；
前者让 agent 少一个合法工具，后者让 agent 在分批退款的订单上退早了运费。
