# 语义层测试 G：文档与配置一致性（docs-config）

**测试对象：** 生产 v0.11.5（工作树 `v0.11.5-4-g5e2749e`，2026-11-22）。
**方法：** 只读比对「文档/工具描述/输出 schema 说了什么」与「代码/注册表/实际输出做了什么」；
未修改任何代码或既有文档，只新增本报告。每条发现同时给出两侧证据。
**范围：** 工具描述、输出 schema、docs/ 数字承诺、configs/ 语义、版本标记、
`platform.info`、known-limitations 中「已修复」抽查。
**结论：** 发现 **14 条**（P1 4、P2 5、P3 5），其中 13 条 CONFIRMED、1 条 SUSPECTED；
另有 4 组 NOT-A-DEFECT 核对。最严重的是 v0.11.5 文档仍把已经实现的限流和会话绑定写成
PLANNED，以及产品类型/销售写能力仍按旧版本描述。

> 说明：本报告中的「文档说的」既包括 `docs/*.md` 的正文，也包括 agent 直接看到的工具
> 描述和输出 schema `description`；「实际做的」以当前工作树代码和测试输出为准。

---

## 1. 发现

### SEM-DOC-01: v0.11.5 文档仍称「没有限流器」，代码已有每实例令牌桶和并发上限

- **严重级别**: P1
- **工具**: 所有 Magento 工具（出站请求层）
- **文档说的**:
  - `docs/architecture.md:273-275`：「**No per-instance semaphore, token bucket, or
    quota limiter exists.** ... that is **PLANNED** (SEC-11, PERF-05)」；
    `:357` 仍把「Per-instance rate limiting, concurrency semaphores」列为 PLANNED。
  - `docs/operations/deployment.md:119`：「The gateway does not implement a rate limiter;
    limit concurrency at the agent or edge.」
  - `docs/integrations/magento.md:588-590`：「The gateway does **not** implement a
    per-instance concurrency semaphore, token bucket, rate limiter ... Those are
    **PLANNED** (SEC-11, PERF-05)」。
- **实际做的**:
  - `internal/magento/limits.go:39-41` 定义 `DefaultRequestsPerSecond=50`、
    `DefaultBurst=100`、`DefaultMaxConcurrent=8`；`:96-142` 实现令牌桶 + 信号量；
    `internal/magento/client.go:197` 在每次请求前 `limiter.acquire(ctx)`；
    `cmd/server/main.go:329` 把 `datasources.magento.instances[].limits` 映射进
    `magento.InstanceSettings.Limits`。
  - `docs/audit/known-limitations.md:218` 也把 PERF-05 标为 **FIXED**：
    「Each Magento instance now has its own token bucket and concurrency cap
    (`limits:` ... default 50 rps / burst 100 / 8 concurrent)」；
    `docs/security/threat-model.md:55` 同样写「Rate limiting ... IMPLEMENTED (Magento)」。
  - 同一份 `docs/configuration.md:147-185` 已正确记录 `limits` 键和默认值。
- **证据**: 上述文件行号；`go test ./internal/magento -run TestLimiter` 通过
  （令牌桶/并发测试），`docs/configuration.md` 与 `limits.go` 的数字一致。
- **影响**: 运维会以为网关没有出站限流、只在边缘限流；实际上每个 Magento 实例默认
  50 rps / burst 100 / 8 并发，错误的容量规划或误判「没有保护」都会发生。文档内部也
  自相矛盾（configuration/threat-model/known-limitations 说有，architecture/deployment/
  integrations 说没有）。
- **状态**: CONFIRMED

### SEM-DOC-02: v0.11.5 文档仍称 MCP 会话未绑定身份，代码已绑定

- **严重级别**: P1
- **工具**: `platform.info` 及所有 MCP 工具（会话层）
- **文档说的**:
  - `docs/architecture.md:296`：「The MCP session is not bound to the caller identity
    (SEC-15)」；`:360` 仍列「Session identity binding | PLANNED (SEC-15)」。
  - `docs/mcp-conformance.md:51` S2：「the gateway **does not** bind it to the identity
    (SEC-15, accepted) | PLANNED」；`:68` A5 同样；`:90` 仍列「Session identity binding |
    PLANNED」。
  - `docs/security/threat-model.md:56`：「Session bound to caller identity | PLANNED」；
    `docs/security/security-review.md:38,45`：A5/A12「**PLANNED** (SEC-15)」。
