# 契约 · Brain（大脑）

## 1. 接口

Persona类型遵守 [P0-PACK-1](persona.md)，Memory/SharedState最小类型和范围遵守 [P0-MEMORY-1](memory.md)。0.6首先验证Task/Event/两份Scope一致及数据范围，错配或含越界私密数据时返回错误、不得调用Provider；这不替代repository的查询层过滤。

```go
type Brain interface {
    Name() string
    Reply(ctx context.Context, task Task, in ContextInput) (Reply, error)
}

type Task struct {
    ID      string
    EventID string
    Channel event.Channel
    Session string
}

type ContextInput struct {
    Event    event.Event
    Persona  persona.Pack
    Memory   memory.Context
    Shared   memory.SharedState
    Tools    []Tool
}

type Reply struct {
    Text    string
    Actions []Action
    Meta    json.RawMessage // model, tokens, ms
}
```

## 2. 上下文组装规则（必须）

```
system:  persona.soul + persona.rules + persona.samples   （稳定，可缓存）
user:    <context> shared_state + memory+跨通道动态
         <event>   要回复的消息
         <instructions> 本轮简短说明
```

- 稳定内容必须在 system 开头（缓存友好）
- 每轮变化的内容必须在 user content 末尾
- 禁止把动态状态塞进 system_prompt

## 3. 失败语义

| 情况 | 返回 |
|---|---|
| 超时/5xx | error，允许上层重试 |
| 4xx | error，标记不可重试（用 typed error） |
| 空回复 | error |
| 超长 | 由 brain 截断到通道上限后返回 |

## 4. 预算

- 单次 prompt token 上限（配置），超限先裁记忆再裁 persona（persona 最后裁且必须保留 soul）
- 每次调用记录 `llm_usage`（model/in/out/cost/ms）

## 5. 工具调用（P6）

- `Reply.Actions` 预留给工具调用
- P0-P5 只返回文本，`Actions` 为空

## 6. 测试

- 组装顺序：system 稳定、user 动态
- 预算截断
- mock LLM 成功/超时/4xx 三条路径
- 截断长度符合通道上限
