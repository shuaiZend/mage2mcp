# 交叉验证：文档与代码一致性

- **被验证对象**：t65（文档 14 条修复）与 t70（RBAC 实例隔离文档），并特别复核 t74 刚改动的多实例启动行为。
- **验证者立场**：独立交叉验证，**不改任何文档或代码**。每条结论只以「代码实际行为」为准，不以文档的自我声明为准。
- **验证时间**：2026-11-22
- **工作树**：`v0.11.5-4`（含 t62/t65/t67/t70/t74 的未发布改动）
- **判定口径**：每条给出「文档原文 → 代码位置 → 是否一致」。不一致即记为发现，**包括文档仍在描述已修复的旧行为**。

## 一、结论摘要

| # | 验证项 | 文档声明 | 代码实际 | 一致？ |
|---|---|---|---|---|
| 1 | 限流器数字与归属 | per-instance 50 rps / burst 100 / 8 并发 | `limits.go` 常量精确一致；limiter 在 `client.go` 构建 | ✅ 一致（归属描述有偏差，见 XV-02） |
| 2 | 会话绑定 | 已绑定到 subject | `sessionOwners.bind/allows` 按 subject 绑定并拒绝他人 | ✅ 一致 |
| 3 | Sales 读写 | 读写（含确认） | 5 类写工具确实注册为 write/destructive | ✅ 一致 |
| 4 | 产品类型 | 仅 simple / virtual | `type_id` enum 恰为 `["simple","virtual"]` | ✅ 一致 |
| 5 | 多实例启动拒绝 | 认证关闭时**也会**拒绝启动（「已知 wiring 问题」） | 认证关闭时**不构建策略、不拒绝**（t74 已修） | ❌ **不一致（XV-01）** |
| 6 | 6 处「已修复」声明 | 见 §3 | 逐条在代码中确认已修 | ✅ 全部一致 |

**最重要的一条**：XV-01。三份文档仍把「多实例在认证关闭时也拒绝启动」当作**当前行为**描述，并把它称为「已知 wiring 问题」——而 t74 已经修好。代码是对的，文档停留在修复前。

**未发现「文档对、代码错」被改文档掩盖的情况**：本次唯一的不一致方向是**文档落后于代码**（代码更正确），不是文档被改成迁就错误代码。

---

## 二、逐项对照

### 2.1 限流器：per-instance 50 rps / burst 100 / 8 并发

**文档原文**（`docs/integrations/magento.md`、`docs/architecture.md`，t65 修复后）：

> Magento 的出站请求按实例限流：每秒 50 次、突发 100、最多 8 个并发。

**代码位置**：

| 事实 | 位置 | 实际值 |
|---|---|---|
| 默认值常量 | `internal/magento/limits.go:39-41` | `DefaultRequestsPerSecond = 50`、`DefaultBurst = 100`、`DefaultMaxConcurrent = 8` |
| 默认值组装 | `internal/magento/limits.go:48-51` | `DefaultLimits()` 返回上述三项 |
| 按实例构建 | `internal/magento/client.go:178` | `limiter: newLimiter(cfg.Limits)` |
| 每次请求取令牌 | `internal/magento/client.go:198` | `release, err := c.limiter.acquire(ctx)` |
| 配置映射 | `cmd/server/main.go:326-332` | `magentoLimits(l *config.InstanceLimits)`；`nil` → `DefaultLimits()` |

**判定**：✅ **一致**。数字、per-instance 语义、默认开启（未配置也有限流）三项都与代码相符。

---

### 2.2 会话绑定：MCP session 绑定到创建者的 subject

**文档原文**（`docs/mcp-conformance.md` S2/A5、`docs/security/threat-model.md`）：

> 会话绑定到创建它的 subject；另一个 subject 复用同一 session id 会被拒绝。

**代码位置**：

| 事实 | 位置 |
|---|---|
| 绑定表与上限 | `internal/mcp/server/session.go:16-21`（`maxTrackedSessions`） |
| 记录创建者 | `session.go:38-53`（`bind(sessionID, subject)`） |
| 校验归属 | `session.go:56-68`（`allows`：无 owner 拒绝；不同 subject 拒绝；相同 subject 放行） |
| 在请求路径上调用 | `session.go:71-96`（`sessionBinding` 从请求取 subject 后绑定/校验） |

**判定**：✅ **一致**。

---

### 2.3 Sales：读写（写操作需确认）

**文档原文**（`docs/integrations/magento.md:21` 及 §Sales，t65 修复后）：

> Sales 既读也写：invoice / shipment / creditmemo / order create / order cancel 五类写操作，均需人工确认。

**代码位置**（`internal/magento/write_tools.go`，均通过 `mcpWriteTool` + `writeToolMetadata` 注册为写权限）：

