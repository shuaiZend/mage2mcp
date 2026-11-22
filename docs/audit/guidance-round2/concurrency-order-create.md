# 并发 `order.create` 是否串单：实测结论

被测对象：生产部署 **v0.11.9**（`platform.info` 实测），隧道 `http://127.0.0.1:18110/mcp`，
demo Magento（store `pl`，PLN，endpoint `https://demo-magento-2.auroracreation.com`）。

**只验证，不改代码。** 测试商品 `R2-CONC-20261123-031629-{A,B}` 已删除并复查为空；
测试订单留在 demo 店（订单不可删），下表逐条列出。

---

## 结论（先给答案）

1. **串单存在，且可稳定复现**：同一 `customer_id` 的两个并发 `order.create`，
   **6 轮里有 3 轮**产出一张**混了两边商品**的订单；另一笔调用以
   `conflict: The cart is locked for processing` 失败。
2. **根因在 Magento 的购物车语义，不在网关的代码逻辑**：
   `POST /rest/pl/V1/customers/{id}/carts` 对**已有活跃购物车**的客户**返回那一辆**，
   不会新建。网关（`internal/magento/order_create.go:143`）忠实按这个语义「取或建购物车」，
   于是两个并发调用拿到**同一个 cart id**，各自往里加商品，谁先下单谁把**两个人的商品**都带走。
3. **串行不串**（实测两笔串行订单各自干净）——因为下单会把购物车消耗掉，下一次调用拿到新车。
   所以这不是「参数写错」，是**时间窗口**问题。
4. **生产事故级：是。** 订单一旦创建不可撤销，混单会带着别人的商品产生真实的库存、发票、
   收款后果。触发条件不苛刻：同一客户两个并发下单（两个 agent、一个 agent 的重试+新请求）。
5. **额外发现（更隐蔽，串行也会中招）**：只要客户名下存在**残留的活跃购物车**（例如上一次调用
   在 add-item 之后、place-order 之前崩了，或调用方自己用 REST 建了车），**下一次下单会把残留
   商品一起带走**。实测订单 25 就是这么来的（见 §5）。

---

## 1. 实验步骤

| 步骤 | 做法 |
|---|---|
| 准备 | MCP `product.create` 建两个可区分商品：`R2-CONC-<ts>-A`、`-B`（simple，各 11.00，库存 50） |
| 并发实验 | 同一 `customer_id=1`，两个线程同时 `order.create`，A 线程只买 A、B 线程只买 B；每次调用带独立 `idempotency_key`（UUID），地址/支付/运费完全相同 |
| 串行对照 | 同样的两笔下单**依次**执行 |
| 直连 REST 对照 | 管理员令牌（`POST /rest/pl/V1/integration/admin/token`，凭据来自部署配置）直接调 `POST /customers/1/carts`、`POST /carts/{id}/items`，绕开网关 |
| 账本取证 | 生产库 `operations` 表里 `operation='magento.order.create'` 的 `state` 与 `result` |
| 订单取证 | `GET /rest/pl/V1/orders/{id}` 逐张读 items |

## 2. 并发实验结果（6 轮）

| 轮次 | 订单 | 该订单的 SKU | 结果 |
|---|---|---|---|
| 1 | 14 | **A + B** | **混单**；另一笔 `conflict: The cart is locked for processing` |
| 2 | 15 / 16 | B / A | 干净（各自单独成单） |
| 3 | 19 | **A + B** | **混单**；另一笔 `conflict` |
| 4 | 20 / 21 | A / B | 干净 |
| 5 | 22 | **A + B** | **混单**；另一笔 `conflict` |
| 6 | 23 / 24 | A / B | 干净 |

**3/6 混单**。混单那一轮的另一笔调用不是「也建了一张单」，而是
`{"code":"conflict", "details":{"stage":"place_order","upstream_status":"400",
"upstream_message":"The cart is locked for processing. Please try again later."}}`
——正是「两个人抢同一辆购物车」的指纹：一个人正在下单，另一个人被锁在外面。

订单 14 的原始载荷（`grand_total` 80，两行各 1 件）：

```json
"items": ["R2-CONC-20261123-031629-A", "R2-CONC-20261123-031629-B"]
```

## 3. 串行对照（排除参数问题）

| 订单 | SKU | 结论 |
|---|---|---|
| 17 | A | 干净 |
| 18 | B | 干净 |

同样的参数、同样的客户、同样的地址，**串行就不串**。所以问题不是「请求构造错误」，
而是**两个调用在时间上重叠**。

## 4. 直连 REST：机制证据（决定性）

绕开网关，只用管理员令牌：

