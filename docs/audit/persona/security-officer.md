# 安全合规岗位场景测试（v0.11.6）

我的日常任务：回答三个问题——**谁能做什么**（越权）、**每次操作有没有留痕**（审计轨迹）、
**数据有没有越界**（PII/密钥/跨租户）。我不写业务代码，我做的是事后取证：出了事，我要能从
`audit_events` 里还原「谁、在哪个实例、用什么审批、调了什么、成功还是失败」。任何一次越权、
任何一条无法关联的审计行，对我来说都是阻断级。

生产端点 `18110` 是 `auth.enabled=false`（无 RBAC、无身份），所以本报告的所有授权结论来自
**我自己按 v0.11.6 源码起的一个开启鉴权的实例**；只读/无副作用结论（版本、匿名可达性）在生产
端点上复核过。

## 环境与边界

| 项目 | 值 |
|---|---|
| 被测代码 | 工作树 `v0.11.6`（`6717d2b release: v0.11.6`），`go build ./cmd/server` + `./cmd/admin` |
| 鉴权实例 | `http://127.0.0.1:18120/mcp`，`auth.enabled=true`，issuer `https://local.test`，audience `mage2mcp`，RS256 JWKS |
| 上游实例 | `magento: demo`（真实 demo 店铺，store `pl`）+ `cloudflare: zone-a`（占位凭据，只用于跨实例授权探测） |
| 假上游实例 | `http://127.0.0.1:18121/mcp` → 本地 fake Magento `127.0.0.1:19091`（`0.4s` 延迟，`1 rps / burst 1 / max_concurrent 1`） |
| 数据库 | 隔离库 `mage2mcp_sec_v0116`（PostgreSQL 127.0.0.1:55440），启动时自动迁移 |
| 角色令牌 | RS256，`roles=[viewer|analyst|operator|admin|approver]`，另有`norole`、`unknownrole`、`multirole`、`service`（`type=service`）、`expired`、`wrongaud`、`wrongiss` |
| 审批 CLI | `/tmp/sec-v0116/mage2mcp-admin -config config.yaml -token-file token-<role> approve <id>` |
| 生产端点 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → `49.51.250.83:8080`），`auth.enabled=false` |
| 代码修改 | **无**。本文件只记录；配置/令牌/探针全部在 `/tmp/sec-v0116/` |

鉴权实例配置（复现用，令牌已省略）：

```yaml
server: {listen: "127.0.0.1:18120"}
auth:
  enabled: true
  issuer: "https://local.test"
  audience: "mage2mcp"
  jwks_url: "https://httpbin.org/base64/<base64 of jwks.json>"
tools: {writes: true}
database: {url: "postgres://mage2mcp:***@127.0.0.1:55440/mage2mcp_sec_v0116?sslmode=disable"}
datasources:
  magento:
    enabled: true
    instances:
      - {id: demo, endpoint: "https://demo-magento-2.auroracreation.com", store_code: pl, credential: "bearer:<redacted>", limits: {requests_per_second: 20, burst: 20, max_concurrent: 4}}
  cloudflare:
    enabled: true
    instances:
      - {id: zone-a, account_id: acct-a, zone_id: zone-a, credential: "dummy-cf-token-a"}
```

⚠️ 环境坑（复现须知）：本机 Go 在 macOS 上**不读** `SSL_CERT_FILE`/`SSL_CERT_DIR`（`crypto/x509`
走 Security 框架），本地自签 JWKS 服务会导致 `{"reason":"jwks_unavailable"}`。所以 JWKS 走
`https://httpbin.org/base64/<base64>`（只含公钥，无秘密）。另外 `text/event-stream` 响应没有
`charset=utf-8`，Python `requests` 会按 ISO-8859-1 解码把波兰语变乱码（见 P-SEC-09），判断时请用
`r.content.decode('utf-8')`。

客户端脚本 `/tmp/sec-v0116/mcp.py`（最小 MCP 客户端：initialize → tools/call，SSE 取最后一条 `data:`）。

## 结论摘要

| ID | 级别 | 状态 | 主题 |
|---|---|---|---|
| P-SEC-01 | **P1** | **CONFIRMED** | 超时/限流的调用**丢失 outcome 行**，审计只留一条永远 pending 的 intent，且无任何日志 |
| P-SEC-02 | **P1** | **CONFIRMED** | 五个自助记账资金工具的审计行：intent 悬空、outcome 无 request_id、`identity_class` 恒为 `service`、失败也记 `decision=allow` |
| P-SEC-03 | **P1（越权）** | **CONFIRMED** | 审批权限不绑定角色：`viewer` 可以批准 `admin` 的 destructive 删除；`type=service` 的 agent 令牌也能批准（与 ADR-0014 冲突） |
| P-SEC-04 | P2 | **CONFIRMED** | `audit_events.confirmation_id`/`role_name`/`params_hash` 从不写入；审批行没有 `confirmation_id` 和 `identity_class`，审批与操作不可关联 |
| P-SEC-05 | P2 | **CONFIRMED** | `instance` 参数在 schema 校验**之前**原样写进审计：5000 字符、换行、任意文本都能落库（审计污染/伪造归因） |
| P-SEC-06 | P3 | **CONFIRMED** | Cloudflare 工具的 `instance` 只是标签（不选凭据），于是「某实例的授权」可调用另一模块的工具，并把 Cloudflare 读操作记到 Magento 实例名下 |
| P-SEC-07 | P3 | **CONFIRMED** | 每个结果的 `_meta.subject` 回显调用者邮箱 |
| P-SEC-08 | P3 | **CONFIRMED** | `upstream_message` 可能带未替换的 `%1` 占位符（Magento `parameters` 为数组时） |
| P-SEC-09 | P3 | **CONFIRMED** | SSE 响应缺 `charset=utf-8`，按 HTTP 默认解码的客户端会把波兰语文本弄乱 |
| P-SEC-10 | — | **NOT-A-DEFECT** | `instance`/SKU/导入负载的注入面已被 schema 与实现封住（审计污染见 P-SEC-05） |
| P-SEC-11 | — | **NOT-A-DEFECT** | 重放不能绕过审批；消费过的审批不能被第二个主体复用 |
| P-SEC-12 | — | **NOT-A-DEFECT** | 401/日志/instructions 未泄漏令牌、密码或上游原文 |

