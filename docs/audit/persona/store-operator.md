# 场景测试：店铺日常运营（store-operator）

被测对象：生产部署 **v0.11.6**（`49.51.250.83:8080`，本机经 SSH 隧道 `http://127.0.0.1:18110/mcp`），
店铺 `https://demo-magento-2.auroracreation.com`，store code `pl`（PLN），实例 `demo`。

测试方式：**按运营的一天的活干**（巡店 → 改价 → 上下架 → 发货 → 查物流），不是逐个工具点测。
凡出现「工具说成功但前台/后台没生效」「报错看不懂」都记录在下面。只记录，不改代码。

## 复现方式

下面每条命令都用同一个包装（省去重复的环境变量）：

```bash
mcp() {  # mcp <tool> '<json-args>'
  MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
  MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
  go run -tags live ./test/live -mode=raw -tool="$1" -args="$2"
}
```

即：

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
go run -tags live ./test/live -mode=raw -tool=<工具名> -args='{...}'
```

前台核对用 `curl` 直取店铺页面；上游核对用管理员令牌直连 Magento REST（令牌获取见
`docs/audit/semantic/README.md`）。

**注意**：这是一家多人共用的测试店（本轮还有其它 6 个 agent 同时在跑）。订单、库存、
价格会被别的 agent 同时改动；下面凡是可能受并发影响的观察，都附了对照实验或标注
SUSPECTED。

---

## 一、先说我按流程做了什么

| 步骤 | 操作 | 结果 |
|---|---|---|
| 1 早上巡店 | `product.search` 拉 40 个 SKU → `inventory.status_batch` 查 10 个 | 见 SO-08 / SO-14（能查，但查不出「缺货在售」清单） |
| 2 改价 | `product.update` 24-UG06 7.00 → 8.55，核对目录页与详情页 | ✅ 前后台一致（`8.55`），但见 SO-10 / SO-11 |
| 2b 做促销 | `product.update` special_price + 起止日期 | ❌ 400，且**改错了字段**（SO-02） |
| 3 上下架 | `product.update` 24-UG07 status=2 → 前台 404 → status=1 → 前台 200 | ✅ 完全正确 |
| 3b 批量上下架 | `products.bulk_update` 两个 SKU | ⚠️ 部分失败被报成「参数错误」（SO-06） |
| 4 处理待发货订单 | `order.search` → `order.get` → `shipment.create` | ⚠️ 看不到收货地址（SO-04）；带备注发货必失败（SO-03）；数量少发（SO-05） |
| 5 顾客问「到哪了」 | `order.search increment_id` → `shipment.list` / `shipment.get` | ✅ 能查到跟踪号（见文末「正常但值得记录」） |

---

## 二、发现

### SO-01: 改「重量」把商品售价改成了重量值（P0，会直接改错钱）

- **严重级别**: P0
- **工具**: `magento.product.update`
- **现象**: 只传 `weight`，返回成功；读回来 `weight=2.5` **同时** `special_price` 变成了 `2.500000`，
  前台售价从 32.00 变成 **2.50 PLN**。
- **期望**: 只改重量，价格不动。
- **证据**:

```bash
# 改之前
mcp magento.product.get '{"instance":"demo","sku":"24-MB04"}'
#   "price": 32
#   "attributes": { "special_price": "32.000000", ... }

# 只改重量
mcp magento.product.update '{"instance":"demo","sku":"24-MB04","weight":"2.5"}'
# is_error=false
#   "weight": 2.5,
#   "attributes": { "special_price": "2.500000", ... }   <-- 重量写进了促销价

# 读回来确认
mcp magento.product.get '{"instance":"demo","sku":"24-MB04"}'
# price 32  weight 2.5  special_price 2.500000