- **实际做的**:
  - `internal/mcp/server/session.go:71-96` 新增 `sessionBinding`：从认证上下文取 subject，
    已有 `Mcp-Session-Id` 但 owner 不匹配时返回 404（不是 403，避免探测），首次响应把
    session id 绑定到 subject。
  - `internal/mcp/server/server.go:625-629` 在 auth middleware 内层安装
    `sessionBinding(newSessionOwners())`；`session_test.go` 覆盖 Alice/Bob 的绑定与拒绝。
  - `docs/audit/known-limitations.md:164` 已把 SEC-15 标为 **FIXED**：
    「An MCP session is now bound to the subject that created it」。
- **证据**: 上述行号；`go test ./internal/mcp/server -run TestSession` 通过。
- **影响**: 安全评审和运维会误以为被盗 session id 在有效期内可用；实际绑定已阻止
  跨 subject 复用。文档还把一项已关闭的 P1 风险留在「PLANNED」，影响发布结论。
- **状态**: CONFIRMED

### SEM-DOC-03: `magento.product.create` 文档仍允许 bundle/downloadable，schema 只接受 simple/virtual

- **严重级别**: P1
- **工具**: `magento.product.create`（以及 import 的 `type_id` 列）
- **文档说的**:
  - `docs/magento-fields.md:14`：`type_id` 允许
    「`simple`, `virtual`, `downloadable`, `bundle`」。
  - `docs/integrations/magento.md:15`：Product types 为
    「Simple, Configurable, Virtual, Downloadable, Bundle」；`:17` 还写
    「Virtual, Downloadable, Bundle: create / update / query ...」。
- **实际做的**:
  - `internal/magento/write_tools.go:91-94`：注释明确「bundle and downloadable need
    option/link endpoints that do not exist here; advertising them produced enabled,
    visible, unsellable products (MAG-21)」，`type_id` 的 enum 是
    `["simple","virtual"]`，description 直接写「bundle and downloadable are rejected」。
  - `docs/releases/v0.11.2.md:40` 也记录「Products the gateway cannot make sellable are
    refused」，与代码一致，但与上述两份文档矛盾。
- **证据**: `grep -n 'typeIDProp' internal/magento/write_tools.go`；schema 文本
  `"enum": ["simple", "virtual"]`。用 `type_id=bundle` 调用会在 schema/handler 处得到
  `invalid_argument`，不会创建产品。
- **影响**: 代理会按文档尝试创建 bundle/downloadable，得到拒绝；更糟的是文档把不可
  创建的类型的「create/update/query」都列为支持，属于能力虚构。
- **状态**: CONFIRMED

### SEM-DOC-04: `docs/integrations/magento.md` 仍称 Sales 只读，代码有订单/发票/发货/退款/取消写工具

- **严重级别**: P1
- **工具**: `magento.order.create`、`magento.invoice.create`、`magento.shipment.create`、
  `magento.creditmemo.create`、`magento.order.cancel` 等
- **文档说的**:
  - `docs/integrations/magento.md:21`：「Sales | Read-only orders, invoices, shipments,
    credit memos, aggregate analytics」。
- **实际做的**:
  - `internal/magento/write_tools.go:954` `magento.invoice.create`、
    `:1022` `magento.shipment.create`、`:1027` `magento.creditmemo.create`、
    `:1611` `magento.order.create`、`:1965` `magento.order.cancel`；这些工具在
    `tools.writes: true` 时注册，均要求 confirmation，并由 operation ledger 管理。
  - 同文档 §10（`docs/integrations/magento.md:525` 起）又写「Sales data is read and
    written」，与第 21 行自相矛盾。
- **证据**: 上述 write tool 常量；`docs/implemented-tools.md` 的 Sales 写工具行；
  `TestWriteToolDescriptionsDiscloseSideEffects` 枚举到 55 个写工具。