**v0.11.5 遗留项的回归结论**：SEM-SEC-01（跨实例授权）、SEM-SEC-02（审批无审计行）、
SEM-SEC-03（资金工具失败记成 allow/succeeded）、SEM-SEC-04（限流被超时覆盖）**均已修复**，
证据见 §7。SEM-SEC-05（`_meta.subject`）保留为设计选择（P-SEC-07）。

---

## 一、越权：谁能做什么

### 1.1 RBAC 阶梯（正常）

```bash
python3 /tmp/sec-v0116/matrix.py
```

```text
（节选；D 行“读另一个模块的实例”见 §2.4）
A read own instance            viewer       http=200 errcode=None                    _meta.instance=demo
B write as viewer              viewer       http=200 errcode=permission_denied       details={"reason":"no matching grant"}
C destructive as viewer        viewer       http=200 errcode=permission_denied       details={"reason":"no matching grant"}
E read unconfigured instance   viewer       http=200 errcode=permission_denied       instance=demo2
F path-traversal instance      viewer       http=200 errcode=permission_denied       instance=../../demo
G platform.info                viewer       http=200 errcode=None
H write as analyst             analyst      http=200 errcode=permission_denied
I write as operator            operator     http=200 errcode=confirmation_required   confirmation_id=76a5487d-...
J destructive as operator      operator     http=200 errcode=permission_denied
K destructive as admin         admin        http=200 errcode=confirmation_required   confirmation_id=59fa97e8-...
M no roles                     norole       http=200 errcode=permission_denied       details={"reason":"no matching grant"}
N unknown role                 unknownrole  http=200 errcode=permission_denied       subject=ghost@test
O viewer+admin multirole       multirole    http=200 errcode=confirmation_required   （admin 授权生效）
```

读 < 写 < 破坏性的阶梯、未知角色零授权、未配置实例零授权都成立。

未注册工具在授权前就被拒（fail-closed）：

```bash
python3 /tmp/sec-v0116/mcp.py admin magento.does.not.exist '{"instance":"demo"}'
# CALL 200 {"code":"permission_denied","details":{"reason":"unregistered_tool","tool":"magento.does.not.exist"}}
```

会话绑定：viewer 建立的 `Mcp-Session-Id` 被 admin 复用时 404（不是 403，防探测）。

```text
viewer session MN4NH7N2ZIO2PFOKIMNAM7GQRE
admin reusing viewer session -> http 404 404 page not found
```

### 1.2 认证失败面（正常）

```bash
python3 - <<'EOF'  # 逐条：无 token / 垃圾 token / 过期 / 错 aud / 错 iss
EOF
```

```text
no token         http=401 body={"error": "unauthenticated", "reason": "missing_token"}
garbage          http=401 body={"error": "unauthenticated", "reason": "malformed_token"}
expired          http=401 body={"error": "unauthenticated", "reason": "expired"}
wrong aud        http=401 body={"error": "unauthenticated", "reason": "bad_audience"}
wrong iss        http=401 body={"error": "unauthenticated", "reason": "bad_issuer"}
```

响应里没有回显任何令牌片段。

---

### P-SEC-03: 审批权限不绑定角色——viewer 能批准 destructive 删除，agent 令牌也能批准

- **级别**: P1（越权；按岗位要求「任何越权都是阻断级」）
- **我的工作流**: 我是合规审计。破坏性操作必须由 `admin` 发起、由**另一个有权限的人**批准。
  我要证明：读权限的身份不能借审批通道获得写/破坏性权限，agent（service）身份不能成为
  「审批人 of record」（ADR-0014 明写 "an agent cannot be an approver of record"）。
