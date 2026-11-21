# 商品运营岗位场景测试（v0.11.6）

我的日常任务：新品上新。建商品、写描述和 SEO、传图、设分类、配阶梯价、建可配置商品
（颜色/尺码），最后做一次批量改价。

我只有一条底线：**上架那一刻起，顾客能在目录页看到它、能选到选项、能加进购物车、价格是我定的那个数**。
工具报错我能改；工具说成功但前台没生效、或者选项悄悄丢了、价格悄悄变成 0，我会直接丢掉一个上新档期。

## 环境与快照

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | v0.11.6（`platform.info` 实测，102 个工具） |
| 实例 id | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store `pl`（PLN） |
| 数据快照 | `2026-11-21T19:25Z ~ 19:34Z`（下文前台价格、库存、阶梯价都绑定这个窗口） |
| 属性集 | `4` = Default（含 `color` attribute_id **93**，`size` 未用） |

```bash
export MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo
export MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"

go run -tags live ./test/live -mode=raw -tool=<工具名> -args='{...}'
```

> 下文命令都是上面这条规范形式（我实测时把 `go run` 预编译成 `/tmp/mcpraw`，并用一个薄封装
> 传 JSON，参数、端点、approval 命令完全一致）。每条结论都用**不经过 MCP 的 REST 直连**独立复核：
> `curl -H "Authorization: Bearer $(cat /tmp/magento_token)" https://demo-magento-2.auroracreation.com/rest/pl/V1/...`
> 前台核对用 `curl` 抓页面 HTML（storefront 有 FPC 全页缓存，需要带 `?cb=<随机>` 破缓存；
> 加购需要同时带 `form_key` 隐藏域值与 `form_key` cookie，否则 Magento 2.4.7+ 报 `Invalid Form Key`）。

### 本轮留在 demo 站的数据（便于回归复现）

| 对象 | 值 | 用途 |
|---|---|---|
| 分类 55 | `MERCH Nowosci 20261122-0326`（parent 2） | 目录页可见性 |
| 简单商品 | `MERCH-MATA-20261122-0326`（id 2140）「Mata do jogi MERCH Pro 6 mm」129.00 → 139.00 | 简单商品全流程 + 阶梯价 |
| 可配置父商品 | `MERCH-BLUZA-20261122-0326`（id 2144）「Bluza MERCH z kapturem」 | 两色可配置商品 |
| 子商品 | `...-GRA`（id 2142，Graphite，239→229，库存 5）、`...-SND`（id 2143，Sand，259→249，库存 3） | 选项/价格/库存 |
| 可配置父商品 | `MERCH-CZAPKA-20261122-0326`「Czapka MERCH z daszkiem」 | 子商品未给价时的默认价验证 |
| 颜色选项 | `MERCH-Graphite-20261122-0326`（237）、`MERCH-Sand-20261122-0326`（238） | 选项标签是否保留 |
| 已删除 | `ZZ-PROBE-COLOR-1`（直连 REST 探测用） | `product.delete` 复核通过 |

---

## 一、五个上新动作的核对结果

### 1) 建简单商品（描述 + SEO + 图片）→ ✅ 前台可买

```bash
# 分类（顺带验证 category.create）
... -tool=magento.category.create -args='{"instance":"demo","parent_id":2,"name":"MERCH Nowosci 20261122-0326","url_key":"merch-nowosci-20261122-0326","is_active":true,"include_in_menu":true}'
# 商品（描述 700+ 字符波兰语、meta_title、meta_description、url_key、weight、库存 12、分类 55）
... -tool=magento.product.create -args="$(cat /tmp/args_simple.json)"
# 主图（800x800 PNG，base64）
... -tool=magento.product.media.upload -args="$(cat /tmp/args_media.json)"
# 复核
... -tool=magento.inventory.status -args='{"instance":"demo","sku":"MERCH-MATA-20261122-0326"}'
... -tool=magento.product.seo -args='{"instance":"demo","sku":"MERCH-MATA-20261122-0326"}'
```

