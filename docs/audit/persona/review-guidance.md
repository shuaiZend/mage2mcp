# 独立审查：MCP 说明面改动（t27 + t28）

审查对象：**t27**（GG-01/02/03/04 四条 P0）与 **t28**（GG-05..20：instructions 重写 + 描述修正）。
方式：**只读**——读代码与测试、在独立 worktree 里做**回退实验**、写**独立探针**驱动真实 MCP 工具调用
（`tools/list`、`initialize`、`prompts/list`、`creditmemo.create`、`order.get`）。未修改任何产品代码。

**快照**：审查开始于 `e1715a3`，期间 HEAD 前进两次，最终结论基于 **`a90a303`**
（`cd97a6d feat(mcp): give the instructions a write contract and a registration-time schema check`

+ `a90a303 ci: run the test-inventory check before compiling`）。关键结论在三个基线上都复测过，
下文标明哪个基线。

```bash
git worktree add /tmp/t29-head a90a303
```

---

## 0. 结论：**needs_revision**（1 个阻断项，内容其余全部通过）

**内容层面 t27 + t28 是可信的**：`shipping_amount` 三态实测正确、105 个描述全部 ≤1024、隐私三处与真实
返回逐字段相符、**回退实验全部按预期变红**、两条 verify 命令 exit 0、测试清单 960→960 无丢失。

唯一阻断项是**提交完整性**：

> **F3**：已提交的 instructions 写着 `Recipe: product_copy.`，但 **HEAD 里没有 `product_copy` prompt**
> ——`prompts/list` 只有 `write_safety`，`skills/` 与 `generatedPrompts` 还只是工作区里的未跟踪文件。
> 也就是说：**把长配方从 instructions 移走的那次提交，没有把它落到任何地方。**

这不是「文档措辞」问题：t28 把 2,498 字符的复制配方压缩成 450 字符红线，同时声明细节「搬到
product_copy prompt 和 skill」。在 `a90a303` 上，agent 按说明去 `prompts/get product_copy` 会拿到
not found，而它原本能在 instructions 里读到的配方已经不在了——**净损失**。

修法（二选一，都很小）：

1. 把工作区里的 `skills/`、`scripts/gen-prompts/`、`internal/mcp/server/prompts_gen.go` 一起提交
   （它们已经生成好、`prompts/list` 里 6 个 prompt 齐了），让 `product_copy` 真实存在；
2. 若 Task B 还要等，则本轮先把 `instructionCopy` 的**必要步骤**留在 instructions 里（至少「先读源、
   再比对目标、再写」三步），并删掉 `Recipe: product_copy` 这句——**不要指向不存在的东西**。

无论选哪条，都要补一条能抓住它的测试：现在 `TestCrossReferencesExist` 把 `known["product_copy"] = true`
写死了（`server_test.go:3165`），所以它**永远不会**因为「引用的 prompt 不存在」而失败。

### 审查过程中发现、并已随并发工作解决的另外两条

| 编号 | 当时的问题 | 现在的状态 |
|---|---|---|
| **F1** | t28 的全部改动**未提交**：`e1715a3` 的 instructions 仍是 3,157 字符，16 条红线只命中 1 条 | **已解决**：`cd97a6d` 提交后 HEAD instructions = 2,474，10/10 抽样红线（16/16 在快照上）命中 |
| **F2** | 工作树快照（`6f09ab3`）**删掉了 U-01 的回归测试** `TestBuildUpdatePayloadPricingFieldsKeepTheirCodes`（−33 行）；新守卫 `scripts/check-test-inventory.sh` 报 `removed: 1`、exit 1 | **已解决**：`a90a303` 上该测试在，守卫报 `960 → 960 (removed 0)`、exit 0。**保留这条守卫**——它正是为此存在的 |

---

## 1. 逐条验收（基线 `a90a303`）