- **发生了什么**: 两条都成立，审批通道是 RBAC 之外的第二个授权入口，且它不查角色、不查身份类别。

  1）admin 发起 destructive 删除（不存在的 SKU，不会真的删数据），viewer 用**自己的令牌**批准：

  ```bash
  CID_DEL=2bf4d848-e776-48ff-8951-979d7841b23d
  ./mage2mcp-admin -config config.yaml -token-file token-viewer approve $CID_DEL
  ```

  ```text
  approved 2bf4d848-e776-48ff-8951-979d7841b23d by viewer@test
  exit=0
  ```

  admin 随后消费这条审批，操作真的进入上游（这里因 SKU 不存在返回 404，但审批闸门已放行）：

  ```bash
  python3 - <<'EOF'
  # admin 调 magento.product.delete，带 confirmation_id=2bf4d848-...
  EOF
  ```

  ```text
  {"_meta":{"instance":"demo","request_id":"29757ff32d8996ebaf86f96de5cbdd87","subject":"admin@test","tool":"magento.product.delete"},
   "structuredContent":{"code":"not_found","details":{"store_code":"pl","upstream_status":"404",...}}}
  ```

  审计里能看到「requester=admin、approver=viewer」两行（subject 已哈希）：

  ```sql
  select seq,identity_class,encode(subject_hash,'hex') subj,tool,phase,state,decision,scope
    from audit_events where seq in (55,56,57);
  --  55 |              | 7c030246...（viewer） | magento.product.delete | approval | approved | approve | destructive
  --  56 | human        | 9d40f11d...（admin）  | magento.product.delete | intent   | pending  | pending |
  --  57 | human        | 9d40f11d...（admin）  | magento.product.delete | outcome  | failed   | error   | destructive
  ```

  2）`type=service` 的 agent 令牌（subject `agent-bot@test`，roles=[admin]）批准 operator 的写操作：

  ```bash
  ./mage2mcp-admin -config config.yaml -token-file token-service approve 65da6239-b91b-483f-91d5-7d6fa57b22b9
  ```

  ```text
  approved 65da6239-b91b-483f-91d5-7d6fa57b22b9 by agent-bot@test
  exit=0
  ```

  对应的审计审批行 `identity_class` 是 **NULL**（`recordApproval` 不写身份类别），事后无法区分
  这是人还是 agent 批准的：

  ```sql
  select seq,identity_class,tool,phase,state,decision,scope from audit_events where phase='approval' order by seq limit 3;
  --  55 |       | magento.product.delete | approval | approved | approve | destructive
  --  58 |       | magento.product.update | approval | approved | approve | write
  --  77 |       | magento.invoice.capture| approval | approved | approve | write
  ```

  自审批被拒（这条正常）：

  ```bash
  ./mage2mcp-admin -config config.yaml -token-file token-operator approve <operator 自己的 cid>
  # mage2mcp-admin: permission_denied: self-approval is not allowed
  ```

  审批人身份来自令牌（`-as` 不能与令牌 subject 冲突，这条也正常）：

  ```bash
  ./mage2mcp-admin -config config.yaml -as approver@test -token-file token-operator approve <cid>
  # mage2mcp-admin: -as "approver@test" does not match the verified subject "operator@test"; ...
  ```

- **我期望什么**: 审批是授权决定，应当与 RBAC 同源：至少要求批准者具备该操作所需 scope 的
  角色（destructive 的批准者应具备 destructive 权限），并且 ADR-0014 承诺的「agent 不能成为
  审批人」应当在 `confirm.Approve` / admin CLI 里被真正执行（`auth.Identity.Class` 已经算出来了，
  但全仓库没有任何地方读它）。
- **代码位置**: `cmd/admin/main.go:121`（`svc.Approve(ctx, id, approver)` 前只验签、不查角色）、
  `cmd/admin/main.go:231`（`resolveApprover` 只验 iss/aud/sub）、
  `internal/security/confirm/confirm.go:122`（`Approve` 只要求 approver 非空）、
  `internal/security/jwt/jwt.go:181-185`（`Class` 计算出来后无人使用）。
- **状态**: **CONFIRMED**。若产品认为「不同的人类」就够、审批人不需要任何角色，那么这不是实现
  缺陷而是模型缺陷——但 ADR-0014 的 agent 条款是明确承诺，必须至少修这一条，并在文档里写清
  「审批人不需要任何 RBAC 角色」。

---

## 二、跨租户：能不能用 A 实例的授权操作 B 实例

### 2.1 启动期拒绝多实例（SEM-SEC-01 已修）

```bash
cat config-two-magento.yaml   # magento 下 demo + demo-alt 两个实例，auth.enabled=true
./mage2mcp -config config-two-magento.yaml
```

```text
mage2mcp: rbac: magento has 2 instances (demo, demo-alt); the default policy binds one instance per module.
Grant per-instance policies explicitly, or call DefaultPolicyAllInstances() to treat them as one trust domain
```

默认策略不再静默地把所有店铺绑成同一个信任域；要跨店必须显式写策略或显式 `DefaultPolicyAllInstances()`。

### 2.2 未配置实例 / 未授权实例（正常）

```text
viewer magento.product.get instance=demo2      -> permission_denied {"reason":"no matching grant"}
viewer magento.product.get instance=../../demo -> permission_denied {"reason":"no matching grant"}
viewer magento.product.get instance=Demo_1     -> permission_denied {"reason":"no matching grant"}
```

### 2.3 审批绑定（正常）

```text
1 different args      -> confirmation_param_mismatch
2 different instance  -> confirmation_param_mismatch        # 同一条审批不能换实例用
3 unconfigured inst   -> permission_denied {"reason":"no matching grant"}
4 different subject   -> permission_denied {"reason":"no matching grant"}
5 different tool      -> permission_denied {"reason":"no matching grant"}
6 correct consume     -> validation_failed（审批通过、请求到了 Magento，被上游拒绝）
7 replay after consume-> confirmation_used
```

审批与 tool+instance+args+requester 绑定，单次使用，换实例即失效。

### 2.4 跨模块实例：Cloudflare 的 `instance` 只是标签（P-SEC-06）

- **级别**: P3
- **我的工作流**: 多店铺/多模块部署里，我要确认「授权是绑在实例上的」。如果 A 模块的实例授权能
  调用 B 模块的工具，那我按实例划分的授权边界就不成立。
- **发生了什么**: `cloudflare.*` 的 `instance` 字段只用于回显，不选凭据、不校验是否属于该模块。
  viewer 只有 `demo`（magento 实例）和 `zone-a`（cloudflare 实例）的 read 授权，用 `demo` 这个
  Magento 实例名去调 Cloudflare 工具，**授权通过并真的打了 Cloudflare API**（占位凭据 → 上游 400），
  审计把这次 Cloudflare 读操作记成 `instance=demo`：

  ```bash
  python3 - <<'EOF'
  # viewer 调 cloudflare.analytics.traffic，instance 分别用 demo / zone-a / nope
  EOF
  ```

  ```text
  instance=demo   -> {"code":"upstream_unavailable","details":{"upstream_status":"400"},"message":"Cloudflare rejected the request"}
  instance=zone-a -> {"code":"upstream_unavailable","details":{"upstream_status":"400"},...}
  instance=nope   -> {"code":"permission_denied","details":{"reason":"no matching grant"},...}
  ```

  ```sql
  select seq,instance_id,tool,state,error_code from audit_events where tool='cloudflare.analytics.traffic' order by seq;
  --  45 | demo   | cloudflare.analytics.traffic | failed | upstream_unavailable
  --  47 | zone-a | cloudflare.analytics.traffic | failed | upstream_unavailable
  ```

  `magento.*` 相反：用 `zone-a` 调 `magento.product.get` 授权能过（admin 在 zone-a 上有 grant），
  但 Magento 解析器会拒绝 → `{"code":"not_found","message":"unknown Magento instance"}`，不会串数据。
