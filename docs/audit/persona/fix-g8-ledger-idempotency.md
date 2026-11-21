# G8 修复：账本与幂等（t13）

任务 t13 / attempt `9008c721-57f9-4743-8c32-522c4ce8d5df`。
范围按队长裁决 **B** 执行：`internal/magento/operations.go`、`internal/magento/product_bulk.go`、
`internal/storage/operation_ledger.go` 及对应测试。**工具注册未接线**（`tools.go` 归 t14），
接线片段见 §4，可直接照抄。

## 1. 一句话结论

四条能在本范围内闭合的都闭合了，并且每条都有"回退即失败"的测试：

| U 编号 | 原问题 | 现状 |
|---|---|---|
| U-71 | 非 money 写工具的幂等重放在 `structuredContent` 里没有 `replayed` | 台账在 replay 时把 `replayed: true` 注入**记录下来的结果**，所有写工具（含 `bulk_update` / `options.bulk_create`）一次到位；`BulkUpdateResult` 也补了字段 |
| U-74 | `operation_unknown` 让调用方"poll"，但 102 个工具里没有可 poll 的东西 | 台账新增 `List`/`Dispose`，`operations.go` 新增 `operation.get` / `operation.list` / `operation.dispose` 的**可注册构造函数**；生产 11 条滞留行可查、可处置 |
| U-72 | 省略 `idempotency_key` 时同参数被**永久**去重（ERP 静默丢单） | 派生 key 只在 `DerivedKeyWindow`（5 分钟）内去重；跨窗口是**新操作** |
| U-73（派生 key 部分） | 一次失败后同参数重试被拒（"operation previously failed"），本地校验失败也占住 key | 派生 key 的行进入 `failed` 时**释放 key**（重命名为 `…:released:<fence8>`，行仍在，审计不丢）；显式 key 保持 exactly-once 不变 |
| U-69 / U-70 / U-73（comment 不入哈希） | 金额口径、`entity_id` 语义、`creditmemoArgs` 不含 comment | **不在本任务范围**：前两者在 `server.go` / `order_documents_write.go`，见 §5 残留依赖 |

## 2. 改了哪些文件

| 文件 | 改动 |
|---|---|
| `internal/storage/operation_ledger.go` | `markReplayed`（replay 注入）、`DerivedKeyWindow`/`windowedDerivedKey`/`ledgerClock`、`releasedKeySQL`（失败释放派生 key）、`OperationFilter`/`OperationPage`/`List`（keyset 分页 + 诚实 total/truncated）、`DisposeJudgment`/`Dispose`（三态处置 + 强制 note）、`OperationRecord` 补 `ReconcileAfter`/`NeedsAttention`/`CreatedAt` 与 schema 描述、`Get` 与 `List` 共用一份投影（`operationRecordColumns`/`scanOperationRecord`）、`Escalate` 注释与实现对齐 |
| `internal/magento/operations.go` | `OperationLedger` 接口加 `List`/`Dispose`；`Operations.Get/List/Dispose`；`operation.get`/`operation.list`/`operation.dispose` 三个 `registry.Tool` 构造函数（含 schema、输入输出类型、`operationNextStep` 提示）；`callerIdentityClass` |
| `internal/magento/product_bulk.go` | `BulkUpdateResult.Replayed` 字段（INT-02 要求"与 money 工具一致"） |
| `internal/storage/operation_ledger_test.go` | 新增 9 个用例（见 §3） |
| `internal/magento/operations_test.go` | `fakeLedger` 补齐 `List`/`Dispose`；新增 4 个用例 |
| `internal/magento/product_bulk_test.go` | 新增 `TestBulkUpdateResultCarriesTheReplayMarker` |

未改动：`product_write.go`、`order_documents_write.go`、`tier_prices.go`、`sales_analysis.go`、
`order_geography.go`、`import.go`、`server.go`、`confirm.go`、`tools.go`、`write_tools.go`。

## 3. 每条修复的"回退即失败"测试

