# 语义层测试汇总与分级（SUMMARY）

- 输入：`catalog.md`（t53）、`sales.md`（t54）、`inventory.md`（t55）、`customers-import.md`（t56）、
  `mcp-protocol.md`（t57）、`security.md`（t58）、`docs-config.md`（t59）
- 范围：生产 v0.11.5（网关 `127.0.0.1:18110` → 生产 8080，实例 `demo`，店铺 `pl`）的**语义层**
  实测；本汇总只汇总与分级，**未修改任何代码**。
- 核验方式：对本汇总中每条 CONFIRMED 至少复核一次。可现场复现的用 MCP 工具 + 直连 Magento REST
  对照（标 `REPRODUCED (live)`）；只能静态确认的读当前工作树代码/文档（标 `REPRODUCED (code)` /
  `REPRODUCED (doc)`）；依赖 demo 状态或特殊配置的标 `ENV-DEPENDENT`；报告标 SUSPECTED 的保留
  `SUSPECTED`，不作为已确认缺陷。
- 重要前提：语义报告测的是**部署的 v0.11.5**；当前工作树是 `v0.11.5-4`（tag 后有变更）。本汇总
  逐条标注「当前树」状态，修复阶段以当前树为准。

## 0. 统计

| 项目 | 数量 |
|---|---|
| 7 份报告的原始发现 | 67 条 SEM-* 小节（含 NOT-A-DEFECT 与 SUSPECTED） |
| 去重后的发现 | **51 条**（P0 2、P1 7、P2 14、P3 27，另有 1 条 NOT-REPRODUCED） |
| 其中 CONFIRMED | **45 条** |
| 其中 SUSPECTED（不作缺陷计） | 5 条（SEM-15/36/49/50/51） |
| 其中 NOT-REPRODUCED（不计入） | 1 条（SEM-06） |
| 非产品缺陷（环境/店铺/部署配置/测试脚本） | 12 条（见 §5） |
| 不建议修复 | 6 条（见 §6） |
| 已在本会话现场复现 | 10 条（P0 1、P1 5、P2 1、P3 3） |
| 通过代码/文档静态复核 | 33 条 |
| ENV-DEPENDENT（依赖 demo 状态/配置） | 2 条（SEM-13、SEM-14） |

覆盖工具：7 份报告合计直接调用 **80+ 个工具**（102 个已注册工具中的绝大多数读工具、全部
customer/import/sales/inventory 写工具、确认/限流/协议面），详见各报告的工具清单。

## 1. 分级口径

- **P0（资金/安全/数据损坏）**：会造成实际资金损失、跨信任域访问，或不可逆的数据错误。
- **P1（声明与行为不符导致误操作）**：工具/文档声称 A，实际做 B，调用方据此会做错事
  （假成功、丢字段、错误状态、必失败的操作）。
- **P2（诚实性问题、边界）**：输出/错误信息与事实不符、边界语义不清、诊断信息缺失，但不直接
  导致错误写入。
- **P3（文档与措辞）**：文档、描述、schema 文字与实际不一致；不影响运行时行为。

与各报告原级别的差异在「备注」中说明；总体原则是：**文档问题一律 P3，工具行为问题按后果定级**，
环境限制不计入产品缺陷。

## 2. 去重后的发现清单

图例：来源 = 各报告原编号；核验 = REPRODUCED (live/code/doc) / ENV-DEPENDENT / SUSPECTED。

### P0

| 编号 | 级别 | 域 | 工具 | 一句话结论 | 来源 | 核验 |
|---|---|---|---|---|---|---|
| SEM-01 | P0 | sales/money | `magento.creditmemo.create` | 部分退款被放大成整张发票全额退款：请求退 1 件，贷项按整张发票金额与数量生成 | SALES-01 | REPRODUCED (live) |
| SEM-02 | P0 | security | 所有带 `instance` 的工具 | 默认 RBAC 策略的 grant 不带 instance，`matchesInstance("")` 视为通配，viewer 可跨实例读 | SEC-01 | REPRODUCED (code) |

### P1

