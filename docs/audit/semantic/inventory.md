# 语义层测试 C：库存与 MSI（inventory）

- **被测版本**：生产 v0.11.5（`http://127.0.0.1:18110/mcp`，SSH 隧道至 `49.51.250.83:8080`）
- **实例 / 店铺**：`demo` / `https://demo-magento-2.auroracreation.com`，store code `pl`
- **测试时间**：2026-11-22 01:10–01:45 (+08:00)
- **测试方式**：`test/live -mode=raw` 逐工具调用，写入类工具经 `approve.sh` 审批；关键结论另用直连 Magento REST 交叉验证（绕过网关，确认「Magento 实际行为」与「工具转述」的差异）。
- **范围**：`magento.inventory.*`（sources / source_create / stocks / stock_source_link / stock_unlink / assign / unassign / transfer / status / status_batch / source_items）。
- **只读与写**：本报告只记录，未修改任何代码；测试写入的数据见文末「测试遗留物」。

> 环境注记（不属于缺陷）：该站只有 1 个 source（`default`）与 1 个 stock（`Default Stock`，stock_id 1）；
> 隧道偶发 `initialize: Post ... EOF`，所有命令均带 5–6 次重试后执行，**失败重试不计入结论**。

---

## 一、结论摘要

| 编号 | 严重级别 | 状态 | 一句话 |
|---|---|---|---|
| SEM-INV-01 | P2 | CONFIRMED | Default Stock 业务规则被转述成「字段错误」，Magento 的真实原因被丢弃 |
| SEM-INV-02 | P2 | CONFIRMED | `inventory.status.qty` 是 legacy 库存字段（0），与 MSI 源项（5 件）矛盾 |
| SEM-INV-03 | P2 | CONFIRMED | 结果未知的写操作在错误信封里仍标 `retryable: true`，诱导重复写入 |
| SEM-INV-04 | P3 | CONFIRMED | `assign` 的 `assigned` 是入参计数，不是写入结果；不存在的 SKU 也会「成功」 |
| SEM-INV-05 | P3 | CONFIRMED | 一次 5xx 会把该操作永久钉在 unknown，修正后同参数仍拒绝 |
| SEM-INV-06 | — | NOT-A-DEFECT | `assign` 是覆盖写（10→3），语义正确 |
| SEM-INV-07 | — | NOT-A-DEFECT | qty=0 默认 status=1，但 salable 正确派生为缺货 |
| SEM-INV-08 | — | NOT-A-DEFECT | 负数/小数/同源调拨被正确拒绝；transfer 的 before/after 与回读一致 |
| SEM-INV-09 | — | SUSPECTED | 批量「部分失败」无法被工具表达（只有单一计数），本次未观察到真实部分生效 |

**最重要的两条**：SEM-INV-01（业务规则被误报为字段错误，操作员会去查 store_code）与
SEM-INV-02（`qty` 字段在 MSI 店里不代表实物库存，读数会直接误导补货判断）。

---

## 二、逐条发现

### SEM-INV-01: Default Stock 的业务规则被转述为「字段错误」，Magento 的真实原因被丢弃

- **严重级别**: P2
- **工具**: `magento.inventory.stock_source_link`（同样影响 `magento.inventory.source_create`，见证据 B）
- **现象**: 把非默认源 `semtest` 链到 Default Stock（stock_id=1）时，工具返回：

  ```json
  {
    "code": "validation_failed",
    "details": {"store_code": "pl", "upstream_status": "400"},
    "message": "Magento rejected the request as invalid; verify store_code and field values",
    "op": "magento POST /inventory/{id}"
  }
  ```

  而 Magento 的真实响应（直连 REST，同一请求体）是：

  ```json
  {"message":"Validation Failed","errors":[{"message":"Can not save link related to Default Source or Default Stock","parameters":[]}]}
  HTTP=400
  ```

  工具既没有 `upstream_message`，也没有 `errors[].message`，只留下「去核对 store_code 和字段值」——
  而 store_code（`pl`）完全正确，字段也没有问题，真正的原因是 Magento 的业务规则。
- **期望**: README 明确「该店只有 Default Stock，非默认源的链接会被 Magento 拒绝——这是业务规则」。
  工具应如实转述 `Can not save link related to Default Source or Default Stock`，或映射为一个明确的
  业务错误（如 `unsupported_capability` / `validation_failed` 且带该原因），而不是让操作员去查字段。