| 修复 | 测试 | 回退后为什么红 |
|---|---|---|
| U-71 replay 标记 | `TestReplayResultCarriesTheReplayedMarker`（storage） | 去掉注入后 `replay.Result` 里没有 `replayed`，断言直接失败；同时断言"原 payload 必须存活"防止把结果覆盖成只有标记 |
| U-71 边界 | `TestReplayMarkerLeavesNonObjectResultsAlone` | 非对象结果（`null`/数组/标量）被改动即失败 |
| U-71 类型契约 | `TestBulkUpdateResultCarriesTheReplayMarker`（magento） | 删掉 `Replayed` 字段则编译失败；字段被写成恒真则"fresh 不得声称 replay"断言失败 |
| U-74 可查 | `TestListFindsOperationsThatNeedAttention`（storage） | 去掉 `needs_attention` 谓词，11 条式滞留行会被普通 unknown 淹没；断言"只返回 escalated 那条""total 是匹配数不是页大小""两页不重复" |
| U-74 可处置 | `TestDisposeRecordsTheJudgement`（applied / not_applied / requeue 三个子用例） | 去掉任一 judgment 的状态迁移、note 落库或 requeue 的 `reconcile_count=0`，对应子用例红；requeue 子用例还断言"对账器能重新 claim"，直接对应"不永久滞留" |
| U-74 边界 | `TestDisposeRefusesNonUnknownStates`、`TestDisposeRequiresJudgementAndNote` | 允许处置非 unknown / 允许空 note 即失败（这正是"不许当作没发生"的护栏） |
| U-74 工具面 | `TestOperationToolsExposePollingAndDisposition`（magento，内存 MCP 会话） | 三个构造函数没注册进去 / `next` 不指向处置工具 / dispose 不需要审批 / note 没进审计行 —— 任一处回退都红 |
| U-74 可注册性 | `TestOperationToolMetadataIsRegistrable` | 构造函数返回的 `registry.Tool` 过不了 `registry.Validate`（缺 capability、read 工具带 confirmation 等）即红 |
| U-74 失败关闭 | `TestOperationToolsFailClosedWithoutLedger` | 没台账时不是 `unsupported_capability` 而是 panic/成功即红 |
| U-72 窗口 | `TestDerivedKeyDeduplicatesOnlyInsideItsWindow` | 去掉窗口 → 第二个窗口的合法重复下单返回 `replay`，断言"必须是新操作""台账里必须有 2 行"失败（这就是 ERP 丢单的形状） |
| U-72 边界 | `TestExplicitKeyIsNotWindowed` | 把窗口错误地应用到显式 key 上即红（显式 key 的 exactly-once 是调用方的承诺） |
| U-73 释放 | `TestFailedAttemptReleasesDerivedKey` | 不释放 → 重试拿到"previously failed"而非 `proceed`，断言失败 |
| U-73 边界 | `TestExplicitKeyIsNotReleasedOnFailure` | 显式 key 被释放即红（会静默允许第二次写入） |

## 4. 接线片段（注册未接线，交给 t14 之后的接线任务）

三个工具**已经在 `operations.go` 里构造好并通过 `registry.Validate`**
（`TestOperationToolMetadataIsRegistrable` 保证），接线只是注册调用。四处改动：

**(1) `internal/magento/tools.go` → `registerReadTools`（当前第 151 行，`newFlatCategoriesTool(lookup),` 之后）**

```go
		newSalesAnalysisTool(lookup), newStoreConfigsTool(lookup), newFlatCategoriesTool(lookup),
		newOperationGetTool(), newOperationListTool(),
```

**(2) `internal/magento/write_tools.go` → `registerWriteTools` 的 `tools := []registry.Tool{…}`（当前第 329 行，`newProductOptionDeleteTool(lookup),` 之后）**

```go
		newProductOptionCreateTool(lookup), newProductOptionUpdateTool(lookup), newProductOptionDeleteTool(lookup),
		newOperationDisposeTool(),
```

> 必须放在 write 注册路径里：`dispose` 是 `PermissionWrite`，而 `cmd/server/main.go` 只在
> `cfg.Tools.Writes` 为真时调用 `RegisterWritesWithResolver`；放进 read 路径会让只读部署
> 启动时报 "disabled by tools.writes"。