| 编号 | 级别 | 域 | 工具 | 一句话结论 | 来源 | 核验 |
|---|---|---|---|---|---|---|
| SEM-03 | P1 | security/audit | `mage2mcp-admin approve` | 审批写审计行用 `decision="approve"`，DB CHECK 只允许 allow/deny/confirmation_required/error/pending，审批审计永远缺失 | SEC-02 | REPRODUCED (code) |
| SEM-04 | P1 | security/audit | 5 个 self-ledgered 资金工具 | 非 gated 分支只看 `err`，`toolerr` 把错误转成 `IsError` 结果并返回 nil error，导致失败被审计成 succeeded/allow | SEC-03 | REPRODUCED (code) |
| SEM-05 | P1 | sales | `magento.order.cancel` | 忽略 Magento 返回的布尔值：`POST /orders/{id}/cancel` 返回 200+`false` 时仍报 `canceled:true`，订单实际未取消 | SALES-02 | REPRODUCED (live) |
| SEM-06 | —（NOT-REPRODUCED） | sales | `magento.order.create` | 报告称地址模型没有 `region_id`；实测部署的 v0.11.5 schema 与当前树都有 `region_id` 并透传，**未能复现**（见 §3 更正） | SALES-03 | NOT-REPRODUCED |
| SEM-07 | P1 | customers | `magento.customer.addresses.delete` | 返回 `{"deleted":true}` 但地址未删除；重复删除仍成功；同一 DELETE 从生产机直连可删 | CUS-04 | REPRODUCED (live) |
| SEM-08 | P1 | customers | `addresses.create` / `addresses.update` | 回退路径丢 `region_id`：传 1003 存成 0/null；create 响应还回显请求的 region 名 | CUS-01, CUS-02 | REPRODUCED (live) |
| SEM-09 | P1 | import/jobs | `magento.import.status` | 部分失败（2 行 1 失败）时 job state=`succeeded`，batch 才是 `partial`；schema 说 job 可为 partial | IMP-01 | REPRODUCED (live) |
| SEM-10 | P1 | import | `magento.import.batch` | 输入描述称 `batch_id` 就是 job id，实际传 job id 返回 not_found；真正 batch id 在 `import.batches[].id` | IMP-02 | REPRODUCED (live) |

### P2

| 编号 | 级别 | 域 | 工具 | 一句话结论 | 来源 | 核验 |
|---|---|---|---|---|---|---|
| SEM-11 | P2 | magento/diagnostics | 所有 Magento 工具 | 400/422 的上游 message/parameters 不返回（`WithUpstream` 只进服务端日志），只剩「verify store_code and field values」，agent 无法自查 | SALES-04, INV-01, CUS-05 注 | REPRODUCED (code) |
| SEM-12 | P2 | magento/errors | 5xx 的读/预检错误 | 5xx 的 `statusError` 同时置 `retryable=true` 与 `outcome=unknown`，信封自相矛盾（账本层会拒绝重试，但信封在教 agent 重试） | INV-03 | REPRODUCED (code) |
| SEM-13 | P3 | inventory | `magento.inventory.status` | `qty` 是 legacy stock item 数量（0），MSI 源项合计 5；同一响应 `is_in_stock=true`。描述已提示「aggregated catalog value / 用 source_items」，剩余问题是措辞与店铺数据不一致 | INV-02 | ENV-DEPENDENT |
| SEM-14 | P2 | sales | `magento.invoice.create` | `capture=false` 在离线支付下仍生成 state=2（已支付）发票，描述称「open invoice」 | SALES-05 | ENV-DEPENDENT（依赖支付方式/店铺工作流） |
| SEM-15 | P2 | sales/analytics | `magento.sales.summary`、`orders.geography` | revenue 含已取消订单（234 含取消的 39，高估约 16.7%）；是否缺陷取决于产品口径 | SALES-06 | SUSPECTED |
| SEM-16 | P2 | sales | `magento.creditmemo.create` | 贷项输出 `total_qty:0`，实际 2 件；Magento 响应无该字段，零值外泄 | SALES-08 | REPRODUCED (live) |
| SEM-17 | P2 | mcp/diagnostics | 所有写工具的错误 `op` | 路由脱敏把动作段也替换：`GET /stockItems/{sku}` → `/{id}/{id}`，`/configurable-products/{sku}/children` → `/{id}/{id}/{id}` | SALES-09, MCP-06 | REPRODUCED (code) |
| SEM-18 | P2 | mcp | 全部工具结果 | `_meta.request_id` 永远缺失：`requestID(ctx)` 的 context key 没有写入方；审计表 request_id 同样恒空 | MCP-02 | REPRODUCED (code) |
| SEM-19 | P2 | mcp/security | 所有 ledger 化写工具 | 重放分支不校验 `confirmation_id`：已成功过的同参数操作，传从未签发的随机 UUID 也返回 replayed 成功 | MCP-03 | REPRODUCED (code) |
| SEM-20 | P2 | mcp/schema | 101 个 Magento 工具 | `instance` 全部 required 且 pattern 拒绝 `""`，与「单实例时由服务器解析」的描述和 ADR-0018 矛盾 | MCP-04 | REPRODUCED (code/static) |
| SEM-21 | P2 | mcp | `platform.info` | `protocol_versions` 把 2026-07-28 列在首位，但带该版本头的请求被同一服务器以 -32022 拒绝 | MCP-01 | REPRODUCED (code) |
| SEM-22 | P2 | mcp/limits | 所有走 Magento client 的工具 | 限流预算耗尽返回 `upstream_timeout`（timeout=30s），不是 `rate_limited`：`timedOut` 分支覆盖了 limiter 的 `rate_limited` | SEC-04 | REPRODUCED (code) |
| SEM-23 | P2 | catalog | `magento.products.bulk_update` | 描述称「整单拒绝」，实际逐 SKU 部分成功；重复 SKU 被静默折叠；`total` 是入参数量 | CAT-01, DOC-05 | REPRODUCED (code) |
| SEM-24 | P2 | docs/schema | 多个输出 schema | `total` 描述说「店铺总数」，实际是本次请求行数/事件总数（bulk/import/cloudflare） | DOC-05 | REPRODUCED (doc) |

