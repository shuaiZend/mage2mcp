# 语义层测试 D：客户、地址与导入任务（customers-import）

- 环境：生产 v0.11.5，网关 `http://127.0.0.1:18110/mcp`（SSH 隧道），实例 `demo`，店铺 `pl`
- 方法：只走 MCP 工具（`test/live -mode=raw`，写入经 `approve.sh` 审批），并用管理员令牌直接调用
  Magento REST 作为对照。只记录，不修改任何代码。
- 时间：2026-11-22 01:30–01:55 HKT

## 结论摘要

| 编号 | 级别 | 工具 | 一句话 | 状态 |
|---|---|---|---|---|
| SEM-CUS-01 | P2 | `magento.customer.addresses.create` | 回退路径静默丢弃 `region`/`region_id`，且响应把请求的 region 当作已写入返回 | CONFIRMED |
| SEM-CUS-02 | P2 | `magento.customer.addresses.update` | 只传 `region_id` 被拒绝（schema 允许），且 `region_id` 从不写入 | CONFIRMED |
| SEM-CUS-03 | P3 | `magento.customer.addresses.update` | 响应缺少 store 中实际存在的 `region`/`region_id`，与 list 不一致 | CONFIRMED |
| SEM-CUS-04 | P1 | `magento.customer.addresses.delete` | 报告 `deleted:true` 但地址并未删除；重复删除仍返回成功 | CONFIRMED |
| SEM-CUS-05 | — | `magento.customer.update` | 改 email 被店铺自身缺陷拒绝；网关错误信息丢失上游原因 | NOT-A-DEFECT（附 P3 诊断项） |
| SEM-CUS-06 | — | `customer.get/search/addresses` | PII 投影符合声明，未泄露 email/姓名/地址/密码 | NOT-A-DEFECT |
| SEM-IMP-01 | P2 | `magento.import.status` | 部分失败（1/2 失败）时 job state 仍为 `succeeded`，batch 才是 `partial` | CONFIRMED |
| SEM-IMP-02 | P2 | `magento.import.batch` | 输入描述称 `batch_id` 就是 job id，实际传 job id 返回 `not_found` | CONFIRMED |
| SEM-IMP-03 | P3 | `magento.import.start/resume` | `rows` 声明为「Per-row results」，实际是行数（整数） | CONFIRMED |
| SEM-IMP-04 | P3 | `magento.import.cancel` | 对已 canceled 的 job 再次 cancel 返回成功，声明说终态应返回 `illegal_transition` | CONFIRMED |
| SEM-IMP-05 | — | `import.validate/execute` | status/visibility 列生效；update_existing 跳过/更新判定正确；无重复创建 | NOT-A-DEFECT |
| SEM-IMP-06 | — | `import.batch/batches/status` | 失败行记录完整；batch↔job 对应关系正确 | NOT-A-DEFECT |

---

## SEM-CUS-01：地址创建回退路径静默丢弃 region / region_id，响应却声称已写入

- **严重级别**: P2
- **工具**: `magento.customer.addresses.create`
- **现象**: 该店不暴露 `POST /customers/{id}/addresses`，工具走 `PUT /customers/{id}` 回退。回退
  把新地址追加进 customer 的 `addresses` 数组时只带了 `"region": <字符串>`，**没有带 `region_id`**。
  对 PL 这种有 region directory 的国家，Magento 只认 `region_id`，于是实际存储为 `region_id: 0`、
  `region: null`。更糟的是：用 `region` 字符串创建时，工具响应把请求里的 region 原样回显
  （`"region": "mazowieckie"`），让调用方以为已写入。
- **期望**: schema 明确说 `region_id` 是「Numeric region (state/province) id. Magento requires it for
  countries that have a region directory (Poland among them)」；描述承诺「adds an address ... the
  response is the PII-safe projection」。传了 `region_id: 1003` 就应写入 1003；写不进去应报错而不是静默丢弃。