- **我期望什么**: 授权判定应绑 `(module, instance)`，或至少让工具解析器拒绝不属于自己的实例名；
  否则「按实例授权」在多模块部署里名不副实，审计里的 `instance_id` 也不能作为归因依据。
- **代码位置**: `internal/cloudflare/tools.go:35`（描述自述 "it does not select credentials"）、
  `internal/cloudflare/resolver.go:39`（`Resolve` 有校验但工具未使用）、
  `internal/security/rbac/rbac.go`（grant 只有 instance，没有 module）。
- **状态**: **CONFIRMED**（默认策略给每个模块的实例同样的阶梯，所以默认部署不产生越权；
  但按实例写自定义策略的部署会。属设计缺陷，非数据泄漏。）

### 2.5 重放不能绕过审批（NOT-A-DEFECT）

我先怀疑：`idempotency_key` 不按调用者隔离（schema 里明说 "The key is not caller-scoped"），
那么 B 用 A 的 key 重放能否跳过审批？实测不能：

```text
A 用显式 key sec-k3 执行 invoice.capture（审批后成功）：
  {"invoice_id":3,"captured":true,"operation_id":"b07c284c-b940-4dbd-839c-65feef3ce7d8"}
B(operator2) 同 key、不带审批 -> confirmation_required（审批闸门在重放判定之前）
B(operator2) 同 key、带 A 的审批 -> confirmation_used（已消费的审批不因重放而复活）
viewer 同参数 -> permission_denied {"reason":"no matching grant"}
```

另外「按参数派生 key」时，带 `confirmation_id` 与不带 `confirmation_id` 的 args hash 不同
（`DeriveOperationKey(subject, CanonicalHash(args))`），所以也不能用「不带审批的同参数调用」去命中
一次已成功的有审批操作。上游计数（fake `/stats`）确认重放没有产生第二次变更。

---

## 三、审计轨迹：能不能还原每一次操作

### 3.1 完整性统计（大部分正常）

```sql
select count(*) total, count(*) filter (where phase='intent') intents,
       count(*) filter (where phase='outcome') outcomes,
       count(*) filter (where phase='approval') approvals,
       count(*) filter (where phase='intent' and not exists
            (select 1 from audit_events o where o.phase='outcome' and o.request_id=audit_events.request_id)) orphan_intents
  from audit_events;
--  total | intents | outcomes | approvals | orphan_intents
--    253 |     127 |      115 |        11 |             15
```

```sql
select phase,state,decision,count(*) from audit_events group by 1,2,3 order by 1,2,3;
--  approval | approved  | approve |    11
--  intent   | pending   | pending |   127
--  outcome  | denied    | deny    |    41
--  outcome  | failed    | allow   |     1     <-- 见 P-SEC-02
--  outcome  | failed    | error   |    24
--  outcome  | succeeded | allow   |    49
```

正常项：每次调用都有 intent；拒绝记 `deny`、失败记 `error`；`request_id` 可把 intent 与 outcome
配对；审批现在有独立行（SEM-SEC-02 已修）。异常项就是下面两条 P1 和 P-SEC-04/05。

### P-SEC-01: 超时/限流的调用丢失 outcome 行，只留一条永远 pending 的 intent

- **级别**: **P1**（审计完整性）
- **我的工作流**: 事后取证时，我最需要的就是失败/超时的调用——那正是事故现场。我要能查到
  「这次调用超时了、是谁、什么工具、什么错误码」。
- **发生了什么**: 一次工具超时的调用，客户端拿到了 `upstream_timeout` + `request_id`，但库里
  只有一条 `intent/pending`，**没有 outcome**，服务端日志也没有任何记录（审计写失败被丢弃）。

  单次超时（fake 上游 sleep 35s，工具声明超时 30s）：

  ```text
  elapsed 30.0s 200 {"code": "upstream_timeout", "details": {"timeout": "30s"}, "message": "tool exceeded its declared timeout"}
  request_id in _meta: 9c174a2461f65237ba755ab2e457cf9a
  ```

  ```sql
  select seq,phase,state,decision,error_code from audit_events
   where request_id='9c174a2461f65237ba755ab2e457cf9a';
  --  244 | intent | pending | pending |
  ```

  限流风暴（40 并发、`1 rps / burst 1 / max_concurrent 1`）：30 成功 + 10 `rate_limited`，
  那 10 次调用全部没有 outcome：

  ```text
  elapsed 30.0s Counter({None: 30, 'rate_limited': 10})
  fake stats: {"count": 43, "max_active": 1}
  ```

  ```sql
  select seq,occurred_at,instance_id,tool from audit_events i
   where i.phase='intent' and not exists (select 1 from audit_events o
         where o.phase='outcome' and o.request_id=i.request_id) order by i.seq desc limit 10;
  --  213..204 | 2026-11-21 19:30:34.57+00 | fake | magento.product.get   （10 条，同一秒）
  ```

  网关日志（`server.log`）在整个过程中只有启动两行，没有审计写失败的告警：

  ```text
  {"time":"...","level":"INFO","msg":"durable audit and confirmations enabled","store":"postgresql"}
  {"time":"...","level":"INFO","msg":"gateway listening","addr":"127.0.0.1:18121",...}
  ```