| 验收项 | 结果 | 证据 |
|---|---|---|
| t27/t28 声称的改动确实存在 | ✅ | §2–§5；t28 的 instructions 五段与 t27 的描述/隐私改动都在 HEAD |
| 工具描述 ≤1024（含 `discloseWrite` 后缀） | ✅ | 105 个工具 **0 个超限**，最长 `magento.operation.dispose` = **1014**；总长 **42,112** ≤ 45,000 |
| server instructions ≤ t21 上限 | ✅ | **2,474** ≤ 目标 2,800 ≤ 硬上限 3,200；五段 511/723/498/288/446 各自在预算内（650/800/500/300/450） |
| GG-01 实测可传、传 0 与不传不同 | ✅ | 不传 → 上游 `shipping_amount=10`；传 0 → `0`；传 4 → `4`；三者都不再 `invalid_argument` |
| 隐私三处一致、与真实返回相符 | ✅ | 三处文案一致；真实 `order.get` 返回地址与配送方式，不返回 `payment`/`customer_email` |
| 逐条回退新增规则 → 测试失败 | ✅ | E1–E10 **10/10** 按预期变红（§5） |
| 安全红线在 instructions 而非只在 skill | ✅（红线本身）/ ❌（它指向的 prompt 不存在） | 16/16 红线短语在 instructions 里；但 `product_copy` 未注册（**F3**） |
| 没有放宽任何既有断言 | ✅（`a90a303`） | `server_test.go` 的重写是**收紧**（新增 3 条反向断言）；`toolerr_test.go` 只是换行；被删的 U-01 测试已恢复（F2 历史） |
| 两条 verify 命令 | ✅ | `go test ./internal/mcp/... ./internal/magento/` exit 0；`node scripts/check-contracts.mjs` exit 0（115 tools / 22 ADRs） |

---

## 2. GG-01：`shipping_amount` 三态实测（我自己写的探针，不引用 t27 的测试）

探针走**注册后的 MCP 工具 + 确认流程**，夹具用**全额退款**（1/1 件）——这是唯一能区分「不传」与「传 0」的场景：

```text
REVIEW shipping omit          -> upstream=10     # 剩余运费（不传时的默认）
REVIEW shipping explicit-zero -> upstream=0      # 明确不退
REVIEW shipping explicit-four -> upstream=4
```

四段链路都在：schema 属性（`write_tools.go:1095`）→ 入参结构体 `ShippingAmount *float64`（`:1126`）→
handler 透传（`:1185`）→ `CreditmemoCreateInput.ShippingAmount`。E1（删 schema 属性）与 E2（handler 改成
传 `nil`）都让 `TestCreditmemoCreateAcceptsShippingAmount` 变红，说明这条链不是「schema 加了但 Go 侧没接」。

**非阻断 O1**：t27 自己的那条测试用**部分退款**（2 件里退 1 件），该场景下「不传」和「传 0」在上游都是
`0`，所以**它证明不了 nil 与 0 的区分**。建议补一条全额退款的工具层用例（我这次的探针就是）。

## 3. 长度（自己算，不引用他们报的数）

从 `initialize` / `tools/list` 的**运行时结果**量，因此包含 `discloseWrite` 追加的后缀：

```text
REVIEW tools=105 over1024=0 description_total=42112
REVIEW longest magento.operation.dispose = 1014
REVIEW longest magento.configurable.create = 991
REVIEW longest magento.product.create = 982
REVIEW longest magento.inventory.transfer = 894
REVIEW longest magento.invoice.create = 866
REVIEW longest magento.creditmemo.create = 858     # 与 t27 自述一致
REVIEW instructions runes=2474                     # a90a303
```

五段实测（从源码字面量求和，与运行时一致）：results **511**/650、writes **723**/800、reads **498**/500、
discovery **288**/300、copy **446**/450，+8 个分隔符 = **2,474**。

两条要记住的：

+ `reads` 只剩 **2 个字符**余量（498/500）。这是设计使然（超预算即失败），但下次改读语义必然要动预算，
  别误判成回归。

+ 历史基线：`e1715a3` 时 instructions 是 **3,157** 且 15/16 红线缺失——那正是 F1 的证据，`cd97a6d` 后消失。

## 4. 隐私声明：三处一致 + 真实调用对照

