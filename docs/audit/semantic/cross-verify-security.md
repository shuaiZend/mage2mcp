# 交叉验证：实例隔离、审批审计、多实例启动（t75）

| 项目 | 值 |
|---|---|
| 验证者 | security-auditor（非 t62/t74 作者，未参与修复） |
| 被验证 | t62（实例隔离 + 审批审计约束）、t74（关闭鉴权时多实例启动回归） |
| 基线 | `c445b34 fix: semantic-layer remediation …`（修复提交）+ 其后的 docs-only 提交（验证时 HEAD 为 `3576d7d`） |
| 方法 | 自己生成密钥与 JWT、自己起真实二进制、自己执行审批、直接 psql 查库；不引用修复者的运行结果 |
| 结论 | **三个修复全部成立，未发现阻断问题**；附带一条 P3 交互缺陷（见 §6） |

环境：本地构建 `cmd/server` 与 `cmd/admin`；auth 实例监听 `127.0.0.1:18115`，issuer
`https://local.test`、audience `mage2mcp`、RS256 JWKS（本次新生成的密钥，kid `t75`）。
JWKS 通过 httpbin 的 base64 端点以公网 HTTPS 提供，原因是本机 Go 在 macOS 上使用平台
根证书校验，`SSL_CERT_FILE` 对本地自签证书不生效（实测 `x509: certificate signed by
unknown authority`）；换用公网受信证书后验证正常。Magento 实例 `demo` 指向 demo 店
（store `pl`），数据库为 `mage2mcp_sec`（已应用 000001–000009 迁移）。

## 1. viewer 对 demo 可读、对 demo-alt 被拒（真实 JWT、真实实例）

配置：`auth.enabled=true`，仅配置一个 Magento 实例 `demo`（见 §3：同一模块两个实例
在 auth 开启时按 t62 设计**拒绝启动**，因此隔离用「未配置的实例名」验证）。

viewer token（`sub=viewer@test`, `roles=["viewer"]`）调 `magento.tax.classes`：

```text
=== viewer -> magento.tax.classes {"instance":"demo"} ===
http 200
_meta: {"instance":"demo","request_id":"a59fd128…","subject":"viewer@test","tool":"magento.tax.classes"}
structuredContent: {"classes":[{"class_id":2,"class_name":"Taxable Goods","class_type":"PRODUCT"},
                               {"class_id":3,"class_name":"Retail Customer","class_type":"CUSTOMER"}]}
```

同一 token 调 `instance=demo-alt`：

```text
=== viewer -> magento.tax.classes {"instance":"demo-alt"} ===
http 200
isError True
structuredContent {"code": "permission_denied", "details": {"reason": "no matching grant"}, "message": "permission denied"}
_meta {"instance": "demo-alt", "subject": "viewer@test", "tool": "magento.tax.classes"}
```

`platform.info` 仍可读（`name=mage2mcp, version=dev`），写工具被拒：

```text
=== viewer -> magento.order.hold {"instance":"demo","order_id":1} ===
isError True
{"code": "permission_denied", "details": {"reason": "no matching grant"}, "message": "permission denied"}
```

判定：拒绝码是 **permission_denied**（不是 not_found/其他），且 demo 的真实读成功。
**通过。**

## 2. 真实审批 → 数据库里有 phase=approval 的审计行

operator token 发起需要确认的写操作，拿到 confirmation id：

```text
=== operator -> magento.order.hold {"instance":"demo","order_id":1} ===
isError True
{"code":"confirmation_required",
 "details":{"confirmation_id":"59d21e15-e565-4ed7-b51c-9a9a0edc744d","expires_at":"2026-11-21T18:21:10Z"},
 "message":"this operation requires approval by a different human"}
```

用 `cmd/admin` 以**另一个身份**（viewer@test，且带该身份的 JWT）审批：

```text
$ MAGE2MCP_ADMIN_TOKEN=$(cat token-viewer) mage2mcp-admin -config config-auth-single.yaml \
    -as viewer@test approve 59d21e15-e565-4ed7-b51c-9a9a0edc744d
approved 59d21e15-e565-4ed7-b51c-9a9a0edc744d by viewer@test   (exit 0)
```

直接查库（psql，不经任何网关代码）：

```text
$ select decision, phase, state, tool, coalesce(instance_id,'') as instance, occurred_at
  from audit_events where phase='approval' and tool='magento.order.hold' order by seq desc limit 3;
 decision |  phase   |  state   |        tool        | instance |         occurred_at
----------+----------+----------+--------------------+----------+------------------------------
 approve  | approval | approved | magento.order.hold | demo     | 2026-11-21 18:16:54.00083+00

$ select state, tool, coalesce(instance_id,''), approver_hash is not null
  from confirmation_tokens where id='59d21e15-…';
 approved | magento.order.hold | demo | t
```

判定：审计行**真的在库里**，`decision=approve`、`phase=approval`、`state=approved`，
并带 tool/instance；确认行同时变为 `approved` 且记录了 approver hash。
**通过。**

## 3. 多实例启动：两个方向都用真实二进制验证

