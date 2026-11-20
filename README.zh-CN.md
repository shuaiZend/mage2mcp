# mage2mcp

mage2mcp 是一个独立的 [Model Context Protocol](https://modelcontextprotocol.io)（MCP）网关，
通过类型化、可审计、默认只读的工具，把 Magento 2、Cloudflare 和 Google Analytics 4
暴露给 AI 智能体。

> **状态：Phase 1 —— 基础设施。**
> 已具备可运行的 MCP 服务器：Streamable HTTP、Cloudflare Access JWT 认证、RBAC、
> 审计、Prometheus 指标，以及 Magento 商品、Cloudflare 分析/安全、GA4 流量与元数据的
> 只读工具与统一流量对比。一个网关可同时服务多个 Magento 店铺、Cloudflare zone 与
> GA4 属性，并具备按实例的授权与审计；可选 PostgreSQL 持久化、审计与任务队列构成
> 完整部署（无 Redis）。写操作需要两步人工审批。[v0.7.5](https://github.com/shuaiZend/mage2mcp/releases/tag/v0.7.5)
> 是首个公开预发布版本（签名产物、SBOM、多架构镜像），接口在 1.0 之前仍可能通过 ADR 变更。

## 当前已有的内容

| 项目 | 状态 |
|---|---|
| 设计文档 | [`docs/`](docs/) 中已完成 Phase 0 基线 |
| 研究基线 | [`docs/development/research-baseline.md`](docs/development/research-baseline.md) |
| 源代码 | Phase 1 基础：MCP 服务器、认证、RBAC、审计、指标、配置、Docker |
| 测试与 CI | 每个 PR 运行单元、集成、竞态、静态、安全与供应链门禁 |
| 写工具 | 商品/变体/库存/导入写工具，由 `tools.writes` 控制并强制两步人工审批 |
| 版本发布与容器镜像 | v0.0.1 预发布（签名 + SBOM + 多架构镜像） |
| 许可证与社区文档 | [Apache-2.0](LICENSE)、[NOTICE](NOTICE)、[CONTRIBUTING.md](CONTRIBUTING.md)、[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)、[SECURITY.md](SECURITY.md)、[SUPPORT.md](SUPPORT.md) |

## 快速开始（开发）

```bash
# 1. 构建
go build -o bin/mage2mcp ./cmd/server

# 2. 配置：复制示例配置；仅限本机回环开发时，将 `auth.enabled` 设为 false。
#    网关拒绝在非回环监听地址上以关闭认证的方式启动。
cp configs/config.example.yaml config.yaml

# 3. 运行
./bin/mage2mcp -config config.yaml

# 4. 检查存活与就绪
curl -s localhost:8080/healthz
curl -s localhost:8080/readyz
```

MCP 端点为 `POST /mcp`（Streamable HTTP）。生产环境应部署在 Cloudflare Tunnel 之后，
并通过 `MAGE2MCP_AUTH_*` 环境变量保持认证开启。

### Docker

镜像内置容器配置，并用二进制自身探测 `/healthz`（distroless 镜像没有 shell）：

```bash
# 本地开发栈（默认关闭认证、仅回环端口、固定开发密码）
docker compose up --build

# 或单容器
docker build -t mage2mcp .
docker run --rm -p 127.0.0.1:8080:8080 \
  -e MAGE2MCP_AUTH_ENABLED=false -e MAGE2MCP_DEV_INSECURE=true mage2mcp
```

生产形态要求提供全部密钥与 Access 配置：

```bash
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d
```

切勿把开发栈暴露到网络：它以关闭认证和固定占位密码运行。

### 高风险操作

写工具在 `tools.writes: true` 之前保持关闭。启用后，每次写入都需要**另一个真人身份**
通过管理面审批：

```bash
./bin/mage2mcp-admin -config config.yaml -as you@example.com pending
./bin/mage2mcp-admin -config config.yaml -as you@example.com approve <confirmation-id>
```

审批需要 `database.url`：管理 CLI 与网关共享 PostgreSQL 确认存储。审批绑定到工具参数的
规范化哈希，5 分钟过期（最长 15 分钟），且只能使用一次。

## 为什么需要它

AI 智能体需要可靠、可审计地访问电商与分析系统。目前这种访问通常只有两种形态，而两者都不理想。

**厂商托管的黑盒。** SaaS 连接器位于智能体与上游 API 之间。它能看到每个请求和响应，但运维方
无法检查工具如何映射、使用哪些凭据、记录了什么日志、权限如何执行。数据离开运维方的边界，
集成范围受限于厂商选择实现的功能，而一旦更换厂商，就必须重建整套集成。

**一次性脚本与临时接口。** 脚本封装几个 API 调用后交给智能体。它没有类型化 Schema，没有认证
模型，没有授权模型，没有审计记录，也没有破坏性操作前的确认步骤。凭据散落在个人电脑和 CI 中。
脚本绕过了所触碰系统的业务逻辑与权限，并且对负责这些系统的团队完全不可见。

mage2mcp 选择了第三种定位：由运维方自行部署、配置和审计的**独立网关**。

- 网关作为独立服务运行在运维方自己的基础设施内。Magento 不会被修改，也不需要安装任何插件。
- 只调用官方上游 API —— Magento REST 与 GraphQL、Cloudflare API、GA4 Data API。绝不连接
  Magento 数据库。
- 每个上游都是一个独立的**实例**，拥有自己的凭据和授权，因此一个网关可以服务多个商店、zone
  和 property，而无需共享令牌。
- 每个工具都将具备类型化的输入 Schema、声明的风险等级和审计记录。也不会存在通用的
  "调用任意端点"工具。
- 源代码、威胁模型和决策记录全部公开，运维方可以在把生产凭据交给网关之前先行评估。

## 架构概览

```mermaid
flowchart TB
  agents["AI 智能体<br/>MCP 客户端"] --> gateway["MCP 网关<br/>Streamable HTTP"]
  gateway --> auth["认证<br/>Cloudflare Access JWT"]
  auth --> rbac["RBAC<br/>权限 + 实例授权"]
  rbac --> registry["工具注册表<br/>类型化 Schema + 风险等级"]
  subgraph modules["数据源模块"]
    direction LR
    magento["Magento 2"]
    cloudflare["Cloudflare"]
    ga4["GA4"]
    unified["统一分析"]
  end
  registry --> magento
  registry --> cloudflare
  registry --> ga4
  registry --> unified
  magento --> unified
  cloudflare --> unified
  ga4 --> unified
  magento --> mageapi["Magento REST / GraphQL"]
  cloudflare --> cfapi["Cloudflare API"]
  ga4 --> gaapi["GA4 Data API"]
  gateway -.-> pg[("PostgreSQL<br/>配置、审计、任务")]
  gateway -.-> pgjobs[("PostgreSQL<br/>任务队列 队列")]
```

网关将使用官方 [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) 和 Streamable HTTP
传输方式。SDK v1.8.0 支持 MCP 协议版本 `2026-07-28`，并可向下协商到 `2025-11-25`。

在预期设计中，一次工具调用经过以下路径：

1. **认证** —— 调用方出示 Cloudflare Access JWT，网关按已配置的 Access 应用进行校验。
2. **授权** —— RBAC 判断调用方是否可以在该实例上使用此工具，以及该实例是否启用了写操作。
3. **校验** —— 在发起任何上游调用之前，按工具的 JSON Schema 校验输入。
4. **分级与确认** —— 高风险工具要求显式确认，确认绑定到规范化参数的哈希；任何参数变化都会使
   确认失效。
5. **执行** —— 数据源模块使用自己的最小权限凭据调用上游 API。
6. **审计** —— 记录操作者、工具、实例、参数哈希、结果和耗时，密钥会被脱敏。
7. **返回** —— 结果按声明的输出 Schema 校验后返回给智能体。

运行时组件：

| 组件 | 职责 |
|---|---|
| Go 服务 | MCP 端点、认证、RBAC、工具注册表、模块编排 |
| PostgreSQL | 实例配置元数据、审计日志、任务状态 |
| PostgreSQL 任务队列 | 批量操作、导入轮询和日志摄取的后台队列 |
| R2 + ClickHouse（可选） | 启用 Logpush 日志分析时的 Cloudflare 原始请求日志 |
| Prometheus | 工具延迟、错误和上游限流的指标端点 |
| `slog` | 带脱敏的结构化 JSON 日志 |
| YAML + 环境变量 | 配置，密钥仅通过环境变量提供 |

## 网关将提供的能力

状态列只记录当前进展，表中没有任何能力在今天可用，也没有任何能力达到"设计完成"或"已实现"。

| 状态 | 含义 |
|---|---|
| 计划中 | 在范围内，尚未详细设计 |
| 设计中 | 设计文档正在 Phase 0 编写或评审 |
| 设计完成 | 设计已评审通过（目前没有） |
| 已实现 | 可用代码已合并（目前没有） |

### 支持矩阵

| 模块 | 能力 | 状态 |
|---|---|---|
| 平台 | 基于官方 Go SDK 的 Streamable HTTP MCP 端点 | 设计中 |
| 平台 | Cloudflare Access JWT 认证 | 设计中 |
| 平台 | RBAC 权限与按实例授权 | 计划中 |
| 平台 | 带类型化 Schema 和风险分级的工具注册表 | 设计中 |
| 平台 | 绑定规范化参数哈希的确认机制 | 设计中 |
| 平台 | 审计日志、Prometheus 指标、健康检查端点 | 计划中 |
| 平台 | 带校验的 YAML + 环境变量配置 | 计划中 |
| 平台 | 容器镜像与 Compose 编排 | 计划中 |
| Magento 2 | simple、configurable、virtual、downloadable、bundle 商品读取 | 设计中 |
| Magento 2 | 通过 REST 写入商品，包括 configurable 选项和子商品 | 计划中 |
| Magento 2 | EAV 属性与属性集发现 | 计划中 |
| Magento 2 | MSI 库存读取与批量 source 操作 | 计划中 |
| Magento 2 | 媒体库读取与上传 | 计划中 |
| Magento 2 | 分类读取与商品分类归属 | 计划中 |
| Magento 2 | 订单读取 | 计划中 |
| Magento 2 | 带显式校验策略的批量导入（CSV/JSON） | 计划中 |
| Magento 2 | 批量与异步操作状态轮询 | 计划中 |
| Magento 2 | GraphQL 只读店面验证 | 计划中 |
| Cloudflare | zone、套餐与数据集清单（只读） | 计划中 |
| Cloudflare | GraphQL Analytics 查询 | 设计中 |
| Cloudflare | Security Events 查询 | 计划中 |
| Cloudflare | Logpush 任务清单与字段发现（可选启用，只读） | 计划中 |
| Cloudflare | 基于客户自行配置目标的 Logpush 日志分析 | 计划中 |
| Cloudflare | WAF 与 bot 事件检查（只读） | 计划中 |
| GA4 | property 元数据与维度/指标兼容性发现 | 设计中 |
| GA4 | 流量、获客与互动报告 | 计划中 |
| GA4 | 带显式数据存在性探测的电商报告 | 计划中 |
| GA4 | 实时报告 | 计划中 |
| GA4 | 配额感知的执行与退避 | 计划中 |
| 统一分析 | 带声明语义的跨源指标目录 | 设计中 |
| 统一分析 | 跨源实体与时间基准映射 | 计划中 |
| 统一分析 | 基于声明连接键的跨源报告 | 计划中 |

### Magento 2

Magento 模块将通过 REST API 管理商品与库存数据，并且只用 GraphQL 验证店面实际渲染的内容。
商品写入必须使用 REST：Magento GraphQL 没有商品写入 mutation。模块将覆盖：

- 所有标准商品类型，包括 configurable 选项和子商品。
- EAV 属性集与属性，使工具输入可以按商店的真实 Schema 校验，而不是依赖硬编码列表。
- MSI source 与 source item，包括批量分配、取消分配和转移操作。
- 媒体库资源、分类与订单读取。
- 通过 `POST /V1/import/csv` 和 `POST /V1/import/json` 执行批量导入，并在工具输入中明确声明
  校验策略与允许的错误数量。
- 批量与异步操作状态轮询。

不会有 Magento 插件，不会有 `app/code` 包，也不会有数据库连接。模块无法通过官方 API 完成
的事情一律不在范围内。

### Cloudflare

Cloudflare 模块在 v0.x 中为只读。它将提供：

- zone、套餐与数据集清单。
- 针对账户可用数据集的 GraphQL Analytics 查询。
- 来自 `firewallEventsAdaptive` 数据集的 Security Events。
- Logpush 任务清单与字段发现，以及针对客户自行配置的目标所投递日志的分析。

Logpush 并非仅限 Enterprise，Free、Pro 和 Business 套餐也可使用，但按用量计费：每个账户每月
包含 25 GB，超出后发往内部目标为 $0.03/GB，发往外部目标为 $0.10/GB。Logpush 不支持历史回填，
任务失败时日志会丢失。Bot Score 与 verified-bot 字段需要 Bot Management（Enterprise 附加组件）。
模块会在工具输出中说明这些限制，而不是静默返回不完整的数据。

原始请求日志按 zone 选择性启用。启用后，Cloudflare 将记录推送到 R2，由 ClickHouse 查询层
提供请求级查询；PostgreSQL 从不存储原始日志。只需要聚合分析的运维方可以不启用 Logpush。

Logpush 任务与字段检查同样需要选择性启用。Cloudflare 要求所有 Logpush API 调用（包括读取）
都具备 `Logs Write` 权限，因此网关为此使用单独的令牌配置，并且从不发起写操作。不接受该权限
的运维方可以在外部管理任务，并通过 R2 文件到达情况推断任务健康状态。

### Google Analytics 4

GA4 模块按只读范围设计，将使用 Data API v1。它将提供流量、获客、互动、电商和实时报告，以及元数据发现，
使智能体可以在查询前确认哪些维度与指标能够组合使用。

Data API 只返回聚合数据。维度与指标的兼容性按字段对逐对判定，并需要按 property 验证：部分
商品级维度不能与会话级指标组合，而另一些组合则可以使用。网关会逐次校验请求，而不是按作用域
规则推断。原始事件数据需要客户自行拥有并配置的 BigQuery 导出。网关不查询 BigQuery，只说明
这一边界。由于 property 可能有流量但未配置电商跟踪，电商工具会先探测数据是否存在，并在缺失
时如实报告，而不是把零当作收入返回。

### 统一分析

统一分析模块将通过共享的数据源契约组合其他模块。它将提供一个指标目录，其中每个指标都带有定义、
分母、归因窗口和时区，并提供仅基于已声明连接键的跨源报告。它不会把在不同系统中含义不同的指标
相加，从而编造一个混合的"收入"或"转化"数字。

### v0.x 明确不做的事

| 不计划 | 原因 |
|---|---|
| Magento 插件或 `app/code` 包 | 网关只使用官方 API，不在 Magento 中安装任何东西 |
| 直接访问 Magento 数据库 | 绕过业务逻辑，且跨版本升级时会失效 |
| Adobe Commerce as a Cloud Service（IMS OAuth） | 认证模型不同，推迟到 v0.x 之后 |
| Cloudflare 写操作（DNS、WAF 规则、Logpush 任务） | Cloudflare 模块在 v0.x 中为只读 |
| GA4 Admin API 与 property 配置变更 | GA4 模块为只读 |
| 通用 `execute_api` 或 `execute_sql` 工具 | 无类型、不可审计，且与 RBAC 和确认机制冲突 |
| 跨设备身份拼接 | 需要网关并不拥有的身份数据 |
| 在网关内查询 GA4 原始事件 | Data API 只提供聚合数据；原始事件保留在客户自有的 BigQuery 导出中 |

## 安全策略

设计规定以下性质（尚未实现）：

- **默认只读。** 除非运维方显式启用写入配置，否则写工具处于禁用状态；默认配置不暴露任何写
  能力。
- **写入需确认。** 高风险操作要求提供绑定到规范化参数哈希的确认令牌。任何参数变化都会使确认
  失效，因此运维方批准的内容就是实际执行的内容。
- **审计。** 每次工具调用都会记录操作者、工具、实例、参数哈希、结果和耗时。密钥与敏感值在
  存储前会被脱敏。
- **不在 Magento 中安装插件。** 网关无法修改 Magento 代码，Magento 升级也不依赖它。
- **不直接访问数据库。** 所有读写都经过官方 API，从而保留业务逻辑、校验和权限。
- **最小权限令牌。** Magento 集成令牌仅授予所需资源，Cloudflare API 令牌默认仅授予分析与 zone 只读
  访问，GA4 服务账号仅授予 Viewer 权限。可选启用的 Logpush 检查配置带有 `Logs Write`
  权限，仅因为 Cloudflare 要求读取调用也具备该权限；代码中不存在写操作路径。
- **密钥不进入配置文件。** 凭据通过环境变量或密钥管理服务提供，绝不写入日志或作为工具输出
  返回。
- **没有逃生通道。** 不存在通用 API 透传工具，也不存在 SQL 执行工具。

完整的威胁模型、信任边界与缓解措施见
[`docs/security/threat-model.md`](docs/security/threat-model.md)。安全评审流程见
[`docs/security/security-review.md`](docs/security/security-review.md)。

## 设计原则

1. **模块边界。** 每个数据源都是独立模块，实现共享的数据源契约并声明自身能力。模块之间不
   导入彼此的内部实现，核心也不感知厂商细节。
2. **只用官方 API。** Magento REST 与 GraphQL、Cloudflare API、GA4 Data API。不抓取页面，
   不访问数据库，不安装插件，不使用未公开端点。
3. **类型化工具，没有逃生通道。** 每个工具都有固定名称、JSON Schema 输入输出、风险等级和
   所需权限。通用的 `execute_api` 或 `execute_sql` 工具会破坏授权、校验和确认机制，因此
   不会存在。
4. **显式指标语义。** 只有当一个指标的定义、分母、归因窗口和时区都被声明时，它才具备可比性。
   统一分析报告会说明所使用的语义，并拒绝混合不兼容的指标。
5. **默认只读与最小权限。** 默认状态即安全状态。启用写操作是运维方的慎重决定，而不是默认
   行为。
6. **实例是配置与授权的基本单位。** 凭据、授权和限流属于实例，而不属于进程，因此一个网关可以
   服务多个上游而不会在它们之间泄漏访问权限。
7. **一切可审计。** 如果某个动作无法归属到具体操作者并接受复核，它就不会作为工具暴露。
8. **1.0 之前的接口纪律。** 公开接口可以变更，但必须通过有记录的架构决策。参见
   [`docs/adr/`](docs/adr/)。

## 兼容性矩阵

以下是 v0.x 的目标组合，不是已测试的配置。各约束对应的厂商文档在相应的集成文档中引用。

| 组件 | 目标支持 | 说明 |
|---|---|---|
| Magento Open Source 与 Adobe Commerce | 2.4.x；主要目标为 2.4.9 | 使用 Integration OAuth 1.0a 凭证调用 REST（仅在显式启用时使用 Bearer Token）。GraphQL 仅用于只读店面验证 |
| Adobe Commerce as a Cloud Service | v0.x 不支持 | 使用 Adobe IMS OAuth，不支持传统令牌生成方式 |
| Cloudflare 套餐 | Free、Pro、Business、Enterprise | 数据集与字段可用性因套餐而异 |
| Cloudflare GraphQL Analytics | 计划中 | 默认限制为每 5 分钟 300 次查询；zone 级查询最多可包含 10 个 zone，账户级查询恰好 1 个账户 |
| Cloudflare Security Events | 计划中 | 基于 `firewallEventsAdaptive`；保留期最长 31 天；结果可能经过采样 |
| Cloudflare Logpush | 可选启用的只读任务清单与日志分析 | 按用量计费，每月包含 25 GB；没有回填；任务失败时日志会丢失 |
| Cloudflare 原始日志 | 可选：R2 + ClickHouse | 仅用于请求级 Logpush 分析；PostgreSQL 从不存储原始日志 |
| Cloudflare bot 信号 | 仅在已授权时可用 | Bot Score、bot 标签和 verified-bot 字段需要 Bot Management |
| GA4 | Data API v1 | 仅聚合数据；原始事件需要 BigQuery 导出 |
| GA4 配额 | 按 property 读取并遵守配额 | 参考 property 报告为每天 200,000 tokens、每小时 40,000 tokens、10 个并发请求 |
| MCP 协议 | 计划中：`2026-07-28`，可向下协商到 `2025-11-25` | 由 MCP Go SDK v1.8.0 提供 |
| Go | 1.26 或更高 | SDK v1.8.0 要求 `go 1.25.0`；当前直接依赖要求 1.26；当前工具链为 1.27.x |
| PostgreSQL | 16 或更高 | 配置元数据、审计日志、任务状态 |
| 容器运行时 | Docker 与 Compose | 本地栈与部署 |

## 文档地图

设计文档在 Phase 0 期间公开编写。以下链接指向正在并行创建、并会在本 README 合并前存在的文件。

| 文档 | 内容 |
|---|---|
| [`docs/index.md`](docs/index.md) | 所有设计、集成与开发文档的入口 |
| [`docs/architecture.md`](docs/architecture.md) | 系统架构、组件、请求生命周期、部署拓扑 |
| [`docs/module-boundaries.md`](docs/module-boundaries.md) | 数据源契约、能力声明、依赖规则 |
| [`docs/database-design.md`](docs/database-design.md) | 用于配置、审计和任务状态的 PostgreSQL Schema |
| [`docs/mcp-tools.md`](docs/mcp-tools.md) | 工具命名、输入输出 Schema、风险等级、确认模型 |
| [`docs/mcp-conformance.md`](docs/mcp-conformance.md) | MCP 一致性矩阵：工具、传输、授权，以及对应的测试 |
| [`docs/implemented-tools.md`](docs/implemented-tools.md) | 当前代码中已注册的工具 |
| [`docs/unified-analytics.md`](docs/unified-analytics.md) | 指标语义、连接键、跨源报告定义 |
| [`docs/development-plan.md`](docs/development-plan.md) | 阶段计划、交付物、退出标准 |
| [`docs/security/threat-model.md`](docs/security/threat-model.md) | 资产、信任边界、威胁、缓解措施 |
| [`docs/security/security-review.md`](docs/security/security-review.md) | 安全评审流程与关卡 |
| [`docs/integrations/magento.md`](docs/integrations/magento.md) | Magento 端点、认证、商品与库存流程 |
| [`docs/integrations/cloudflare.md`](docs/integrations/cloudflare.md) | Cloudflare 数据集、Security Events、Logpush 约束 |
| [`docs/integrations/ga4.md`](docs/integrations/ga4.md) | GA4 维度、指标、兼容性规则、配额 |
| [`docs/development/research-baseline.md`](docs/development/research-baseline.md) | 设计所依据的已验证事实与证据标签 |
| [`docs/adr/`](docs/adr/) | 架构决策记录（ADR） |

## 路线图

各阶段有先后顺序，但没有排期。只有退出标准达成、且下一阶段的设计文档与研究基线保持一致时，
阶段才算结束。

| 阶段 | 重点 | 退出标准 |
|---|---|---|
| 0 | 设计 | 架构、边界、工具目录、威胁模型、ADR 与治理机制通过评审；文档 CI 通过 |
| 1 | 基础设施 | 基于 Streamable HTTP 的服务骨架、Cloudflare Access JWT 认证、RBAC、工具注册表、配置校验、审计、PostgreSQL、Docker、CI 关卡 |
| 2 | Magento 2 | 商品、EAV、MSI、媒体、分类、订单、批量导入的读写工具，以及 GraphQL 验证 |
| 3 | Cloudflare | GraphQL Analytics、Security Events、Logpush 清单与分析，全部只读 |
| 4 | GA4 | 元数据发现、核心报告、电商报告、实时报告、配额处理，以及有文档记录的 BigQuery 边界 |
| 5 | 统一分析 | 指标目录、实体映射、带声明语义的跨源报告 |
| 6 | 加固 | 安全评审、故障注入、负载测试、SBOM、运维文档 |
| 7 | 发布 | v0.1.0，包含签名产物、校验和以及有文档记录的升级路径 |

## 参与贡献

项目处于 Phase 0。目前欢迎通过 GitHub issue 提交设计评审意见、威胁模型反馈和文档修正。
代码贡献从 Phase 1 开始。

所有贡献都需要 Developer Certificate of Origin 签署（`git commit -s`），不要求 CLA。
开发环境、分支与提交规范、测试要求以及模块归属规则见 [CONTRIBUTING.md](CONTRIBUTING.md)，
社区行为准则见 [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md)。

## 安全

请勿为漏洞创建公开 issue。请按照 [SECURITY.md](SECURITY.md) 的说明，通过 GitHub Security
Advisories 私下报告。在 1.0 之前，安全修复只针对最新的 v0.x 次版本。

## 许可证

基于 Apache License 2.0 授权。参见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。第三方组件及其
许可证记录在依赖清单中，并随每个版本发布的 SBOM 一并提供。

mage2mcp 是独立的开源项目，与 Adobe、Magento、Cloudflare 或 Google 不存在隶属、
背书或赞助关系。产品名称仅用于描述性说明。
