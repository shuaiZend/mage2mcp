# 语义层测试 A：商品与目录（catalog）

- **测试日期**：2026-11-22
- **被测版本**：生产 v0.11.5（`platform.info` 返回 `"version":"v0.11.5"`）
- **端点**：`http://127.0.0.1:18110/mcp`（SSH 隧道到生产；`auth.enabled=false`，按 README 的 live 测试方式）
- **店铺**：`demo-magento-2.auroracreation.com`，store `pl`，instance `demo`
- **令牌/审批**：`/tmp/magento_token`；`MCP_APPROVE_CMD` 走 `ssh ... approve.sh {id}`
- **代码修改**：无。所有测试只读或创建/删除 `MCPTEST-*` 测试对象；没有修改产品代码。

## 0. 环境限制（不是产品缺陷）

1. 生产隧道不稳定：`test/live` 的 initialize 偶尔返回 `EOF`，后续若干调用 `connection refused`。重试后成功；`catalog-read` 的 7 个用例全部 pass。
2. 该部署 `auth.enabled=false`，catalog 测试不依赖 RBAC；鉴权语义在 `docs/audit/semantic/security.md` 的独立鉴权实例上测试。
3. demo 店会周期性重置；测试对象用 `MCPTEST-SEM-*` 前缀并在结束时删除。
4. `magento.product.get` 的 configurable 父价格 0 是 Magento 的真实返回，不是工具伪造；本报告把它记为“解释不足”（SUSPECTED），不是“价格错误”。

复现通用环境：

```bash
export MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo
export MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'"
go run -tags live ./test/live -mode=raw -tool=<tool> -args='{...}'
```

## 1. 结论摘要

| ID | 严重级别 | 状态 | 主题 |
|---|---|---|---|
| SEM-CAT-01 | P2 | **CONFIRMED** | `products.bulk_update` 描述称“任一 SKU 不存在则整单拒绝”，实际逐 SKU 部分成功；重复 SKU 被静默折叠 |
| SEM-CAT-02 | P2 | **SUSPECTED** | configurable 父价格 0 在 `product.get` 中无解释，调用方可能读成免费 |
| SEM-CAT-03 | — | **NOT-A-DEFECT** | 价格精度、status/visibility、搜索 total、category depth 的声明与实测一致 |
| SEM-CAT-04 | — | **NOT-A-DEFECT** | `attributes.options.bulk_create` 重复 label 跳过是描述明确声明的行为 |
| SEM-CAT-05 | — | **NOT-A-DEFECT** | 读路径 7 个 catalog 用例全部 pass；媒体/链接/tier 的既有 live 用例未发现语义偏差 |

---

### SEM-CAT-01: `products.bulk_update` 的“整单拒绝”声明与逐 SKU 部分成功不符；重复 SKU 被静默折叠

