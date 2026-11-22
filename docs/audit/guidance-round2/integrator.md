# 照指引做：CSV 导入（imports prompt + import.start）

被测对象：生产部署 **v0.11.9**（`platform.info` 实测 `version: v0.11.9`，`writes_enabled: true`，实例 `demo`），
隧道 `http://127.0.0.1:18110/mcp`。店铺 demo Magento（store `pl`，PLN）。

只记录，不改代码。全部测试数据用 `R2-IMPORT-20261123-025710` 前缀，**已全部清理**（见文末）。

---

## 0. 先读文档（两份，互相矛盾）

### 0.1 `prompts/get imports`（全文 2,498 字符，摘关键句）

> - **Every CSV row needs `name`.** A "price only" file is rejected row by row: the importer writes
>   whole products. Read the product first and send the field back.
> - **`partial: true` is a real write.** Rows succeed or fail individually; a partial job changed
>   data and needs follow-up.
> - **`job.result` is a JSON string**, not an object: parse it before reading counts.
> - **`import.resume` is a new job** from the failed rows only.
> - **A repeated `import.start` is not a second import.** Same arguments return the original job;
>   change the file or the arguments to import again.
>
> ## Async lifecycle
>
> `start` → `status` (**queued, running, partial, failed, complete**) → `batches`/`batch` → `resume` or
> `cancel`. `execute` is the synchronous variant for a handful of rows.

### 0.2 `magento.import.start` 描述（1,000 字符，摘关键句）

> REQUIRED COLUMNS: **sku on every row; name and attribute_set_id on a row that creates a product
> (an update row may omit name)**. Other columns become custom attributes. **ONE INVALID ROW REJECTS
> THE PAYLOAD** before a job exists: validation_failed with **details.invalid_rows and details.rows
> naming every bad row**. Execution failures are **per-row, not retried**.

### 0.3 `magento.import.validate` 描述（450 字符，摘关键句）

> Structural check only: it does not accept `update_existing` and **does not run the same
> required-column rules** as magento.import.start, so a payload can validate here and still be
> rejected there.

**两份文档已经互相冲突**：prompt 说「每一行都需要 name，纯改价会被逐行拒绝」；`import.start` 说
「创建行才需要 name，更新行可以省略」。下面逐条实测谁对。

---

## 1. 场景：正常导入（创建行 + 更新行混合）

### 1.1 第一次：完全按 `import.start` 的「必需列」写，创建行失败

**文档**：创建行需要 `sku` + `name` + `attribute_set_id`。

**实测**（CSV 只有这三列）：

```csv
sku,name,price,attribute_set_id
R2-IMPORT-…-UPD,R2 Import Updated,11.50,4
R2-IMPORT-…-NEW,R2 Import New,22.00,4
```

```text
import.start → {"job_id":"52d74ced-3df5-40e7-adb0-112f0bb51b50","queued":true,"rows":2,"state":"queued"}
poll → state=partial
job.result = {"created":0,"failed":1,
  "failures":[{"action":"create",
    "error":"invalid_argument: type_id must be one of simple, virtual",
    "row_number":3,"sku":"R2-IMPORT-…-NEW"}],
  "partial":true,"skipped":0,"total":2,"updated":1}
```

- 更新行成功（`updated: 1`，价格 10.00 → 11.50）；
- **创建行失败**，原因是缺 `type_id` —— 而 `import.start` 的必需列清单里**没有 `type_id`**。

**判定**：**P1 文档缺口（必需列清单不完整）**。一行完全遵守文档写出的创建行，必然在执行期失败。
错误文本本身可读（`type_id must be one of simple, virtual`），但它出现在**异步执行期**（agent 已经
拿到 job_id、以为提交成功），而不是结构校验期。

### 1.2 第二次：补上 `type_id=simple` 后完全成功

```csv
sku,name,price,attribute_set_id,type_id
R2-IMPORT-…-UPD,R2 Import Updated,11.75,4,
R2-IMPORT-…-NEW,R2 Import New,22.00,4,simple
```

```text
job  c28a4441-7d00-48ad-981a-ae4ca4b4f69d
state = succeeded
job.result = {"created":1,"updated":1,"skipped":0,"failed":0,"failures_truncated":false,"total":2}
batch 9c7bb74f-… = {"state":"succeeded","total":2,"processed":2,"created":1,"updated":1,"failed":0}
products.get → R2-IMPORT-…-NEW price 22 · R2-IMPORT-…-UPD price 11.75
```

**判定**：正常路径可用，`import.status` 与 `import.batch` 的计数一致、与实际店铺状态一致。

