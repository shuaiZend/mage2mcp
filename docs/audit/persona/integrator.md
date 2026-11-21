# 岗位场景测试：系统集成工程师（ERP / WMS 对接）

被测对象：生产部署 `49.51.250.83`，**v0.11.6**（`platform.info` 实测返回 `v0.11.6`，
`writes_enabled: true`），店铺 `demo` / store code `pl`（PLN）。
网关端点：`http://127.0.0.1:18110/mcp`（SSH 隧道到生产 8080）。
测试时间：2026-11-22 03:20–03:40 (+08:00)。

本岗位关注点（来自团队任务）：

1. 同一 `idempotency_key` 重复下单 / 开票 / 退款，是否不重复写入且返回一致；
2. 批量操作（多 SKU 改价、批量属性选项）的部分失败是否**逐行**报告；
3. 故意发错参数（缺必填、类型错、超长）时错误码是否**可编程区分**；
4. 并发写请求是否出现竞态或重复；
5. `operation_unknown` 场景下调用方能否安全处理（不盲目重试）。

只记录，不修改代码。测试期间改动的店铺数据已还原（见文末「测试数据与清理」）。

---

## 复现方法

每次写操作都要「先拿 `confirmation_id` → 人工批准 → 带 id 重发」。下面所有命令都用同一个
最小驱动（等价于仓库 `test/live` 的 `-mode=raw`，但会自动走审批环）：

```bash
# 保存为 /tmp/mcpx.py，用法：python3 /tmp/mcpx.py call <tool> '<json>' [--no-confirm]
# 初始化 session、tools/call、遇 confirmation_required 时调用
# ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}' 后自动重发
```

等价的手工 curl（不含自动审批）：

```bash
curl -sS -D /tmp/h -X POST http://127.0.0.1:18110/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"p","version":"1"}}}'
# 取 mcp-session-id 后：
curl -sS -X POST http://127.0.0.1:18110/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -H "Mcp-Session-Id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"magento.order.create","arguments":{...}}}'
ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh <confirmation_id>'
```

下文 `命令` 一律给出工具名 + 实际 arguments；`输出` 是网关返回的 `structuredContent`（裁剪）。
订单/发票/退款/取消这类写操作的编号是本次测试的真实 id（订单 23/24/30/33/…），
demo 站会周期性重置，重跑时编号会不同。

---

## 结论摘要

| 编号 | 结论 | 严重级别 |
|---|---|---|
| INT-01 | `products.bulk_update` 的 `price`/`name` 被静默丢弃，却返回 `status: "updated"` | **P1** |
| INT-02 | 非 money 写工具的幂等重放**在 structuredContent 里不可见**；派生 key 让「同参数再写一次」永久变成 no-op，且仍报成功 | **P1** |
| INT-03 | `invoice.create` 在 `capture=false` 时返回 `captured: true`（离线支付方式下 state 恒为 2），把「已开票」当「已收款」 | **P1** |
| INT-04 | 幂等重放仍要求**新的人工审批**，所以「丢响应后自动重试」这条路走不通 | P2 |
| INT-05 | 预检（订单已取消 / 已全额开票 / 最小起订量）也排在审批之后，无人值守时无法先校验 | P2 |
| INT-06 | 并发失败者收到 `confirmation_used`，**没有 operation_id、没有提示**，无法定位已成功的那次操作 | P2 |
| INT-07 | `operation_unknown` 提示「poll」，但**没有任何按 operation_id 轮询的工具**；`order.create` 的 unknown 在构造上不可判定，reconciler 8 次后升级人工、`reconcile_after=infinity`，调用方永久卡住 | **P1**（对账不可闭环） |
| INT-08 | `attributes.options.bulk_create`：`total=3` 但 `rows` 只有 2 行（重复标签被静默去掉），`created+failed≠total`；重试时「已存在」报 `failed` 而非 skipped，且与真失败共用同一通用错误 | P2 |
| INT-09 | 并发写同一产品 → Magento 400「linked products data is invalid」→ 映射成 `validation_failed`（不可重试码），实际应重试 | P2 |
| INT-10 | 批量导入必须每行带 `name`，纯改价 CSV 被整单拒绝；`start` 只报第一行错误；`validate` 不认识 `update_existing` | P2 |
| INT-11 | 不存在的 SKU 在导入里报「attribute_set_id is required to create a product」；状态冲突报 `validation_failed` + 波兰语原文，没有稳定 reason code | P2 |
| INT-12 | 不带 `idempotency_key` 时，**相同参数的合法重复下单被永久去重**（`replayed: true`），ERP 省略 key 会静默丢单 | P2 |
| INT-13 | schema 自相矛盾：`instance` 声称可自动解析但 `required`；`bulk_update` 顶层说「部分成功逐行报告」而 `skus` 说「整单拒绝」（实测是整单拒绝） | P3 |
| INT-14 | `import.start` 重放已完成任务仍返回 `queued: true`，无 `replayed` | P3 |
| INT-15 | `import.status` 的 `job.result` 是 JSON **字符串**，需要二次解析 | P3 |
| INT-16 | 超长 `idempotency_key` 被完整回显在 validation 错误里（文档要求把它当 secret） | P3 |
| INT-17 | `order.create` 的 ledger `entity_type=order` 但 `entity_id` 存的是**客户 id**，运维对账会误读 | P3 |
| INT-18 | `order.search` 不返回明细行，无法区分同一客户的多笔订单，「读回对账」路径不够用 | P3 |
| INT-19 | 错误信封只在 `retryable=true` 时才带该字段，与 ADR-0010「每条错误都携带 Retryable」不一致 | P3 |
| INT-20 | 调用**不存在的工具名**返回 `code: "internal"`（"an unexpected error occurred"），与服务器内部故障无法区分 | P2 |
| INT-21 | 传输层错误（JSON 解析失败、未知 method）返回**纯文本** 400，不是 JSON-RPC 错误对象 | P3 |

