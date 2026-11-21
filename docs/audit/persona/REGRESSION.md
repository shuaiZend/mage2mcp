# 修复后回归测试（v0.11.6 + persona 修复集）

我的任务：在修复落地后，用**完整门禁 + 全量 live 矩阵 + 真实调用抽查**回答一个问题——
「这些修复有没有把别的东西弄坏」。结论先给：**门禁（代码侧）全绿、live 矩阵与 v0.11.6 基线逐用例一致、
抽查的 6 条修复全部按预期生效；唯一红灯是 v0.11.6 起就存在的文档契约检查，不是本次回归引入。**

本文件只记录回归过程与证据，不改产品代码。

## 0. 快照与测试实例

| 项目 | 值 |
|---|---|
| 被测代码 | 工作树 HEAD `5d274eb`（`test(qa): assert the retryable value, not the key's absence`）+ 构建时在场的未提交改动 |
| 门禁快照 | 最终门禁在 HEAD `c8593cf` + 1 个未提交文件上复跑（§1 全部结论取自该快照）；`c8593cf` 与构建快照之间只有测试/文档改动 |
| 二进制 | `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X main.version=v0.11.6+persona"`，`sha256:14da18f250ea40be…` |
| 构建时间 | 2026-11-22 04:04:39 HKT |
| 部署 | 测试主机 `49.51.250.83` 上的**独立第二实例**：容器 `mage2mcp-regress-server`（127.0.0.1:8081）+ `mage2mcp-regress-worker`，配置与生产同形（`auth.enabled=false`、`tools.writes=true`、demo/pl、同一 demo 店铺），**独立数据库** `mage2mcp_regress`（复用现有 postgres 容器） |
| 生产实例 | `mage2mcp-server-1` / `mage2mcp-worker-1`（v0.11.6 镜像）**全程未动**，仍 healthy |
| 客户端 | SSH 隧道 `127.0.0.1:18082 → 8081`；审批 `sudo /opt/mage2mcp/regress/approve.sh {id}`（同一份构建的 admin 二进制，`-as live-reviewer`） |
| 基线 | `test/live/out/results-20261122-025615.json`（生产 v0.11.6，2026-11-22 02:56:15，123 用例） |

为什么不直接换掉生产容器：生产是 7 个岗位共用的测试端点，替换镜像会中断别人的工作，而且回滚
需要重建镜像。用「同主机 + 同配置 + 独立库」的第二实例，既能跑真实 demo 店铺，又不影响生产容器；
矩阵结果只取决于二进制与上游，不取决于端口。

复现：

```bash
# 构建
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -X main.version=v0.11.6+persona" -o /tmp/regress/mage2mcp-server ./cmd/server
# 部署（在测试主机上；/opt/mage2mcp/regress/{config.yaml,approve.sh} 已就位）
sudo docker run -d --name mage2mcp-regress-server --network mage2mcp_default \
  -p 127.0.0.1:8081:8081 --no-healthcheck \
  -v /opt/mage2mcp/regress/config.yaml:/etc/mage2mcp/config.yaml:ro \
  -v /opt/mage2mcp/regress:/regress:ro -e MAGE2MCP_DEV_INSECURE=true \
  --entrypoint /regress/mage2mcp-server ghcr.io/shuaizend/mage2mcp:v0.11.6 -config /etc/mage2mcp/config.yaml
# 跑矩阵
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18082/mcp MCP_INSTANCE=demo \
  MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/regress/approve.sh {id}'" \
  go run -tags live ./test/live -mode=all -out test/live/out/results-regress3-<ts>.json
```

> 容器健康检查：镜像自带的 healthcheck 调的是镜像内 `/usr/local/bin/mage2mcp-server`（不是我挂载的新
> 二进制），所以用 `--no-healthcheck` 启动；实例本身用 `/healthz` 验证（200）。

## 1. 门禁结果

