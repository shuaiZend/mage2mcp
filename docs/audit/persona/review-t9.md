# 独立审查：t9 的 22 条修复（v0.11.7）

审查对象：t9 交付的 22 条缺陷修复（U-01 / 07 / 08 / 10 / 11 / 12 / 13 / 14 / 15 / 21 / 22 / 24 / 25 / 26 / 27 / 28 / 30 / 31 / 36 / 69 / 76 / 78），
已随 v0.11.7 发布并在生产验证过。审查方式：**只读**——读代码、读测试、在独立 worktree 里做
「回退实验」、并对生产 v0.11.7 做真实调用。未修改被审代码。

快照：`git describe` = `v0.11.7-2-ge214ee7`（HEAD `e214ee7`）。回退实验在
`git worktree add /tmp/review-t9 HEAD` 的副本里做，做完 `git checkout --` 还原，主工作区零改动。

## 0. 结论（verdict）

**needs_revision。**

交付的核心是**可信**的：P0（U-01）真实修复且有回退即失败的测试；资金三项（U-10/U-11/U-69）
在生产 v0.11.7 上实测生效，其中 U-11 的根因我用 REST 独立证实；U-69 那条「2500→2000」的断言
改动**不是放宽断言，而是契约修正**（证据见 §3）；门禁三项（`go test ./...`、`-race`、QA）我复跑
全绿（§2）。

但按 t9 自己的交付声明（「每条配回退即失败测试」）和本轮验收口径，有 4 条没有回退即失败的测试，
2 条（U-31 的收货地址、U-36 的列表字段）在真实店铺上**基本没有生效**，2 条（U-24/U-25）只修了
一半。这些不是「重做」，是补测试与补实现的具体返工项（§5）。

## 1. 逐条核对（22/22）

代码存在 = 我在代码里定位到真实改动（非注释）；测试钉住 = 把改动回退后该测试会失败（我实际回退验证过
的标「回退实测」，其余按断言内容判断）；真实调用 = 我在生产 v0.11.7 上用 MCP 实际调过。