- **我期望什么**: 超时/限流的调用必须留下 `outcome: failed/error` + `error_code=rate_limited|upstream_timeout`；
  即使审计写失败，也必须进日志（现在连失败都没人知道）。
- **根因（代码）**: `internal/mcp/server/server.go:339` 用 `context.WithTimeout` 派生 ctx 后把它交给
  工具，超时后仍用**同一个已过期的 ctx** 调 `recordOutcome(opts, ctx, ...)`（`server.go:397`），
  而 `cmd/server/main.go:129-142` 的审计回调是 `_ = auditor.Record(ctx, ...)`——`ctx` 已过期导致
  INSERT 失败，错误被丢弃。
- **影响**: 事故/限流期间正是审计最该完整的时刻，而现场恰好缺失；且「intent 永远 pending」会让
  任何按「intent 无 outcome」做的告警失效（因为平时也会出现，见 P-SEC-02）。
- **状态**: **CONFIRMED**。

### P-SEC-02: 五个自助记账资金工具的审计行不可关联，并把人类调用者记成 service、把失败记成 allow

- **级别**: **P1**（审计完整性/归因错误）
- **我的工作流**: 资金工具（invoice.capture / invoice.create / creditmemo.create / order.create /
  order.cancel）是我最关心的一类。我要能说清：这笔钱是谁批的、谁执行的、成功还是失败。
- **发生了什么**: 这类工具在自己的账本事务里写审计行（`operation_ledger.go:443/479/514` 的 SQL），
  结果与中间件约定不一致，一次调用留下三条互相对不上的信息：

  ```sql
  select seq,phase,state,decision,scope,identity_class,error_code,request_id is null as req_null
    from audit_events where tool='magento.invoice.capture' and instance_id='fake' order by seq;
  ```

  ```text
  132 | intent   | pending   | pending |       | human    |          | f     <- 中间件 intent
  133 | outcome  | failed    | allow   | self  | service  | conflict | t     <- 工具自己写的 outcome
  137 | intent   | pending   | pending |       | human    |          | f     <- 悬空
  141 | intent   | pending   | pending |       | human    |          | f     <- 悬空
  142 | outcome  | succeeded | allow   | self  | service  |          | t
  152 | intent   | pending   | pending |       | human    |          | f     <- 悬空
  153 | outcome  | succeeded | allow   | self  | service  |          | t
  ```

  四个问题：

  1. **intent 悬空**：每次调用中间件先写一条 intent（有 request_id），工具再写自己的 outcome
     （request_id 为 NULL），中间件因为 `MarkOperationOwned` 不再写 outcome → intent 永远 pending。
     全库 15 条悬空 intent 里有 4 条是这类（其余 11 条见 P-SEC-01）。
  2. **outcome 无 request_id**：无法与 intent、也无法与客户端 `_meta.request_id` 关联，只能靠
     subject+tool+时间猜。v0.11.6 的 changelog 写的是「ledger outcomes carry the request id」，
     对中间件 gated 路径成立，对这五个自助记账工具**不成立**。
  3. **`identity_class` 恒为 `service`**：这几次调用的主体是 `operator@test`（human，见 seq 132 的
     人类 intent 行与 `_meta.subject`），但工具写的行硬编码 `'service'`：

     ```sql
     select seq,identity_class,scope,decision,state,error_code from audit_events
      where identity_class='service' order by seq;
     --  59 | service |       | pending   | pending   |        （agent-bot@test，正确）
     --  60 | service |       | allow     | succeeded |        （agent-bot@test，正确）
     -- 133 | service | self  | allow     | failed    | conflict   <-- operator@test，人类
     -- 142 | service | self  | allow     | succeeded |            <-- operator@test，人类
     -- 153 | service | self  | allow     | succeeded |            <-- operator@test，人类
     ```

  4. **失败记成 `decision=allow`**：seq 133 是一次失败的 capture（`state=failed`、
     `error_code=conflict`），但 `decision` 是 `allow`。SEM-SEC-03 修好了「Begin 之前失败」的
     路径（见 §7），但「Begin 之后失败」仍然写成 allow——审计里出现
     `outcome|failed|allow` 这种自相矛盾的行（全库仅 1 条，就是它）。
- **我期望什么**: 一条调用 = 一条可配对、可归因的审计轨迹。至少：工具写的那一行要带上
  `request_id` 和真实 `identity_class`；`decision` 要随 state 走（失败 → `error`）；
  或者干脆让工具不写、由中间件在账本事务提交后写（REL-16 的设计目标）。
- **代码位置**: `internal/storage/operation_ledger.go:443-448`（succeeded）、`:479-484`（failed）、
  `:514-519`（unknown）——三条 SQL 都硬编码 `'service'`、`'allow'`，且不写 `request_id`；
  `internal/magento/operations.go:243-245`（`audit.MarkOperationOwned`）让中间件放弃写 outcome。
- **影响**: 资金路径的审计无法回答「谁执行的」和「这次成功了吗」；`identity_class=service` 会让
  取证者以为这是 agent 自动行为而不是人批准的写操作。
- **状态**: **CONFIRMED**。

### P-SEC-04: 审批行与操作行不可关联（`confirmation_id`/`role_name`/`params_hash` 从不写入）

- **级别**: P2
- **我的工作流**: 「谁批准了哪一次操作」是我最常被问到的问题。审批行应该带 confirmation id，
  操作行也应该带。
