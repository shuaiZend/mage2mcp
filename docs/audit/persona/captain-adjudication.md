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
