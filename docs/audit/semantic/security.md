# 语义层测试：安全与授权（security）

- **测试日期**：2026-11-22

- **被测版本**：生产 v0.11.5（工作树 `v0.11.5-4-g5e2749e`；相关安全文件与 v0.11.5 无差异）

- **测试者**：mcp-engineer

- **测试方式**：生产装配本地起鉴权实例（`cmd/server` + `server.NewMCP` + `rbac.DefaultPolicy` + `auth.Middleware`），对同一 demo Magento 店铺 `demo-magento-2.auroracreation.com`（store `pl`）做语义测试；限流另起本地 fake Magento 以可控延迟测试。

- **代码修改**：无。本文件只记录；本地配置/令牌/探针均在 `/tmp/sem-security/`。

## 0. 环境边界（必须先读）

| 项目 | 值 |
|---|---|
| 原测试端点 | `http://127.0.0.1:18110/mcp`，**`auth.enabled=false`**（任务给定） |
| 鉴权测试实例 | `http://127.0.0.1:18112/mcp`，`auth.enabled=true`，issuer `https://local.test`，audience `mage2mcp`，RS256 JWKS |
| 限流测试实例 | `http://127.0.0.1:18113/mcp`，鉴权同上；Magento endpoint 为本地 fake `http://127.0.0.1:19090`，`limits: 20 rps / burst 20 / max_concurrent 4` |
| Magento 实例 | `demo` 与 `demo-alt`，同一 demo 店铺、同一 store `pl`；令牌只存在 `/tmp`，不写入本文件 |
| 审批 CLI | 本地 `mage2mcp-admin`，连接隔离库 `mage2mcp_sem_security` |

**结论边界**：生产 18110 未开鉴权是**测试部署配置**，不是代码缺陷；因此 18110 上无法验证 RBAC，所有 RBAC/确认/审批结论来自 18112 的鉴权实例。下面 SEM-SEC-01/02/03/04 是 v0.11.5 **代码缺陷**；SEM-SEC-05 是 SUSPECTED；SEM-SEC-06/07 是 NOT-A-DEFECT（检查过）。没有发现“未开鉴权时仍能绕过 RBAC”的代码路径——因为未开鉴权时根本没有 RBAC，只有部署控制。

### 本地鉴权实例（复现用）

```yaml
server: {listen: "127.0.0.1:18112"}
auth: {enabled: true, issuer: "https://local.test", audience: "mage2mcp", jwks_url: "https://<local-jwks>/jwks.json"}
tools: {writes: true}
database: {url: "postgres://mage2mcp:<local-db-password>@127.0.0.1:55440/mage2mcp_sem_security?sslmode=disable"}
datasources:
  magento:
    enabled: true
    instances:
      - {id: demo, endpoint: "https://demo-magento-2.auroracreation.com", store_code: pl, credential: "bearer:<redacted>", limits: {requests_per_second: 20, burst: 20, max_concurrent: 4}}
      - {id: demo-alt, endpoint: "https://demo-magento-2.auroracreation.com", store_code: pl, credential: "bearer:<redacted>", limits: {requests_per_second: 20, burst: 20, max_concurrent: 4}}

```

```bash
go build -o /tmp/sem-security/mage2mcp ./cmd/server
SSL_CERT_FILE=/tmp/sem-security/ca.crt /tmp/sem-security/mage2mcp -config /tmp/sem-security/config.yaml
# tokens: RS256, iss=https://local.test, aud=mage2mcp, roles=[viewer|operator|admin|approver]
python3 /tmp/sem-security/mcp_client.py viewer magento.product.get '{"instance":"demo","sku":"24-MB01"}'

```

## 1. 结论摘要