# 前台（目录页 gear.html 与详情页）
curl -sS 'https://demo-magento-2.auroracreation.com/pl/strive-shoulder-pack.html' | grep -o 'data-price-amount="[0-9.]*"'
# data-price-amount="2.5"
curl -sS 'https://demo-magento-2.auroracreation.com/pl/gear.html' | grep -o 'data-price-amount="[0-9.]*"'
# 目录页一次返回整页商品的价格，其中就包含 data-price-amount="2.5"（Strive Shoulder Pack）；
# 这里只摘这一项，其它行是该页其它商品的价格
```

- **根因（代码定位，供修复阶段参考）**: `internal/magento/product_write.go` 的
  `buildUpdatePayload` 把参数按
  `[]*string{in.Weight, in.SpecialPrice, in.SpecialFromDate, in.SpecialToDate, in.Cost}`
  传给 `pricingAttributesFromPointers`，而后者按
  `pricingAttributeOrder = ["special_price","special_from_date","special_to_date","cost","tax_class_id"]`
  逐位对齐 —— **整体错位一格**。`buildCreatePayload` 用的是 `pricingAttributes(_, specialPrice, ...)`，
  参数顺序正确，所以 `product.create` 不受影响（已实测，见 SO-02 末尾）。
- **影响**: 运营改运费重量/包装重量是日常操作，一次就把在售价格打到 2.5 折，且没有任何提示。
  一天几百单的店，损失按小时计。
- **状态**: CONFIRMED

### SO-02: 同一个错位导致「促销价」「促销结束日」「成本」全部写错字段（P0）

- **严重级别**: P0
- **工具**: `magento.product.update`（`magento.configurable.update`、导入更新同样复用这段代码）
- **现象**: 五个字段整体错位一格，实测映射如下：

| 我传的参数 | 实际写到 | 观察到的结果 |
|---|---|---|
| `weight` | `special_price` | 见 SO-01，售价 32 → 2.5 |
| `special_price` | `special_from_date` | 400 报错（日期格式非法），**促销价永远设不上** |
| `special_from_date` | `special_to_date` | 促销结束日被改成我传的日期 |
| `special_to_date` | `cost` | 成本被改成日期的年份（`2026-12-31` → `cost=2026.000000`） |
| `cost` | `tax_class_id` | 税类从 12（Taxable Goods）被改成 7 |

- **期望**: 每个参数写到自己的字段；做不到就报错，不要静默写错。
- **证据**:

```bash
# (a) 设促销价 → 400，且报错只说 special_from_date 有问题
mcp magento.product.update '{"instance":"demo","sku":"24-MB01","special_price":"29.00","special_from_date":"2026-11-20","special_to_date":"2026-11-30"}'
# is_error=true code=validation_failed
#   "details": {"store_code":"pl","upstream_field":"special_from_date","upstream_status":"400"}
#   "message": "Magento rejected the request as invalid; verify store_code and field values"
# 同一个 body 直接打 Magento 是成功的（证明不是 Magento 的锅）：
curl -sS -X PUT 'https://demo-magento-2.auroracreation.com/rest/pl/V1/products/24-MB01' \
  -H "Authorization: Bearer $(cat /tmp/magento_token)" -H 'Content-Type: application/json' \
  -d '{"product":{"sku":"24-MB01","custom_attributes":[{"attribute_code":"special_price","value":"29.00"},{"attribute_code":"special_from_date","value":"2026-11-20"},{"attribute_code":"special_to_date","value":"2026-11-30"}]}}'
# HTTP 200，促销价正常生效

# (b) 促销开始日 → 写进了促销结束日
mcp magento.product.update '{"instance":"demo","sku":"24-MB04","special_from_date":"2026-12-31"}'
# is_error=false；读回：special_from_date=2026-11-21 00:00:00（Magento 自动），
#                          special_to_date=2026-12-31 00:00:00   <-- 我传的是开始日
#                          special_price=32.000000（没动）

# (c) 促销结束日 → 写进了成本
mcp magento.product.update '{"instance":"demo","sku":"24-MB04","special_to_date":"2026-12-31"}'
# is_error=false；读回：cost = "2026.000000"

# (d) 成本 → 写进了税类
mcp magento.product.update '{"instance":"demo","sku":"24-MB04","cost":"7.77"}'
# is_error=false；读回：tax_class_id = 7（原来是 12），cost 依旧是 2026.000000

# 对照：新建商品这条路是对的（同样的字段全部落到正确位置）
mcp magento.product.create '{"instance":"demo","sku":"SO-TEST-WSHIFT","name":"SO Weight Shift Probe","attribute_set_id":4,"type_id":"simple","price":"50.00","weight":"1.75","special_price":"45.00","cost":"20.00","visibility":1,"status":2,"quantity":5}'
# weight 1.75, special_price 45.000000, cost 20.000000, tax_class_id 2  ← 全对
```

- **影响**:
  1. 「做个周末促销」这个最日常的动作**必然失败**，而报错指向 `special_from_date`，运营会以为是日期格式写错，
     反复试错也修不好（真实原因是 special_price 被塞进了日期字段）。
  2. 一旦有人绕过（比如只传 `special_from_date`/`cost`），就会静默改掉促销结束日、成本、税类 ——
     成本和税类错了，毛利报表和含税价全错，且没人会发现。
  3. `magento.configurable.update` 内部就是调 `Update()`（`internal/magento/configurable.go:1056`），
     女装/男装那一批可配置商品同样中招；`magento.import.execute` 的更新分支复用
     `UpdateProductInput`（`internal/magento/import.go:744`），CSV 里带 `special_price`/`cost` 的批量导入也会写错。
- **状态**: CONFIRMED

### SO-03: 带备注发货 100% 失败，报错不给原因（P1）

- **严重级别**: P1
- **工具**: `magento.shipment.create`
- **现象**: 只要传了 `comment`，就 400；错误详情里没有 `upstream_message`，也没有 `upstream_field`，
  运营完全无从下手。
- **期望**: 要么发货成功并附上备注，要么明确说清哪个字段不合法。
- **证据**:

```bash
mcp magento.shipment.create '{"instance":"demo","order_id":1,"items":[{"order_item_id":1,"qty":1}],"tracks":[{"track_number":"PL123456789","carrier_code":"dhl","title":"DHL Parcel"}],"comment":"Wyslane 2026-11-22","notify":false}'
# is_error=true code=validation_failed
# {
#   "code": "validation_failed",
#   "details": {"store_code":"pl","upstream_status":"400"},     <-- 没有任何原因字段
#   "message": "Magento rejected the request as invalid; verify store_code and field values",
#   "op": "magento POST /order/{id}/{id}"
# }

