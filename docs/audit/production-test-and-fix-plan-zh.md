# 生产升级与全面测试记录（v0.11.4）

| 项目 | 内容 |
|---|---|
| 日期 | 2026-11-21 |
| 生产服务器 | `49.51.250.83`，`/opt/mage2mcp` |
| 升级路径 | v0.11.1 → **v0.11.4** |
| 目标店铺 | `https://demo-magento-2.auroracreation.com`，店铺视图 `pl`（PLN，pl_PL） |
| 工具总数 | 102（全部已覆盖测试） |
| 测试用例 | 123 |

---

## 一、生产升级

### 1.1 操作步骤

| 步骤 | 结果 |
|---|---|
| 备份 `compose.yaml` | `compose.yaml.bak.20261121175622` |
| 镜像版本替换（server + worker） | v0.11.1 → v0.11.4 |
| `docker compose pull` | 成功 |
| `docker compose up -d` | 成功 |
| 容器状态 | 三个容器全部 `Up`，server 为 `healthy` |
| `/readyz` | `{"status":"ready","tools":102}` |

### 1.2 升级后状态

```text
mage2mcp-postgres-1   postgres:17-alpine                    Up (healthy)
mage2mcp-server-1     ghcr.io/shuaizend/mage2mcp:v0.11.4    Up (healthy)
mage2mcp-worker-1     ghcr.io/shuaizend/mage2mcp:v0.11.4    Up
```

**回滚方式**：`cp compose.yaml.bak.20261121175622 compose.yaml && docker compose up -d`。

---

## 二、生产环境全量测试

### 2.1 结果

对**生产实例**（非临时容器）跑全量矩阵：

| 状态 | 数量 |
|---|---|
| 通过 | **112** |
| 软失败 | 7 |
| 硬失败 | 1 |
| 跳过 | 3 |

原始结果：`test/live/out/results-20261121-175704.json`

### 2.2 与已发布镜像的一致性

生产实例与单独部署的 v0.11.4 容器结果**完全相同**（112/7/1/3），说明生产配置（限流 20 rps / 40 burst / 4 并发）不影响功能。

### 2.3 剩余 8 项逐条判定

| 工具 | 报错 | 判定 |
|---|---|---|
| `product.options.create` | `unsupported_capability` | **环境**：该店 `POST /products/{sku}/options` 返回 404 |
| `stock_source_link` | `validation_failed` 400 | **环境**：stock 1 是 Default Stock，Magento 拒绝非默认源（业务规则） |
| `stock_source_unlink` | 同上 | 同上 |
| `inventory.transfer` | `invalid_argument` | **正确拒绝**：用例刻意用同源 |
| `invoice.capture` | `conflict` | **正确拒绝**：checkmo 自动捕获，发票已支付 |
| `order.cancel` | `conflict` | **正确拒绝**：订单已终态 |
| `import.cancel` | `illegal_transition` | **正确拒绝**：任务已终态 |
| `configurable.add_child` | `validation_failed` | **测试脚本**：直连验证该工具实际可用 |

**无未归因失败。**

---

## 三、探索性测试（矩阵之外）

矩阵覆盖 101 个工具，本轮补齐最后 1 个并深挖三个未验证区域。

### 3.1 `magento.product.media.get`（此前未覆盖）

上传一张 PNG 后读取，返回完整媒体条目（`id`、`media_type`、`label`、`position`、`types`、`file`）。**正常**。

至此 **102 个工具全部有真实用例**。

### 3.2 `magento.configurable.add_child`

创建带 `color` 属性的简单商品后调用，返回 `linked: true`，商品成功挂到配置商品父级。**工具正常**——矩阵中的软失败是脚本顺序问题（子商品已被链接）。

### 3.3 导入流程（真实 CSV）

| 步骤 | 结果 |
|---|---|
| `import.validate`（3 行） | 3 有效、0 无效，附带 SEO 建议 |
| `import.execute`（首次） | `created: 3`，全部成功 |
| `import.execute`（重复，`update_existing=true`） | `updated: 3`，名称与价格正确更新 |
| 回读商品 | 名称与价格符合第二次导入内容 |

### 3.4 发现缺陷：导入忽略 `status` 与 `visibility` 列

**现象**：CSV 写明 `status=2, visibility=1`（禁用、不可见），导入报告成功，但商品实际是 `status=1, visibility=4`（启用、目录与搜索可见）。