```json
{"category": {"id": 55, "parent_id": 2, "name": "MERCH Nowosci 20261122-0326", "is_active": true, "level": 2, "path": "1/2/55"}}
{"product": {"id": 2140, "sku": "MERCH-MATA-20261122-0326", "type_id": "simple", "status": 1, "visibility": 4,
  "price": 129, "attribute_set_id": 4, "category_ids": [55], "website_ids": [1],
  "url_key": "mata-do-jogi-merch-pro-6-mm",
  "meta_title": "Mata do jogi MERCH Pro 6 mm | antyposlizgowa mata TPE",
  "meta_description": "Mata do jogi MERCH Pro 6 mm z pianki TPE: stabilna przyczepnosc, ochrona stawow i pasek w zestawie. Zamow online z dostawa w 24 h."}}
{"status": {"sku": "MERCH-MATA-20261122-0326", "qty": 12, "is_in_stock": true, "manage_stock": true, "backorders": 0}}
{"audit": {"sku": "MERCH-MATA-20261122-0326", "score": 100, "checks": [description pass, short_description pass,
  url_key pass, meta_title pass, meta_description pass, meta_keywords warn(optional)]}}
{"media": {"id": 3427, "media_type": "image", "label": "Mata do jogi MERCH Pro 6 mm", "position": 1,
  "types": ["image","small_image","thumbnail"], "file": "/m/e/merch-mata-hero.png"}}
```

前台（`GET /mata-do-jogi-merch-pro-6-mm.html`，HTTP 200，75 KB）：

```text
<title>Mata do jogi MERCH Pro 6 mm | antyposlizgowa mata TPE</title>
<meta name="description" content="Mata do jogi MERCH Pro 6 mm z pianki TPE: ...">
data-price-amount="129"   /   <span>W magazynie</span>   /   "Dodaj do koszyka"
img src=/media/catalog/product/cache/.../m/e/merch-mata-hero.png
form action=.../checkout/cart/add/.../product/2140/
```

真加购（`POST /checkout/cart/add/product/2140/`，qty=2）：

```text
HTTP/1.1 302
mage-messages: [{"type":"success","text":"You added Mata do jogi MERCH Pro 6 mm to your shopping cart."}]
购物车页：data-cart-item-id="MERCH-MATA-20261122-0326"，qty=2，小计 <span class="price">258,00 zł</span>
```

**结论**：简单商品一次建对，描述/SEO/图片/库存/分类全部落到前台，顾客能加购、金额正确。

### 2) 建可配置商品（两色，各带库存）→ ⚠️ 能买、选项没丢，但父商品价格被静默丢弃

```bash
... -tool=magento.attributes.options.create -args='{"instance":"demo","attribute_code":"color","label":"MERCH-Graphite-20261122-0326"}'
... -tool=magento.attributes.options.create -args='{"instance":"demo","attribute_code":"color","label":"MERCH-Sand-20261122-0326"}'
... -tool=magento.configurable.create -args="$(cat /tmp/args_conf.json)"
# parent price 249.00 / GRA price 239.00 qty 5 / SND price 259.00 qty 3，variant_attributes=[{attribute_id:93,code:"color",values:[237,238]}]
```

```json
{"outcome": "completed", "operation_id": "cfg_a34b0e13915388ae",
 "parent": {"id": 2144, "sku": "MERCH-BLUZA-20261122-0326", "type_id": "configurable", "price": 0, "visibility": 4},
 "children": [{"id": 2142, "sku": "...-GRA", "visibility": 1, "price": 239, "attributes": {"color": "237"}},
              {"id": 2143, "sku": "...-SND", "visibility": 1, "price": 259, "attributes": {"color": "238"}}],
 "parent_in_stock": true, "parent_stock_source": "source_items: 2 of 2 children in stock, 8 units observed",
 "stages": [precheck completed, children "created 2, reused 0", parent created,
            links "linked 2, reused 0, verified 2 of 2", stock completed]}
```

前台可配置页 `GET /bluza-merch-z-kapturem.html`（HTTP 200，90 KB）——**选项没丢**：

```text
价格：<span class="price-label">Już od</span> <span class="price">239,00 zł</span>
jsonConfig.attributes.93 = {"code":"color","label":"Color","options":[
    {"id":"237","label":"MERCH-Graphite-20261122-0326","products":["2142"]},
    {"id":"238","label":"MERCH-Sand-20261122-0326","products":["2143"]}]}
jsonConfig.index = {"2142":{"93":"237"},"2143":{"93":"238"}}
jsonConfig.optionPrices = {"2142":{finalPrice:239},"2143":{finalPrice:259}}
jsonConfig.images = {"2142":[{... merch_hoodie_graphite.png}],"2143":[{... merch_hoodie_sand.png}]}
```

真加购（`product=2144&selected_configurable_option=2142&super_attribute[93]=237&qty=1`）：

