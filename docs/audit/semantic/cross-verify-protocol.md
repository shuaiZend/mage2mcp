# 交叉验证：MCP 协议语义与并发确认（cross-verify-protocol）

- **验证对象**：t63（MCP 服务器 7 条语义修复）与 t69（replay 并发回归修复）
- **被测代码**：当前工作树 HEAD `c445b34`（`fix: semantic-layer remediation across money, security, protocol, inventory and docs`），本地构建
- **验证方式**：**实际启动网关并发送真实 JSON-RPC 请求**，审计结论**逐条查 PostgreSQL 核对**；不是只读代码
- **日期**：2026-11-22（UTC 18:14–18:22）
- **只读验证**：未修改任何产品代码；仅新增本报告

## 环境

| 项目 | 值 |
|---|---|
| 网关（鉴权开启） | `http://127.0.0.1:18330/mcp`，`auth.enabled=true`，issuer `https://local.test`，audience `mage2mcp` |
| 网关（限流验证） | `http://127.0.0.1:18331/mcp`，`auth.enabled=false`，`limits: {requests_per_second: 0.01, burst: 1, max_concurrent: 1}` |
| 数据库 | `postgres://mage2mcp@127.0.0.1:55440/mage2mcp_t76`（审计、确认、操作账本） |
| Magento 上游 | 本地假上游 `http://127.0.0.1:19091`：`GET /invoices/{id}` → 404（不存在的发票），`GET`/`PUT` `/products/{sku}` → 200，其余 404 |
| 身份 | 复用 RS256 测试令牌（viewer / operator / admin / approver），JWKS 由公网 httpbin 提供（见「环境限制」） |
| 审批 | `mage2mcp-admin -as approver@test -token-file token-approver approve <id>` |

构建与启动（真实命令）：

```bash
go build -o /tmp/t76/mage2mcp ./cmd/server && go build -o /tmp/t76/mage2mcp-admin ./cmd/admin
/tmp/t76/mage2mcp -config /tmp/t76/config-auth.yaml        # auth on, 1 instance, fake upstream
/tmp/t76/mage2mcp -config /tmp/t76/config-ratelimit.yaml   # auth off, tiny budget
```

## 结论摘要

| # | 验证项 | 结论 |
|---|---|---|
| 1 | self-ledgered 工具失败时审计不是 allow | ✅ 通过：`outcome / error / failed / not_found` |
| 2 | `_meta.request_id` 非空且与审计一致 | ✅ 通过（非 gated 路径）；⚠️ 见新问题 CV-PROTO-02（gated 写工具的 outcome 行 request_id 为空） |
| 3 | replay：有效确认成功、随机 UUID → not_found、过期 → confirmation_expired | ✅ 通过三项；⚠️ 见新问题 CV-PROTO-01（replay 留悬空 intent） |
| 4 | 限流耗尽返回 `rate_limited`（可重试），不是 `upstream_timeout` | ✅ 通过：30s 后 `rate_limited` + `retryable: true` |
| 5 | 早退路径（denied / confirmation_required）带 `_meta` | ✅ 通过，两者都带 instance/request_id/subject/tool |
| 6 | 并发确认：一个审批只有一次成功、`confirmation_used=0` | ✅ 通过：`ok=1 conflict=7 replayed=0 confirmation_used=0` |

**t63 的 7 条与 t69 的回归修复，在其声明的范围内全部成立。** 交叉验证另外发现两条**审计完整性问题**（不在 7 条声明之内，但位于同一代码路径）：

- **CV-PROTO-01（P2）**：replay 成功返回时**不写 outcome 审计行**，留下悬空 intent。
- **CV-PROTO-02（P2）**：ledger-gated 写工具的 outcome 审计行 **request_id 为空**，与同一次调用的 intent 行无法按 request_id 关联。

---

## 1. self-ledgered 工具失败 → 审计不是 allow（t63 SEM-04）

**命令**（真实调用）：`magento.invoice.capture`，`invoice_id=999999`（假上游返回 404），带已审批确认。

```text
PROBE1 invoice.capture on a nonexistent invoice:
  isError: True
  _meta: {"instance": "demo", "request_id": "7b602af21f2443e70e5e5dd94ea482eb", "subject": "admin@test", "tool": "magento.invoice.capture"}
  structured: {"code": "not_found", "details": {"store_code": "pl", "upstream_message": "The entity that was requested doesn't exist. Verify the entity and try again.", "upstream_status": "404"}, "message": "Magento has no such resource or route", "op": "magento GET /invoices/{id}"}
```

**查库**（同一 request_id）：

```sql
select phase, decision, state, error_code, request_id from audit_events where request_id='7b602af21f2443e70e5e5dd94ea482eb' order by seq;
-- intent | pending | pending |          | 7b602af2...
-- outcome| error   | failed  | not_found| 7b602af2...
```

**判定：通过。** 失败的自记账调用被记为 `error/failed`，不是 `allow/succeeded`（t58 原始缺陷的症状已消失）。

## 2. `_meta.request_id` 与审计行一致（t63 SEM-18）