- **发生了什么**: 全库 253 行 `audit_events`，`confirmation_id` 全为 NULL、`role_name` 全为 NULL、
  `params_hash` 全为 NULL：

  ```sql
  select count(*) filter (where confirmation_id is not null) with_conf,
         count(*) filter (where role_name is not null) with_role,
         count(*) filter (where params_hash is not null) with_params,
         count(*) filter (where scope is not null) with_scope, count(*) total
    from audit_events;
  --  with_conf | with_role | with_params | with_scope | total
  --         0 |         0 |           0 |          2 |   253
  ```

  审批行只有 `tool/instance/scope/decision=approve/state=approved/subject_hash`：

  ```text
  55 | magento.product.delete | demo | approval | approved | approve | destructive
  58 | magento.product.update | demo | approval | approved | approve | write
  ```

  也就是说：同一 tool+instance 有两次审批时，审计里两条 approve 行**完全无法区分**（没有 id、
  没有参数哈希、没有时间之外的区别），也无法对应到是哪一次调用消费了它。`operations` 表里其实
  有 `confirmation_id`（`operation_ledger.go`），但审计表没写。
- **我期望什么**: 审批行带 `confirmation_id`（以及 `identity_class`，见 P-SEC-03），gated 调用的
  intent/outcome 也带 `confirmation_id`；或者至少在文档里明确「审批与调用的关联只能通过
  `operations` 表做」，不要把「可追溯」说成审计表的能力。
- **代码位置**: `cmd/admin/main.go:165-179`（`recordApproval` 不设 `ConfirmationID`/`IdentityClass`/
  `Role`）、`cmd/server/main.go:129-142`（审计回调只填 subject/class/tool/instance/decision/phase/
  state/error_code/request_id）。`docs/database-design.md:140-152` 把这些列列为表结构的一部分，
  但没有标注「不写」。
- **状态**: **CONFIRMED**。

### P-SEC-05: `instance` 在 schema 校验前原样落库（审计污染/伪造归因）

- **级别**: P2
- **我的工作流**: 审计表是我的证据链。调用者不该能决定自己这条记录「写在哪个实例名下」。
- **发生了什么**: 中间件在授权**之前**就写 intent，`instance` 直接取参数原文，SDK 的 schema
  校验（pattern `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`）发生在之后。于是 5000 字符、换行、任意文本
  都能进 `audit_events.instance_id`：

  ```bash
  python3 - <<'EOF'
  # viewer 调 magento.product.get，instance 分别为 "A"*5000 与 "demo\nFORGED audit line: subject=admin"
  EOF
  ```

  ```text
  long instance -> http 200 code permission_denied
  newline instance -> http 200 code permission_denied
  ```

  ```sql
  select seq, length(instance_id) len, left(replace(instance_id,E'\n','<NL>'),60) inst, phase, decision
    from audit_events where length(instance_id)>20 or instance_id like '%FORGED%' order by seq;
  --  116 | 5000 | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA | intent  | pending
  --  117 | 5000 | AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA | outcome | deny
  --  118 |   37 | demo<NL>FORGED audit line: subject=admin                     | intent  | pending
  --  119 |   37 | demo<NL>FORGED audit line: subject=admin                     | outcome | deny
  ```

  `instance_id` 里已经积累了一批不存在/恶意的值：`../../demo`、`../demo`、`Demo_1`、`demo2`、
  `nope`（全部 `deny`，不会变成 allow，但归因被污染）。`tool` 同理：`magento.does.not.exist`
  这种不存在的工具名也会原样落库（seq 63）。
- **我期望什么**: 审计写入前对 `instance`/`tool` 做长度与字符集归一化（未知值记 `invalid`），
  或者把 intent 写在校验之后；任何情况下都不该允许换行符进入一个会被导出到 SIEM/CSV 的字段。
- **代码位置**: `internal/mcp/server/server.go:229-235`（先 `opts.Audit(..., "intent", ...)`，后
  `authorizeCall`；schema 校验在 handler 内更晚）。
- **影响**: 审计表可被写入任意字符串（体积、索引膨胀、日志注入式伪造行）；按 instance 做的取证
  查询会被噪声污染；不能作为合规证据。
- **状态**: **CONFIRMED**。

### 3.5 失败是否记为失败（回归）

- 中间件 gated 路径：`magento.product.delete`（SKU 不存在）→ `outcome|failed|error|not_found` ✅
- 自助记账工具、失败发生在 Begin 之前：`magento.invoice.capture`（invoice 不存在）→
  `outcome|failed|error|not_found` ✅（SEM-SEC-03 已修，见 §7）
- 自助记账工具、失败发生在 Begin 之后：`outcome|failed|allow` ❌（P-SEC-02）
- 授权拒绝：`outcome|denied|deny|permission_denied` ✅
- 审批缺失/不匹配：`outcome|denied|deny|confirmation_required` ✅

---

## 四、PII：有没有不该出现的字段

### 4.1 审计表本身（正常）

`audit_events` 只存 `subject_hash`（SHA-256），不存邮箱、不存参数原文（`params_hash` 未使用）：

```sql
select distinct identity_class from audit_events;   -- human / service / (NULL)
select count(*) from audit_events where tool like '%customer%';  -- 2（只是工具名，没有客户字段）
```

代价是无法直接读出「谁」——需要拿候选 subject 去比对哈希。这是可接受的最小化取舍，但配合
P-SEC-04（没有 role_name）会让取证很费力。

### P-SEC-07: 每个结果都回显调用者邮箱

- **级别**: P3
- **我的工作流**: 我把工具结果转给客服/运营时，不希望业务数据里夹带调用者身份。
- **发生了什么**: 所有 tools/call 结果的 `_meta` 带 `subject`（IdP 邮箱/subject）：

  ```text
  "_meta": {"instance": "demo", "request_id": "54f8085ac1b4b8b6ec88c2580cb1fde6",
            "subject": "viewer@test", "tool": "magento.product.get"}
  ```

  匿名调用（生产 18110）时没有 `subject` 字段。
