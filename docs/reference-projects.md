# 参考项目

> 三个主人既有的 Go 项目，用于对齐**工程风格（码风）**。
> ⚠️ **原则：码风可抄，选型要按本项目需求判断。**
> 三个项目的技术选型服务于它们自己的场景（微服务 / 聊天机器人 / 云盘），
> RaiseMyFish 是单机事件驱动系统，不照搬 GORM、eino、WS 库等选型。

## 如何参考（判断标准）

| 类别 | 处理 |
|---|---|
| 目录结构、命名、注释语言、错误包装、构造注入、Makefile、commit 规范 | ✅ 直接对齐 |
| 日志库（zap）、中文注释、GORM 参数化这种「团队习惯」 | ✅ 可沿用 |
| 数据库 ORM、LLM 编排框架、WS 库、HTTP 框架 | ⚠️ 按本项目需求判断，不因「别处用了」就引入 |
| 分布式/微服务/消息队列/可观测全家桶 | ❌ 本项目的非目标 |

## 1. lanmei-dream（最接近，优先参考）

本地路径：`<既有项目目录>/lanmei-dream-main`

- 跨平台聊天机器人，反向 WS 网关接入 OneBot v12/v11，支持 NapCat 直连
- 技术栈：Go、PostgreSQL 17 + pgvector（GORM + pgx）、Redis StateStore、`lxzan/gws`、Conduit（行为树+管线）、eino、zap
- 目录：`cmd/lanmei`、`internal/{ai,bot,command,database,gateway,infra,model,plugin,manager,config}`、`docs/{architecture,data-flow,plans,specs}`
- **可直接借鉴**：
  - `internal/gateway/`：OneBot 11/12 反向 WS 网关实现 → RaiseMyFish 的 QQ 通道参考
  - `internal/infra/`：基础设施统一 Setup/Close → RaiseMyFish 的 infra 约定
  - LOD 记忆（L0 原始 → L1 EpisodeSummary → L2 TopicCluster+向量）→ RaiseMyFish 记忆分级参考
  - `CLAUDE.md` 的结构 → RaiseMyFish AGENTS.md 对齐
  - Makefile（docker compose 全栈 + dev-server）→ RaiseMyFish Makefile 对齐
- **差异**：lanmei 用 Redis 做 StateStore；RaiseMyFish 单机不引入 Redis（用 PG）。

## 2. ClaranAIM（AI 编排参考）

本地路径：`<既有项目目录>/ClaranAIM`

- Agent-Native IM 系统，微服务架构（Kitex RPC + Hertz 网关 + DTM + Kafka/Outbox）
- 技术栈：eino / eino-ext、Hertz、Kitex、zap、OpenTelemetry / Prometheus / Jaeger / ELK
- 目录：`cmd/<service>`、`internal/<service>`、`idl/`、`kitex_gen/`、`pkg/`、`docs/{apiDoc.md,plan.md}`
- **可借鉴**：
  - eino 的 agent / tool / skill 编排方式 → RaiseMyFish 的 Brain 实现参考（P6 起）
  - `docs/plan.md` 的路线图写法（完成度勾选 + 阶段）→ RaiseMyFish 的 plans 参考
  - 可观测体系（OTel/Prometheus）→ RaiseMyFish P4 可选接入
- **差异**：ClaranAIM 是分布式微服务；RaiseMyFish 单机多进程，**不引入** Kitex/DTM/Kafka。

## 3. ClaranCloudDisk（经典分层参考）

本地路径：`<既有项目目录>/ClaranCloudDisk`

- 云盘后端，gin + gorm/mysql + redis + minio
- 目录：`handler/`、`service/`、`dao/`、`model/`、`middleware/`、`util/`、`config/`、`docs/`
- **可借鉴**：
  - handler/service/dao 分层 → RaiseMyFish 的 collector/scheduler/brain/sender 分层思路
  - README 写法（功能特性 + 技术栈表 + 文档索引）
- **差异**：本项目不用 gin（P0-P4 用标准库 `net/http`），不用 MySQL。

## 4. 共性约定清单（来自三个项目）

| 约定 | 采用 |
|---|---|
| 中文注释 / 文档 / commit | ✅ |
| 构造注入 `NewXxx` | ✅ |
| `fmt.Errorf` + `%w` | ✅ |
| 参数化查询 | ✅（GORM；幂等/状态机/锁用 Raw SQL） |
| `internal/infra` 统一外部连接 | ✅ |
| zap 日志 | ✅ |
| 目录 cmd / internal / pkg / config / docs / scripts / deployment | ✅ |
| docker compose 一键 | ✅ |
| 直接 push，不走 PR | ✅ |
| eino 作为 LLM 编排 | P0 自写；按需引入（ADR-0016） |