```text
JSON-RPC _meta: {"instance": "demo", "request_id": "70d5c4f12c9b2ad5bb17fd638e6cee8d", "subject": "viewer@test", "tool": "magento.product.get"}

-- select tool, phase, decision, state, request_id from audit_events where request_id='70d5c4f1...'
magento.product.get|intent |pending|pending  |70d5c4f12c9b2ad5bb17fd638e6cee8d|demo
magento.product.get|outcome|allow  |succeeded|70d5c4f12c9b2ad5bb17fd638e6cee8d|demo
```

**判定：通过（非 gated 路径）。** 结果 `_meta` 与审计的 intent/outcome 两行 request_id 完全一致。
**但是**：对 ledger-gated 写工具，outcome 行的 request_id 为空——见 **CV-PROTO-02**。

## 3. replay 的三种输入（t63 SEM-19 / t69）

同一 `magento.product.update`（假上游 200）依次执行：

| 输入 | 实际响应 | 判定 |
|---|---|---|
| 已审批确认，首次执行 | 成功返回 product；`_meta.request_id=93c8a846…`；确认行变 `consumed` | ✅ 正常执行一次 |
| 同一确认再次提交（replay） | `replayed: this operation already succeeded (operation d54da49a-0616-482e-91f8-a4d44898c766)`；确认行**仍为 consumed**（未被二次消费） | ✅ t69：只读校验 |
| 从未签发的随机 UUID | `not_found: confirmation not found` | ✅ t63 SEM-19 |
| 已审批但已过期（TTL 5 分钟） | `confirmation_expired: confirmation has expired`；确认行保持 `approved` 且 `expired=t`（未被消费、未落库） | ✅ t63 SEM-19 |
| 完全不带确认 | `confirmation_required` | ✅ |

过期调用的审计（同一 request_id）：

```sql
-- intent |pending|pending|               | aa1dd6f4...
-- outcome|error  |failed | confirmation_expired | aa1dd6f4...
```

**判定：通过。** 三种 replay 输入返回码与 t63/t69 的声明一致；`confirmation_used` 不会被 replay 触发。

## 4. 限流耗尽 → `rate_limited`（t63 SEM-22）

配置 `requests_per_second: 0.01, burst: 1, max_concurrent: 1`；第一次调用消耗唯一令牌，第二次立即调用：

```text
first call:  isError=None elapsed=0.0s
second call: isError=True  elapsed=30.0s
  code: rate_limited | retryable: True | details: {"reason": "instance_budget_exhausted"}
  message: the Magento instance request budget was exhausted before the call deadline; retry later
  _meta: {"instance": "demo", "request_id": "8604e8525a54264e0894761e92ed277f", "tool": "magento.product.get"}
```

**判定：通过。** 等待 30.0 秒（工具声明超时）后返回的是 `rate_limited` 且 `retryable: true`，**不是** `upstream_timeout`——t58 观察到的「超时覆盖限流」已修复。

## 5. 早退路径的 `_meta`（t63 SEM-41）

| 路径 | 实际 `_meta` |
|---|---|
| `confirmation_required`（写工具缺确认） | `{"instance": "demo", "request_id": "fe06489d…", "subject": "admin@test", "tool": "magento.invoice.capture"}` |
| `permission_denied`（viewer 调写工具） | `{"instance": "demo", "request_id": "96c7ea25…", "subject": "viewer@test", "tool": "magento.invoice.capture"}` |

**判定：通过。** 两条早退路径都带完整 `_meta`（t57 记录的 `_meta: null` 已消失）。

## 6. 并发确认（t69 回归）

```bash
MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_t76_qa?sslmode=disable' \
  go test -tags qa -count=1 ./test/qa/          # ok 2.535s（全绿）
MAGE2MCP_TEST_DATABASE_URL=… go test -tags qa -count=1 ./test/qa/ -run TestConcurrentApprovalTokenYieldsOneSuccess -v
--- PASS: TestConcurrentApprovalTokenYieldsOneSuccess (0.11s)
    confirm_concurrency_test.go:105: results: ok=1 conflict=7 replayed=0 confirmation_used=0
```

**判定：通过。** 一个审批恰好一次成功，输家没有一个走到 consume（`confirmation_used=0`），上游只被写一次。
注：t69 报告的是 `conflict=6 replayed=1`，本次是 `conflict=7 replayed=0`——并发竞争下哪个输家先被拒是可变的，但不变式（ok=1、confirmation_used=0）成立。

---

## 交叉验证发现的新问题

### CV-PROTO-01: replay 成功返回时没有写 outcome 审计行，留下悬空 intent

- **严重级别**: P2
- **工具**: 所有 ledger-gated 写工具（本次复现：`magento.product.update`）
- **现象**: replay 请求只留下 `intent/pending/pending` 一行，**没有对应的 outcome 行**；同一次会话里其它分支（拒绝、过期、正常执行）都是成对的。

  ```sql
  -- select seq, phase, decision, state, request_id from audit_events where tool='magento.product.update' order by seq;
  18|intent |pending|pending  |93c8a846…   <- 首次执行
  19|outcome|allow  |succeeded|NULL        <- 见 CV-PROTO-02
  20|intent |pending|pending  |23ee785d…   <- replay：只有 intent，没有 outcome
  21|intent |pending|pending  |30ada362…   <- 随机 UUID
  22|outcome|deny   |denied   |30ada362…   <- 成对
  ```