- **影响**: 读者会以为订单/发票/发货/退款只有读工具，忽略写工具的确认、账本和金额风险；
  这是能力/风险声明的严重不一致。
- **状态**: CONFIRMED

### SEM-DOC-05: 多个输出 schema 的 `total` 描述说「店铺总数」，实际是「本次请求行数/事件总数」

- **严重级别**: P2
- **工具**: `magento.products.bulk_update`、`magento.attributes.options.bulk_create`、
  `magento.configurable.create_variations`、`magento.product.links.bulk`、
  `magento.import.validate/execute`、`magento.import.batch(es)`、
  `cloudflare.security.summary`
- **文档说的**（agent 看到的输出 schema description，统一为）:
  「Number of records the store holds; may exceed the number returned.」
  出现在 `internal/magento/product_bulk.go:30`、`catalog.go:340`、
  `configurable.go:1079`、`product_links.go:142`、`import.go:105,385,612`、
  `internal/cloudflare/tools.go:119`、`internal/jobs/jobs.go:487`。
- **实际做的**:
  - `product_bulk.go:94` `BulkUpdateResult{Total: len(skus)}` —— 本次请求的 SKU 数。
  - `catalog.go:399` `OptionBulkResult{Total: len(labels)}` —— 本次请求的 label 数。
  - `configurable.go:1143` `VariationResult{Total: len(variants)}` —— 本次请求的变体数。
  - `product_links.go:170` `LinkBulkResult{Total: len(targets)}` —— 本次请求的目标数。
  - `import.go:147` `ImportPreview{Total: len(rows)}`、`:441` `ImportResult{Total: len(rows)}`、
    `:628` `ImportReport{Total: result.Total}` —— 本批行数，不是店铺记录总数。
  - `cloudflare/security.go:81` `SecuritySummary{Total: total}` —— `actions` 事件数之和，
    不是「店铺记录数」。
  - `jobs/jobs.go:487` 的 `ImportBatch.Total` 是批次行数。
- **证据**: 上述 description 与赋值行；`TestEveryRegisteredToolSchemaIsClosed` 只验证
  description 存在，不验证语义，所以这些错误能通过注册。
- **影响**: agent 会把 `total` 解释成「店铺共有 N 条」而不是「本次处理 N 条」，在
  批量更新、导入、Cloudflare 安全汇总上做出错误的分页/完整性判断。
- **状态**: CONFIRMED

### SEM-DOC-06: `/metrics` 文档说「未认证」，代码在非回环监听时强制认证

- **严重级别**: P2
- **工具**: n/a（运维端点）
- **文档说的**:
  - `docs/architecture.md:295`：「`/metrics` is not authenticated by the gateway;
    restrict it at the edge.」
  - `docs/operations/deployment.md:110`：「Prometheus metrics (same listener; not
    authenticated by the gateway)」。
  - `docs/operations/monitoring.md:11-12`：「`/metrics` ... is **not authenticated by
    the gateway**. Restrict it with the edge/Access policy ...」。
- **实际做的**:
  - `cmd/server/main.go:155-157`：`MetricsRequireAuth: !isLoopbackListen(cfg.Server.Listen)`，
    非回环监听时把 `/metrics` 放进 `AuthMiddleware`。
  - `internal/mcp/server/server.go:652-657`：`MetricsRequireAuth && AuthMiddleware != nil`
    时 `mux.Handle("/metrics", opts.AuthMiddleware(opts.Metrics))`，否则裸挂。
  - `docs/releases/v0.11.2.md:30-33` 也记录「`/metrics` requires authentication on a
    non-loopback listener」。
- **证据**: 上述行号；`TestMetricsRequireAuthOnPublicListener`（server 测试）覆盖。
- **影响**: 运维会以为生产 `/metrics` 公开、只靠边缘保护；实际生产非回环监听已经要求
  Access/JWT。文档把安全边界说反了（更保守，但不准确），会误导排障和暴露面审计。
- **状态**: CONFIRMED

### SEM-DOC-07: `/readyz` 文档说只看进程，代码在配置数据库时 ping 数据库并可能 503