### P3

| 编号 | 级别 | 域 | 工具 | 一句话结论 | 来源 | 核验 |
|---|---|---|---|---|---|---|
| SEM-25 | P3 | docs | n/a | 文档仍称「没有限流器/PLANNED」，代码已实现每实例 50rps/burst100/8 并发 | DOC-01 | REPRODUCED (doc) |
| SEM-26 | P3 | docs | n/a | 文档仍称会话未绑定身份/PLANNED，代码已绑定 subject | DOC-02 | REPRODUCED (doc) |
| SEM-27 | P3 | docs | `magento.product.create` | 文档允许 bundle/downloadable，schema 只接受 simple/virtual 并明确拒绝 | DOC-03 | REPRODUCED (doc) |
| SEM-28 | P3 | docs | n/a | integrations/magento.md 称 Sales 只读，代码有订单/发票/发货/退款/取消写工具 | DOC-04 | REPRODUCED (doc) |
| SEM-29 | P3 | docs | n/a | `/metrics` 文档说未认证、`/readyz` 文档说只看进程，代码均不同 | DOC-06, DOC-07 | REPRODUCED (doc) |
| SEM-30 | P3 | docs | n/a | v0.11.2 release note 称每个 required 输入都有 description，实际 8 个缺失 | DOC-08 | REPRODUCED (doc) |
| SEM-31 | P3 | docs | `shipment.get`/`creditmemo.get`/email | 文档路由仍写复数/`/email`，实际单数/`/emails` | DOC-09 | REPRODUCED (doc) |
| SEM-32 | P3 | docs/schema | 幂等写工具 | 描述说 idempotent，MCP annotation 仍 `IdempotentHint:false` | DOC-10 | REPRODUCED (code) |
| SEM-33 | P3 | docs | cloudflare/ga4 读工具 | 超时表写 Read 30s，实际 60s | DOC-11 | REPRODUCED (code) |
| SEM-34 | P3 | docs/schema | `cloudflare.security.events` | `truncated` 描述「还有更多记录」，代码只判断「本页满」 | DOC-12 | REPRODUCED (code) |
| SEM-35 | P3 | docs | n/a | configuration.md 称 ADR-0018 仍是旧状态，ADR 已更新 | DOC-13 | REPRODUCED (doc) |
| SEM-36 | P3 | docs | n/a | 文档标 as-built for v0.11.5，工作树是 v0.11.5-4 且含 tag 后变更 | DOC-14 | SUSPECTED |
| SEM-37 | P3 | import/schema | `import.start`/`import.resume` | `rows` 描述「Per-row results」，实际是整数行数 | IMP-03 | REPRODUCED (live) |
| SEM-38 | P3 | import | `import.cancel` | 对已 canceled 的 job 再 cancel 返回成功；succeeded 的返回 illegal_transition | IMP-04 | REPRODUCED (live) |
| SEM-39 | P3 | customers | `addresses.update` | 响应缺 store 中实际存在、list 能返回的 region/region_id | CUS-03 | REPRODUCED (live) |
| SEM-40 | P3 | sales | 资金类写工具 | `idempotency_key` 永久全局占用、无 TTL；失败行跨 demo 重置仍阻断 | SALES-07 | REPRODUCED (code) |
| SEM-41 | P3 | mcp | 需确认的写工具 | 策略层早退（confirmation_required/未知确认/重放）一律 `_meta=null` | MCP-05 | REPRODUCED (code) |
| SEM-42 | P3 | mcp | 带 maxLength 的工具 | 超长取值（11MB）的 schema 拒绝丢失 `details.validation`（WithDetail 1024 上限静默丢弃） | MCP-07 | REPRODUCED (code) |
| SEM-43 | P3 | mcp | 协议层 | 二次 initialize 返回 JSON-RPC code 0（非法） | MCP-08 | REPRODUCED (code) |
| SEM-44 | P3 | mcp | 协议层 | 未知 method 返回 HTTP 400 纯文本而非 -32601 | MCP-09 | REPRODUCED (code) |
| SEM-45 | P3 | mcp/schema | 分页读工具 | cursor 是 string 但描述写「row offset」，传整数被类型错误拒绝 | MCP-10 | REPRODUCED (code) |
| SEM-46 | P3 | docs | n/a | implemented-tools.md 计数表推不出本部署 102 工具 | MCP-11 | REPRODUCED (doc) |
| SEM-47 | P3 | inventory | `magento.inventory.assign` | `assigned` 是入参计数；不存在的 SKU 也「成功」，拼写错误静默产生孤儿源项 | INV-04 | REPRODUCED (code) |
| SEM-48 | P3 | inventory | 经账本的写工具 | 一次 5xx 把参数组合永久钉在 unknown；修正根因后同参数仍被拒，响应无解除指引 | INV-05 | REPRODUCED (code) |
| SEM-49 | P3 | security | 所有 tools/call 结果 | `_meta.subject` 回显调用者邮箱（无令牌/密码泄漏） | SEC-05 | SUSPECTED |
| SEM-50 | P2 | catalog | `magento.product.get`/search | configurable 父价格 0 没有解释；代码注释与 write 侧语义不一致 | CAT-02 | SUSPECTED |
| SEM-51 | P3 | inventory | `magento.inventory.assign` | 批量「部分失败」无法被工具表达（未观察到真实部分生效） | INV-09 | SUSPECTED |

