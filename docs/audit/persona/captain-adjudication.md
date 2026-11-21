# 队长裁决与独立核验

对岗位报告中需要裁决或需亲验的条目。

## 裁决：P-SEC-03 审批人不绑角色 —— **是缺陷，P1**

安全合规岗位报告称：`viewer@test` 能批准 admin 的 destructive 删除；`type=service` 的 agent
令牌也能批准。它请我裁决这算不算缺陷。

**我的判定：算，P1。** 依据：

1. **ADR-0014 有明确立场**。文档 `docs/adr/0014*.md:28-29` 在「考虑过的方案」里写道：
   「Approval through the same MCP connection by a second agent. Cheap, but **an agent cannot
   be an approver of record**.」——这是被明确拒绝的方案，不是未定义行为。
2. **代码没有执行这个立场**：
   - `internal/security/jwt/jwt.go:181-183` 会算出 `Identity.Class`（human / service）
   - 但全仓库对 `Class` 的使用只有审计归因（`internal/mcp/server/server.go:1093`、
     `cmd/server/main.go:134`），**没有任何授权判断用它**
   - `internal/security/confirm/confirm.go:122-130` 的 `Approve` 只检查 approver 非空
   - `cmd/admin/main.go` 验签之后不查角色

3. **为什么严重**：两步确认的全部意义是「**另一个人**同意」。如果发起操作的那个 agent 能自己
   批准，这个闸门就不存在了。这不是「产品对审批人角色的定义有分歧」，而是**代码没有实现文档
   写下的承诺**。

修复方向：`cmd/admin` 验签后要求 `Identity.Class == human`；`confirm.Approve` 拒绝
service 类身份。是否额外要求「审批人角色高于发起人」需要产品决策，但「agent 不能批准」是
ADR 已经决定的。

## 独立核验：P-CS-02 带备注的退款/发货必定 400 —— **CONFIRMED**

客服岗位报告称 `creditmemo.create` / `shipment.create` 只要带 comment 就 400，上游原因被吞。
它给出的根因是 `is_visible_on_front` 的类型错了。

**我用店铺自身 schema 核对，确认**：

```text
sales-data-creditmemo-comment-creation-interface:
  comment              : string
  is_visible_on_front  : integer      ← Magento 要整数
```

网关实际发送：

```text
internal/magento/order_documents_write.go:360-363
  IsVisibleOnFront bool  `json:"is_visible_on_front"`   ← 发的是布尔
```

**结论：CONFIRMED。** Magento 的 `is_visible_on_front` 是 int（0/1），传 JSON 布尔会被校验
拒绝，因此**带备注的退款与发货整条路径不可用**。这是一行类型修正，但它挡住的是「退款时给顾客
留说明」这个日常操作。

## 待核验

- P-CS-01 部分退款连带退全额运费（需产品决策：是否暴露 `shipping_amount`）
- P-SEC-01 超时/限流调用无 outcome 审计行
- P-DA-01..04 数据分析口径问题

## 独立核验：P-FIN-12 不做发票级数量校验 —— 需亲验

财务报告称：对只开 1 件的发票冲销 2 件成功，贷项金额超过该发票。若成立，这是**资金级缺陷**
——贷项可以超出对应发票，等于凭空多退。

同时 P-FIN-07 / P-CS-01 两个岗位独立报了同一件事：**部分退款把运费全额退掉**，且没有参数可控。

这两条都与「退款金额」直接相关，我要求修复阶段优先处理，并在回归时用真实金额验证。

## 环境观察：P-FIN-19 可能是部署造成的

财务观察到 19:23–19:26Z 期间「上游成功、账本记 succeeded，调用方却收到 internal」，时间与我在
19:22:25Z 的重新部署重合，之后 16 次调用未复现。

**我的判断：这是部署导致，不是代码缺陷**——`docker compose down` 发 SIGTERM，服务在
`shutdown_grace` 窗口内处理在途请求时可能返回错误，而上游调用已经完成。

**但它揭示了一个真实风险**：优雅关闭期间，调用方可能收到错误而操作已经生效。若调用方**没有**
用 `idempotency_key` 就重试，会产生重复退款。这一点应在文档中说明：**写操作必须带
idempotency_key 才能安全重试**。

## 独立核验：P-MERCH-01 阶梯价静默失败 —— **CONFIRMED**