- **严重级别**: P2
- **工具**: n/a（运维端点）
- **文档说的**:
  - `docs/architecture.md:200`：「`/readyz` returns 200 with the registry tool count
    whenever the process can serve MCP」；`:311` 同义。
  - `docs/operations/deployment.md:109`：「the process can serve MCP; returns the
    registry tool count ...」。
  - `docs/operations/monitoring.md:8`：「readiness probe; the process can serve MCP and
    the registry tool count」。
- **实际做的**:
  - `cmd/server/main.go:159-163`：`ReadyCheck: readyCheckFor(store)`，注释明确
    「readiness must consult the durable store when one is configured」。
  - `internal/mcp/server/server.go:641-650`：`ReadyCheck` 非 nil 且返回错误时写
    `503 {"status":"not ready","reason":...,"tools":...}`。
  - `docs/audit/remediation-plan.md:173` 记录 QA-06 **FIXED**：`/readyz` 在 durable store
    不可达时 503。
- **证据**: 上述行号；`TestReadyzConsultsTheDependencyCheck` 与 QA-06 回归测试。
- **影响**: 运维会以为数据库宕机时 `/readyz` 仍 200，从而把 503 误判为网关自身故障；
  反过来也会低估 readiness 对数据库的依赖。
- **状态**: CONFIRMED

### SEM-DOC-08: v0.11.2 release note 称「每个 required 输入都有 description」，实际有 8 个缺失

- **严重级别**: P2
- **工具**: `magento.import.validate`、`magento.import.execute`、
  `unified.traffic.compare`
- **文档说的**:
  - `docs/releases/v0.11.2.md:47-48`：「**API documentation.** Every required tool input
    and every output property carries a description, enforced at registration.」
- **实际做的**:
  - `internal/mcp/toolerr/toolerr.go:106` 只调用 `requireDescribedOutput`，输入 schema 的
    required 字段没有同样检查；`TestNamedWriteSchemasDocumentTheirRequiredFields` 只抽查
    5 个 schema，不是全量。
  - 用真实 maximal 注册表列出 112 个工具并检查每个 `required` 字段的 description，
    得到 8 个缺失：
    `magento.import.execute` 的 `instance/format/data_base64`、
    `magento.import.validate` 的 `instance/format/data_base64`、
    `unified.traffic.compare` 的 `start/end`。
  - 源码对应：`internal/magento/tools.go:269-277` 的 `importValidateSchema` 三个 required
    字段没有 `description`；`internal/unified/tools.go:26-27` 的 `Start/End` 没有
    `jsonschema` tag。
- **证据**: 复现命令见 §3；输出 `tools=112 missing_required_input_descriptions=8`。
- **影响**: release note 把「所有 required 输入都有描述」作为已交付能力，实际 agent
  在导入和 unified 工具上仍看到裸字段名；这是可验证的发布说明不准确。
- **状态**: CONFIRMED

### SEM-DOC-09: `docs/integrations/magento.md` 的 shipment/creditmemo 读路径和 email 路径仍写复数/`/email`

- **严重级别**: P2
- **工具**: `magento.shipment.get`、`magento.creditmemo.get`、
  `magento.shipment.email`、`magento.creditmemo.email`
- **文档说的**:
  - `docs/integrations/magento.md:284`：
    「Shipments / credit memos | GET, POST | `/V1/shipments`, `/V1/creditmemos`
    (+ `/{id}`, `/email`) ...」——暗示 `GET /V1/shipments/{id}`、
    `GET /V1/creditmemos/{id}`、`/email`。
  - `:283` 的 Invoices 行也写 `/email`。
- **实际做的**:
  - `internal/magento/order_documents.go:156-158`：shipment 读单条是
    `GET /shipment/{id}`（singular）；`:214` creditmemo 是 `GET /creditmemo/{id}`；
    注释明确「/shipments/{id} is a 404 on a standard instance」。
  - `internal/magento/order_documents.go:226-232`：email 路径是
    `/invoices/%d/emails`、`/shipment/%d/emails`、`/creditmemo/%d/emails`（复数 `emails`，
    shipment/creditmemo 单数资源）。
  - `docs/releases/v0.11.4.md:45-52` 已记录 v0.11.4 修复了这些 plural/singular 路由；
    `internal/magento/live_defects_test.go:1219-1220` 专门断言工具描述使用
    `/V1/shipment/{id}`、`/V1/creditmemo/{id}`。
