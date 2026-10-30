# 设计文档索引

| 编号 | 文档 | 内容 | 状态 |
|---|---|---|---|
| 01 | [vision.md](01-vision.md) | 愿景、范围、非目标 | ✅ |
| 02 | [architecture.md](02-architecture.md) | 分层架构、数据流、模块边界 | ✅ |
| 03 | [events.md](03-events.md) | 事件模型、幂等、事件总线 | ✅ |
| 04 | [scheduler.md](04-scheduler.md) | 调度器：冷却、频控、优先级、锁 | ✅ |
| 05 | [brain.md](05-brain.md) | 大脑接口、实现策略、上下文预算 | ✅ |
| 06 | [memory.md](06-memory.md) | 记忆：双表、提炼、召回、版本化 | ✅ |
| 07 | [channels.md](07-channels.md) | 通道适配：B站直播/评论/私信、QQ | ✅ |
| 08 | [persona.md](08-persona.md) | 人格系统、包化、回归测试 | ✅ |
| 09 | [data-model.md](09-data-model.md) | 数据库 schema 与索引 | ✅ |
| 10 | [observability-cost.md](10-observability-cost.md) | 可观测、静默失败、成本工程 | ✅ |
| 11 | [mcp.md](11-mcp.md) | MCP 边界、对外工具设计 | ✅ |
| 12 | [security-compliance.md](12-security-compliance.md) | 安全、隐私、平台合规 | ✅ |
| 13 | [deployment.md](13-deployment.md) | 开发期运行、交付部署 | ✅ |
| 14 | [decisions.md](14-decisions.md) | 关键决策记录（ADR 汇总） | ✅ |
| 15 | [testing.md](testing.md) | 测试策略与质量门禁 | ✅ |
| 16 | [glossary.md](glossary.md) | 项目术语表 | ✅ |
| 17 | [runbook.md](runbook.md) | 故障排查手册 | ✅ |
| 18 | [reference-projects.md](reference-projects.md) | 既有项目参考（lanmei-dream / ClaranAIM / ClaranCloudDisk） | ✅ |

契约文档：`../contracts/`（event / collector / sender / brain）

计划文档：`../plans/`（phase-0 ~ phase-8）

> 阅读顺序建议：02 → 03 → 04 → 05 → 06 → 07 → 08 → 09 → 10 → 11 → 12 → 13。
> 写码前必读：当前 Phase 计划 + 对应契约 + AGENTS.md。