> 说明：SEM-50 原报告标 P2/SUSPECTED，保留 P2 但按「不作缺陷计」处理；SEM-51 为 SUSPECTED。

## 3. 关键核验记录（本会话实测）

### SEM-01 部分退款放大（P0）— REPRODUCED (live)

- 直连 REST 给 order 7 开发票（items qty 2）：invoice 6，`grand_total=78`、`total_qty=2`。
- 通过 MCP 请求部分退款 1 件：

```text
$ ... -tool=magento.creditmemo.create -args='{"instance":"demo","order_id":7,"invoice_id":6,"items":[{"order_item_id":9,"qty":1}]}'
is_error=false
{"creditmemo":{"entity_id":5,"order_id":7,"grand_total":78,"subtotal":68,"shipping_amount":10,"total_qty":0,...},
 "invoice_id":6,"operation_id":"9b0508ca-..."}
```

- 直连 `GET /V1/creditmemos` 显示 creditmemo 5 = **78**（整张发票），而不是请求的 1 件（应约 44）。
   根因与报告一致：`order_documents_write.go` 把报文包成 `{"entity":{...}}` 发往 `/order/{id}/refund`，
   该路由的 `items` 在顶层，`entity` 被忽略 → Magento 收到空 items → 默认全额退款。

### SEM-06 更正：未能复现（NOT-REPRODUCED）

