# 0.8 契约变更提案 P0-SEND-1（待主人批准）

2026-11-01，Codex。主人放行0.8；按coordination §5.5，现有契约变更须先请示。本文件是具体提案，**尚未修改contracts或实现相应Go状态协议**；可先完成独立Node传输适配及合成测试。

## 问题与推荐裁决

旧Sender.Send(ctx,reply,event)没有租约，无法拒绝失效worker。现有generate/verify两阶段也不能表达“已有不可变回复、允许稍后投递”：沿generate恢复可能重新调模型，沿verify恢复又明确禁止投递。网络超时无法证明平台未收，不能盲目重发。

推荐新增deliver阶段、租约化Sender入口和持久化发送意图。外部POST之前先把该尝试记为可能已投递并切verify；未知结果只核验，只有明确尚未POST或可信平台否定回执才允许后续尝试。平台无真正幂等键，本项目不宣称分布式exactly-once。

## 1. 拟更新contracts/sender.md

```go
type Reply struct {
    Text string
    Actions []Action // P0必须空
    Meta json.RawMessage // 不下传平台、不作为Scope授权
    Scope memory.Scope // 可信worker显式提供，须与Claim.Event一致
}
type Sender interface {
    Name() string
    Send(context.Context, event.Claim, Reply) (Result, error)
    Resume(context.Context, event.Claim) (Result, error)
}
// Result保留OK/MsgID/Verified/Error；错误额外提供受控分类与RetryAt。
```

- Send只接受live/in/public、有效租约下replying/generate，Reply.Scope须相同且Private=false，正文1..40个Unicode字符。先保存不可变回复，再最多投递一次；不接受私密/错误Scope，拒绝时不把私密正文写成公开out。
- Resume用于已有deliver/verify计划：deliver读取既有回复，不重新调Brain；verify只核验/记账，不发送。错误分类为retry/dead/skipped/auth/uncertain；固定安全文字，保留原因链，不回显Cookie/原始响应。
- 每次Send/Resume最多一次POST，无内部POST重试。上层恢复仅在确认尚未发起POST或可信否定回执后安排新尝试，最多3次；超时/断管/5xx等未知结果不得转投递重试。
- code=0不独自等于Verified。仅可信成功回执带稳定消息ID，或回读同时匹配本账号/规范房间/精确ID（或平台回显的已存rnd）和正文，才可Verified=true。仅同文/时间接近不能核验；缺失历史不证明未发出。
- 未知结果有界核验后转人工处理：输入事件可dead停止自动调度，但账本/out仍明确unknown，不伪造“未发送”或自动重发。

## 2. 拟更新contracts/event.md

- 新增StageDeliver="deliver"。唯一Task不变；生成回复持久化后generate→deliver；schedule仍只做首次过滤；deliver恢复不重新生成；verify仍禁止投递。
- prepare/arm在PG事务内验证Owner/Version/未过期及实际Scope；真正POST前持久化尝试、send_log和out初始未知记录，并将事件切verify。租约过期后，新worker只核验原尝试，旧worker不能覆盖新代结果。
- 有确定的“未POST/平台否定回执”才可verify→deliver等待下一次；未知结果不允许这条边。总线Mark不开放任意跨阶段，由专用发送repository在证据约束下原子执行。
- Sender结果记账与输入事件done/retry/dead/skipped、lease释放同事务；更新后续核验结果不重复插账。replying/deliver超时恢复queued/deliver；replying/verify仍恢复retry/verify。
- out审计键固定为`live:out:{canonical_room}:{attempt_id}`，与数字room入事件命名空间分离；真实MsgID另存Meta/send_log。该键来自持久化随机尝试ID，不用时间或内容hash冒充平台ID。
- out允许done/retry/dead/skipped，分别承载已核验/未知或待重试/明确失败/未投递跳过。失败out不是实际发言事实；out.At为尝试/投递可观测时间，Meta明确时间与证据种类，不伪造平台时间。

## 3. 拟更新contracts/memory.md与对应查询

Recent/CrossChannel仅将`dir=out AND status=done`当作已发言历史；retry/dead/skipped out仅作审计，不交给Brain冒充已经说过。公开/私密Scope边界不放宽。memory不写send_log/out，发送repository仍是唯一写入责任。

## 4. 拟实现与允许路径

- `migrations/0003_send_delivery.sql`：新增send_plans（每输入事件唯一不可变回复）、send_attempts（有界尝试/许可/结果）；send_log增加唯一attempt_id。已有迁移原字节不改，迁移框架追加版本。
- 发送前按账号PG事务锁检查实际POST间隔及10秒同账号/同房间/同正文防重；x/time/rate预留属于同一发送许可，PG拒绝则退款。核验不扣发送令牌。0.4的任务准入额度/速率不计作实际发送扣账。
- `internal/sender/`、`internal/store/`、`internal/database/`实现、测试；`internal/event/`及`internal/scheduler/`只补deliver分流和相应测试。Scheduler Workers增加可选Deliver回调，遇deliver没有回调时明确失败，不默认转Generate。
- `tools/bili-whale/danmaku.mjs`移植POST表单、凭据和核验，不搬DSH gate/主目录文件防重/outbox。Node采用两阶段stdin协议：预检ready不POST；Go原子arm成功后才发一次commit许可。超时/取消保守视为未知；Node自身不重试POST。
- 默认发送关闭，不接生产模型/真实平台；只做本机模拟HTTP、Node子进程和随机PG schema。必要config/cmd仅诊断/未来装配接缝，不自动开启发送。CI、文档与本轮报告按实际实现同步。

## 5. 验证承诺

先失败测试：成功回执/严格回读、明确拒绝、网络未知不重发、PG原子意图与结果、发送前/后崩溃窗口、旧租约不能覆盖、并发一许可、10秒防重/重启、令牌退款与verify不扣、失败out不进记忆、取消回收和无秘密日志。完成工程检查后DSH独立验收；真实平台留0.11另验。

## 审批状态

- [x] 主人批准P0-SEND-1及上述三个契约/查询增量 — 2026-11-01明确回复“批准推荐契约变更，继续完整0.8”。
- [ ] DSH同步契约基线并独立验收后续实现

若不批准，现有契约保持不变；独立Node薄适配仍可完成，但本轮不把不安全的旧接口包装成完整0.8。