| ID | 严重级别 | 状态 | 主题 |
|---|---|---|---|
| SEM-SEC-01 | P1 | **CONFIRMED** | 默认策略不限制 instance：viewer 可读另一个实例 |
| SEM-SEC-02 | P1 | **CONFIRMED** | 审批成功但审批审计行写入失败（DB CHECK 拒绝 `decision=approve`） |
| SEM-SEC-03 | P1 | **CONFIRMED** | 自记账工具的错误被审计成 `allow/succeeded` |
| SEM-SEC-04 | P2 | **CONFIRMED** | 限流预算耗尽被 MCP 超时覆盖成 `upstream_timeout`，不是 `rate_limited` |
| SEM-SEC-05 | P3 | **SUSPECTED** | 每个结果的 `_meta.subject` 回显调用者邮箱 |
| SEM-SEC-06 | — | **NOT-A-DEFECT** | instance/SKU/导入负载的 SSRF/路径注入面已被 schema/实现封住 |
| SEM-SEC-07 | — | **NOT-A-DEFECT** | 401/日志/instructions/上游错误文本未发现令牌、密码或上游原文泄漏 |

**正常项**：RBAC 阶梯、确认绑定 tool+instance+args、审批人 JWT 验证、自审批拒绝、拒绝审计 `deny`、每次调用 intent+outcome、限流 4 并发均实测通过。

---

### SEM-SEC-01: 默认策略不限制 instance，viewer 可跨实例读取

- **严重级别**: P1

- **工具**: 所有带 `instance` 的工具（以 `magento.product.get` 为例）

- **现象**: viewer 对 `demo` 的 `product.get` 被允许；把 `instance` 改为 `demo-alt` 后仍返回完整产品，不是 `permission_denied`。`rbac.DefaultPolicy()` 的四个 grant 都没有 `Instance` 字段，`matchesInstance("", "demo-alt")` 返回 true。

- **期望**: 若实例代表不同租户/店铺权限，跨实例调用应被拒绝；至少应支持 per-instance grant。

- **证据**:

  ```bash
  python3 /tmp/sem-security/mcp_client.py viewer magento.product.get '{"instance":"demo-alt","sku":"24-MB01"}'
  ```

  ```text
  CALL 200 {"_meta":{"instance":"demo-alt","subject":"viewer@test","tool":"magento.product.get"},
            "structuredContent":{"product":{"id":1,"sku":"24-MB01","name":"Joust Duffle Bag",...}}}
  ```

  代码：`internal/security/rbac/rbac.go` 的 `DefaultPolicy()` 使用 `Tool:"*"` 且 `Instance` 为空；`matchesInstance` 对空 grant 返回 true。

- **影响**: 多店铺/多租户部署中，拥有某店 viewer 的调用者可读取所有已配置店铺的数据；写入/破坏性操作同样按角色全局生效。

- **状态**: **CONFIRMED**（代码/策略设计缺陷；若产品明确把所有实例视为同一信任域，则应记录为 accepted limitation，而不是声称跨实例隔离）。

### SEM-SEC-02: 审批成功但审批审计行永远写不进去

- **严重级别**: P1

- **工具**: `mage2mcp-admin approve`（所有需要人工审批的写工具）

- **现象**: `svc.Approve` 已把 confirmation 置为 `approved`，随后 `recordApproval` 写 `audit_events` 时失败，CLI 返回错误。`recordApproval` 写 `Decision: "approve"`，而 `audit_events_decision_check` 只允许 `allow/deny/confirmation_required/error/pending`。

- **期望**: 审批成功应留下一条可查询的 approval 审计行；要么把 `approve` 加入约束，要么使用允许的 decision 值。

- **证据**:

  ```bash
  /tmp/sem-security/mage2mcp-admin -config /tmp/sem-security/config.yaml \
    -as approver@test -token-file /tmp/sem-security/token-approver \
    approve f4415a64-2edd-4534-b8ed-c9595c8325ab
  ```

  ```text
  mage2mcp-admin: approved, but the audit row could not be written:
  storage: insert audit event: ERROR: new row for relation "audit_events"
  violates check constraint "audit_events_decision_check" (SQLSTATE 23514)
  ```

  ```sql
  select count(*) as approval_rows from audit_events where phase='approval'; -- 0
  select count(*) as confirmed from confirmation_tokens where state='approved'; -- 4
  ```

- **影响**: 审批动作没有审计记录；CLI 在审批已生效后返回失败，操作者可能重复审批或误判。对资金写工具，审批链的不可抵赖性受损。