用 `start_test.sh`（起进程 → 4s 后 curl /healthz → SIGTERM → wait 取退出码）：

```text
=== auth=false + 2 Magento instances ===
label=noauth-multi health=200 running=yes exit_after_sigterm=0
{"level":"INFO","msg":"shutting down","grace":"10s"}

=== auth=true + 2 Magento instances ===
label=auth-multi health=000 running=no exit=1
mage2mcp: rbac: magento has 2 instances (demo, demo-alt); the default policy binds one
instance per module. Grant per-instance policies explicitly, or call
DefaultPolicyAllInstances() to treat them as one trust domain
```

判定：关闭鉴权时多实例**能启动**（health 200）且能优雅退出（exit 0）；开启鉴权时
**exit 1**，错误信息点名两个实例、给出两条可执行出路（写 per-instance 策略 / 显式
`DefaultPolicyAllInstances()`）。**通过。**

（附：auth 开启 + 单实例的实例即 §1 用的 18115，启动成功、health 200，JWT 验证正常。）

## 4. `rbac.New` 真的拒绝无 instance 的 grant；空 instance 不再是通配

用 `go test -overlay` 注入一个探针测试（**不落盘到仓库**，`ls` 确认仓库无该文件）：

```text
PROBE no-instance grant -> err=rbac: grant 0 has no instance; name one, or use "*" for every instance
PROBE authorize demo     -> <nil>
PROBE authorize demo-alt -> permission_denied: permission denied
PROBE authorize empty    -> permission_denied: permission denied
PROBE wildcard demo-alt  -> <nil>
--- PASS: TestProbeNoInstanceGrantRejected
```

判定：无 instance 的 grant 在**构造期**被拒；只绑 `demo` 的策略对 `demo-alt` 与空串都
是 permission_denied；只有显式 `"*"` 才放行其它实例。**通过。**

## 5. 回归验证（仓库自带命令）

```text
$ go test -count=1 ./internal/security/... ./cmd/server/ ./internal/storage/
ok  internal/security/audit    0.131s
ok  internal/security/auth     0.100s
ok  internal/security/confirm  0.089s
ok  internal/security/jwt      1.067s
ok  internal/security/rbac     0.080s
ok  cmd/server                 0.309s
ok  internal/storage           0.097s

$ MAGE2MCP_TEST_DATABASE_URL=… go test -count=1 ./internal/storage/
ok  internal/storage           3.518s
```

（不带数据库 URL 时 storage 测试会 skip，因此额外跑了一次带 DB 的真实覆盖。）

## 6. 附带发现（不阻断，P3，建议记录不修）

**`mage2mcp-admin` 在已启用鉴权、且已提供 approver token 时仍强制要求 `-as`。**
`run()` 在加载配置之前就检查 `if as == "" { return "-as <approver identity> is required" }`，
而随后 `resolveApprover` 在鉴权开启时用的是**已验证 token 的 subject**，并会拒绝与
token 矛盾的 `-as`。于是实际使用必须同时给 `-as <与 token 一致的 subject>` 和 token：

```text
$ MAGE2MCP_ADMIN_TOKEN=… mage2mcp-admin -config … approve <id>
mage2mcp-admin: -as <approver identity> is required        # 即使 token 已提供
$ MAGE2MCP_ADMIN_TOKEN=… mage2mcp-admin -config … -as viewer@test approve <id>
approved <id> by viewer@test
```

影响：只是交互契约不一致（错误信息暗示 token 足够，实际还需要 `-as`）；安全语义没有
被削弱（身份仍来自已验证 token，`-as` 与 token 不一致会被拒）。建议后续把 `-as` 改为
「可选、仅在与 token 不一致时报错」，或在 usage 中写明两者都要给。

## 7. 未能验证 / 边界说明

1. **auth 开启 + 两个同模块实例的运行时拒绝**无法在运行态观察：按 t62 的设计，这种配置
   在启动阶段就被拒绝（§3 已实测）。因此「viewer 被 demo-alt 拒绝」是在**单实例运行态**
   用未配置的实例名验证，加上 §4 的库级探针（demo-only 策略拒绝 demo-alt）。两者合起来
   覆盖了「默认策略按实例绑定、空 instance 不通配」这一性质。
2. JWKS 用公网 httpbin 托管（原因见文首）；验证的是网关的 JWT 校验路径，与托管位置无关。
3. 审批审计验证的是 `cmd/admin` 的审批路径；网关侧「确认被消费时的审计」不在本任务范围。

## 8. 结论

- 实例隔离：**成立**（真实 JWT 下 demo 可读、demo-alt 为 permission_denied；库级探针
  证明无 instance 的 grant 在构造期被拒、空串不再通配）。
- 审批审计：**成立**（真实审批后 `audit_events` 有 `decision=approve, phase=approval,
  state=approved` 的行，确认行同步 approved）。
- 多实例启动：**两个方向都成立**（auth 关闭可启动并优雅退出；auth 开启 exit 1 且错误
  可操作）。
- 未发现阻断问题；§6 的 `-as` 交互问题为 P3，不影响安全语义。