| # | 门禁 | 命令 | 结果 |
|---|---|---|---|
| 1 | gofmt | `gofmt -l $(git ls-files '*.go')` | **PASS**（输出为空） |
| 2 | go vet | `go vet ./...` | **PASS**（exit 0） |
| 3 | 单元/集成测试 | `go test -count=1 ./...` | **PASS**（exit 0，全部 `ok`，无 skip） |
| 4 | 竞态 | `go test -count=1 -race ./...` | **PASS**（exit 0，23 个包 `ok`，无 DATA RACE） |
| 5 | 独立 QA 套件 | `MAGE2MCP_TEST_DATABASE_URL=… ./scripts/qa-independent.sh` | **PASS**（18 passed / 0 skipped / 0 failed） |
| 6 | markdownlint | `node_modules/.bin/markdownlint-cli2 "**/*.md" "#node_modules"` | **PASS**（147 files，0 issues） |
| 7 | 链接检查 | `find … markdown-link-check -q -c .markdown-link-check.json` | **PASS**（exit 0，0 broken） |
| 8 | 文档契约 | `node scripts/check-contracts.mjs` | **FAIL（5）——v0.11.6 起就红，非本次回归** |

### 1.1 QA 套件：先红后绿，红的是断言不是行为

第一次跑 QA 是**红**的，三条断言报 `ambiguous money outcome must not be marked retryable`。
我没有直接当成回归，先探了实际响应：

```text
code=operation_unknown   retryable present=true   value=false
```

`retryable` 的**值**是 `false`。断言写的是 `if _, retryable := res.Structured["retryable"]; retryable`——
`map[key]` 的第二个返回值是「键是否存在」，不是值。U-30 按 ADR-0010 让该字段**始终存在**（调用方不该
从键缺席推断 false），于是断言在「键存在」时必红。修复是把断言改成检查值（`5d274eb`），改后：

```text
qa-independent: PASS - 18 test(s) passed, 0 skipped, 0 failed
```

这条记在报告里，是因为「测试红了」不等于「代码坏了」——本轮差一点把一次契约改进当成回归。

### 1.2 文档契约：5 条，v0.11.6 基线同样红

```bash
node scripts/check-contracts.mjs   # 当前工作树
# Design contract check failed (5):
#   - no tools found in docs/mcp-tools.md; the table format changed
#   - docs/mcp-conformance.md is missing: tools matrix
#   - docs/mcp-conformance.md is missing: transport matrix
#   - docs/mcp-conformance.md is missing: authorization matrix
#   - docs/mcp-conformance.md is missing: test plan
```

用 v0.11.6 标签的干净工作树跑同一个脚本，**同样 5 条失败**：

```bash
git worktree add /tmp/base-v0116 v0.11.6 && cd /tmp/base-v0116 && node scripts/check-contracts.mjs
# Design contract check failed (5): …（逐条相同）
```

根因是文档重构后脚本没跟上，不是本次修复引入：

- `scripts/check-contracts.mjs:18` 用正则 `^\| \`magento.x.y\` \| (read|write|…)\|` 从
  `docs/mcp-tools.md` 抽工具表；该页现在写明「The authoritative list of registered tools is
  implemented-tools.md; this page does not duplicate it」，表被移走了。
- 脚本要的锚点是 `### 1.1 Tools` / `### 1.2 Transport and sessions` / `### 1.3 Authorization …` /
  `## 4. Conformance tests to implement`；`docs/mcp-conformance.md` 现在是一级编号
  `## 2. Tools` / `## 3. Transport and sessions` / `## 4. Authorization …` / `## 7. Test map`。

**判定**：既有缺陷（v0.11.6 基线就有），不影响运行时代码；修法二选一——把脚本锚点更新到新结构，
或把工具表/小节编号改回去。这属于文档任务，不在本轮回归范围，记在这里供派发。

## 2. 全量 live 矩阵（123 用例 / 105 工具）

三次运行，同一份二进制、同一实例、同一店铺：