```text
HTTP/1.1 302
mage-messages: [{"type":"success","text":"You added Bluza MERCH z kapturem to your shopping cart."}]
购物车：data-cart-item-id="MERCH-BLUZA-20261122-0326-GRA"，qty=1，单价 <span class="price">239,00 zł</span>
可选属性：Color = MERCH-Graphite-20261122-0326
```

**结论**：颜色选项、子商品映射、变体图、按变体计价全部正确，顾客能选中具体颜色并下单。
**但**：我传的父商品价 `249.00` 没生效（存储与返回都是 `0`），前台显示「Już od 239,00 zł」（取子商品最低价）。
控制实验（见「排除的疑似缺陷」）证明这是 Magento 对 configurable 的行为，不是网关 bug，
但工具没有告知/校验（详见 `P-MERCH-05`）。
另外子商品 `visibility=1`（不单独展示）是对的：前台搜索 `/catalogsearch/result/?q=Bluza+MERCH`
只返回父商品（以及因 "MERCH" 命中的另一个商品），**没有**出现 `...-GRA` / `...-SND` 这两个重复条目。

子商品不给价时，工具会用父商品价兜底（这点做得对）：

```bash
... -tool=magento.configurable.create -args="$(cat /tmp/args_conf2.json)"   # 父 89.00，两个子商品都不传 price
```

```json
{"outcome":"completed","operation_id":"cfg_22155c0e5655e3f1",
 "children":[{"sku":"MERCH-CZAPKA-20261122-0326-GRA","price":89},{"sku":"MERCH-CZAPKA-20261122-0326-SND","price":89}]}
前台 GET /czapka-merch-z-daszkiem.html → <span class="price">89,00 zł</span>，optionPrices 2145/2146 均为 89
```

### 3) 设阶梯价 → ❌ MCP 工具静默失效，最后只能绕过 MCP 才让价格随数量变化

我的意图：`买 5 → 119.00`、`买 10 → 109.00`（customer_group 用 Magento 的 ALL GROUPS 常量 `32000`）。

```bash
... -tool=magento.product.tier_prices.set -args='{"instance":"demo","sku":"MERCH-MATA-20261122-0326","prices":[{"price":119.00,"customer_group":"32000","quantity":5},{"price":109.00,"customer_group":"32000","quantity":10}]}'
```

工具说成功：

```json
{"sku": "MERCH-MATA-20261122-0326", "prices": 2, "set": true}
```

读回来什么都没有（MCP 读 + REST 直连都为空）：

```json
... -tool=magento.product.tier_prices -args='{"instance":"demo","sku":"MERCH-MATA-20261122-0326"}'
{"prices": []}

curl .../rest/V1/products/MERCH-MATA-20261122-0326  →  "tier_prices": []
```

同一个请求直连 Magento，真相是 **HTTP 200 + 错误体**（波兰语：该 customer group 不存在）：

```bash
curl -X POST .../rest/V1/products/tier-prices \
  -d '{"prices":[{"price":119.0,"price_type":"fixed","website_id":0,"sku":"MERCH-MATA-20261122-0326","customer_group":"0","quantity":5}]}'
```

```json
[{"message":"Brak takiego podmiotu z grupą klientów =%customerGroup. ...","parameters":["MERCH-MATA-20261122-0326","0","0","5"]}]
http=200
```

把 `customer_group` 换成 **组代码**（`ALL GROUPS` / `General` / `NOT LOGGED IN`）后同样的调用才真正落库：

```bash
curl -X POST .../rest/V1/products/tier-prices \
  -d '{"prices":[{"price":119.0,"price_type":"fixed","website_id":0,"sku":"MERCH-MATA-20261122-0326","customer_group":"ALL GROUPS","quantity":5},
                 {"price":109.0,"price_type":"fixed","website_id":0,"sku":"MERCH-MATA-20261122-0326","customer_group":"ALL GROUPS","quantity":10}]}'
```

```json
"tier_prices": [{"customer_group_id":32000,"qty":5,"value":119},{"customer_group_id":32000,"qty":10,"value":109}, ...]
```

阶梯价生效后，前台确实「价格随数量变化」：

```text
产品页：<ul class="prices-tier items">
          <li>Kup 5 po <span data-price-amount="119">119,00 zł</span> za każdy i zaoszczędź 8 %</li>
          <li>Kup 10 po <span data-price-amount="109">109,00 zł</span> za każdy ...</li>
购物车（qty=5）：单价 <span class="price">119,00 zł</span>，小计 <span class="price">595,00 zł</span>
改价到 139 之后：产品页 "Kup 5 po 119,00 zł ... zaoszczędź 14 %"（基准价变了，折扣率自动重算）
```