- **证据**:

  复现命令（README 形式）：

  ```bash
  MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
  go run -tags live ./test/live -mode=raw -tool=magento.customer.addresses.create \
    -args='{"instance":"demo","customer_id":2,"firstname":"Sem","lastname":"Tester","street":["ul. Regionowa 2"],"city":"Warszawa","postcode":"00-002","country_id":"PL","telephone":"500100201","region_id":1003}'
  ```

  实际输出：

  ```text
  is_error=false code=
  {
    "address": {
      "id": 3,
      "country_id": "PL",
      "default_billing": false,
      "default_shipping": false
    }
  }
  ```

  直接读 Magento（管理员令牌）确认实际存储：

  ```bash
  curl -g -sS -H "Authorization: Bearer $TOKEN" \
    'https://demo-magento-2.auroracreation.com/rest/V1/customers/2'
  ```

  ```text
  {'id': 3, 'street': ['ul. Regionowa 2'], 'city': 'Warszawa', 'postcode': '00-002',
   'country_id': 'PL', 'region': {'region_code': None, 'region': None, 'region_id': 0},
   'region_id': 0, 'telephone': '500100201'}
  ```

  同一请求直接 PUT 则正确写入（证明是网关回退的问题，不是店铺不接受）：

  ```bash
  curl -g -sS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d '{"customer":{"id":2,"email":"...","firstname":"Sem","lastname":"Tester","website_id":1,
         "addresses":[{"firstname":"Sem","lastname":"Tester","street":["ul. Direct 4"],
         "city":"Krakow","postcode":"30-001","country_id":"PL","telephone":"500100202",
         "region_id":1003}]}}' \
    'https://demo-magento-2.auroracreation.com/rest/pl/V1/customers/2'
  ```

  ```text
  HTTP 200
  {'id': 4, 'city': 'Krakow', 'region': {'region_code': 'PL-14', 'region': 'mazowieckie',
   'region_id': 1003}, 'region_id': 1003}
  ```

  用 `region` 字符串（`"mazowieckie"`）创建时，工具响应回显：

  ```text
  {"address": {"id": 2, "country_id": "PL", "region": "mazowieckie",
   "default_billing": false, "default_shipping": false}}
  ```

  但直接读回仍是 `region_id: 0, region: null`。
- **影响**: 客户地址缺少省/州。对 PL 这类国家，运费/税率计算依赖 region；调用方拿到「成功」且
  响应里还有 region 名，不会知道数据丢了。属于「静默丢弃 + 响应与存储不一致」。
- **状态**: CONFIRMED（两次独立创建、直接 API 对照、以及同请求直接 PUT 成功三方印证）

---

## SEM-CUS-02：地址更新只传 region_id 被拒绝，且 region_id 从不写入

- **严重级别**: P2
- **工具**: `magento.customer.addresses.update`
- **现象**: schema 允许 `region_id`，但只传 `region_id` 时返回
  `invalid_argument: at least one field must be provided to update`；代码里
  `UpdateCustomerAddress` 的「至少一个字段」判断没有包含 `RegionID`，payload 也从未赋值
  `RegionID`，回退的 `applyAddressUpdate` 同样只处理 `region` 字符串。
- **期望**: 声明「partially updates a customer address ... Unset fields are omitted」，且 schema 列出
  `region_id`；只改 region_id 应当被接受并写入。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.customer.addresses.update \
    -args='{"instance":"demo","customer_id":2,"address_id":4,"region_id":1003}'
  ```

  ```text
  is_error=true code=invalid_argument
  {
    "code": "invalid_argument",
    "message": "at least one field must be provided to update"
  }
  text: invalid_argument: at least one field must be provided to update
  ```

  同一地址只改 city 则成功，但响应同样不含 region/region_id（见 SEM-CUS-03）；store 中 region_id 仍为 1003。
- **影响**: 调用方无法通过工具修正 region；只能重建地址（而创建又会丢 region，见 SEM-CUS-01）。
- **状态**: CONFIRMED

---

## SEM-CUS-03：地址更新响应缺少 store 中实际存在的 region/region_id

- **严重级别**: P3
- **工具**: `magento.customer.addresses.update`
- **现象**: 地址 4 在 store 中有 `region: mazowieckie, region_id: 1003`（list 工具与直接 API 都能读到），
  但 update 的响应只有 id/country_id/default 标志，没有 region/region_id。回退读取用
  `stringField(candidate, "region")`，而 Magento 返回的 `region` 是对象（`{region_code, region, region_id}`），
  类型断言失败后变成空串；`region_id` 根本没有赋值。
- **期望**: 输出 schema 的 address 投影包含 `region`/`region_id`；list 与 update 对同一地址应一致。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.customer.addresses.update \
    -args='{"instance":"demo","customer_id":2,"address_id":4,"city":"Gdansk"}'
  ```

  ```text
  is_error=false code=
  {"address": {"id": 4, "country_id": "PL", "default_billing": false, "default_shipping": false}}
  ```

  紧接着 list：

  ```text
  {"addresses": [{"id": 4, "country_id": "PL", "region": "mazowieckie", "region_id": 1003,
    "default_billing": false, "default_shipping": false}]}
  ```

  直接 API：`{'id': 4, 'city': 'Gdansk', 'region': {...region_id: 1003}, 'region_id': 1003}`。
