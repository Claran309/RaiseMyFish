# 契约 · Event 与事件总线

> P0-EVENT-2，2026-11-01：主人已授权 Codex 统一 P0-G01/G02 并更新本契约后实现。替代旧的无持有权 Bus 签名；Event 事实字段保持不变。总线实现不等于平台发送闭环，P0-G03 仍须在 0.8 前解决。

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

0.6已落地事实DTO；0.2已独立验收幂等仓储和条件迁移。本次0.3追加租约及恢复，旧证据不改写。

| 通道 | 键 |
|---|---|
| live | `live:{room_id}:{msg_id}` |
| comment | `comment:{oid}:{rpid}` |
| dm | `dm:{uid}:{msg_id}` |
| qq | `qq:{bot_id}:{msg_id}` |

- 入库：`INSERT ... ON CONFLICT (id) DO NOTHING`
- 入队去重：`Publish` 返回 `inserted=false` 即重复
- 抢占语义：`ClaimPending` 原子地把 `pending → queued`，返回的事件归本消费者所有
- 消费者依据返回 Claim 的 Stage 分发，不再要求事件仍为 pending；只有 schedule 阶段执行首次过滤。禁止两个有效租约同时持有同一事件。
- 禁止用自增 ID、时间+内容哈希做键

## 3. 总线接口

```go
type Stage string
const (
    StageSchedule Stage = "schedule" // 首次过滤/延期，尚未创建Task
    StageGenerate Stage = "generate" // 已有唯一Task，生成分支
    StageVerify   Stage = "verify"   // 核验/记账补偿，禁止重新生成或投递
)
type Lease struct {
    Owner   string // 每个Bus实例独立的随机标识，不是凭据
    Version int64  // 数据库递增的持有代次，不能仅比较Owner
}
type Claim struct {
    Event      Event
    Stage      Stage
    Lease      Lease
    LeaseUntil time.Time
    StateAt    time.Time // 当前状态开始时间，与平台At分离
}
type Bus interface {
    // Publish 幂等入库；inserted=false 表示重复。
    Publish(ctx context.Context, ev Event) (inserted bool, err error)

    // ClaimPending 抢占到期入事件，返回有独占租约的queued；limit为1..100。
    ClaimPending(ctx context.Context, limit int) ([]Claim, error)

    // Mark 同时检查实际状态、阶段及未过期Owner/Version。
    Mark(ctx context.Context, claim Claim, from []Status, to Status) error

    // Defer 保留阶段、设置未来retry_at、转retry并释放租约；禁止靠Mark省略期限。
    Defer(ctx context.Context, claim Claim, until time.Time) error
    // Renew 使用PG时间续租，不改变平台At、StateAt或代次。
    Renew(ctx context.Context, claim Claim) (Claim, error)
    // Recover 有界恢复过期queued/replying/sent；不是重启时清空所有持有权。
    Recover(ctx context.Context, limit int) (int, error)

    // Notify 同进程唤醒信号（实现可为 channel）。
    Notify() <-chan struct{}
}
```

Bus 实现位于 `internal/event/bus`，仅依赖 store 接口；event 事实/接口包不 import store，避免循环。PG实现位于 database，经构造注入。New(repo, leaseDuration) 默认租约5分钟（传0），合法1秒..24小时；每实例产生随机Owner，无后台goroutine。所有方法除Notify外要求非nil Context；SQL沿用调用方取消/超时。Notify是容量1的合并信号，只在入事件首次Publish/恢复有进展时非阻塞唤醒；重复、失败、out不唤醒。信号不代表队列长度，不关闭channel；消费者必须定时轮询/恢复，不能靠通知保证不丢。

## 3.1 抢占、过滤与延期（P0-G01裁决）

