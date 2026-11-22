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

## 亲验：t20 的两条 P0 —— 一条是「修复了但用不到」，一条是「隐私承诺已不成立」

t20 盘点出 20 条说明面缺口。其中两条我自己核了，都成立，而且比报告描述的更严重。

### GG-01：U-11 的运费修复对 agent **完全不可达**

**实测（生产 v0.11.7）**：

```text
magento.creditmemo.create {"order_id":41, "items":[...], "shipping_amount":0}
→ invalid_argument: validating root: unexpected additional properties ["shipping_amount"]
```

原因：`shipping_amount` 加进了 Go 结构体（`order_documents_write.go:64`），但 **`creditmemoCreateSchema`
是手写的字符串**（`write_tools.go:1085`），有 `"additionalProperties": false`，没加这个字段。

**后果比一个字段没生效更严重**：我在 v0.11.7 的发布说明里写了「It now takes `shipping_amount` and
refunds remaining shipping only when the last units go」。**这句话是假的**——参数在 schema 层就被拒绝，
根本到不了 Go 结构体。

这是本轮最值得记的教训：**Go 结构体改了不等于工具契约改了。** 这个项目的工具 schema 有两套真相——
结构体上的 `jsonschema` tag（用于输出）与手写的 schema 字符串（用于输入校验）。修复轮只改了前者。

### GG-02：隐私承诺已经不成立

`OrderAddress` 包含 `Firstname`/`Lastname`/`Street`/`City`/`Postcode`/`Telephone`/`Email`，
`Order` 含 `ShippingAddress`/`BillingAddress`——**订单读取返回完整地址与邮箱**。

但三处都说不是：

| 位置 | 原文 |
|---|---|
| `tools.go:323`（工具描述） | "Customer email, addresses, and payment data are not returned" |
| `order.go:74-77`（结构体注释） | "Customer email, addresses, and payment details are intentionally absent … keeps the tool free of personal data" |
| `README.md:38-39` | "PII stays inside Magento … order reads omit email, addresses, and payment data" |

这大概率是 U-31（客服岗位报「order.get 缺地址，无法确认发货」）修复时**有意加上地址**，但没人回头改
那三处声明。

**为什么这条比普通文档滞后严重**：隐私声明是**承诺**，不是描述。用户会据此判断能不能把网关放在
PII 敏感环境里。一条不成立的隐私承诺比没有承诺更危险——它让人放心地做了一个本不该做的决定。

修法必须二选一并写清理由：要么去掉地址（牺牲客服可用性），要么**如实声明地址会被返回**并说明理由
（支持需要它确认发货）。不能只改一处留两处矛盾。

## 亲验：t19 审查的两条 medium —— 都成立

t19 判 t9 交付 **needs_revision**。我核了它最重的两条。

### F2：U-31 的「收货地址」是永远为空的死字段 —— CONFIRMED

**代码证据（决定性）**：

```text
grep UnmarshalJSON internal/magento/order.go   → 零命中
Order.ShippingAddress 只有顶层 tag: json:"shipping_address"
```

而 Magento 的订单资源**不返回**顶层 `shipping_address`，它在
`extension_attributes.shipping_assignments[0].shipping.address`。没有自定义 Unmarshal，这个字段
**永远 nil**。

所以客服「复述收货地址」这个场景，在 U-31 被标记为「已修复」之后**仍然做不到**——只有账单地址可用。

**这条值得记**：字段存在、类型正确、编译通过、测试通过，但**永远是空值**。这比字段缺失更难发现，
因为 schema 里有它，agent 会以为能拿到。

### U-69 的 2500→2000 是契约修正，不是放宽断言 —— CONFIRMED（接受 t19 的判定）

t19 用三条独立证据判定：把 `creditmemoTotal` 改回行小计 → 测试 FAIL；2000 可由输入独立复算
（25+0+10−15）；生产实测账本 `amount_minor=3500` 与 Magento `grand_total=35.00` 一致（旧实现记 3400）。

**我接受这个判定。** 这也回答了我当初把这条改动交给独立审查的理由——**「改既有断言」必须由不是
作者的人判断**，作者说「这是契约修正」不能自证。

## 裁决：GG-02 选 A（保留地址）

t27 的 GG-02 要求实现者二选一：保留地址并改声明，或删地址保留声明。

**我直接裁决为 A**，理由是 t30 正在做的 F2 恰恰是**让地址真正可用**。如果选 B 删字段，两个任务
互相抵消。

这也说明一个协调原则：**两个任务在同一文件上的语义必须由队长先裁决，不能留给两个 agent 各自判断。**
否则它们会各自做出合理但相反的选择，然后都在报告里说「已完成」。