- **证据**: 上述代码行；工具描述本身（`internal/magento/tools.go:807,984`）已经是单数，
  但集成文档仍保留旧表。
- **影响**: 读者按集成文档调用会得到 404；v0.11.4 的修复在文档层没有闭环。
- **状态**: CONFIRMED

### SEM-DOC-10: 幂等工具的描述说「idempotent」，MCP annotation 仍是 `IdempotentHint:false`

- **严重级别**: P3
- **工具**: `magento.category.update`、`magento.category.assign_product`、
  `magento.customer.update`、`magento.product.media.update`、
  `magento.product.options.update`、`magento.product.tier_prices.set` 等
- **文档说的**:
  - `internal/magento/write_tools.go:420-468` 的 `writeDisclosures` 对上述工具写
    「idempotent; repeating the same field values leaves ... unchanged」等；
    `discloseWrite`（`:477-493`）把该句追加到 agent 可见的 description。
- **实际做的**:
  - `writeToolMetadata`（`internal/magento/write_tools.go:502`）和
    `destructiveToolMetadata`（`:775`）对所有写工具固定
    `IdempotentHint: false`，没有根据 `writeDisclosures` 的幂等声明设置。
  - MCP 客户端看到的 `tools/list` annotation 因此与 description 矛盾；写工具合同测试
    只检查 description 短语，不检查 annotation。
- **证据**: 上述行号；`docs/implemented-tools.md` 的 `Scope/Write?/Confirmation` 表不展示
  annotation，所以文档层也看不到这个差异。
- **影响**: 客户端会用 annotation 决定是否安全重试；对真正幂等的工具提示「非幂等」会
  导致不必要的人工确认/重试策略，反之亦然。
- **状态**: CONFIRMED

### SEM-DOC-11: 超时表写「Read 30s」，Cloudflare/GA4 读工具实际 60s

- **严重级别**: P3
- **工具**: `cloudflare.analytics.traffic`、`cloudflare.security.summary`、
  `cloudflare.security.events`、`ga4.analytics.traffic`、`ga4.analytics.realtime`、
  `ga4.ecommerce.overview`、`ga4.ecommerce.products`
- **文档说的**:
  - `docs/mcp-tools.md:304-306`：`Read | 30 s`。
- **实际做的**:
  - `internal/cloudflare/tools.go:166` `toolTimeout = 60 * time.Second`（三个工具）；
  - `internal/ga4/tools.go:66` traffic 60s，`internal/ga4/extra_tools.go` realtime/ecommerce
    60s；只有 `ga4.property.metadata` 是 30s。
  - 生成的 `docs/implemented-tools.md` 已列出每个工具的 60s，和 `mcp-tools.md` 的
    「Read 30s」不一致。
- **证据**: 上述常量；`docs/implemented-tools.md` 中 `cloudflare.*`/`ga4.*` 行。
- **影响**: 调用方会按 30s 预算规划，实际可以跑到 60s；对超时/重试策略是错误承诺。
- **状态**: CONFIRMED

### SEM-DOC-12: `cloudflare.security.events.truncated` 描述「还有更多记录」，代码只判断「本页满」

- **严重级别**: P3
- **工具**: `cloudflare.security.events`
- **文档说的**:
  - `internal/cloudflare/tools.go:94`（输出 schema）：
    「True when more records exist than were returned.」
- **实际做的**:
  - `internal/cloudflare/security.go:184`：`page.Truncated = len(page.Events) == limit`。
    当恰好返回 `limit` 条且后面没有更多记录时，也会是 `true`；代码没有 total/下一页探测。
- **证据**: 上述两行；`TestEventsValidation` 只验证 limit 边界，没有断言
  「恰好 limit 且无更多」的语义。
