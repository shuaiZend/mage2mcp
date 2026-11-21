# 交叉验证：P0 资金路径与销售域 P1

- **验证日期**：2026-11-22
- **验证对象**：t61 修复后的工作区代码，使用 t61 构建的 `/tmp/t61/mage2mcp`（dev，端口 `18114`，`writes=true`，auth 关闭，DB `mage2mcp_sec`）。
- **说明**：生产 v0.11.5 是修复前版本；这里验证的是修复代码本身。未修改任何产品代码，只写本报告。
- **店铺**：`https://demo-magento-2.auroracreation.com`，store `pl`，instance `demo`；直连 REST 使用 admin token。
- **审批**：auth 关闭，使用 `/tmp/t61/mage2mcp-admin -config /tmp/t61/config.yaml -as approver approve <id>`。
- **清理**：测试地址 10 已用直连 `DELETE /addresses/10` 删除（返回 `true`）；订单/发票/贷项/发货留给 demo 周期性重置。

## 0. 结论

| 项 | 结果 | 证据 |
|---|---|---|
| P0 退款 body/金额 | **PASS** | 订单 13：2 件，grand 34；发票 10：34；只退 1 件 → 贷项 8：**17.00**（精确半额），`total_qty=1`；直连 line qty=1 |
| P0 发货 body/数量 | **PASS** | 订单 15：2 件，grand 78；发票 12：78；只发 1 件 → 发货 5：`total_qty=1`；直连 line qty=1 |
| 三个路由 body 形状 | **PASS（有环境限制）** | `invoice/ship/refund` 均为顶层参数；测试断言无 `entity`；实际部分数量证明不是整单操作。店铺 Swagger 端点不可用，见 §4 |
| P1 取消假成功 | **PASS** | 已开票+部分发货订单 15：直连 cancel 返回 HTTP 200 `false` 且仍 processing；网关返回 `conflict`；`operations` 行 `failed/conflict` |
| P1 region_id | **PASS** | 地址 10 创建 region_id 1003 → 直连 `mazowieckie/1003`；只传 region_id 997 更新 → 直连 `dolnośląskie/997` |
| P1 creditmemo total_qty | **PASS** | 贷项 8：工具 `total_qty=1`；直连 Magento 响应 `total_qty:null`，line qty=1，工具从行回读/请求量兜底 |

未发现 t61 修复的回归或不完整；唯一需要说明的是含运费的订单 11 退 1 件得到 44（34 商品 + 10 运费），不是 39；这是 Magento 对“单件+运费”的准确退款，不是 body 形状缺陷。为了给出严格的“半额”，另建了零运费虚拟商品订单 13（2×17=34），退 1 件得到 17。

## 1. 测试数据（本轮新建）

| 对象 | 内容 |
|---|---|
| 订单 11 | 2×24-MB01，grand 78（68 + 10 运费）；发票 9 = 78；贷项 7 = 44（1 件 + 运费） |
| 订单 13 | 2×`downolable`（virtual，17/件），grand 34（无运费）；发票 10 = 34；贷项 8 = **17**（1 件） |
| 订单 15 | 2×24-MB01，grand 78（68 + 10 运费）；发票 12 = 78；发货 5 = 1 件 |
| 地址 10 | customer 1，PL，初始 region_id 1003，更新为 997 |

> 中间产物订单 12（4 件）是测试脚本复用了同一 customer 的购物车导致的 4 件订单，未用于任何结论；订单 13 是清空购物车后重新创建的 2 件订单。这是测试脚本问题，不是产品缺陷。

## 2. P0 退款：订单 13，2 件，只退 1 件

### 2.1 创建订单（网关）