---

## 2. 场景：故意坏一行（缺 sku）→ 整单拒绝

**文档**：`ONE INVALID ROW REJECTS THE PAYLOAD before a job exists: validation_failed with
details.invalid_rows and details.rows naming every bad row.`

**实测**（第 3 行 sku 为空）：

```csv
sku,name,price,attribute_set_id
R2-IMPORT-…-OK3,R2 Ok Three,5.00,4
,R2 Bad Row,6.00,4
```

```text
import.start → isError=true code=validation_failed
{"code":"validation_failed",
 "details":{"invalid_rows":"1","reason":"invalid or missing SKU","row":"3",
            "rows":"row 3: invalid or missing SKU"},
 "message":"import has invalid rows","retryable":false}
```

**没有 job 被创建**（响应里无 `job_id`），`details.rows` 点名了坏行。

两行同时坏时（第 2、3 行都缺 sku）：

```text
{"details":{"invalid_rows":"2","reason":"invalid or missing SKU","row":"2",
            "rows":"row 2: invalid or missing SKU; row 3: invalid or missing SKU"}}
```

**判定**：**规则成立**。「every bad row」确实是逐行列出（`invalid_rows=2` + 两条 `rows`）。
小瑕疵：`details.row` / `details.reason` 只给第一行，与 `rows` 的全量列表不对称（不影响可用性）。

`import.validate` 在同一份 payload 上先给出逐行错误（`row_number:3, field:"sku",
message:"invalid or missing SKU"`），与 `import.start` 结论一致 —— 这一格文档说的「validate 会先
报」是对的。

---

## 3. 场景：执行期失败（结构合法、执行会失败）→ 逐行报告

**文档**：`Execution failures are per-row, not retried.`

**实测**（第 3 行 `attribute_set_id: 9999`，结构上是合法整数，validate 不拦）：

```csv
sku,name,price,attribute_set_id,type_id
R2-IMPORT-…-EXEC-OK,R2 Exec Ok,7.00,4,simple
R2-IMPORT-…-EXEC-BAD,R2 Exec Bad,8.00,9999,simple
```

```text
job 59b04797-08ef-48cf-be35-89e1ae09fd11 → state=partial
job.result = {"created":1,"failed":1,"partial":true,"total":2,"updated":0,
  "failures":[{"action":"create",
    "error":"validation_failed: Magento rejected the request as invalid; verify store_code and field values (Nieprawidłowe dane produktu: (omitted))",
    "row_number":3,"sku":"R2-IMPORT-…-EXEC-BAD"}]}

import.batch b8a67555-… = {"state":"partial","total":2,"processed":2,"created":1,"failed":1,
  "failures":[ {…同上…} ]}

products.get → 只有 EXEC-OK（price 7）；EXEC-BAD 不存在
```

**判定**：**规则成立**。成功行真的生效（`created:1` + 店铺可查），失败行逐行报告，`partial:true`
诚实。`import.batch` 与 `job.result` 给出同一份失败明细。

**但错误文本不可编程（P2）**：上游原因被压成
`Nieprawidłowe dane produktu: (omitted)` —— 占位符被替换成 `(omitted)`，调用方**看不出是
`attribute_set_id` 非法**。行号 + SKU 有，但「改哪个字段」没有。

---

## 4. 场景：纯改价（只有 `sku,price`）—— 两份文档在这里正面冲突

**prompt 文档**：`Every CSV row needs name. A "price only" file is rejected row by row.`

**`import.start` 文档**：`an update row may omit name`。

**实测**（一行更新已有 SKU、一行创建新 SKU，都只有 `sku,price`）：

```csv
sku,price
R2-IMPORT-…-UPD,12.34
R2-IMPORT-…-PRICEONLY,9.99
```

```text
import.validate → valid:2 invalid:0，只有 warning：
  {"row_number":2,"field":"name","message":"missing name; required when this row creates a product, ignored for an update"}

import.start → 被接受（无 validation_failed），job 4a7a9fcc-b76e-4e9b-ac91-b71dfd528d8a
state=partial
job.result = {"created":0,"failed":1,"partial":true,"total":2,"updated":1,
  "failures":[{"action":"create","error":"validation_failed: name is required to create a product (field=name)",
               "row_number":3,"sku":"R2-IMPORT-…-PRICEONLY"}]}

products.get → R2-IMPORT-…-UPD price = 12.34   （更新行真的生效）
```

**判定**：**prompt 文档错，`import.start` 文档对**。