- **影响**: agent 会把「刚好一页」误判为「还有更多」，多翻一页或错误报告不完整。
- **状态**: CONFIRMED

### SEM-DOC-13: `docs/configuration.md` 称 ADR-0018 仍是旧状态，ADR 已更新

- **严重级别**: P3
- **工具**: n/a（配置文档）
- **文档说的**:
  - `docs/configuration.md:222-224`：「The ADR still carries the older status
    "implementation in progress" and a positive-consequence bullet that claims
    multi-instance bindings work; this section is the as-built statement until the ADR
    is refreshed.」
- **实际做的**:
  - `docs/adr/0018-multi-instance-routing.md:3` 已是
    「Accepted (per-tool routing implemented; unified bindings still single-instance —
    see Implementation progress)」；正文还有 current limitation 段。
  - ADR 的 positive bullet（`:79`）仍写「Bindings and unified analytics work across real
    multi-instance deployments」，所以「positive-consequence bullet」这半句仍成立；
    「older status implementation in progress」这半句已过时。
- **证据**: 上述行号。
- **影响**: 配置文档把已更新的 ADR 说成旧状态，读者会以为 ADR 尚未刷新；属于文档维护
  失真（部分成立，故不升 P2）。
- **状态**: CONFIRMED

### SEM-DOC-14: 文档标记「as-built for v0.11.5」，工作树实际是 v0.11.5-4 且包含 tag 后代码变更

- **严重级别**: P3
- **工具**: n/a（版本标记）
- **文档说的**:
  - `docs/architecture.md:3`：「**Status:** as-built for v0.11.5」；
    `docs/index.md:4,9`：「describe the shipped v0.11.5 gateway」「**v0.11.5** ... is the
    current pre-1.0 release」。
- **实际做的**:
  - `git describe --tags --always` = `v0.11.5-4-g5e2749e`；
  - `git diff --stat v0.11.5..HEAD` 显示 tag 后还有代码变更：
    `internal/magento/tools.go`、`internal/magento/write_tools.go`、
    `internal/magento/live_defects_test.go`、`docs/implemented-tools.md`（4 个提交）。
    这些是 v0.11.4/v0.11.5 路由描述修复的后续，不是纯文档提交。
- **证据**: 上述 git 命令；`docs/architecture.md` 的版本标记。
- **影响**: 读者无法判断「v0.11.5」是否包含 tag 后的描述/测试修复；发布报告若引用
  「as-built v0.11.5」会把未打 tag 的字节算进 v0.11.5。因为变更主要是描述和测试，
  不升 P2。
- **状态**: SUSPECTED（「as-built」可解释为发布版本；但工作树确实不等于 tag）

---

## 2. 抽查「已修复」声明的诚实性（5 条）

| 声明 | 代码证据 | 结论 |
|---|---|---|
| `known-limitations.md:218` PERF-05「每实例令牌桶/并发上限已修复」 | `internal/magento/limits.go:39-142`；`client.go:197`；`cmd/server/main.go:329` | **声明为真**；但 architecture/deployment/integrations 仍说没有（SEM-DOC-01） |
| `known-limitations.md:164` SEC-15「会话绑定已修复」 | `internal/mcp/server/session.go:71-96`；`server.go:627`；`session_test.go` | **声明为真**；但 architecture/conformance/threat-model/security-review 仍说 PLANNED（SEM-DOC-02） |
| `known-limitations.md:193` ARCH-07「NewMCP 在缺 Confirm 时拒绝启动」 | `internal/mcp/server/server.go:126-138`；`TestNewMCPRequiresConfirmationServiceForApprovalTools` | **声明为真**，未发现矛盾 |
| `known-limitations.md:191` REL-11「重试导入复用 open batch，按 created_at DESC,id DESC 查找」 | `internal/storage/import_batches.go:55-56,134-135` | **声明为真**，未发现矛盾 |
| `releases/v0.11.4.md:45-52`「shipment/creditmemo 单数读路由、email 路由已修复」 | `internal/magento/order_documents.go:156-158,214,226-232`；`live_defects_test.go:1219-1220` | **代码为真**；但 `docs/integrations/magento.md:284` 仍是旧复数/`/email`（SEM-DOC-09） |

