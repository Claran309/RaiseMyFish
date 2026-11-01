# 0.8 P0-SEND-1实现范围

主人已批准契约提案，按更新后的sender/event/memory契约执行。Codex负责Go service/子进程/DTO、契约和迁移框架、集成验收；内部助手可按固定DTO独写database/send*.go及其测试，不代替DSH。Node薄适配已完成并经内部审查，核验CLI失败unknown缺陷已RED/GREEN修复。

允许：internal/sender/、internal/store/delivery.go、internal/database/send*.go及测试、migrations/0003_send_delivery.sql、migration.go、event/scheduler/memory必要分流/查询及测试、tools/bili-whale新增发送脚本/测试/说明、CI、当前三个契约及本轮计划/报告/教学说明。既有迁移不改，人格/评分/知识/旧证据不改。生产开关仍关闭、不调用真实模型或B站。

执行：先失败测试；持久化计划/尝试及审计→限流防重arm→单POST→证据约束结果→只读恢复。PG许可期限之后再加10秒作为保守实际发送间隔；随机63位正nonce并按账号唯一。网络/断管未知只核验，失败out不入记忆。全仓Node/Go/unit/integration/vet/build/lint/WSL race、独立内部审查后冻结交DSH。

- [x] 主人批准并更新契约
- [x] Node薄适配测试先RED后GREEN
- [x] Go/PG失败测试与实现 — 持久化意图/permit/单POST/核验/原子审计及恢复路径已验证。
- [x] 全仓工程验证/交接 — Node35项、Go unit/integration/vet/build/lint/WSL race通过，证据/教学说明冻结。
- [ ] DSH独立验收