**(3) `internal/magento/write_tools.go` → `writeToolSchemas()`（当前第 348 行的 map 内）**

```go
		operationDisposeToolName:       operationDisposeSchema,
```

**(4) `internal/magento/write_tools.go` → `writeDisclosures`（当前第 446 行的 map 内）**

```go
	"magento.operation.dispose": {mutates: "the operation ledger's state for one unknown operation", idempotency: "not idempotent; only an unknown operation can be disposed, so a second call returns conflict"},
```

> (4) 不是可选项：`TestWriteToolIdempotencyMatchesDisclosures` 会枚举所有已注册写工具，
> 缺条目直接 fail；而条目先于注册存在时，同一测试的反向检查也会 fail。所以 (2)(3)(4) 必须同批落地。

## 5. `operation_unknown` 的处置路径

### 5.1 语义：处置是"记录人的判断"，不是"当作没发生"

`operation_unknown` 的含义是**网关不知道上游做没做**。所以处置接口不接受"关掉它"这种动作，
只接受三个必须带 `note` 的判断：

| judgment | 含义 | 台账结果 |
|---|---|---|
| `applied` | 人读了上游，变更**在** | `state=succeeded`、`error_code=manual_applied`、`upstream_ref` 记录、`reconcile_after=NULL`；之后同 key 的重放会返回它而不是再写一次 |
| `not_applied` | 人读了上游，变更**不在** | `state=failed`、`error_code=manual_not_applied`；派生 key 同时释放，允许安全重试 |
| `requeue` | 还没查清 | `state=unknown`、`reconcile_count=0`、`reconcile_after=now()`；对账器下一轮重新读上游（**"不永久滞留"的实现点**） |

`note` 与判断一起写进 `operation_transitions.reason`（`disposed_applied: <note>`），
并写一条 `audit_events`（`scope=dispose`，`identity_class` 取真实调用者类别，不再硬编码）。
`TestDisposeRequiresJudgementAndNote` 保证空 note / 非法 judgment 不会动行。

### 5.2 生产那 11 条：可查

只读查询（就是 `List` 生成的 `needs_attention` 谓词），生产库 `mage2mcp-postgres-1`：

```bash
ssh ubuntu@49.51.250.83 'cd /opt/mage2mcp && sudo docker compose exec -T postgres psql -U mage2mcp -d mage2mcp -c "SELECT count(*) AS needs_attention FROM operations WHERE TRUE AND (state = '"'"'unknown'"'"' AND (COALESCE(error_code, '"'"''"'"') = '"'"'needs_manual_reconciliation'"'"' OR reconcile_after IS NULL OR reconcile_after = '"'"'infinity'"'"'));"'
```

```text
 needs_attention
-----------------
              11
```

第二条（同一个谓词 + `List` 的投影，含 `reconcile_never` 与 `LIMIT 12`）：

```bash
ssh ubuntu@49.51.250.83 'cd /opt/mage2mcp && sudo docker compose exec -T postgres psql -U mage2mcp -d mage2mcp -c "SELECT id::text, operation, state, error_code, reconcile_count, CASE WHEN reconcile_after = '"'"'infinity'"'"' THEN NULL ELSE reconcile_after END AS reconcile_after, COALESCE(reconcile_after = '"'"'infinity'"'"', false) AS reconcile_never, created_at FROM operations WHERE TRUE AND (state = '"'"'unknown'"'"' AND (COALESCE(error_code, '"'"''"'"') = '"'"'needs_manual_reconciliation'"'"' OR reconcile_after IS NULL OR reconcile_after = '"'"'infinity'"'"')) ORDER BY created_at DESC, id DESC LIMIT 12;"'
```

