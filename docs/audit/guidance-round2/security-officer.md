# t50 运维：处置 12 条 `operation_unknown`（生产 v0.11.9）

**任务**：把生产库积压的 12 条 `needs_attention` 逐条判定并处置，同时验证 v0.11.9 的
「`Escalate` 的 reason 终于写库」修复。

**结论先行**：

- 12 条里 **1 条可以给出确定判定**（`stock_source_link`，判 `not_applied`），**已 dispose**，
  `needs_attention` 从 **12 → 11**；transition 的 reason 是
  `disposed_not_applied: <note>`，**v0.11.9 的修复成立**。
- 另外 **11 条 `order.create` 无法判定**，建议**保持 unknown**（不 requeue、也不假判 applied/not_applied）。
  根因有三层：账本 `entity_id` 存的是客户 id（U-70）、**demo 店在本轮之后被重置**（证据见 §2）、
  且这 11 条升级时的 reason 属于修复前的行（从未写库）。
- 一个必须记下来的运维事实：**demo 店的保留窗口只有几小时**。这 12 条是 11-20/21 的，我 11-22 晚
  去查时，它们指向的客户、订单、商品、库存源**全部已被重置掉**——「去上游读一眼」这个标准处置
  动作，在 demo 环境上过期即失效。

---

## 0. 方法与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | `platform.info` → `v0.11.9` |
| 实例 | `demo` |
| 审批 | 与 README 同一路径：`mage2mcp-admin -as live-reviewer approve <id>`（用 v0.11.9 镜像，与 server 同版本） |
| 只读手段 | `operation.list` / `operation.get` / `order.search` / `order.get` / `product.get` / `customer.get` / `inventory.sources` |
| 写手段 | 仅 1 次 `operation.dispose`（两步审批） |
| 快照时间 | 2026-11-22T19:0xZ（处置时刻）；数据读取 18:5x–19:0xZ |

未改任何代码，未改 demo 店的既有商品/订单；唯一的写是那一条 dispose。

## 1. 12 条清单（`operation.list {"needs_attention":true}`）

`total=12`，`truncated=false`。全部 `state=unknown`、`error_code=needs_manual_reconciliation`、
`reconcile_count=9`、`attempt=1`、`result=null`。

