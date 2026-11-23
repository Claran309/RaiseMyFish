# 02 · 一条弹幕的 ReAct

## 问题

弹幕经常需要「查一下再答」：记不记得我、刚才聊了什么、这个梗啥意思。单步 Brain 只能凭 prompt 里已有的资料回答，容易编。

## 方案

```
Prepare(人格+资料+预算) → ReAct 循环(最多 N 步)
  ├─ 模型输出 {thought, action, args}
  ├─ 工具执行(只读查证) → observation
  └─ action=final → 正文校验 → Reflection(可选一次)
```

- 两条协议并存：文本 ReAct（`agent.go`）与原生 tool calling（`native.go`），共用工具箱、证据与预算。
- 工具只在**当前事件 Scope** 内查询；Agent 按事件现造，避免并发串会话。
- 预算：工具步数与 Reflection 分离；超时/步数/预算耗尽 → 降级保留证据，由调用方回落单步。
- Reflection 只在 live 默认开启；评论/私信需显式开关，按长度/工具/问答触发。

## 关键代码路径

- `internal/reasoning/agent.go`：文本循环、Reflect、降级。
- `internal/reasoning/native.go`：`tool_calls` 回填与原生降级。
- `internal/reasoning/tools_schema.go`：工具 JSON Schema 与参数校验。
- `cmd/fishd/react.go`：工厂、通道预算、native/auto 接线与 trace 埋点。

## 失败路径

- 模型不支持 tools：`KindUnsupported` → 文本回退，不失败事件。
- 参数非法：不执行副作用工具，把错误回填给模型自纠。
- 步数用满：保留 Evidence，降级正文 + 独立 Reflection。
- 超时：不再调用模型，交回单步生成，保留最终正文时间。

## 三个可回答问题

1. 为什么不做答案缓存？—— 答案依赖现场状态、提问人和时间，缓存会串人、串会话。
2. 为什么工具要按事件现造？—— 共享可变 Scope 会数据竞争并越界查别人会话。
3. 如何防「查对了但回落又不知道」？—— Evidence 随降级结果返回，回落单步复用。
