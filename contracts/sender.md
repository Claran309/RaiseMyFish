# 契约 · Sender（发送器）

> P0-SEND-1：2026-11-01主人已批准plans/phase-0.8-contract-proposal.md。以下租约入口替代旧三参数签名，Go/PG实现须按本契约验收；无真实平台调用授权。

## 1. 接口

```go
type Sender interface {
    Name() string
    // Send 发送一条回复。不负责重试（重试由上层任务队列负责）。
    // 必须做核验，核验结果如实在 Verified 中返回。
    Send(ctx context.Context, claim event.Claim, reply Reply) (Result, error)
    // Resume读取已有不可变回复；deliver可投递，verify只核验。
    Resume(ctx context.Context, claim event.Claim) (Result, error)
}

type Reply struct {
    Text    string
    Actions []Action // P0 只支持文本
    Meta    json.RawMessage
    Scope   memory.Scope // 可信worker提供，须与实际事件一致且公开
}

type Result struct {
    OK       bool
    MsgID    string
    Verified bool   // 核验通过
    Error    string
}
```

## 2. 语义（硬要求）

1. **不重试**：sender 只发一次，失败返回 error，由任务队列决定重试
2. **必须核验**：发送后确认（回读/回执），`Verified=false` 表示未确认
3. **必须记账**：无论成功失败，写 `send_log` 和 `out` 事件
4. **必须限流**：发送前从令牌桶取令牌（或由 scheduler 保证，二选一但必须明确）
5. **隐私**：公开通道的 sender 不得接收 private 内容

## 3. 通道差异

| 通道 | 核验方式 | 特殊 |
|---|---|---|
| live | 回执/回读 | 长度 ≤40 字 |
| comment | 回读 state == 0 | state 17 视为未通过，不重发 |
| dm | 回读最后一条 | 长度 ≤500 |
| qq | 协议端回执 | 群聊需 @ |

## 4. 错误分类

| 错误 | 上层处理 |
|---|---|
| 网络失败 | retry |
| 平台限流 | 退避后 retry |
| 内容违规（state 17 / 12006） | 记 dead，不重发 |
| 评论区关闭（12002） | skipped |
| 鉴权失效 | 暂停通道，告警 |

## 5. 记账

```sql
INSERT INTO send_log (event_id, channel, ok, verified, msg_id, error)
```

同时写 `out` 事件（status=done/retry/dead）。

## 6. 测试

- 成功→verified
- 失败→error，不重试
- 核验不通过→verified=false，error 说明
- 记账：每次发送都有 send_log 行
- 限流：令牌不足时返回明确错误

## 7. P0-SEND-1投递协议与恢复

- 只接受live/in/public、合法Scope、1..40 Unicode字符、空Actions；Meta不下传平台。实际数据库行与租约才是授权真源，不能信任被修改的Claim快照。非法Scope/私密输入不写公开out。
- 首次Send在replying/generate下保存不可变回复及ready尝试，并切deliver。Resume从持久化计划读取，不重新生成回复；同事件换正文/账号/房间拒绝。
- Node预检只读，凭据/账号/房间匹配后ready。PG原子arm成功才给Node一次commit；arm之前即持久化未知send_log/out并切verify。节点或进程断管一律保守unknown；已获许可的旧进程无法被数据库撤回外部请求，因此不宣称平台exactly-once。
- Sender每次调用最多一次POST，无内部POST重试。只有明确not_posted或可信否定回执可回deliver，最多3次尝试；超时、5xx、断管及未知响应不得回投递。预检失败有独立有界计数；鉴权失败暂停实例。
- code0无稳定ID不等于Verified。成功回执有稳定非零ID，或回读匹配本账号/房间/完整正文及精确ID（未知ID时允许匹配原尝试唯一rnd并有稳定ID），才能verified。缺历史、同文、时间接近都不能证明未发出。
- 失败核验始终unknown（即使凭据/预检失败），不能返回not_posted。核验默认最多3次后停止自动处理：输入dead并入DLQ，out仍保留retry/unknown_manual，不把未知伪装成明确未投递。
- 实际POST令牌在Sender唯一扣取，x/time/rate预留和PG许可为同一发送额度，PG拒绝则退还本地预留；Verify不扣发送令牌。Scheduler桶仍只约束Task准入，不是平台发送账本。PG按账号锁，以许可最晚起始时间+10秒保守保护间隔，防止arm到实际POST间的延迟绕过间隔；同账号/房间/正文10秒防重，覆盖重启。
- send_log和out由发送repository唯一写入；arm、结果、输入事件状态/阶段及释放租约同事务。attempt_id唯一，重复核验更新已有记录，不重复插账；memory只读。
- 默认发送关闭，不接生产模型或真实平台。独立Sender/Node/PG组合不代表0.11真实房间验收。

## 8. 私信接收账号归属（N10增量）

- 私信发送只认事件持久化的接收账号：优先 `Meta.recv_actor`，其次复合 `Session = "{recv_actor}:{peer_uid}"`。首次建计划的 AccountID 必须是该账号，Destination 保持事件 Session；不得用当前活跃账号替代。
- 已有计划按计划原账号恢复（`ActorByMID`），切号不改写；原账号凭据不可用时保持 ready 并返回 uncertain，不 arm、不 POST、不换号。
- 旧数据兼容：事件无 `recv_actor` 且 Session 为裸 `peer_uid` 时，仅当已有计划存在才按计划账号继续；无计划则 dead 冻结，等待人工处理。
- 明确归属冲突一律 dead 冻结，不 arm、不 POST：(a) 复合 Session 与 `Meta.recv_actor` 都给出接收账号且不一致；(b) 已有计划账号与事件明确接收账号不一致。冲突检查必须先于「已有计划优先」分支，Send 与 Resume 同规则。
- 冲突冻结只改变终止判定，不改变其余投递状态机与恢复语义。