商品运营报告称 `tier_prices.set` 把 Magento 的「HTTP 200 + 错误数组」当成功。

**我实测复现**：

```text
调用: tier_prices.set sku=24-MB01 customer_group="0" quantity=3 price=30
网关返回: {"sku":"24-MB01","prices":1,"set":true}     ← 报告成功

直连同一路由，Magento 实际返回:
[{"message":"Brak takiego podmiotu z grupą klientów =%customerGroup. ..."}]
```

Magento 用 **200 + 错误数组**表示失败，网关只检查 HTTP 状态码，于是「一行没写入」被报告成
「set: true」。

**这是同一类静默失败的第三次出现**（前两次是导入的 status/visibility、阶梯价的读取映射）。
共同特征：**上游用 200 传递业务失败，而网关只看状态码**。

## 独立核验：P-MERCH-02 阶梯价读取字段映射错 —— **CONFIRMED**

Magento 的**读取**接口与**写入**接口字段名不同：

| 接口 | 字段 |
|---|---|
| 读（`catalog-data-product-tier-price-interface`） | `customer_group_id`、`qty`、`value` |
| 写（`catalog-data-tier-price-interface`） | `customer_group`、`quantity`、`price` |

网关读取时用的是**写入**的字段名，所以 `price` 恒 0、`qty` 恒 0、客户组为空。

**两条叠加 = 阶梯价对商品运营完全不可用**：设不进去，也读不出来。而前台能力本身正常
（运营用 REST 直连设好后，产品页显示「Kup 5 po 119」、购物车单价正确）——**问题全在网关**。

## 独立核验：改重量会把售价改掉（P0 数据损坏）—— **CONFIRMED，实测复现**

店铺运营报了一条 P0：`buildUpdatePayload` 把参数**整体错位一格**传给定价属性构造函数。

### 代码证据

```text
internal/magento/product_write.go:634-636
  pricingAttributeOrder = ["special_price", "special_from_date", "special_to_date", "cost", "tax_class_id"]

internal/magento/product_write.go:286-288   buildUpdatePayload
  传: [in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost]   ← 5 个，含 Weight
  → index 0 的 Weight 被当成 special_price

internal/magento/product_write.go:666-672   pricingAttributes（创建路径）
  传: [specialPrice, specialFrom, specialTo, cost]                                  ← 4 个，正确
```

**创建路径正确，更新路径错位**——所以 `product.create` 正常，`product.update` 全线出错。

### 实测复现（生产 v0.11.6）

1. 建一个简单商品：`price=100.00`，无促销价
2. 只调用 `product.update` 设置 `weight: "9.99"`，**不碰任何价格字段**
3. 回读：

```text
price: 100
weight: 9.99
special_price: 9.990000        ← 重量泄漏进了促销价
```

**一个只改重量的操作，把商品变成促销价 9.99 在卖。** 探针商品已删除。

### 影响面

按同一错位，其余字段全部串位：

| 想设的 | 实际写进 |
|---|---|
| weight | special_price |
| special_price | special_from_date |
| special_from_date | special_to_date |
| special_to_date | cost |
| cost | tax_class_id |

所以「设促销价」必然 400（价格字符串被塞进日期字段），而报错还指向 `special_from_date`——运营会去改日期，永远改不好。

`configurable.update` 与导入的更新路径都复用 `Update()`，同样中招。

### 判定

**P0，发布阻断。** 这是本轮最严重的一条：一个日常的、看似无关的操作（改重量）会静默改变售价，且没有任何报错。同时 `internal/` 下**没有任何测试覆盖** `buildUpdatePayload` / `pricingAttributesFromPointers`——这正是它能活到现在的原因。

## 裁决：order.go 的写入权归 t9，SalesSummary 接线延后

t11（报表口径）开工前报了一个真实的范围冲突：验收要求「summary 与 analysis 的按天分桶用同一
时区口径」，但 `magento.sales.summary` 的实现、schema、输出结构都在 `internal/magento/order.go`，
而该文件同时被 t9（integrator 的订单读取修复）需要。

**裁决：选 B，不把 order.go 分给两个 agent。**

理由不是保守，是**并发改同一文件的覆盖在 git 里看不出来**——两个 agent 各自都会报告「改成功」，
合并时一方的修改静默消失。P0（价格错位）正在 t9 手上，这个节骨眼上不值得引入文件级竞态。