**结论**：阶梯价的**前台效果是好的**（阶梯表 + 购物车单价都随数量变），但**通过 MCP 设不进去**：
工具把 Magento 的 200+错误体当成功，返回 `set: true` 却一行没写（`P-MERCH-01`）。
读取工具还把价格字段映射错了，读出来永远是 0（`P-MERCH-02`）。
我用 REST 直连才完成这一步，并把「工具声称 replace、实际 append」的问题也测出来了（`P-MERCH-03`）。

### 4) 归类到目录 → ✅ 目录页可见（两条路径都对）

```bash
# 路径 A：建商品时带 category_ids:[55]
... -tool=magento.product.create -args="$(cat /tmp/args_simple.json)"
# 路径 B：后置归类
... -tool=magento.category.assign_product -args='{"instance":"demo","category_id":4,"sku":"MERCH-CZAPKA-20261122-0326","position":3}'
```

```json
{"category_id": 4, "sku": "MERCH-CZAPKA-20261122-0326", "assigned": true}
```

前台目录页 `GET /merch-nowosci-20261122-0326.html`：

```text
product-item-link → "Mata do jogi MERCH Pro 6 mm" / "Bluza MERCH z kapturem" / "Czapka MERCH z daszkiem"
data-price-amount → 139 / 229 / 89     （改价后的实际价）
product-image-photo ×2（czapka 我没传图，走占位图）/ toolbar-number = 3
```

后置归类的商品在 `Gear/Bags`（id 4）第 2 页出现：`/gear/bags.html?p=2` → `Czapka MERCH z daszkiem`。
但用 MCP 读目录商品时拿到的是**一堆零值**（`P-MERCH-07`）：

```json
... -tool=magento.category.products -args='{"instance":"demo","category_id":55,"limit":50}'
{"products": [
  {"id": 0, "sku": "MERCH-MATA-20261122-0326", "name": "", "type_id": "", "status": 0, "visibility": 0, "price": 0, ...},
  {"id": 0, "sku": "MERCH-BLUZA-20261122-0326", "name": "", "type_id": "", "status": 0, "visibility": 0, "price": 0, ...}],
 "total": 2}
```

### 5) 批量改价（多 SKU）→ ⚠️ 没有批量改价，只能逐 SKU；父商品改价还是空转

```bash
# 想批量：直接拒绝
... -tool=magento.products.bulk_update -args='{"instance":"demo","skus":["MERCH-MATA-20261122-0326","MERCH-BLUZA-20261122-0326"],"price":"99.00"}'
```

```json
{"code":"invalid_argument","details":{"validation":"validating root: unexpected additional properties [\"price\"]"},
 "message":"arguments do not match the tool schema"}
```

`bulk_update` 只能改 status/visibility/attributes：

```json
... -tool=magento.products.bulk_update -args='{"instance":"demo","skus":["MERCH-MATA-20261122-0326","MERCH-BLUZA-20261122-0326"],"visibility":4}'
{"result": {"total": 2, "updated": 2, "failed": 0, "rows": [{"sku":"MERCH-MATA-20261122-0326","status":"updated"},{"sku":"MERCH-BLUZA-20261122-0326","status":"updated"}]}}
```

于是 3 个 SKU 的改价 = 3 次 `product.update` = **3 次人工审批**（每次都是 `confirmation_required` → `approve.sh` → 重发）：

```bash
... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-MATA-20261122-0326","price":"139.00"}'   # → price 139
... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326-GRA","price":"229.00"}' # → price 229
... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326-SND","price":"249.00"}' # → price 249
```

前台破缓存后立刻跟上（`?cb=` 破 FPC；不带破缓存参数的旧页面仍可能是旧价，这是 storefront 缓存行为）：

```text
/mata-do-jogi-merch-pro-6-mm.html        → 139,00 zł（阶梯 119 / 109 保留）
/bluza-merch-z-kapturem.html             → 229,00 zł；optionPrices = {"2142":229, "2143":249}
购物车重新加 1 件 mata                    → 单价 139,00 zł
```

但**改父商品价格是空转**（`P-MERCH-05`）：

```bash
... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326","price":"199.00"}'
```