**可以放心用的部分**（同样重要，见文末「验证通过的行为」）：错误码本身稳定可编程
（`invalid_argument` / `not_found` / `conflict` / `idempotency_key_reuse` /
`confirmation_used` / `operation_unknown` / `unsupported_capability`），money 工具的
`replayed: true` 可见，5 路并发同 key 下单只落 1 单，导入逐行失败 + `partial: true` 诚实。

---

## INT-01: bulk_update 静默丢弃 price / name，却报告 updated

- **严重级别**: P1
- **工具**: `magento.products.bulk_update`
- **现象**: 把价格放进 `attributes` 发出去，返回 `updated: 1`、`rows[0].status = "updated"`，
  但商品价格没有任何变化。`name` 同样被丢弃。
- **期望**: 工具描述说「Name and price are not part of the bulk update」——不支持的字段应当
  **报错拒绝**（例如 `invalid_argument` 列出不支持的字段），而不是接受后静默丢弃还报成功。
- **证据**:

```bash
# 改价前
curl -sS -g -H "Authorization: Bearer $TOK" \
  'https://demo-magento-2.auroracreation.com/rest/pl/V1/products/24-MB04?fields=sku,price'
# {"sku":"24-MB04","price":32}

magento.products.bulk_update
{"instance":"demo","skus":["24-MB04"],"attributes":{"price":"12.34"}}
```

```json
{
  "result": {
    "failed": 0,
    "rows": [ { "sku": "24-MB04", "status": "updated" } ],
    "total": 1,
    "updated": 1
  }
}
```

```bash
# 改价后
curl -sS -g -H "Authorization: Bearer $TOK" \
  'https://demo-magento-2.auroracreation.com/rest/pl/V1/products/24-MB04?fields=sku,price'
# {"sku":"24-MB04","price":32}      ← 没变
```

`name` 同样复现（`24-MB05`，`attributes: {"name":"INTG Name Probe"}` → `updated: 1`，
读回仍是 `"Wayfarer Messenger Bag"`）。

- **影响**: ERP 的价格同步 / 商品改名如果走 bulk 接口，会拿到「成功」并写入本地同步水位，
  店铺端却完全没有变化——静默数据漂移，且没有任何可观测信号。
- **状态**: CONFIRMED（代码路径：`product_bulk.go` 把 `attributes` 原样交给
  `customAttributes()` 拼成 `custom_attributes`，Magento 对 `price`/`name` 走自定义属性通道
  静默忽略；网关不做回读校验）

---

## INT-02: 幂等重放在 structuredContent 里不可见，派生 key 让同参数写入永久 no-op

- **严重级别**: P1
- **工具**: 所有非 money 写工具（示例 `magento.products.bulk_update`、`magento.attributes.options.bulk_create`）
- **现象**: 同一写请求（参数完全相同）第二次调用时，中间件用**派生的幂等键**判定为 replay，
  直接返回**上一次记录的 result**，但：
  1. `structuredContent` 里**没有** `replayed` 字段（money 工具的结果结构体有该字段，这些工具没有）；
  2. replay 标记只出现在 `content[0].text`（`"replayed: this operation already succeeded (...)"`），
     只解析 `structuredContent` 的客户端完全看不到；
  3. 因为 key 由参数派生，**同样参数的写请求永远变成 no-op**，包括「外部改动之后我想再写一次」。
- **期望**: 至少与 money 工具一致，在 structuredContent 里给 `replayed: true`；或者提供显式的
  「强制重新执行」手段（新 key）。
- **证据 A（标记只在不被解析的 text 里）**:

```bash
magento.products.bulk_update
{"instance":"demo","skus":["24-MB05"],"attributes":{"name":"INTG Name Probe"}}
```

```json
{
  "_meta": {"instance":"demo","request_id":"9b1e732b…","tool":"magento.products.bulk_update"},
  "content": [{"type":"text","text":"replayed: this operation already succeeded (operation 1904639c-…)"}],
  "structuredContent": {
    "result": {"failed":0,"rows":[{"sku":"24-MB05","status":"updated"}],"total":1,"updated":1}
  }
}
```

- **证据 B（replay 让一次真实的写请求变成 no-op，且报成功）**:

```bash
# 1) 先成功执行一次
magento.products.bulk_update {"instance":"demo","skus":["24-MB06"],"status":1}
# → {"failed":0,"rows":[{"sku":"24-MB06","status":"updated"}],"total":1,"updated":1}

# 2) 带外把商品禁用（模拟别人改了它）
curl -sS -g -X PUT -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  'https://demo-magento-2.auroracreation.com/rest/pl/V1/products/24-MB06' \
  -d '{"product":{"sku":"24-MB06","status":2}}'     # http=200, 读回 status=2

# 3) 原样再发一次同样的 bulk_update
magento.products.bulk_update {"instance":"demo","skus":["24-MB06"],"status":1}
# → {"failed":0,"rows":[{"sku":"24-MB06","status":"updated"}],"total":1,"updated":1}

# 4) 读回
curl -sS -g -H "Authorization: Bearer $TOK" '…/products/24-MB06?fields=sku,status'
# {"sku":"24-MB06","status":2}     ← 仍然是禁用，但工具说 updated
```

- **影响**: ERP 的重试、补偿、以及「修好下游问题后再推一次」全部会被静默吞掉；更糟的是
  调用方拿到的成功响应与真正执行成功**完全一样**（结构化输出里没有任何区别），对账无法发现。
- **状态**: CONFIRMED（`server.go` `DecisionReplay` → `replayResult()` 把记录的
  structuredContent 原样返回；`BulkUpdateResult` / `OptionBulkResult` 无 `Replayed` 字段）

---

## INT-03: invoice.create 的 captured 把「已开票」当「已收款」

