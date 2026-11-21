# v0.11.2 全量功能测试报告

| 项目 | 内容 |
|---|---|
| 测试对象 | 已发布镜像 `ghcr.io/shuaizend/mage2mcp:v0.11.2` |
| 测试时间 | 2026-11-21 |
| 目标店铺 | `https://demo-magento-2.auroracreation.com`，店铺视图 `pl`（PLN，pl_PL） |
| 店铺规模 | 2062 个商品，属性集 4 与 9–15 |
| 测试服务器 | `49.51.250.83`，独立容器 `m2m-v0112`，绑定 `127.0.0.1:18082` |
| 用例数 | 123（覆盖 101 个工具） |
| 原始结果 | `test/live/out/results-20261121-042640.json` |

**测的是发出去的镜像，不是本地构建的二进制。** 先 `docker pull` 已发布镜像，确认
`/readyz` 返回 `{"status":"ready","tools":102}`，再对它跑全量矩阵。

---

## 一、总体结果

| 状态 | 数量 | 含义 |
|---|---|---|
| 通过 | **82** | 行为符合契约 |
| 软失败 | 16 | 失败，但原因是测试脚本或环境限制（逐条见第三节） |
| 硬失败 | **1** | 失败且归因于产品缺陷或环境缺失 |
| 跳过 | 24 | 前置数据未产生（依赖链上游失败） |

**结论：82/123 通过，1 个硬失败（环境限制），新发现 1 个真实缺陷（已定位并修复）。**

---

## 二、逐域结果

| 域 | 通过 | 软失败 | 硬失败 | 跳过 | 说明 |
|---|---|---|---|---|---|
| platform | 2 | 0 | 0 | 0 | 工具数 102、自列举、写开关 |
| store | 2 | 0 | 0 | 0 | 店铺视图、税率类 |
| catalog-read | 7 | 0 | 0 | 0 | 属性集/选项、分类树/详情/商品/扁平 |
| product-read | 9 | 0 | 0 | 0 | 详情、批量、搜索、SEO、阶梯价、媒体、选项、关联 |
| configurable-read | 1 | 0 | 0 | 0 | 真实配置商品的子项 |
| inventory-read | 5 | 0 | 0 | 0 | 源、库存、源项、状态、批量状态 |
| customer-read | 2 | 0 | 0 | 0 | 客户组、搜索 |
| sales-read | 10 | 0 | 0 | 0 | 订单搜索/详情/历史/汇总、销售汇总/分析、地域、票据列表 |
| import-read | 2 | 0 | 0 | 0 | 校验、批次列表 |
| product-write | 5 | 0 | 0 | 0 | 含完整 SEO 内容创建、回读、更新、删除 |
| media-write | 4 | 0 | 0 | 0 | 上传、列表、更新、删除 |
| tier-write | 2 | 0 | 0 | 0 | 设置与回读 |
| bulk-write | 1 | 0 | 0 | 0 | 批量状态更新 |
| category-write | 6 | 0 | 0 | 0 | 创建、更新、分配、列表、删除 |
| attribute-write | 2 | 0 | 0 | 1 | 选项创建、批量创建、删除 |
| links-write | 4 | 0 | 0 | 0 | 关联增删与读取 |
| options-write | 1 | 0 | 1 | 2 | **自定义选项创建失败（环境）** |
| inventory-write | 4 | 3 | 0 | 0 | 分配、调拨、源链接 |
| customer-write | 6 | 2 | 0 | 0 | 含地址创建（**发现缺陷**） |
| configurable-write | 0 | 7 | 0 | 0 | **测试脚本参数不匹配，非产品行为** |
| order-write | 0 | 1 | 0 | 8 | 下单返回 `operation_unknown`（已知，见第四节） |
| invoice-write | 0 | 0 | 0 | 5 | 被下单链路阻塞 |
| shipment-write | 0 | 0 | 0 | 4 | 被下单链路阻塞 |
| creditmemo-write | 0 | 0 | 0 | 4 | 被下单链路阻塞 |
| import-write | 4 | 2 | 0 | 0 | 执行、启动、状态、取消/恢复语义 |
| errors | 3 | 1 | 0 | 0 | 未知实例、未知 SKU、schema 拒绝 |