- **状态**: **CONFIRMED**（代码缺陷：`cmd/admin/main.go` 的 `recordApproval` 与 migration `000001_init.up.sql:70` 的 CHECK 不一致；`000007` 只加了 `pending`，没有 `approve`）。

### SEM-SEC-03: 自记账工具的错误被审计成 allow/succeeded

- **严重级别**: P1

- **工具**: `magento.invoice.capture`（五个 self-ledgered 资金工具同类）

- **现象**: 对不存在的 invoice 调用 `magento.invoice.capture`，客户端收到 `not_found`；但 `audit_events` 的 outcome 是 `succeeded/allow`，不是 `failed/error`。该工具没有走到 `Operations.Begin`（无 operations 行），`callState.OperationOwned()` 为 false，middleware 走非 gated 分支，只看 `err`，不看 `result.IsError`。

- **期望**: 工具返回错误时 outcome 应为 `failed/error`（或 ambiguous 时 `outcome_unknown`）；审计不能把失败写成允许成功。

- **证据**:

  ```bash
  python3 /tmp/sem-security/selfledger.py
  ```

  ```text
  CALL ... {"code":"not_found","message":"Magento has no such resource or route","op":"magento GET /invoices/{id}"}
  ```

  ```sql
  select seq,tool,phase,state,decision,error_code from audit_events
   where tool='magento.invoice.capture' order by seq;
  -- 54 | magento.invoice.capture | intent  | pending   | pending |
  -- 55 | magento.invoice.capture | outcome | succeeded | allow   |
  ```

  代码：`internal/mcp/server/server.go:371-376` 非 gated 分支只用 `err` 计算 `state`；`toolerr` 把 handler 错误折叠进 `CallToolResult.IsError`，`err` 为 nil。

- **影响**: 资金/订单工具失败在审计中显示为成功允许；安全审计、事后取证和异常检测都会漏报。该工具只写一条 outcome（不重复），但内容错误。

- **状态**: **CONFIRMED**（代码缺陷；与 self-ledgered 的 operation-ownership marker 有关，但失败发生在 Begin 之前时 marker 未设置）。

### SEM-SEC-04: 限流预算耗尽返回 `upstream_timeout`，不是 `rate_limited`

- **严重级别**: P2

- **工具**: `magento.product.get`（所有走 Magento client 的工具）

- **现象**: 实例配置 `20 rps / burst 20 / max_concurrent 4`。650 个并发调用中，fake 上游只收到 628 个请求，`max_active=4`（桶和并发都生效）；22 个调用返回 `upstream_timeout`（`details.timeout=30s`），没有 `rate_limited`。

- **期望**: 文档/`limits.go` 声称预算在工具 deadline 前无法满足时返回 `rate_limited`（retryable）；实际被 `installPolicy` 的 `timedOut` 分支覆盖成 `upstream_timeout`。

- **证据**:

  ```bash
  # fake upstream sleep=0.01s, gateway limits 20/20/4
  # 650 concurrent calls, same session/token
  ```

  ```text
  elapsed 30.46 codes Counter({200: 650})
  errors [(None, 628), ('upstream_timeout', 22)]
  fake stats: {"count": 628, "max_active": 4}
  first error: {"code":"upstream_timeout","details":{"timeout":"30s"},"message":"tool exceeded its declared timeout"}
  ```

  代码：`internal/magento/limits.go:182` 返回 `CodeRateLimited`；`internal/mcp/server/server.go:325-328` 在 `ctx.Err()==DeadlineExceeded` 时无条件用 `CodeUpstreamTimeout` 覆盖。

- **影响**: 调用者无法区分“上游慢/超时”和“本地限流”；`upstream_timeout` 不携带 retryable 的 `rate_limited` 语义，重试策略和告警归因错误。桶本身工作正常。

- **状态**: **CONFIRMED**（代码缺陷；限流生效，错误分类错误）。

### SEM-SEC-05: `_meta.subject` 在每次结果中回显调用者邮箱

- **严重级别**: P3

- **工具**: 所有 tools/call 结果

