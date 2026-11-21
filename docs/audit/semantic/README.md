# 语义层测试记录

本轮目标：对已发布版本（生产 v0.11.5）做**语义层**测试——不查路由是否存在，而查
「工具声称的行为」与「Magento 实际行为」是否一致。路由层已在前一轮系统审计中确认无缺陷
（52 条实测全部正确，schema 407 条路由比对通过）。

## 环境

| 项目 | 值 |
|---|---|
| 网关端点 | `http://127.0.0.1:18110/mcp`（SSH 隧道到生产 `49.51.250.83:8080`） |
| 版本 | v0.11.5 |
| 实例 id | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store code `pl`（PLN） |
| 直接 API 令牌 | `/tmp/magento_token`（会过期，重新获取见下） |
| 审批命令 | `ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'` |

重新获取管理员令牌：

```bash
curl -sS -X POST https://demo-magento-2.auroracreation.com/rest/V1/integration/admin/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo_admin","password":"demo_admin123"}' | tr -d '"' > /tmp/magento_token
```

跑单个工具：

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
go run -tags live ./test/live -mode=raw -tool=<工具名> -args='{...}'
```

## 重要环境事实

1. **demo 站会周期性重置**：订单与自建数据会消失。测试若因 `not_found` 失败，先确认实体是否还在。
2. **两种 404 必须区分**：`Wniosek nie odpowiada żadnej ścieżce`（波兰语）= 路由不存在；
   `The entity that was requested doesn't exist` = 路由存在但实体不存在。
3. **该店不暴露的路由**（已知，不是缺陷）：`POST /products/{sku}/options`、
   `POST|PUT /customers/{id}/addresses`。
4. **stock 1 是 Default Stock**，Magento 拒绝把非默认源链接到它（业务规则）。
5. 货币 PLN，税率类、属性集等以店铺实际数据为准。

## 语义层要找什么

路由正确不等于行为正确。重点：

- **声明与行为不符**：工具描述、输出字段、错误码说 A，实际做 B
- **静默丢弃**：参数传了但没生效，且不报错（如导入忽略 status/visibility）
- **语义歧义**：分页/截断/部分成功是否有诚实标记
- **幂等与重放**：同一 key 是否真的不重复写入
- **边界条件**：0、负数、超长、空数组、重复项、并发
- **金额与精度**：小数位数、舍入、货币
- **状态机**：允许的状态转换是否与 Magento 一致

## 报告格式

每个分类一份 `docs/audit/semantic/<分类>.md`，每条发现：

```text
### SEM-<分类>-NN: <一句话>

- **严重级别**: P0/P1/P2/P3
- **工具**: magento.xxx
- **现象**: 实际观察到什么（附命令与输出）
- **期望**: 工具声称什么
- **证据**: 复现步骤
- **影响**: 谁会因此受损
- **状态**: CONFIRMED / SUSPECTED / NOT-A-DEFECT（附理由）
```

**只记录，不修改代码**——修复统一在后续阶段进行。