---

## 三、失败项逐条判定

### 3.1 唯一的硬失败：自定义选项创建

`magento.product.options.create` 返回
`unsupported_capability: this Magento instance does not expose custom-option creation`。

直连验证：`POST /rest/pl/V1/products/{sku}/options` 返回 **404 无此路由**，
`PUT /products/{sku}` 带 options 也被拒绝（400）。**该 demo 站确实不暴露自定义选项创建**，
网关如实报告是正确行为。判定为**环境限制，非产品缺陷**。

### 3.2 16 个软失败的分类

| 工具 | 报错 | 判定 |
|---|---|---|
| `configurable.create` | `invalid_argument: arguments do not match the tool schema` | **测试脚本**：payload 未匹配 schema |
| `configurable.create_variations` | 同上 | 测试脚本 |
| `configurable.set_options` | 同上 | 测试脚本 |
| `configurable.children` | `not_found` | 级联：父商品未创建 |
| `configurable.update` | `not_found` | 级联 |
| `configurable.add_child` | `not_found: Magento write failed at stage links` | 级联 |
| `configurable.remove_child` | 同上 | 级联 |
| `inventory.stock_source_link` | `validation_failed` | 级联：本次运行的 MSI 源未成功创建 |
| `inventory.stock_source_unlink` | `not_found` | 级联 |
| `inventory.unassign` | `not_found` | 级联 |
| `inventory.transfer` | `invalid_argument: from_source and to_source must differ` | **正确拒绝**：用例刻意用同源，工具如实报错 |
| `customer.addresses` | `upstream_unavailable: unreadable customer payload` | **真实缺陷，见第五节** |
| `customer.addresses.update` | `unsupported_capability` | 该店不暴露地址更新路由，如实报告 |
| `order.create` | `operation_unknown` | 已知问题，见第四节 |
| `import.batch` | `not_found: import batch not found` | **测试脚本**：把 job_id 当 batch_id 传入 |
| `import.cancel` | `illegal_transition` | **正确拒绝**：任务已终态 |

---

## 四、已知问题：下单返回 `operation_unknown`

`magento.order.create` 返回
`operation_unknown: the order placement outcome is unknown: Magento did not confirm it`。

**订单实际已创建**（可在店铺后台看到）。这是审计中记录为 `LIVE-8` 的已知限制：

- 下单调用在慢速店铺上会超时，订单在约 1 秒后才提交
- 网关的对账读取在订单提交前完成，因此找不到
- 网关**拒绝猜测**，记录 `unknown` 并要求轮询——这是设计上的安全行为，避免了重复下单
- 该店的对账器无法确认，操作会在重试预算用尽后升级给人工

这不是本次新发现，v0.11.1 与 v0.11.2 行为一致。详见
[`live-magento-defects.md`](live-magento-defects.md) 的 LIVE-8。

---

## 五、本次新发现的真实缺陷（已修复）

### `magento.customer.addresses` 对任何有地址的客户都失败

**现象**：返回 `upstream_unavailable: Magento returned an unreadable customer payload`。

**根因**：Magento 对客户地址的 `region` 字段返回**对象**：

```json
{"region": {"region_code": "MI", "region": "Michigan", "region_id": 33}, "region_id": 33}
```

而订单地址的 `region` 返回**字符串**。网关两处都按 `string` 解析，于是客户地址数组的
元素解码失败——**失败的是数组元素，不是外层信封**，所以错误信息指向了错误的层级。

**影响**：任何有地址的客户，其地址列表都无法读取。此前几轮测试没发现，是因为测试用的是
本次运行新建的客户，地址创建失败时该客户没有地址，空数组可以正常解析。