另外抽查 v0.11.5 的 import `status/visibility` 修复：`internal/magento/import.go:654-657,726-738`
把两列当 top-level 字段处理，`TestImportAppliesStatusAndVisibility` 通过——声明为真。

---

## 3. NOT-A-DEFECT 核对

### 3.1 `platform.info` 的 tools 与 `docs/implemented-tools.md` 一致

- **文档说的**：`docs/implemented-tools.md:29-34` 的配置计数为 maximal 112/101、
  read-only 54/43、read-only+DB 56/46；`platform.info` 行在 `:43`。
- **实际做的**：`internal/mcp/server/server.go` 的 `platformInfoTool` 遍历
  `opts.Registry.List()`；`NewMCP` 先把 `platform.info` 注册进同一 registry，所以
  tools 包含自身。生成器 `scripts/toolinventory` 用同一 registry 生成
  `docs/implemented-tools.md`；`TestGeneratedDocumentIsCurrent` 与
  `TestPlatformInfoReportsRegistryTruth` 通过。
- **结论**：NOT-A-DEFECT（tools 计数/自身列出）。**版本字段**：`platform.info` 的
  `Version` 来自 `cmd/server/main.go:39` 的 `var version = "dev"`，发布构建用
  `-ldflags -X main.version=...`；源码运行显示 `dev` 是预期，不是与 v0.11.5 的矛盾。

### 3.2 配置键和默认值与 `docs/configuration.md` 一致

- **文档说的**：`server.max_body_bytes` 默认 16 MiB、写启用下限 12 MiB；
  `database.max_conns` 默认 10；`auth.enabled` 默认 true；`tools.writes` 默认 false；
  `limits` 默认 50/100/8，只有 Magento 生效。
- **实际做的**：`internal/core/config/config.go:138-150` 的 `Default()`、
  `:211-219` 的 body cap 校验、`:290-303` 的 limits 校验；`cmd/server/main.go:299-308`
  的 `magentoLimits` 映射；`storage.Open` 使用 `MaxConns`。
- **结论**：NOT-A-DEFECT（本组键/默认值一致）。注意 `docs/architecture.md` 对 limits 的
  描述仍错（SEM-DOC-01），但 `docs/configuration.md` 本身是对的。

### 3.3 `docs/magento-fields.md` 的 8 MiB / 5000 行 / 50 SKU / 100 项数字

- **文档说的**：`docs/magento-fields.md:149-163`。
- **实际做的**：`internal/magento/import.go:31-33`（8 MiB/5000/64）、
  `product.go:23`（50 SKU）、`inventory.go:25`（100 items）、
  `product_bulk.go:18`（50 SKU）、`product_links.go:131`（50 targets）、
  `tier_prices.go:17`（50 tiers）、`media.go:62`（5 MiB）。
- **结论**：NOT-A-DEFECT（抽查数字一致）。

### 3.4 `platform.info` 的 `writes_enabled` 与 registry 一致

- **文档说的**：`docs/mcp-tools.md:101` / `docs/implemented-tools.md:43` 说
  platform.info 报告 writes flag。
- **实际做的**：`platformInfoTool` 使用 `opts.Registry.HasWrites()`；registry 的
  `SetWritesEnabled(cfg.Tools.Writes)` 在 `cmd/server/main.go:315` 注册前调用；
  read-only+DB 的 56 工具构建（见 t50 记录）零非读。
- **结论**：NOT-A-DEFECT。

---

## 4. 复核命令与关键输出