报告 SALES-03 称 `magento.order.create` 的地址模型缺少 `region_id`。核对结果：

- 部署的 v0.11.5 `tools/list` 中 `magento.order.create.inputSchema` 的 billing/shipping address
  **包含** `region_id`（`"region_id": {"type":"integer","minimum":1,...}`）；
- 当前树的 `orderAddressWire`（`internal/magento/write_tools.go:1658`）与 `OrderAddressInput`
  （`internal/magento/order_create.go:53`）都有 `RegionID`，`toInput`/`toPayload` 直接转换；
- 下单路由是 `PUT /carts/{id}/order`（`order_create.go:170`），不是报告中的 POST。

因此「地址模型没有 region_id」这一结论不成立，标 **NOT-REPRODUCED**，不计入缺陷。报告观察到的
400 更可能是其构造的地址未带 `region_id` 或购物车路由差异，需要重新用带 `region_id` 的请求验证。

### SEM-05 取消假成功（P1）— REPRODUCED (live)

```text
$ ... -tool=magento.order.cancel -args='{"instance":"demo","order_id":1}'
is_error=false
{"order_id":1,"canceled":true,"operation_id":"7afc2c0f-..."}

$ curl .../rest/V1/orders/1     # 之后
state = processing               # 订单未取消
$ curl -X POST .../rest/V1/orders/1/cancel
false                            # Magento 用 body 布尔值表示未取消
```

`order_status_write.go` 的 `_, postErr := s.client.do(...)` 丢弃了 body，200 即报成功。

### SEM-07 地址删除假成功（P1）— REPRODUCED (live，t56)

`addresses.delete` 返回 `{"address_id":7,"deleted":true}`，随后 `GET /customers/2` 仍有 id 7；
从生产机直连 `DELETE /rest/pl/V1/addresses/7` 返回 true 并真正删除；不存在的 id 才返回
`unsupported_capability`(404)。根因未最终定位（网关代码发送的路径与直连一致），但行为已复现三次。

### SEM-08 地址 region_id 丢失（P1）— REPRODUCED (live + code)

传 `region_id:1003` 创建 → 响应无 region_id，直连读回 `region_id:0, region:null`；传 region 字符串
"mazowieckie" → 响应回显 `"region":"mazowieckie"`，实际存 null。代码 `createAddressViaCustomerUpdate`
只写 `"region"`，不写 `"region_id"`；`UpdateCustomerAddress` 的「至少一个字段」判断不含 `RegionID`，
payload 也从不赋值。

### SEM-09 / SEM-10 导入 job 状态与 batch_id（P1）— REPRODUCED (live，t56)

2 行导入 1 失败：`import.status` job state=`succeeded`，result 里 failed:1；`import.batches` 中同 job
的 batch state=`partial`。`import.batch` 传 job id → `not_found`；传 `batches[].id` 成功并带完整
failures（row_number/sku/action/error）。

### SEM-02 / SEM-03 / SEM-04 / SEM-19 / SEM-22（安全与协议）— REPRODUCED (code)

- `rbac.go:92-96` DefaultPolicy 无 Instance；`matchesInstance` 对 `""` 返回 true → 通配。
- `cmd/admin/main.go:174` 写 `Decision:"approve"`；migration 000007 的 CHECK 不含 approve → 审批审计插入失败。
- `toolerr.Add` 在 `res.IsError` 时返回 `(res, nil)`；`server.go` 非 gated 分支只用 `err` 判定 state → 失败被审计成 succeeded。
- `prepareConfirmation` 对非空 `confirmation_id` 直接返回、不校验；replay 分支在 consume 之前返回 → 随机 UUID 也能重放。
- `server.go:325-330` 的 `timedOut` 分支把 `err` 覆盖成 `upstream_timeout`，丢弃 limiter 的 `rate_limited`。

### 其余条目

- SEM-11/12/13/16/17/18/20/21/23/24/32-35/40-48：读当前树代码/文档逐条核对（见各报告的证据与
  本汇总的「来源」列）；均能定位到具体文件与行。