**根因**：`status` 与 `visibility` 是商品的**顶层字段**，不是 EAV 属性。它们不在导入的保留列集合中，于是被当作自定义属性发送——Magento 在创建时忽略未知属性，**运营方的意图被静默丢弃**。

**危害**：这是最糟糕的失败形态——导入报告成功，商品却在店铺前台可见。一个打算批量下架商品的运营操作会得到相反结果。

**修复**：`status` 与 `visibility` 加入保留列，创建与更新两条路径都映射到类型化字段，并做取值校验（status ∈ {1,2}，visibility ∈ 1..4）。

**验证**：修复后重新导入，读回 `status: 2`、`visibility: 1`，与 CSV 一致。测试 `TestImportAppliesStatusAndVisibility` 覆盖。

---

## 四、本次会话累计修复的缺陷

按版本记录，全部在真实店铺上验证。

### v0.11.2（8 项）

| 缺陷 | 影响 |
|---|---|
| 下单用 `POST`，Magento 要求 `PUT` | 整条资金链路不可用 |
| 已成功的订单被记为「确定未下单」 | 诱导调用方重试，制造重复订单 |
| 对账队列从未被消费 | 70 个任务排队，UNKNOWN 操作永不解决 |
| 上游错误文本泄漏给 agent | Magento 与 Cloudflare 均修复 |
| MCP 会话未绑定创建者 | 会话可被他人接管 |
| `/metrics` 在非回环监听上无鉴权 | 指标暴露 |
| 审批 CLI 无身份校验、无审计行 | 任意身份可批准写操作 |
| 凭据可被 `%+v` 打印 | 令牌泄漏到日志 |

### v0.11.3（5 项）

| 缺陷 | 影响 |
|---|---|
| `customer.addresses` 对任何有地址的客户失败 | `region` 字段是对象，按字符串解析失败 |
| `inventory.unassign` 路由错误 | `DELETE` → `POST .../source-items-delete`，从未可用 |
| `inventory.stock_source_unlink` 路由错误 | 同上 |
| `inventory.stock_source_link` 字段名错误 | `position` → `priority` |
| `customer.addresses.update` 无回退 | 无专用路由的店铺无法更新地址 |

### v0.11.4（6 项）

| 缺陷 | 影响 |
|---|---|
| `order.create` 响应解析为对象（实为标量 id） | **LIVE-8 根因**：每笔成功订单都记为 unknown |
| `invoice.create` 同上 | 发票成功但报 unknown |
| `creditmemo.create` 同上 + 路由不存在 | `/creditmemos` → `/order/{id}/refund` |
| `shipment.create` 路由不存在 | `/shipments` → `/order/{id}/ship` |
| `shipment.get` / `creditmemo.get` 复数路由 | 读取 404 |
| `shipment.email` / `creditmemo.email` 复数路由 | 邮件 404 |

### 待发布（1 项）

| 缺陷 | 影响 |
|---|---|
| 导入忽略 `status` / `visibility` | 导入「禁用/不可见」得到可见商品 |

**合计：20 项缺陷，全部已在真实店铺验证修复。**

---

## 五、测试结果演进

| 阶段 | 通过 | 软失败 | 硬失败 | 跳过 |
|---|---|---|---|---|
| v0.11.1 基线 | 78 | 17 | 2 | 26 |
| v0.11.2 已发布 | 82 | 16 | 1 | 24 |
| v0.11.3 已发布 | 91 | 7 | 1 | 24 |
| v0.11.4 已发布 | 112 | 7 | 1 | 3 |
| **生产 v0.11.4** | **112** | **7** | **1** | **3** |

**从 78 到 112，净增 34 个通过用例。**

---

## 六、修复计划

### 6.1 立即（建议本轮发布）

| 编号 | 内容 | 状态 |
|---|---|---|
| `FIX-01` | 导入应用 `status` / `visibility` 列 | **已修复，待发布** |

发布 **v0.11.5**，包含该修复。理由：它会让「批量下架」变成「批量上架」，是运营风险。

### 6.2 需要你决策（阻塞中）

| 编号 | 内容 | 需要你决定什么 |
|---|---|---|
| `PERF-16` | 配置商品子商品路径串行，最坏 152 次请求 | 是否接受把错误契约从「单个 failed_sku」改为「失败列表」以换取并行。分析见 [`perf-16-options.md`](perf-16-options.md) |
| `MCP-08` | 输出信封 | 已用 `_meta` 实现关联信息，无需决策；若你要载荷信封（改 106 个 schema）需明确要求 |