| 运行 | 结果文件 | pass | soft-fail | fail | skipped |
|---|---|---|---|---|---|
| 基线（生产 v0.11.6） | `results-20261122-025615.json` | 112 | 7 | 1 | 3 |
| run1（新库、旧 harness） | `results-regress-20261122-040546.json` | 109 | 8 | 2 | 4 |
| run2（热库、旧 harness） | `results-regress2-20261122-041306.json` | 110 | 8 | 2 | 3 |
| **run3（热库、harness 已跟上）** | `results-regress3-20261122-041925.json` | **112** | **7** | **1** | **3** |

**run3 与基线逐用例零差异**（按 `(group, tool, case)` 对齐 123 条，无新增/缺失/状态变化）：

```text
baseline           {'pass': 112, 'soft-fail': 7, 'fail': 1, 'skipped': 3}
run3_harnessFixed  {'pass': 112, 'soft-fail': 7, 'fail': 1, 'skipped': 3}
（per-case status deltas: none）
```

剩下的 `1 fail + 3 skipped` 与基线完全相同，是该店的既有环境限制：`magento.product.options.create`
走 `POST /products/{sku}/options`，本店未暴露（`unsupported_capability`），其后续 update/delete 因此
缺前置条件而 skip。

### 2.1 run1/run2 与基线的 3 个差异（全部已解释，无一为产品回归）

| 用例 | 基线 → run1 | 判定 | 证据 |
|---|---|---|---|
| `platform.info / reports the registered surface` | pass → fail（`tools = 105, want 102`） | **harness 期望过时**：INT-07/U-74 新增 `magento.operation.get/list/dispose` 三个工具，102+3=105 | 实例 `tools/list` 实测 105，且含这三个工具名 |
| `magento.product.tier_prices.set / sets a tier price` | pass → soft-fail（`validation_failed`） | **基线的 pass 是假通过**：U-07 让 200+错误数组如实报失败，暴露了 fixture 用数字 customer_group 的错 | 见 §3.2：上游 200 + `Brak takiego podmiotu z grupą klientów`，读回 `{"prices":[]}`（零写入） |
| `magento.import.batch / reads the batch` | pass → skipped（`missing prerequisite "batch_id"`） | **新库冷启动**：`import-read` 在 `import-write` 之前跑，新库还没有任何 batch 可取 id | run2（同一库、第二次跑）该用例恢复 pass；且运行后 `import.batches` 返回 2 条，`import.batch` 用真实 id 可读 |

这三条都在 `b9117a3`（`test(live): follow the catalog to 105 tools and fix the tier-price fixture`）
里被修好——修的是 harness，不是产品代码：`platform.info` 期望 102→105，tier-price fixture 的
`customer_group` 从 `"0"` 改成 `"ALL GROUPS"`（Magento 要的是 group code）。run3 用修好的 harness
复跑，回到基线水平。

### 2.2 分组对照（run3 vs 基线）

26 个分组全部一致；无任何分组的 pass 数低于基线。基线里的 7 个 soft-fail（`errors`、
`configurable-write`、`inventory-write`×2、`invoice-write`、`import-write`、`order-write`）在 run3
同样是 soft-fail 且错误码相同（`invalid_argument` / `validation_failed` / `conflict` /
`illegal_transition`），属于共享 demo 店的可预期状态（同源库存、已付款发票、已终结 job、不可取消订单）。

## 3. 抽查：6 条 p9 修复的真实调用效果

不看单测，直接用 MCP 调真实店铺，并对关键数字做 REST 直连复核。全部通过网关
`http://127.0.0.1:18082/mcp`（部署的是修好的二进制）。

### 3.1 U-01（P0）改重量不再把价格五字段整体错位

