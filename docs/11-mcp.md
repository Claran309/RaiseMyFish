# 11 · MCP 边界与设计

> 一句话：**内部用 Go interface 解耦，对外用 MCP 暴露能力。**
> MCP 是生态接口，不是内部总线。

## 1. 为什么要 MCP

让现成 agent（Codex / Claude Desktop 等）能调用养鱼的能力：
发弹幕、送礼、查直播状态、玩游戏。

它解决的是「**跨进程 / 跨生态**」的互操作，不是「模块解耦」。

## 2. 边界（硬规则）

| 场景 | 用什么 |
|---|---|
| 同进程模块之间（scheduler ↔ brain ↔ sender） | Go interface |
| 对外暴露给外部 agent | MCP Server |
| Go 后端调用别人的工具（文件/浏览器） | MCP Client（可选） |

**禁止**：把内部调用也写成 MCP（延迟、序列化、调试成本）。

## 3. MCP Server 设计（P6）

### 3.1 工具清单

| tool | 作用 | 风险 |
|---|---|---|
| `send_danmaku` | 发弹幕 | 频控/风控 |
| `send_comment` | 发评论 | 核验 |
| `send_gift` | 送礼 | **花钱，必须 dry-run + 额度** |
| `get_live_status` | 查直播间状态 | 只读 |
| `get_memory` | 查记忆 | 隐私（带通道 ACL） |
| `get_daily_stats` | 查日报 | 只读 |

### 3.2 工具定义示例

```json
{
  "name": "send_gift",
  "description": "给直播间送指定礼物。默认 dry-run，需显式 confirm=true 才真发。",
  "inputSchema": {
    "type": "object",
    "properties": {
      "room_id": {"type": "string"},
      "gift_id": {"type": "string"},
      "num": {"type": "integer", "minimum": 1},
      "confirm": {"type": "boolean"}
    },
    "required": ["room_id", "gift_id", "num"]
  }
}
```

### 3.3 安全约束

- 所有「写/花钱」工具必须 dry-run 默认
- 额度上限：每日、单次
- 账本：每次调用落 `send_log` / `llm_usage`
- 鉴权：MCP server 只监听本地，或带 token

### 3.4 实现方式

- Go：`mark3labs/mcp-go` 或手写 JSON-RPC（stdio / HTTP+SSE）
- 传输：P6 先做 stdio（客户端拉起进程），后续可选 HTTP

## 4. MCP Client（可选）

如果养鱼需要调用别的 MCP server（如浏览器、文件系统），在 brain 工具循环里实现 client。
P6 之前不需要。

## 5. 与 agent 循环的关系

MCP 只负责「有哪些工具」，**不负责**：
- 何时调用（决策）
- 调完怎么继续（循环）
- 多步任务编排

这些属于 brain/agent loop（`docs/05-brain.md` §6）。

## 6. 玩游戏（P7）的 MCP 用法

```
SMAPI mod（游戏内 HTTP/WS）
   ↕ MCP server（包装成 tools）
外部 agent（Codex/Claude）或自有决策循环
```

- 回合制/慢节奏游戏：MCP 延迟够用
- 实时动作游戏：MCP 太慢，需本地控制循环（不在 P7 范围）

## 7. 测试要求

- 工具 schema 校验
- dry-run 路径不产生副作用
- 额度上限生效
- 鉴权失败拒绝

## 8. 防坑清单

- ❌ 内部模块调用也走 MCP
- ❌ 送礼没有 dry-run
- ❌ MCP server 暴露公网且无鉴权
- ❌ 把 MCP 当 agent 框架（它不管决策）