- SEM-14/15/36/49/50/51：SUSPECTED 或 ENV-DEPENDENT，未计入已确认缺陷。

## 4. 修复分组（可直接派发）

### 组 A：`internal/magento` 资金与订单（最高优先，独立文件可并行）

| 子组 | 文件 | 修复项 | 依赖 |
|---|---|---|---|
| A1 退款 | `internal/magento/order_documents_write.go` | SEM-01：`/order/{id}/refund` 报文改为顶层 `items`（不包 `entity`）；补一条部分退款回归测试（对比金额/数量） | 无 |
| A2 取消 | `internal/magento/order_status_write.go` | SEM-05：解析 `POST /orders/{id}/cancel` 的布尔 body；false → 读回确认，未取消则 Fail/Unknown，不写 succeeded | 无 |
| A3 订单地址 | `internal/magento/order_create.go`、地址 wire 类型 | SEM-06 **未复现**，不需要改；仅保留「place_order 失败时透出上游原因」（并入 A6） | A6 |
| A4 客户地址 | `internal/magento/customer.go` | SEM-07/08/39：回退写 region_id；update 接受并写 region_id；delete 校验真实删除（读回）或改用可用的删除路径；update 响应回读 region/region_id | 无 |
| A5 发票/贷项 | `internal/magento/invoice_write.go`、`order_documents_write.go` | SEM-14（capture=false 语义/描述）、SEM-16（total_qty 从明细回读，不用零值） | 无 |
| A6 错误诊断 | `internal/magento/client.go` | SEM-11（把上游 message 有界地放进 details，或至少给可操作的 reason）、SEM-12（unknown 时 retryable=false） | 影响 A3/A4 的错误文本 |
| A7 库存 | `internal/magento/inventory.go` | SEM-13（qty 语义：改用 MSI 源项合计或改名/加说明）、SEM-47（assigned 用实际写入数；SKU 存在性校验） | 无 |
| A8 批量 | `internal/magento/product_bulk.go` | SEM-23：描述与 `total` 语义对齐（逐 SKU 结果、total=请求数） | 无 |

### 组 B：`internal/mcp/server`（同一文件，建议单 owner 串行）

| 文件 | 修复项 |
|---|---|
| `internal/mcp/server/server.go` | SEM-04（非 gated 分支同时看 `result.IsError`）、SEM-18（写入 request_id 到 context 并落审计）、SEM-19（replay 前校验 confirmation_id/过期）、SEM-22（timedOut 不覆盖 rate_limited）、SEM-17（routeWords 补 invoice/ship/refund 等动作词）、SEM-41（早退也 attach `_meta`）、SEM-42（validation 细节不因 1024 上限丢弃） |
| `internal/mcp/server/session.go` | SEM-43（二次 initialize 用合法错误码）、SEM-44（未知 method 返回 -32601 JSON-RPC） |
| `internal/mcp/server/server.go`（platform.info） | SEM-21（protocol_versions 用传输层实际支持集） |

### 组 C：`internal/security` + `cmd/admin`（独立，可与 A/B 并行）

| 文件 | 修复项 |
|---|---|
| `internal/security/rbac/rbac.go` | SEM-02：DefaultPolicy 的 grant 显式绑定 instance，或把「空=通配」改为「空=仅默认实例」并文档化 |
| `cmd/admin/main.go` | SEM-03：审批审计 decision 改为 CHECK 允许的值（如 `allow`）并加 `phase=approval`；补一条 DB 集成测试 |
| `internal/storage/migrations/` | 如需新增 decision 值，加迁移（不要改历史迁移） |

### 组 D：`docs`（纯文档，可完全并行）

| 文件 | 修复项 |
|---|---|
| `docs/architecture.md`、`docs/operations/deployment.md`、`docs/integrations/magento.md` | SEM-25（限流）、SEM-26（会话绑定）、SEM-27（bundle/downloadable）、SEM-28（Sales 只读） |
| `docs/integrations/magento.md` | SEM-31（shipment/creditmemo 路由单复数） |
| `docs/configuration.md`、`docs/adr/0018-*.md` | SEM-35（ADR 状态） |
| `docs/implemented-tools.md` | SEM-46（计数表补 102 行） |
| `docs/audit/*` / release notes | SEM-30（required description 声明）、SEM-36（as-built 版本） |
| 工具描述（`internal/magento/write_tools.go` 等 schema 字符串） | SEM-10（batch_id）、SEM-20（instance 描述/required）、SEM-24（total）、SEM-29（metrics/readyz 文档）、SEM-32（IdempotentHint）、SEM-33（超时表）、SEM-34（truncated）、SEM-37（rows）、SEM-45（cursor） |