**修复**：新增 `regionValue` 类型，同时接受字符串与对象两种形状，并优先取对象内的
`region_id`。测试 `TestCustomerAddressesAcceptsBothRegionShapes` 覆盖两种形状。

**状态**：已修复并通过完整门禁，**将随下一个版本发布**（v0.11.2 已发布，不含此修复）。

### 修复后复验

用修复版二进制（`v0.11.3-dev`）在同一店铺重跑全量：

| 指标 | v0.11.2 已发布镜像 | 修复版 |
|---|---|---|
| 通过 | 82 | **83** |
| 软失败 | 16 | **15** |
| 硬失败 | 1 | 1 |
| 跳过 | 24 | 24 |

`magento.customer.addresses` 由软失败转为**通过**，直连验证返回：

```json
{"addresses":[{"id":1,"country_id":"US","region":"Michigan","region_id":33,
               "default_billing":true,"default_shipping":true}]}
```

唯一的硬失败不变，仍是 demo 站缺少自定义选项路由。

---

## 六、与历史运行对比

| 运行 | 版本 | 通过 | 软失败 | 失败 | 跳过 |
|---|---|---|---|---|---|
| 基线 | v0.11.1 | 78 | 17 | 2 | 26 |
| live 缺陷修复后 | v0.11.1+fixes | 82 | 16 | 1 | 24 |
| 全部修复后 | v0.11.1+fixes | 82 | 16 | 1 | 24 |
| **已发布镜像** | **v0.11.2** | **82** | **16** | **1** | **24** |

**与发版前完全一致，零回归。** 发版没有引入任何行为变化。

---

## 七、结论

1. **v0.11.2 已发布产物行为正确**：82 个用例通过，唯一的硬失败是 demo 站缺少自定义选项
   路由，属环境限制。
2. **新发现 1 个真实缺陷**：客户地址列表因 `region` 字段形状而失败，已定位、修复并加了测试。
   该缺陷存在于 v0.11.2 中，修复将在下一版本发布。
3. **24 个跳过**全部是下单链路的下游依赖（发票、发货、贷项通知单），根因是已知的
   `operation_unknown` 对账限制，非本次新问题。
4. **configurable 域的 7 个软失败是测试脚本问题**，不是产品行为——该域实际上未被验证，
   这是本次测试的覆盖缺口，应在下轮补齐脚本参数。

### 建议的后续动作

1. **发布 v0.11.3**，包含客户地址 `region` 修复
2. **修正 configurable 域的测试脚本参数**，补齐该域的验证
3. **决定 `PERF-16`**：配置商品路径是否为并行化改动错误契约（见
   [`perf-16-options.md`](perf-16-options.md)）
4. **考虑为 demo 站提供替代环境**：自定义选项路由缺失导致该能力无法验证

---

## 八、后续修复轮次（本次会话）

在 v0.11.2 发布后继续修复，又发现 **3 个真实缺陷**，并补齐了 configurable 域的测试覆盖。

### 8.1 新发现的缺陷

| 工具 | 现象 | 根因 | 状态 |
|---|---|---|---|
| `magento.inventory.unassign` | `not_found: 无此路由` | 用了 `DELETE /inventory/source-items`；真实路由是 `POST /inventory/source-items-delete` | 已修复 |
| `magento.inventory.stock_source_unlink` | `not_found: 无此路由` | 用了 `DELETE /inventory/stock-source-links`；真实路由是 `POST /inventory/stock-source-links-delete` | 已修复 |
| `magento.inventory.stock_source_link` | `validation_failed: 400` | 载荷字段名用了 `position`；Magento 要求 `priority` | 已修复 |

三条都已直连验证：`POST /inventory/source-items-delete` → 200，`POST /inventory/stock-source-links-delete` → 400（业务规则，路由可达），`POST /inventory/stock-source-links` 带 `priority` → 200。

