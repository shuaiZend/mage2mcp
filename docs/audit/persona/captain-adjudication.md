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
