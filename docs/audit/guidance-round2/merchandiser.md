# 照指引做：复制商品（`product_copy` prompt 实测）

测试对象：生产 v0.11.9（隧道 `http://127.0.0.1:18110/mcp`，实例 `demo`，店铺 store `pl` / PLN）。
方法：**只读 + 自建副本**。把 `prompts/get product_copy` 的全文当作唯一手册，照它逐项执行；源商品 `WJ01` 未改；
自建副本以 `R2-COPY-20261123-0300` 前缀命名，核对完成后全部删除（`product.search "R2-COPY"` 现在 `total: 0`）。

**结论：清单方向正确，但有 2 条会直接产出残缺商品的漏项/错项，和 1 条不可执行的创建顺序。**
照清单原样执行，我会得到一个「父商品有图、子商品无图、子商品无库存、且创建顺序被工具拒绝」的副本。

---

## 0. 源商品与清单

**源**：`WJ01` Stellar Solar Jacket（可配置，attribute set 9 `Top`，9 个子商品 = 3 色 × 3 码，价格 75，每个子商品库存 100，父商品相册 3 张图，无阶梯价、无自定义选项、无 meta 字段）。

**清单原文（`prompts/get product_copy`，3,916 字符）关键段**：

```text
1. Discover the source. For each product, record:
   - `magento.product.get` — type, attribute set, status, visibility, `url_key`, price, tier
     prices, custom options, media.
   - `magento.configurable.children` — every child SKU, and for each child its attribute values.
   - `magento.attributes.list` (for the attribute set) — the full attribute set, not the fields
     that happen to be filled in.
   - `magento.product.options` — custom options and their price types.
   - `magento.category.products` or the product's categories — where it is listed.
2. Check what exists on the target. ...
3. Ask for taxonomy changes in writing. ...
4. Create in dependency order. Simple children first (`magento.product.create`), then the
   configurable parent (`magento.configurable.create` with its options and children), then
   media (`magento.product.media.upload`), then category assignment
   (`magento.category.assign_product`), then links (`magento.product.links.add`).
5. Verify like a shopper. `magento.configurable.children` must list every child;
   `magento.product.get` must show the same type and attribute values; `magento.inventory.status`
   must show the parent as in stock. ...
6. Report the gaps. A copy is done only when type, options and attribute values match. ...
```

---

## 1. 照清单执行的记录

| 清单步骤 | 我做了什么 | 结果 |
|---|---|---|
| 1 发现 | `product.get WJ01`、`configurable.children WJ01`、`attributes.list 9`、`product.options WJ01`、`product.get` 的 `category_ids` | 拿到 82 个属性、9 个子商品（color 50/58/60 × size 167/168/169，各 75.00，qty 100）、4 个分类 `[23,8,34,2]`；**tier prices / custom options / media 不在 `product.get` 里**（见 F1） |
| 2 目标核对 | `attribute.sets`（有 9）、`attributes.list 9`、`attributes.options color/size`（有 50/58/60、167/168/169）、`categories.flat`（有 2/8/23/34） | 全部已存在，**无需 taxonomy 变更**，所以第 3 步「书面请求」不触发 |
| 4 创建 | 先按原文顺序试了一次「先建子商品」→ 被拒（见 F2）；改走一次 `configurable.create`（9 子 + 父）→ `outcome: completed`，`operation_id: cfg_2fcfdcbc84269b1e`，stages `children created 9 → parent created → links verified 9 of 9 → stock 900 units` | 父商品 id 2071，9 个子商品全部建成并链接 |
| 4 媒体 | 先只给父商品传 3 张（照清单字面）→ 前台选颜色不换图（见 F3）；再给 9 个子商品各传 1 张 → 正常 | 父相册 3 张 + 子商品 9 张 |
| 4 分类 | 父商品建时即带 `category_ids [23,8,34,2]`（与源一致），未再调 `assign_product` | `product.get` 回读 `[2,8,23,34]`，`category.products 23` 里能看到副本 |
| 5 验证 | `configurable.children`、`product.get`、`inventory.status`、`product.media`、`product.seo` + 前台页面 | 见 §2 |
| 6 报告 | 本文 | — |

## 2. 逐项核对（源 WJ01 vs 副本）

