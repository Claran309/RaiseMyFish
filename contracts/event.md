# 契约 · Event 与事件总线

## 1. Event 结构（权威定义）

```go
package event

type Channel string // live | comment | dm | qq

const (
    ChLive    Channel = "live"
    ChComment Channel = "comment"
    ChDM      Channel = "dm"
    ChQQ      Channel = "qq"
)

type Dir string // in | out

const (
    DirIn  Dir = "in"
    DirOut Dir = "out"
)

type Status string

const (
    StatusPending  Status = "pending"
    StatusSkipped  Status = "skipped"
    StatusQueued   Status = "queued"
    StatusReplying Status = "replying"
    StatusSent     Status = "sent"
    StatusDone     Status = "done"
    StatusRetry    Status = "retry"
    StatusDead     Status = "dead"
)

type Event struct {
    ID        string          // 幂等键，全局唯一
    Channel   Channel
    Dir       Dir
    Session   string
    FromID    string
    FromName  string
    Text      string
    Private   bool
    ReplyTo   string
    At        time.Time
    Status    Status
    Meta      json.RawMessage // 平台原始字段
}
```

## 2. 幂等键规则（必须遵守）

| 通道 | 键 |
|---|---|
| live | `live:{room_id}:{msg_id}` |
| comment | `comment:{oid}:{rpid}` |
| dm | `dm:{uid}:{msg_id}` |
| qq | `qq:{bot_id}:{msg_id}` |

- 入库：`INSERT ... ON CONFLICT (id) DO NOTHING`
- 消费前：查 status，非 `pending` 跳过
- 禁止用自增 ID、时间+内容哈希做键

## 3. 总线接口

```go
type Bus interface {
    // Publish 幂等入库；inserted=false 表示重复。
    Publish(ctx context.Context, ev Event) (inserted bool, err error)

    // ClaimPending 抢占待处理事件（状态置 queued）。
    // 多进程场景用 SELECT ... FOR UPDATE SKIP LOCKED。
    ClaimPending(ctx context.Context, limit int) ([]Event, error)

    // Mark 更新状态（带条件：仅当当前状态在 from 集合中）。
    Mark(ctx context.Context, id string, from []Status, to Status) error

    // Notify 同进程唤醒信号（实现可为 channel）。
    Notify() <-chan struct{}
}
```

## 4. 状态机（合法迁移）

```
pending  → skipped | queued
queued   → replying | retry | dead
replying → sent | retry | dead
sent     → done | retry
retry    → queued | dead
```

非法迁移必须返回错误（防状态错乱）。

## 5. 顺序保证

- 同一 `(Channel, Session)` 按 `At` 升序处理（scheduler 负责串行）
- 跨 session 可并行

## 6. 保留策略

- `events` 保留 90 天，之后归档
- `out` 事件同样保留（用于防重与记忆）

## 7. 测试用例（必须覆盖）

- 同 id 两次 Publish → 第二次 inserted=false
- ClaimPending 并发两个消费者 → 同事件只被一个拿到
- Mark 非法迁移 → 错误
- 崩溃恢复：replying 超时 → 重置为 queued