| U | 代码改动 | 测试钉住 | 真实调用 / 备注 |
|---|---|---|---|
| U-01 (P0) | ✅ `product_write.go:289-297` 价格切片去掉 Weight | ✅ 回退实测：`TestBuildUpdatePayloadPricingFieldsKeepTheirCodes` FAIL（cost=2026-02-01, special_from_date=40.00） | ✅ t10 实测：只改 weight，price 仍 45、special_price/cost 未变 |
| U-07 | ✅ `client.go:549 decodeUpstreamBusinessError` | ✅ `TestSetTierPricesRejectsBusinessErrorIn200` | ✅ 实测：tier set 返回 `validation_failed` + `upstream_business_error=true/upstream_status=200` |
| U-08 | ✅ `tier_prices.go:86 tierPriceUpstream` 双拼写归一 | ✅ `TestTierPricesReadsProductResourceFieldNames` | ⚠ 实测 SKU 无阶梯价 → `{"prices":[]}`（空是正确结果，未能正向验证 value/qty） |
| U-10 | ✅ `IsVisibleOnFront int` | ✅ 回退实测：FAIL（`false, want a JSON number`） | ✅ 实测：带 comment 的 creditmemo 成功（entity 2），REST comments 落库 |
| U-11 | ✅ `shipping_amount` + `shippingRefund()` + 总是下发 | ✅ 回退实测：3 条测试 FAIL | ✅ 实测 + 根因实证（§4） |
| U-12 | ✅ `checkRefundableAmount` 金额上限 | ✅ `RejectsAdjustmentOverRefundable` | ⚠ 只按「件数×单价+剩余运费」建模，未扣已发生的调整费（§5 F6） |
| U-13 | ✅ `invoiceItemQuantities` + `checkInvoiceQuantities` | ✅ 2 条 | ✅ t10 实测：超额退款被拒，details 带 invoice_qty |
| U-14 | ✅ `captured = in.Capture && state==2` | ❌ **无**（回退实测：`TestCreateInvoice` 仍 ok） | ✅ 实测：`captured:false` 而 `invoice.state:2` |
| U-15 | ✅ `invoice_id` + `invoice_linked` | ✅ `ReportsInvoiceLinkFromReadBack` | ⚠ `invoice_linked` 在 false 时缺省（实测 None，§5 F7） |
| U-21 | ✅ details 带 `refundable_units/requested_units` | ✅ `RefundableQuantityIsStructured` | ✅ t10 实测超额退款 details 结构化 |
| U-22 | ✅ `routeWords` 加 `"ship"`（`client.go:383`） | ❌ **无**（回退实测：`TestRedactedRouteKeepsActionWords` 仍 ok，用例里没有 `/order/1/ship`） | — |
| U-24 | ✅ `upstream_reason` 降级 + 标识符替换 | ✅ 回退实测：FAIL（`upstream_reason = ""`） | ⚠ 含纯数字的原因仍整句丢弃（§5 F5） |
| U-25 | ✅ 数字占位符按数组位置解析/省略 | ✅ 2 条 | ⚠ 命名占位符 + 数组参数仍原样透出（实测 `=%customerGroup`，§5 F4） |
| U-26 | ✅ `classifyUpstreamReason` transient → conflict+retryable | ✅ `TestStatusErrorMarksContentionRetryable` | — |
| U-27 | ✅ 状态冲突 → `conflict` + `reason=document_state_conflict` | ✅ `TestStatusErrorClassifiesStateConflict` | — |
| U-28 | ✅ 未注册工具 → `not_found`（`server.go`） | ✅ `TestUnknownToolReturnsNotFoundNotInternal` | ✅ 实测：`{"code":"not_found","details":{"tool":"magento.operation.status"}}` |
| U-30 | ✅ `retryable` 恒存在 | ✅ `toolerr_test.go:139` | ✅ 实测：每个错误都带 `"retryable": false` |
| U-31 | ⚠ `qty_*`/`shipping_description` 有效；`shipping_address` 是死字段 | ❌ **无**（回退实测：`TestGetOrderReturnsProjection` 仍 ok） | ⚠ 实测 order 3/4：`billing_address` 有、`shipping_address` 无（§5 F2） |
| U-36 | ⚠ 只补 `limit` 行；本店列表不认 pageSize | ✅ 单测过，但 fake 掩盖了真实行为 | ⚠ 实测 limit=50 → 返回 182 行、仅 50 行有 name（§5 F3） |
| U-69 | ✅ `creditmemoTotal` = 行 + 运费 + 调整 | ✅ 回退实测：FAIL（want 2000 USD） | ✅ 实测：账本 `amount_minor=3500` == 单据 `grand_total=35.00` |
| U-76 | ✅ `ApproveAs` 拒绝非 human 身份 + admin 传 class | ✅ `TestApproveRejectsServiceIdentity` + admin 测试 | — |
| U-78 | ✅ `recordOutcome` 用 `context.WithoutCancel` + 审计失败打日志 | ❌ **无**（回退实测：`internal/mcp/server` 与 `cmd/server` 全绿） | — |

**统计**：22 条中代码改动真实存在 22/22；有「回退即失败」测试 18/22（U-14/U-22/U-31/U-78 缺）；
真实调用正向验证 9 条（U-01/U-07/U-10/U-11/U-13/U-14/U-28/U-30/U-69），另有 3 条实测暴露残留
（U-24/U-25/U-36）。

## 2. 门禁复核（真实通过）

| 门禁 | 命令 | 结果 |
|---|---|---|
| 全量测试 | `MAGE2MCP_TEST_DATABASE_URL=… go test -count=1 ./...` | **exit 0**（全部 `ok`，无 skip） |
| 竞态 | `MAGE2MCP_TEST_DATABASE_URL=… go test -count=1 -race ./...` | **exit 0**（无 DATA RACE） |
| QA 套件 | `MAGE2MCP_TEST_DATABASE_URL=… go test -tags qa -count=1 ./test/qa/` | **exit 0**（`ok …/test/qa 3.129s`） |

三条都是我自己重跑的，不是引用 t9 的输出。为了排除「别人的未提交改动混进结果」，这三条最终都在
**干净的 v0.11.7 worktree**（`/tmp/review-t9`，HEAD `e214ee7`，`git status` 无已跟踪文件改动）里复跑，
结果同上；审查期间主工作区确实出现过其他 agent 的未提交改动，所以以 worktree 结果为准。另：
`scripts/qa-independent.sh` 也在同一快照上 PASS（18 passed / 0 skipped / 0 failed）。