# 同样的 body 直接打 Magento，才知道真实原因：
curl -sS -X POST 'https://demo-magento-2.auroracreation.com/rest/pl/V1/order/1/ship' \
  -H "Authorization: Bearer $(cat /tmp/magento_token)" -H 'Content-Type: application/json' \
  -d '{"items":[{"order_item_id":1,"qty":1}],"tracks":[{"track_number":"PL123456789","title":"DHL Parcel","carrier_code":"dhl"}],"comment":{"comment":"Wyslane 2026-11-22","is_visible_on_front":false},"appendComment":true,"notify":false}'
# {"message":"Error occurred during \"is_visible_on_front\" processing. The \"\" value's type is invalid. The \"int\" type was expected. Verify and try again."}
# HTTP 400
```

- **根因**: `internal/magento/order_documents_write.go` 的 `documentComment.IsVisibleOnFront` 是 `bool`，
  而 Magento 的 `ShipOrderInterface` 要求 `int`。另外，Magento 这句报错里带引号字段名，
  被 `upstreamTextIsSafe`（`internal/magento/client.go:451`）判为「可能泄露标识符」而整句丢弃，
  于是 `details` 里连 `upstream_message` 都没有 —— **可执行的原因被脱敏规则吃掉了**。
- **影响**: 运营发货时写「已发 DHL，单号 xxx」是标准动作；现在只要写备注就发不了货，
  且错误信息等于「失败，自己猜」。绕开备注虽然能发货，但备注是给客服用来看物流的。
- **状态**: CONFIRMED

### SO-04: 订单详情看不到收货地址、支付方式、已发数量（P2，实际上无法完成「发货」）

- **严重级别**: P2
- **工具**: `magento.order.get`
- **现象**: `order.get` 只返回 16 个字段 + 瘦身后的 items，**没有收货地址、没有配送方式、没有支付方式、
  没有 qty_shipped/qty_invoiced**。运营拿到订单也没法打包发货（不知道寄给谁）。
- **期望**: 处理待发货订单，至少要有：收件人/地址、配送方式、每行已发/未发数量。
- **证据**:

```bash
mcp magento.order.get '{"instance":"demo","order_id":1}'
# {
#   "order": {
#     "entity_id":1, "increment_id":"000000001", "status":"processing", "state":"processing",
#     "store_id":1, "customer_id":1, "created_at":"...", "updated_at":"...",
#     "grand_total":36.39, "subtotal":29, "discount_amount":0, "shipping_amount":5,
#     "tax_amount":2.39, "total_qty_ordered":1, "order_currency_code":"PLN",
#     "items": [{"item_id":1,"sku":"WS03-XS-Red","name":"Iris Workout Top","product_type":"configurable","qty_ordered":1,"price":29,"row_total":29}]
#   }
# }
# item 的键只有：item_id, sku, name, product_type, qty_ordered, price, row_total

