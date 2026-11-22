# 独立审查（第二轮）：t30 对 t19 审查发现的修复

审查对象：**t30（repair-round-2）**——对我在 `review-t9.md` 里提出的 F1–F7 的修复，以及它顺带修的
GG-01（`creditmemo.create` 的 `shipping_amount`）与「order 读不返回地址」的说明面更正。
方式：**只读**——读代码与测试、在独立 worktree 里做**回退实验**、对真实店铺做**只读探针**、在干净
worktree 上复跑门禁。未修改任何产品代码。

快照：`git describe` = `v0.11.7-10-g84bb447` → 审查期间 HEAD 前进到 `7b7b207`（仅文档）。
回退实验与门禁都在 `/tmp/review-t30`（`git worktree add HEAD`）里做，做完 `git checkout --` 还原；
审查期间主工作区有 t28 的未提交改动（`server.go`/`server_test.go`/`toolerr.go`），所以**一律以
干净 worktree 为准**，避免把 t28 的在飞工作混进结论。

## 0. 结论：**pass**

t30 的 7 条修复全部真实落地，**10 条「回退即失败」我逐条复现，全部按预期变红**；
没有放宽任何既有断言（test 改动只有一处重排）；门禁三项在干净 worktree 上真实通过；
两条最关键的行为修复（F2 收货地址、F3 分类列表补数）我用**真实店铺调用**独立验证：
order 3/4 现在返回 `shipping_address`（城市 Warszawa），分类 2 的 **182 行全部补全**（t19 时是 50/182）。

三条非阻断观察写在 §7。

## 1. 逐条核对（F1–F7 + 顺带项）

| 项 | 代码改动 | 钉住它的测试 | 回退实验（我做的） | 独立证据 |
|---|---|---|---|---|
| **F1/U-14** | `invoice_write.go` 只改注释；逻辑 `Captured = in.Capture && state==paid` 已在 v0.11.7 | `TestCreateInvoiceDoesNotClaimCaptureForOfflinePayment` | 回退成 `Captured = state==paid` → **FAIL** | — |
| **F1/U-22** | `routeWords` 已有 `"ship"`；补测试用例 | `TestRedactedRouteKeepsActionWords`（新增 `/order/1/ship`） | 删 `"ship"` → **FAIL**（得到 `/order/{id}/{id}`） | — |
| **F1/U-31** | 补测试（`qty_*` 三个字段） | `TestGetOrderReturnsFulfilmentCounters` | 改 `qty_shipped` 的 json tag → **FAIL** | — |
| **F1/U-78** | 补测试（审计 ctx） | `TestOutcomeAuditSeesLiveContextAfterTimeout` | 回退 `context.WithoutCancel` → **FAIL**（deadline exceeded） | — |
| **F2/U-31** | `order.go` 新增 `fillShippingAddressFromAssignments`（顶层优先、解包失败不影响读单） | `TestGetOrderReadsShippingAddressFromAssignments`、`TestGetOrderPrefersTopLevelShippingAddress` | 去掉调用 → **FAIL**（shipping nil） | **真实调用**：order 3/4 `shipping=true, city="Warszawa"`（§4） |
| **F3/U-36** | `hydrateCategoryProducts` 去掉 `limit`，按列表实际 SKU 分批（100/批，每批 pageSize=该批数量） | `TestCategoryProductsHydratesMoreRowsThanLimit` | 把 pageSize 改成「批大小−1」（模拟旧的 pageSize<行数）→ **FAIL** | **真实调用**：分类 2 返回 182 行、**182 行全部有 name**（§4） |
| **F4/U-25** | 命名占位符查不到也替换 `(omitted)`；无参数分支同时处理命名/数字 | `TestStatusErrorOmitsUnresolvedNamedPlaceholder`、`TestStatusErrorOmitsNamedPlaceholderWithoutParameters` | 改回 `return token` → **FAIL** | — |
| **F5/U-24** | `upstreamSKURE` 改为「必须同时含字母与数字」 | `TestStatusErrorKeepsReasonThatMentionsAnAmount`、`TestStatusErrorStillHidesMixedIdentifier` | 回退旧正则 → **FAIL** | — |
| **F6/U-12** | `orderRefundState` 新增 `TotalRefunded`；上限 = min(行估算, grand_total−total_refunded)，负值钳 0 | `TestCreateCreditmemoCeilingIncludesEarlierAdjustments` | 禁用上限（`if false`）→ **FAIL** | — |
| **F7/U-15** | `creditmemoCreateOutput.InvoiceLinked` 无 `omitempty` 并赋值 | `TestCreditmemoToolOutputAlwaysCarriesInvoiceLinked`（断言 false/true 都显式序列化） | 加回 `omitempty` → **FAIL** | — |
| **GG-01**（顺带） | schema 加 `shipping_amount`（`write_tools.go:1095`）+ 工具入参结构体 `*float64`（:1126）+ handler 透传（:1185）+ 描述 + 文档 | `TestCreateCreditmemo` 链路 + schema 校验 | （行为已由 F6/U-11 系列测试覆盖） | 三处链路齐全（§5） |
| **隐私说明面**（顺带） | `tools.go` 的 `order.get` 描述、`order.go` 的结构体注释、README 三处同步改口 | `TestOrderGetAddressDisclosureIsConsistent`（三个来源都必须写 "shipping and billing addresses" 与 "address email"，且禁止四句旧否认） | — | — |

## 2. 十次回退实验（全部按预期变红）

在干净 worktree 里逐条打补丁 → 跑对应测试 → `git checkout --` 还原：