| 项 | 源 | 副本 | 判定 |
|---|---|---|---|
| 类型 | configurable | configurable | ✅ |
| 属性集 | 9 | 9 | ✅ |
| status / visibility | 1 / 4 | 1 / 4 | ✅ |
| 分类 | `[23,8,34,2]` | `[2,8,23,34]`（同集合） | ✅ |
| 站点 | `[1]` | `[1]` | ✅ |
| 价格（父） | 0 | 0（我写的是 75.00，读回 0） | ✅ 与源一致，且正是清单 trap 说的现象 |
| 价格（子） | 9 × 75 | 9 × 75 | ✅ |
| 子商品矩阵 | (50/58/60) × (167/168/169) | 完全相同，`signature set` 逐项相等 | ✅ |
| 子商品 visibility / status | 1 / 1 | 1 / 1 | ✅（工具设的，见 F9） |
| 自定义属性 | climate/material/pattern/style_general/eco_collection/erin_recommends/new/sale/performance_fabric/msrp/msrp_display_actual_price_type | **逐字段相等**（脚本比对，唯一差异是 `image*` 文件名后缀 `_1` 与 `has_options`） | ✅ |
| 图片 | 父 3 张 + 每个子商品 1 张 | 父 3 张 + 每个子商品 1 张；前台 `jsonConfig.images` 按子商品映射 | ✅（**但清单字面只要求父商品媒体，见 F3**） |
| SEO | `product.seo` score 40（meta 全缺） | score 40（同样的 warn 项） | ✅ 仅因源也没写（见 F5） |
| 库存 | 子 100 / 父 qty 0 + is_in_stock true | 子 100 / 父 qty 0 + is_in_stock true | ✅ |
| 前台 | `/stellar-solar-jacket.html` 200，75,00 zł | `/stellar-solar-jacket-r2-copy-20261123-0300.html` 200，75,00 zł，画廊 12 张，搜索 `R2-COPY` 命中 | ✅ |
| 分类页可浏览 | 404（源与副本都一样） | 404 | ⚠️ 店铺层面：两个商品都 404，不是复制缺陷（见 §3 备注） |

---

## 3. 发现（每条：清单原文 / 实际执行 / 判定）

### F1（P1）清单说 `product.get` 带 tier prices / custom options / media，工具不带

- **清单原文**：`magento.product.get` — type, attribute set, status, visibility, `url_key`, price, **tier prices, custom options, media**.
- **实际执行**：`product.get WJ01` 返回的键只有
  `[attribute_set_id, attributes, category_ids, created_at, description, id, name, price, sku, status, type_id, updated_at, url_key, visibility, website_ids]`
  —— 没有 `tier_prices`、没有 `options`、没有 `media_gallery_entries`。这三样要分别用 `magento.product.tier_prices`、`magento.product.options`、`magento.product.media` 读（我读了：tier prices `[]`、options `[]`、media 3 条）。
- **判定**：清单与工具不符。后果不是「报错」，而是**静默漏项**——agent 以为已按清单读到媒体/阶梯价，于是跳过 `product.media`/`product.tier_prices`，复制出的商品没图、没阶梯价。建议把这三个字段从 `product.get` 那一条里删掉，或改成「`magento.product.media` / `magento.product.tier_prices` / `magento.product.options`」。

### F2（P1）第 4 步的创建顺序「先建子商品，再 configurable.create」会被工具拒绝

- **清单原文**：Create in dependency order. **Simple children first** (`magento.product.create`), **then the configurable parent** (`magento.configurable.create` with its options and children)…
- **实际执行**：先建 `R2-COPY-20261123-0300-PROBE`（simple，属性集 9，color 50/size 167，qty 1）→ 再调 `configurable.create` 带这个 child：

  ```json
  {"code":"conflict","details":{"collision_skus":"R2-COPY-20261123-0300-PROBE","operation_id":"cfg_17797ade219c9e73","outcome":"partial","stage":"precheck"},
   "message":"configurable product create failed at precheck: SKU already exists in Magento and a fresh create never overwrites it: R2-COPY-20261123-0300-PROBE (resume with the operation id of the earlier attempt to reuse the products it created)"}
  ```

  stage=precheck、outcome=partial，**没有创建任何商品**。清单末尾提到的 `configurable.create_variations`「when you already have the children」是给**已存在的 parent** 加 child，不能从零建 parent。
- **判定**：清单给出的顺序不可执行，且与它自己的第 4 步措辞（"with its options and children"）自相矛盾。**正确路径是一次 `configurable.create` 由工具先建 children 再建 parent**（我最终这样做，stages 记录 `children created 9 → parent created → links verified 9 of 9`）。建议第 4 步改成「一次 `configurable.create` 建 parent + children；只有给已存在 parent 追加变体时才用 `create_variations`」。

### F3（P1）清单没区分「父商品相册」与「每个子商品的图」——只给父商品传图，前台选颜色不换图