```bash
# /tmp/t61/money_verify.py 内 call() = MCP 调用 + confirmation 审批后重试
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
addr={'firstname':'Veronica','lastname':'Costello','street':['6146 Honey Bluff Parkway'],
      'city':'Warsaw','postcode':'00-001','country_id':'PL','telephone':'5552293326','region_id':997}
args={'instance':'demo','customer_id':1,'items':[{'sku':'downolable','qty':2}],
      'billing_address':addr,'shipping_address':addr,
      'shipping_carrier_code':'flatrate','shipping_method_code':'flatrate',
      'payment_method':'checkmo','idempotency_key':'t73-order-v3-...'}
print(call('magento.order.create',args))
PY
```

实际输出（节选）：

```json
{"order":{"entity_id":13,"increment_id":"000000013","status":"pending","state":"new",
"grand_total":34,"subtotal":34,"discount_amount":0,"shipping_amount":0,"tax_amount":0,
"total_qty_ordered":2,"order_currency_code":"PLN",
"items":[{"item_id":16,"sku":"downolable","name":"downolable","product_type":"virtual",
"qty_ordered":2,"price":17,"row_total":34}]}}
```

### 2.2 全额发票（网关）

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.invoice.create',{'instance':'demo','order_id':13,'capture':True,
  'items':[{'order_item_id':16,'qty':2}],'idempotency_key':'t73-invoice-13'}))
PY
```

实际输出：

```json
{"invoice":{"entity_id":10,"increment_id":"000000010","order_id":13,"state":2,
"grand_total":34,"subtotal":34,"shipping_amount":0,"total_qty":2,"order_currency_code":"PLN"}}
```

直连回读：

```bash
T=$(cat /tmp/magento_token)
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/invoices/10" |
jq '{entity_id,order_id,grand_total,total_qty,items:[.items[]|{order_item_id,qty,row_total}]}'
```

```json
{"entity_id":10,"order_id":13,"grand_total":34,"total_qty":2,
 "items":[{"order_item_id":16,"qty":2,"row_total":34}]}
```

### 2.3 只退 1 件（网关）

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.creditmemo.create',{'instance':'demo','order_id':13,'invoice_id':10,
  'items':[{'order_item_id':16,'qty':1}],'idempotency_key':'t73-creditmemo-13'}))
PY
```

实际输出：

```json
{"creditmemo":{"entity_id":8,"increment_id":"000000008","order_id":13,"state":2,
"grand_total":17,"subtotal":17,"discount_amount":0,"shipping_amount":0,"tax_amount":0,
"adjustment":0,"total_qty":1,"order_currency_code":"PLN"},
"invoice_id":10,"operation_id":"ed3de030-4ac6-4957-9f8e-39401af70d96"}
```

直连回读：

```bash
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/creditmemo/8" |
jq '{entity_id,order_id,grand_total,total_qty,items:[.items[]|{order_item_id,qty,row_total}]}'
```

```json
{"entity_id":8,"order_id":13,"grand_total":17,"total_qty":null,
 "items":[{"order_item_id":16,"qty":1,"row_total":17}]}
```

**判定**：grand_total 17 = 34/2，精确半额；`total_qty=1`；直连 line qty=1。订单 13 回读 `qty_refunded=1`：

```json
{"entity_id":13,"status":"complete","state":"complete","grand_total":34,
 "total_qty_ordered":2,"items":[{"item_id":16,"sku":"downolable",
 "qty_ordered":2,"qty_invoiced":2,"qty_refunded":1,"qty_shipped":0}]}
```

### 2.4 含运费订单 11 的补充

订单 11（2×24-MB01，grand 78，运费 10）退 1 件得到贷项 7：

```json
{"creditmemo":{"entity_id":7,"order_id":11,"state":2,"grand_total":44,
"subtotal":34,"shipping_amount":10,"total_qty":1,"order_currency_code":"PLN"}}
```

44 = 34 商品 + 10 运费；不是 39（78/2）。这是 Magento 对单件退款同时退运费的准确结果，不是 P0 body 形状回归。为了严格验证“半额”，使用零运费订单 13。

## 3. P0 发货：订单 15，2 件，只发 1 件

创建订单 15（2×24-MB01，grand 78）：