```text
                  id                  |      operation       |  state  |         error_code          | reconcile_count | reconcile_after | reconcile_never |          created_at
--------------------------------------+----------------------+---------+-----------------------------+-----------------+-----------------+-----------------+-------------------------------
 46b1322a-2c8d-4c93-8151-fc5176a05325 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-21 07:53:25.101946+00
 f1c4bb88-d916-49ac-8ed7-8001025d4ee9 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-21 07:19:49.204027+00
 52bf1889-c6ca-4b13-a604-bff5ab44c868 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-21 06:14:33.253448+00
 a878332e-90cb-445d-aa3c-3c872dd42d62 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-21 06:07:27.602495+00
 0685e5ea-b7f9-4768-8cdf-dd212b417070 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-21 06:00:50.470134+00
 aa3fca27-ade6-4f06-93af-ff463beb5c2f | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 20:35:53.180975+00
 205bb927-4912-4187-a109-eb9037212333 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 20:29:07.445815+00
 3b436b34-4d03-4425-86b8-7aec72ad7e8f | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 20:21:28.354778+00
 7654408c-cfc5-4685-8d4f-4f0821f1618a | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 18:16:09.174155+00
 6dd09077-44d9-412f-a480-0b857cd54e11 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 17:49:34.90824+00
 13380e5e-5dc4-4bbf-9122-bb87a63c9865 | magento.order.create | unknown | needs_manual_reconciliation |               9 |                 | t               | 2026-11-20 17:33:30.797593+00
(11 rows)
```

`reconcile_after` 为 `infinity` 时 `List` 会把它投影成 `null` 并置 `needs_attention=true`
（`reconcile_never=t` 就是那个投影列）——"永远不会再看"不会伪装成一个日期。

### 5.3 处置路径跑通（本地库，`127.0.0.1:55440`，未动生产）

用一个临时转录用例（跑完即删）在真实 PostgreSQL 上走完整条路径：

```text
=== A. the state the 11 production order.create rows are in
escalated unknown        state=unknown   error_code="needs_manual_reconciliation" needs_attention=true  reconcile_after=never  upstream_ref=""
operation.list           total=1 rows=1 (the row is now findable)
=== B. disposition: requeue (finding not in yet)
after requeue            state=unknown   error_code=""   needs_attention=false reconcile_after=2026-11-22T03:49:58+08:00 upstream_ref=""
reconciler               claimed again, reconcile_count=1 (the budget restarted)
=== C. disposition: applied (operator read the upstream and found the order)
after applied            state=succeeded error_code="manual_applied" needs_attention=false reconcile_after=never upstream_ref="order:1042"
  none          -> pending       begin
  pending       -> executing     execute
  executing     -> unknown       unknown
  unknown       -> reconciling   reconcile_claim
  reconciling   -> unknown       escalated
  unknown       -> succeeded     disposed_applied: read /V1/orders/1042: the order exists
operation.list           total=0 (nothing is left needing a human)
```

操作员的实际用法（接线后）：

```bash
# 1) 找出所有需要人处理的行
... -tool=magento.operation.list -args='{"needs_attention":true,"limit":50}'
# 2) 看一条的完整状态与建议动作（next 字段直接说明该做什么）
... -tool=magento.operation.get -args='{"operation_id":"46b1322a-2c8d-4c93-8151-fc5176a05325"}'
# 3) 记录判断（dispose 是写工具，需要另一个身份审批）
... -tool=magento.operation.dispose -args='{"operation_id":"46b1322a-...","judgment":"applied","note":"read /V1/orders/1043: order exists","upstream_ref":"order:1043"}'
```

### 5.4 残留依赖（必须一起读）

- **U-70（`entity_id` 语义）不在本任务范围**，但它决定处置时"去上游查哪一单"：
  生产这 11 条的 `entity_id` 是 `2..7`，那是**客户 id**（`server.go: entityFor`），不是订单号。
  所以现在能查、能关、能重排，但操作员定位上游仍要结合原始请求/客户 id 人工推断。
  U-70 修好后（`order.create` 记订单号或至少记 customer+增量 id），这条路径才算闭环。
- **U-69（账本金额只算商品行）** 在 `order_documents_write.go`，同样不在本任务范围。
  它不影响 unknown 的处置，但影响处置时的金额核对。
