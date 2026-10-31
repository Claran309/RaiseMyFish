# 05 · 大脑（Brain）设计

> 大脑 = 把「一个事件 + 记忆 + 人格」变成「一条回复」的执行器。
> 它是可插拔的：DSH、AstrBot、OpenAI 兼容 API、外部 agent 都是实现之一。

0.6实际Go边界、重试/预算/计费与配置以 [P0-BRAIN-1](../contracts/brain.md) 实现基线为准；Memory DTO/范围按 [P0-MEMORY-1](../contracts/memory.md)。当前实现待DSH独立验收，默认不启用模型、不消费事件；`fishd check-brain`只是无模型调用的装配诊断。

## 1. 接口契约

```go
type Brain interface {
    // Name 返回实现名，用于日志与路由
    Name() string
    // Reply 处理一个任务，返回回复
    Reply(ctx context.Context, task Task, in ContextInput) (Reply, error)
}

type ContextInput struct {
    Event      Event
    Persona    PersonaPack   // 人格包（soul/rules/samples）
    Memory     MemoryContext // 召回的记忆（已按预算裁剪）
    Shared     SharedState   // 跨通道共享状态
    Tools      []Tool        // 可选：工具定义
}

type Reply struct {
    Text    string   // 回复正文
    Actions []Action // 可选：发送/工具调用（P6 起）
    Meta    JSONB    // 模型、token、耗时
}
```

## 2. 上下文组装（每次请求）

顺序固定，**静态在前、动态在后**：

```
[system]
  └─ 人格包 soul（稳定，可缓存）
  └─ 规则 rules（稳定，可缓存）
  └─ 语气样本 samples（稳定，可缓存）

[user content]
  └─ <context> 共享状态 + 召回记忆 + 别通道动态（每轮变）
  └─ <event> 当前要回的那条消息
  └─ <instructions> 本轮任务说明（简短）
```

**关键**：稳定内容放 system 开头（吃提示词缓存），每轮变化的内容放末尾。
混在一起会让缓存失效，成本高 7~20 倍（AstrBot 官方文档实测警告）。

## 3. 实现路线与换模型

**P0**：自写 OpenAI 兼容客户端（约 200 行），**不做 eino**。

**设计目标：换模型 = 改配置，不改代码。**

### 3.1 Provider 抽象

```go
type Provider interface {
    Name() string
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}

type ChatRequest struct {
    Model       string
    Messages    []Message   // role/content
    Tools       []ToolDef   // 可选
    MaxTokens   int
    Temperature float64
}

type ChatResponse struct {
    Text      string
    ToolCalls []ToolCall
    Usage     Usage         // in/out tokens
}
```

- 内置实现 `openai`：覆盖 OpenAI / DeepSeek / Ark / OpenRouter / 硅基流动 / Ollama 等兼容端点
- 非兼容协议（Anthropic 原生等）以后加独立适配器，**Brain 接口不变**

### 3.2 配置：多 Provider + 路由

```toml
[llm.providers.deepseek]
type        = "openai"
base_url    = "https://api.deepseek.com/v1"
api_key_env = "FISH_LLM_KEY"
models      = ["deepseek-chat", "deepseek-reasoner"]

[llm.providers.ark]
type        = "openai"
base_url    = "https://ark.cn-beijing.volces.com/api/v3"
api_key_env = "FISH_ARK_KEY"
models      = ["doubao-seed-1.6"]

[llm.routing]
default = "deepseek:deepseek-chat"
live    = "deepseek:deepseek-chat"   # 弹幕用便宜模型
dm      = "deepseek:deepseek-chat"   # 可换更强模型
```

- 换模型/换厂商 = 改 `llm.routing` 或加一个 provider 段，不碰代码
- 每次请求记录实际使用的 `provider:model`（进 `llm_usage`），便于对比与回滚

要求：
- 统一走 `net/http` + 自定义 client（超时、重试、并发限制）
- 不在日志里打印完整 prompt（只打印长度和 hash）
- 失败：超时/5xx 指数退避重试（最多 2 次），4xx 不重试

## 4. 预算与分级

| 场景 | 模型 | 说明 |
|---|---|---|
| 弹幕回复 | 便宜模型 | 高频、短回复 |
| 评论/私信 | 中档 | 需要理解上下文 |
| 记忆提炼 | 便宜模型 | 后台批量 |
| 路由/分类（要不要回） | 最便宜或规则 | 能不调就不调 |

每次请求记录：输入 token、输出 token、估算费用、耗时。写入 `llm_usage` 表。

硬预算：单次请求 prompt 超过 `max_prompt_tokens`（如 3000）则截断记忆部分。

0.6采用保守UTF-8字节计数+64框架余量，默认8192而非模型精确tokenizer；现有人格较大，配置3000可能连soul+场景都放不下，此时明确拒绝。真实费用只依赖服务端usage与用户配置报价，未知字段NULL；HTTP正文不合协议但有可用usage也要记账。完整实现规则见Brain契约，不以估算prompt字节数冒充实际token账单。

## 5. 失败处理

| 失败 | 处理 |
|---|---|
| 超时 | 重试 2 次（指数退避），仍失败 → task retry |
| 4xx（参数/鉴权） | 不重试，记 dead + 告警 |
| 内容为空 | 视为失败，重试一次 |
| 回复超长 | 截断到通道上限（弹幕 40 字，评论 200 字） |

## 6. 工具调用（P6 起）

大脑可声明 tools（发弹幕、送礼、查直播状态），由 LLM 决定调用。
执行循环在 brain 内部或独立的 agent loop 中（见 `plans/phase-6.md`）。
**P0-P5 不实现工具调用**，只做纯文本回复。

## 7. 多大脑路由

按「通道/场景」路由到不同 `provider:model`：

```toml
[llm.routing]
default = "deepseek:deepseek-chat"
live    = "deepseek:deepseek-chat"     # 弹幕：便宜模型
dm      = "deepseek:deepseek-chat"     # 私信：可换更强
complex = "deepseek:deepseek-reasoner" # 复杂任务：推理模型
```

路由表在 `cmd/fishd` 装配时注入，scheduler 只写 `Task.Brain`。
**换模型/厂商只改这里，不碰代码。**

## 8. 测试要求

- 单元：上下文组装顺序正确（静态在 system、动态在末尾）
- 单元：预算截断逻辑
- 集成：mock LLM 返回 → Reply 正确
- 回归：人格回归集（`docs/08-persona.md`）通过

## 9. 防坑清单

- ❌ 把每轮状态塞进 system_prompt（缓存失效）
- ❌ 把完整 prompt 写日志（隐私 + 体积）
- ❌ 无限重试
- ❌ 让大脑直接发送（发送归 sender，核验归 sender）