```json
{"product": {"sku": "MERCH-BLUZA-20261122-0326", "type_id": "configurable", "price": 0}}   # 工具说更新成功
前台：<span class="price">239,00 zł</span>，optionPrices 仍为 239/259                        # 一点没变
```

---

## 二、缺陷清单

### P-MERCH-01: `tier_prices.set` 把 Magento 的 200+错误体当成功，返回 `set: true` 却一行没写

- **级别**: P1
- **我的工作流**: 新品定阶梯价：买 5 件 119、买 10 件 109（customer_group 用 Magento 常量 `32000` = ALL GROUPS）
- **发生了什么**:

  ```bash
  ... -tool=magento.product.tier_prices.set -args='{"instance":"demo","sku":"MERCH-CZAPKA-20261122-0326-SND","prices":[{"price":79.00,"customer_group":"0","quantity":3}]}'
  ```

  ```json
  {"sku": "MERCH-CZAPKA-20261122-0326-SND", "prices": 1, "set": true}
  ```

  紧接着两种读法都证明**库里没有**：

  ```json
  ... -tool=magento.product.tier_prices ...   →  {"prices": []}
  curl .../rest/V1/products/MERCH-CZAPKA-20261122-0326-SND  →  "tier_prices": []
  ```

  同一个 payload 直连 Magento 的真实响应是 **HTTP 200 + 错误数组**：

  ```json
  [{"message":"Brak takiego podmiotu z grupą klientów =%customerGroup. ...","parameters":["MERCH-CZAPKA-20261122-0326-SND","0","0","3"]}]
  http=200
  ```

  换成组代码 `ALL GROUPS` 再走 MCP，同一个工具就能落库（说明通道本身是通的，只是失败没被判出来）：

  ```json
  ... -tool=magento.product.tier_prices.set -args='{"instance":"demo","sku":"MERCH-CZAPKA-20261122-0326-GRA","prices":[{"price":79.00,"customer_group":"ALL GROUPS","quantity":3},{"price":69.00,"customer_group":"ALL GROUPS","quantity":6}]}'
  → {"prices": 2, "set": true}；REST 直连："tier_prices": [{qty:3,value:79},{qty:6,value:69}]
  ```

- **我期望什么**: `POST /V1/products/tier-prices` 是本店唯一「HTTP 200 也可能整体失败」的写接口，
  网关必须解析响应体里的错误数组，非空即报错（并且错误里要带 Magento 原文，否则我不知道
  「customer_group 要传组代码，不是 `customer.groups` 返回的 id」）。工具自己的 schema 只写
  `customer_group: string`，读模型又把 id 当 `customer_group` 返回，等于把我往坑里带。
- **状态**: CONFIRMED（MCP 与 REST 双向对齐；同 SKU 用组代码可成功，排除环境故障）

### P-MERCH-02: `tier_prices` 读取字段映射错误，价格恒为 0、SKU 为空

- **级别**: P1
- **我的工作流**: 设完阶梯价，读回来确认「买 5 件是不是 119」
- **发生了什么**:

  ```json
  ... -tool=magento.product.tier_prices -args='{"instance":"demo","sku":"MERCH-CZAPKA-20261122-0326-GRA"}'
  {"prices": [
    {"sku": "", "price": 0, "price_type": "", "website_id": 0, "customer_group": "32000", "quantity": 3},
    {"sku": "", "price": 0, "price_type": "", "website_id": 0, "customer_group": "32000", "quantity": 6}]}
  ```

  Magento 实际返回的每一项是 `{"customer_group_id":32000,"qty":3,"value":79,"extension_attributes":{"website_id":0}}`
  —— 工具把 `quantity`/`customer_group` 映射对了，但 `price` 取了一个 Magento 不返回的字段（应为 `value`），
  `sku`、`price_type` Magento 本来就不返回，工具却填了零值。
- **我期望什么**: 读回来的价格必须等于真实阶梯价（79/69）。现在读一次就是「所有阶梯价都是 0 元」，
  我会误以为上新价配错了，回头再改一遍，越改越乱。
- **状态**: CONFIRMED（REST 直连同刻对比）

### P-MERCH-03: `tier_prices.set` 文档说「replaces」，实际是 append，旧阶梯删不掉