- **严重级别**: P1
- **工具**: `magento.invoice.create`
- **现象**: `capture: false`（明确要求**不收款**）时，返回 `captured: true`，发票 `state: 2`。
  原因是 `captured` 由 `invoice.State == 2` 推断，而离线支付方式（本店 `checkmo`）下
  Magento 无论是否 capture 都把发票置为 paid。
- **期望**: 工具描述写的是「`capture=false` records an open invoice and charges nothing;
  `capture=true` captures the payment and the returned invoice state (2 = paid) proves
  whether it did」。既然离线支付下 state 无法证明是否 capture，就不该把 state==2 当成 captured。
- **证据**:

```bash
magento.invoice.create
{"instance":"demo","order_id":23,"items":[{"order_item_id":26,"qty":1}],
 "capture":false,"idempotency_key":"intg-inv-20261122-032618"}
```

```json
{
  "captured": true,
  "invoice": {"entity_id":22,"increment_id":"000000022","order_id":23,"state":2,"grand_total":39},
  "operation_id": "30524923-5984-4c73-8452-af19f0d8a4f1"
}
```

```bash
curl -sS -g -H "Authorization: Bearer $TOK" '…/orders/23?fields=payment[method],total_paid'
# {"payment":{"method":"checkmo"},"total_paid":39}
```

第二次用订单 33 复现：`capture:false` → `"captured": true`，`state: 2`。

- **影响**: ERP 同步「已收款金额 / 已捕获」时会把未收款的线下订单记为已收款，账实不符；
  对账差异只能靠人工发现。注意本店无真实资金流动，是**状态语义**错误而非实际扣款。
- **状态**: CONFIRMED（`invoice_write.go`: `Captured: invoice.State == invoiceStatePaid`）

---

## INT-04: 幂等重放仍要求新的人工审批

- **严重级别**: P2
- **工具**: 所有写工具（示例 `magento.order.create`）
- **现象**: 用同一 `idempotency_key`、同一参数重发（这正是幂等存在的场景：上一次响应丢了），
  网关**先**要 `confirmation_id`，返回 `confirmation_required`，批准后才走到 replay 分支。
- **期望**: replay 不产生任何变更，应当直接返回记录结果（或允许复用审批），否则「自动重试」
  这条幂等的主要用途在生产上不可用。
- **证据**:

```bash
magento.order.create            # 与 A1 完全相同的 arguments（同一 idempotency_key），不带 confirmation_id
{"instance":"demo","customer_id":3,"items":[{"sku":"24-MB01","qty":1}],
 "billing_address":{…},"shipping_address":{…},
 "shipping_carrier_code":"flatrate","shipping_method_code":"flatrate","payment_method":"checkmo",
 "idempotency_key":"intg-order-20261122-032618"}
```

```json
{
  "code": "confirmation_required",
  "details": {"confirmation_id":"26d1c7f5-a0d9-4ef3-abe6-e310f43adffa","expires_at":"2026-11-21T19:31:37Z"},
  "message": "this operation requires approval by a different human"
}
```

批准后重发 → `{"replayed": true, "order": {"entity_id": 23, …}}`（同一订单，无重复写入）。

- **影响**: 对 ERP 来说「网络超时后自动重试」会卡在人工审批上；集成只能退化成「失败 → 人工」，
  幂等键带来的自动化收益被审批闸门抵消。
- **状态**: CONFIRMED（`server.go` 先 `prepareConfirmation`，再 `BeginOperation`；
  `DecisionReplay` 分支只 `Validate` 不 `Consume`）

---

## INT-05: 预检结果也排在审批之后

- **严重级别**: P2
- **工具**: `magento.order.cancel`、`magento.invoice.create`、`magento.order.create`
- **现象**: 纯预检（不改任何数据）也要先审批：对已取消订单再取消、对已全额开票订单再开票、
  发一个必然被拒的 0.5 数量，都先返回 `confirmation_required`；批准后才得到真实原因。
- **期望**: 能确定不会产生写入的预检（状态冲突、业务规则）应在审批前返回，让集成方可以
  无人值守地做「能不能做」的判断。
- **证据**:

```bash
# 1) 订单 24 已经是 canceled
magento.order.cancel {"instance":"demo","order_id":24}          # 不带 confirmation_id
# → {"code":"confirmation_required", "details":{"confirmation_id":"72fbb24b-…"}}

# 批准后
magento.order.cancel {"instance":"demo","order_id":24,"confirmation_id":"72fbb24b-…"}
# → {"code":"conflict","details":{"order_state":"canceled","order_id":"24","operation_id":"d2e757f1-…"},
#    "message":"the order is already canceled; no cancel request was sent"}

# 2) 订单 23 已全额开票
magento.invoice.create {"instance":"demo","order_id":23,"items":[{"order_item_id":26,"qty":1}]}
# → confirmation_required；批准后：
# → {"code":"validation_failed","details":{"upstream_status":"400",
#    "upstream_message":"Invoice Document Validation Error(s):The invoice can't be created without products. Add products and try again.Nie można utworzyć faktury, gdy zamówienie ma status closed"}}
```

- **影响**: 集成方的「先探测能否执行」逻辑必须消耗一次人工审批，批量任务无法预判。
- **状态**: CONFIRMED

---

## INT-06: 并发失败者收到 confirmation_used，但拿不到任何可定位信息

- **严重级别**: P2
- **工具**: 所有写工具
- **现象**: 5 个并发请求共用一个 `confirmation_id` 时，1 个成功、4 个返回
  `confirmation_used`；该错误**没有 operation_id、没有 details、没有「可能已成功」的提示**。
- **期望**: 至少带上 `operation_id` 或提示「用同一 idempotency_key 重发以取回结果」，
  否则调用方无法区分「别人抢先执行成功」与「自己的审批被误用」。
- **证据**（5 并发、同一 key、同一 confirmation_id）:

```text
--- call 1 isError=None code=   → {"operation_id":"dbbbc063-…","order":{"entity_id":30,…}}
--- call 0 isError=True code=confirmation_used  {"code":"confirmation_used","message":"confirmation has already been used"}
--- call 2 isError=True code=confirmation_used  {"code":"confirmation_used","message":"confirmation has already been used"}
--- call 3 isError=True code=confirmation_used  {"code":"confirmation_used","message":"confirmation has already been used"}
--- call 4 isError=True code=confirmation_used  {"code":"confirmation_used","message":"confirmation has already been used"}
```

对照：另一组「每个请求各自审批」的并发（5 个不同 confirmation_id、同一 key）返回的是**可定位**的
`conflict`：

```json
{"code":"conflict","details":{"operation_id":"9b02fcea-dde6-4957-9921-863fbc544a37","state":"executing"},
 "message":"the operation is already in progress; poll it instead of retrying"}
```

- **影响**: 同一批并发重试里，失败者拿不到线索；只有 `conflict` 那一类可对账，
  `confirmation_used` 那一类只能靠调用方自己再发一次同样的 key 去「碰」replay。
- **状态**: CONFIRMED

---

## INT-07: operation_unknown 没有轮询手段，order.create 的 unknown 永远无法闭环

- **严重级别**: P1（对账不可闭环）
- **工具**: `magento.order.create` / `magento.invoice.create` / `magento.creditmemo.create` 等
- **现象**:
  1. 错误信息与文档都说「poll, never retry」，但 `tools/list` 里**没有任何按 `operation_id`
     查询状态的工具**（102 个工具中无 `operation.*`；`operation_id` 只出现在**输出**里，
     以及 `configurable.create` 的「resume」入参上）。
  2. `order.create` 的 unknown 在实现上是**构造上不可判定**的（ledger 记的是 customer id，
     不是 order id，读回会读错单），所以 reconciler 只会反复 inconclusive，
     8 次后升级人工并把 `reconcile_after` 设为 `infinity`。
  3. 结果是调用方永久停在 `operation_unknown`：既不能重试（被拒），也没有查询接口。
- **期望**: 提供一个「按 operation_id 查状态」的只读工具（对账/运维都要），或者让
  `order.create` 的 unknown 可被 reconciler 收敛（例如按 customer + 时间窗 + 明细读回）。
- **证据 A（错误本身是可编程的、且确实没发上游请求）**:

```bash
magento.order.create      # 参数对应的 ledger 行处于 unknown（见证据 C 的构造方式）
{"instance":"demo","customer_id":3,"items":[{"sku":"24-MB07","qty":1}], …,
 "idempotency_key":"intg-unknown-20261122-033216"}
```

```json
{
  "code": "operation_unknown",
  "details": {"operation_id":"fe2c1a0f-a633-4c48-af05-df71a5b42338","state":"unknown"},
  "message": "operation outcome is unknown; poll for reconciliation and do not retry"
}
```

同 key 再发一次：**完全相同的** `operation_unknown`（被拒，不重试、不重复写）。
期间 Magento 侧没有产生任何 customer 3 + 24-MB07 的订单（已验证）。
注意：即使这次调用什么都没做，它仍然先要了一次人工审批（同 INT-04）。

- **证据 B（102 个工具里没有轮询入口）**:

```bash
magento.instance.list   # 不存在；platform.info 的 tools[] 全量 102 项
# 其中与 operation 相关：无。只有 magento.import.status（针对 import job_id），
# 不接受 money 工具的 operation_id。
```

- **证据 C（真实卡死样本：生产库里 11 条 order.create 永久 unknown）**:

```sql
select operation, state, error_code, reconcile_count, reconcile_after
from operations where state='unknown' order by created_at desc limit 3;
```

```text
 magento.order.create | unknown | needs_manual_reconciliation | 9 | infinity
 magento.order.create | unknown | needs_manual_reconciliation | 9 | infinity
 magento.order.create | unknown | needs_manual_reconciliation | 9 | infinity
```

```sql
select from_state,to_state,reason,attempt from operation_transitions
where operation_id='46b1322a-2c8d-4c93-8151-fc5176a05325' order by attempt;
```

```text
 none        | pending     | begin               | 0
 pending     | executing   | execute             | 1
 executing   | unknown     | unknown             | 1
 unknown     | reconciling | reconcile_claim     | 1
 reconciling | unknown     | reconciled_unknown  | 1
 …（重复 9 轮）
```

代码侧确认：`cmd/worker/main.go:209 maxReconcileAttempts = 8`，超过即 `Escalate`；
`internal/magento/client.go:712` 对 `magento.order.create` 直接返回
`ObservationInconclusive`（注释说明：ledger 里存的是 customer id，读 `/orders/{id}`
会读到无关订单，所以「by construction」不可判定）。

- **影响**: 每一次 ambiguous 的下单都会变成一个需要人工介入的悬案，且**没有工具**能让集成方
  或运维按 operation_id 查它。生产库里已经积压 11 条这样的记录，说明这不是理论场景。
- **状态**: CONFIRMED

---

## INT-08: 批量属性选项：total 与 rows 对不上，重试语义不可区分

- **严重级别**: P2
- **工具**: `magento.attributes.options.bulk_create`
- **现象**: 请求 3 个标签（其中 1 个是请求内重复）→ `total: 3`，但 `rows` 只有 2 行，
  `created + failed = 2 ≠ total = 3`；被跳过的重复标签**没有任何一行**代表它。
  另外「已存在的标签」被算作 `failed`，错误是通用的 `validation_failed`，与真正的失败无法区分。
- **期望**: 逐行可见（任务要求「部分失败要逐行可见」）：重复/已存在的行应出现并标为
  `skipped`/`exists`，或者 `total` 应当反映实际处理数并给出 `skipped` 计数。
- **证据**:

```bash
magento.attributes.options.bulk_create
{"instance":"demo","attribute_code":"color",
 "labels":["INTG-OPT-A-20261122-032618","INTG-OPT-A-20261122-032618","Black"]}
```