# 上游其实什么都有（同一个订单）：
curl -sS 'https://demo-magento-2.auroracreation.com/rest/pl/V1/orders/1' -H "Authorization: Bearer $(cat /tmp/magento_token)" | python3 -c '...'
# shipping_description: Flat Rate - Fixed；billing_address.city: Calder；
# items[0].qty_shipped = 1, qty_invoiced = 1；extension_attributes.shipping_assignments 里含完整收货地址
```

- **影响**: 「查订单 → 发货」这条链在 MCP 上是断的：看不到地址、看不到已发数量。更糟的是
  **已发完的单也看不出来**（见 SO-05 的报错），运营只能靠 400 猜。
- **状态**: CONFIRMED

### SO-05: 发货数量比要求少 1，但工具报成功（P1，SUSPECTED）

- **严重级别**: P1
- **工具**: `magento.shipment.create`
- **现象**: 请求发 4 件，实际只发 3 件；请求发 2 件，实际只发 1 件。工具返回 `is_error=false`，
  返回体里 `total_qty` 就是少后的数字，**没有任何警告**。订单随后被 Magento 标成 `complete`。
  但在自建的新订单上同样参数（2 件、6 件）完全正确，因此**无法稳定复现**，标 SUSPECTED。
- **期望**: 发 4 件就是 4 件；做不到就要报错或明确告知「实际发了 3 件」。
- **证据**:

```bash
# 订单 8：24-MB01 订购 4 件、已发 0 件
mcp magento.shipment.create '{"instance":"demo","order_id":8,"items":[{"order_item_id":10,"qty":4}],"tracks":[{"track_number":"PL987654321","carrier_code":"dhl","title":"DHL Parcel"}],"notify":false}'
# is_error=false
#   "shipment": {"entity_id":15,"increment_id":"000000015","order_id":8,"total_qty":3, ...}   <-- 要 4 发 3
curl -sS -g 'https://demo-magento-2.auroracreation.com/rest/pl/V1/shipments?searchCriteria%5Bfilter_groups%5D%5B0%5D%5Bfilters%5D%5B0%5D%5Bfield%5D=order_id&searchCriteria%5Bfilter_groups%5D%5B0%5D%5Bfilters%5D%5B0%5D%5Bvalue%5D=8' \
  -H "Authorization: Bearer $(cat /tmp/magento_token)"
# total 1；ship 000000015 items=[(10, 3)]        <-- Magento 侧也确实只有 1 张 3 件的发货单（不是并发发了两次）
curl -sS 'https://demo-magento-2.auroracreation.com/rest/pl/V1/orders/8' -H "Authorization: Bearer $(cat /tmp/magento_token)" \
  | python3 -c 'import sys,json;o=json.load(sys.stdin);print([(i["item_id"],i["qty_ordered"],i["qty_shipped"]) for i in o["items"]], o["status"])'
# item 10 ordered 4 shipped 3；order status = complete

# 订单 11：订购 2 件、已发 0 件
mcp magento.shipment.create '{"instance":"demo","order_id":11,"items":[{"order_item_id":13,"qty":2}],"notify":false}'
# is_error=false；"total_qty": 1                                   <-- 要 2 发 1
# 上游：item 13 ordered 2 shipped 1；order status = complete

# 确认「请求参数本身是 4 和 2」（不是我的 shell 传错）：网关的确认记录里存的就是原参数
ssh -o BatchMode=yes ubuntu@49.51.250.83 \
  'sudo docker exec mage2mcp-postgres-1 psql -U mage2mcp -d mage2mcp -c \
   "select summary from confirmation_tokens where tool='"'"'magento.shipment.create'"'"' order by created_at desc limit 4;"'
# order_id=8  instance=demo items=[{"order_item_id":10,"qty":4}] ...
# order_id=11 instance=demo items=[{"order_item_id":13,"qty":2}] notify=false

# 对照实验（自建订单，同样走工具）：数量完全正确
mcp magento.order.create '{"instance":"demo","customer_id":1,"items":[{"sku":"24-MB03","qty":2}], ... ,"idempotency_key":"so-qty-test-20261122-d"}'
# entity_id 36, qty_ordered 2
mcp magento.shipment.create '{"instance":"demo","order_id":36,"items":[{"order_item_id":43,"qty":2}],"notify":false}'
# "total_qty": 2   ← 正确
mcp magento.shipment.create '{"instance":"demo","order_id":34,"items":[{"order_item_id":41,"qty":6}],"notify":false}'
# "total_qty": 6   ← 正确
```

- **怀疑方向**: 订单 8/11 是本轮另一个 agent 用 `order.create` 造的（收货人 "Sem Tester"），
  它们的 item 结构/库存状态可能与本轮自建订单不同；也可能是那两张单在窗口期内被并发写入过。
  两张单都是「少 1 件」而不是随机数，不像并发丢写。**留给修复阶段用干净的订单复现**。
- **影响**: 少发货 → 客户投诉、补发运费；而订单被标 complete，运营的「待发货」列表里也看不出来。
- **状态**: SUSPECTED

### SO-06: 并发下 Magento 的数据库死锁被报成「参数错误」，不可重试（P2）

- **严重级别**: P2
- **工具**: `magento.products.bulk_update`（单条 `product.update` 同理）
- **现象**: 批量下架两个 SKU，其中一个报 `validation_failed`，消息是
  「Magento rejected the request as invalid; verify store_code and field values (Database deadlock found when trying to get lock)」。
  这是**瞬时冲突**，应该可重试；被归成参数错误后，运营会以为这个 SKU 的数据有问题。
- **期望**: 死锁应映射为 `conflict`/可重试错误，并保留「可重试」标记。
- **证据**:

```bash
mcp magento.products.bulk_update '{"instance":"demo","skus":["24-UG01","24-UG02"],"status":2}'
# is_error=false（整体成功，逐行结果）
#   "rows":[
#     {"sku":"24-UG01","status":"failed","error":"validation_failed: Magento rejected the request as invalid; verify store_code and field values (Database deadlock found when trying to get lock)"},
#     {"sku":"24-UG02","status":"updated"}]

