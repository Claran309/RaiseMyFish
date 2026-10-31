# 0.3 事件总线交付：P0-EVENT-2（待DSH独立验收）

2026-11-01，Codex。主人确认0.2 DSH通过并授权先改契约、再推进0.3；已按§5.5自主执行。契约及范围先于实现发布，详见 `contracts/event.md` 与 `plans/phase-0.3-event-bus-dispatch.md`。

## 结果与边界

Publish幂等入库、容量1非阻塞Notify、有界原子SKIP LOCKED领取、Owner/Version/PG时间租约、条件Mark、Defer与Renew、有界Recover均已实现。schedule过滤可skipped、延期进retry；任务与generate阶段同事务；replying/generate过期回queued；sent或未知投递阶段只进入verify补偿，不能重入生成。

新增0002与顺序迁移框架，v1原source/checksum不变。旧无租约入口仅允许Owner为空且Version=0；已领取后，即使释放也不能绕回旧协议。没有后台worker或生产装配，不开启通道，不花钱、不调用模型/平台，不更新公共业务库。P0-G03外部投递未知结果窗口仍未闭合，0.8前另行设计；本报告不宣称真实发送重启不重发已验收。

## 实际RED/GREEN及审查

- transcripts/red-unit.txt / red-integration.txt：先写总线/PG测试，再运行，因缺少QueueRepo/Claim/New/Bus等接口失败。
- transcripts/red-typed-nil.txt：构造器曾接受接口包装的nil指针，行为测试失败；加入nil接口值检查后GREEN。
- transcripts/red-released-legacy.txt：内部审查发现兼容入口只看Owner会在释放后重新获权；两条PG测试复现绕过未来延期和verify恢复。改为Owner NULL且Version=0，并保留原ErrInvalid分类，transcripts/green-legacy-pg.txt通过。
- transcripts/red-renew-lock-wait.txt：真实PG复现单UPDATE等待纯行锁后复活过期租约。改为事务内先锁行、随后独立语句复检PG时钟，transcripts/green-renew-lock-wait.txt通过。
- transcripts/green-pg.txt含其他并发/事务/迁移/取消验证。补充测试有直接验证既有功能者，不虚称每条都先RED。
- 内部event_bus_review两项Important均修复并只读复核通过；无剩余Critical/Important。它不是DSH，未代写验收回执。

## DSH独立验收入口

全仓unit/integration、vet/build、integration标签lint（0 issues）及WSL race全部通过。event覆盖100.0%、event/bus 95.0%、store 98.0%、database含真实PG 90.4%。真实PG在Windows随机隔离schema运行；WSL race不含PG integration。WSL启动有既有.wslconfig选项提示，随后测试通过退出0，未改宿主配置。

执行输出归档在transcripts/*.txt（coverage-summary.txt为覆盖率）。本地*.log受既有gitignore影响，故归档为UTF-8/LF文本以供后续提交，保留文本内容；vet/build成功时无输出，退出0记录于verification.json，不伪造空控制台日志。源码快照已核对均为LF，无需修改根属性文件。

manifest.json / snapshot/ 是本次精确交付与基线依赖；verify.ps1核对源码与快照。checks结果和覆盖率在verification.json及对应日志。请独立设计并发/失败数据，不只照抄开发测试。

建议重点：两个消费者不重复、锁住最早行仍可领下一条；schedule过滤与未来retry期限；有效租约不受旧At/重启影响；过期旧代次不能Mark/Renew/建Task；释放后旧兼容入口也不能绕过；Task冲突无半成品；generate恢复不回首次过滤，verify不回生成；锁等待跨到期不能续租；升级v1不改变历史checksum，失败整体回滚。

DSH请在自己的报告回执、更新0.3看板，并将旧事件/调度设计中pending过滤等示例同步为P0-EVENT-2真源。0.4按Stage分流并定期轮询/Recover，不能依赖Notify计数。WorkBuddy暂不启动0.5/0.8，待后续范围与平台协议正式派发。

旧0.2快照和本轮RED日志全部保留；仅本轮明确路径有增量，未修改其他共享工作区交付。当前没有暂存、提交或push，仍按既有搁置推送要求执行。仓库通知不代表跨应用消息已送达。

教学说明：`docs/update/2026-11-01-1130-租约事件总线与崩溃恢复.md`。
