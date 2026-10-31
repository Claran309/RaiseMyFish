# 0.2 存储交付与接口基线 P0-STORE-1

2026-11-01，Codex 按主人 MVP 顺序授权执行；采用既有 Phase 0 / Event / Memory 设计，不扩展 P2。K0 已收尾：相关率 82.14% 达标、覆盖 43/60 未达标；关键词留 P2 前，不跑模型开关对照。

## 范围与裁决

- 唯一开发者 Codex；允许新增 `internal/store/{errors,event,task,memory}.go`、`internal/database/{events,tasks,memories}.go` 及相应测试和本轮报告/教学说明。既有 UsageRepo 保持兼容。
- 不改已应用迁移、不改人格/知识/评估/旧证据、不启用业务通道、不访问真实模型/平台、不提交未验收功能。此前搁置的 push 继续搁置。
- EventRepo：Insert 幂等写入、Get 精确读取、Transition 按调用方 from 集合及既有合法边条件更新。入事件只接受 pending；出事件只接受 done/retry/dead，避免出事件进入采集调度。
- TaskRepo：CreateForEvent 在事务内锁定已 queued 的入事件，核对通道/会话，再写唯一 event_id 的初始 queued 任务；GetByEvent 返回任务投影。不同事件抢用同 task ID 是冲突。这里不声明已领取任务、已开始生成或已发送。
- MemoryRepo：InsertRecord 幂等写入；Recent / CrossChannel / Records 只返回 DTO。SQL 层执行 P0-MEMORY-1 隐私隔离、有效性和确定排序；上限 1–100。无跨私密身份绑定；不提供完整 Body 给 Brain。
- store 依赖 event/memory 的既有纯 DTO；两者纯类型包不反向 import store。后续总线/Reader 的具体装配必须放独立实现层，不能制造循环依赖。
- 错误可用 errors.Is 判断 Invalid / NotFound / Conflict；数据库错误只展示固定阶段文字并保留原因链，不展示 SQL 参数/正文。

## 任务与验证

1. 先新增纯校验与真实 PG 行为测试，记录缺符号/缺功能的 RED；再实现上述接口。
2. PG 随机 schema 复用既有隔离工具；验证并发幂等、非法/陈旧迁移失败、唯一任务、事务失败无半成品、参数化输入、公开/私密/跨会话隔离与有效记录排序、取消/连接错误。
3. 全仓 unit/integration、vet/build/lint、WSL race；无真实模型调用。交付报告记录实际结果和文件 SHA256，教学说明基于本轮文件。
4. DSH 独立验收后才勾选 0.2、集成提交；由 DSH 将基线签名同步到契约索引。开发自测不等于验收。

## 仍待 0.3 前裁决的设计缺口

P0-G01/G02 继续保留，**本次不冻结 Bus/恢复协议**：ClaimPending 后过滤、延期、持有权/状态起始时间和 queued/retry 重新领取须在 0.3 中统一并先更新契约；本次 Transition 严格遵守现行 event.md，不私加 queued→skipped。0.2 接口只是持久化基础，不可用来证明重启不丢/不重发。Task 的后续领取/执行状态、记忆提炼/版本化留对应任务；P0-G03 发送一致性仍在 0.8 前解决。

## 交付状态

- [x] 失败测试证据 — 缺接口编译失败、校验空实现行为失败、记忆空实现真实 PG 失败；见交付报告。
- [x] 实现与真实 PG 验证 — 并发幂等/CAS/任务唯一与 SQL 隐私边界通过。
- [x] 全仓检查与教学说明 — unit/integration/vet/build/lint/WSL race 通过，教学说明见交付报告。
- [ ] DSH 独立验收（只由 DSH 回执）
