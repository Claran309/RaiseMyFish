# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与语义化版本。

## [Unreleased]

### Added
- 项目文档骨架：愿景、架构、事件、调度、大脑、记忆、通道、人格、数据模型、可观测成本、MCP、安全合规、部署、决策记录
- 接口契约：event / collector / sender / brain
- 开发计划：Phase 0–8
- 工程配套：editorconfig、golangci-lint、CI、Makefile、环境变量模板（个人项目，不设协作流程）
- 原生 tool calling 第一批：Provider 可选 `ToolProvider`、工具 JSON Schema、`RunNative` 循环与文本回退（默认 `native_tools=off`）
- B1–B11 改进计划与延迟诊断 L 系列计划；延迟口径与评论/私信排查文档
- L0 延迟口径：`collected_at`/`collect_mode`、`event_timings` 七阶段计时、五段延迟与 fresh/all 输出
- L1 死信止血：`event_errors` 分类落库、dead Top 分布、`fishctl dlq list/replay`、comment/dm 独立回复窗口
- B1-D 原生工具接线：真实 HTTP mock 端到端与 `auto` 不支持降级测试通过（默认仍 `off`）
- B2 per-event trace：本地零依赖 trace（collect/schedule/generate/tool.call/deliver），面板慢回复入口与 `/api/traces`
- B3 评测入口：`make eval` 统一人格 fixture/坏回复/检索报告，CI eval job 与 nightly（无密钥明确 skip）
- B4 轨迹级评测：50 条工具选择用例、轨迹指标与录制轨迹复算（缺轨迹明确 skip）
- M1 记忆事实版本链：`fact_key`/`status`/`memory_conflicts`，改口时旧行失效可追溯
- R1 Reflection 预算独立：`reflect_budget`、工具步与反思分离、跳过原因与改写归因
- R2/R3 评论/私信反思开关与归因：可解释触发、50 条对照集、`reflection.json`（未录制明确 skip）
- M3 记忆写入质量闸门：偏好需稳定信号，低价值主人相关/玩笑/复读不写正式表
- M5 记忆使用归因：`memory_usage` + `reply_meta.memory_ids`（只 ID，不复制正文）
- M6 记忆巩固/遗忘 dry-run：归档建议、重复 fact_key 提示、时间衰减分
- M2 记忆实体与关系：明确句式抽取、实体/关系幂等写入、1 跳邻域查询
- M4 默认召回混合检索：普通回复先走关键词+向量，失败逐级回退
- L2 评论自适应轮询：活跃/空闲间隔、抖动与 412/429 指数退避
- B10 红队用例：20 条弹幕注入回归，验证固定 prompt 段不可伪造
- B7 设计讲解：`docs/design-decisions/` 三页链路白板稿
- B6 README 重写：真实取数（事件/记忆/档案/发送/LLM/测试）+ Mermaid 数据流 + ADR 摘要
- B8 检索实验：BM25+RRF 对照 +0.0 点，按门槛不采用；结论与复现写入报告
- B9 成本面板：单条回复成本与缓存命中率（近 24h，无报价显示未知）
- L4 日报增强：fresh 延迟、死信 Top、单条回复成本（空数据不新增段落）
- L3 快慢双路径：短评论/私信走单步，阈值可配（0=关闭）
- B11 根目录清理：exe/config.local/migrate 导出归档到本地 `tmp/`（只归档不删除）
- L5 索引收尾：事件/发送/记忆使用组合索引
- 会话状态 C0/C1/C4/C6：按通道会话键、`conversation_state`、会话段注入与会话锁（默认关闭）
- 会话状态 C5：滚动摘要任务表 + 租约/重试/幂等，失败保留旧摘要；`summary_enabled` 默认关闭
- 会话状态 C8：`/api/conversations` 六项观测指标 + 面板区块；30 条离线会话评测
  （追问解决率 100% vs 基线 10%，串人/键冲突 0，prompt token +0.4%）
- 会话状态 C2/C3：评论 thread 按会话键确定性灰度；直播 30 秒内存短窗口（默认关闭）
- L2 第二批：评论 `(ctime,rpid)` 水位持久化、目标级有界并发；私信轮询 120→60 秒
- L3 第二批：通道软并发配额、backfill 低优先级、通道 prompt 预算、同用户 30 秒合并
- M5 100 条记忆评测：该记住/不该记住/隐私/写入闸门/版本键（召回 100%、误报 0、泄漏 0）
- M8 记忆运维：`fishctl memory audit`、面板来源/版本/引用次数、日报记忆健康度
- B1 工具参数错误可观测：除日志外新增 trace `tool.args_error`，便于按 span 计数
- B2 可选 OTLP 导出：标准库 OTLP/HTTP JSON（`/v1/traces`），不新增依赖，默认关闭

### Changed
- 仓库卫生：根目录临时文件归档到本地 `tmp/`；`AGENTS.md`、`CLAUDE.md`、协作表、内部报告与 prompts 改为本地保留，不再上传

### Fixed
- 回退 3 条未评审的人格评测用例，修正 v1.3 人格包基线与 `fishctl` 预算断言，恢复 `go test ./...` 全绿

---

<!-- 发布时记录：

## [0.1.0] - YYYY-MM-DD
### Added
- Phase 0：弹幕单线闭环
-->

## 版本规划

| 版本 | 内容 |
|---|---|
| 0.1.0 | Phase 0：弹幕单线闭环 |
| 0.2.0 | Phase 1：评论 + 私信 |
| 0.3.0 | Phase 2：记忆正式化 |
| 0.4.0 | Phase 3：QQ |
| 1.0.0 | Phase 4–5：稳定与面板 |