```json
{
  "result": {
    "created": 1,
    "failed": 1,
    "rows": [
      {"label":"INTG-OPT-A-20261122-032618","status":"created","value":"239"},
      {"label":"Black","status":"failed",
       "error":"validation_failed: Magento rejected the request as invalid; verify store_code and field values"}
    ],
    "total": 3
  }
}
```

原样重发（模拟部分失败后的重试）：

```json
{"created":1,"failed":1,
 "rows":[{"label":"INTG-OPT-A-20261122-032618","status":"created","value":"239"},   ← 实际是 replay，非新建
         {"label":"Black","status":"failed","error":"validation_failed: …"}],
 "total":3}
```

店铺侧核对：`color` 选项里 `INTG-OPT-A-…` 只有 1 条（`value: 239`），说明重试没有重复建；
但响应里 `created: 1` 让调用方以为又建了一个，而且没有 `replayed` 标记（同 INT-02）。

- **影响**: 调用方按 `total == created + failed` 对账必然失败；重试时「已存在」与「真失败」
  同为 `failed` + 同一句通用错误，无法自动决策（跳过 vs 报警）。
- **状态**: CONFIRMED（`catalog.go` `BulkCreateAttributeOptions`：`Total: len(labels)` 用原始
  输入长度，`Rows` 用去重后的 `unique`）

---

## INT-09: 并发写同一产品 → 上游 400 被映射成不可重试的 validation_failed

- **严重级别**: P2
- **工具**: `magento.product.update`
- **现象**: 对同一 SKU 并发发 3 个改价请求，1 个成功、2 个返回
  `validation_failed` + 上游原文「The linked products data is invalid」；同样的改价
  **串行**执行则成功。
- **期望**: 这类上游 400 是并发写冲突（Magento 侧锁/链接数据），应映射为可重试或
  `conflict`，至少带上 `retryable: true`，否则按错误分类学调用方「不该重试」，与事实相反。
- **证据**:

```bash
# 并发 3 个（各自审批）
magento.product.update {"instance":"demo","sku":"24-MB05","price":"42.11", …}   # ok
magento.product.update {"instance":"demo","sku":"24-MB05","price":"42.22", …}
magento.product.update {"instance":"demo","sku":"24-MB05","price":"42.33", …}
```

```text
0 full= {"product":{"sku":"24-MB05","price":42.11,…}}                       ← 成功
1 full= {"code":"validation_failed","details":{"store_code":"pl",
        "upstream_message":"The linked products data is invalid. Verify the data and try again.",
        "upstream_status":"400"},
        "message":"Magento rejected the request as invalid; verify store_code and field values",
        "op":"magento PUT /products/{id}"}
2 full= （同上）
```

```bash
# 串行对照：成功
magento.product.update {"instance":"demo","sku":"24-MB05","price":"45.00"}
# → isError=None, "price": 45
```

- **影响**: ERP 并行化目录写入时，冲突被标成「你的参数错了」，调用方不会重试，
  部分 SKU 永久漏更新，而错误码看起来像永久性失败。
- **状态**: CONFIRMED（错误映射表把上游 400 一律归入 `validation_failed`）

---

## INT-10: 批量导入不支持「纯改价」，且 start 只报第一行错误

- **严重级别**: P2
- **工具**: `magento.import.validate` / `magento.import.start`
- **现象**:
  1. 只有 `sku,price` 两列的 CSV（`update_existing: true` 的典型 ERP 改价场景）被整单拒绝：
     `name is required and must be at most 255 characters`，且 `start` 只报**第一行**的错误。
  2. `import.validate` 的 schema **不接受** `update_existing`，所以「先 dry-run 再 start」
     的两次调用语义不一致（validate 不知道这是更新还是新建）。
- **期望**: 纯改价（更新已有商品）不应强制 `name`；`start` 的失败报告应列出全部坏行
  （validate 就能列全部），否则 5000 行里逐行试错。
- **证据**:

```bash
printf 'sku,price\n24-MB01,35.50\nINTG-NOPE-1,10.00\n' | base64
magento.import.validate {"instance":"demo","format":"csv","data_base64":"…","update_existing":true}
```

```json
{"code":"invalid_argument",
 "details":{"validation":"validating root: unexpected additional properties [\"update_existing\"]"},
 "message":"arguments do not match the tool schema"}
```

```bash
magento.import.validate {"instance":"demo","format":"csv","data_base64":"…"}   # 去掉 update_existing
```

```json
{"preview":{"errors":[
   {"field":"name","message":"name is required and must be at most 255 characters","row_number":2},
   {"field":"name","message":"name is required and must be at most 255 characters","row_number":3}],
 "invalid":2,"total":2,"valid":0}}
```

```bash
magento.import.start {"instance":"demo","format":"csv","data_base64":"…","update_existing":true,
                      "idempotency_key":"intg-imp-20261122-0333xx"}
```

```json
{"code":"validation_failed",
 "details":{"reason":"name is required and must be at most 255 characters","row":"2"},
 "message":"import has invalid rows"}
```

对比：`validate` 报了 2 行，`start` 只报第 2 行（`row: "2"`），且整单不入队（无部分导入）。

- **影响**: ERP 的日常改价/改库存必须拼上 `name` 等无关字段（且一旦拼错整批被拒）；
  坏行定位只能反复 `start` 试，行数多时不可操作。
- **状态**: CONFIRMED

---

## INT-11: 导入/状态冲突的错误文本不可编程

- **严重级别**: P2
- **工具**: `magento.import.start`、`magento.invoice.create`、`magento.order.create`
- **现象**: 三类完全不同的原因，都落到 `validation_failed`，真实原因只在
  `details.upstream_message` 的自由文本里，且常常是**波兰语**：
  1. 不存在的 SKU（`update_existing: true`）→ `attribute_set_id is required to create a product`
     （不是「SKU 不存在」）；
  2. 已 closed 的订单再开票 → `…Nie można utworzyć faktury, gdy zamówienie ma status closed`；
  3. 0.5 数量（低于最小起订量）→ `Najmniej, ile możesz zakupić to 1.`
