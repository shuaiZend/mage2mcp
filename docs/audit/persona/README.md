# 岗位场景测试（v0.11.6）

本轮不是「逐个工具点测」，而是让 7 个不同身份背景的 agent **按本岗位的日常任务**使用 MCP，
看工具在真实工作流里是否站得住。

## 岗位与关注点

| 岗位 | 关心什么 | 报告 |
|---|---|---|
| 店铺运营 | 工具说成功但实际没生效 | `store-operator.md` |
| 商品运营 | 上架后能不能买到、选项有没有丢 | `merchandiser.md` |
| 客服 | 退款金额、看到的和顾客看到的是否一致 | `customer-service.md` |
| 财务 | 金额可追溯、口径一致 | `finance-controller.md` |
| 系统集成 | 幂等、部分失败、错误码可编程 | `integrator.md` |
| 安全合规 | 越权、审计轨迹、PII | `security-officer.md` |
| 数据分析 | 口径一致、截断诚实、可复现 | `data-analyst.md` |

## 环境

| 项目 | 值 |
|---|---|
| 网关 | `http://127.0.0.1:18110/mcp`（SSH 隧道 → 生产 `49.51.250.83:8080`） |
| 版本 | **v0.11.6** |
| 实例 id | `demo` |
| 店铺 | `https://demo-magento-2.auroracreation.com`，store `pl`（PLN） |
| 管理员令牌 | `/tmp/magento_token`（会过期，重新获取见下） |
| 审批 | `ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'` |

重新获取令牌：

```bash
curl -sS -X POST https://demo-magento-2.auroracreation.com/rest/V1/integration/admin/token \
  -H 'Content-Type: application/json' \
  -d '{"username":"demo_admin","password":"demo_admin123"}' | tr -d '"' > /tmp/magento_token
```

调用单个工具：

```bash
MCP_LIVE=1 MCP_ENDPOINT=http://127.0.0.1:18110/mcp MCP_INSTANCE=demo \
MCP_APPROVE_CMD="ssh -o BatchMode=yes ubuntu@49.51.250.83 'sudo /opt/mage2mcp/approve.sh {id}'" \
go run -tags live ./test/live -mode=raw -tool=<工具名> -args='{...}'
```

## 必须知道的环境事实

1. **demo 站会周期性重置**：订单与自建数据会消失。遇到 `not_found` 先确认实体是否还在。
2. **两种 404**：`Wniosek nie odpowiada żadnej ścieżce`（波兰语）= 路由不存在；
   `The entity that was requested doesn't exist` = 路由存在但实体不存在。
3. **该店不暴露的路由**（已知，不是缺陷）：`POST /products/{sku}/options`、
   `POST|PUT /customers/{id}/addresses`。
4. **stock 1 是 Default Stock**：Magento 拒绝把非默认源链到它。
5. 生产部署 `auth.enabled=false`；安全岗位需自建开启鉴权的实例。

## 报告格式

每条发现：

```text
### P-<岗位>-NN: <一句话>

- **级别**: P0/P1/P2/P3
- **我的工作流**: 我本来想做什么
- **发生了什么**: 实际观察（附命令与输出）
- **我期望什么**: 为什么这不合预期
- **状态**: CONFIRMED / SUSPECTED / NOT-A-DEFECT
```

**只记录，不修改代码。** 修复统一在后续任务进行。
