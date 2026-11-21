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