具体安排：

| 项 | 归属 |
|---|---|
| analysis 侧时区日桶 + `timezone` 字段 + 日期不越窗 | t11（data-analyst） |
| 共享 helper（包内可见） | t11，供后续复用 |
| **summary 接线（SalesSummary 日桶、Timezone 字段）** | **t9 完成后由队长另建任务** |
| U-54（1000 单误报 truncated）、U-56（366 天上限）、U-47（无 tax/refund 字段） | 同上，并入 summary 接线任务 |

同时确认：`refunded_amount` 由 t11 修成**单据级退款总额**（含运费退款、减调整费）。它当前是行
小计，比真实退款少——数值变化正是修复的意义。要求 t11 给出修前/修后数字对比，以及它与
`sales.summary.revenue` 能否对上。

**给 t11 的验收口径**：按 B 判，即「analysis 侧完成 + helper 可供接线」，summary 接线不在其验收内。
要求它如实标注哪些验收项未闭环，不许凑完整。

## 裁决：tools.go 的写入权归 t14，operation.* 注册延后

t13（账本与幂等）开工前报了第二个范围冲突：要新增 `operation.get/list/dispose` 三个工具，
构造函数可以放在它的 `operations.go`，但**注册必须在 `tools.go`**，否则 `tools/list` 里不会出现，
验收无法通过。它倾向自己改（3–5 行、纯增量）。

**裁决：选 B，不给它 `tools.go`。** 理由与 order.go 那次是同一条：`tools.go` **已经派给 t14**
（描述文本与 schema 协议修复）。两个 agent 并发改同一文件，覆盖在 git 里看不出来，各自都会报成功。

不能对 data-analyst 说「一个文件只能有一个写入者」，转头对 customer-service 说「3–5 行没关系」——
那是两套标准。

具体安排：

| 项 | 归属 |
|---|---|
| `operation.get/list/dispose` 的构造函数与台账查询/处置方法 | t13，放在 `operations.go` |
| 精确接线片段（加在哪个函数、哪一行、schema 常量名） | t13 报告里给出 |
| **实际注册** | **t14 完成后由队长另建任务** |

t13 的第 2/3 条验收按 B 判：「构造函数与查询方法完成 + 接线片段可供复用」，注册不在其验收内；
要求它明确写「注册未接线」。

### 数据库访问边界（已批准 t13 的方案）

- 生产 `mage2mcp-postgres-1`：**只读**查那 11 条 unknown
- 写操作（dispose 等）：**只在本地 127.0.0.1:55440** 验证，不动生产数据

### 对 unknown 处置路径的要求

我要求 t13 在报告里说清处置语义：**是标记人工已处理、重试、还是丢弃**。

`operation_unknown` 的含义是「不知道上游做没做」。因此 `dispose` **不能**是「当作没发生」——
必须记录人的判断（谁、何时、依据什么、结论是什么）。**这比工具叫什么名字重要**：如果 dispose
只是把状态改掉，等于把「不确定」洗成「确定」，那比留着 unknown 更危险。

## t11 交付与待接线的配方

t11 按 B 交付：改了 4 个 in-scope 文件，13 条修复，16 条新测试，**11 组变异验证**（逐条把修复
改回旧实现，11/11 都被测试杀掉）。

### 它诚实标注的未闭环项

| 项 | 位置 | 状态 |
|---|---|---|
| U-50 summary 侧（时区日桶、`timezone` 字段） | `order.go` / `tools.go` | **未接线** |
| U-47（无 tax/refund 字段）、U-54（1000 单误报 truncated）、U-56（366 天上限） | `order.go` | 未动 |
| U-62/63/64 | `order.go` / `tools.go` | 未动 |

### 后续任务的接线配方（t11 提供）

共享 helper 签名：

```text
(s *Service) resolveTimezone(ctx, timezone string) (*time.Location, string, error)
reportDay(createdAt string, loc *time.Location) string
windowLocalRange(from, to time.Time, loc *time.Location) WindowLocal
parseReportWindow(fromRaw, toRaw string, loc *time.Location) (time.Time, time.Time, error)
```

接线三步：