## t27 交付核验：它自己发现「只改 schema 不够」

我确认 t27 的生成产物与端到端接线，并记下它这次最重要的判断。

### GG-01 的完整修复链有三段，不是一段

我最初给 t27 的指示只说了「schema 里加 `shipping_amount`」。**它没有照做就交差**，而是发现了第二、
第三段：

```text
1. schema（write_tools.go:1085）        ← 我指出的
2. 工具输入结构体 creditmemoInput      ← 原本没有该字段
3. handler 传参                        ← 原本没往下传
```

只改第 1 段，schema 会接受参数，然后**在结构体处静默丢弃**——测试会过，功能仍不可用，而且比
`invalid_argument` 更难发现（不再报错，只是不生效）。

它用指针类型 `*float64` 区分「省略」与「0」，因为这两者语义不同（省略=默认不退、0=明确不退）。
端到端实测：传 4 → 上游 `arguments.shipping_amount=4`；不传 → 上游显式 0。

**这条值得单独记**：我给的指示是不完整的，而它把链路补全了。如果它严格照我的指示做，会交付一个
「测试通过但功能仍不可用」的修复——正是本轮反复出现的那个失败模式。

### 生成产物 `docs/implemented-tools.md`

它重新生成了该文件（描述改动后 `TestGeneratedDocumentIsCurrent` 必然失败），但没列进 changedPaths
因为不在 inScope。**我核验并确认**：重跑 `go run ./scripts/gen-implemented-tools -write` 无差异，
即工作树里的文件就是生成器的输出。这是正确的处理——生成产物必须跟着描述走，否则测试会红。

### 并发协调结果：两处改动共存

`order.go` 被 t27（隐私注释）与 t30（F2 解包）同时改。我要求双方「只做各自区域的最小编辑、写前重读」，
结果**两处都完整保留**：

- `order.go:74-80`：隐私注释已改为如实描述（含「payment data 永不投影」）
- `order.go`：`fillShippingAddressFromAssignments` 6 处引用，F2 解包逻辑在位

三处声明现在一致且属实：`tools.go:321-325` 工具描述、`order.go:74-80` 结构体注释、
`README.md:38-42`。

## 裁决：接受「配方迁出常驻」的取舍，但常驻版必须是清单而非口号

t21 提出一个需要产品决策的问题：把 instructions 里的商品复制配方（B/C 段，2,498 字符 = 79% 预算）
迁到 prompt/skill，让常驻空间留给**写操作契约**与读语义。代价是「从不加载 prompt/skill 的一次性
agent」复制商品时会少步骤清单。

### 我的裁决：接受迁移，但有条件

**理由一：占用比例失衡。** 2,498 字符给**一个任务**（复制商品），而写操作契约适用于**每一次写**。
用 79% 的常驻预算服务低频任务，等于让高频高风险场景没有空间。

**理由二：红线与步骤是两回事。** 安全关键的是「先发现再写」「不擅自新建分类/属性」「复制完成的标准是
类型/选项/属性值一致」——这些留在常驻。迁走的是**具体调哪些工具、按什么顺序**，那在 agent 遵循
「先发现」时是可以自己推导的。

**但——条件**：常驻版**不能只写「先发现再写」**。那是口号，agent 读到它仍然不知道该发现什么。
常驻版必须是一份**清单**，明确点名要发现的东西：

```text
复制商品前先读源站并记录：类型（simple/configurable/grouped/bundle/downloadable/virtual）、
每个可配置选项及其取值、完整属性集、所属分类。再与目标站已有内容比对。
不要在未获人工同意时新建分类、属性、选项值或属性集。
复制完成的标准是类型、选项、属性值都与源一致；无法复现的部分要说明，不要交一个更简单的商品。
```

这大约 200 字符，不是 2,498。**它足以防止最严重的伤害（建出一个错的商品），只是不提供逐步操作手册。**
一次性 agent 得到的是「知道该查什么但需要自己找路」，而不是「什么都不知道」。

**理由三：已有两道补充防线。** `configurable.create` 自带 staged 警告；`product.create` 描述指向
`product_copy` prompt。三层叠加后，迁移的风险是可接受的。

### 关于「突破 3,200 硬上限」

**不接受。** 硬上限存在的意义就是防止常驻内容无限膨胀——一旦为某个场景破例，下一次会为另一个场景
再破一次。要在预算内做取舍，这正是设计该做的事。

## 另立 CODE 任务的决定

t21 指出 GG-08（报表缺 `canceled_orders`/`refunded_total`）、GG-09（`attributes` 未拒绝保留字段）、
GG-11（`bulk_create` 缺显式 `skipped`）是**行为变更**，不是说明面问题。设计里对每条写了
「描述先说实话，行为另立项」。