```text
# 建探针商品：price 45.00 / weight 1 / tax_class_id 2
magento.product.create {"sku":"REG-U01-041139", ..., "price":"45.00","weight":"1.0000","tax_class_id":2}
→ {"product":{"id":2170,"sku":"REG-U01-041139","price":45,...,"status":1,...}}

# 只改重量
magento.product.update {"sku":"REG-U01-041139","weight":"2.0000"}
→ {"product":{"id":2170,"sku":"REG-U01-041139","price":45,...,"updated_at":"2026-11-21 20:11:48",...}}

# 读回
magento.product.get → {"sku":"REG-U01-041139","price":45,"special_price":null,"cost":null,"weight":2,"status":1}
```

REST 直连复核同一商品：`{'sku': 'REG-U01-041139', 'price': 45, 'weight': 2, 'special_price': None,
'cost': None}`。

**结论**：改重量只改重量；`price` 仍 45，`special_price`/`cost` 没有被写成 2.5 / 日期 / 2026。
修复前同一操作会把 `special_price` 打成 2.5（P0，见 store-operator 报告）。

### 3.2 U-07 200 + 业务错误的写入不再报成功

```text
magento.product.tier_prices.set {"sku":"REG-U01-041139","prices":[{"quantity":5,"price":18.0,"website_id":0,"customer_group":"0"}]}
→ {"code":"validation_failed",
   "details":{"store_code":"pl","upstream_business_error":"true","upstream_status":"200",
              "upstream_message":"Brak takiego podmiotu z grupą klientów =%customerGroup. …"},
   "message":"Magento reported a business failure; the request did not apply","op":"magento POST /products/{id}"}

magento.product.tier_prices {"sku":"REG-U01-041139"} → {"prices":[]}
```

**结论**：上游用 HTTP 200 包了一个业务错误（波兰语「没有该客户组对应的实体」），修复后如实返回
`validation_failed` 且**零写入**（读回空）；修复前这里返回成功、什么都没写。这也是 §2.1 里
tier-price 用例从「假 pass」变成 soft-fail 的原因。

> 残留观察（非本轮回归）：该消息里的 `%customerGroup` / `%SKU` / `%qty` 占位符没有被替换
> （Magento 的 `parameters` 是数组，U-25 只处理了 `%1` 数字式）。消息仍可读，但不够精确。

### 3.3 U-10 贷项通知单带 comment 不再必然 400

```text
magento.creditmemo.create {"order_id":40,"items":[{"order_item_id":47,"qty":1}],
                           "comment":"regress partial refund comment 041222", ...}
→ {"creditmemo":{"entity_id":27,"increment_id":"000000027","order_id":40,"grand_total":34,
                 "subtotal":34,"shipping_amount":0,"total_qty":1,...},"invoice_id":31,...}
```

REST 直连 `GET /rest/pl/V1/creditmemo/27`：

```text
grand_total 34 shipping 0 invoice_id None
comments [('regress partial refund comment 041222', 0)]
```

**结论**：带备注的退款成功落库，备注在 Magento 侧保存且 `is_visible_on_front=0`（整数 0，不是
bool）。修复前 `IsVisibleOnFront` 是 bool，带备注的退款**必定 400**。

### 3.4 U-11 部分退款不再连带退全额运费；最后一件才退剩余运费

同一订单 40（`grand_total 51 = subtotal 41 + shipping 10`，两件各 1）：

```text
# 第一次：只退 item 47（1/2 件）
magento.creditmemo.create {"items":[{"order_item_id":47,"qty":1}]}
→ {"creditmemo":{"entity_id":27,"grand_total":34,"subtotal":34,"shipping_amount":0,...}}

# 第二次：退最后一件 item 48（剩余 1 件）
magento.creditmemo.create {"items":[{"order_item_id":48,"qty":1}]}
→ {"creditmemo":{"entity_id":28,"grand_total":17,"subtotal":7,"shipping_amount":10,...}}
```

REST 复核订单 40：

```text
first  : grand_total 51 total_refunded 34 total_invoiced 51 state processing items qty_refunded [(47,1),(48,0)]
after  : grand_total 51 total_refunded 51 state closed                         items qty_refunded [(47,1),(48,1)]
```