- **现象**: 每个结果 `_meta` 包含 `subject: "viewer@test"`（生产中是 IdP 邮箱/subject）。这是调用者自己的身份，不是另一个用户的 PII；但如果结果被转发、缓存或记录，邮箱会随业务数据扩散。

- **期望**: 只回传 `request_id`/`instance`/`tool`，或使用不可逆 subject hash；若保留，应在文档中说明。

- **证据**:

  ```bash
  python3 /tmp/sem-security/mcp_client.py viewer magento.product.get '{"instance":"demo","sku":"24-MB01"}'
  ```

  ```text
  "_meta": {"instance":"demo","subject":"viewer@test","tool":"magento.product.get"}
  ```

  代码：`internal/mcp/server/server.go:542-545`。

- **影响**: PII 扩散面；非跨用户泄漏，但属于数据最小化问题。

- **状态**: **SUSPECTED**（是否构成缺陷取决于结果保留/转发策略；无令牌或密码泄漏）。

### SEM-SEC-06: instance/SKU/导入负载的 SSRF 与路径注入面

- **严重级别**: —（NOT-A-DEFECT）

- **工具**: `magento.product.get`、`magento.import.validate`

- **现象/验证**:
  - `sku="../../V1/orders/1"` → schema `invalid_argument`（SKU pattern `^[A-Za-z0-9][A-Za-z0-9._:-]*$`）。
  - `instance="../../"` → schema `invalid_argument`（instance pattern `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`）。
  - `sku="24-MB01?x=1"` → schema `invalid_argument`。
  - CSV 负载含 `http://169.254.169.254/latest/meta-data/` 与 `../../etc/passwd` → 只做字段校验，返回 `preview`，没有任何 fetch；导入路径不会把字段当 URL 请求。
  - endpoint 由部署配置决定，工具不能设置；`magento.NewClient` 要求 https（或 loopback http），拒绝 userinfo/path/query/fragment。

- **期望**: 不接受调用者控制的 URL/路径；实现满足。

- **证据**: 上述命令输出见本次会话；schema 由 `toolerr`/`mcp` 在调用前校验，导入只解析 CSV/JSON。

- **影响**: 未发现可利用的 SSRF/路径注入面。

- **状态**: **NOT-A-DEFECT**。

### SEM-SEC-07: 令牌、密码、上游原文、instructions 泄漏

- **严重级别**: —（NOT-A-DEFECT）

- **验证**:
  - 无 token / 伪造 token 的 401 响应只有 `{"error":"unauthenticated","reason":"malformed_token"}`，不回显凭据。
  - `server.log` 中没有 `Bearer`、JWT、密码、上游 `upstream_message`、产品描述或客户数据；auth 失败只记录稳定 reason。
  - `instructions` 是静态安全提示，不含令牌/密码。
  - 上游 4xx 文本由 `errs.UpstreamOf` 仅写日志；客户端只收到 `validation_failed` + `store_code`/`upstream_status`，没有 Magento 原文。
  - 确认摘要 `confirm.Summarize` 对 secret-shaped key 做 redaction（本测试未发现泄漏）。

- **状态**: **NOT-A-DEFECT**。

## 2. RBAC 阶梯（鉴权实例实测）

| 角色 | 工具 | 结果 | 判定 |
|---|---|---|---|
| viewer | `magento.product.get` | `_meta` + product，成功 | 允许 read |
| viewer | `magento.product.update` | `permission_denied` / `no matching grant` | 拒绝 write |
| viewer | `magento.product.delete` | `permission_denied` / `no matching grant` | 拒绝 destructive |
| operator | `magento.product.update` | `confirmation_required` | 允许 write（进入审批） |
| operator | `magento.product.delete` | `permission_denied` / `no matching grant` | 拒绝 destructive |
| admin | `magento.product.delete` | `confirmation_required` | 允许 destructive（进入审批） |

复现：

```bash
python3 /tmp/sem-security/mcp_client.py viewer magento.product.update '{"instance":"demo","sku":"NO-SUCH-SKU","name":"x"}'
python3 /tmp/sem-security/mcp_client.py operator magento.product.update '{"instance":"demo","sku":"NO-SUCH-SKU","name":"x"}'
python3 /tmp/sem-security/mcp_client.py operator magento.product.delete '{"instance":"demo","sku":"NO-SUCH-SKU"}'
python3 /tmp/sem-security/mcp_client.py admin magento.product.delete '{"instance":"demo","sku":"NO-SUCH-SKU"}'

```