- **期望**: 给出稳定的 reason code（例如 `not_found` / `illegal_transition` / `min_qty_violation`），
  自由文本仅作补充；至少不要把「SKU 不存在」报成「缺少 attribute_set_id」。
- **证据**:

```bash
# 1) 导入里写一个不存在的 SKU
magento.import.status {"instance":"demo","job_id":"8066170b-55f0-466e-a0ff-5cfdde0027bd"}
```

```json
{"job":{"state":"partial",
 "result":"{\"created\":0,\"failed\":1,\"failures\":[{\"action\":\"create\",
   \"error\":\"validation_failed: attribute_set_id is required to create a product\",
   \"row_number\":3,\"sku\":\"INTG-NOPE-1\"}],\"partial\":true,\"total\":2,\"updated\":1}"}}
```

```bash
# 2) 已 closed 订单再开票（批准后）
magento.invoice.create {"instance":"demo","order_id":23,"items":[{"order_item_id":26,"qty":1}]}
# → {"code":"validation_failed","details":{"upstream_status":"400","store_code":"pl",
#    "upstream_message":"Invoice Document Validation Error(s):The invoice can't be created without products.
#     Add products and try again.Nie można utworzyć faktury, gdy zamówienie ma status closed"}}

# 3) 0.5 数量（批准后）
magento.order.create {… "items":[{"sku":"24-MB01","qty":0.5}] …}
# → {"code":"validation_failed","details":{"stage":"cart_items","failed_sku":"24-MB01","store_code":"pl",
#    "upstream_status":"400","upstream_message":"Najmniej, ile możesz zakupić to 1.","completed_skus":""}}
```

- **影响**: 集成方只能对上游文本做字符串匹配（且是多语言文本）才能区分「重试无意义」
  与「需要人工补数据」，错误码层面不可编程。
- **状态**: CONFIRMED

---

## INT-12: 省略 idempotency_key 时，合法重复下单被永久去重

- **严重级别**: P2
- **工具**: `magento.order.create`
- **现象**: 不传 `idempotency_key` 时，中间件用 `DeriveKey(subject, argsHash)` 派生 key，
  因此**参数完全相同的第二笔真实订单**会命中 replay：返回 `isError=false` +
  `replayed: true` + 同一 `entity_id`，不会新建订单。
- **期望**: 文档说 `idempotency_key` 是 optional，且「Without a key one is derived from the
  arguments」——语义上没错，但对 ERP 很危险：省略 key 不等于「不去重」。应当至少在
  description 里写明「省略 key = 同参数永久去重」，或对 money 工具要求显式 key。
- **证据**:

```bash
# 第一笔（不带 idempotency_key）
magento.order.create {"instance":"demo","customer_id":3,"items":[{"sku":"24-MB04","qty":1}], …}
# → isError=None  entity_id=24  increment_id=000000024  operation_id=b051ca10-…

# 第二笔，参数完全相同（另一笔真实订单）
magento.order.create （同上 arguments）
# → isError=None  entity_id=24  increment_id=000000024  replayed=true
```

店铺侧核对：customer 3 的 `24-MB04` 订单只有 1 笔（entity 24）。

- **影响**: ERP 若把 key 当可选字段（文档允许），同客户同商品的重复购买会被静默吞掉；
  只有检查 `replayed: true` 才能发现，而很多集成只看 `isError`/`entity_id`。
- **状态**: CONFIRMED（`Operations.Begin`：`if key == "" { key = DeriveKey(subject, argsHash) }`）

---

## INT-13: schema 描述自相矛盾

- **严重级别**: P3
- **工具**: `magento.product.search`（以及所有带 `instance` 的工具）、`magento.products.bulk_update`
- **现象**:
  1. `instance` 的描述是「The instance input is resolved by the server when only one Magento
     instance is configured」，但 schema 把它列为 `required`——按描述省略会直接报错。
  2. `bulk_update` 顶层描述说「Partial success: failed SKUs are reported per row」，
     而 `skus` 字段描述说「Every SKU must exist or the whole call is rejected」——实测是**整单拒绝**。
- **期望**: 描述与 schema/行为一致。
- **证据**:

```bash
magento.product.search {"sku":"24-MB01"}     # 按描述省略 instance
# → {"code":"invalid_argument","details":{"validation":"validating root: required: missing properties: [\"instance\"]"}}

magento.products.bulk_update {"instance":"demo","skus":["24-MB04","INTG-NOPE-1","INTG-NOPE-2"],"status":1}
# → {"code":"invalid_argument","details":{"unknown_skus":"INTG-NOPE-1,INTG-NOPE-2"},
#    "message":"unknown SKU(s): INTG-NOPE-1, INTG-NOPE-2; no product was updated"}   ← 整单拒绝
```

- **影响**: 集成方按描述写代码会踩坑；「部分成功」与「整单拒绝」两种语义影响重试策略设计。
- **状态**: CONFIRMED

---

## INT-14: import.start 重放已完成任务仍返回 queued: true

- **严重级别**: P3
- **工具**: `magento.import.start`
- **现象**: 同一 `idempotency_key` 重发，返回同一个 `job_id`，但 `queued: true`、`rows: 2`
  与首次完全一样，没有 `replayed` 标记；而该 job 早已 `state: partial` 结束。
- **期望**: 与 money 工具一致，带 `replayed: true`（或 `queued: false`），让调用方知道没有新任务。
- **证据**:

```bash
magento.import.start {… "idempotency_key":"intg-imp2-20261122-033344"}   # 首次
# → {"job_id":"8066170b-55f0-466e-a0ff-5cfdde0027bd","queued":true,"rows":2}
magento.import.start （同一 arguments）                                  # 重放（任务已完成）
# → {"job_id":"8066170b-55f0-466e-a0ff-5cfdde0027bd","queued":true,"rows":2}
magento.import.status {"instance":"demo","job_id":"8066170b-…"}
# → {"job":{"state":"partial","finished_at":"2026-11-21T19:33:49Z", …}}
```

