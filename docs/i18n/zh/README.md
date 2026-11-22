<!-- translated-from: README.md @ 0326e3c -->

# mage2mcp — 中文入门

Magento 2 的 MCP 服务器。AI agent 通过 MCP 与它对话，它用 REST 与 Magento 对话；agent
不持有任何 Magento 凭据。

其他语言：[English](../../../README.md) · [Español](../es/README.md) · [Nederlands](../nl/README.md) ·
完整教程：[中文](tutorial.md)。

## 它提供什么

- **105 个工具**：商品与目录、订单与资金（发票、发货、贷项）、库存与 MSI、客户与地址、
  导入、报表，以及 Cloudflare 与 GA4 的读取
- **默认拒绝的授权**：`read < write < destructive < admin`，按实例绑定
- **两步人工审批**：每次写操作由**另一个真人**批准，绑定到工具、实例与参数哈希
- **操作账本**：每次写操作都有 `intent` 与 `outcome`，结果不明确时记为 `operation_unknown`
  并提供查询与处置工具
- **幂等**：带 `idempotency_key` 的重试会重放结果，而不是重复执行

## 快速开始

```bash
go build -o bin/mage2mcp ./cmd/server
go build -o bin/mage2mcp-admin ./cmd/admin

cp configs/config.example.yaml config.yaml
./bin/mage2mcp -config config.yaml

curl -s localhost:8080/readyz   # {"status":"ready","tools":1}
```

`tools: 1` 是正确的：随包的示例配置未启用任何数据源，只暴露 `platform.info`。启用 Magento
数据源后工具数会变成 105。

MCP 端点是 Streamable HTTP 的 `POST /mcp`：

```json
{
  "mcpServers": {
    "mage2mcp": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

## 三个必须知道的事

1. **写工具默认关闭。** 需要 `tools.writes: true`，且每次写操作由另一个真人通过管理 CLI
   批准。审批需要 `database.url`（PostgreSQL）。
2. **`instance` 必填。** 每个 Magento 工具都要指名一个已配置的实例，拼错会明确报错，而不是
   静默读到别的店铺。
3. **结果不明确时不要重试。** `operation_unknown` 表示上游可能已经执行。用
   `magento.operation.get` 查，用 `magento.operation.dispose` 给出人工判断。

## 接下来读

| 目标 | 文档 |
|---|---|
| 完整上手教程 | [中文教程](tutorial.md) |
| 每个工具的说明 | [mcp-tools.md](../../mcp-tools.md) |
| 配置参考 | [configuration.md](../../configuration.md) |
| 生产部署 | [operations/deployment.md](../../operations/deployment.md) |
| 已知限制 | [audit/known-limitations.md](../../audit/known-limitations.md) |

## 安全提示

随包的开发配置**关闭了认证**、使用固定占位密码，只允许回环监听。**绝不要**把它暴露到
网络上。生产部署请从 `configs/config.production.template.yaml` 开始，并提供
`MAGE2MCP_AUTH_*` 与 `MAGE2MCP_DATABASE_URL`。