```text
== F1/U-14 ==  FAIL as expected
== F1/U-22 ==  FAIL as expected
== F1/U-31 ==  FAIL as expected
== F1/U-78 ==  FAIL as expected
== F2 ==       FAIL as expected
== F3 ==       FAIL as expected
== F4 ==       FAIL as expected
== F5 ==       FAIL as expected
== F6 ==       FAIL as expected
== F7 ==       FAIL as expected
（worktree 已还原，git status 干净）
```

这与 t30 自述的「10 次回退实验，每次都变红」一致——但这是我自己重跑的，不是引用它的结论。

## 3. 断言是否被放宽：没有

- 逐文件看 `322b438` 的 `-` 行：**测试文件里只有一行被删**，是 `client_test.go` 把
  `"/shipments/1": "/shipments/{id}"` 重排成对齐格式（同一用例仍在，并新增 `/order/1/ship`）。
  其余测试改动全是新增（`+1528 / −55`，其中 −55 含产品代码的旧注释与旧实现）。
- **测试名清单对比**（`c49d201` → HEAD，全部 `*_test.go`）：**丢失 0 条**，测试函数从 **911 → 930**（+19）。
  这条专门用来复核队长记录的「整文件重写冲掉 3 条测试」事故：被冲掉的 3 条
  （`TestGetOrderReturnsFulfilmentCounters`、`TestGetOrderReadsShippingAddressFromAssignments`、
  `TestGetOrderPrefersTopLevelShippingAddress`）现在都在，且我逐个跑过（含在回退实验里）。
- 既有断言没有被改成「非空/任意值」之类：F7 的测试断言精确的 `"invoice_linked":false`/`true`；
  F6 的测试断言「本地拒绝 + POST 0 次」；F5 的测试断言原因里出现金额、且混合标识符仍不泄漏。

## 4. 真实调用复核（只读，v0.11.7 生产店铺 + 当前代码）

用当前 worktree 的 `magento` 包直连 demo 店铺（`store pl`），只读：

```text
order 4: billing=true shipping=true shipping_city="Warszawa" shipping_desc="Flat Rate - Fixed"
order 3: billing=true shipping=true shipping_city="Warszawa" shipping_desc="Flat Rate - Fixed"
分类 2: returned=182 total=182 hydrated=182
```

对照我在 `review-t9.md` 的记录：F2 之前 order 3/4 的 `shipping_address` **恒为 nil**（REST 里也没有顶层
`shipping_address`）；F3 之前 limit=50 时 182 行只有 50 行有 name、limit=2 时只有 2 行。
**两条都在真实数据上闭环。**

## 5. GG-01 的三处链路（t30 顺带修）

`creditmemo.create` 的 `shipping_amount` 在 v0.11.7 里是「Go 服务层有、工具层没有」，t30 把三处接通：

- schema：`write_tools.go:1095` 的 `shipping_amount`（`number`，`minimum: 0`，含默认语义说明）；
- 工具入参结构体：`write_tools.go:1126` 的 `*float64`（指针，区分「省略」与「0」）；
- handler：`write_tools.go:1185` 透传 `ShippingAmount: in.ShippingAmount`；
- 描述与 `docs/magento-fields.md` 同步写了「省略=部分退款不退运费、全额退剩余」。

这解释了我在 t19 里发现的「schema 手写字符串与 Go 结构体不同步」的根因——t28 计划里的
`schema↔结构体`注册期校验正是防它复发的那道闸，尚未落地（t28 在飞）。

## 6. 门禁（干净 worktree，HEAD `7b7b207`）

| 门禁 | 命令 | 结果 |
|---|---|---|
| 全量测试 | `go test -count=1 ./...` | **exit 0**（23 包 ok） |
| 竞态 | `go test -count=1 -race ./...` | **exit 0**（23 ok，无 DATA RACE） |
| QA 套件 | `go test -tags qa -count=1 ./test/qa/` | **exit 0**（`ok …/test/qa 2.929s`） |

三条都在干净 worktree 上跑，排除了主工作区里 t28 在飞改动的干扰。

## 7. 非阻断观察（不影响 verdict）

1. **F5 的取舍**：新正则要求「字母+数字」，所以上游原因里的**纯数字**（金额、数量、订单号）现在会透出
   给调用方。这是修复 F5 的必要代价（`16` 不再被当 SKU），且金额/数量正是调用方需要的；
   纯数字标识符（如订单号）本身不是 PII。记录为已接受的取舍。
2. **隐私面扩宽**：`order.get` 现在返回收货/账单地址，其中地址自带 `email` 字段。这是队长在
   `322b438` 里明确裁决的（「support 无法在没有地址的情况下确认配送」），三处文案与一条测试同步，
   不是 t30 擅自扩大 PII。若产品后续要求最小化，应作为独立决策而不是回退这条修复。
3. **流程风险（非 t30 责任）**：队长已记录「整文件重写冲掉 3 条测试而门禁仍绿」的事故。我复核确认
   当前无测试丢失（911→930），但**门禁无法证明「该存在的测试还在」**——建议把「测试名清单对比」
   固化成脚本（本次我用一次性脚本做的），否则下一轮同类事故仍会静默通过。

## 8. 后续

- t28（说明面 Task A）在飞：`instructions` 五段重写 + `toolerr.Add` 的 schema↔结构体校验 + GG-15 的
  35 处 `instance` 措辞。它落地后，`shipping_amount` 这类「三层链路」问题就有注册期闸门了。
- **生产部署后的 live 回归仍待做**：本轮所有证据都来自「当前代码 + 真实店铺只读调用」，尚未把
  t30 的构建部署到 `49.51.250.83` 再跑 123 用例矩阵。按 t10 的做法（独立第二实例 + 独立库）即可，
  不需要动生产容器。