- **影响**: 调用方在 update 后无法确认 region 是否保留，可能重复写入或误判。
- **状态**: CONFIRMED

---

## SEM-CUS-04：地址删除报告成功但实际未删除

- **严重级别**: P1
- **工具**: `magento.customer.addresses.delete`
- **现象**: 工具返回 `{"address_id": N, "deleted": true}`，但地址仍在（list 与直接 API 都能读到）。
  对同一个 id 重复删除仍返回成功；对从未存在的 id（如 99999）才返回
  `unsupported_capability`（upstream_status 404）。从生产机用同一管理员凭证直接
  `DELETE /rest/pl/V1/addresses/{id}` 可以真正删除，说明店铺路由正常，问题在网关的删除路径。
- **期望**: 声明「deletes a customer address ... Irreversible」「a retry on a deleted address returns
  not_found」。实际既没删，也没有在重试时返回 not_found。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.customer.addresses.delete \
    -args='{"instance":"demo","address_id":7}'
  ```

  ```text
  is_error=false code=
  { "address_id": 7, "deleted": true }
  ```

  立即直接读：

  ```text
  before delete: [{'id': 6, 'city': 'Poznan'}, {'id': 7, 'city': 'Wroclaw'}]
  after  delete: [{'id': 6, 'city': 'Poznan'}, {'id': 7, 'city': 'Wroclaw'}]
  ```

  再次删除同一个 id：

  ```text
  is_error=false code=
  { "address_id": 7, "deleted": true }
  ```

  从生产机直接删除（同一管理员 token）：

  ```bash
  ssh ubuntu@49.51.250.83 'TOKEN=$(curl -sS -X POST .../rest/V1/integration/admin/token ...); \
    curl -sS -X DELETE -H "Authorization: Bearer $TOKEN" \
      https://demo-magento-2.auroracreation.com/rest/pl/V1/addresses/7'
  ```

  ```text
  true
  HTTP 200
  # 随后 customers/2 的 addresses = [6]  —— 确实删除
  ```

  对照：不存在的 id 返回 `unsupported_capability`（details.upstream_status=404），说明请求确实到达
  Magento 并拿到了 404；存在 id 的「成功」却没有产生删除。
- **影响**: 破坏性操作谎报成功；调用方/审批人会以为地址已删除（隐私合规场景下尤其严重），
  而数据仍在。重复删除也不报错，掩盖了问题。
- **状态**: CONFIRMED（多次复现，含新 id 7 的首次执行；排除了幂等重放：新 id 的首次调用也如此）

---

## SEM-CUS-05：改 email 被店铺自身缺陷拒绝；网关丢失上游原因

- **严重级别**: —（网关侧诊断问题 P3）
- **工具**: `magento.customer.update`
- **现象**: 只传 `email` 返回 `validation_failed`，details 只有 `store_code`/`upstream_status`，
  没有 Magento 的具体信息。直接 PUT（同一 token）返回 400
  `{"message":"Something went wrong while logging out customer."}`；只改 firstname 的 PUT 返回 200。
  因此是 demo 店铺的 email 变更流程坏了（logout 插件），不是网关拒绝。
- **期望**: 网关无法修复店铺缺陷，但声明「updates customer email」在该店铺实际不可用；错误信息
  「verify store_code and field values」会误导调用方。建议把上游 message 放进 details（与 SEC-10
  声称的「bounded upstream text」一致）。
- **证据**:

  ```text
  $ ... -tool=magento.customer.update -args='{"instance":"demo","customer_id":2,"email":"..."}'
  is_error=true code=validation_failed
  {"code":"validation_failed","details":{"store_code":"pl","upstream_status":"400"},
   "message":"Magento rejected the request as invalid; verify store_code and field values",
   "op":"magento PUT /customers/{id}"}

  $ curl -X PUT .../rest/pl/V1/customers/2 -d '{"customer":{"email":"..."}}'
  {"message":"Something went wrong while logging out customer."}
  HTTP 400
  ```

- **影响**: 调用方无法从错误里知道真正原因；会去检查 store_code/字段，浪费排查时间。
- **状态**: NOT-A-DEFECT（email 变更失败是店铺缺陷）；附 P3 诊断项（丢失上游 message）

---

## SEM-CUS-06：客户读取的 PII 投影符合声明

- **严重级别**: —
- **工具**: `magento.customer.get` / `magento.customer.search` / `magento.customer.addresses`
- **现象**: get 只返回 id/group/website/store/created_in/时间戳；search 不回显 email/姓名/地址；
  addresses 只返回 id/country/region/region_id/default 标志。直接 API 的 customer 对象含
  email/firstname/lastname/addresses，均未出现在工具输出中。
- **期望**: 与声明一致（「PII-safe: omits email, name, and addresses」）。
- **证据**:

  ```text
  $ ... -tool=magento.customer.get -args='{"instance":"demo","customer_id":2}'
  {"customer":{"id":2,"group_id":1,"website_id":1,"store_id":1,"created_in":"PL",
   "created_at":"2026-11-21 17:32:42","updated_at":"2026-11-21 17:32:42"}}

  $ ... -tool=magento.customer.search -args='{"instance":"demo","email":"sem-t56-...@example.com"}'
  {"customers":[{"id":2,"group_id":1,...}],"total":1}   # 无 email 回显

  $ ... -tool=magento.customer.addresses -args='{"instance":"demo","customer_id":1}'
  {"addresses":[{"id":1,"country_id":"US","region":"Michigan","region_id":33,
   "default_billing":true,"default_shipping":true}]}    # 无 name/street/city/postcode/telephone
  ```

- **影响**: 无（正面验证）。
- **状态**: NOT-A-DEFECT

---

## SEM-IMP-01：部分失败的导入 job state 仍为 succeeded

- **严重级别**: P2
- **工具**: `magento.import.status`
- **现象**: 2 行导入，1 行成功、1 行失败（attribute_set_id 9999）。`import.status` 返回
  `state: "succeeded"`，但 result 里 `failed:1`，且 `import.batches` 中对应 batch 的
  `state: "partial"`。输出 schema 明确说 job 的 state 可为
  「queued, running, succeeded, partial, failed or cancelled」。
- **期望**: 有失败行时 job state 应为 `partial`（与 batch 一致），否则轮询 status 的调用方会把
  部分失败当成全部成功。
- **证据**:

  ```text
  $ ... -tool=magento.import.status -args='{"instance":"demo","job_id":"eb240023-..."}'
  {"job":{"id":"eb240023-...","state":"succeeded","attempts":1,"max_attempts":2,
   "result":"{\"total\":2,\"created\":1,\"failed\":1,\"failures\":[...]}"}}

  $ ... -tool=magento.import.batches -args='{"instance":"demo","limit":5}'
  {"batches":[{"id":"c284c55a-...","job_id":"eb240023-...","state":"partial",
   "total":2,"processed":2,"created":1,"failed":1}, ...]}
  ```

- **影响**: 只读 status.state 的自动化会把部分失败当成功，漏掉失败行与 resume。
- **状态**: CONFIRMED

---

## SEM-IMP-02：import.batch 的 batch_id 描述错误（说成 job id）

- **严重级别**: P2
- **工具**: `magento.import.batch`
- **现象**: 输入 schema 的 `batch_id` 描述是「Import batch id, which is the job id that produced it」，
  但传 job id 返回 `not_found`。真正的 batch id 是另一个 UUID，需从 `import.batches[].id` 获取。
  输出 schema 又把 `job_id` 描述为「used by import.status and import.batch」，同样不成立。
- **期望**: 描述应说明 `batch_id` 是 `import.batches` 返回的 `id`，或工具应接受 job id 并自行解析。
- **证据**:

  ```text
  $ ... -tool=magento.import.batch -args='{"instance":"demo","batch_id":"eb240023-858d-4d99-906b-b2a6b93e0813"}'
  is_error=true code=not_found
  {"code":"not_found","message":"import batch not found"}

  $ ... -tool=magento.import.batches -args='{"instance":"demo","limit":5}'
  {"batches":[{"id":"c284c55a-8ccf-4240-b5f0-474f65bd46d5",
   "job_id":"eb240023-858d-4d99-906b-b2a6b93e0813","state":"partial",...}]}

  $ ... -tool=magento.import.batch -args='{"instance":"demo","batch_id":"c284c55a-8ccf-4240-b5f0-474f65bd46d5"}'
  {"batch":{"id":"c284c55a-...","job_id":"eb240023-...","state":"partial","failed":1,
   "failures":[{"row_number":3,"sku":"SEM-T56-014819-B","action":"create",
   "error":"validation_failed: ..."}]}}
  ```

- **影响**: 按文档传参必失败；调用方需先猜/查 batches 才能拿到 id。
- **状态**: CONFIRMED

---

## SEM-IMP-03：import.start / import.resume 的 rows 字段是行数，不是「per-row results」

- **严重级别**: P3
- **工具**: `magento.import.start` / `magento.import.resume`
- **现象**: 输出 schema 对 `rows` 的描述是「Per-row results, in input order」，实际值是整数
  （`2`、`1`、`300`）。
- **期望**: 描述应与实际一致（行数），或返回真正的逐行结果数组。
- **证据**:

  ```text
  $ ... -tool=magento.import.start -args='{"instance":"demo","format":"csv","data_base64":"..."}'
  {"job_id":"eb240023-...","rows":2,"queued":true}

  $ ... -tool=magento.import.resume -args='{"instance":"demo","job_id":"eb240023-..."}'
  {"job_id":"cbee3187-...","rows":1}
  ```

- **影响**: 依赖 schema 生成客户端的调用方会按数组解析，得到类型错误或误解。
- **状态**: CONFIRMED

---

## SEM-IMP-04：对已 canceled 的 job 再次 cancel 返回成功

- **严重级别**: P3
- **工具**: `magento.import.cancel`
- **现象**: 对 succeeded 的 job cancel → `illegal_transition`（符合声明）；但对 canceled 的 job
  再次 cancel → `{"canceled": true}` 成功。声明说「not idempotent; a retry after the job reaches a
  terminal state returns illegal_transition」。
- **期望**: 两种终态应一致（都返回 illegal_transition，或声明改为「canceled 幂等」）。
- **证据**:

  ```text
  $ ... -tool=magento.import.cancel -args='{"instance":"demo","job_id":"f936d5bb-..."}'   # 运行中
  {"job_id":"f936d5bb-...","canceled":true}
  $ ... -tool=magento.import.cancel -args='{"instance":"demo","job_id":"f936d5bb-..."}'   # 已 canceled
  {"canceled":true,"job_id":"f936d5bb-..."}
  $ ... -tool=magento.import.cancel -args='{"instance":"demo","job_id":"eb240023-..."}'   # 已 succeeded
  is_error=true code=illegal_transition
  {"code":"illegal_transition","details":{"job_id":"eb240023-...","state":"succeeded"},
   "message":"job is already in a terminal state"}
  ```

- **影响**: 调用方无法用错误码区分「取消成功」与「本来就是 canceled」。
- **状态**: CONFIRMED

---

## SEM-IMP-05：status/visibility 列生效；update_existing 判定正确；无重复创建（正面验证）

- **严重级别**: —
- **工具**: `magento.import.validate` / `magento.import.execute`
- **现象与证据**:

  - status/visibility 列生效（此缺陷已修，验证通过）：

  ```text
  # 创建：status=2, visibility=2
  $ ... -tool=magento.import.execute -args='{"instance":"demo","format":"csv","data_base64":"..."}'
  {"result":{"total":1,"created":1,"updated":0,"skipped":0,"failed":0,
   "rows":[{"row_number":2,"sku":"SEM-T56-014627-1","action":"create","status":"succeeded"}]}}

  $ ... -tool=magento.product.get -args='{"instance":"demo","sku":"SEM-T56-014627-1"}'
  {"product":{"id":2060,"status":2,"visibility":2,"price":12.34,...}}

  # 更新：status=1, visibility=4
  $ ... -tool=magento.import.execute -args='{...,"update_existing":true}'
  {"result":{"updated":1,"rows":[{"row_number":2,"action":"update","status":"succeeded"}]}}
  $ ... -tool=magento.product.get ...
  {"product":{"id":2060,"status":1,"visibility":4,"price":99.99,"name":"Import One Renamed",...}}
  ```

  - `update_existing=false` 跳过、`true` 更新，且不重复创建：

  ```text
  $ ... -tool=magento.import.execute -args='{...,"update_existing":false}'
  {"result":{"created":0,"updated":0,"skipped":1,"failed":0,
   "rows":[{"row_number":2,"sku":"SEM-T56-014627-1","action":"exists","status":"skipped"}]}}
  # 产品仍是 id 2060、原 name/price/status/visibility，未被修改
  ```

  - 校验按行报告，非法行不执行：

  ```text
  $ ... -tool=magento.import.validate -args='{...}'
  {"preview":{"total":2,"valid":1,"invalid":1,
   "errors":[{"row_number":3,"field":"price","message":"price must be a non-negative decimal with at most 4 decimals"}],
   "warnings":[{"row_number":2,"field":"description","message":"..."}]}}

  $ ... -tool=magento.import.execute -args='{...含非法行...}'
  is_error=true code=validation_failed
  {"code":"validation_failed","details":{"invalid_rows":"1"},
   "message":"import has invalid rows; fix them before executing"}
  ```

- **影响**: 无（正面验证）。
- **状态**: NOT-A-DEFECT

---

## SEM-IMP-06：失败行记录完整；batch↔job 对应关系正确（正面验证）

- **严重级别**: —
- **工具**: `magento.import.status` / `magento.import.batch` / `magento.import.batches` /
  `magento.import.resume` / `magento.import.cancel`
- **现象与证据**:

  - 失败行记录完整（row_number/sku/action/error），job result 与 batch 都有：

  ```text
  $ ... -tool=magento.import.batch -args='{"instance":"demo","batch_id":"c284c55a-..."}'
  {"batch":{"state":"partial","total":2,"processed":2,"created":1,"failed":1,
   "failures":[{"row_number":3,"sku":"SEM-T56-014819-B","action":"create",
   "error":"validation_failed: Magento rejected the request as invalid; verify store_code and field values"}]}}
  ```

  - batch 的 `job_id` 与 `import.status` 的 job id 一致；batches 列表按新→旧返回。

  - resume 语义：有失败行 → 新 job（只含失败行，rows=1），重复 resume 返回同一个新 job；
     无失败行 → `invalid_argument: job has no failed rows to resume`：

  ```text
  $ ... -tool=magento.import.resume -args='{"instance":"demo","job_id":"eb240023-..."}'
  {"job_id":"cbee3187-90a4-45c5-8253-6cc306b14298","rows":1}
  $ ... -tool=magento.import.resume -args='{"instance":"demo","job_id":"eb240023-..."}'   # 再次
  {"job_id":"cbee3187-90a4-45c5-8253-6cc306b14298","rows":1}                              # 同一个

  $ ... -tool=magento.import.resume -args='{"instance":"demo","job_id":"99f9ca2c-..."}'   # 无失败行
  is_error=true code=invalid_argument
  {"code":"invalid_argument","message":"job has no failed rows to resume"}
  ```

  - cancel 运行中的 job → canceled，status 反映 `state:"canceled"`、`error_code:"canceled"`、
     `cancel_requested:true`；取消后的进度 result 为 `{"progress":{"done":0,"total":300}}`。

- **影响**: 无（正面验证）。
- **状态**: NOT-A-DEFECT

---

## 附：环境说明

- demo 店铺会周期性重置；测试中自建的 customer 2、address 2–7、product 2060 可能随时消失。
- 该店不暴露 `POST|PUT /customers/{id}/addresses`（README 已知限制）。本报告考核的是回退路径的
  **行为正确性**，发现 SEM-CUS-01/02/03 正是回退路径的语义缺陷。
- `addresses.delete` 的问题与回退无关：店铺的 `DELETE /rest/pl/V1/addresses/{id}` 从生产机直接调用
  可以删除，说明是网关侧删除路径的问题（SEM-CUS-04）。
- 测试期间隧道曾多次断开并重启；断线时的失败（如 `initialize: ... EOF`）已排除，不计入发现。