1. `SalesSummary` 加 `Timezone string`，用 `resolveTimezone(ctx, "")` 解析，日桶改 `reportDay(order.CreatedAt, loc)`
2. `tools.go` 里 `newSalesAnalysisTool` 的 handler 换成 `svc.AnalyzeSalesWindow(ctx, in.From, in.To, in.MaxOrders, in.Timezone)`（一行）
3. 可复用 `parseReportWindow` 给 summary 解析 from/to

### 一个值得记的过程信息

t11 报告：工作期间别人并发改同包，`go test` 一度编译失败（`document_write_test.go` 的 fakeLedger
缺 `Dispose`、`operations_test.go` 的 `operation.dispose` 缺描述）。它用
`git worktree add --detach HEAD` 建隔离树跑门禁，等对方落定后在真实树重跑也全绿。

**这是正确的应对**：并发编辑下，一个 agent 看到红不一定是自己造成的。它没有去改别人的文件来
"修好编译"，而是隔离验证、事后复核——这避免了互相踩踏。

**但它也留下一条要求**：t9/t13 后续若再改，**以真实树重跑为准**，不能只看隔离树的绿。

## 第三、四次裁决：两处接线合并为一个任务

t11 与 t13 都按 B 交付，各留下一处未闭环：

| 未闭环项 | 需要改的文件 |
|---|---|
| SalesSummary 时区接线 | `order.go` + `tools.go` |
| `operation.get/list/dispose` 注册 | `tools.go` |

**两者都要改 `tools.go`**，于是又回到同一个问题：一个文件两个写入者。

**裁决：合并成一个接线任务**（t16），而不是拆成两个再排依赖。理由：这两处改动都是「加几行接线」，
合起来仍然很小；拆开反而要串行两次、各自跑一遍门禁，成本更高。

这已经是本队的**第三次同类裁决**（order.go、tools.go 分配给 t13、现在的合并），原则始终一致：

> **同一文件同一时刻只能有一个写入者。** 并发编辑的覆盖在 git 里看不出来——两边都报成功，
> 合并时一方静默消失，而所有日志都正常。

这条原则值得带进后续任何多 agent 协作：**按文件划分工作，不按发现划分。** 按发现划分会让多个
agent 落在同一个文件上，而缺陷报告是按发现组织的，所以这个诱惑很自然。

## 独立核验：U-70 entity_id 语义 —— CONFIRMED，但位置与报告不同

t13 报 U-70「`unknown` 的 order.create 行 `entity_id` 是客户 id 不是订单号」，并说位置在
`server.go: entityFor`。我核对后确认缺陷存在，**但位置不对**。

### 实际位置

```text
internal/magento/order_create.go:120
  EntityType: storage.EntityOrder, EntityID: strconv.Itoa(in.CustomerID),
```

下单是**自记账工具**，它自己调用 `ops.Begin`，不走 `server.go` 的 `entityFor`。所以
`server.go` 里那个通用的 entity 推导与这条无关——t13 指错了地方（它自己也说「不在我可改的文件里」，
实际上在 `order_create.go`）。

### 生产数据佐证

```text
entity_type | entity_id | count
order       | 5         | 3
order       | 3         | 2
order       | 4         | 2
order       | 2         | 2
order       | 6         | 1
order       | 7         | 1
stock       | semtest   | 1
```

`entity_id` 是 2..7，正好是客户 id 的范围。

### 为什么这条重要

t13 的 dispose 工具让那 11 条「可查、可关、可重排」，但操作员拿到的 `entity_id=5` 指向的是
**客户 5**，不是订单。他按这个号去 Magento 查，会看到别人的订单——然后据以判断「变更在」，
做出错误处置。**这让处置工具的语义是错的，而不只是不方便。**

### 修法方向（不唯一，需在任务里定）

下单时订单还不存在，所以 Begin 阶段无法知道订单 id。两个诚实的选择：

1. Begin 时记 `entity_type=customer` + `entity_id=customer_id`（记录当时已知的事实），
   成功后把订单号写进 `upstream_ref` / result，处置时两者都能看到
2. Begin 时 `entity_type=order` + 空 id，成功后回填

**关键是不要在 Begin 阶段把客户 id 谎称成订单 id。** 这是本轮反复出现的同一类问题的又一例：
**字段有值、类型正确、但语义是错的**——比空值更危险，因为空值会让人去查，错值会让人下结论。

## 我自己的操作失误：`git add -A` 把 agent 的未完成改动一起提交了

t13 指出：它工作期间我的 `git add -A` 把它的改动一起提交进了 `69fa64d`，提交信息写的是别人的交付。