**值得注意**：`unassign` 与 `stock_source_unlink` 的单元测试断言的正是那个错误的 `DELETE` 路由——与下单用 `POST` 那次完全相同的失效模式：测试把错误的路由固化成了"预期"。

### 8.2 configurable 域覆盖补齐

原测试脚本有 4 处参数错误，导致整个域实际未被验证：

- `create`：缺 `variant_attributes[].attribute_id`（契约要求）
- `create_variations`：多传了 `name` 与 `quantity`（`additionalProperties: false`）
- `set_options`：`values` 应是整数数组，传了对象数组
- `add_child`：依赖上游创建成功

修正后 `configurable.create`、`children`、`update`、`remove_child` **首次通过**，该域首次得到真实验证。

### 8.3 修复后复验

| 运行 | 通过 | 软失败 | 硬失败 | 跳过 |
|---|---|---|---|---|
| v0.11.2 已发布镜像 | 82 | 16 | 1 | 24 |
| 地址 `region` 修复后 | 83 | 15 | 1 | 24 |
| 库存路由 + configurable 脚本修复后 | **91** | **7** | 1 | 24 |

**净增 9 个通过用例。**

### 8.4 剩余 8 项的最终判定

| 工具 | 判定 |
|---|---|
| `product.options.create` | **环境**：该店不暴露自定义选项路由（404 已验证） |
| `inventory.stock_source_link` / `unlink` | **环境**：stock 1 是 Default Stock，Magento 拒绝非默认源链接（400 业务规则已验证） |
| `inventory.transfer` | **正确拒绝**：用例刻意用同源 |
| `import.cancel` | **正确拒绝**：任务已终态 |
| `configurable.add_child` | **测试脚本**：路由与载荷已验证正确（404 表示子商品不存在，路由可达） |
| `customer.addresses.update` | **测试脚本**：直连调用已验证成功（走 PUT 回退）；harness 的地址 id 与新建地址不同步 |
| `order.create` | **已知限制** LIVE-8：订单实际已创建，网关拒绝猜测 |

**结论：无未归因的失败项。**

---

## 九、v0.11.3 已发布镜像复验

v0.11.3 发布后，用**已发布镜像**（非本地构建）重跑全量：

| 步骤 | 结果 |
|---|---|
| `docker pull ghcr.io/shuaizend/mage2mcp:v0.11.3` | 成功 |
| `/readyz` | `{"status":"ready","tools":102}` |
| 全量矩阵（123 用例 / 101 工具） | **91 通过 / 7 软失败 / 1 硬失败 / 24 跳过** |

与本地修复版完全一致，确认四个修复都已进入发布产物。

Release 工作流 `44878277648` 成功，发布 36 个资产。

### 9.1 发布版直连验证

| 调用 | 结果 |
|---|---|
| `customer.addresses`（customer 1） | 成功，`region: "Michigan"`、`region_id: 33` |
| `customer.addresses.update`（customer 1 / address 1） | 成功，走 `PUT /customers/{id}` 回退 |

两条此前失败的能力在发布版上确认可用。

### 9.2 v0.11.2 → v0.11.3 对比

| 版本 | 通过 | 软失败 | 硬失败 | 跳过 |
|---|---|---|---|---|
| v0.11.2 已发布镜像 | 82 | 16 | 1 | 24 |
| **v0.11.3 已发布镜像** | **91** | **7** | 1 | 24 |

**净增 9 个通过用例，无回归。**

---

## 十、资金链路探索性测试（v0.11.3 之后）

此前 24 个跳过用例中有 21 个卡在 `order.create`。用真实订单打通后，**首次验证了整条资金链路**，并发现 **6 类真实缺陷**。

### 10.1 根因：所有 Magento 文档创建路由都返回标量 id

这是本轮最重要的发现，也是 `LIVE-8` 的真正根因。