## 3. 重点复核一：`document_write_test.go` 的 2500→2000 —— 是契约修正，不是放宽断言

这是本次审查最重要的一项，我的判定是**契约修正**，理由三条，每条都有独立证据：

**（a）回退实验证明这条断言现在真的钉住行为。** 在 worktree 里把 `creditmemoTotal` 改回「只记行小计」，
`TestCreateCreditmemo` 立即失败：

```text
--- FAIL: TestCreateCreditmemo (0.00s)
    document_write_test.go:211: ledger amount = 0x… USD, want 2000 USD
```

如果这是「为了让测试变绿而改断言」，回退实现就不该让它变红。它变红了，说明断言与新契约绑定。

**（b）2000 的算术可从测试输入独立复算。** 测试输入是 1×25.00 行、`shipping_amount` 由 `shippingRefund()`
算出 0（部分退款）、`adjustment_positive=10`、`adjustment_negative=15`：25 + 0 + 10 − 15 = **20.00 = 2000**。
旧断言 2500 恰好等于「行小计」，也就是被 U-69 判定为少记的那个值。断言没有变成「非空」或「任意值」，
仍是精确值。

**（c）真实店铺上账本与单据对得上。** 我在生产 v0.11.7 建了订单 4（两件，运费 10）、全额开票，
然后做一次**部分**退款：行 34.00、`adjustment_positive=2`、`adjustment_negative=1`、运费 0。

```text
MCP: creditmemo.grand_total = 35  (subtotal 34, shipping_amount 0, adjustment 1)
SQL: select operation, amount_minor, currency from operations where id='aa9fcc51-…';
     magento.creditmemo.create | 3500 | PLN | succeeded
```

`amount_minor = 3500` 与 Magento 侧 `grand_total = 35.00` 一致；旧实现会记 3400。U-69 的原始发现
（FIN-17：账本少记运费与调整费，7 张单据累计少记 82.00）就是这条，所以「账本记单据总额」是
**修正后的契约**，不是放宽。

> 一个诚实的边界：测试里的 fake 回读固定返回 `grand_total:25`，与算出来的 20 不一致。这不影响判定
> （断言的是网关算出的、将要下发的单据总额），但 fake 与真实 Magento 的偏差建议后续修一下，否则
> 未来有人想断言「账本 == 回读单据总额」时会踩到。

## 4. 重点复核二：U-11 的根因判断成立（不传 shipping_amount 就是全退）

t9 的说法是「省略 `arguments.shipping_amount` 时 Magento 默认退全部剩余运费」。我在真实店铺上
直接验证了这个根因：对订单 3（发票 4 已开、运费 15）用 REST 做一次**部分**退款（1/3 件，行 34.00），
**不带** `arguments`：

```bash
curl -X POST .../rest/pl/V1/order/3/refund -d '{"items":[{"order_item_id":5,"qty":1}],"notify":false}'
# → "3"（credit memo id）
curl .../rest/pl/V1/creditmemo/3
# → {"grand_total":49, "subtotal":34, "shipping_amount":15, ...}
```

部分退款却退了 **全部 15 运费**（34 + 15 = 49）。根因成立。修复后的行为（同店、v0.11.7 网关）：
部分退款 `shipping_amount=0`、最后一件退剩余运费 10（订单 4：34 → 35，7 → 17，合计 51 = 订单总额）。

## 5. 发现（按严重度）

### F1（medium）22 条里 4 条没有「回退即失败」的测试 —— 交付声明与事实不符

t9 声明「每条配回退即失败测试」。我在 worktree 里逐条回退，以下 4 条回退后测试**仍然全绿**：

| U | 回退动作 | 结果 |
|---|---|---|
| U-14 | `Captured: invoice.State == invoiceStatePaid` | `TestCreateInvoice` ok（表里只有 `capture:false,state:1`，没有缺陷现场 `capture:false,state:2`） |
| U-22 | 从 `routeWords` 删 `"ship"` | `TestRedactedRouteKeepsActionWords` ok（用例有 `/order/1/refund`，没有 `/order/1/ship`） |
| U-31 | 删掉三个 `Qty*` 字段 | `TestGetOrderReturnsProjection` ok（没有断言新字段） |
| U-78 | `opts.Audit(ctx, …)` 改回失效 ctx | `internal/mcp/server` + `cmd/server` 全 ok |