三处（`tools.go` 的 `orderGetToolDescription`、`order.go` 的 `Order` 类型注释、`README.md` 的 PII bullet）
现在说的是同一件事：单订单读返回收货/账单地址（收件人、街道、电话、地址上的 email）与配送方式；
不返回支付数据；order search 不含地址。

我抽了一次**真实 `order.get`**（注册后的工具；载荷同时含顶层地址、`extension_attributes.shipping_assignments`
与 `payment`）：

```text
REVIEW order.get shipping_address     present=true
REVIEW order.get billing_address      present=true
REVIEW order.get shipping_description present=true
REVIEW order.get payment              present=false
REVIEW order.get customer_email       present=false
```

`order.search` 的 `fields` 白名单（`order.go:31`）确实不含任何地址字段 → 「search 不含地址」也成立。
三处任一改回旧文案（E3/E4）都会让 `TestOrderGetAddressDisclosureIsConsistent` 变红。

**非阻断 O2**：该测试把**整个 `order.go`** 当「结构体注释」断言，理论上别的注释也能满足它（当前每个短语
只出现一次，所以不会漏）。建议按 AST 取 `Order` 的 doc comment。

## 5. 回退实验：10 条规则逐条删掉，全部变红

在 `/tmp/t29-wt`（工作树快照）与 `/tmp/t29-head`（`a90a303`）里逐条打补丁 → 跑定向测试 → `git checkout --` 还原：

| # | 删掉/改坏的东西 | 期望变红的测试 | 实测 |
|---|---|---|---|
| E1 | `creditmemoCreateSchema` 的 `shipping_amount` 属性 | `TestCreditmemoCreateAcceptsShippingAmount` | **FAIL** ✅ |
| E2 | handler 的 `ShippingAmount: in.ShippingAmount` → `nil` | 同上 | **FAIL** ✅ |
| E3 | `order.go` 的 `Order` 注释改回「Addresses are not returned」 | `TestOrderGetAddressDisclosureIsConsistent` | **FAIL** ✅ |
| E4 | `README.md` 的 PII bullet 改回「order reads omit email, addresses」 | 同上 | **FAIL** ✅ |
| E5 | creditmemo 描述里的运费默认句 | `TestCreditmemoCreateDescriptionStatesShippingDefault` | **FAIL** ✅ |
| E6 | instructions 里「different human identity」那句 | `TestInstructionsCarryRedLines` | **FAIL** ✅ |
| E7 | 给 `instructionCopy` 追加 120 字符（超预算 450） | `TestInstructionsBudget` | **FAIL** ✅ |
| E8 | `bulk_update` 描述里的「silently ignored … use magento.product.update」 | `TestGuidanceCorrectionsArePresent` | **FAIL** ✅ |
| E9 | 共享 `idempotencyKeyProp` 里的派生键语义 | `TestIdempotencyKeyDescriptionsStateTheDerivedKey` | **FAIL** ✅ |
| E10 | 入参结构体 `json:"shipping_amount"` → `shipping_amount_x` | 注册期校验 | **3 个 FAIL** ✅ |

E10 值得单独记：t28 的「附加 E」（schema 属性名必须与 Go 入参结构体一致）**真的在注册期生效**——这正是
GG-01 那一类「schema 加了字段但 Go 侧没接」的复发防线。

## 6. 阻断项 F3：instructions 指向不存在的 `product_copy` prompt

```text
# a90a303 上，通过 initialize 拿到的 instructions：
"…The full recipe moved to the product_copy prompt and the mage2mcp skill; only its red lines stay here…"
"…reproduce. Recipe: product_copy."
REVIEW instructions reference product_copy=true

# prompts/list 实测：
REVIEW prompt "write_safety"
REVIEW prompt count=1

# 仓库：
$ ls internal/mcp/server/prompts_gen.go   → No such file（HEAD 里没有）
$ ls skills                               → No such file（HEAD 里没有）
$ git status                              → ?? skills/  ?? scripts/gen-prompts/  ?? internal/mcp/server/prompts_gen.go
```

`product_copy` 在 HEAD 里只出现在 instructions 文本、`server.go` 的注释，以及测试的白名单
`known["product_copy"] = true`（`server_test.go:3165`）。**没有任何注册。**