| 工具 | 常量 / 注册位置 |
|---|---|
| `magento.invoice.create` | `:984` 常量，`:1022` 写元数据，`:1024` 注册 |
| `magento.shipment.create` | `:1052`，`:1126`，`:1128` |
| `magento.creditmemo.create` | `:1057`，`:1153`，`:1155` |
| `magento.order.create` | `:1641`，`:1718`，`:1720` |
| `magento.order.cancel` | `:380`（schema 表）；`orderCancelToolName` 注册于写工具集 |
| `magento.order.hold` / `unhold` | `:1190` / `:1194`（同一 schema），`:1249` 起注册 |

**判定**：✅ **一致**。文档所说的五类写操作在代码中确实以写权限注册，不是只读。

---

### 2.4 产品类型：只允许 simple / virtual

**文档原文**（`docs/magento-fields.md`、`docs/integrations/magento.md`，t65 修复后）：

> 只支持 simple 与 virtual；bundle、downloadable 会被明确拒绝，因为它们的选项/链接无法通过本网关创建。

**代码位置**：`internal/magento/write_tools.go:94`

```go
typeIDProp = `"type_id": {"type": "string", "enum": ["simple", "virtual"],
  "description": "Product type. bundle and downloadable are rejected: ..."}`
```

**判定**：✅ **一致**。enum 恰为两项，且描述里明确写出拒绝理由。

---

### 2.5 多实例启动拒绝（t74 刚改动）— **不一致**

**代码实际行为**（先看代码，再看文档）：

`cmd/server/main.go:275-284`：

```go
func defaultPolicy(cfg config.Config) (*rbac.Policy, error) {
    if !cfg.Auth.Enabled {
        return nil, nil          // 认证关闭：不构建策略，因此不触发多实例拒绝
    }
    return rbac.DefaultPolicy(configuredInstances(cfg))
}
```

`internal/security/rbac/rbac.go:170-175`：一个模块配置了多个实例时返回错误——**只有当 `rbac.DefaultPolicy` 被调用时才会到达**，即只在认证开启时。

结论：**t74 之后，`auth.enabled: false` 的多实例部署正常启动**；拒绝只发生在认证开启时。

**文档原文（三份，均描述修复前的行为）**：

| 文档 | 行 | 原文 | 与代码 |
|---|---|---|---|
| `docs/configuration.md` | 100-102 | 「`cmd/server` builds `rbac.DefaultPolicy(...)` at startup (**currently before the `auth.enabled` branch**)」 | ❌ 已不成立 |
| `docs/configuration.md` | 116-127 | 「**Multiple instances in one module fail startup.** … The same check **runs before the `auth.enabled` branch today**, so it also rejects a multi-instance module **with authentication disabled**; that is a **known wiring issue**: policy construction should move inside the auth-enabled branch.」 | ❌ 三处均不成立：①检查已移入 auth 分支；②认证关闭时不再拒绝；③「wiring issue」已修 |
| `docs/architecture.md` | 217-219 | 「The policy is **currently constructed before the `auth.enabled` branch**, so a multi-instance module also fails **with authentication disabled**; that is a **reported code issue**」 | ❌ 同上 |
| `docs/security/threat-model.md` | 225-228 | 「**Current wiring note.** `cmd/server` builds the policy before the `auth.enabled` branch, so a multi-instance module also fails startup **with authentication disabled**. The intended behavior is auth-enabled only; this is a **reported wiring issue**」 | ❌ 同上 |
| `docs/security/threat-model.md` | 592 | 「the default policy binds one instance per module; **a multi-instance module fails startup**」 | ⚠️ 缺「认证开启时」限定，按字面已不成立 |

**判定**：❌ **不一致（XV-01）**。文档把已经修好的 wiring 问题写成「当前行为 + 已知问题」，四处位置需要按「认证开启时才拒绝」重写。

**连带影响**：`docs/configuration.md:141-143` 的建议——

> Do not disable authentication to work around the startup error

——在 t74 之后与事实相反：关闭认证**现在确实**能绕过该拒绝（代价是失去授权，仍只允许 loopback）。建议应改为「关闭认证可以绕过，但那会失去授权检查，因此不推荐」，而不是「不要这么做，因为它不管用」的暗示。

---

## 三、「已修复」声明抽查（6 条，全部命中）