- 只领取dir=in。pending到期、queued无租约或租约已过期、retry到期均可领取；有有效租约的一律不抢。replying/sent须先Recover，终态和out不领取。
- 领取用`FOR UPDATE SKIP LOCKED`，按At、ID升序选取并原子置queued、清retry_at、换Owner且Version递增。queued重新领取保持Stage；retry恢复也保持Stage。queued不再等同“已经创建Task”。
- schedule阶段ACL/噪音不通过：Mark queued→skipped；冷却/配额/会话繁忙：Defer→retry，期限之前不能重新领取。不要改回pending或永久丢弃。
- 接受事件时通过`store.ClaimedTaskRepo.CreateClaimed(ctx, claim, task)`在**同一事务**检查租约/实际范围、写唯一Task并把Stage设为generate。返回bool表示是否新写Task；重复事件使用已有Task，不能另发一份。失败整体回滚，不留下“有Task却仍schedule”的半成品。不能调用无租约的旧CreateForEvent绕过持有权。
- generate重新领取直接处理已有Task，不重新跑首次过滤；verify重新领取只进入核验/记账。0.4必须按此分流，不得看到queued就一律调Brain。

## 3.2 持有权及时间（P0-G02裁决）

新增迁移0002，不改0001：`stage`、`status_changed_at`、`lease_owner`、`lease_version`、`lease_until`。PG时钟决定到期；状态真正改变才更新status_changed_at，续租和queued重新领取不伪造状态开始时间。Event.At永远是平台时间。

每次领取、释放/恢复递增Version；操作必须同时匹配ID、Owner、Version、lease_until>PG当前时间。租约丢失或过期返回`event.ErrLeaseLost`，旧worker即便Owner相同也不能覆盖新结果；状态条件不匹配返回store.ErrConflict，非法边/无效期限返回store.ErrInvalid，底层失败固定安全文字并保留原因链。Claim携带的正文/阶段/状态不是写操作授权真源，PG实际行才是。

0.2的无租约Transition/CreateForEvent仅保留**从未领取**的存储路径兼容：必须lease_owner IS NULL且lease_version=0，否则Conflict。延期/恢复释放后Version仍大于0，绝不能退回旧入口绕过retry_at或verify阶段。旧记录升级时补阶段及状态时间；原有queued/replying/sent赋已过期迁移租约，未知是否已投递的旧replying保守归verify，不能凭空断言可以重发。

## 4. 状态机（合法迁移）

```
pending  → skipped | queued          (ClaimPending)
queued   → replying | retry | dead
queued   → skipped                    (仅schedule过滤)
queued   → done                       (仅verify核验成功)
replying → sent | retry | dead
replying → queued                    (崩溃恢复：超时重置)
sent     → done | retry              (done = 核验通过/补偿记账)
retry    → queued | dead
```

阶段进一步限制：Mark的queued→replying仅generate；queued→skipped仅schedule；queued→done仅verify；replying→sent切换verify；sent→done保持verify；queued/replying→dead仅相应执行分支。retry迁移、replying→queued恢复由专用方法执行，不能借Mark绕过期限/租约。Mark成功进入replying/sent会续租；终态释放租约。Defer可从queued/replying/sent转retry，保留Stage并释放；schedule延期不伪装成生成重试。

崩溃恢复策略（仅过期租约，默认5分钟；按lease_until判断，不按平台At）：

- `replying` + generate 超时→queued，保留generate，由Claim重新取走；不能重新创建Task或重新做首次过滤
- `replying` + verify（旧迁移/以后发送准备标记）超时→retry/verify，只补核验
- `sent` 超时 → 进入 `retry` 做核验/记账补偿，**不重发**（已发出的消息不重复发）
- `queued` 租约超时→保持queued并释放旧租约，或由Claim原子换租；保留Stage
- Recover以SKIP LOCKED有界操作并使旧租约失效，续租中的活跃worker不受重启影响。到期retry可直接Claim，未来retry不提前取；pending直接Claim。

Recover恢复sent/verify后同样不能重发。0.3只交付持久化分流，没有模型/平台I/O；0.8前还需定义**外部投递之前**持久化发送意图、回执与未知结果的补偿（P0-G03），否则禁止启用真实发送。本契约不能证明“平台已收但尚未标sent”的窗口已闭合。

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
- 领取后过滤/未来延期/到期retry重领；有效租约不抢、过期可换租，旧token续租/Mark/创建Task均失败
- 平台At很旧但租约有效不能恢复；StateAt与At独立，续租不改StateAt
- 原子Task+Stage、失败回滚；恢复generate不回schedule；sent/verify补偿不回生成
- v1真实PG升级到v2、checksum不变/重复迁移/并发迁移/错误回滚