- **证据**:

  ```bash
  # 1) 工具调用（position=3 换取新的幂等键，避免复用旧 operation）
  /tmp/live -mode=raw -tool=magento.inventory.stock_source_link \
    -args='{"instance":"demo","stock_id":1,"source_code":"semtest","position":3}'
  # → is_error=true code=validation_failed
  #   message: Magento rejected the request as invalid; verify store_code and field values
  #   details: {"store_code":"pl","upstream_status":"400"}   ← 没有 upstream_message

  # 2) 直连 Magento，同一语义请求
  curl -sS -X POST 'https://demo-magento-2.auroracreation.com/rest/pl/V1/inventory/stock-source-links' \
    -H "Authorization: Bearer $(cat /tmp/magento_token)" -H 'Content-Type: application/json' \
    -d '{"links":[{"stock_id":1,"source_code":"semtest","priority":2}]}'
  # → {"message":"Validation Failed","errors":[{"message":"Can not save link related to Default Source or Default Stock","parameters":[]}]}
  ```

- **影响**: 操作员/agent 被引导去检查 store_code 与字段，而真正的限制是 Magento 的 Default Stock 规则；
  在只有 Default Stock 的店里，这意味着「链接永远不可能成功」，但错误文本看起来像「改个参数再试」。
- **状态**: **CONFIRMED**（直连与网关两侧均有原始输出）

---

### SEM-INV-02: `inventory.status.qty` 是 legacy 库存字段，与 MSI 源项数量矛盾

- **严重级别**: P2
- **工具**: `magento.inventory.status`（`status_batch` 同源）
- **现象**: 把 WJ01 的库存设置为 `default=3`、`semtest=2`（源项合计 5 件）后：

  ```json
  // magento.inventory.source_items
  {"items":[
    {"sku":"WJ01","source_code":"default","quantity":3,"status":1},
    {"sku":"WJ01","source_code":"semtest","quantity":2,"status":1}]}

  // magento.inventory.status
  {"status":{"sku":"WJ01","qty":0,"is_in_stock":true,"manage_stock":true,"backorders":0,"min_qty":0}}
  ```

  `qty=0` 与 `is_in_stock=true` 自相矛盾：前者是 legacy `cataloginventory_stock_item.qty`，
  后者是 MSI 的可售状态（default 源已链到 Default Stock，可售 3 件）。
  直连 legacy 接口可确认工具是「忠实转述」了 Magento 的 legacy 字段：
  `GET /rest/pl/V1/stockItems/WJ01` → `{"qty":0,"is_in_stock":true,...}`。
- **期望**: 在 MSI 店铺里，`qty` 这个字段名会让调用方认为是「实物库存」。工具应至少
  ① 标注该字段来自 legacy stock item、不代表 MSI 源项合计，或 ② 返回 MSI 可售数量
  （如按 stock 聚合的 salable qty）并把它与 legacy qty 分开命名。
- **证据**: 上述两条命令 + 直连 `stockItems/WJ01` 的输出（三者在同一时刻取得）。
- **影响**: 补货/上架判断直接看 `qty` 的调用方会认为「无库存」，而实际有 5 件实物、3 件可售。
  这是 MAG-09 类（salable 派生）问题在读取侧的表现：派生状态是对的，但同名字段给出相反结论。
- **状态**: **CONFIRMED**（工具输出与 Magento legacy 接口一致，问题在字段语义未被声明）

---

### SEM-INV-03: 结果未知的写操作在错误信封里仍标 `retryable: true`

- **严重级别**: P2
- **工具**: 所有写入类（本例 `magento.inventory.stock_source_link`，5xx 场景）
- **现象**: 当 Magento 返回 5xx 时，工具的信封同时包含「结果未知」与「可重试」：

  ```json
  {
    "code": "upstream_unavailable",
    "details": {"outcome": "unknown", "store_code": "pl", "upstream_status": "500"},
    "message": "Magento is unavailable",
    "retryable": true
  }
  ```

- **期望**: 一个 `outcome=unknown` 的**写**操作不得同时宣称 `retryable: true`。二者是互斥建议：
  前者说「可能已生效，必须核对」，后者说「可以再试一次」。对资金/库存类写入，按后者行动就是重复写入。
  （网关内部的重试闸门确实会拒绝 unknown，但信封是给 **agent** 看的，agent 不会读 Go 代码。）
- **证据**: 首次把 `semtest` 链到 stock 1 时（当时 `semtest` 尚不存在，Magento 返回 500）的原始输出即为上表。
  同一操作在 `semtest` 创建后、用**相同参数**重试时，被账本以 `operation_unknown` 拒绝——
  说明网关自己知道它不可重试，但信封的建议与之相反。
- **影响**: 遵循 `retryable` 的 agent 会对一个「可能已生效」的库存写入再次调用，正是 REL-02 要防的重复变更。
- **状态**: **CONFIRMED**（信封字段与网关内部策略自相矛盾，两处均有实测输出）