- **清单原文**：…then **media** (`magento.product.media.upload`)…；trap 只说「Media is per store view… check the image roles after upload」。
- **实际执行**：
  1. 照字面只给父商品传 3 张图（red_main 带 `image/small_image/thumbnail`，red_alt1、red_back 无 role）→ 父相册 3 条；但 `product.media R2-COPY-…-S-BLU` 返回 `{"media": []}`，前台 swatch `jsonConfig` 的 `"images": []`，画廊只有父商品 3 张。**选 Blue/Red/Yellow 不会换图**。
  2. 再给 9 个子商品各传 1 张（蓝/红/黄主图，`image/small_image/thumbnail`）→ 前台 `"images":{"2062":[{"img":"…/wj01-blue_main_1_1.jpg",…}]}`，画廊 12 张，选色换图正常。
- **判定**：漏项，而且正是清单开头警告的那种「看起来对、卖不了」的半复制。建议第 4 步明确写「父相册 **+** 每个子商品自己的图（前台按选项换图靠子商品的图）」。

### F4（P2）发现清单漏了库存/数量

- **清单原文**：第 1 步发现项只有 `product.get` / `configurable.children` / `attributes.list` / `product.options` / 分类 —— **没有 `inventory.status`**。
- **实际执行**：我必须额外调 `magento.inventory.status` 才知道源子商品 `qty=100`、父商品 `qty=0, is_in_stock=true`；而 `product.create` 的 `quantity` 是子商品可卖的前提。
- **判定**：漏项。照清单复制出的子商品没有库存 → 前台「能看不能买」，而清单第 5 步才要求「verify the parent is in stock」，那时已经晚了（要先有库存）。建议第 1 步加 `magento.inventory.status`（父 + 每个子商品）。

### F5（P2）发现清单漏了 SEO 字段

- **清单原文**：第 1 步只列 `url_key`、`price`、类型、属性集等；未提 `short_description` / `meta_title` / `meta_description` / `meta_keywords`。
- **实际执行**：源 `WJ01` 本身没写 meta 字段（`product.get` 的 attributes 里没有），副本自然也没有——`product.seo` 两侧都是 `score=40`，warn 项完全相同（short_description/meta_title/meta_description/meta_keywords）。**这次没丢数据只是因为源也没写**。
- **判定**：漏项。源若有 meta_title，照清单不会读、也就不会写。建议第 1 步列出 SEO 字段，或改成「`product.get` 里除系统属性外的所有属性都算发现项」。

### F6（P2）「复制完成的标准」可执行但不可自动化

- **清单原文**：A copy is done only when type, options and attribute values match. …`configurable.children` must list every child; `product.get` must show the same type and attribute values; `inventory.status` must show the parent as in stock.
- **实际执行**：没有对比/差异工具（`product.compare` 之类不存在）。我只能把源与副本各读一遍，再用脚本逐字段比对：`type_id / attribute_set_id / status / visibility / price / category_ids / attributes(11 个自定义属性) / children(color,size,price,visibility,status)`。比对结果见 §2。
- **判定**：标准**可操作但纯手工**，清单没说「拿哪些字段、与谁比、哪些允许不同」（例如 `url_key`、`image` 文件名后缀 `_1`、`has_options` 必然不同）。建议给一份可照抄的核对字段表 + 「允许不同」清单，或补一个对比工具。

### F7（P2）清单要求「检查 image roles」，但媒体工具没有 store 维度

- **清单原文**：Media is per store view. Uploading once does not populate every store view; **check the image roles after** `magento.product.media.upload`.
- **实际执行**：`magento.product.media` 的入参只有 `instance` + `sku`；`magento.product.media.upload` 也没有 `store` 参数。本店有 `pl` / `en` 两个 store view，我只能看到一份（默认作用域）的 roles，无法核对另一个 store view。
- **判定**：清单要求的那一步工具做不到。建议说明「网关只写默认作用域」，或给媒体工具加 store 参数。

### F8（P3）清单没说要取图字节；网关没有下载图片的工具

- **清单原文**：then media (`magento.product.media.upload`) —— 它要 base64 字节。
- **实际执行**：`product.media` 只返回文件路径（`/w/j/wj01-red_main_1.jpg`），网关没有「下载图片」工具。同店复制时我用 storefront 的 media URL `curl` 下载再上传；跨店复制时源店的图只能靠调用方自己在网关外抓。
- **判定**：能力缺口，清单应写明「图片来源由调用方负责」。

### F9（P3）清单没提子商品 visibility 应为 1；这次是工具兜的底

- **清单原文**：未提子商品的 visibility。
- **实际执行**：`configurable.create` 自己把 9 个子商品建成 `visibility=1`（实测每个 child vis=1），所以结果正确。但如果按 F2 的错误顺序用 `product.create` 建 child，agent 很可能给 `visibility=4`，目录/搜索里就会出现重复商品。
- **判定**：漏项（本次由工具兜底）。建议写明「子商品必须 visibility=1」。