```json
{"order":{"entity_id":15,"increment_id":"000000015","status":"pending","state":"new",
"grand_total":78,"subtotal":68,"shipping_amount":10,"total_qty_ordered":2,
"items":[{"item_id":18,"sku":"24-MB01","qty_ordered":2,"price":34,"row_total":68}]}}
```

全额发票 12：

```json
{"invoice":{"entity_id":12,"order_id":15,"state":2,"grand_total":78,
"subtotal":68,"shipping_amount":10,"total_qty":2}}
```

只发 1 件：

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.shipment.create',{'instance':'demo','order_id':15,
  'items':[{'order_item_id':18,'qty':1}]}))
PY
```

实际输出：

```json
{"shipment":{"entity_id":5,"increment_id":"000000005","order_id":15,
"total_qty":1,"created_at":"2026-11-21 18:14:15","updated_at":"2026-11-21 18:14:15"}}
```

直连回读：

```bash
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/shipment/5" |
jq '{entity_id,order_id,total_qty,items:[.items[]|{order_item_id,qty,sku}]}'
```

```json
{"entity_id":5,"order_id":15,"total_qty":1,
 "items":[{"order_item_id":18,"qty":1,"sku":"24-MB01"}]}
```

订单 15 回读 `qty_shipped=1`：

```json
{"entity_id":15,"status":"processing","state":"processing","grand_total":78,
 "items":[{"item_id":18,"sku":"24-MB01","qty_ordered":2,"qty_invoiced":2,
 "qty_refunded":0,"qty_shipped":1}]}
```

**判定**：发货 total_qty=1，直连 line qty=1，订单 qty_shipped=1；不是整单 2 件。

## 4. 三个路由的 body 形状

### 4.1 店铺 schema 的限制

尝试读取店铺 Swagger：

```bash
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/swagger.json"
```

```json
{"message":"Specified request cannot be processed."}
```

店铺未暴露可用的 Swagger/OpenAPI（52 字节错误体），因此不能把店铺 schema 文件作为直接证据。改用三层证据：

1. Magento 路由契约：`POST /order/{orderId}/invoice`（`InvoiceOrderInterface`）、`POST /order/{orderId}/ship`（`ShipOrderInterface::execute`）、`POST /order/{orderId}/refund`（`RefundOrderInterface::execute`）均接收顶层参数，不接受 `{"entity":...}` 包装。
2. 代码中的请求体结构体：`invoiceOrderBody`、`shipmentOrderBody`、`refundOrderBody` 的字段都在顶层；`refundOrderBody` 的调整额在 `arguments` 内（不是顶层）。
3. 实际行为：如果 body 被 `entity` 包装，Magento 会收不到 items，退款会退整张发票、发货会发整单。实测贷项 8 只退 1/2、发货 5 只发 1/2，且直连行数量均为 1，证明 items 确实到达顶层。

### 4.2 请求体断言测试

```bash
go test -count=1 -run \
 'TestCreateInvoice$|TestCreateShipment$|TestCreateCreditmemo$|TestCreateCreditmemoPartialQuantityIsSentAndReported$' \
 -v ./internal/magento/
```

```text
--- PASS: TestCreateShipment
--- PASS: TestCreateCreditmemo
--- PASS: TestCreateCreditmemoPartialQuantityIsSentAndReported
--- PASS: TestCreateInvoice/record_only
--- PASS: TestCreateInvoice/capture
```

`TestCreateShipment` 断言 `body.Items[0].Qty` 在顶层，并显式检查 `raw["entity"]` 不存在；`TestCreateCreditmemo` 同样检查 `raw["entity"]` 不存在，且 `arguments.adjustment_*` 正确；`TestCreateInvoice` 解码 `invoiceOrderBody`，路由为 `POST /order/42/invoice`。

## 5. P1 取消：已开票订单 15

网关调用：

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.order.cancel',{'instance':'demo','order_id':15,
  'idempotency_key':'t73-cancel-15'}))
PY
```

实际输出：