- **影响**: 集成方会误以为排了新任务，重复轮询/重复记账。
- **状态**: CONFIRMED

---

## INT-15: import.status 的 result 是 JSON 字符串

- **严重级别**: P3
- **工具**: `magento.import.status`
- **现象**: `job.result` 是一段被转义的 JSON 字符串，调用方需要二次解析；逐行失败信息
  （`failures[]`）藏在这一层里。
- **证据**:

```json
{"job":{"state":"partial",
 "result":"{\"created\":0,\"failed\":1,\"failures\":[{\"action\":\"create\",\"error\":\"…\",\"row_number\":3,\"sku\":\"INTG-NOPE-1\"}],\"failures_truncated\":false,\"partial\":true,\"skipped\":0,\"total\":2,\"updated\":1}"}}
```

- **影响**: 机器消费要写两层解析；类型化客户端（按 schema 生成）拿不到 `failures` 的结构。
- **状态**: CONFIRMED

---

## INT-16: 超长 idempotency_key 被完整回显

- **严重级别**: P3
- **工具**: 所有带 `idempotency_key` 的写工具
- **现象**: `maxLength` 校验失败时，错误详情把**整段 300 字符的 key 原样回显**；而
  `idempotency_key` 的字段描述明确说「treat it as a secret」。
- **证据**:

```bash
magento.order.cancel {"instance":"demo","order_id":23,"idempotency_key":"xxxx…（300 个 x）"}
```

```json
{"code":"invalid_argument",
 "details":{"validation":"validating root: validating /properties/idempotency_key: maxLength: \"xxxx…（300 个 x）\" contains 300 Unicode code points, more than 256"},
 "message":"arguments do not match the tool schema"}
```

- **影响**: 如果调用方用真实 secret 做 key（文档建议不可猜测的值），它会以明文进入错误日志/审计。
- **状态**: CONFIRMED（建议截断回显，例如 `xxx…(300)`）

---

## INT-17: order.create 的 ledger entity_id 是客户 id

- **严重级别**: P3
- **工具**: 操作台账（`operations` 表）/ 对账流程
- **现象**: `order.create` 的 ledger 行是 `entity_type='order'`，但 `entity_id` 存的是
  **customer id**（下单时订单还不存在）。运维按 `entity_type/entity_id` 查「哪一笔订单」会查错。
- **证据**:

```bash
magento.order.create {"instance":"demo","customer_id":3, …}
# → order.entity_id = 23
```

```sql
select operation, entity_type, entity_id from operations
where idempotency_key='intg-order-20261122-032618';
-- magento.order.create | order | 3        ← 3 是 customer id，不是订单 23
```

- **影响**: 人工对账/工单系统会把「订单 3」当成目标；真实订单号只出现在 `result` 里。
- **状态**: CONFIRMED（代码注释亦承认这一点，见 `client.go:712`）

---

## INT-18: order.search 不含明细行，读回对账无法区分订单

- **严重级别**: P3
- **工具**: `magento.order.search`
- **现象**: `operation_unknown` 后的官方建议是「读回客户近期订单」，但 `order.search`
  不返回 items（无 SKU/数量），同一客户近期多笔订单时无法判断哪一笔是本次要下的。
- **证据**:

```bash
magento.order.search {"instance":"demo","customer_id":3,"limit":5}
```

```json
{"orders":[{"entity_id":16,"created_at":"2026-11-21 18:28:49","grand_total":29.5,"state":"closed", …},
           {"entity_id":23,"created_at":"2026-11-21 19:26:23","grand_total":39,"state":"closed", …},
           …]}       ← 没有 items 字段
```

- **影响**: 对账要退化成「先 search 再逐个 order.get」，且仍需人工比对；与 INT-07 叠加后，
  unknown 的闭环成本很高。
- **状态**: CONFIRMED

---

## INT-19: 错误信封的 retryable 只在为 true 时出现

- **严重级别**: P3
- **工具**: 全部（错误信封）
- **现象**: `{"code","message","details","op"}` 是常见形态，只有 `Retryable=true` 时才多一个
  `"retryable": true`；ADR-0010 写的是「Every error carries Code, Message, **Retryable**,
  Upstream, Op, and bounded redacted Details」。
- **期望**: 明确「缺省 = false」（并在文档写明），或始终输出该字段，让客户端不必猜。
- **证据**: 本报告里所有 `operation_unknown` / `confirmation_used` / `conflict` /
  `idempotency_key_reuse` 的输出都**没有** `retryable` 字段；而代码里
  `limits.go:188`（rate_limited）、`client.go:306`（5xx）等会置 `true`。
- **影响**: 通用重试中间件若按「字段缺失=未知」处理，会保守不重试（可接受）；
  若按 schema 生成客户端则字段可选，容易写出错误的默认值。
- **状态**: CONFIRMED（文档与实现不一致，非功能缺陷）

---

## INT-20: 不存在的工具名返回 internal

- **严重级别**: P2
- **工具**: 任意（tools/call 的 name）
- **现象**: 把工具名打错（集成期最容易犯的错）时，网关返回 `isError=true` +
  `code: "internal"` + `"an unexpected error occurred"`，**没有**任何「工具不存在」的提示，
  与真实的服务器内部故障完全同形。MCP 协议本身有 `-32602 Unknown tool` 可用。
- **期望**: 未知工具应是协议级错误（`-32602`）或 `invalid_argument` + `details.tool`，
  明确告诉集成方「名字错了，改代码」，而不是给一个看起来可重试的 `internal`。
- **证据**:

```bash
# tools/call 的 name 不存在
magento.does.not.exist {"instance":"demo"}
```