- **注册未接线**：在接线任务完成前，`tools/list` 仍然是 102 个工具，上面的三个工具**不可调用**。
  这是按队长裁决 B 的刻意取舍，不是遗漏。

## 6. 验证

| 命令 | 结果 |
|---|---|
| `go test -count=1 ./internal/magento/ ./internal/storage/` | `ok magento 4.7s` / `ok storage 0.1s`（storage 的 DB 用例无 DSN 时 skip，与 CI 一致） |
| `go test -count=1 -race ./internal/magento/ ./internal/storage/` | `ok magento 13.0s` / `ok storage 1.3s` |
| `test -z "$(gofmt -l ./internal/magento ./internal/storage)"` | 无输出（clean） |
| `go vet ./internal/magento/ ./internal/storage/` | 无输出（clean） |
| **额外**：`MAGE2MCP_TEST_DATABASE_URL=postgres://mage2mcp:…@127.0.0.1:55440/mage2mcp?sslmode=disable go test -count=1 ./internal/storage/ ./cmd/worker/` | `ok storage 2.9s` / `ok cmd/worker 0.3s`（真正跑了 `operation_ledger_test.go` 的 25 条 + `operation_gate_test.go` 的 16 条 SQL 用例，含新增 9 条） |
| **额外**：`go build ./...` | 通过 |

新增用例逐个跑的结果（DB 版）：

```text
--- PASS: TestDerivedKeyDeduplicatesOnlyInsideItsWindow (0.04s)
--- PASS: TestExplicitKeyIsNotWindowed (0.03s)
--- PASS: TestFailedAttemptReleasesDerivedKey (0.03s)
--- PASS: TestExplicitKeyIsNotReleasedOnFailure (0.02s)
--- PASS: TestReplayResultCarriesTheReplayedMarker (0.02s)
--- PASS: TestReplayMarkerLeavesNonObjectResultsAlone (0.00s)
--- PASS: TestListFindsOperationsThatNeedAttention (0.02s)
--- PASS: TestDisposeRecordsTheJudgement (0.05s)
--- PASS: TestDisposeRequiresJudgementAndNote (0.00s)
--- PASS: TestDisposeRefusesNonUnknownStates (0.03s)
```

## 7. 设计取舍与风险（供 review）

1. **窗口长度 5 分钟**（`DerivedKeyWindow`）。取舍：窗口内是"重复提交保护"，窗口外是"新操作"。
   太短 → 丢响应后的重试会变成第二单；太长 → ERP 的补偿重推仍被吞。5 分钟覆盖了一次
   HTTP 超时 + 人工重试的典型间隔，同时远短于"客户再买一次"的间隔。
   它是变量，测试靠它把窗口变成一步而不是 sleep。
2. **窗口加在台账层而不是调用方**。`server.DeriveOperationKey` 与 `storage.DeriveKey` 必须
   逐字节一致（有契约测试），所以不能改推导公式；改成"插入时按窗口作用域"后，
   middleware 与自记账工具仍然落在同一行，契约测试不动。
3. **`auto:` 前缀成为保留前缀**：调用方若显式传一个 `auto:` 开头的 key，它也会被加窗口后缀。
   文档已在 `DeriveKey`/`windowedDerivedKey` 注明；正常调用方用 UUID。
4. **失败释放 key 只针对派生 key**，且是"重命名行"而不是删除：审计行、transition、结果全部保留，
   只有唯一键被让出。显式 key 失败后仍然 `DecisionRetry`（exactly-once 是调用方的承诺）。
5. **`Dispose` 只接受 `state=unknown`**：`reconciling` 由对账器租约持有，`pending/executing`
   由活着的尝试持有——对它们处置会破坏 fencing。`requeue` 的 `reconcile_count=0` 是刻意重置：
   否则 `reconcile_count(9) > maxReconcileAttempts(8)` 会让对账器下一轮直接再 escalate，
   "requeue" 变成空操作。
6. **`List` 用 keyset 分页**（`(created_at, id)` 降序 + 不透明 cursor），`total` 是过滤后的
   真实匹配数，`truncated` 只在还有下一页时为真——避免 G6/G8 反复出现的"截断不诚实"。