- **级别**: P2
- **我的工作流**: 促销结束，把「买 3 / 买 6」两档换成「买 12」一档
- **发生了什么**:

  ```bash
  # 先设两档（3→79、6→69，已验证落库）
  ... -tool=magento.product.tier_prices.set -args='{"instance":"demo","sku":"MERCH-CZAPKA-20261122-0326-GRA","prices":[{"price":59.00,"customer_group":"ALL GROUPS","quantity":12}]}'
  ```

  ```json
  {"sku": "MERCH-CZAPKA-20261122-0326-GRA", "prices": 1, "set": true}
  # REST 直连复核：三档都在，旧的两档没被删
  "tier_prices": [{"qty":3,"value":79},{"qty":6,"value":69},{"qty":12,"value":59}]
  ```

  工具描述原文：「Magento's storage interface replaces the SKU's stored tiers, so omitted tiers are removed.」
- **我期望什么**: 要么真的 replace（先删后写），要么在描述里说清「只追加，删档需要另外的接口」。
  现在工具既没有删除能力，又声称会删，我只能看到前台多出两档已经不该卖的价。
- **状态**: CONFIRMED（描述与实测不一致；Magento 的 POST 语义本身是追加）

### P-MERCH-04: `products.bulk_update` 明确不含 price → 批量改价只能逐 SKU、逐个审批

- **级别**: P2
- **我的工作流**: 季末把 3 个（真实场景 20+ 个）SKU 统一改价
- **发生了什么**: 带 `price` 直接被 schema 拒绝：

  ```json
  {"code":"invalid_argument","details":{"validation":"validating root: unexpected additional properties [\"price\"]"}}
  ```

  只能 `product.update` 逐个来，每个 SKU 一次 `confirmation_required` → `approve.sh` → 重发（实测 3 SKU = 3 次审批）。
  另外可配置商品的父商品价格必须改子商品才有前台效果，所以「一个商品」往往等于「N 个子商品 + N 次审批」。
- **我期望什么**: 商品运营的批量动作里改价是最高频的（`bulk_update` 描述也承认「Name and price are not
  part of the bulk update」）。至少要有 `products.bulk_price_update`（一次审批、按 SKU 报每行结果），
  否则一次 50 SKU 的季末调价要 50 次人工确认，实际不可用。
- **状态**: CONFIRMED（能力缺口，非崩溃）

### P-MERCH-05: 对 configurable 父商品写 price 静默无效，工具返回成功且不提示

- **级别**: P2
- **我的工作流**: 改可配置商品售价 249 → 199
- **发生了什么**:

  ```bash
  ... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326","price":"199.00"}'
  ```

  ```json
  {"product": {"sku": "MERCH-BLUZA-20261122-0326", "type_id": "configurable", "price": 0}}   # 成功，但 price 还是 0
  ```

  前台（破缓存重抓）价格仍是子商品最低价：

  ```text
  /bluza-merch-z-kapturem.html → <span class="price">239,00 zł</span>；optionPrices {"2142":239,"2143":259}
  ```

  直连 REST 复现同样的空转（说明根因在 Magento，不是网关的 REST 调用写错）：

  ```bash
  curl -X PUT .../rest/V1/products/MERCH-BLUZA-20261122-0326 -d '{"product":{"sku":"...","price":199}}'
  → http=200，返回 "price": 0，再 GET 仍是 0
  ```

  必须改子商品才有用（这条是通的）：

  ```bash
  ... -tool=magento.product.update -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326-GRA","price":"229.00"}'
  前台 → 229,00 zł；optionPrices {"2142":229,"2143":249}
  ```

- **我期望什么**: `product.update` 明说「Updates one product ... price」，我传了价、它返回成功、前台没变。
  网关完全有能力在写后回读并判断「configurable 父商品的 price 由子商品决定，本次写入被 Magento 忽略」，
  至少应该给一个 warning（或在 schema 描述里写死这条规则）。
- **状态**: CONFIRMED（根因 Magento；网关缺少校验/告警，属语义层缺陷）

### P-MERCH-06: `configurable.create` 相同参数重放返回缓存的 completed 记录，且无 replay 标记

- **级别**: P2
- **我的工作流**: 上一次调用超时/我不确定有没有成功，于是原样再发一次（真实的重复提交场景）
- **发生了什么**: 同一份 `args` 共发 3 次：第 1 次真建，第 2、3 次返回**完全相同**的 payload（同一
  `operation_id`、同一 `created_at`、`stages` 仍写着 `children: "created 2, reused 0"`）：

  ```json
  {"outcome":"completed","operation_id":"cfg_a34b0e13915388ae",
   "parent":{"id":2144,"created_at":"2026-11-21 19:27:57"},
   "stages":[{"name":"precheck","detail":"0 of 2 children already present; parent absent"},{"name":"children","detail":"created 2, reused 0"}]}
  ```

  但目录里始终只有 2 个子商品（没有重复 SKU），说明第二次起是**回放**而不是真执行。
  工具描述说：「a fresh run is not idempotent and refuses a pre-existing SKU」。