- **期望**: 审计表是「intent 必有 outcome」的追加记录（`internal/storage/audit.go` 的设计注释即如此承诺）。replay 是一次被审计的调用，应记一条 outcome（例如 `allow/succeeded`，并带 `replayed` 标记）。
- **证据**: `internal/mcp/server/server.go:279` 的 `case DecisionReplay:` 分支在确认校验通过后直接 `return withMetaResult(ctx, tool, instance, res), replayErr`（`:292-293`），**没有调用 `recordOutcome`**；同文件其它决策分支（Retry/InProgress/Unknown/default）以及确认校验失败分支都调用了。
- **影响**: 一次 replay 在审计里表现为「发起过、但从未结束」。按 request_id 或按 phase 审计时会出现无法解释的悬空 intent；对资金类写工具，事后追溯「这次调用最终做了什么」会缺一行。不是功能缺陷（replay 返回正确结果），是审计完整性缺陷。
- **状态**: **CONFIRMED**（实测 + 代码定位；t62 时代即存在，t63 改写了该分支但未补 outcome）。

### CV-PROTO-02: ledger-gated 写工具的 outcome 审计行 request_id 为空

- **严重级别**: P2
- **工具**: 所有走操作账本的写工具（本次复现：`magento.product.update`；invoice/creditmemo/order 系列同路径）
- **现象**: 同一次成功调用，intent 行有 request_id，outcome 行没有：

  ```sql
  18|intent |pending|pending  |93c8a84691a456b6eb3d825a8d1edc1e
  19|outcome|allow  |succeeded|NULL
  ```

  而结果 `_meta.request_id` 正是 `93c8a846…`。
- **期望**: t63 的 SEM-18 声明是「审计回调与结果 `_meta` 携带同一个 id」。gated 写工具的 outcome 由账本事务写入，也应携带同一个 id。
- **证据**: outcome 行由 `auditEventFor` 构造（`internal/mcp/server/server.go:1084`），其 `audit.Event`（`:1094-1097`）只设置 Subject/IdentityClass/Tool/Instance/Scope/Phase/ErrorCode，**没有 `RequestID`**；而中间件的审计回调在 `cmd/server/main.go:140` 设置了 `RequestID: server.RequestID(ctx)`。t63 的覆盖测试 `TestRequestIDFlowsToAuditAndMeta`（`internal/mcp/server/server_test.go:2161`）用的是 **read** 工具且未接 `BeginOperation`，因此只覆盖了中间件路径，没有覆盖账本路径。
- **影响**: 最敏感的一类调用（资金与目录写）无法用 request_id 把结果与 outcome 审计行对上——只能靠 tool+时间猜。SEM-18 的目的（把一次 MCP 结果与它的审计行关联起来）在 gated 写工具上未达成。修复很小：`auditEventFor` 加 `RequestID: RequestID(ctx)`（`ctx` 已在签名里）。
- **状态**: **CONFIRMED**（实测 + 代码定位 + 测试覆盖缺口）。

---

## 环境限制与诚实声明

- **JWKS 走公网 httpbin**：本机 Go 的 TLS 信任库不读 `SSL_CERT_FILE`（实测 `x509: certificate signed by unknown authority`），本地自签 JWKS 端点无法被网关信任，因此沿用 t58 的 `https://httpbin.org/base64/<jwks>`。这不影响被测逻辑（JWT 校验路径与生产一致），但引入了外部依赖；若 httpbin 不可达，鉴权类探针无法执行。
- **上游是本地假服务**，不是真实 Magento：本次验证的是网关语义（审计、request_id、replay、限流、`_meta`），不是 Magento 业务行为；假上游的 404/200 是刻意构造的。
- **限流探针**用 `requests_per_second: 0.01` 制造「预算在工具超时前无法满足」，因此单次探针耗时 30 秒；这是为了让 t63 的「超时不得覆盖 rate_limited」分支真正触发。
- **未覆盖**：并发 replay 与并发 consume 的交错（由 QA 套件覆盖）、`operation_unknown` 的真实触发（需要上游在写入中途超时，未在假上游上构造）。
- 本报告只记录，**未修改任何产品代码**；探针脚本在 `/tmp/t76/`。

## 结论

t63 的 7 条修复与 t69 的 replay 回归修复，**在本次独立实测中全部成立**：self-ledgered 失败不再审计为 allow、`_meta.request_id` 与审计一致（非 gated 路径）、replay 三种输入的返回码正确且不重复消费、限流耗尽返回可重试的 `rate_limited`、早退路径带 `_meta`、并发确认 `confirmation_used=0`。

同时发现两条同区域的**审计完整性**缺陷（CV-PROTO-01/02，均 P2，均已定位到文件:行并有实测证据）。它们不影响 t63/t69 的功能正确性，但使「一次调用必有一对审计行、且可按 request_id 关联」这一承诺在 replay 与 gated 写工具两条路径上不成立，建议在发布前一并修复（两处都只需几行）。