**要求的修复**：各补一条能在回退时失败的测试——U-14 加 `capture:false, state:2 → captured:false`；
U-22 加 `/order/1/ship → /order/{id}/ship`；U-31 断言 `qty_shipped/qty_invoiced/qty_refunded` 与地址
（地址测试要先修 F2）；U-78 用 `Audit` 回调断言 `ctx.Err() == nil`。

### F2（medium）U-31 的「收货地址」没有真正交付：`shipping_address` 是永远为空的死字段

`Order.ShippingAddress` 是顶层 JSON 字段 `json:"shipping_address"`，但 Magento 的订单资源**不返回**
顶层 `shipping_address`，它在 `extension_attributes.shipping_assignments[0].shipping.address`。代码里
没有自定义 Unmarshal 去取它。实测（v0.11.7 生产）：

```text
order 3: billing_address 有、shipping_address 无、shipping_description "Flat Rate - Fixed"
order 4: billing_address 有、shipping_address 无、shipping_description "Flat Rate - Fixed"
REST /orders/4: top-level shipping_address = False；shipping_assignments[0].shipping.address.city = Warszawa
```

而 U-31 的原始发现（SO-04/CS-04）明确包含「无收货地址」。客服「复述收货地址」的场景**仍然做不到**
（账单地址可以）。**要求的修复**：从 `extension_attributes.shipping_assignments` 取地址填充
`ShippingAddress`，并加一条断言它的测试。

### F3（medium）U-36 在本店基本无效：只补了 `limit` 行，而列表接口不认 pageSize

本店 `GET /categories/{id}/products` **忽略** `searchCriteria[pageSize]`：要 3 条返回 182 条。
`hydrateCategoryProducts` 用 `limit` 作为补数搜索的 pageSize，于是最多只补 `limit` 行：

```text
MCP limit=50 → products 返回 182 行，其中 50 行有 name，132 行仍是 name ""/price 0/status 0
MCP limit=2  → 返回 182 行，仅 2 行被补（且补的是 Magento 搜索顺序里的前 2 条，不是列表前 2 条）
代码直探：listing rows=182，hydration search items=2（pageSize=2）
```

单测用的是「一页只有 1 条」的 fake，所以测不出这个耦合。**要求的修复**：补数搜索的 pageSize 与
「实际返回行数」解耦（例如按实际行数分批补，或按 100/批分片），并加一条「列表返回行数 > limit」的
fake 用例。

### F4（low-medium）U-25 只修了数字占位符；命名占位符 + 数组参数仍原样透出

实测 v0.11.7 的 tier-price 拒绝消息：

```text
"upstream_message": "Brak takiego podmiotu z grupą klientów =%customerGroup. … SKU = %SKU, … %websiteId, … %qty."
```

`resolveUpstreamPlaceholders` 在 `len(parameters)>0` 时，把命名占位符查 `lower[name]`，查不到就
`return token`（原样保留）。而 U-25 的发现文本里点名了这个例子（`=%customerGroup`）。**要求的修复**：
命名占位符查不到参数时也替换成 `(omitted)`，并加一条「命名占位符 + 数组参数」的测试。

### F5（low-medium）U-24 的 SKU 形状启发式会把「纯数字」当成 SKU，整句原因被丢弃

真实退款被 Magento 拒绝时（订单 4 第二次退款，见 F6），原始原因是：

```text
{"message":"Creditmemo Document Validation Error(s):\nWiększość pieniędzy do zwrotu to 16."}
```

网关给调用方的只有 `{"store_code":"pl","upstream_status":"400"}`，没有 `upstream_message` 也没有
`upstream_reason`。我用 `statusError()` 直探确认原因进了 `Upstream`（只进日志）。根因：`upstreamSKURE`
（`[A-Z0-9][A-Z0-9_-]*[0-9][A-Z0-9_-]*`）能匹配 `16` 这样的纯数字，于是「含 SKU」判定成立、整句被丢。
这正是 U-24 想解决的「资金失败原因不可读」，在金额场景下又出现了。**要求的修复**：SKU 启发式要求
至少一个字母（例如 `[A-Z][A-Z0-9_-]*[0-9]`），或对纯数字做白名单。