- **我期望什么**: 要么真的按文档拒绝（409/conflict，告诉我 SKU 已存在），要么回放时明确标记
  `replayed: true` / `reused`。现在 stages 声称「created 2」而实际什么都没做，我无法判断这次
  上新到底是新建的还是旧的——对账时会把 `created_at` 当成本次上新时间。
- **状态**: CONFIRMED（3 次调用输出逐字节一致；目录无重复）

### P-MERCH-07: `category.products` 返回的商品除 SKU 外全是零值

- **级别**: P2
- **我的工作流**: 上新后核对「这个分类里到底有哪些商品、价格对不对」
- **发生了什么**:

  ```json
  {"products":[{"id":0,"sku":"MERCH-MATA-20261122-0326","name":"","type_id":"","status":0,"visibility":0,"price":0,...},
               {"id":0,"sku":"MERCH-BLUZA-20261122-0326","name":"","type_id":"","status":0,"visibility":0,"price":0,...}],"total":2}
  ```

  Magento 的 `GET /V1/categories/{id}/products` 只返回 `sku`（+ `position`），网关把缺的字段填了零值。
- **我期望什么**: 缺失的字段应当省略（或标 `null`），而不是伪装成 `name: ""`、`price: 0`、`status: 0`。
  按当前输出，我会以为这两个商品没名字、被禁用、卖 0 元；如果拿这个结果去做批量改价/校验，会直接误判。
- **状态**: CONFIRMED

### P-MERCH-08: `product.get` 丢掉 `extension_attributes`，看不到可配置商品的选项与子商品链接

- **级别**: P3
- **我的工作流**: 上新后只查一次父商品，确认「颜色选项和子商品都挂上了」
- **发生了什么**:

  ```json
  ... -tool=magento.product.get -args='{"instance":"demo","sku":"MERCH-BLUZA-20261122-0326"}'
  {"product": {"sku":"MERCH-BLUZA-20261122-0326","type_id":"configurable","price":0,
               "options": null, "extension_attributes": null, ...}}
  ```

  Magento 原生响应里 `extension_attributes` 带 `configurable_product_options` / `configurable_product_links`，
  网关没有透出，只能改用 `configurable.children` + 抓前台 `jsonConfig` 才能确认选项没丢。
- **我期望什么**: 「选项有没有丢」是上新验收的第一问。`product.get` 应该带出 variant 配置（或明确提示
  「用 configurable.children 查」），否则一个只查 product.get 的商品运营会误以为选项丢了，或者更糟——
  误以为没丢。
- **状态**: CONFIRMED

### P-MERCH-09: 所有工具 schema 强制 `instance` 必填，与描述「单实例时由服务端解析」矛盾

- **级别**: P3
- **我的工作流**: 第一次上手，按描述直接调工具（描述写着「The instance input is resolved by the server
  when only one Magento instance is configured」）
- **发生了什么**:

  ```json
  ... -tool=magento.categories.flat -args='{}'
  {"code":"invalid_argument","details":{"validation":"validating root: required: missing properties: [\"instance\"]"},
   "message":"arguments do not match the tool schema"}
  ```

  102 个工具里 **101 个**的 `required` 带 `instance`（只有 `platform.info` 不用），而其中 **57 个**工具的
  描述同时写着「resolved by the server when only one Magento instance is configured」。实测
  `attribute.sets`/`categories.flat`/`inventory.sources`/`inventory.stocks` 全部如此。
- **我期望什么**: 要么 schema 里 `instance` 可选（真的服务端解析），要么把描述改掉。现在描述和 schema
  互相打脸，新接手的运营第一次调用就撞墙。
- **状态**: CONFIRMED（schema 与描述不一致；不影响老手，但影响上手成本）

### P-MERCH-10: `product.create` 带 `quantity` 时响应里没有库存回执

- **级别**: P3
- **我的工作流**: 一次调用建商品 + 给库存，期望响应告诉我库存是否真的写进去了
- **发生了什么**: `product.create` 传 `quantity: 12`，响应只有商品字段，没有库存/quantity 任何信息
  （描述里承诺「When quantity is supplied the product is created first and its default-source inventory is
  assigned afterwards」）。我必须再发一次 `inventory.status` 才知道 12 件在不在。