# 同样的 SKU 立刻重试就成功 —— 证明是瞬时冲突，不是数据问题
mcp magento.products.bulk_update '{"instance":"demo","skus":["24-UG01"],"status":2}'
# {"total":1,"updated":1,"failed":0,"rows":[{"sku":"24-UG01","status":"updated"}]}
```

- **影响**: 运营批量改价/上下架时看到「参数错误」，会去改商品数据；正确动作是重试。
- **状态**: CONFIRMED（并发来自本轮其它 agent，但错误分类问题与并发无关）

### SO-07: 下单失败留下购物车，下一次下单数量翻倍；错误是波兰语且占位符没替换（P2）

- **严重级别**: P2
- **工具**: `magento.order.create`
- **现象**:
  1. 第一次下单（地址缺 `region_id`）失败，错误信息是
     `upstream_message: "Proszę sprawdzić adres dostawy. %1"`（波兰语「请检查收货地址」+ 未替换的 `%1`），
     同时 `cart_cleanup: failed: not_found`。
  2. 补上 `region_id` 再下单，数量从我要求的 3 件变成 **6 件**（上一次失败留在购物车里的 3 件被累加），
     `subtotal` 228、`total_qty_ordered` 6，工具返回成功。
- **期望**: 参数缺失应在本地拦下（schema 里已经写明「波兰需要 region_id」），而不是丢给 Magento 回波兰语；
  失败后不能留下会污染下一单的购物车。
- **证据**:

```bash
# (1) 缺 region_id
mcp magento.order.create '{"instance":"demo","customer_id":1,"items":[{"sku":"24-MB03","qty":3}],"billing_address":{"firstname":"Anna","lastname":"Kowalska","street":["ul. Testowa 1"],"city":"Warszawa","postcode":"00-001","country_id":"PL","telephone":"500100200"}, ... }'
# is_error=true code=validation_failed
#   "details":{"cart_cleanup":"failed: not_found","completed_skus":"","stage":"place_order",
#              "store_code":"pl","upstream_message":"Proszę sprawdzić adres dostawy. %1","upstream_status":"400"}

# (2) 补 region_id 再来一次，同样的 3 件
mcp magento.order.create '{... "items":[{"sku":"24-MB03","qty":3}] ... "region_id":997 ... ,"idempotency_key":"so-qty-test-20261122-b"}'
# is_error=false
#   "entity_id":34,"increment_id":"000000034","total_qty_ordered":6,"subtotal":228,
#   "items":[{"item_id":41,"sku":"24-MB03","qty_ordered":6, ...}]
# 上游核对：qty_ordered = 6（3+3）
```

- **影响**: 少见的「下单」动作会多买一倍货；地址校验失败给的是波兰语 + `%1`，中文/英文运营看不懂。
- **状态**: CONFIRMED（数量翻倍与残留购物车的因果链为强推断：随后用干净 key 下单 1 件 → 1 件、2 件 → 2 件）

### SO-08: 库存三处数字互相矛盾，且没有解释（P2）

- **严重级别**: P2
- **工具**: `magento.inventory.status` / `magento.inventory.source_items`
- **现象**:
  1. 可配置商品（WJ01/WJ02）`qty=0` 但 `is_in_stock=true` —— 运营看到「0 件还有货」，不知道该不该补货。
  2. 把 `24-UG07` 的源库存设为 0 后：目录 `is_in_stock=false`（前台显示「Brak w magazynie」），
     但 `source_items` 里同一条记录仍然是 `"status": 1`（在售）。同一件事两个答案。
- **期望**: 三处口径一致，或至少字段名/描述说清「这个 status 是源状态，不是可售状态」。
- **证据**:

```bash
mcp magento.inventory.status_batch '{"instance":"demo","skus":["WJ01","WJ02"]}'
# {"sku":"WJ01","status":{"qty":0,"is_in_stock":true,...}}      <-- 0 件却 in stock
# {"sku":"WJ02","status":{"qty":0,"is_in_stock":true,...}}

