# 契约 · Memory最小上下文（P0-MEMORY-1）

> Codex按主人「确认Memory最小类型基线，放行0.6」授权制定。
> 仅供P0 Brain消费的只读数据、范围校验与读取接口；不是数据库模型，不实现P2实体/版本化/向量功能。
> Event字段/枚举仍以event.md为真源；本文件与brain.md的memory.Context、memory.SharedState对齐。

## 1. Go类型与纯校验API

```go
package memory

import (
    "context"
    "time"
    "RaiseMyFish/internal/event"
)

type Scope struct {
    Channel event.Channel
    Session string
    Private bool
}

type Message struct {
    EventID  string
    Channel  event.Channel
    Session  string
    Dir      event.Dir
    FromID   string
    FromName string
    Text     string
    Private  bool
    At       time.Time
}

type Record struct {
    ID         string
    Channel    event.Channel
    Session    string
    Summary    string
    Importance float64
    Confidence float64
    Private    bool
    EventIDs   []string
    ValidFrom  time.Time
}

type Fact struct {
    Key      string
    Value    string
    Channel  event.Channel
    Session  string
    Private  bool
    SourceID string
    At       time.Time
}

type Context struct {
    Scope        Scope
    Recent       []Message
    CrossChannel []Message
    Records      []Record
}

type SharedState struct {
    Scope Scope
    Facts []Fact
}

type Reader interface {
    Recall(ctx context.Context, scope Scope) (Context, error)
    Shared(ctx context.Context, scope Scope) (SharedState, error)
}

func ValidateScope(scope Scope) error
func (c Context) Validate() error
func (s SharedState) Validate() error
```

纯校验无I/O，不要求Context；读取接口必须Context。返回数据由调用方当作只读，Brain裁剪时复制slice，不原地改共享数据。没有any/RawMessage/任意map兜底，Message不携带平台Meta，Record只给摘要，不把底层整表/完整Body直接塞进prompt。

## 2. Scope来源与一致性

- Scope由可信装配/worker依据目标Event决定，不能从消息正文、模型输出或不受信任工具参数中取。Channel仅live/comment/dm/qq，Session非空；live/comment必须Private=false，dm必须true，qq允许两种。
- 私信（N10）的 Session 固定为 `{接收账号MID}:{对方UID}`；同一对方发给两个接收账号属于不同私密会话，Recent/Records/Facts 不得跨复合 Session 混用。旧裸 Session（仅对方UID）只用于读取兼容，不得据此新建或合并会话。
- Brain入参Task、Event及Context.Scope、SharedState.Scope必须在目标Channel/Session/Private上相符；Task没有Private时从Event取。Event与Task的EventID也要一致。错配返回错误且不调用Provider，不静默丢弃坏记录后继续。
- 即使没有记忆，也要显式传 `Context{Scope: target}` 与 `SharedState{Scope: target}`；nil slice是正常空数据。零值Scope无效，不能用空上下文掩盖查询失败。
- Reader查询失败必须返回error，不把失败当作「没有记忆」。0.6测试可注入空数据或fake Reader；生产接入真实Memory前不得宣称已经具备召回/隐私查询能力。

## 3. 查询层硬隔离（0.2/0.9实现责任）

| 数据 | 查询边界 |
|---|---|
| Recent | 同Channel、同Session；公开Scope强制NOT private；私密Scope只允许本会话私密记录和本会话公开记录 |
| CrossChannel | Channel不同于目标，且强制NOT private；不因当前目标是私密会话就开放其他私密会话 |
| Records | 必须valid_to IS NULL且archived=false；公开Scope强制NOT private；私密记录仅当目标Private=true且来源Channel/Session完全相同 |
| SharedState.Facts | 来源读取或持久化查询执行同样公开/私密范围；私密事实只进入相同私密Channel/Session |

SQL必须通过repository参数化执行，不能在上层拼SQL或在prompt里要求模型忽略私密数据。未建立跨平台身份绑定前，不将「都是主人」当作跨私密会话查询授权。P0不增加private_channels数组或ACL表来规避现有schema；需要新授权模型必须另行裁决。

DTO校验是额外防线，不能替代查询层过滤或证明SQL实现已通过验收。0.6收到不符范围的数据直接拒绝调用；0.9及实际MemoryRepo验收仍必须用真实PG验证公开查询拿不到私密行。

## 4. 数据校验与展示顺序

- Message的EventID、Channel、Session、Text非空，Dir为in/out，At非零；Recent只含目标会话，CrossChannel只含其他通道的公开消息。FromID/FromName可空。
- dm来源的消息必须Private=true，不能通过误标private=false进入公开Context。Record/Fact的dm来源同样不能主动降为公开；未建立脱敏/解密授权流程前一律拒绝这种输入。
- Record的ID/Summary非空；Importance/Confidence为有限且位于[0,1]的数，不能是NaN/Inf；私密Record必须有可核对的Channel/Session，且满足Scope。EventIDs是可追溯来源；不把旧状态历史当成当前事实。有效期筛选由repository负责，DTO不增加版本化功能。
- Fact的Key/Value/SourceID非空、At非零、Channel合法、Session非空；来源缺失、余额未知、事实未观测时不伪造0或旧值，而是省略该事实。SourceID可引用事件ID或可信探测器名称，不含凭据或正文。
- Fact仍须满足公开/私密范围；例如旧「存粮约90」没有当前来源/时效就不作为事实注入。事实新鲜度策略由其来源/0.9配置决定，本契约不自行发明状态更新机制。
- 排序由提供方保持确定：Recent/CrossChannel按At升序、同时间按EventID；Records按Importance降序再ID；Facts按Key再At/SourceID。Brain不得使用map随机遍历改变前缀顺序。
- 召回上限由Memory构造配置控制；最终prompt硬预算归Brain。0.6在已筛选数据上做裁剪，不引入P2三因子/语义检索实现。所有内容以user数据段呈现，不能提升为system指令或工具执行参数。

## 5. 0.6与0.9职责

P0-SEND-1（主人2026-11-01批准）查询增量：Recent/CrossChannel的out行必须status=done。retry/dead/skipped out只是尝试审计，不能当作本鱼已经说过的话；查询层SQL过滤，不能靠prompt。memory不写send_log/out，发送repository仍是唯一写入责任，公开/私密Scope不放宽。

0.6可以创建上述DTO和纯校验及对应测试，让ContextInput可编译并拒绝范围不符输入；需要时只创建event.md中已有的Event/枚举类型，不实现总线或状态迁移。Brain.Reply消费显式ContextInput，不直接读SQL/账本/旧人设文件；worker通过注入接口准备数据。

Reader实际查询、提炼、游标恢复与共享事实采集留0.9；不提供会吞错的生产空Reader，不添加全局记忆缓存。空/假Reader仅用于mock测试或明确未装配的开发路径，当前fishd业务通道仍关闭。

## 6. 最小验收

0.6纯单测至少覆盖：有效空上下文；Scope与Task/Event错配；live/comment含private、dm范围错误；Recent跨会话；CrossChannel私密；私密Record/Fact跨会话；NaN/Inf评分；无来源/时间事实；错误路径Provider调用次数为0；裁剪不污染输入；静态system不含动态记忆。

0.9另验真实PG的查询过滤、有效记录与重启补提炼。本契约冻结不代表那些实现存在，也不替代0.7c的真实模型人格回归。