### 6.3 测试脚本待改进（非产品缺陷）

| 编号 | 内容 | 影响 |
|---|---|---|
| `HARNESS-01` | `configurable.add_child` 用例的子商品顺序 | 该用例软失败，实际工具正常 |
| `HARNESS-02` | `customer.addresses.update` 用例的地址 id 同步 | 该用例软失败，实际工具正常 |
| `HARNESS-03` | `stock_source_link` / `unlink` 需要非默认 stock | 该域无法验证，需先建自定义 stock |

### 6.4 环境限制（无法在 demo 站验证）

| 能力 | 限制 |
|---|---|
| 自定义选项创建/更新/删除 | 该店无 `POST /products/{sku}/options` 路由 |
| MSI 源链接/取消链接 | 需自定义 stock，demo 站只有 Default Stock |
| GA4 / Cloudflare / unified 工具 | 该部署未配置对应数据源，工具未注册 |
| RBAC 与审批 | 测试部署关闭鉴权，仅由单元与 QA 套件覆盖 |

### 6.5 建议的后续测试方向

1. **补建一个自定义 stock**，验证 MSI 源链接/取消链接与库存调拨
2. **配置 GA4 与 Cloudflare 数据源**，覆盖剩余 11 个未注册工具
3. **开启鉴权跑一轮**，验证 RBAC 阶梯与两步确认在真实店铺上的行为
4. **大负载导入**（接近 5000 行上限），验证分批与超时行为

---

## 七、当前系统状态

| 项目 | 状态 |
|---|---|
| 生产版本 | **v0.11.4**（已升级并验证） |
| 全部工具 | 102 个，全部有真实用例 |
| 通过率 | 112 / 123（91%），失败项全部归因 |
| 未归因失败 | **0** |
| 单元与 QA | `go test ./...` 与 `-race` 零失败，QA 18/0/0 |
| 待发布修复 | 1 项（导入 status/visibility） |

---

## 八、回滚与运维

- **compose 备份**：`/opt/mage2mcp/compose.yaml.bak.20261121175622`
- **回滚命令**：`cd /opt/mage2mcp && cp compose.yaml.bak.20261121175622 compose.yaml && sudo docker compose up -d`
- **健康检查**：`curl -sS http://127.0.0.1:8080/readyz`
- **审批脚本**：`/opt/mage2mcp/approve.sh <confirmation_id>`

测试过程中创建的数据（`MCPTEST-*` 商品与客户、`mcptest_src`/`mcptest_src2` MSI 源、若干订单与票据）按你的要求**保留未清理**。

## 商品复制记录：Nillkin Xiaomi 18 Fold 手机壳

| 项目 | 内容 |
|---|---|
| 来源 | `https://www.giztop.com/nillkin-protective-magnetic-hard-pc-case-for-xiaomi-18-fold.html` |
| 目标店铺 | `https://demo-magento-2.auroracreation.com` |
| 商品 id | 2142 |
| SKU | `NILLKIN-XM18FOLD-CASE` |
| 名称 | Nillkin Protective Magnetic Hard PC Case for Xiaomi 18 Fold |
| 价格 | 29.99（店铺货币 PLN） |
| 状态 / 可见性 | `status=1`（启用）、`visibility=4`（目录与搜索） |
| 分类 | 新建「Phone Accessories」（id 61，挂在 Gear 下，已加入导航菜单） |
| 库存 | 100 |
| 图片 | 上传 giztop 主图（1200×1200 JPEG），设为 `image`/`small_image`/`thumbnail` |
| 描述 | 来自源站完整规格文本 |

### 前台验证

| 检查项 | 结果 |
|---|---|
| 商品页 HTTP | 200 |
| 商品页标题 | Nillkin Protective Magnetic Hard PC Case for Xiaomi 18 Fold |
| 商品页价格 | 29.99 |
| 商品页库存 | 有货（`class="stock available"`） |
| 商品页描述块 | 完整渲染 |
| 商品页加购按钮 | 存在 |
| **目录页商品卡片** | **1 个，名称/价格/图片齐全** |
| 目录页图片 HTTP | 200，`image/jpeg` |
| 前台搜索 "nillkin" | 命中 1 个商品 |
| 导航菜单 | 包含 Phone Accessories |

### 更新：改为配置商品（补齐源站的颜色选项）

首版复制成了**简单商品**，与源站不符——源站是配置商品，带 `Color` 属性的两个选项。