### 组 E：`internal/jobs` + `cmd/worker`

| 文件 | 修复项 |
|---|---|
| `cmd/worker/main.go` / `internal/jobs/jobs.go` | SEM-09：导入结果有失败行时 job state 置 `partial`（与 batch 一致），或明确文档化「job 只看执行、batch 看数据」 |

### 依赖关系

- A6 的错误透出会影响 A3/A4 的失败文本与测试断言，建议 A6 先行或同 PR。
- B 组全部在 `server.go`，必须单 owner；其中 SEM-19 依赖 storage 的 confirm 接口（只读调用，不改 schema）。
- C 组独立；SEM-02 若产品决定「所有实例同一信任域」，则改为文档化 + 限制默认策略，不算代码缺陷。
- D 组与代码无编译依赖，可随时并行。

## 5. 非产品缺陷清单（不计入修复）

| 编号 | 事项 | 归属 | 理由 |
|---|---|---|---|
| ENV-01 | demo 店铺周期性重置，订单/自建数据消失 | 环境 | 报告中的金额均为当时真实输出；不是网关缺陷 |
| ENV-02 | 该店不暴露 `POST/PUT /customers/{id}/addresses` | 店铺限制 | 已知限制；本次考核的是回退路径行为（SEM-08 是回退缺陷） |
| ENV-03 | `POST /carts/{id}/order` 路由不存在（order.create 失败的另一半） | 店铺限制 | SEM-06 的网关侧缺陷（region_id）仍成立 |
| ENV-04 | `DELETE /inventory/sources/{code}` 路由不存在 | 店铺限制 | source_create 单向；留给 demo 重置 |
| ENV-05 | 改 customer email 返回 400 "Something went wrong while logging out customer." | 店铺缺陷 | 网关正确转述；附带的诊断缺失已并入 SEM-11 |
| ENV-06 | 生产 18110 `auth.enabled=false` | 部署配置 | 不是代码缺陷；安全测试用本地 auth 实例完成 |
| ENV-07 | 隧道偶发 `initialize ... EOF` | 测试环境 | 所有结论均在重试成功的调用上取得 |
| ENV-08 | MSI legacy `qty` 与源项不一致本身是 Magento 数据模型 | 店铺数据 | 网关问题是字段名/语义未声明（SEM-13） |
| ENV-09 | `invoice.create capture=false` 在离线支付下仍 paid | 店铺工作流 | 网关问题在描述与 `captured` 语义（SEM-14） |
| ENV-10 | 多行部分成功/并发 lost update 未构造 | 测试覆盖 | 报告已诚实声明未覆盖，不算发现 |
| ENV-11 | 邮件工具未调用、在线支付 capture 无法验证 | 测试覆盖 | 同上 |
| ENV-12 | 限流测试用本地 fake Magento | 测试方法 | 限流逻辑本身已验证（SEM-22 是错误分类问题） |

## 6. 不建议修复（或不应按缺陷修）

| 编号 | 理由 |
|---|---|
| SEM-15（revenue 含取消） | 需产品口径决策；改口径会影响历史报表。建议先在文档写清「revenue 含已取消订单」，不要擅自改 SQL |
| SEM-40（idempotency_key 无 TTL） | 设计取舍：永久占用保证「同一 key 绝不二次扣款」；清理会引入重放风险。建议文档说明并提供人工清理 SQL |
| SEM-49（_meta.subject 回显邮箱） | 审计关联需要；无令牌泄漏。建议在威胁模型记录为 accepted |
| SEM-50（父价格 0） | Magento 语义（configurable 父价格不参与定价）；补文档/描述即可，不必改行为 |
| SEM-51（批量部分失败不可表达） | 未观察到真实部分生效；构造代价高，收益低 |
| SEM-36（as-built 版本） | 纯书签问题；发版时统一改版本标记即可 |