- **我期望什么**: 响应带一行库存回执（qty / is_in_stock / source），或者至少带 `inventory: {qty: 12}`。
  否则「上架了但没库存」只能靠顾客投诉发现。
- **状态**: CONFIRMED（可用但多一跳；与描述承诺的「分阶段结果」不一致）

---

## 三、排除的「疑似缺陷」（查过，不是网关的锅）

| 疑似现象 | 结论 | 依据 |
|---|---|---|
| 可配置父商品 `price` 恒为 0（`configurable.create` 传 249，存 0） | NOT-A-DEFECT（Magento 行为） | 控制实验：老商品 `WJ01 Stellar Solar Jacket` 的 `product.search`/`product.get` 也是 `price: 0`；前台用子商品最低价渲染「Już od」。网关应加提示（见 P-MERCH-05） |
| 前台 `jsonConfig.salable` 是空数组 | NOT-A-DEFECT | 控制实验：老可配置商品 `/stellar-solar-jacket.html` 同样 `"salable":[]`；且按钮 `disabled` 也相同（选完选项由 JS 解锁）。我的商品真加购成功 |
| `product.create` 响应里 `attributes.color = "49"`（我没传） | NOT-A-DEFECT | Magento 的 POST 响应会把带 default_value 的属性物化进 `custom_attributes`；随后 GET 与 `custom_attributes` 都没有 color。直连 REST POST 复现同样现象 |
| 前台加购报 `Invalid Form Key` | 环境现象（FPC + 2.4.7 双提交 cookie），不是网关 | 页面被全页缓存时不发 PHPSESSID；带 `form_key` cookie 后加购成功（`{"type":"success"...}`）。仅影响我用 curl 复现的方式 |
| 子商品 `visibility` 会不会变成 4 导致目录里出现重复条目 | NOT-A-DEFECT | `configurable.create` 给子商品设了 `visibility: 1`；前台搜索 `Bluza MERCH` 只返回父商品 |
| 子商品不给 `price` 会不会变成 0 元商品 | NOT-A-DEFECT | 工具用父商品价兜底（89.00），前台 `89,00 zł` 正确 |

---

## 四、给修复阶段的优先级建议（只记录，不改代码）

1. **P-MERCH-01 / 02（P1，同一个功能）**：`tier_prices.set` 必须解析 Magento 的 200+错误体；
   `tier_prices` 的 `price` 应取 `value`、`quantity` 取 `qty`、`customer_group` 取 `customer_group_id`，
   `sku` 用入参补齐或省略。修好这两条，阶梯价才算能用。
2. **P-MERCH-05（P2）**：`product.update` 写后可回读校验；对 configurable 的 `price` 明确返回
   warning（「由子商品决定，请改子商品」）。
3. **P-MERCH-03（P2）**：`tier_prices.set` 的 replace 语义要么实现（先删后写），要么改描述。
4. **P-MERCH-04（P2）**：补一个按 SKU 批量改价的工具（一次审批、逐行结果）。
5. **P-MERCH-06 / 07 / 08 / 09 / 10（P2/P3）**：回放标记、零值字段改省略、透出
   `extension_attributes`、`instance` schema 与描述对齐、`product.create` 带库存回执。

## 五、数据漂移与遗留数据

- demo 站会周期性重置，本轮新建的 5 个商品 + 分类 55 + 2 个颜色选项随时可能消失；
  报告里的前台价格/库存都以 `2026-11-21T19:25Z ~ 19:34Z` 为快照。
- 阶梯价状态（快照时刻，REST 直连复核）：
  - `MERCH-MATA-20261122-0326`：`qty 5 → 119`（三行：group 32000/1/0，来自我直连排障时的三次写入）、`qty 10 → 109`
  - `MERCH-CZAPKA-20261122-0326-GRA`：`qty 3 → 79`、`qty 6 → 69`、`qty 12 → 59`（P-MERCH-03 的 append 证据）
  - `MERCH-CZAPKA-20261122-0326-SND`：`[]`（P-MERCH-01 的静默失败证据，故意保留空状态）
- 探测用的 `ZZ-PROBE-COLOR-1` 已用 `product.delete` 删除（返回 `{"sku":"ZZ-PROBE-COLOR-1","deleted":true}`）。
- 其他岗位同时在动同一店铺（目录里能看到 `MCPTEST Category v2 ...` 等并发数据），本报告不涉及这些对象。