mcp magento.inventory.assign '{"instance":"demo","items":[{"sku":"24-UG07","source_code":"default","quantity":0}]}'
# {"assigned":1}
mcp magento.inventory.status '{"instance":"demo","sku":"24-UG07"}'
# {"qty":0,"is_in_stock":false,...}
mcp magento.inventory.source_items '{"instance":"demo","skus":["24-UG07"]}'
# {"sku":"24-UG07","source_code":"default","quantity":0,"status":1}   <-- 数量 0 但 status=1
curl -sS 'https://demo-magento-2.auroracreation.com/pl/dual-handle-cardio-ball.html' | grep -o 'Brak w magazynie'
# Brak w magazynie    （前台确实缺货）
```

- **影响**: 巡店时「缺货/有货」判断不可靠；对可配置商品会误判为有货。
- **状态**: CONFIRMED（数据口径为 Magento 语义，但工具未做任何区分或说明）

### SO-09: 「改库存」没有入口，报错也不告诉你该用哪个工具（P2）

- **严重级别**: P2
- **工具**: `magento.product.update`
- **现象**: 运营最常做的「把这个商品库存改成 50」，在 `product.update` 上直接报
  「unexpected additional properties ["quantity"]」，不提示 `magento.inventory.assign`。
  讽刺的是 `product.create` 有 `quantity` 字段（上新能填库存），改库存却没有。
- **期望**: `product.update` 支持 quantity，或在报错里指路。
- **证据**:

```bash
mcp magento.product.update '{"instance":"demo","sku":"24-UG06","quantity":50}'
# is_error=true code=invalid_argument
#   "details":{"validation":"validating root: unexpected additional properties [\"quantity\"]"}
#   "message":"arguments do not match the tool schema"
# （schema 里 product.create 有 quantity，product.update 没有）
```

- **影响**: 新运营会卡在第一步；只能靠猜或看文档才知道要用 MSI 的 `inventory.assign`。
- **状态**: CONFIRMED

### SO-10: 价格接受 3-4 位小数并回显，前台只显示 2 位（P3，说成功但货架价不同）

- **严重级别**: P3
- **工具**: `magento.product.update`
- **现象**: 传 `8.555`，工具返回 `"price": 8.555`，读回也是 `8.555`；前台显示 **8.56**。
  schema 明确允许「up to 4 fraction digits」，PLN 却只展示 2 位。
- **期望**: 要么按币种精度四舍五入后再回显，要么提示「前台会显示 8.56」。
- **证据**:

```bash
mcp magento.product.update '{"instance":"demo","sku":"24-UG06","price":"8.555"}'
# is_error=false；"price": 8.555
mcp magento.product.get '{"instance":"demo","sku":"24-UG06"}'
# "price": 8.555
curl -sS 'https://demo-magento-2.auroracreation.com/pl/affirm-water-bottle.html' | grep -o 'data-price-amount="[0-9.]*"'
# data-price-amount="8.56"
```

- **影响**: 报价/对账时 0.005 的差异被放大成客诉；运营以为改成了 8.555。
- **状态**: CONFIRMED

### SO-11: 两种 404 又混在一起了（P3）

- **严重级别**: P3
- **工具**: `magento.inventory.status`
- **现象**: 查一个不存在的 SKU，返回「Magento has no such resource or route」——
  和「路由不存在」的说法完全一样。README 特意强调这两种 404 必须区分，这里没有区分。
- **期望**: SKU 不存在应说「SKU 不存在」，并保留上游 404 的原始语义。
- **证据**:

```bash
mcp magento.inventory.status '{"instance":"demo","sku":"NO-SUCH-SKU"}'
# is_error=true code=not_found
#   "details":{"store_code":"pl","upstream_status":"404"}
#   "message":"Magento has no such resource or route"     <-- 到底是 SKU 没了还是工具坏了？
#   "op":"magento GET /stockItems/{id}"

# 对照：product.search 对同一个不存在的 SKU 给的是空列表（不报错），行为也不一致
mcp magento.product.search '{"instance":"demo","sku":"NO-SUCH-SKU"}'
# {"products":[],"total":0}
```

- **影响**: 巡店时 SKU 打错一个字母，会被理解成「系统坏了」。
- **状态**: CONFIRMED

### SO-12: 参数错误的「原因」只在 structuredContent 里，text 通道只有一句话（P3）

- **严重级别**: P3
- **工具**: 所有工具（schema 校验层）
- **现象**: 缺参数、类型不对、多余字段、小数点点错（波兰习惯的 `8,55`）都返回同一句
  `text: invalid_argument: arguments do not match the tool schema`；
  具体原因（哪个字段、哪个正则不匹配）只在 `structuredContent.details.validation` 里。
  只读 `content[].text` 的客户端（很多对话式客户端就是这样）拿不到任何可用信息。
- **期望**: text 里带上「price 必须是 12 位以内的十进制，最多 4 位小数」这类可执行信息。
- **证据**:

```bash
mcp magento.product.update '{"instance":"demo","sku":"24-UG06","price":"8,55"}'
# is_error=true code=invalid_argument
#   "details":{"validation":"validating root: validating /properties/price: pattern: \"8,55\" does not match regular expression \"^[0-9]+(\\\\.[0-9]{1,4})?$\""}
#   "message":"arguments do not match the tool schema"
# text: invalid_argument: arguments do not match the tool schema      <-- 只有这句