- **严重级别**: P2
- **工具**: `magento.products.bulk_update`
- **现象**: 工具描述明确说 “Every SKU must exist or the whole call is rejected.” 实际对两个不存在的 SKU 返回 `is_error=false`，`total=2`、`failed=1`、`updated=0`、`rows` 只有 1 行；没有整单拒绝，也没有说明另一个 SKU 去了哪里。两个输入 SKU 相同，输出只保留一行（重复被静默折叠），`total` 仍按输入计数。
- **期望**: 描述与行为一致：要么先校验全部 SKU 并整单拒绝，要么把“逐 SKU 部分成功 + 重复折叠”写成明确契约，并在结果中给出 `skipped`/`duplicate` 行或计数。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.products.bulk_update \
    -args='{"instance":"demo","skus":["MCPTEST-DUP-20261122","MCPTEST-DUP-20261122"],"status":2}'
  ```

  ```text
  is_error=false code=
  {
    "result": {
      "total": 2,
      "updated": 0,
      "failed": 1,
      "rows": [
        {
          "sku": "MCPTEST-DUP-20261122",
          "status": "failed",
          "error": "validation_failed: Magento rejected the request as invalid; verify store_code and field values"
        }
      ]
    }
  }
  ```

  代码：`internal/magento/product_bulk.go` 的 `BulkUpdateProducts` 对 SKU 做 `strings.ToUpper` 去重后只发唯一 SKU；`Total` 用 `len(skus)`，`Rows` 用 `unique`；描述字符串在 `internal/magento/write_tools.go:2158-2160` 仍写 “Every SKU must exist or the whole call is rejected.”
- **影响**: 自动化调用方按描述预期“有不存在 SKU 就整单失败”，实际已对存在的 SKU 做了写入；重复 SKU 没有可对账的行，`total`/`rows`/`updated+failed` 三者不一致，重试和补偿会误判。
- **状态**: **CONFIRMED**（产品描述/行为缺陷；不是测试脚本问题）。

### SEM-CAT-02: configurable 父价格 0 在 `product.get` 中没有解释

- **严重级别**: P2
- **工具**: `magento.product.get`（同样影响 `product.search`/`products.get` 的 Product 投影）
- **现象**: 已知 configurable 父 `WJ01` 的 `product.get` 返回 `type_id=configurable` 且 `price=0`；`Product.Price` 的 schema 只说 “Base price in the store currency, as a number”，没有说明 configurable 父价格可能为 0/由子商品决定。`docs/write-safety.md` 说 “The configurable parent price is derived from its children”，但工具结果没有这个提示，且 `internal/magento/configurable.go:58` 的注释说空子价格继承父价格，两个说法方向相反（已知 MAG-24）。
- **期望**: 要么在 Product 投影/描述中给出 `price_note`（configurable parent price is derived; read children for actual prices），要么修正文档和代码注释，使三者一致。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.product.get \
    -args='{"instance":"demo","sku":"WJ01"}'
  ```

  ```text
  is_error=false code=
  {
    "product": {
      "sku": "WJ01",
      "name": "Stellar Solar Jacket",
      "type_id": "configurable",
      "price": 0,
      ...
    }
  }
  ```

  `magento.product.search` 同样返回 `WJ01` `price:0`（`total=2057` 的样本中包含它）。
- **影响**: Agent 可能把 `price:0` 读成免费或数据缺失，进而错误报价/筛选；不会造成写入损坏，但属于声明与语义不诚实。
- **状态**: **SUSPECTED**（Magento 返回 0 是事实；缺陷在于工具输出/文档没有解释，且代码注释与 write-safety 文案矛盾）。

### SEM-CAT-03: 价格精度、status/visibility、搜索分页与 category depth

- **严重级别**: —（NOT-A-DEFECT）
- **验证**:
  - **价格精度**：创建 `MCPTEST-SEM-013206`，`price:"19.9999"`；create 与 get 都返回 `price:19.9999`（不是 19.99/20.0），删除返回 `deleted:true`。
  - **status/visibility**：创建 `MCPTEST-SEM-STAT-013408`，`status:2, visibility:1`；create 返回 `status:2, visibility:1`，未静默改成默认 1/4；删除成功。
  - **搜索分页**：`product.search limit=1` 返回 1 个 product 和 `total=2057`；`total` 明确大于返回行数，没有伪称完整列表。
  - **category depth**：`category.tree depth=2` 返回扁平列表，depth 是显式输入；没有发现“截断但不标记”的路径。
- **复现**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.product.create \
    -args='{"instance":"demo","sku":"MCPTEST-SEM-013206","name":"Semantic Price Probe","attribute_set_id":4,"type_id":"simple","price":"19.9999","quantity":0,"status":1,"visibility":4,"website_ids":[1]}'
  go run -tags live ./test/live -mode=raw -tool=magento.product.get -args='{"instance":"demo","sku":"MCPTEST-SEM-013206"}'
  go run -tags live ./test/live -mode=raw -tool=magento.product.delete -args='{"instance":"demo","sku":"MCPTEST-SEM-013206"}'
  ```

  ```text
  create/read: "price": 19.9999
  delete: {"sku":"MCPTEST-SEM-013206","deleted":true}
  ```

- **状态**: **NOT-A-DEFECT**。

### SEM-CAT-04: `attributes.options.bulk_create` 重复 label 跳过

- **严重级别**: —（NOT-A-DEFECT）
- **验证**: 描述明确说 “Duplicate labels are skipped; earlier options stay on failure.” 实测 `labels:["MCPTEST-SEM-DUP-...","MCPTEST-SEM-DUP-..."]` 返回 `total=2, created=1, failed=0, rows=1`；跳过是声明的行为，不是静默丢参数。删除创建的 option 215 成功。
- **证据**:

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.attributes.options.bulk_create \
    -args='{"instance":"demo","attribute_code":"color","labels":["MCPTEST-SEM-DUP-013505","MCPTEST-SEM-DUP-013505"]}'
  ```

  ```text
  {"result":{"total":2,"created":1,"failed":0,"rows":[{"label":"MCPTEST-SEM-DUP-013505","value":"215","status":"created"}]}}
  ```

  删除：

  ```bash
  go run -tags live ./test/live -mode=raw -tool=magento.attributes.options.delete \
    -args='{"instance":"demo","attribute_code":"color","option_id":215}'
  # {"attribute_code":"color","option_id":215,"deleted":true}
  ```