```json
{
  "_meta": {"instance":"demo","request_id":"28761c7170e43f54b8bbcbf89f841ae4",
            "tool":"magento.does.not.exist"},
  "content": [{"type":"text","text":"internal: an unexpected error occurred"}],
  "structuredContent": {"code":"internal","message":"an unexpected error occurred"},
  "isError": true
}
```

- **影响**: 集成方按错误码分流时会把「工具名写错」当成服务器故障，可能触发告警/重试，
  或者反过来在真正的 internal 故障上以为是自己拼错了。排查成本高。
- **状态**: CONFIRMED

---

## INT-21: 传输层错误返回纯文本

- **严重级别**: P3
- **工具**: 协议层
- **现象**: JSON 体解析失败、未知 method 都返回 HTTP 400 + **纯文本** body，
  不是 JSON-RPC 错误对象（`-32700` / `-32601`）。
- **期望**: 按 JSON-RPC 2.0 返回错误对象，便于客户端统一解析。
- **证据**:

```bash
curl -sS -w 'http=%{http_code}\n' -X POST http://127.0.0.1:18110/mcp \
  -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  -H "Mcp-Session-Id: $SID" -d '{"jsonrpc":"2.0","id":7,"method":"tools/call","params":'
# http=400
# malformed payload: unmarshaling jsonrpc message: unexpected EOF

curl -sS -w 'http=%{http_code}\n' … -d '{"jsonrpc":"2.0","id":8,"method":"tools/nope","params":{}}'
# http=400
# JSON RPC not handled: "tools/nope" unsupported
```

- **影响**: 客户端的错误处理分两条路（HTTP 文本 vs JSON-RPC 对象），集成时要写特例；
  错误分类/重试逻辑覆盖不到这两类。
- **状态**: CONFIRMED

---

## 验证通过的行为（同样需要记录）

1. **money 工具的幂等是真的幂等**（`replayed: true` 在 structuredContent 里可见，且无重复写入）：
   - `order.create` 同 key 重放 → 同一 `entity_id: 23`、`replayed: true`；Magento 侧仅 1 笔订单；
   - `invoice.create` 同 key 重放 → 同一 `entity_id: 22`、`replayed: true`；订单 23 只有 1 张发票；
   - `creditmemo.create` 同 key 重放 → 同一 `entity_id: 18`、`replayed: true`；只有 1 张 credit memo；
   - `order.cancel` 同 key 重放 → `canceled: true` + `replayed: true`，`operation_id` 相同。
2. **同 key 不同参数 → `idempotency_key_reuse`**，且带 `operation_id` 与 `state`：

   ```json
   {"code":"idempotency_key_reuse",
    "details":{"operation_id":"2c5ba420-c882-44f0-ac79-aff8bd886d3e","state":"succeeded"},
    "message":"idempotency key was already used with different arguments"}
   ```

3. **并发不重复写**：5 路并发同 key 下单，Magento 只落 1 单（entity 30），ledger 只有 1 行
   （`state=succeeded, attempt=1`），其余 4 路 `confirmation_used`；另一组「各自审批」的
   5 路并发也只落 1 单（entity 33），其余 4 路是可对账的 `conflict` + `operation_id`；
   竞态结束后用同 key 重试 → `replayed: true`、拿到胜者的订单（entity 33），**恢复路径可用**。
4. **并发读没有竞态**：同一 session 5 路并发 `product.get` 全部 200，无串行化问题
   （注意：**同一个 JSON-RPC id** 并发会得到 `-32600 duplicate in-flight request ID`，
   这是协议层的正确拒绝，不是缺陷；不同 id 即正常）。
5. **`operation_unknown` 时同 key 重试被拒且不发上游请求**（不会造成重复写入）——
   语义正确，问题只在「没有轮询出口」（INT-07）。
6. **导入的逐行失败报告是诚实的**：`partial: true`、`failures[].row_number/sku/error` 齐全，
   成功行真的生效（`24-MB01` 价格被改成 35.5，已验证），失败行没有副作用
   （`INTG-NOPE-1` 未创建，404）。
7. **不存在的路由 → `unsupported_capability`**（不是静默失败）：

   ```json
   {"code":"unsupported_capability",
    "details":{"option_route_status":"404","fallback_code":"validation_failed",
               "fallback_message":"The ProductSku is empty. Set the ProductSku and try again."},
    "message":"this Magento instance does not expose custom-option creation; saving the option through the product update also failed"}
   ```

8. **参数校验严格且可编程**：缺必填 / 类型错 / 超长 / 枚举越界 / 负数数量 全部返回
   `invalid_argument` + `details.validation`（带 JSON-Schema 路径），不会静默纠正。
9. **未知 instance / 未知实体可分**：`instance:"nope"` → `not_found` + `details.instance`；
   未知订单/商品 → `not_found` + `details.upstream_status` + `details.upstream_message`
   （注意 message 把「实体不存在」和「路由不存在」都写作 "no such resource or route"，
   需靠 details 区分）。
10. **状态冲突可编程**：对已取消订单再取消 → `conflict` + `details.order_state: "canceled"`。

---

## 测试数据与清理

- 本次创建的订单：23（24-MB01×1）、24（24-MB04×1，已取消）、30、33（并发测试胜者）；
  发票 22（订单 23）、28（订单 33）；credit memo 18（订单 23）。
- 为验证 `operation_unknown` 的响应形态，插入过 **1 条** 受控 ledger 行
  （`idempotency_key='intg-unknown-20261122-033216'`，`state='unknown'`，
  `reconcile_after=+2 days`），测试后已删除（`DELETE 1`），未产生任何上游写入。
- 属性选项新增：`color` 下 `INTG-OPT-A-20261122-032618`（value 239）。
- 价格改动已全部还原：24-MB01=34、24-MB02=59、24-MB04=32、24-MB05=45、24-MB06=45；
  24-MB06 的 status 已还原为 1。
- 未修改任何代码。