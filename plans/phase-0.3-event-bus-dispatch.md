# 0.3 事件总线实现与交付

2026-11-01，主人确认0.2 DSH通过并放行0.3。依§5.5自主执行；本轮契约变更已获明确授权。接口真源为先行更新的P0-EVENT-2，不改v5、人格/知识/评分或旧证据，不调用模型/平台。

## 精确范围

Codex独写：contracts/event.md；internal/event新bus.go及bus/实现/测试；internal/store新增queue.go、task.go增量；internal/database新增queue*.go、tasks/events/migration及其测试必要增量；migrations/0002_event_leases.sql；本派发、本轮报告/教学说明、自己的codex报告。cmd/config/Brain/Memory实现/平台适配不在本轮；不碰既有ADR或0001/seed。共享工作区历史改动保留，push继续搁置。

## 已裁决设计

见Event契约：Claim返回带owner/version租约的queued，schedule可skipped/Defer retry；Task与generate阶段原子落地；generate/verify恢复分流；PG时间与平台时间分离；过期worker失去更新权。新增迁移，不修改已应用SQL；旧replying未知投递事实保守进verify。无后台worker、无需在fishd提前装配生产链路。

## 执行计划

1. 先新增纯Bus与真实PG测试并运行RED：幂等/合并通知、并发领取、SKIP LOCKED、有界排序、过滤/延期/续租/旧token失效、原子Task与阶段、replying/sent恢复、迁移v1→v2。
2. 落地event纯接口/租约DTO与store注入接口；实现非阻塞Notify包装，不访问数据库实现或开启goroutine。
3. 扩展迁移为有checksum的顺序版本，保留v1原checksum；实现PG claim/lease/transition/recovery；旧无租约入口不能改已领取行。
4. 真实PG定向GREEN后跑全仓unit/integration、vet/build/lint/race；审查与修复；新报告保留本轮RED/GREEN、哈希、快照，生成教学说明。
5. DSH独立验收；不代填回执，不以总线通过代替0.4/0.8/完整Phase退出。0.4按阶段路由；WorkBuddy继续等待正式0.5/0.8派发。

## 进度

- [x] 先发布契约与范围
- [x] RED测试 — 缺接口编译失败；typed-nil、释放后旧入口、锁等待跨到期续租均有实际RED日志。
- [x] 实现与真实PG验证 — 并发领取/换租/阶段/任务事务/迁移升级及回滚通过。
- [x] 工程检查/交接 — 全仓unit/integration、vet/build、integration标签lint与WSL race通过；证据/教学说明已归档。
- [ ] DSH独立验收