- **我期望什么**: 只回 `request_id`/`instance`/`tool`，或用不可逆哈希；若保留，文档要说明
  「结果里含调用者身份，转发/缓存时按 PII 处理」。
- **代码位置**: `internal/mcp/server/server.go:563-566`。
- **状态**: **CONFIRMED**（`docs/audit/remediation-plan.md` 的 MCP-08 把 `subject` 列为协议关联字段，
  属有意设计；作为 PII 最小化问题记录）。

### 4.2 日志（正常）

```bash
grep -c "Bearer" server.log     # 0
grep -ci "eyJ" server.log       # 0
grep -c "@test" server.log      # 0
```

日志里没有令牌、JWT、邮箱、产品/客户数据；`upstream diagnostic` 只在 WARN 级别按需打印，
本次测试没有触发含标识符的上游原文。

### P-SEC-08: `upstream_message` 可能带未替换的 `%1` 占位符

- **级别**: P3
- **我的工作流**: 我看到 `validation_failed` 想知道到底哪个字段不对；占位符和乱码都让我无法转述给运营。
- **发生了什么**: Magento 的 `parameters` 是**数组**时（不是对象），占位符不被替换，原文带 `%1` 透出：

  ```bash
  # 审批后执行 magento.product.update（SKU 不存在），Magento 返回：
  curl ... -X PUT .../rest/pl/V1/products/NO-SUCH-SKU -d '{"product":{"sku":"NO-SUCH-SKU","name":"a"}}'
  # {"message":"Nieprawid\u0142owe dane produktu: %1","parameters":["Invalid attribute set entity type"]}
  ```

  ```text
  "details": {"store_code": "pl", "upstream_message": "Nieprawidłowe dane produktu: %1", "upstream_status": "400"}
  ```

  脱敏本身是有效的（`scrubUpstreamText` 把邮箱替换成 `<email>`、32+ 字符令牌替换成 `<redacted>`、
  去掉控制字符、截断 300 runes），这次也没有泄漏标识符；问题只是占位符没解析，消息不可用。
- **我期望什么**: `upstreamParamMap` 支持数组形式的 `parameters`（按位置替换 `%1`/`%2`），
  或干脆不带占位符的消息不输出。
- **代码位置**: `internal/magento/client.go:529-541`（`upstreamParamMap` 只接受 JSON 对象）。
- **状态**: **CONFIRMED**（内容质量，非泄漏）。

### P-SEC-09: SSE 响应缺 `charset=utf-8`

- **级别**: P3
- **我的工作流**: 波兰店，商品名/错误信息里全是 `ł`、`ą`、`ę`。我用 Python 客户端读结果，看到的是
  `NieprawidÅ‚owe dane produktu`——第一反应是「网关把数据弄坏了」。
- **发生了什么**: 响应头是 `Content-Type: text/event-stream`（无 charset）。按 HTTP 规范，
  `text/*` 无 charset 时默认 ISO-8859-1，`requests` 就按它解码：

  ```text
  content-type: text/event-stream encoding: ISO-8859-1
  raw bytes: b'upstream_message":"Nieprawid\xc5\x82owe dane produktu: %1"'
  utf8 decode ok: upstream_message":"Nieprawidłowe dane produktu: %1"
  ```

  字节本身是合法 UTF-8，问题只在头部没声明。
- **我期望什么**: `Content-Type: text/event-stream; charset=utf-8`。
- **状态**: **CONFIRMED**（互操作/数据完整性；对非 ASCII 店铺有实际影响）。

### 4.3 客户数据（工具职责，非缺陷）

`magento.customer.*` 会返回邮箱/姓名/地址——这是工具用途。需要说明的是它**不进**审计表、
不进日志（见 4.1/4.2），`_meta` 里只有调用者自己的 subject。

---

## 五、注入：`instance`、SKU、导入负载

### P-SEC-10: 注入面探测（NOT-A-DEFECT）

```bash
python3 - <<'EOF'
# SKU / instance / 导入负载 注入探测
EOF
```

```text
1 CSV 公式注入 + SSRF URL + 路径穿越：
  {"preview":{"format":"csv","total":2,"valid":0,"invalid":2,
   "errors":[{"row_number":2,"field":"sku","message":"invalid or missing SKU"},
             {"row_number":3,"field":"sku","message":"invalid or missing SKU"},
             {"row_number":3,"field":"description","message":"invalid_argument: description must not contain control characters"}],
   "warnings_truncated":false}}
2 JSON 负载（__proto__ / 嵌套对象 / 500 字符 / 邮箱）：{"code":"validation_failed","message":"JSON import must be an array of objects"}
3 instance 大写/下划线/穿越：permission_denied（授权在 schema 之前拦下）
4 空负载：invalid_argument
```

- CSV 里的 `http://169.254.169.254/latest/meta-data/`、`file:///etc/passwd`、`../../etc/passwd`
  只被当作字段值校验，**没有任何 fetch**（导入路径不解析 URL）。
- 控制字符（`\n`、`\x1b`）在 `description` 字段被拒（`must not contain control characters`），
  错误消息不回显负载原文（无日志注入）。
- `sku="../../V1/orders/1"` → schema `invalid_argument`（pattern `^[A-Za-z0-9][A-Za-z0-9._:-]*$`）。
- `instance="../demo"` / `"../../demo"` / `"Demo_1"` → `permission_denied`：v0.11.6 的授权在 schema
  校验之前就把未配置实例拦下了（v0.11.5 因为默认策略是通配实例，才会走到 schema 报错）。schema 的
  pattern 仍在，作为第二层。**但审计行已经带着这个值落库**（见 P-SEC-05）。
