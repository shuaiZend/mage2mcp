# 队长独立核验记录

对成员报告的**关键 P1 发现**做独立复现。不是替代 t60 的汇总，而是提前给汇总阶段一份已验证的证据。

## 已核验：SEM-SEC-02 审批审计行永远写不进去（CONFIRMED）

**来源**：t58（mcp-engineer）。

**声明**：审批成功，但审计行被数据库约束拒绝，因此每次审批都没有审计记录。

**我的独立核验**（未使用 t58 的产物，直接查代码与数据库）：

代码侧：

```text
cmd/admin/main.go:174   Decision: "approve"
cmd/admin/main.go:175   Phase:    "approval"
```

约束侧：

```text
internal/storage/migrations/000001_init.up.sql:70
  decision text NOT NULL CHECK (decision IN ('allow','deny','confirmation_required','error'))
internal/storage/migrations/000007_audit_pending_decision.up.sql:9
  CHECK (decision IN ('allow','deny','confirmation_required','error','pending'))
internal/storage/migrations/000001_init.up.sql:71
  phase text NOT NULL CHECK (phase IN ('intent','outcome'))
```

`approve` 不在 decision 集合内，`approval` 不在 phase 集合内——**同一行同时违反两个约束**。

数据库实测（对本地 PostgreSQL 直接插入该行）：

```text
ERROR:  new row for relation "audit_events" violates check constraint "audit_events_decision_check"
DETAIL:  Failing row contains (..., 'approve', 'approval', 'approved', ...)
```

**结论：CONFIRMED，P1。** 每次审批的审计行都写不进去，生产上审批没有审计轨迹。

**修复方向**：新增迁移把 `approve` 加入 decision 集合、`approval` 加入 phase 集合。审计表是只追加记录，审批是合法事件类型——**扩展约束比改写事件值更诚实**，因为把审批记成 `allow` 会丢失「这是审批动作」的语义。

**顺带核验**：代码里另有 `Decision: "proceed"` 与 `Decision: "unknown"`，但两者都出现在 `internal/mcp/server/server_test.go` 的测试替身里（`OperationTicket.Decision`，与审计 decision 是不同类型），**不是审计值，无缺陷**。

## 待核验（等其余报告）

- SEM-SEC-01 实例隔离（DefaultPolicy Instance 为空）
- SEM-SEC-03 自记账工具在错误结果下审计记 allow
- SEM-SEC-04 限流误报为超时
- SEM-DOC-01..04 文档与代码状态不符

## 已核验：部分退款被放大成全额（SEM-SALES-01，P0，CONFIRMED）

**来源**：t54（security-auditor）。我独立复现，并**发现范围比报告更大**。

### 代码与规范的对比

Magento 规范（店铺自身 schema，`/rest/pl/schema?services=all`）：

```text
POST /V1/order/{orderId}/invoice  body props: capture, items, notify, appendComment, comment, arguments
POST /V1/order/{orderId}/ship     body props: items, notify, appendComment, comment, tracks, packages, arguments
POST /V1/order/{orderId}/refund   body props: items, notify, appendComment, comment, arguments
```

三个路由的参数都在**顶层**。

网关实际发送：

```text
internal/magento/invoice_write.go:155      invoiceOrderBody{Capture, Items}            → 顶层，正确
internal/magento/order_documents_write.go:348  shipmentCreateBody{Entity ...}  `json:"entity"`  → 错误
internal/magento/order_documents_write.go:366  creditmemoCreateBody{Entity ...} `json:"entity"`  → 错误
```

**发票是对的，发货与退款都包错了。** Magento 忽略未知的 `entity` 字段，于是收到空 `items`，按默认行为处理整单。

### 实测复现（生产 v0.11.5，demo 站）

**退款**：订单 6，2 件，发票 78 PLN。请求退 1 件：

```text
请求: items=[{order_item_id:8, qty:1}]  （1 件 = 34 + 运费 10 = 应为 44）
返回: grand_total=78, subtotal=68, shipping_amount=10, total_qty=0
```

**实际退了整张发票 78，多退 34 PLN。**

**发货**：订单 7，2 件。请求只发 1 件：

```text
请求: items=[{order_item_id:9, qty:1}]
返回: total_qty=2
直连核对: shipment 3 total_qty=2, line qty=2
```

**实际发了整单 2 件。**

### 结论

**两条 P0，同一根因**：`/order/{id}/ship` 与 `/order/{id}/refund` 的请求体多包了一层 `entity`。

- 退款：**多退钱**，且发票余额被一次耗尽，后续真实部分退款会因「0 refundable」被拒
- 发货：**整单发出**，客户为未发出的商品拿到物流信息，剩余数量无法再发

`total_qty: 0` 也是错的（实际 2 件），属同一缺陷的次生现象（SEM-SALES-08）。

### 修复方向

把两个 body 结构从 `{Entity: ...}` 改为顶层字段，与 `invoiceOrderBody` 一致。修复后必须用「订单 2 件、只退/只发 1 件」的用例验证，而不是只验证「能成功」。

## 已核验：SEM-SALES-03 是误报（NOT-A-DEFECT）

t54 报告称「order.create 在 PL 店铺必然失败，因为地址模型无 region_id」。

代码事实：`internal/magento/order_create.go:53` 有 `RegionID int \`json:"region_id,omitempty"\``，下单地址**支持** region_id。

我实测下单两次均成功（订单 6 与 7，PL 店铺，region_id=997）。**该发现不成立**——是测试时未传 region_id，不是模型缺失。

真正的缺陷是：**不传 region_id 时错误信息只说「verify store_code and field values」**，不告诉调用方缺的是 region_id（这与 SEM-SALES-04 的「400 上游文本被丢弃」是同一个根因）。

## 已核验：SEM-CUS-04 部分不成立（降级为 P3）

**来源**：t56（qa-engineer）。报告称 `addresses.delete` 返回 `{"deleted":true}` 但**地址没删**，属「破坏性操作谎报成功」（P1）。

**我的复现**：

1. 用网关 `customer.addresses.create` 给客户 1 建地址 → 得到 id 8
2. 用网关 `customer.addresses.delete` 删 id 8 → 返回 `{"address_id":8,"deleted":true}`
3. **直连核对** `GET /customers/1` → 剩余地址 `[1]`，**地址 8 已不存在**

**结论：删除是真实生效的，P1 的核心指控不成立。**

但报告的另一半成立：

1. 再次删除已不存在的 id 8 → 仍返回 `{"deleted":true}`

**所以真正的缺陷是**：删除一个不存在的地址也报成功（幂等式成功），而不是 `not_found`。这会让调用方无法区分「删掉了」与「本来就没有」。**降级为 P3 诚实性问题**，不是 P1 数据未删。

判断依据的差异可能来自测试对象不同（我用的是刚创建的地址，报告可能用了别的 id 或遇到瞬时故障），但结论以可复现的证据为准。

## 核验方法说明

以上每条都用「网关调用 + 直连 REST 核对」两步：只信工具自己的返回不足以判断真伪——`addresses.delete` 这一例正好说明，工具返回 `deleted:true` 时数据可能真删了也可能没删，必须回读确认。