**结论**：部分退款 `shipping_amount=0`（只退商品 34）；退完最后一件时返回剩余运费 10
（`grand_total 17 = 7 + 10`）；两次合计 34+17 = 51 = 订单总额，订单 closed，不多退不少退。
修复前第一次部分退款就把 10 运费全额退掉（退 34 实退 44）。

### 3.5 U-14 `capture:false` 不再报 `captured:true`

```text
magento.invoice.create {"order_id":40,"items":[{item 47,1},{item 48,1}],"capture":false}
→ {"invoice":{"entity_id":31,"state":2,"grand_total":51,...},"captured":false,"operation_id":"ca1a1594-…"}
```

**结论**：离线 `checkmo` 下 Magento 的 invoice.state 恒为 2（已付款），修复前据此报
`captured:true`；现在 `captured` 跟随调用方 `capture` 参数（false），不再把「已开票」当「已收款」。

### 3.6 U-13 / U-21 超额退款被结构化拒绝

```text
magento.creditmemo.create {"order_id":40,"items":[{"order_item_id":47,"qty":2}]}   # 只有 1 件可退
→ {"code":"validation_failed",
   "details":{"invoice_qty":"1","order_item_id":"47","requested_qty":"2"},
   "message":"the refund exceeds the quantity on the invoice it is linked to"}
```

**结论**：超额退款被拒，`details` 里有可编程的 `order_item_id` / `invoice_qty` / `requested_qty`
（U-13 的发票级校验先命中；U-21 的 `refundable_units` 键在订单级校验路径上）。上游零写入可由
REST 侧的总额闭合反证：订单 40 只有两张贷项（27 退 34、28 退 17），`total_refunded=51` 恰等于
两者之和，若超额请求写入过，总额会超过 51。

## 4. 测试残留（共享 demo 店）

| 类型 | 数据 |
|---|---|
| 探针商品 | `REG-U01-041139`（id 2170，price 45，weight 2） |
| 订单 | 40（已 closed：两件全退，total_refunded 51） |
| 发票 | 31（订单 40，全额） |
| 贷项通知单 | 27（退 34）、28（退 17，含运费 10） |
| 矩阵自建数据 | 每轮 `product-write` / `category-write` / `customer-write` 等按用例自建的实体（与基线轮次同性质） |
| 数据库 | 测试主机 `mage2mcp_regress`（独立库，含三轮矩阵的审计与账本行） |

探针商品与订单不删除（删除本身要审批且会再产生写操作）；如需清理，按 SKU/订单号在 Magento 后台处理。

## 5. 结论与建议

1. **零产品回归**：run3 与 v0.11.6 基线 123 用例逐条一致（112/7/1/3），失败与跳过均为该店既有
   环境限制。run1/run2 的 3 个差异全部定位为「harness 期望/冷启动」，且 `b9117a3` 已修好 harness。
2. **门禁**：代码侧 7 项全绿（gofmt/vet/test/race/QA/markdownlint/links）；文档契约检查 8 项中的
   第 8 项在 v0.11.6 基线就红（脚本锚点与重构后的文档不一致），**不是本轮回归**，建议单开一条
   文档任务修脚本或文档。
3. **抽查 6 条修复全部生效**（U-01 / U-07 / U-10 / U-11 / U-14 / U-13+U-21），关键金额与状态均用
   REST 直连复核：部分退款 34 → 最后一件 17 → 合计 51 = 订单总额。
4. **残留小项**（不阻断）：上游业务错误里的 `%customerGroup` 类占位符未替换（U-25 只覆盖 `%1`）；
   `creditmemo.create` 响应里 `invoice_linked` 字段在未确认时缺省而非显式 false（U-15 的语义可再收紧）。
5. **回归实例保留**：`mage2mcp-regress-server`（8081）/`mage2mcp-regress-worker` 仍在测试主机运行，
   便于队长复核；不再需要时 `sudo docker rm -f mage2mcp-regress-server mage2mcp-regress-worker`
   （独立库 `mage2mcp_regress` 可保留或 `DROP DATABASE`）。