- 顺带发现 schema 不一致（非安全）: 导入类工具的 `instance` pattern 是 `^[a-z0-9][a-z0-9-]{0,63}$`，
  其他 magento 工具是 `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`。一个有大小写/下划线的实例 id 会让
  导入工具全部不可用（授权层先拒绝，所以本次无法实测到 schema 报错，属代码层观察）。

### P-SEC-11: 重放/审批绕过（NOT-A-DEFECT）

见 §2.5：跨主体重放被审批闸门与 `confirmation_used` 挡住，上游计数未增加。

### 5.3 生产端点（部署风险，非代码缺陷）

```bash
python3 /tmp/sec-v0116/mcp.py '' platform.info '{}'
# version=v0.11.6 writes_enabled=true
python3 /tmp/sec-v0116/mcp.py '' magento.product.delete '{"instance":"demo","sku":"NO-SUCH-SKU"}'
# {"code":"confirmation_required","details":{"confirmation_id":"84241364-036b-441f-acbd-9955012625ee",...}}
```

生产 `18110` 是 `auth.enabled=false` 且 `writes_enabled=true`：任何能连到该端口的人都可以
（a）调用所有读工具；（b）发起写/破坏性调用并拿到 `confirmation_id`。审批仍需 `mage2mcp-admin`
- 数据库访问，所以不是「匿名可删库」，但**没有 RBAC、没有身份、审计里 subject 是 `anonymous`**。
这是部署配置问题（前一轮已记录），不是 v0.11.6 代码缺陷；本轮未在该端点执行任何写操作。

---

## 六、生产/本地版本一致性

```text
platform.info @ 18110 -> {"name":"mage2mcp","version":"v0.11.6","writes_enabled":true}
```

本地构建来自同一 commit `6717d2b`，授权/审计结论对生产代码成立（生产只是没开鉴权）。

---

## 七、v0.11.5 遗留项回归（均已修复）

| 旧 ID | 结论 | 证据 |
|---|---|---|
| SEM-SEC-01 跨实例授权 | **FIXED** | 两实例启动即失败：`rbac: magento has 2 instances (demo, demo-alt); ...`；未配置实例 `permission_denied` |
| SEM-SEC-02 审批无审计行 | **FIXED** | `audit_events` 出现 `phase=approval, decision=approve`（11 行），CHECK 约束已含 `approve`（`000009_audit_approval_event`） |
| SEM-SEC-03 资金工具失败记 allow/succeeded | **FIXED（部分）** | Begin 之前失败 → `outcome/failed/error/not_found`（demo invoice.capture, seq 79）。Begin 之后失败仍为 `failed/allow`，见 P-SEC-02 |
| SEM-SEC-04 限流被超时覆盖 | **FIXED** | 40 并发风暴返回 10×`rate_limited`，无 `upstream_timeout`（`internal/mcp/server/server.go` 的 `resultCode != CodeRateLimited` 分支） |
| SEM-SEC-05 `_meta.subject` | 保留 | 见 P-SEC-07 |
| SEM-SEC-06 注入面 | 复核通过 | 见 P-SEC-10 |
| SEM-SEC-07 令牌/日志泄漏 | 复核通过 | 见 §1.2、§4.2 |

---

## 八、未覆盖与限制

1. **REL-16-R 崩溃窗口**（自助记账工具「工具提交后、审计写入前」崩溃会丢审计行）没有实测——
   需要注入进程崩溃，属可靠性测试范畴；本轮只证明了同路径上的**非崩溃**缺陷（P-SEC-02）。
2. **SEC-R01**（`magento.import.batches` 列表只按实例、不按 subject 过滤）没有实测：需要 worker
   真正创建 batch 才能复现，本环境没有 worker（`import.start` 只返回 `queued:true`，`import.batches`
   为空）。文档已记录为 P3。
3. **限流/超时**只在 fake 上游上测，未在生产 Magento 上制造压力。
4. 审批 CLI 的 `MAGE2MCP_ADMIN_TOKEN` 环境变量形式没有测（`ps` 可见性属部署注意项，文档已提示
   用 `-token-file`）。

## 九、建议优先级

1. **P-SEC-03（P1，越权）**：审批人必须与 RBAC 同源；`auth.Identity.Class` 已经算出来了，用它
   拒绝 `service` 审批（ADR-0014 的承诺），并给审批人一个明确的角色要求。
2. **P-SEC-01（P1）**：审计写不能用已过期的请求 ctx；写失败必须进日志（现在完全静默）。
   一行 `context.WithoutCancel(ctx)` + 一条 `logger.Error` 就能堵住「事故现场没有审计」。
3. **P-SEC-02（P1）**：资金工具的三条审计 SQL 补 `request_id`、真实 `identity_class`，`decision`
   随 state 走；或改成中间件在账本事务后统一写。
4. **P-SEC-04 / P-SEC-05（P2）**：审批行补 `confirmation_id`/`identity_class`；`instance`/`tool`
   写审计前归一化。
5. **P-SEC-06（P3）**：授权判定加 module 维度，或让 Cloudflare 工具校验 instance 属于本模块。
6. **P-SEC-07/08/09（P3）**：`_meta.subject` 去标识化；`parameters` 数组支持；SSE 加 `charset=utf-8`。

## 十、复现环境清理

本地鉴权实例（18120）、fake 上游实例（18121）、fake Magento（19091）、JWKS（18443）与隔离库
`mage2mcp_sec_v0116` 都在 `/tmp/sec-v0116/` 和本地 PostgreSQL；不修改产品代码。生产端点只做了
只读查询与一次「必然返回 `confirmation_required`、不产生变更」的 destructive 探测（SKU 不存在）。