- **影响**: `total` 与 `created+failed` 不一致，但描述已声明 duplicate skipped；这是可解释的，不构成产品缺陷（建议未来加 `skipped` 字段）。
- **状态**: **NOT-A-DEFECT**。

### SEM-CAT-05: 读路径与既有 live 用例

- **严重级别**: —（NOT-A-DEFECT）
- **验证**:

  ```bash
  MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
    go run -tags live ./test/live -mode=read -out /tmp/catalog-read.json
  ```

  `catalog-read` 7/7 pass：attribute.sets、attributes.list、attributes.options、categories.flat、category.tree、category.get、category.products；`product-read` 9/9 pass（product.get/products.get/search/seo/tier_prices/media/options/links/media.types）。`configurable-read` 1/1 pass。后续 sales/import 用例因隧道 `EOF`/`connection refused` 失败，属于环境限制；catalog 范围没有失败。
- **状态**: **NOT-A-DEFECT**（环境限制已单独记录）。

## 2. 覆盖但未发现缺陷的语义点

- `magento.product.create` 的 `status`、`visibility`、`price` 未被静默丢弃；`quantity` 在 create 中通过独立 inventory 调用写入，失败会报 partial（本次 quantity=0 创建成功）。
- `magento.product.delete` 返回 `deleted:true`；重复删除会由 Magento 返回 not_found（未在本轮单独重复调用）。
- `magento.product.search` 的 `total` 与返回行数不同，但没有伪称完整列表。
- `magento.product.tier_prices.set` 的 1–50、正价格/数量、重复键校验在代码/单测中明确；本轮未对 demo 写入 tier 数据。
- `magento.category.create/update` 的 `is_active`/`include_in_menu` 默认 true 在代码中显式处理；未发现静默 false。
- `magento.configurable.create` 的 partial/outcome/stages/operation_id 在既有 live 用例中覆盖；本轮未重新创建 configurable，避免污染 demo。

## 3. 测试脚本/环境限制清单

| 现象 | 归类 | 证据 |
|---|---|---|
| `test/live` initialize 偶发 `EOF`；后续 `connection refused` | **环境限制**（SSH 隧道/生产端点） | `catalog-read` 全部 pass；重试后 write 用例成功 |
| `magento.product.get` 返回 configurable `price:0` | **Magento 真实值**；解释不足见 SEM-CAT-02 | WJ01 |
| `attributes.options.bulk_create` `total` 与 rows 不一致 | **描述已声明 duplicate skipped**，非静默 | SEM-CAT-04 |
| `products.bulk_update` `total`/rows 不一致 | **产品描述与行为不符**，见 SEM-CAT-01 | duplicate + nonexistent SKU |

## 4. 建议优先级

1. **SEM-CAT-01（P2）**：修正 `bulk_update` 描述为逐 SKU partial，并增加 `skipped`/duplicate 行；或实现真正的整单预检。当前描述会让调用方误以为不会发生部分写入。
2. **SEM-CAT-02（P2 SUSPECTED）**：在 Product 输出中加 configurable price 说明，或统一 `write-safety` 与 `configurable.go` 的“父/子价格继承”方向。
3. 其余为 NOT-A-DEFECT；`bulk_create` 的 `total` 建议未来补 `skipped` 字段（非阻塞）。

## 5. 清理

- 已删除 `MCPTEST-SEM-013206`（price probe）、`MCPTEST-SEM-STAT-013408`（status probe）和 color option 215。
- 未创建 configurable/category/media/link/tier 测试对象；demo 店未留下本轮 `MCPTEST-SEM-*` 对象。