**这是我的问题。** 我在提交文档时用 `git add -A`，把所有 agent 的 in-flight 代码改动一并纳入。
后果：

- 提交信息与实际内容不符
- 如果要按 commit 粒度审 diff，无法区分谁改了什么
- 未完成的改动被标记为「已交付」

**从本条起改为只提交明确指定的文件**（`git add <path>`），不再用 `-A`。工作树里 agent 的改动
留在工作树，等它们各自完成后再按任务边界提交。

这不算严重事故——内容是对的、门禁是绿的——但它让历史不可读，而**可读的历史是多 agent 协作的
基本要求**：事后无法回答「这行是谁、为什么改的」。

## 核验：QA 套件红了，但不是行为回归

t9 交付后 QA 套件失败，三条断言报「ambiguous money outcome must not be marked retryable」。

**我没有直接判定是回归，先查了实际值。** 用真实 fixture + 500 上游的探针：

```text
code=operation_unknown   retryable present=true   value=false
```

**行为是对的**：`retryable` 的**值**是 `false`。问题出在测试断言：

```text
test/qa/money_faults_test.go:72-74
  if _, retryable := res.Structured["retryable"]; retryable {
      t.Fatal(...)
  }
```

`map[key]` 的第二个返回值是「键是否存在」，不是值。**键一存在就失败**，与值无关。

而 t9 的 U-30 正是按 ADR-0010「每个错误都携带 `Retryable`，调用方不应从键缺席推断 false」
让该键**始终存在**——这是有意的契约改进，测试没跟上。

### 判定

**不是产品回归，是测试断言过时。** 处理方式：改断言为检查**值**，并要求改完后实测
「把 `Retryable` 设为 true 时测试确实失败」，确保它仍能抓到真回归。

这条值得记：**「测试红了」不等于「代码坏了」。** 如果一个测试断言的是「键必须缺席」，而契约
改成「键必须存在」，测试会红而行为是对的。**不查实际值就回退代码，会把一个契约改进当成回归修掉。**

## 裁决：U-70 的修法必须连带修对账器（t18）

t18 按我给的「记当时已知的事实」方向改成了 `entity_type=customer`，然后用一个探针发现
**次生问题比原缺陷更严重**：

```text
ObserveOperation(ctx, "magento.order.create", "customer", "7") -> applied
```

原因：`client.go:873-900` 的 `case "order"` 里有一句专为 U-70 写的守卫——order.create 记的是
客户 id，所以读 `/orders/{id}` 会取到无关订单，返回 `ObservationInconclusive`。改成 customer 之后
这句不再命中，落到 `case "customer"` → `GET /customers/7` 200 → **applied**。

后果：任何一次其实没下成的 ambiguous 下单，会在 1 分钟后被对账器**自动判成 succeeded**。

### 对比两个缺陷的性质

| | 原缺陷（U-70） | 次生问题 |
|---|---|---|
| 谁做错判断 | 操作员 | **系统自己** |
| 何时 | 人工去查上游时 | 1 分钟后自动 |
| 后果 | 可能 disposition 错 | 未下单被标记为已下单 |

**后者更严重**，因为它不需要人参与，而且结果是「确定成功」。

### 裁决：批准 A

允许在 `case "customer"` 开头加同一条守卫（4 行），并收敛 `case "order"` 里已成死代码的那段
（保留注释说明原因）。

**同时要求测试双向**：
- `order.create` 的 customer 实体 → 必须 `ObservationInconclusive`
- **其他** operation 的 customer 分支（如 customer.update）→ 必须仍能正常判定

否则守卫可能被写成「customer 分支一律 inconclusive」，那是引入另一个 bug。

### 历史 11 条的处置规则（采纳 t18 的建议）

**不要 requeue。** 它们 `entity_type=order` 而 id 是客户 id，requeue 会让对账器走 order 分支读
`/orders/5`——读到别人的订单。正确做法是人工逐条判定后 `dispose`，note 里写明依据
（「该 customer 在 created_at 窗口内、quote_id=该 cart」）。

**这条值得记**：修一个「记录语义错」的缺陷，可能让**依赖那个错误语义的守卫失效**。t18 没有
改完就走，而是加探针验证了改动对下游的影响——这正是我们应该对所有「改数据语义」的修复做的。