**我采纳这个切分**：本目标的说明任务只做「如实描述现状」，行为变更另立任务。理由是用户的目标是
「完善 mcp 缺失的**通用内容**，再生成 skill」——先把 agent 能读到的信息变准确，再谈改行为。

F2（`shipping_address` 解包）**已经**在 t30 里由 integrator 处理，不重复立项。

## 并发事故：测试被整文件重写冲掉，而门禁仍是绿的

t30 报告了一件必须记下来的事：store-operator 在 15:04:35 **整文件重写** `order_test.go`，
把 integrator 先追加的 3 条测试冲掉了。**当时全量门禁仍然是绿的**——因为那 3 条测试已经不在文件里了。

integrator 靠「最后核对测试名」才发现，重新追加后确认在位。我复核了全部 10 条 F1–F7 测试，
现在都在。

### 为什么这条比普通冲突严重

普通的并发冲突会**变红**：两个 agent 改同一行，后写覆盖先写，编译或测试失败，有人会看见。

**这个不会变红。** 一方追加的测试被另一方整文件重写时，测试文件本身仍然是合法的 Go 代码，
只是少了几条函数。门禁跑的是「现存的测试」，所以全绿。**门禁只能证明「存在的测试通过」，
不能证明「该存在的测试还在」。**

### 我之前的做法不够

我给双方发过协调指令（「只做各自区域的最小编辑、写前重读」）。这防住了 `order.go` 的注释与解包
逻辑共存，**但没防住 `order_test.go` 被整文件重写**——因为追加测试在 agent 看来正是「最小编辑」，
而它读到的文件版本里还没有对方刚追加的内容。

### 应记入协作约定

1. **测试文件按人分文件**，不要多人追加同一个 `_test.go`
2. 若必须共用，**改完必须核对「测试名清单」而非只看门禁绿**
3. 更根本的：**门禁通过 ≠ 交付完整**。这一轮已经出现过两次同类问题——上一次是「测试通过了，
   但测的不是那条路径」（t63 用读工具测 ledger 路径）。这次是「测试通过了，但测试已经不存在」。

两次的共同点：**绿色的信号被当成了正确性的证明，而它只证明了它实际检查的东西。**

## 静默覆盖事故第三次：这次冲掉的是测试加固本身

t37 报告：它在 attempt 1 改过 `server_test.go` 的交叉引用检查，**被某次整文件重写冲掉**——当时
`git status` 干净、HEAD 里也没有。所以本轮重做了一遍。

这是本轮**第三次**同类事故：

| # | 被冲掉的东西 | 怎么发现的 |
|---|---|---|
| 1 | integrator 追加的 3 条 order 测试 | 人工核对测试名 |
| 2 | U-01 的回归测试（33 行） | t29 审查时用 `check-test-inventory.sh` 抓到 |
| 3 | t37 的交叉引用检查改动 | 它自己发现要重做 |

第 2 次之后我们做了 `check-test-inventory.sh`。**它能抓到「测试函数消失」，但抓不到「测试函数内容被改回旧版」**——第 3 次是后者：函数还在，内容被覆盖。

### 结论

测试清单脚本只解决了一半问题。**同一文件被两个 agent 写，任何一侧的改动都可能静默消失，而两种检查（门禁绿、清单齐全）都不会报警。**

唯一可靠的防护仍然是**一个文件一个写入者**。本轮我做过三次这样的裁决（order.go、tools.go、write_tools.go），但 `server_test.go` 没有受到同样保护——因为 t23 和 t37 的 inScope 里它只出现在一侧，另一侧是「测试文件也算」。

### 应记入协作约定（补充）

> 共享的 `_test.go` 也必须按文件分配，不能因为「只是加测试」就认为低风险。
> 加测试的改动被覆盖时，**门禁不会变红**——因为覆盖后的文件仍然是合法、能通过的 Go 代码。

## t37 的契约范围问题（我接受，记录在案）

t37 报：契约 inScope 列的是 `server.go`，没有 `server_test.go`，而它被指定的改动就在 `server_test.go` 里。所以它只能把 changedPaths 填成 inScope 内的文件，**台账与实际工作树改动对不上**。

**这是我的契约错误**——我写 inScope 时漏了测试文件。t37 没有擅自填错，而是明确报告了这个不一致，这是对的。

由于 t37 已完成（任务不可再 amend），我在此记录：**实际改动是 `internal/mcp/server/server_test.go`**，已随 `b9335e9` 提交。