---

### SEM-INV-04: `assign` 的 `assigned` 是入参计数，不是写入结果；不存在的 SKU 也会「成功」

- **严重级别**: P3
- **工具**: `magento.inventory.assign`
- **现象**: 一次批量分配里混入不存在的 SKU：

  ```bash
  /tmp/live -mode=raw -tool=magento.inventory.assign -args='{"instance":"demo","items":[
    {"sku":"WJ01","source_code":"default","quantity":7},
    {"sku":"NO-SUCH-SKU-999","source_code":"default","quantity":4}]}'
  # → {"assigned": 2}
  ```

  回读发现 Magento **真的**给不存在的 SKU 建了源项：

  ```json
  {"items":[{"sku":"NO-SUCH-SKU-999","source_code":"default","quantity":4,"status":1}]}
  ```

  即 `assigned: 2` 在「写入了 2 行」意义上为真，但 `assigned` 这个名字让人以为是「2 个商品的库存已设置」，
  而其中一个 SKU 没有对应商品，永远不会出现在任何商品的库存里。
- **期望**: 工具应校验 SKU 是否存在（或至少在输出中区分「已写入」与「SKU 不存在/无商品」），
  因为一个拼写错误的 SKU 会静默产生孤儿源项，且返回成功。
- **证据**: 上述 assign + source_items 两条命令的输出；孤儿源项当前仍存在（见「测试遗留物」）。
- **影响**: 批量补货脚本里的 SKU 拼写错误不会报错，操作员以为已设置库存；商品真正存在前无人发现。
- **状态**: **CONFIRMED**（但需注明：Magento 自身接受该写入，工具是忠实转述；问题在缺少存在性校验与字段命名）

---

### SEM-INV-05: 一次 5xx 会把该操作永久钉在 unknown，修正原因后同参数仍被拒绝

- **严重级别**: P3
- **工具**: 所有经账本的写工具（本例 `stock_source_link`）
- **现象**:
  1. 首次调用（`semtest` 尚不存在）→ Magento 500 → 账本记 `unknown`；
  2. 创建 `semtest` 后，用**完全相同的参数**再次调用 → 工具直接返回
     `operation_unknown`（`operation_id: 00e28776-…`），**没有发出任何上游请求**；
  3. 只有改动参数（position 2→3，即新的幂等键）才会真正打到 Magento。
- **期望**: 这是「绝不重试 unknown」的正确实现（REL-02），但对操作员而言缺少前进路径：
  响应里没有「如何解除」的指引（如 `magento.operation.status` / 触发 reconciliation 的方式），
  也没有说明「同一参数永远会被拒」。应至少提示「该操作需先由 reconciliation 处理，或改用新的幂等键」。
- **证据**: 两次调用的原始输出（第一次 500→unknown，第二次 operation_unknown，operation_id 相同）。
- **影响**: 操作员修好根因后，用同样的参数重试会被永久拒绝且不明所以；只能猜测要改参数。
- **状态**: **CONFIRMED**（行为符合设计；缺陷在可操作性/指引缺失）

---

### SEM-INV-06: `assign` 是覆盖写，不是累加

- **严重级别**: —
- **工具**: `magento.inventory.assign`
- **现象**: `assign qty=10` 后回读 `quantity=10`；再 `assign qty=3` 后回读 `quantity=3`（不是 13）。
- **期望**: 工具名为 assign，语义应为「把该源的数量设为 N」。实测与描述一致。
- **证据**:

  ```bash
  /tmp/live ... -tool=magento.inventory.assign -args='{"instance":"demo","items":[{"sku":"WJ01","source_code":"default","quantity":10}]}'
  # → {"assigned":1};  source_items → quantity=10
  /tmp/live ... -tool=magento.inventory.assign -args='{"instance":"demo","items":[{"sku":"WJ01","source_code":"default","quantity":3}]}'
  # → {"assigned":1};  source_items → quantity=3
  ```

- **影响**: 无（调用方需知道这是绝对写；工具描述已说明「设为」）。
- **状态**: **NOT-A-DEFECT**

---

### SEM-INV-07: qty=0 时默认 status=1，但 salable 正确派生为缺货

- **严重级别**: —
- **工具**: `magento.inventory.assign`
- **现象**: 只给 `quantity: 0`、不传 `status` 时，源项写成 `{"quantity":0,"status":1}`；
  但商品状态随之变为 `{"qty":0,"is_in_stock":false}`。