**影响**：按说明行动的 agent 会去 `prompts/get product_copy` 拿到 not found；而它原本能在 instructions 里
读到的 2,498 字符配方已经被压缩掉了。对不支持 prompt 的客户端更糟：它连「去哪里找配方」都拿不到。
这条与 F1 是同一类风险的两个方向——F1 是「改了没提交」，F3 是「提交了但依赖没提交」。

**修法**：

1. 提交工作区里已经生成好的三件东西（`skills/`、`scripts/gen-prompts/`、`prompts_gen.go`）——
   我实测工作区里 `generatedPrompts` 有 6 个 prompt（write_contract / refunds / reports / imports /
   product_copy / write_safety），注册点 `server.go:907` 也在；或
2. 本轮先把 `instructionCopy` 的必要步骤留在 instructions 里，并删掉 `Recipe: product_copy` 这句。
3. **无论哪条**：把 `TestCrossReferencesExist` 的 `known["product_copy"] = true` 改成「断言该 prompt
   真的注册了」（遍历 `prompts/list`），否则这条测试永远抓不到悬空引用。

## 7. 非阻断观察

+ **O1**：t27 的 shipping 测试用部分退款，无法区分「不传」与「传 0」（§2）。建议补全额退款用例。
+ **O2**：`TestOrderGetAddressDisclosureIsConsistent` 把整个 `order.go` 当结构体注释（§4）。建议按 AST 取。
+ **O3**：`guidanceExemptions` 把 4 个工具（`import.start/cancel/resume`、`operation.list`）排除在字段/
  instance 覆盖之外，理由写清了（常量在别的文件），但它们不在「95% 覆盖」的分母约束里，后续应回收。

+ **O4**：`instructionReads` 只剩 2 字符余量（498/500），属设计内，但下次改读语义要动预算。

## 8. 断言放宽审查（逐条看 diff）

| 文件 | 改动性质 | 结论 |
|---|---|---|
| `internal/mcp/server/server_test.go` | 重写 `TestServerInstructionsRequireDiscoveryBeforeCopying`：8 条短语 → 10 条（含 taxonomy 同意与验收标准），并**新增 3 条反向断言**（长配方必须不在 instructions 里） | **收紧** |
| `internal/mcp/toolerr/toolerr_test.go` | `Add(srv, …)` 拆行 | 纯格式 |
| `internal/magento/product_write_test.go` | 审查期间快照里被删了 33 行（U-01 测试）；`a90a303` 已恢复 | **曾经放宽，现已恢复**（F2） |
| 其余 | 只有新增 | — |

## 9. 门禁（`a90a303`）

```text
MAGE2MCP_TEST_DATABASE_URL='postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp?sslmode=disable' \
  go test -count=1 ./internal/mcp/... ./internal/magento/     → exit 0
node scripts/check-contracts.mjs                              → exit 0（115 tools / 22 ADRs）
bash scripts/check-test-inventory.sh                          → exit 0（960 → 960，removed 0）
```

注意：任务书给的两条 verify 命令**都不跑** `check-test-inventory.sh`。F2 证明「门禁绿」与「删了测试」
可以同时成立，所以提交前请额外跑 `scripts/local-ci.sh`（`a90a303` 已把它接上）。

## 10. 复现清单

```bash
git worktree add /tmp/t29-head a90a303

# 探针（审查期间写在 worktree 内，审查结束已删）：
#   TestReviewProbeAtHead            → tools/list 长度、instructions 长度与红线、prompts/list
#   TestReviewProbeShippingThreeWay  → omit / 0 / 4 三态的上游 body
#   TestReviewOrderGetFieldsAtHead   → order.get 的地址与 payment 字段
#   TestReviewProbeShippingAmountThreeWay / TestReviewProbeDescriptionLengths（快照基线）

# 回退实验见 §5（每条都是 patch → go test -run → git checkout --）
git worktree remove --force /tmp/t29-head
```

> 探针只存在于 `/tmp` worktree 内，已随 worktree 删除；主工作区与 HEAD 零改动（本文件是唯一新增产物）。