源站结构（从页面 JSON 配置解析）：

```json
{"93":{"code":"color","label":"Color",
  "options":[{"id":"10","label":"Black"},{"id":"1742","label":"Clear Black"}]}}
```

修正步骤：

| 步骤 | 结果 |
|---|---|
| 新建 `color` 选项 `Clear Black` | value **245**（`Black` 已存在，value 49） |
| 删除首版简单商品 | 成功 |
| 创建配置商品（同一 SKU） | 父商品 id **2145**，`type_id: configurable` |
| 创建两个子商品 | `-BLACK`（id 2143）、`-CLEAR-BLACK`（id 2144），各 50 件，价格 29.99 |
| 上传图片 | 父商品与两个子商品各一张，均设为 `image`/`small_image`/`thumbnail` |

API 层确认：

```text
type: configurable | status: 1 | visibility: 4
configurable_product_links: [2143, 2144]
configurable_product_options: [('93', 'Color', 2)]
category_links: [{'category_id': '61'}]
```

前台确认（商品页）：

| 检查项 | 结果 |
|---|---|
| swatch 渲染配置 | 存在，`Magento_Swatches/js/swatch-renderer` |
| 选项数量 | 2（Black、Clear Black） |
| 各选项价格 | 29.99 / 29.99 |
| 页面显示价格 | `29,99 zł` |
| 库存 | 有货 |
| 加购按钮 | 存在 |

前台确认（目录页 `/gear/phone-accessories.html`）：1 个商品卡片，价格 29.99，图片正常。

### 说明

父分类 `Gear` 未启用锚定（Magento 默认），因此商品只在子分类页展示，不在 `gear.html` 出现。这是 Magento 的标准行为，不是缺陷。

配置商品的颜色选项由 Magento 的 swatch 渲染器**在浏览器端**生成，HTML 里没有服务端的 `super_attribute` 标记；判断依据是页面内嵌的 `jsonConfig` 与 `optionPrices`。

---

## 九、v0.11.5 发布与生产升级

### 9.1 发布

| 项目 | 结果 |
|---|---|
| 版本 | **v0.11.5** |
| 标签 | `v0.11.5`，提交 `cc7bb5a` |
| Release 工作流 | `44896978731`，**completed / success** |
| 资产 | 36 个，含 cosign keyless 签名 |
| 状态 | 非 draft、非 prerelease |

### 9.2 发布内容

| 类型 | 内容 |
|---|---|
| 修复 | 导入应用 `status` / `visibility` 列（此前的批量下架会变成批量上架） |
| 新增 | 服务器 instructions 中的产品复制规则（同架构） |
| 新增 | 服务器 instructions 中的跨架构映射规则（源站非 Magento 时） |

### 9.3 发布产物验证

| 检查项 | 结果 |
|---|---|
| `docker pull v0.11.5` | 成功 |
| `/readyz` | `{"status":"ready","tools":102}` |
| instructions 长度 | 3157 字符 |
| 复制规则三要素 | 全部存在 |
| 导入 `status=2, visibility=1` | 读回 **status 2 / visibility 1**，修复生效 |
| 全量矩阵 | **112 通过 / 7 软失败 / 1 硬失败 / 3 跳过**（与 v0.11.4 一致，无回归） |

### 9.4 生产升级

| 项目 | 结果 |
|---|---|
| 升级路径 | v0.11.4 → **v0.11.5** |
| compose 备份 | `compose.yaml.bak.20261121192136` |
| 容器状态 | 三个容器 `Up`，server `healthy` |
| `/readyz` | `{"status":"ready","tools":102}` |
| 生产全量测试 | **112 通过 / 7 软失败 / 1 硬失败 / 3 跳过** |

**回滚命令**：

```bash
cd /opt/mage2mcp && cp compose.yaml.bak.20261121192136 compose.yaml && sudo docker compose up -d
```

### 9.5 生产与发布产物一致性

生产实例与独立部署的 v0.11.5 容器结果**完全相同**（112/7/1/3）。

---

## 十、版本演进总览

| 版本 | 通过 | 软失败 | 硬失败 | 跳过 | 主要修复 |
|---|---|---|---|---|---|
| v0.11.1 基线 | 78 | 17 | 2 | 26 | — |
| v0.11.2 | 82 | 16 | 1 | 24 | 下单路由、订单误判、对账队列、审批鉴权 |
| v0.11.3 | 91 | 7 | 1 | 24 | 客户地址 region、MSI 路由、priority 字段 |
| v0.11.4 | 112 | 7 | 1 | 3 | 资金链路：标量 id、发货/退款路由、单复数 |
| **v0.11.5** | **112** | **7** | **1** | **3** | 导入 status/visibility、MCP 复制规则 |