1. 纯改价**没有被拒绝**，更新行**成功生效**（10.00 → 11.75 → 12.34 可查）；
2. 失败的是**创建行**，而且是**执行期逐行失败**（`name is required to create a product`），
   不是 prompt 说的「rejected row by row」的校验期行为；
3. `import.validate` 的 warning 文案与 `import.start` 一致（"required when this row creates a
   product, ignored for an update"），进一步说明 prompt 那句是过时/错误的。

一个照 prompt 做事的 agent 会得出完全相反的结论：它会以为「纯改价必须先把 name 读回来塞进 CSV」，
于是多做一轮 `product.search`/`products.get`，还可能因为拿不到 name（例如商品被禁用/无权限）而放弃
一次本来可以成功的批量改价。

---

## 4b. 补测：创建行缺 `attribute_set_id` 也只在执行期失败

**文档**：创建行需要 `sku` + `name` + `attribute_set_id`。

**实测**（创建行只给 `sku,name,price,type_id`，没有 `attribute_set_id` 列）：

```csv
sku,name,price,type_id
R2-IMPORT-…-NOAS,R2 No AttrSet,5.00,simple
```

```text
import.start → 被接受，job ed72a39a-1be7-4373-8dc8-00e56006e757
state=partial
job.result = {"created":0,"failed":1,"partial":true,"total":1,
  "failures":[{"action":"create","error":"validation_failed: attribute_set_id is required to create a product",
               "row_number":2,"sku":"R2-IMPORT-…-NOAS"}]}
```

**判定**：与 `name`、`type_id` 同一类——**创建行的必需列（name / attribute_set_id / type_id）
全部只在执行期逐行失败，不在结构校验期**。也就是说 `import.start` 描述里
「ONE INVALID ROW REJECTS THE PAYLOAD before a job exists」这句话，对 `sku` 成立，对另外三个
「必需列」不成立：调用方已经拿到 `job_id`，之后才发现整份 payload 里没有一行能建成。

## 5. 三条「必需列规则」逐条判定

| # | 文档说法（来源） | 实测 | 判定 |
|---|---|---|---|
| 1 | `sku` 每行必需（`import.start`） | 缺 sku → `validation_failed` + `details.rows` 点名，无 job | **成立** |
| 2 | 创建行还需 `name` 与 `attribute_set_id`（`import.start`） | 缺 name → 执行期 `name is required to create a product`；缺 attribute_set_id → 执行期 `attribute_set_id is required to create a product`；缺 type_id → 执行期 `type_id must be one of simple, virtual`。三者都**不**在结构校验期拦 | **部分成立**：清单**漏了 `type_id`**，且这三个「必需列」实际是执行期检查，不是「整单拒绝」 |
| 3 | 一行不合法整单拒绝、`details` 列出每行（`import.start`） | 结构坏行（缺 sku）→ 整单拒绝 + `invalid_rows` + 逐行 `rows`；结构合法但执行失败 → 不整单拒绝，逐行失败 + `partial:true` | **成立**，且分界正确（见 §6） |
| 4 | 每行都需要 name，纯改价逐行拒绝（**`imports` prompt**） | 纯改价被接受，更新行成功 | **不成立（文档错误）** |

---

## 6. 「整单拒绝」与「逐行失败」的分界是否清楚

**`import.start` 的描述把分界写清楚了**：结构校验（`before a job exists`）→ 整单拒绝；执行期
（`Execution failures are per-row`）→ 逐行。两条我都实测到了，与描述一致。

**但「必需列」正好横跨这条分界**，描述没有说明：`sku` 在提交前拦（整单拒绝），
`name`/`attribute_set_id`/`type_id` 在执行期拦（逐行失败、`partial:true`）。也就是说
「REQUIRED COLUMNS」这个标题下面混了两种语义，agent 无法据此预判「我这份文件会被整单退回，还是
会先排队再部分失败」。

**但 `imports` prompt 没写这条分界**，还给了相反的描述（"rejected row by row"）。一个只读 prompt
（不支持工具描述裁剪的客户端、或按 prompt 决策的 agent）会预期「校验期逐行拒绝」，实际拿到的是
「先排队、再在执行期部分失败」。两者的**后续动作不同**：
- 整单拒绝：改文件重发，不会产生任何写入；
- 逐行失败：**已经写入了成功行**（`partial:true`），必须对账 + `resume`。

建议：prompt 里补一句与工具描述一致的分界（「结构错 → 整单拒绝、无 job；执行错 → 逐行失败、
`partial:true` 是真写入」），并删掉 "Every CSV row needs name / rejected row by row"。

---