| # | 声明（来源） | 代码位置 | 结论 |
|---|---|---|---|
| 1 | `inventory.unassign` 改用 `POST /inventory/source-items-delete`（v0.11.3） | `internal/magento/inventory.go:156` | ✅ 确为 POST + 该路由 |
| 2 | `stock_source_unlink` 改用 `POST /inventory/stock-source-links-delete`（v0.11.3） | `internal/magento/inventory.go:696`（注释 `:682`） | ✅ 确为 POST + 该路由 |
| 3 | `stock_source_link` 发送 `priority` 而非 `position`（v0.11.3） | `internal/magento/inventory.go:678` `json:"priority"` | ✅ 字段名正确，且注释写明旧字段会被 Magento 拒绝 |
| 4 | 客户地址的 `region` 按对象解码（v0.11.3） | `internal/magento/customer.go:733` 起 | ✅ 注释与解码逻辑均按 `{region_code, region, region_id}` |
| 5 | `customer.addresses.update` 在无专用路由时回退到 `PUT /customers/{id}`（v0.11.3） | `internal/magento/customer.go:603` 起 | ✅ 回退路径存在 |
| 6 | 导入的 `status` / `visibility` 映射为顶层字段并校验（v0.11.5） | `internal/magento/import.go:654-657` | ✅ 两者在顶层保留列中，不再作为属性发送 |

**判定**：6/6 与代码一致。这些「已修复」声明不是自我宣称——每一处都能在代码里找到对应实现。

---

## 四、发现清单

### XV-01: 三份文档仍把「多实例在认证关闭时也拒绝启动」当作当前行为（P1）

- **严重级别**: P1
- **类型**: 文档落后于代码（代码已修，文档未跟）
- **位置**: `docs/configuration.md:100-102`、`:116-127`；`docs/architecture.md:217-219`；`docs/security/threat-model.md:225-228`、`:592`
- **现象**: 五处文本断言「策略在 `auth.enabled` 分支之前构建」，因此「认证关闭时多实例也拒绝启动」，并把这件事称为「known wiring issue / reported code issue」。t74 已把策略构建移入认证开启分支（`cmd/server/main.go:275-284`），该断言不再成立。
- **期望**: 改写为「认证开启时，一个模块配置多个实例会拒绝启动；认证关闭时不做授权决策，因此不触发该规则」。同时修正「不要靠关闭认证绕过」的措辞。
- **影响**: 运维按文档判断部署可行性会得出错误结论——以为关闭认证的多店铺开发环境也会启动失败；反之，也会误以为「关闭认证绕过」这条路根本不存在。
- **状态**: **CONFIRMED**（代码与文档直接矛盾，两侧均已取证）

### XV-02: 限流器的「构建位置」描述与代码不符（P3）

- **严重级别**: P3
- **类型**: 归属描述偏差（数字与语义正确）
- **位置**: 文档称 `cmd/server` 构建 per-instance limiter（t65 修复文本）；实际 `cmd/server/main.go:326-332` 只做 `config → magento.Limits` 映射，limiter 由 `internal/magento/client.go:178` 构建、`:198` 取令牌。
- **现象**: 读者会以为限流器在 server 层，实际在 Magento client 内，且「未配置也有限流」来自 `DefaultLimits()` 而非 server 判断。
- **期望**: 把构建位置改为「每个实例的 Magento client 在构造时按 `limits` 建立 limiter；`cmd/server` 只负责把配置映射为 `magento.Limits`」。
- **影响**: 低——数字正确，不影响运维判断；影响的是排障时找错文件。
- **状态**: **CONFIRMED**（轻微）

### 观察（不构成发现）

- `docs/configuration.md:270-274` 说 ADR-0018 的「positive-consequence bullet 仍夸大 multi-instance bindings」。核对 `docs/adr/0018-multi-instance-routing.md:80`：`Bindings and unified analytics work across real multi-instance deployments.` 确实仍在，而代码拒绝多实例统一绑定（`rbac.go:170-175`）。**文档的这条自我提醒是准确的**，属于已登记的已知文档债，不是新发现。
- t70 声称修正了 ADR 里「空 instance 作用域适用于所有实例」的旧句：核对 `grep "empty instance scope applies to all"` → 不存在。**已修正**。

---

## 五、方法与可复现命令

```bash
# 1) 限流器
grep -n "DefaultRequestsPerSecond\|DefaultBurst\|DefaultMaxConcurrent" internal/magento/limits.go
grep -n "newLimiter\|limiter.acquire" internal/magento/client.go

# 2) 会话绑定
grep -n "func (s \*sessionOwners) bind\|func (s \*sessionOwners) allows" internal/mcp/server/session.go

# 3) Sales 写工具
grep -n "invoiceCreateToolName\|shipmentCreateToolName\|creditmemoCreateToolName\|orderCreateToolName\|orderCancelToolName" internal/magento/write_tools.go

# 4) 产品类型 enum
grep -n "typeIDProp" internal/magento/write_tools.go

# 5) 多实例启动（t74 后的真实行为）
sed -n '275,284p' cmd/server/main.go
grep -n "func instanceModules" -A20 internal/security/rbac/rbac.go

# 6) 文档侧证据
sed -n '96,148p' docs/configuration.md
sed -n '205,222p' docs/architecture.md
sed -n '220,232p' docs/security/threat-model.md
```

**验证者声明**：本报告未修改任何文档或代码；所有「一致/不一致」判定均以代码为唯一依据，文档原文仅作为被检对象引用。