## 第五次「信号不可信」：`go test -race ./...` 会随机失败

清完 lint 债务后我跑 `local-ci.sh`，它在 `go test -race` 段失败。逐层查下去，发现的问题比 lint 债务严重得多。

### 现象（实测）

```text
go test -race -count=1 ./internal/storage/   连跑 3 次 → 0 / 12 / 0 个失败
go test -race -count=1 ./...                 连跑 3 次 → 20 / 0 / 1 个失败
```

**单包自己就会 flaky**，不是跨包干扰。典型失败：

```text
cleanup operations: ERROR: update or delete on table "operations" violates
  foreign key constraint "operation_transitions_operation_id_fkey"
```

### 根因（初步判断，已派任务验证）

`operation_ledger_test.go` 的 `ledgerFixture` 清理是两条**独立** `DELETE`：

```go
DELETE FROM operation_transitions
DELETE FROM operations          ← 两者之间若有新 transition 插入，就 FK 冲突
```

而注释写着「these tests must own the whole table」——它假设独占，但可能有对账器 goroutine 仍在写入。

### 为什么这比 lint 债务严重

`go test -race ./...` 是**发布工作流 verify 阶段的命令**（`release.yml:46`）。flaky 的后果：

| 后果 | 说明 |
|---|---|
| **发布可能随机失败** | v0.11.8 通过是运气，不是保证 |
| **红也不可信** | 真回归与 flake 分不开 |
| **人们开始重跑直到绿** | 这是门禁失效的最终形态 |

### 这是本轮第五次同一类问题

| # | 信号 | 它没有证明的 |
|---|---|---|
| 1 | 测试通过 | 测的不是那条路径 |
| 2 | 门禁绿 | 该存在的测试已不在 |
| 3 | 结构体改了 | 参数到不了结构体 |
| 4 | lint 报 73 条 | 真实是 101 条（默认上限截断） |
| 5 | **测试绿** | **它可能只是这一次绿** |

第 4 条是我自己的错误：t41 与 t40 都报告了「真实数量比默认输出多」，我才发现我量的是截断视图。已修正 `docs/audit/lint-debt.md`。

**这五次的共同点**：我把一个信号当成了它没有承诺的东西。信号本身没错，错的是我读它的方式。

## t44 找到了我没猜到的第二个根因：我自己是干扰源

我在 t44 里判断根因是「清理不是原子的」——**这条成立，但不完整**。

t44 用「两个并发进程」100% 复现了报错，发现第二个原因：

> `testSchemaDSN(t, "test_storage")` 让**两个并发 go test 进程共享同一套表**。

也就是说，「单包自己 flaky（0/12/0）」不是包内时序问题，而是**运行重叠**——本机有 7 个 agent 同时在跑测试，加上我自己也在跑。

**我一直在怀疑别人，而我本身就是并发源之一。** 我跑 `go test -race ./...` 验证时，agent 们也在跑；我们共享 `test_storage`，互相删对方的行。那几次「20 个失败」里有相当一部分是我造成的。

### 两个修法

| 修法 | 解决什么 |
|---|---|
| `TRUNCATE ... CASCADE` 替代两条独立 DELETE | 清理原子化（我猜到的） |
| schema 加 pid：`test_storage_<pid>` + `TestMain` 里 DROP | **并发进程共享 schema**（我漏掉的） |

外加它自己发现的第三条：`TestJobQueueClaimDoesNotWaitOnOtherQueues` 里有一个**没 join 的 goroutine**（1200ms 后提交事务），测试返回后它还在写 jobs 表——邻居用例变抖的原因之一。

### 验证方式值得记

它没有只跑一次：

- 修复前：**两个并发进程** → 两边都 FAIL，报 FK 23503
- 修复后：两个并发进程 → 两边 exit 0
- 单包 10 次、全仓 5 次 → 全绿

**「并发复现」比「连跑 N 次」更可靠**——它把偶发变成必然，一次就能证明修好了。

### 结果

`local-ci.sh` **第一次完整通过**（`local CI passed`，exit 0）。这个门禁自 2026-11-21 起就没绿过：先是 skip 门禁把包级记录当跳过测试，然后卡在 101 条 lint 债务，最后是 flaky 套件。三层问题叠在一起，任何一层不修，后面两层都看不见。

### 遗留代价（可接受）

进程被 kill 时可能残留一个 `test_storage_<pid>` schema。正常退出会 drop。我验证了当前本地库与生产库都**没有残留**（`test_%` 为 0 行）。

我接受这个代价：**偶发残留一个 schema 比随机红一个门禁轻得多**。若将来觉得碍事，可以在 CI 启动前加一句清理。