## 7. `import.status` / `import.batch` 是否足以定位坏行

**够用**，但有一个状态词表不一致会让 agent 卡住：

```text
job.result（JSON 字符串，需二次解析）:
  {"total":2,"created":1,"failed":1,"failures":[{"row_number":3,"sku":"…","error":"…"}], …}
import.batch（对象，无需解析）:
  {"state":"partial","total":2,"processed":2,"created":1,"failed":1,"failures":[{…}]}
```

行号 + SKU + 错误文本三者齐全，`import.batch` 与 `job.result` 内容一致，可以定位到行。

**偏差（P2）**：`imports` prompt 的生命周期写的是
`queued, running, partial, failed, complete`，**实际终态是 `succeeded`**（代码里
`jobs.StateSucceeded = "succeeded"`，另有 `canceled`）。我自己的轮询脚本按 prompt 等 `complete`，
结果在 `succeeded` 上空转了 30 次（实测：`poll 0..30: state=succeeded`）。一个照 prompt 写轮询的
agent 会**永远等不到终态**。

---

## 8. 其他实测到的小偏差

| 项 | 实测 | 判定 |
|---|---|---|
| 重放同一 `idempotency_key` | 返回同一 `job_id` + `"replayed": true` | 成立；但响应仍带 `"queued": true, "state": "queued"`（任务早已结束），与 U-67 同类，属旧的「重放仍报 queued」问题 |
| `import.resume` | 新 job `92565268-…`，`rows: 1`（只含失败行），再次以 `name is required` 失败 | 成立（"a new job from the failed rows only"） |
| resume 后的 `row_number` | 仍为原 CSV 的第 3 行（`total:1` 但 `row_number:3`） | 可接受（指回原文件），但 `total` 与 `row_number` 并列时容易误读 |
| `job.result` 类型 | 是 JSON **字符串**，需二次解析 | 成立（prompt 说对了） |
| `import.validate` 与 `import.start` 口径 | validate 对纯改价 `valid:2 invalid:0`，start 也接受；两者一致（validate 的 warning 也写明 name 仅创建行必需） | 成立 |

---

## 9. 结论（按严重度）

1. **P1｜`import.start` 的「必需列」与实际校验时机不一致**，两层问题：
   (a) 清单**漏了 `type_id`** —— 完全按文档写的创建行必然在执行期失败
   （`type_id must be one of simple, virtual`）；
   (b) 清单里的 `name` / `attribute_set_id` 同样**不在结构校验期**检查，只在执行期逐行失败 ——
   于是「ONE INVALID ROW REJECTS THE PAYLOAD before a job exists」只对 `sku` 成立，对另外三个
   「必需列」不成立：agent 先拿到 `job_id`，之后才发现整份 payload 建不出任何商品。
   修法二选一：把这四列都做成结构校验期检查（与 `sku` 一致，整单拒绝），或在描述里写明
   「只有 sku 是提交前校验；name/attribute_set_id/type_id 在执行期逐行失败」，并补上 `type_id`。
2. **P1｜`imports` prompt 与 `import.start` 正面冲突**：prompt 说「每行都要 name、纯改价逐行拒绝」，
   实测纯改价被接受且更新行成功。照 prompt 做事会多做一轮无谓读取、甚至放弃可行任务。修法：删掉
   那两句，改成与工具描述一致的「name 仅创建行必需」。
3. **P2｜状态词表不一致**：prompt 写 `complete`，实际是 `succeeded`；按 prompt 轮询永远不结束。
4. **P2｜执行期失败的文本不可编程**：`attribute_set_id` 非法只得到
   `Nieprawidłowe dane produktu: (omitted)`，看不出是哪个字段。
5. **P3｜`details.row`/`details.reason` 只给第一行**（`rows` 是全量），以及重放仍报 `queued:true`。

**做对的部分**（值得保留）：结构错整单拒绝 + `details.rows` 逐行点名、执行期逐行失败 + `partial:true`
诚实、`import.batch` 与 `job.result` 明细一致、`resume` 只重跑失败行、`validate` 提前给逐行错误。

---

## 10. 数据卫生

- 前缀 `R2-IMPORT-20261123-025710`。
- 创建并删除的商品：`…-UPD`、`…-NEW`、`…-EXEC-OK`（`product.delete` 均返回 `{"deleted": true}`；
  删除后 `products.get` 返回空、`product.search query="R2 Import"` 返回 `total: 0`）。
- `…-PRICEONLY` 从未创建成功（执行期失败），无需清理。
- 未修改任何代码；本文是唯一新增文件。