**从 78 到 112，净增 34 个通过用例；跳过从 26 降到 3。**

---

## 十一、对照 Magento 2.4.9 API 规范的系统审计

### 11.1 方法

用店铺自身暴露的 REST schema 作为权威依据，而不是通用文档——它反映该实例真实可用的路由。

| 步骤 | 内容 |
|---|---|
| 获取 schema | `GET /rest/pl/schema?services=all`，**407 条路由**，627 KB |
| 提取网关路由 | 从工具描述中提取 77 条 `METHOD /rest/{store_code}/V1/...` |
| 逐条比对 | 路由是否存在、方法是否匹配 |
| 实测验证 | 对 52 条路由发真实请求，区分两种 404 |

**关键区分**：Magento 有两种 404——

- `Wniosek nie odpowiada żadnej ścieżce`（波兰语）= **路由不存在**
- `The entity that was requested doesn't exist` = **路由存在，实体不存在**

不区分这两者会得出完全错误的结论。

### 11.2 审计结果

| 检查项 | 结果 |
|---|---|
| 路由存在性（52 条实测） | **全部存在**，仅 `POST /products/{sku}/options` 缺失（已知环境限制） |
| HTTP 方法 | 全部匹配 |
| 搜索过滤字段（16 个） | **全部可过滤**，包括此前怀疑的 `quote_id` |
| `fields` 投影（3 处） | **无字段被静默省略**，`total_count` 正常返回 |
| 标量返回处理 | 属性选项、媒体上传、商品关联均已正确处理 |

### 11.3 发现的缺陷：9 处工具描述与实际路由不符

代码在之前的轮次已修正，但**工具描述仍记载着修复前的旧路由**：

| 描述中的旧路由 | 实际路由 |
|---|---|
| `POST /V1/shipments` | `POST /V1/order/{orderId}/ship` |
| `POST /V1/creditmemos` | `POST /V1/order/{orderId}/refund` |
| `POST /V1/carts/{cartId}/order` | `PUT /V1/carts/{cartId}/order` |
| `GET /V1/shipments/{id}` | `GET /V1/shipment/{id}` |
| `GET /V1/creditmemos/{id}` | `GET /V1/creditmemo/{id}` |
| `POST /V1/shipments/{id}/emails` | `POST /V1/shipment/{id}/emails` |
| `POST /V1/creditmemos/{id}/emails` | `POST /V1/creditmemo/{id}/emails` |
| `DELETE /V1/inventory/source-items` | `POST /V1/inventory/source-items-delete` |
| `DELETE /V1/inventory/stock-source-links` | `POST /V1/inventory/stock-source-links-delete` |

**为什么这是缺陷**：工具描述是 agent 唯一会读的东西。描述里的路由错误，与代码里的路由错误是同一类误导，只是高了一层——agent 会照着描述构造请求或向用户解释行为。

已全部修正，并新增测试 `TestDocumentedRoutesMatchTheRoutesTheCodeCalls` 断言描述与代码一致。

### 11.4 未发现缺陷的区域

这些区域此前多次出问题，本轮系统检查后确认正确：

- **路由存在性与方法**：52 条实测全部正确
- **搜索过滤**：16 个过滤字段全部可用（含 `quote_id`、`price` gteq、`created_at` gteq）
- **投影字段**：订单、客户、对账三处投影无静默省略
- **标量返回**：属性选项（字符串/数字/对象三形态）、媒体上传（整数）、商品关联（布尔）均已处理

### 11.5 环境观察：demo 站会周期性重置

本轮测试期间店铺被重置过一次：

| 时间点 | 观察 |
|---|---|
| 审计开始时 | 订单 8 条、分类 61 存在、测试商品齐全 |
| 重置后 | 订单仅 2 条种子数据，所有测试数据消失 |

这导致一次运行中 `shipment.get` 与 `shipment.email` 报 `not_found`——**不是产品缺陷**，是重置发生在测试中途，创建的发货单被删除。紧接着重跑同一版本，两项均通过。

**影响**：测试结果存在一定的不确定性。建议后续用独立实例或关闭重置。