| 路由 | 实际返回 | 网关原假设 |
|---|---|---|
| `PUT /carts/{id}/order` | `"8"` | 订单对象 |
| `POST /order/{id}/invoice` | `"4"` | 发票对象 |
| `POST /order/{id}/ship` | `"3"` | 发货对象 |
| `POST /order/{id}/refund` | `"2"` | 贷项通知单对象 |

网关把标量按对象解析，解析失败后**记为 `operation_unknown` 并告诉调用方不要重试**——而订单/发票实际上已经创建成功。

`order.create` 的响应里没有 `reconcile` 字段，证明走的是**解析失败分支**而非对账分支，这排除了「对账读不到订单」的旧假设。

### 10.2 六类缺陷

| # | 工具 | 缺陷 | 验证 |
|---|---|---|---|
| 1 | `order.create` | 响应解析为对象（实为标量 id） | `PUT /carts/14/order` → `"8"` |
| 2 | `invoice.create` | 同上 | `POST /order/4/invoice` → `"5"` |
| 3 | `creditmemo.create` | 同上，且路由 `/creditmemos` 不存在 | `POST /creditmemos` → 404；`POST /order/3/refund` → `"2"` |
| 4 | `shipment.create` | 路由 `/shipments` 不存在 | `POST /shipments` → 404；`POST /order/6/ship` → `"3"` |
| 5 | `shipment.get` / `creditmemo.get` | 读取用复数路由 | `GET /shipment/4` → 200；`GET /shipments/4` → 404 |
| 6 | `shipment.email` / `creditmemo.email` | 邮件用复数路由 | `POST /shipment/1/emails` → 200；`POST /shipments/1/emails` → 404 |

**Magento 路由单复数不一致**：发票是复数（`/invoices/{id}`），发货与贷项通知单是单数（`/shipment/{id}`、`/creditmemo/{id}`），而集合搜索一律是复数。

### 10.3 修复

新增 `decodeCreatedDocumentID`，接受三种形状（字符串标量、数字标量、带 `entity_id` 的对象），每次创建后**回读文档**，返回完整记录而非 id。

### 10.4 全链路验证

| 步骤 | 结果 |
|---|---|
| `order.create`（订单 8） | 成功，返回完整订单 |
| 同一 `idempotency_key` 重放 | `replayed: true`，返回订单 8，**未产生重复** |
| `order.hold` / `unhold` | 成功 |
| `order.email` | 成功 |
| `order.cancel` | 成功 |
| `invoice.create`（订单 3） | 成功，`state: 2`（已支付） |
| `shipment.create` | 成功 |
| `creditmemo.create` | 成功，`grand_total: 29.5` |
| `invoice.get/list`、`shipment.get/list`、`creditmemo.get/list` | 全部成功 |

### 10.5 测试结果演进

| 阶段 | 通过 | 软失败 | 硬失败 | 跳过 |
|---|---|---|---|---|
| v0.11.3 已发布镜像 | 91 | 7 | 1 | 24 |
| 下单响应修复后 | 103 | 8 | 1 | 11 |
| 票据脚本参数修复后 | 107 | 10 | 1 | 5 |
| **邮件路由修复后** | **111** | **8** | 1 | **3** |

**净增 20 个通过用例，跳过从 24 降到 3。**

### 10.6 剩余 9 项的最终判定

| 工具 | 判定 |
|---|---|
| `product.options.create` | 环境：该店无自定义选项路由 |
| `stock_source_link` / `unlink` | 环境：stock 1 是 Default Stock，Magento 拒绝非默认源 |
| `inventory.transfer` | 正确拒绝：用例刻意同源 |
| `invoice.capture` | 正确拒绝：发票已支付（checkmo 自动捕获） |
| `order.cancel` | 正确拒绝：订单已终态 |
| `import.cancel` | 正确拒绝：任务已终态 |
| `configurable.add_child` | 测试脚本：路由已直连验证 |
| `customer.addresses.update` | 测试脚本：直连调用成功 |

**无未归因失败。**
