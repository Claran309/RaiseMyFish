# C 档 ReAct 思考链派发单

> 背景：P0 两段式规则判断只能「查/不查」，查什么靠关键词。要升级成「LLM 自主决定查什么、多步组合」——结合事实源（记忆/知识/状态/历史）+ 工具查证。
> ⚠️ 预算控制：MaxSteps=3、总超时 8s、失败降级单步——不能变成 DSH 的 30 秒。

## 允许路径

- 新增 `internal/reasoning/agent.go`（ReAct 循环）
- `internal/reasoning/tools.go`（工具定义）
- `internal/brain/types.go`（ContextInput 加 Tools/State，已有 Tools）
- `cmd/fishd/serve.go`（Generate 接入：复杂弹幕走 ReAct）
- `internal/config/reasoning.go` + `config/fishd.toml` 的 `[reasoning]` 段
- 测试文件随实现
- 不碰 contracts、调度/发送/人格/知识库

## 设计

### 1. 工具（tools.go）

```go
type Tool struct {
    Name string // memory_search | knowledge_search | presence | history
    Desc string
    Run  func(ctx, args) (string, error)
}
```

四个工具：
- `memory_search`：按查询词查记忆（Reader.Records/Recent）
- `knowledge_search`：查 K0 梗库/知识（knowledge.Reader）
- `presence`：查当前状态（Presence.Active）
- `history`：查直播间最近 N 条事件（events 查询）

### 2. ReAct 循环（agent.go）

```
输入：事件文本 + 人设摘要 + 工具列表
循环（最多 MaxSteps=3）：
  1. LLM 返回：{thought, action: tool|final, args}
  2. action=tool → 执行工具 → 结果加入上下文
  3. action=final → 结束，返回回复
超时（8s）/步数耗尽 → 用当前上下文生成（降级）
```

- 每次 LLM 调用用便宜模型？还是主模型？——**用主模型**（简单），但 prompt 短（工具描述+当前上下文）
- 输出解析：JSON（{thought, action, args}），解析失败 → 降级单步

### 3. 触发（serve.go）

- 简单弹幕（单步判断 NeedMemory/NeedKnowledge 均 false）→ 单步生成（快）
- 需要查事实的（判断 true）→ ReAct 循环
- 配置 `[reasoning] enabled=true`（默认关？先 default true 测试）、max_steps=3、timeout_ms=8000
- 日志：`action="react"`、steps、tools_used、latency_ms、degraded

### 4. 测试

- mock LLM 返回 tool 调用 → 断言工具执行 + 结果进上下文
- 步数耗尽 → 降级
- 超时 → 降级
- 解析失败 → 降级
- 简单弹幕不进 ReAct（走单步）

## 禁止

- ❌ 不改 contracts、不碰调度/发送/人格/知识库
- ❌ 不调真实模型（mock LLM）
- ❌ 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、预算（步数/超时）、降级行为、未做项、是否暂停写入
