# 03 · 事件模型与事件总线

## 1. 为什么以事件为核心

多通道、异步、可恢复——这三个需求决定了一切围绕**标准化事件**：
采集只产出事件，调度只消费事件，发送只产出发送结果，
记忆从事件里提炼。任何模块重启，都能从事件表恢复。

## 2. Event 结构

```go
type Event struct {
    ID        string    // 全局唯一（幂等键），见 §3
    Channel   string    // live | comment | dm | qq
    Dir       string    // in（收到）| out（发出）
    Session   string    // 会话标识：房间号 / BV号 / 用户uid / QQ群号
    From      string    // 发送者标识（uid 或昵称）
    FromName  string    // 展示名
    Text      string    // 内容（已剥离平台格式）
    Private   bool      // 是否私密（dm/qq 私聊 = true）
    ReplyTo   string    // 回复引用的消息 id（可空）
    At        time.Time // 事件发生时间（平台时间，非入库时间）
    Meta      JSONB     // 平台原始字段（保留，用于排障）
}
```

约束：

- `Text` 长度上限 4000 字符，超长截断并记 meta
- `At` 使用平台时间；入库时间单独一列 `created_at`
- `Meta` 不允许存凭据/cookie

## 3. 幂等键规则

**每个事件必须有稳定、可复现的 ID。** 推荐组合：

| 通道 | 幂等键 |
|---|---|
| live | `live:{room_id}:{msg_id}` |
| comment | `comment:{oid}:{rpid}` |
| dm | `dm:{uid}:{msg_id}` |
| qq | `qq:{bot_id}:{msg_id}` |

入库用 `INSERT ... ON CONFLICT (id) DO NOTHING`。
消费前先 `SELECT status FROM events WHERE id = $1`，非 `pending` 则跳过。

**禁止**：用自增 ID 当幂等键；用「时间 + 内容哈希」当幂等键（同内容不同事件会撞车）。

## 4. 生命周期状态机

```
pending ──调度判定──► queued ──brain 处理──► replying
   │                                            │
   │ 噪音/不回复                                  ▼
   └──────────────► skipped                  sent ──核验──► done
                                                 │
                                                 └──失败──► retry ──► dead
```

| 状态 | 含义 | 谁写 |
|---|---|---|
| `pending` | 已入库，未调度 | collector |
| `skipped` | 决定不回（噪音/重复/策略） | scheduler |
| `queued` | 已生成 Task，等待 brain | scheduler |
| `replying` | brain 正在生成 | brain worker |
| `sent` | 已发出，待核验 | sender |
| `done` | 核验通过 | sender |
| `retry` | 发送/生成失败，等待重试 | sender/brain |
| `dead` | 重试耗尽，进死信 | sender/brain |

**注意**：`out` 事件（本鱼发出的消息）也要入库，状态直接 `done`，
用于「不重复说同一句话」和「记忆写入」。

## 5. 事件总线实现

P0 用 **PG 表 + 进程内通知**，不引入 MQ：

```go
type Bus interface {
    Publish(ctx context.Context, ev Event) (inserted bool, err error) // 幂等入库
    ClaimPending(ctx context.Context, limit int) ([]Event, error)    // 抢占待处理
    Mark(ctx context.Context, id string, status Status) error
    Notify() <-chan struct{}                                          // 内存信号
}
```

- 同进程：collector INSERT 后向 channel 发信号，scheduler 收到后 `ClaimPending`
- 跨进程（未来拆进程）：scheduler 定时 `ClaimPending`（PG `FOR UPDATE SKIP LOCKED`）
- 进程重启：从 `pending/queued/replying/retry` 状态恢复

**顺序保证**：同一 session 的事件按 `At` 升序处理（scheduler 内按 session 串行）。

## 6. 重放与补漏

| 场景 | 做法 |
|---|---|
| 采集断线重连 | 从 `events` 最后一条该通道 `At` 之后补拉（平台支持时） |
| 进程崩溃 | 启动时把 `replying` 超时的记录重置为 `queued` |
| 发送失败 | `retry` 状态 + 指数退避，上限 3 次 → `dead` |
| 死信 | 人工 `fishctl dlq list` 查看，可手动重放 |

## 7. 事件与记忆的关系

```
events（全量原始） ──异步提炼──► memory_records（长期记忆）
```

- `events` 是事实层，永久保留（按天归档）
- `memory_records` 是提炼层，带重要性/置信度/有效期
- 发送完成后**必须**写一条 `out` 事件（无论成功失败，成功写 done、失败写 retry/dead）

## 8. 防坑清单

- ❌ 用「最近 N 条」内存状态做去重（重启即丢）
- ❌ 发送失败不记录（无法补）
- ❌ 把本鱼自己发出的消息再当 `in` 事件处理（影分身）
- ❌ 事件表无限增长不归档