### F6（low）U-12 的金额上限模型不含「已发生的调整费」，超额仍会打到上游

同一订单 4：第一次退款带 `+2/−1`（贷项总额 35，其中 1.00 是净调整）。第二次退最后一件时本地
`refundableRemaining = 7 + 10 = 17` 通过，但发票剩余可退只有 16（51 − 35），上游拒绝：

```text
本地：validation_failed 未触发（17 ≤ 17）
上游：400，原因见 F5（"最多可退 16"），调用方只看到 "verify store_code and field values"
```

**要求的修复**：`refundableRemaining` 扣掉「已退款额（含调整）」与发票已退额，或至少把上游的
「可退金额」原因透出（F5）。

### F7（info）U-15 的 `invoice_linked` 在 false 时缺省

实测 credit memo 响应：`"invoice_id": 3`，`invoice_linked` 字段**不存在**（`omitempty`）。语义上
「缺省 = 未确认」可接受，但 U-30 刚刚确立了「布尔字段要显式」的契约，这里不一致。建议显式输出 false。

### F8（info）交付溯源：多数修复落在 docs 标题的提交里

`git log v0.11.6..v0.11.7 -- <file>` 显示 `order_documents_write.go` / `tier_prices.go` /
`invoice_write.go` / `confirm.go` / `server.go` 的修复都落在 `69fa64d docs(persona): …` 这类提交里，
`product_write.go` 落在 `bccb4bd docs(persona): …`。功能上不影响，但「哪次提交修了 U-01」从历史里
答不出来。队长已在 `captain-adjudication.md` 里认领这个 `git add -A` 事故，这里只作为审查记录。

## 6. 我实际跑过的复现（≥4 条，逐字证据见 §3/§4/§5）

1. **U-01**：worktree 回退 → 测试 FAIL（字段错位值原样打出）；生产实测只改 weight 不改价。
2. **U-11**：REST 省略 `arguments` → 部分退款退全部运费（49 = 34+15）；网关修复后部分退 0、最后一件退 10。
3. **U-10**：生产带 comment 的 creditmemo 成功，REST comments 落库 `is_visible_on_front=0`；回退 → 测试 FAIL。
4. **U-78**：回退 `WithoutCancel` → 全部测试仍绿（= 无保护），代码本身正确。
5. **U-69**：回退 `creditmemoTotal` → 测试 FAIL；生产账本 `amount_minor=3500` == 单据 35.00。
6. **U-14/U-28/U-30/U-31/U-36/U-25**：生产实测（见上表与 §5）。

## 7. 返工清单（按优先级）

1. **F2**：`shipping_address` 从 `extension_attributes.shipping_assignments` 取；补测试。这是唯一
   「声称修了但功能不存在」的项。
2. **F3**：U-36 补数与列表实际行数解耦；补「列表返回行数 > limit」的用例。
3. **F1**：补 U-14 / U-22 / U-31 / U-78 四条回退即失败的测试。
4. **F4 / F5**：命名占位符兜底；SKU 启发式要求含字母。两条都直接影响资金失败的可读性。
5. **F6 / F7**：可退金额模型扣已退款；`invoice_linked` 显式 false。

## 8. 审查边界

- 我只审了这 22 条与它们触及的文件，没有重审其他 owner 的 G6/G7/G8/G11 工作（t11/t12/t13/t14）。
- 审查快照是发布提交 `e214ee7`（`v0.11.7-2`，代码与 `v0.11.7` 相同）。审查期间主工作区有其他 agent
  的未提交改动，我以干净 worktree 为准；截至本报告完成，`internal/magento/order.go` 里仍没有
  `shipping_assignments` 的处理（F2 未被并发修掉）。
- 真实调用都在共享 demo 店上做，产生了订单 3/4、发票 3/4、贷项 2/3、客户 2 等测试数据（未清理）。
- 生产 v0.11.7 的「P0 实测生效」我复核过（U-01 在本轮又跑了一次），与队长的结论一致。