mcp magento.product.get '{"sku":"24-MB03"}'
# "details":{"validation":"validating root: required: missing properties: [\"instance\"]"}
# text: invalid_argument: arguments do not match the tool schema
```

- **影响**: 运营（以及代表运营说话的模型）只能靠猜字段名和格式；波兰店用逗号小数是习惯动作。
- **状态**: CONFIRMED

### SO-13: 三处工具描述是别的工具抄过来的（P3，会误导）

- **严重级别**: P3
- **工具**: `magento.inventory.assign`、`magento.product.update`
- **现象**:
  1. `inventory.assign` 的 `items` 描述：「Each entry names an **order item** and a quantity;
     the quantities must not exceed **what is still outstanding**」——这是发货行的描述，
     库存操作根本没有「未发数量」概念。
  2. `product.update` 的 `category_ids` / `website_ids` 数组 items 描述是「Requested page size.」。
- **期望**: 描述与该工具实际语义一致。
- **证据**:

```bash
# 看 schema（-mode=schema 直接打印某个工具的 inputSchema）
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
go run -tags live ./test/live -mode=schema -tool=magento.inventory.assign | grep -A5 '"items"' | head -7
#     "items": {
#       "description": "Lines to act on. Each entry names an order item and a quantity; the quantities must not exceed what is still outstanding.",
#       "items": {
#         "additionalProperties": false,
#         "properties": {
#           "quantity": {
#             "minimum": 0,

MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
go run -tags live ./test/live -mode=schema -tool=magento.product.update | grep -A4 '"category_ids"'
#   "category_ids": {
#     "description": "Replaces the product's category assignments when provided; an empty array clears them.",
#     "items": {
#       "description": "Requested page size.",
```

- **影响**: 按描述理解会以为 `inventory.assign` 会拒绝超过订单量的库存；实际不会。
- **状态**: CONFIRMED

### SO-14: 巡店找不出「缺货但仍在售」的清单（P3，能力缺口）

- **严重级别**: P3
- **工具**: `magento.product.search` + `magento.inventory.*`
- **现象**: 店铺 2114 个 SKU。`product.search` 只能按 status/type/price/created 过滤，**不能按库存过滤**；
  `inventory.status_batch` 一次 25 个。要回答「今天哪些商品缺货还在卖」，得自己拉 2114 个 SKU 再分批查
  （约 85 次调用），而且 `product.search` 单页最多 200。
- **期望**: 提供一个「可售状态 × 库存」的筛选（例如 `product.search` 加 `in_stock` / `qty_below` 参数）。
- **证据**:

```bash
mcp magento.product.search '{"instance":"demo","limit":1}'
# {"products":[...],"total":2114}
# schema properties: instance, query, sku, type_id, status, price_from, price_to, created_from, created_to, limit, cursor
# —— 没有库存相关过滤