```bash
# 1. 当前版本与 tag 后差异
git describe --tags --always
git diff --stat v0.11.5..HEAD

# 2. 限流/会话/指标/readiness 代码位置
grep -n 'DefaultRequestsPerSecond\|DefaultBurst\|DefaultMaxConcurrent' internal/magento/limits.go
grep -n 'sessionBinding' internal/mcp/server/server.go internal/mcp/server/session.go
grep -n 'MetricsRequireAuth\|ReadyCheck' cmd/server/main.go internal/mcp/server/server.go

# 3. required 输入 description 全量检查（overlay test）
go test -overlay /tmp/m2mcp-docs-audit/overlay.json ./cmd/server \
  -run TestDocsAuditRequiredInputDescriptions -v
# 输出：tools=112 missing_required_input_descriptions=8
#   magento.import.execute instance/format/data_base64
#   magento.import.validate instance/format/data_base64
#   unified.traffic.compare start/end

# 4. 总数字段语义
grep -n 'Number of records the store holds' internal/magento/*.go internal/cloudflare/*.go internal/jobs/*.go
grep -n 'Total: len' internal/magento/*.go

# 5. 文档/代码对照
grep -n 'No per-instance semaphore\|not bound to the caller identity\|not authenticated\|whenever the process can serve' \
  docs/architecture.md docs/operations/deployment.md docs/operations/monitoring.md
grep -n 'type_id\|Read-only orders' docs/magento-fields.md docs/integrations/magento.md
```

**关键输出：**

- `git describe --tags --always` → `v0.11.5-4-g5e2749e`
- `git diff --stat v0.11.5..HEAD` → 5 files, 187 insertions/18 deletions；含
  `internal/magento/tools.go`、`write_tools.go`、`live_defects_test.go`
- required input 全量检查 → `tools=112 missing_required_input_descriptions=8`
- `go test ./internal/magento -run TestLimiter`、`go test ./internal/mcp/server -run TestSession`
  → pass（证明限流/会话绑定确实存在）
- `go test ./scripts/toolinventory` → pass（证明 `docs/implemented-tools.md` 与 registry 同步）

---

## 5. 影响排序

| 优先级 | 发现 | 理由 |
|---|---|---|
| P1 | SEM-DOC-01 限流 | 安全/容量控制被文档说成不存在，且 docs 内部矛盾 |
| P1 | SEM-DOC-02 会话绑定 | 已修复的 P1 安全项被写成 PLANNED，影响发布结论 |
| P1 | SEM-DOC-03 产品类型 | 文档承诺不可创建的 bundle/downloadable |
| P1 | SEM-DOC-04 Sales 只读 | 文档漏掉订单/发票/发货/退款写能力及风险 |
| P2 | SEM-DOC-05 total 语义 | 多个输出字段的 agent-facing 语义错误 |
| P2 | SEM-DOC-06 metrics 认证 | 生产安全边界被说反 |
| P2 | SEM-DOC-07 readyz 数据库依赖 | 运维会把 503 误判为网关故障 |
| P2 | SEM-DOC-08 required 输入描述 | 发布说明声称全量覆盖，实际 8 个缺失 |
| P2 | SEM-DOC-09 文档路由 | 读者按文档调用会 404 |
| P3 | SEM-DOC-10 idempotent annotation | 客户端重试策略受影响 |
| P3 | SEM-DOC-11 超时表 | 30s vs 60s 预算错误 |
| P3 | SEM-DOC-12 truncated 边界 | 恰好一页被误报「还有更多」 |
| P3 | SEM-DOC-13 ADR 状态 | 配置文档引用过时状态 |
| P3 | SEM-DOC-14 版本标记 | 工作树不等于 v0.11.5 tag |

**修复建议（仅记录，不在本任务修改）：**

1. 先改 SEM-DOC-01/02：把 architecture/deployment/integrations/conformance/threat-model/
   security-review 的 limiter/session 描述改为 as-built，并引用 `known-limitations` 的
   FIXED 行。
2. 改 SEM-DOC-03/04/09：以 `write_tools.go` 的 schema/路由为准，删除 bundle/downloadable
   和 Sales read-only 的旧声明，修正 shipment/creditmemo 单数路由与 `/emails`。
3. 改 SEM-DOC-05/08/10/11/12：输出 `total` 描述按字段真实语义重写（request rows /
   event count / store total 分开），required input 补 description 或把 release note
   改为「输出全量、输入抽查」，annotation 由 `writeDisclosures` 派生，超时表按工具类
   分 30/60/120，`truncated` 描述改为「本页已满，是否还有更多未探测」。
4. SEM-DOC-06/07/13/14 是运维/版本文案，随下一次文档发布修正。
