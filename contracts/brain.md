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

## 7. 0.6实现基线（P0-BRAIN-1）

本轮由Codex细化后按失败测试实现；Provider只执行单次HTTP请求，所有重试由Brain统一管理。`Tool`与`Action`各仅含Name string保留字段，P0传入Tools非空立即拒绝，返回Actions为空，不实现工具协议。

Provider数据：Message{Role,Content string}；Request{Model string, Messages []Message, MaxTokens int, Temperature float64}；Response{Text string, Usage Usage}；Usage{InputTokens,OutputTokens *int}。Provider接口Name()/Chat(ctx,Request)(Response,error)。未知token为nil，明确返回0为非nil零值，不互相替代。

OpenAI兼容实现仅支持非流式POST `<base_url>/chat/completions`，字段model/messages/max_tokens/temperature/stream=false。base_url仅可信配置，必须HTTPS或本机loopback HTTP；不含userinfo/query/fragment，不跟随重定向，Authorization只用于该端点。响应有大小上限，不把HTTP响应正文、请求正文、URL中的凭据或key放入错误/日志。网络/超时/5xx可重试，4xx（含429）不可重试；格式错误不可重试；ctx取消不可重试。Provider不得自行重试。

Brain Options：Model、MaxPromptTokens（默认8192）、MaxReplyTokens（128）、AttemptTimeout（10s）、TotalTimeout（40s）、Backoff（250ms，指数退避上限2s）、MaxConcurrent（4）、Temperature（0.7）及可空Input/OutputUSDPerMillion。固定最多3次实际Provider调用；空回复最多补1次，不能再套另一层重试。总超时包含等待并发槽、退避及Provider调用；记录用量使用脱离取消但独立5秒上限的清理Context，防止取消后丢账。

New(provider,UsageRepo,Options,zapLogger)构造Brain，Name为provider:model；依赖必须非nil。先做Scope/Task/Event/Persona校验和预算，失败时Provider零调用且不产生虚假用量行。合法Event必须是DirIn、有效Scope、非空Text/ID与非零At，Task.ID非空且引用同一Event。Persona必须有非空soul/rules/samples及对应场景。

预算采用可审计的保守估算：两条消息UTF-8字节数加固定64token消息框架余量，不称作模型精确token数；配置max_prompt_tokens只限制估算prompt，不包含输出上限。默认8192以容纳现有人格soul与场景；配置过小返回明确预算错误。按低importance记录→较旧跨通道消息→较旧会话消息→共享事实的顺序裁记忆，随后只裁samples、rules的后缀（按rune，不切断中文），不裁soul、场景、当前事件或本轮固定instructions；仍超限则拒绝请求。完整人格可用时system稳定不含动态信息；裁剪只改变静态文本前缀/后缀，不注入动态内容。

user按context/event/instructions分段，段内内容使用有HTML转义的JSON结构化编码，事件Meta不注入。对记忆slice复制并稳定排序，不改入参。回复按rune截断live40/comment200/dm500/qq200（qq为P0防御默认值，QQ发送未装配），结果Meta只含provider/model、可空token、耗时、实际尝试次数、可空费用，不含prompt/key。

每次实际Provider.Chat调用都经注入store.UsageRepo.RecordUsage(ctx,Usage)记录到既有llm_usage：At、Channel、Model=provider:model、可空InputTokens/OutputTokens/CostUSD、Milliseconds、OK。Usage失败立即返回不可自动重试的用量错误，不因生成已经成功而吞掉；实际费用仅在两项usage与两项价格均已知时估算并四舍五入到6位，否则NULL。0价格可明确表示免费。无配置价格不虚报0费用，不做汇率或全局日预算。

错误分为input/budget/provider/http/timeout/canceled/protocol/empty/usage，采用typed error并提供Retryable分类；不因4xx进入Brain内部重试。记录和日志保留安全错误链，禁止打印原始HTTP体。0.6只提供装配与check-brain诊断（无模型调用），不打开尚未实现通道，不提供假的生产Memory Reader。

实现审查补充：总超时从Reply入口开始，包含校验/预算准备，并发槽也覆盖准备过程；准备中检查Context并用按类别的二分/批量裁剪避免逐条全量重编码。输入安全上限为4096个上下文条目（含Record.EventIDs引用）与4MiB被处理文本，超出返回input错误、不调用Provider。该上限不是记忆召回策略，实际Reader仍应按配置限量。

总时限/等待槽DeadlineExceeded不在本轮继续调用，但typed error标记Retryable=true供上层用新Context决策；主动Canceled保持false。协议结构错误若仍有完整有效的usage，应保留统计记账后返回错误；JSON无法解析或统计本身无效则保留未知，不伪造数值。Reply.Meta的token/费用汇总仅在所有尝试对应值均已知时合计，任一未知即为nil。

Reply.Meta.ms包含准备、等待并发槽、调用与记账的总耗时；llm_usage.ms只记录各次Provider调用耗时。汇总费用仍按6位小数呈现，不把缺失统计混入已知合计。