# 实际找到的一个「缺货仍在售」样本（qty=0、is_in_stock=false、status=1、visibility=4）
mcp magento.inventory.status '{"instance":"demo","sku":"SEM-T56-014627-1"}'
# {"qty":0,"is_in_stock":false,...}；product.search 里它 status=1 vis=4
```

- **影响**: 这是运营每天早上的第一个动作，现在只能抽样，无法覆盖全店。
- **状态**: CONFIRMED（能力缺口，不是错误）

### SO-15: `instance` 描述说「可省略」，schema 却必填（P3，第一次调用就踩）

- **严重级别**: P3
- **工具**: 全部工具
- **现象**: 每个工具的 `instance` 描述都是「resolved by the server when only one instance is configured」，
  但 `required` 里有 `instance`。按描述省略 → 立刻 schema 校验失败。
- **期望**: 描述与 schema 一致（要么真的可省略，要么把描述改成必填）。
- **证据**:

```bash
mcp magento.product.search '{"limit":3}'
# is_error=true code=invalid_argument
#   "details":{"validation":"validating root: required: missing properties: [\"instance\"]"}
```

- **影响**: 每个新接入的客户端/新同事第一次调用都会踩，且报错是 JSON Schema 术语。
- **状态**: CONFIRMED

### SO-16: 上游错误原文是波兰语、占位符没替换（P2）

- **严重级别**: P2
- **工具**: 任意透传 `upstream_message` 的工具
- **现象**: Magento 侧本地化消息会原样透出，且 `%1`/`%field` 占位符没被替换，例如
  - `Proszę sprawdzić adres dostawy. %1`（请检查收货地址）
  - `Nieprawidłowe dane produktu: %1`（商品数据无效）
  - `Shipment Document Validation Error(s): ... Nie można utworzyć przesyłki bez produktów.`（不能创建没有商品的发货单）
- **期望**: 占位符至少有兜底替换；或者补一句英文摘要（「Magento 拒绝：收货地址不完整」）。
- **证据**:

```bash
mcp magento.order.create '{... 缺 region_id ...}'
# "upstream_message": "Proszę sprawdzić adres dostawy. %1"
mcp magento.shipment.create '{"instance":"demo","order_id":1,"items":[{"order_item_id":1,"qty":1}],"notify":false}'
# "upstream_message": "Shipment Document Validation Error(s):The order does not allow a shipment to be created.Nie można utworzyć przesyłki bez produktów."
#   （订单 1 其实早就发完了：上游 items[0].qty_shipped=1=qty_ordered；工具不说「已发完」，只说「不能创建」）
```

- **影响**: 非波兰语运营看不懂；「已发完」这种状态需要自己推断。
- **状态**: CONFIRMED

---

## 三、正常但值得记录（不要「修」坏了）

1. **上下架端到端正确**：`status=2` → `product.search status=1` 里消失、`status=2` 里出现、前台 404；
   `status=1` → 前台 200。没有缓存延迟问题。
2. **改价前后台一致**：7.00 → 8.55，`product.get`、`product.search`、目录页 `gear.html`、详情页全部 8.55。
3. **批量部分成功是诚实的**：`products.bulk_update` 逐行返回 `updated/failed`，整体 `is_error=false`，
   比「整批失败」好用（问题只在错误分类，见 SO-06）。
4. **`inventory.status_batch` 单 SKU 失败不拖累整批**：
   `{"sku":"NO-SUCH-SKU","error":"not_found: ..."}`，好设计。
5. **发货 + 跟踪号可查**：`shipment.create` 带 `tracks` 后，`shipment.list`/`shipment.get` 能拿到
   `track_number/carrier_code/title`，客服回答「到哪了」够用；`order.search increment_id` 也能用顾客报的
   单号（`000000008`）反查 `entity_id`。
6. **`inventory.assign` 改库存有效**：`quantity: 0` → 目录 `is_in_stock=false` → 前台「Brak w magazynie」；
   改回 100 → 恢复在售。

---

## 四、测试数据清理

为不把测试店弄坏，改动过的数据已尽量还原：

| 对象 | 改动 | 还原情况 |
|---|---|---|
| `24-MB04` | weight 2.5 → special_price 2.5；cost → tax_class_id 7；special_to_date → cost 2026 | 已用管理员令牌直接 REST 还原：price 32、weight 空、special_price 32.000000、cost 空、tax_class_id 12 |
| `24-UG06` | price 7 → 8.55 → 8.555 | 已还原为 7.00 |
| `24-MB01` | 直接 REST 设过 special_price 29 + 起止日期 | 已清空（注：测试期间 price 被其它 agent 改成 35.5，不是我的改动） |
| `24-UG07` | 源库存 100 → 0 → 100 | 已还原 100，前台恢复在售 |
| `24-UG01` / `24-UG02` | bulk_update 下架 | 已还原 status=1 |
| `24-UG07` | status 2 → 1 | 已还原 |
| `SO-TEST-WSHIFT` | 用于验证 create 路径的探针商品 | 已 `product.delete` |
| 订单 34/35/36 及其发货单 | `order.create` + `shipment.create` 造的测试单 | 保留（无删除路由；demo 站会周期性重置） |

## 五、给修复阶段的优先级建议

1. **先修 SO-01/SO-02**（同一处 `buildUpdatePayload` 错位）：一个参数顺序问题，影响售价、促销、
   成本、税类四类数据，且是静默写错。改完必须补一条「update 的每个定价字段落到自己字段」的测试
   —— 现在 `internal/` 下没有任何测试覆盖 `buildUpdatePayload` / `pricingAttributesFromPointers`
   （`grep -rn "buildUpdatePayload\|pricingAttributesFromPointers" --include=*_test.go internal/` 无结果），
   create 路径对、update 路径错，正好说明这条链路是裸奔的。
2. **SO-03**（发货备注 → `is_visible_on_front` 类型）——一行类型问题，但让「带备注发货」100% 不可用。
   顺带修 `upstreamTextIsSafe` 过度脱敏：带引号字段名的上游原因也应能透出。
3. **SO-05**（少发 1 件）需要干净订单复现；**SO-04**（order.get 缺地址/已发数量）是运营能不能真正
   用这套工具发货的前提，建议一起做。
4. **SO-06/SO-07/SO-16** 是错误分类与错误文案问题，改起来便宜、对运营体验提升明显。
