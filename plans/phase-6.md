# Phase 6 · MCP 对外工具

> 目标：让外部 agent（Codex / Claude 等）能调用养鱼的能力。
> 只暴露「工具」，不做 agent 循环。

## 依赖

Phase 3（QQ 通道验证了多平台）、Phase 4（成本/安全基础）。

## 范围

| Tool | 作用 | 风险 |
|---|---|---|
| `get_live_status` | 查直播间状态 | 只读 |
| `send_danmaku` | 发弹幕 | 频控/风控 |
| `send_comment` | 发评论 | 核验 |
| `send_gift` | 送礼 | 花钱，必须 dry-run + 额度 |
| `get_memory` | 查记忆 | 隐私 ACL |
| `get_daily_stats` | 查日报 | 只读 |

## 任务清单

### 6.1 MCP Server 骨架
- [ ] 选型：`mark3labs/mcp-go` 或手写 JSON-RPC
- [ ] 传输：先 stdio（本地客户端拉起）
- [ ] 工具注册框架

### 6.2 工具实现
- [ ] 只读工具（live status / memory / stats）
- [ ] 写工具（danmaku / comment），带频控
- [ ] 花钱工具（gift）：
  - [ ] 默认 dry-run
  - [ ] `confirm=true` 才真发
  - [ ] 单次/每日额度
  - [ ] 记账

### 6.3 安全
- [ ] 鉴权（本地 token 或仅本地监听）
- [ ] 工具参数校验（schema）
- [ ] 敏感操作日志

### 6.4 测试
- [ ] schema 校验
- [ ] dry-run 无副作用
- [ ] 额度上限
- [ ] 鉴权失败

### 6.5 异步工具（Super-agent 对齐）
- [ ] 耗时工具支持 start() → 返回 Started → 后台结果作为新事件回到会话
- [ ] 不阻塞前台对话；结果投递走 Event Bus
- [ ] 超时/取消/重试语义与既有 Brain 一致
- [ ] 只在 MCP 工具层实现，不改变 Brain 单次调用契约

## 退出标准

1. 外部 agent 能列出工具并调用只读工具
2. 送礼必须 dry-run 通过后才可真发，额度生效
3. 所有调用有日志与账本
4. MCP 不监听公网（或带鉴权）

## 风险

| 风险 | 应对 |
|---|---|
| 工具被滥用（外发消息） | 频控 + 额度 + 只本地 |
| 花钱不可逆 | dry-run 默认 + confirm + 账本 |
| 协议版本差异 | 先支持核心子集 |