## 7. 结论

1. **发布阻断项只有 1 条：SEM-01（P0，部分退款放大为全额退款）**。已在当前部署用新发票/新贷项
   现场复现（invoice 6 → creditmemo 5 = 78，请求只退 1 件）。修复前不应把资金链路称为可用。
2. **SEM-02（P0，跨实例读）**取决于部署是否把多实例视为同一信任域；默认策略当前是通配。若产品
   不接受，必须在发布前改为实例绑定。
3. **7 条 P1** 都是「声明与行为不符导致误操作」：取消假成功（SEM-05）、地址删除假成功
   （SEM-07）、地址丢 region（SEM-08）、导入 job 状态（SEM-09）、batch_id 描述（SEM-10）、
   审批审计缺失（SEM-03）、自记账审计错误（SEM-04）。建议与 SEM-01 同批修复。
4. 12 条 P2 以诚实性/诊断为主；24 条 P3 是文档与措辞，可并行清理。
5. 7 份报告合计覆盖 80+ 工具、45 条 CONFIRMED；没有「CONFIRMED=0」的情况。所有 CONFIRMED 均有
   复现状态（live/code/doc）；SUSPECTED 5 条、NOT-REPRODUCED 1 条未计入缺陷。

---

## 附录：队长核验对汇总的两处更正

汇总完成后，队长按同一标准（网关调用 + 直连回读）复核了两条，结论与汇总不同。

### 更正 1：漏了一个 P0——发货也被放大成整单

汇总把 SEM-01 列为**唯一**发布阻断项，但同一根因还影响 `magento.shipment.create`，汇总未收录。

店铺自身 schema：

```text
POST /V1/order/{orderId}/ship     body props: items, notify, appendComment, comment, tracks, packages, arguments
POST /V1/order/{orderId}/refund   body props: items, notify, appendComment, comment, arguments
```

两个路由的参数都在**顶层**。网关：

```text
internal/magento/order_documents_write.go:348  shipmentCreateBody{Entity ...}   `json:"entity"`  ← 错误
internal/magento/order_documents_write.go:366  creditmemoCreateBody{Entity ...} `json:"entity"`  ← 错误
internal/magento/invoice_write.go:155          invoiceOrderBody{Capture, Items}                  ← 正确
```

实测（生产 v0.11.5，订单 7，2 件）：

```text
请求: shipment.create items=[{order_item_id:9, qty:1}]
返回: total_qty=2
直连核对: shipment total_qty=2, line qty=2
```

**请求发 1 件，实际发 2 件。** 客户为未发出的商品拿到物流信息，剩余数量无法再发。

**SEM-01 应扩展为「退款与发货两个工具、同一根因」，两者都是发布阻断项。** 修复范围是 `order_documents_write.go` 的两个 body 结构，不是只改退款。

### 更正 2：SEM-07 降级为 P3

汇总保留 SEM-07 为 P1「返回 deleted:true 但地址未删除」。队长复现：

1. 网关 `customer.addresses.create` 建地址 → id 8
2. 网关 `customer.addresses.delete` 删 id 8 → `{"deleted":true}`
3. **直连回读** `GET /customers/1` → 剩余 `[1]`，**地址 8 已不存在**

**删除是真实生效的，P1 的核心指控不成立。**

成立的是另一半：再次删除已不存在的 id 8 → 仍返回 `{"deleted":true}`。真正的缺陷是**删除不存在的地址也报成功**，调用方无法区分「删掉了」与「本来就没有」。**降级为 P3 诚实性问题。**

### 对修复计划的影响

| 项 | 汇总原判定 | 更正后 |
|---|---|---|
| 发布阻断项 | 1 个（SEM-01 退款） | **2 个**（SEM-01 退款 + 发货，同一根因同一文件） |
| SEM-07 | P1 数据未删 | **P3 删除不存在的地址也报成功** |

修复任务 A1 的范围相应扩大：`order_documents_write.go` 的 **shipment 与 creditmemo 两个 body 结构**都改为顶层字段，并各补一条「订单 2 件、只退/只发 1 件」的回归测试。