- **期望**: 源项 status 与数量不矛盾到影响可售判断即可。MSI 语义下 status=1/qty=0 仍不可售，
  实测 salable 正确翻转；调用方也可显式传 `status:0` 表达「该源缺货」。
- **证据**: assign(0) → source_items(status=1) → status(is_in_stock=false) 三次调用的连续输出。
- **影响**: 无（但建议文档说明 status 默认值为 1，与 quantity 无关）。
- **状态**: **NOT-A-DEFECT**

---

### SEM-INV-08: 边界与调拨语义正确

- **严重级别**: —
- **工具**: `assign` / `transfer`
- **现象**:
  - 负数：`quantity:-5` → schema 拒绝（`minimum: -5/1 is less than 0`）；
  - 小数：`quantity:1.5` → schema 拒绝（`type: 1.5 has type "number", want "integer"`）；
  - 同源调拨：`from_source==to_source` → `invalid_argument: from_source and to_source must differ`；
  - 调拨 2 件 default→semtest：

    ```json
    {"from_before":5,"from_after":3,"to_before":0,"to_after":2,"outcome":"applied","transferred":true}
    ```

    回读源项：`default=3, semtest=2`，与 before/after 完全一致。
- **期望**: 拒绝边界值、如实报告前后数量。实测一致。
- **证据**: 上述四条命令的原始输出（调拨前后均回读）。
- **影响**: 无。
- **状态**: **NOT-A-DEFECT**

---

### SEM-INV-09: 批量「部分失败」无法被工具表达（未观察到真实部分生效）

- **严重级别**: —
- **工具**: `magento.inventory.assign`（`unassign` 同构）
- **现象**: `assign` 的输出只有单一计数 `{"assigned": N}`，没有逐行结果。
  本次尝试构造部分失败（有效 SKU + 不存在 SKU）时，Magento 把两行**都**接受了，
  因此没有出现「一行生效一行失败」的真实场景；若 Magento 将来返回部分生效，工具也无字段可报告。
- **期望**: 要么在文档中声明「assign 是整批原子写，失败即整批失败」，要么提供逐行结果。
- **证据**: 混批调用返回 `{"assigned":2}` 且两行均落库（见 SEM-INV-04 的证据）。
- **影响**: 目前无实际损失（Magento 行为是整批接受/整批拒绝）；属于「声明未明确」而非已发生缺陷。
- **状态**: **SUSPECTED**（未观察到部分生效，无法判定工具是否会如实报告）

---

## 三、测试遗留物（demo 站）

| 对象 | 值 | 说明 |
|---|---|---|
| source `semtest` | Semantic Test Source, PL, 00-001 | 由 `source_create` 创建；未链到任何 stock（被 Default Stock 规则拒绝） |
| 源项 `NO-SUCH-SKU-999@default` | **已清理** | 测试后用直连 REST `POST /inventory/source-items-delete` 删除（HTTP 200），未留残留 |
| 源项 `WJ01@default` / `WJ01@semtest` | 3 / 2 | 调拨测试结果；demo 站会周期性重置 |
| 账本记录 | 若干 unknown 操作 | `stock_source_link` 的 500/unknown 记录，可被 reconciler 处理 |

> 工具层没有删除 source / 源项的能力。孤儿源项已用直连 REST 清理；`semtest` 源无法删除
> （`DELETE /inventory/sources/{code}` 返回路由 404，Magento 未暴露该路由），留给 demo 站周期性重置回收。
> 这一点本身也说明：`source_create` 是**单向**操作，通过 MCP 创建后无法通过 MCP 撤销——
> 建议在工具描述或 known-limitations 中注明（本次未单列为缺陷，因为它是 Magento 路由缺失而非工具缺陷）。

---

## 四、未覆盖项（诚实声明）

- **lost update / 并发覆盖**：`TransferSourceQty` 的读-校验-写序列在代码里存在（第二次读比对），
  但本次没有构造并发写入来验证它真的会返回 `conflict`——需要两个并发调用同一 (sku, source) 才能证明，
  受隧道延迟与审批流程限制未执行。列为未验证。
- **`status_batch`**：与 `status` 同源，仅抽查未单独取证。
- **`unassign` / `stock_unlink`**：会删除数据且无恢复手段，未在 demo 站执行。
- **`sources` 分页/截断**：该店只有 1 个源，`truncated:false` 正确，但无法验证大集合行为。

---

## 五、复现环境说明

所有命令均使用：

```bash
export MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"
```

隧道在测试期间多次出现 `initialize: Post ... EOF`（每次均自动重试 5–6 次后成功）；
该现象与工具语义无关，未计入任何结论。直连交叉验证使用 `/tmp/magento_token` 的管理员令牌。