实际输出（节选）：

```text
viewer update   permission_denied {"reason":"no matching grant"}
operator update confirmation_required {"confirmation_id":"..."}
operator delete permission_denied {"reason":"no matching grant"}
admin delete    confirmation_required {"confirmation_id":"..."}

```

跨实例例外见 SEM-SEC-01。

## 3. 确认绑定与审批人身份（实测）

- 改参数（`name` 变）→ `confirmation_param_mismatch`；改 instance → 同；改 tool → 同；另一 subject 消费 → `permission_denied: confirmation belongs to a different requester`；上述失败后 confirmation 仍为 `approved`（未烧毁）。

- 自审批：operator 审批自己创建的 confirmation → `permission_denied: self-approval is not allowed`。

- 审批人身份：`-token-file token-operator -as approver@test` → `-as "approver@test" does not match the verified subject "operator@test"`；无 token → 明确要求 token。

- 正确 approver token 可审批（但触发 SEM-SEC-02 审计错误）。

- 复现命令见 `/tmp/sem-security/confirm_bind.py` 与 `mage2mcp-admin` 调用；输出已在上文摘录。

## 4. 审计行（实测）

```sql
select decision, phase, state, count(*) from audit_events group by 1,2,3 order by 1,2,3;

```

```text
 allow    | outcome | succeeded | 3
 deny     | outcome | denied    | 9
 error    | outcome | failed    | 10
 pending  | intent  | pending   | 22

```

- 每次读/写调用都有 intent+outcome；viewer 拒绝写为 `deny`，不是 allow。

- gated 工具错误为 `error/failed`（例：product.update permission_denied）。

- 例外：SEM-SEC-02 审批无 approval 行；SEM-SEC-03 self-ledgered 错误为 `allow/succeeded`；self-ledgered 只有一条 outcome，不重复，但内容错误。

## 5. 限流（实测）

- fake 上游 + 鉴权实例，`limits: requests_per_second 20, burst 20, max_concurrent 4`。

- 20 并发、每请求 0.5s：fake `max_active=4`，20/20 成功。

- 650 并发、每请求 0.01s：fake 收到 628 个上游请求，`max_active=4`；22 个客户端结果为 `upstream_timeout`，无 `rate_limited`（SEM-SEC-04）。

- 复现：`/tmp/sem-security/fake_magento.py` + `config-fake.yaml` + 并发 Python 脚本；stats 端点 `/stats` 输出 `{"count":628,"max_active":4}`。

## 6. 部署未开鉴权的说明（非代码缺陷）

生产测试端点 18110 的 `auth.enabled=false`，因此该端点没有 RBAC、没有确认审批身份，任何可达客户端都可以调用已注册工具。这是**部署配置风险**，不是 v0.11.5 代码缺陷；本报告不在该端点执行写操作。修复是启用 `auth.enabled` 并提供 issuer/audience/JWKS；本地 18112 已证明同一代码路径在开启鉴权时按上述结果工作。

## 7. 建议优先级

1. **SEM-SEC-02 / SEM-SEC-03（P1）**：审批审计约束与 self-ledgered 错误分类；两者都直接破坏安全审计可信度。
2. **SEM-SEC-01（P1）**：若多实例属于不同信任域，默认策略必须支持 per-instance grant；否则在文档中明确“实例不是租户边界”。
3. **SEM-SEC-04（P2）**：保留 `rate_limited`，不要让 MCP timeout 覆盖预算错误。
4. **SEM-SEC-05（P3）**：考虑移除/哈希 `_meta.subject`，或记录为可接受的调用者自身标识。

## 8. 复现环境清理

- 本地鉴权/限流实例、fake 上游、JWKS、令牌、隔离数据库均在 `/tmp/sem-security/` 和本地 PostgreSQL `mage2mcp_sem_security`；不修改产品代码。

- 生产 18110 未做写操作；demo 店铺只读查询。