### F10（P3，清单做对的地方，值得留）

- **清单原文**：A configurable parent's price is derived from its children; a price you write on the parent may read back as 0.
- **实际执行**：我给父商品写 `price: "75.00"`，`product.get` 回读 `0`；前台显示 `75,00 zł`（来自子商品），`optionPrices` 每个子商品 75。清单这条**准确且有价值**。
- 另外第 5 步「父商品自己的 stock 才算 salable」也准确：副本父商品 `qty=0, is_in_stock=true`，与源一致。

### F11（P3，观察）`has_options` 与 `tax_class_id` 的两个小坑

- 源 `WJ01` 的 `has_options=1`，副本 `has_options=0`；不影响可配置性（children、前台、搜索都正常），属存储字段差异，记录备查。
- `tax_class_id` 不能放进 `attributes` map：`product.create` 直接拒绝
  `{"code":"invalid_argument","message":"tax_class_id must be set through its dedicated field, not the attributes map"}`。
  清单说「the full attribute set」，但没说哪些属性要走 typed 字段（`tax_class_id`/`weight`/`status`/`visibility`/SEO/价格）。这一条工具会用错误信息纠正，代价是一次失败调用。

### 备注：分类页对源与副本都 404

`product.get` 回读的分类与源一致，`category.products 23`（Jackets）里也能看到副本（total 13）；但前台 `/women/tops/jackets.html`、`/jackets.html` 等 URL 对**源商品与副本都返回 404**，源商品页的 breadcrumbs 也是空的。这是店铺层面的现象（不是复制缺陷），但说明「分类页可见」这件事**不能用前台验证**，只能靠 `category.products` 回读。

---

## 4. 如果只按清单字面执行，我会交出什么

1. 子商品先建、`configurable.create` 被 `conflict` 拒绝 → 卡住（F2）；
2. 绕过之后只给父商品传图 → 前台选颜色不换图（F3）；
3. 子商品没有库存（清单没让读 `inventory.status`）→ 前台不可买（F4）；
4. 若源有 SEO 字段也不会被复制（F5）；
5. 没有对比工具，只能手工逐字段核对（F6）。

## 5. 数据卫生

- 副本：`R2-COPY-20261123-0300`（父）+ 9 个子商品（`-S-BLU`…`-L-YEL`）→ 已全部 `product.delete` 删除；`product.search "R2-COPY"` 现在 `total: 0`。
- 探针：`R2-COPY-20261123-0300-PROBE`（为验证 F2 而建）→ 已删除；它触发的那次 `configurable.create` 停在 precheck，未创建任何商品。
- 源商品 `WJ01` 未改：`updated_at` 仍是 `2022-08-26 12:18:47`，9 个子商品仍在。
- 副本上传的 12 张图随商品删除；`product.delete` 返回 `deleted: true` 逐个确认。

## 6. 复现命令（摘）

```bash
# 清单全文
python3 - <<'PY'  # MCP prompts/get
{"jsonrpc":"2.0","id":1,"method":"prompts/get","params":{"name":"product_copy"}}
PY

# 发现
magento.product.get        {"instance":"demo","sku":"WJ01"}
magento.configurable.children {"instance":"demo","parent_sku":"WJ01"}
magento.attributes.list    {"instance":"demo","attribute_set_id":9}
magento.product.media      {"instance":"demo","sku":"WJ01"}
magento.product.tier_prices{"instance":"demo","sku":"WJ01"}
magento.product.options    {"instance":"demo","sku":"WJ01"}
magento.inventory.status   {"instance":"demo","sku":"WJ01-S-Blue"}   # 清单没让做，但不做就没有库存

# 创建（一次搞定 children + parent）
magento.configurable.create  <见 /tmp/copy_args.json：9 children + 2 variant_attributes + parent 11 个自定义属性>

# 媒体（父 3 + 子 9）
magento.product.media.upload {"instance":"demo","sku":"R2-COPY-20261123-0300","filename":"wj01-red_main_1.jpg","mime_type":"image/jpeg","data_base64":"…","types":["image","small_image","thumbnail"],"position":1}

# 核对
magento.configurable.children / magento.product.get / magento.inventory.status / magento.product.media / magento.product.seo

# 清理
magento.product.delete {"instance":"demo","sku":"R2-COPY-20261123-0300"}   # 先父
magento.product.delete {"instance":"demo","sku":"R2-COPY-20261123-0300-S-BLU"}  # 再 9 个子
```