```json
{"code":"conflict",
 "details":{"operation_id":"15e1002b-bba0-4849-8e76-1386e4ac2a6d",
 "order_id":"15","order_state":"processing"},
 "message":"Magento declined the cancellation and the order state did not change (an invoiced or otherwise non-cancelable order); verify the order before acting",
 "isError":true}
```

直连 Magento 同一操作：

```bash
curl -sS -H "Authorization: Bearer $T" -X POST \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/orders/15/cancel" \
  -w '\nHTTP:%{http_code}\n'
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/orders/15" |
jq '{entity_id,status,state}'
```

```text
false
HTTP:200
{"entity_id":15,"status":"processing","state":"processing"}
```

Ledger：

```bash
psql 'postgres://mage2mcp:mage2mcp-dev@127.0.0.1:55440/mage2mcp_sec?sslmode=disable' \
  -Atc "select id,state,error_code from operations where id='15e1002b-bba0-4849-8e76-1386e4ac2a6d';"
```

```text
15e1002b-bba0-4849-8e76-1386e4ac2a6d|failed|conflict
```

**判定**：Magento 200+false，网关 conflict，ledger 不是 succeeded。

## 6. P1 region_id：创建与更新

### 6.1 创建地址（region_id 1003）

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.customer.addresses.create',{
 'instance':'demo','customer_id':1,'firstname':'T73','lastname':'Region',
 'street':['Test Street 73'],'city':'Warsaw','postcode':'00-002','country_id':'PL',
 'telephone':'555000073','region_id':1003}))
PY
```

```json
{"address":{"id":10,"country_id":"PL","region_id":1003,
"default_billing":false,"default_shipping":false}}
```

直连回读：

```bash
curl -sS -H "Authorization: Bearer $T" \
  "https://demo-magento-2.auroracreation.com/rest/pl/V1/customers/1" |
jq '.addresses[] | select(.id==10) | {id,region_id,region}'
```

```json
{"id":10,"region_id":1003,
 "region":{"region_code":"PL-14","region":"mazowieckie","region_id":1003}}
```

### 6.2 只传 region_id 更新（997）

```bash
python3 - <<'PY'
exec(open('/tmp/t61/money_verify.py').read().split("if __name__")[0])
print(call('magento.customer.addresses.update',{
 'instance':'demo','customer_id':1,'address_id':10,'region_id':997}))
PY
```

```json
{"address":{"id":10,"country_id":"PL","region_id":997,
"default_billing":false,"default_shipping":false}}
```

直连回读：

```json
{"id":10,"region_id":997,
 "region":{"region_code":"PL-02","region":"dolnośląskie","region_id":997}}
```

**判定**：创建/更新后 region_id 与直连回读一致；只传 region_id 不再 invalid_argument。

## 7. P1 total_qty：贷项 8

- 工具输出：`total_qty: 1`（见 §2.3）。
- 直连 Magento `/creditmemo/8`：`total_qty: null`，但 `items[0].qty=1`。
- 工具从 items 求和/请求量兜底得到 1，未再输出 0。

```json
{"entity_id":8,"grand_total":17,"total_qty":null,
 "items":[{"order_item_id":16,"qty":1,"row_total":17}]}
```

## 8. 回归测试

```bash
go test -count=1 ./internal/magento/
```

```text
ok      github.com/shuaiZend/mage2mcp/internal/magento    5.264s
```

## 9. 环境/脚本限制

- 生产 v0.11.5 不包含 t61 修复；本轮用 t61 构建的 dev 二进制（18114）验证修复代码。生产部署仍需发布后复测。
- demo 店周期性重置；本轮对象 11/13/15、发票 9/10/12、贷项 7/8、发货 5 可能被重置回收。
- 店铺 Swagger 不可用，body 形状以 Magento 路由契约、请求体结构体、显式 `entity` 断言和实际部分数量共同证明。
- 订单 12 的 4 件是测试脚本复用购物车导致的中间产物，未用于结论；订单 13 为干净 2 件。