```text
POST /customers/1/carts        → 31
POST /carts/31/items  {A, qty 1}  → 200, item_id 46
POST /customers/1/carts        → 31      ← 再次调用仍返回同一辆车（车里有 A）
POST /carts/31/items  {B, qty 1}  → 200, item_id 47
GET  /carts/31/items           → [A ×1, B ×1]     ← 两个「调用」的商品进了同一辆车
```

也就是说 **`POST /customers/{id}/carts` 是「取或建」，不是「新建」**：
只要客户名下有一辆活跃购物车，它就返回那一辆。网关 `order_create.go:143` 正是调这个路由，
拿到 cart id 后 `POST /carts/{cartID}/items` —— 两个并发调用拿到同一个 id，商品就混了。

对照：两次**同时**发出的 `POST /customers/1/carts` 偶尔会得到两个不同 id（实测 31 和 32），
说明 Magento 在「两辆车都还没落库」时会各建一辆。**这就是为什么只有一部分轮次混单**：
谁先建车、谁后到，决定了是「各自一辆」还是「共用一辆」。

## 5. 网关确实在用客户既有购物车（最直接的证据）

我在 REST 里把 A、B 两件商品留在了客户 1 的购物车 31 里（不结账），然后通过**网关**下一次单
（请求里只有 A）：

```text
gateway order.create(items=[A]) → 订单 25，items = [A, B]   ← 把 REST 留在车里的 B 也带走了
之后 GET /carts/31/items → No such entity（购物车已被下单消耗）
```

这一条把责任钉死在语义上：**网关没有自己的购物车状态，它用的就是 Magento 上属于该客户的那辆车**。
所以任何写进那辆车的商品——并发的另一个调用，或上一次失败/放弃的调用残留——都会被下一张订单带走。

## 6. t47 报的订单 3 是什么

`GET /rest/pl/V1/orders/3`：

```text
items = [WT09-S-Yellow ×2, 24-MB01 ×1, 24-MB04 ×1]   customer_id = 1   grand_total = 115
```

`24-MB01`/`24-MB04` 是 t47 请求的，`WT09-S-Yellow ×2` 是**别人**当时在做的可配置品的子 SKU。
这与本报告 §2 的混单指纹完全一致：**同一客户、并发下单、共用购物车**。
t47 怀疑的「同一 customer 并发 order.create 会串单」**成立**。

## 7. 账本与可观测性

`operations` 表记录了每笔调用的 `state` 与 `result`（含最终订单的 items），但**没有记录 cart id**：
`internal/magento/order_create.go` 里 `cartID` 只用于拼 URL（`/carts/{cartID}/items`、
`/carts/{cartID}/order`），既不进 `upstream_ref` 也不进 `result`。网关日志（`docker logs`）里也没有
cart id。**后果**：出事之后无法从网关侧证明「两笔调用用了同一辆车」，只能像本报告这样用订单内容
反推。建议把 cart id 记进 ledger 的 `result`（或单独的列/日志字段），这是取证的关键句柄。

## 8. 修法方向（不在本任务范围，供决策）

按代价从低到高：

1. **按客户串行化**：`order.create` 对 `(instance, customer_id)` 加互斥（例如
   `pg_advisory_xact_lock(hashtext(instance||':'||customer_id))`，或把 ledger 的唯一键扩到
   「同客户未完成的下单」）。并发调用排队，第二笔拿到新车（第一笔已把车消耗掉）。
2. **下单前校验购物车为空**：拿到 cart id 后 `GET /carts/{id}/items`，非空即拒绝
   （typed conflict：「该客户已有活跃购物车，内含 N 件商品」），并给出 cart id 让调用方处理。
   这条同时能挡住 §5 的残留场景。
3. **失败时清理购物车**：`cleanupCart` 已存在，但实测错误里出现过
   `"cart_cleanup": "failed: not_found"`；清理失败要有记录（否则残留车会污染下一单）。
4. **取证**：cart id 进 ledger result/日志（§7）。

1+2 组合能同时解决并发混单与残留混单；只做 1 不解决残留，只做 2 不解决并发下的「后到者看到别人
刚加的商品」——严格说 2 会把并发变成失败而不是混单，对资金路径已经是更好的结果。

## 9. 数据卫生

- 测试商品：`R2-CONC-20261123-031629-A/B`，已 `product.delete`（均 `deleted: true`），
  `products.get` 复查返回空。
- 测试订单：14、19、22、25（混单，证据保留）、15、16、20、21、23、24（干净对照）、17、18（串行对照）。
  demo 店订单不可删；未修改任何既有商品或订单。
- 未修改代码；本文件是唯一新增物。