| # | id（前 8） | operation | entity | key | created_at (UTC) |
|---|---|---|---|---|---|
| 1 | `13380e5e` | order.create | order/**2** | `mcptest-order-20261121-013023` | 11-20 17:33:30 |
| 2 | `6dd09077` | order.create | order/**3** | `mcptest-order-20261121-014646` | 11-20 17:49:34 |
| 3 | `7654408c` | order.create | order/**4** | `mcptest-order-20261121-021328` | 11-20 18:16:09 |
| 4 | `3b436b34` | order.create | order/**5** | `mcptest-order-20261121-041859` | 11-20 20:21:28 |
| 5 | `205bb927` | order.create | order/**6** | `mcptest-order-20261121-042640` | 11-20 20:29:07 |
| 6 | `aa3fca27` | order.create | order/**7** | `mcptest-order-20261121-043325` | 11-20 20:35:53 |
| 7 | `0685e5ea` | order.create | order/**2** | `mcptest-order-20261121-135657` | 11-21 06:00:50 |
| 8 | `a878332e` | order.create | order/**3** | `mcptest-order-20261121-140252` | 11-21 06:07:27 |
| 9 | `52bf1889` | order.create | order/**4** | `mcptest-order-20261121-141036` | 11-21 06:14:33 |
| 10 | `f1c4bb88` | order.create | order/**5** | `mcptest-order-20261121-151544` | 11-21 07:19:49 |
| 11 | `46b1322a` | order.create | order/**5** | `mcptest-order-explore-1` | 11-21 07:53:25 |
| 12 | `00e28776` | **stock_source_link** | stock/**semtest** | `auto:2f183a4e…:b170cde6…` | 11-21 17:30:03 |

注意第 1 列：`entity_type=order` 但 `entity_id` 是 **客户 id**（2..7），不是订单号——这是 U-70，
也是「不要 requeue」的原因（见 §3.1）。

## 2. 关键环境事实：demo 店已被重置，历史证据已消失

这是本次处置最重要的发现，也是 11 条无法判定的直接原因。

| 证据 | 结果 |
|---|---|
| `order.get 45`（账本 11-21 20:28 有它的 shipment/invoice/comment） | **not_found** |
| `order.get 5`（账本 11-21 17:34 `order.cancel` 成功过） | **not_found** |
| `order.get 4` | 存在，但 `created_at=2026-11-22 18:58:22` —— 不是历史那笔 4 |
| `product.get MCPTEST-PROD-20261122-203540`（账本 11-22 12:37 创建成功） | **not_found** |
| `product.get G2-SO-1001`（账本 11-22 18:57 创建） | 存在 |
| `customer.get 2..7`（11 条 unknown 指向的客户） | **全部 not_found**（只有 customer 1，2022 样店数据） |
| `inventory.sources` | 只剩 `default`（`semtest` 已不在） |
| `order.search`（无过滤） | `total=3`：order 1、2（2022-08-26）+ order 3（2026-11-22 18:57） |

**重置发生在 2026-11-22 12:37Z 与 18:57Z 之间**（12:37 的商品没了、18:57 的还在），
即我处置前约 5 小时。账本里的 order id 序列也印证了多次重置（11-21 11:26 到 `order/17`，
16:38 又从 `order/3` 开始）。

**含义**：对这 11 条来说，「上游现在没有这笔订单」**不能**推出「当时也没创建」——数据是被抹掉的，
不是没发生。这也解释了为什么标准处置动作（去上游读一眼）在 demo 上过期即失效。

## 3. 逐条判断

### 3.1 11 条 `order.create`：**不可判定 → 建议保持 unknown**

每条的账本上下文都相同：`created_at` 前 19–25 秒有 `customer.create` 成功（所以客户当时存在），
`order.create` 之后 1 小时内**没有**该订单的任何后续操作（comment/hold/email/cancel 都没有）。

| # | 客户 | 前序 `customer.create` | 之后 1 小时内的 order.* 操作 | 判定 |
|---|---|---|---|---|
| 1–9 | 2..4 | 19–25s 前成功 | 无 | 不可判定 |
| 10 | 5 | 24s 前成功 | 无（1287s 后才有 `order/6` 的 comment，与它无关联） | 不可判定 |
| 11 | 5 | 30s 内无 | 177s 后 `order/8` hold —— 无法归属 | 不可判定 |

为什么不可判定，三条互相独立的原因：

1. **账本本身指不出订单号**。`entity_type=order` 但 `entity_id` 是客户 id（U-70）。想核「这一单
   建没建」，得先知道订单号；账本没记。查「该客户在窗口内的订单」需要客户存在——见第 2 条。
2. **上游证据已被重置**（§2）。客户 2..7 与那两天的订单都不在了，现在读到的「没有」不可作为
   「当时没有」的证据。
3. **没有同 key 的兄弟行**。12 个 key 在 1917 行账本里各出现 1 次，没有「后来重试成功」的行可以
   反推（若同一 key 后来 succeeded，就能说明第一次也已落地或至少被 replay 覆盖）。
4. 对账器试了 9 次后升级（`reconcile_count=9`），但它升级时的 reason **从未写库**（修复前的行），
   所以连「它当时看到了什么」都不可读。

**为什么不能 requeue**：按 `docs/audit/persona/captain-adjudication.md`「历史 11 条的处置规则」，
`entity_type=order` + 客户 id 会让对账器走 order 分支读 `/orders/5`——**读到别人的订单**。所以
requeue 会把「未知」变成「按错误实体判定」，比不处置更糟。

**为什么也不假判 applied/not_applied**：dispose 是**记录人工判断**，写进去就是账本的一部分
（`manual_applied` / `manual_not_applied`），后续对账与审计都会当真。在证据已被抹掉的情况下填
一个「已验证不存在」，是把「不知道」伪装成「查过了」——这轮审计反复强调的「绿信号 ≠ 正确性」，
在运维侧就是「处置完成 ≠ 判定正确」。

**建议**（需要队长决策，我没有执行）：

- 保持 unknown（默认，什么都不做），等原始请求/响应日志或上游备份出现；
- 或者，如果团队认定这 11 条是**测试残留、无业务影响**，可以按**策略**批量 dispose 为 `not_applied`，
  note 写明「policy: test residue from 11-20/21; store reset; no business impact」——但那是策略判断，
  不是证据判断，应由人签字，不应由我以「已验证」的名义写。

### 3.2 第 12 条 `stock_source_link`（`semtest`）：**not_applied，已处置**

这条有**账本内的完整证据链**，不依赖已被重置的店铺状态：

| 时刻 (UTC) | 事件 | 含义 |
|---|---|---|
| 11-21 17:29:40 | `magento.inventory.source_create` **failed**（`validation_failed`） | 第一次建源失败，源 `semtest` **不存在** |
| 11-21 17:30:03 | **本行**：`stock_source_link` → unknown | 在源不存在时尝试建立「源→库存」链接 |
| 11-21 17:32:17 | `magento.inventory.source_create` **succeeded**（`stock/semtest`） | 源**到这时才被创建** |
| 11-21 17:34:12 | `stock_source_link` **failed**（`validation_failed`） | 源存在之后的再次尝试仍失败 |

推理：Magento 的 stock-source link 要求 `source_code` 已存在（否则 400 validation）。本行发生在
17:30:03，而该源 17:32:17 才创建成功（此前 17:29:40 的创建还是失败的）——**链接不可能在源不存在
时被上游接受**。所以判 `not_applied`。

残留不确定性（已写进 note 的措辞里）：17:34:12 那次失败的具体报文没有记录（账本只存
`validation_failed`），所以「为什么失败」不可知；但「17:30:03 时源不存在」这一点是账本自证的。

## 4. 实际处置（1 条）与证据

### 4.1 两步审批

```text
① magento.operation.dispose {operation_id: 00e28776…, judgment: not_applied, note: <503 字符>}
   → isError=true, code=confirmation_required
     confirmation_id = 5fe85cb5-a8ce-445a-851a-2ae02cd05f77
     expires_at      = 2026-11-22T19:05:50Z
     message         = "this operation requires approval by a different human"

② 人工批准（生产机，v0.11.9 镜像的 admin 二进制）
   mage2mcp-admin -config /etc/mage2mcp/config.yaml -as live-reviewer approve 5fe85cb5-…
   → warning: authentication is disabled, so "live-reviewer" is a self-asserted approver identity
   → approved 5fe85cb5-a8ce-445a-851a-2ae02cd05f77 by live-reviewer

③ 原样重发 + confirmation_id → 成功
```

审批记录落在 `confirmation_tokens`（不是 `audit_events`）：

```text
id=5fe85cb5… tool=magento.operation.dispose scope=write
params_hash=cbaf241f… requester_hash=2f183a4e… approver_hash=0810c1e4…
summary="judgment=not_applied note=<503 chars> operation_id=00e28776…"
state=consumed  approved_at=19:00:56.218Z  consumed_at=19:01:01.775Z
```

### 4.2 处置后的行状态

```text
state           unknown → failed
error_code      needs_manual_reconciliation → manual_not_applied
needs_attention true → false
finished_at     2026-11-22T19:01:01.777038Z
idempotency_key …:b170cde6… → …:b170cde6…:released:1458d0c3   ← 派生 key 已释放，允许安全重试
```

`needs_attention` 计数：`operation.list` **12 → 11**；SQL 交叉核对 `needs_attention=11`、
`manual_not_applied=1`。

### 4.3 v0.11.9 的 reason 修复：成立

同一行的 transition 历史（`operation_transitions`，按 seq 倒序）：

```text
unknown → failed   disposed_not_applied: Ledger chain for source semtest on 2026-11-21: source_create failed at
                   17:29:40Z (validation_failed), this stock_source_link went unknown at 17:30:03Z, source_create
                   succeeded only at 17:32:17Z, and the next stock_source_link failed at 17:34:12Z. …（note 全文）
                   2026-11-22 19:01:01.777038+00
reconciling → unknown   escalated        2026-11-21 21:40:40.43957+00     ← 修复前的行：只有字面量 'escalated'
unknown → reconciling   reconcile_claim  2026-11-21 21:40:40.437182+00
```

- 新的处置行：`disposed_<judgment>: <note>` ✅ 与 v0.11.9 发布说明一致。
- 同一行里还留着修复前的 `escalated`（无 reason）——**修复只对之后写入的行生效**，历史行不会补写。
  这正是本轮处置这 11 条时「读不到升级原因」的原因。

### 4.4 审计轨迹（顺带观察）

同一次 dispose 在 `audit_events` 里是 3 行：

```text
seq 16492  (middleware) intent/pending   request_id=9c6653df…  identity_class=anonymous
seq 16495  scope=dispose outcome/failed  request_id=<空>  confirmation_id=<空>  decision=allow  identity_class=anonymous
seq 16496  (middleware) outcome/succeeded request_id=9c6653df…
```

两点与第一轮报告一致，仍未修（不在本任务范围）：

- 工具自写的 outcome 行**没有 `request_id`、没有 `confirmation_id`**——审批（5fe85cb5）与这次操作
  在 `audit_events` 里无法关联，只能靠 `confirmation_tokens.summary` 里那行 `operation_id=` 手工串。
- `identity_class=anonymous` 是 `auth.enabled=false` 的直接结果；`-as live-reviewer` 只是自述身份
  （admin 自己也打了 warning）。

## 5. 顺带观察（不属本任务，供决策）

1. **201 个未消费的 confirmation token** 堆在生产库里（`consumed_at IS NULL`）。多半是测试残留；
   它们会让「待审批」视图与真实待办混在一起，建议定期清理。
2. **处置必须在保留窗口内做**：demo 店几小时一重置，任何「先放着、以后再核」的 unknown 都会变成
   永久不可判定。要么把 12 条这类残留清掉，要么把 demo 店的重置计划写进运维约定。
3. **U-70 是这 11 条不可判定的根**：`order.create` 记客户 id 而非订单号，导致「去上游查哪一单」
   无从下手。它已在第一轮汇总里，这次处置又验证了一遍——修它比逐条猜更值。

## 6. 附录：原始证据

```text
# 1) 取 12 条
magento.operation.list {"needs_attention": true, "limit": 100}
→ total=12, truncated=false（§1 表）

# 2) 逐条 get（示例，第 12 条）
magento.operation.get {"operation_id": "00e28776-7981-4b66-a108-d9465046f2c8"}
→ state=unknown, entity=stock/semtest, reconcile_count=9,
  next="unknown and escalated: read the upstream and dispose of it with magento.operation.dispose (applied,
        not_applied or requeue); do not retry the write"

# 3) 重置证据
magento.order.get {"order_id": 45}        → not_found
magento.order.get {"order_id": 5}         → not_found
magento.order.get {"order_id": 4}         → created_at "2026-11-22 18:58:22"
magento.product.get {"sku": "MCPTEST-PROD-20261122-203540"} → not_found
magento.product.get {"sku": "G2-SO-1001"} → exists
magento.customer.get {"customer_id": 2..7} → not_found（1 存在）
magento.inventory.sources {}              → ["default"]

# 4) semtest 账本链（operation.list 全量 1917 行过滤）
17:29:40 source_create  failed     stock/semtest  validation_failed
17:30:03 stock_source_link unknown stock/semtest  needs_manual_reconciliation  ← 本行
17:32:17 source_create  succeeded  stock/semtest
17:34:12 stock_source_link failed  stock/semtest  validation_failed

# 5) 处置后
magento.operation.get {"operation_id": "00e28776…"}
→ state=failed, error_code=manual_not_applied, needs_attention=false
magento.operation.list {"needs_attention": true} → total=11

# 6) 数据库交叉核对（生产机，只读）
needs_attention = 11 ; manual_not_applied = 1
operation_transitions: unknown→failed  reason="disposed_not_applied: …"  @ 19:01:01.777Z
```

## 7. 一句话总结

**12 条里只有 1 条在证据被重置掉之前还留得下自证链**——那条我处置了，并验证了 v0.11.9 的
`disposed_<judgment>: <note>` 与 `needs_attention` 递减；其余 11 条我拒绝用一个假判定把它们从
视图里清掉：**「处置完成」不等于「判定正确」**，在证据已消失时，诚实的答案是保持 unknown。

## 8. 队长签字与后续（处置之后追加）

**签字决定：不批量 dispose 那 11 条。** 理由（队长原话）：dispose 会把人工判定写进账本，证据被抹掉后
填「已验证不存在」等于把「不知道」伪装成「查过了」；而且 `not_applied` 会**释放派生 key**，等于允许
重试——对一个我们不知道是否执行过的下单操作，这是错的方向。

### 8.1 已知基线（运维侧照此读，不要把基线当告警）

| 基线 | 值（2026-11-22T19:0xZ） | 说明 |
|---|---|---|
| `operation.list{needs_attention:true}` | **11**（全部 `magento.order.create`） | **本轮测试残留**，不是客户真实操作；demo 店已重置，**永久不可判定**，保持 unknown |
| 未消费的 `confirmation_tokens` | **201** | 测试残留（`state=pending` 的真实行）；**不在 demo 上 DELETE**，真实部署按运维手册定期清理过期项 |

**读法**：`needs_attention` 的 **11 是已知基线**，不是新信号；只有**超过 11** 才说明出现了新的
不可判定操作。同理，pending 确认的**增长**才是信号。

### 8.2 处置规则补充：上游不可查时，用账本内部的时间序自证

这次 `semtest` 那条的判法值得固化成规则：

> 当上游已被重置/不可查时，不要停在「查不到所以没发生」。先看**账本自身的时间序**：同一实体的
> `create`/`link`/`delete` 行按时间排开，找出**前置条件是否在当时成立**。本例里，链接所命名的源在
> 尝试时刻**还不存在**（创建成功发生在 2 分 14 秒之后），所以链接不可能被上游接受——这是
> 「当时不可能」的证明，而不是「现在看不到」的推断。

这条与「现在读上游」是两种证据强度：前者在数据被抹掉后仍成立，后者会随保留窗口过期而失效。

### 8.3 功能缺口（队长记为 finding，不在本任务修）

现有三种 judgment（`applied` / `not_applied` / `requeue`）都断言了「**发生了什么**」，缺少一种断言
「**这条不该存在于生产**」的处置类型（例如 `test_residue`）。这正是那 11 条卡住的根因：它们需要的
不是「判定了什么」，而是「承认它们是测试残留」。在补上这个类型之前，正确做法就是保持 unknown。

### 8.4 关于 `audit_events` 的精确化（供核实）

我上一版把「outcome 行无 `request_id`」说得太笼统。按 seq 精确到行：

```text
seq 16492  (middleware) intent/pending    request_id=9c6653df…   confirmation_id=<空>
seq 16495  scope=dispose outcome/failed   request_id=<空>        confirmation_id=<空>   ← 工具自写的处置行
seq 16496  (middleware) outcome/succeeded request_id=9c6653df…   confirmation_id=<空>
```

结论：**中间件写的 intent/outcome 行有 `request_id`（v0.11.7 已修）；缺的是工具自写的那条
`scope=dispose` 行**，以及三行的 `confirmation_id` 都为空（审批记录在 `confirmation_tokens`，
不在 `audit_events`）。这与第一轮 P-SEC-02/04 的结论一致，但影响面比「所有 outcome 行」小。
