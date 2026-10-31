# RaiseMyFish · 自研养鱼框架

> 一条 AI 肥鱼的多通道陪伴系统：看直播、回弹幕、回私信、回评论、上 QQ，
> 有共享记忆、有人格一致性、能无人值守跑。
>
> **状态：Phase 0 实现中。0.7a 人格素材已接收；0.1 基础设施已通过DSH独立验收并提交（364dcb3）。Pack基线P0-PACK-1已确认，0.7b加载器已放行待接手。业务通道尚未装配。**

---

## 一句话定位

不是「又一个 QQ 机器人」，而是：

> **一个事件驱动的、多通道、带人格与记忆的陪伴 agent harness。**

## 核心决策（详见 `docs/14-decisions.md`）

| 决策 | 结论 |
|---|---|
| 语言 | Go |
| 数据库 | PostgreSQL（从 Phase 0 起），pgvector 备用 |
| 中间件 | 不引入 Redis / 消息队列 / Milvus / 分布式（单机多进程足够） |
| 架构 | 事件驱动：Collector → Event Bus → Scheduler → Memory/Brain → Sender |
| 人格 | Phase 0 就做，配回归测试集，人设包化可热更新 |
| 通道顺序 | Phase 0 弹幕单线闭环 → Phase 1 评论 → Phase 1 私信 → Phase 3 QQ |
| MCP | 只用于对外暴露能力（给 Codex/Claude 等外部 agent）；内部模块用 Go interface |
| 部署 | 开发期本机直跑；交付 Docker Compose |

## 文档导航

| 文档 | 内容 |
|---|---|
| `AGENTS.md` | **给开发 agent 的硬规则**（技术栈、边界、规范、验收） |
| `docs/00-index.md` | 全部设计文档索引 |
| `docs/01-vision.md` | 愿景、范围、非目标 |
| `docs/02-architecture.md` | 分层架构总览 |
| `docs/03-events.md` | 事件模型与事件总线 |
| `docs/04-scheduler.md` | 调度器设计（事件/冷却/频控/优先级/锁） |
| `docs/05-brain.md` | 大脑接口与实现策略 |
| `docs/06-memory.md` | 记忆系统（双表、提炼、召回、版本化） |
| `docs/07-channels.md` | 通道适配：B站直播/评论/私信、QQ |
| `docs/08-persona.md` | 人格系统与回归测试 |
| `docs/09-data-model.md` | 数据库 schema |
| `docs/10-observability-cost.md` | 可观测性、静默失败、成本工程 |
| `docs/11-mcp.md` | MCP 边界与设计 |
| `docs/12-security-compliance.md` | 安全、隐私、合规 |
| `docs/13-deployment.md` | 开发期与交付期运行方式 |
| `docs/14-decisions.md` | 关键决策记录（ADR 汇总） |
| `docs/testing.md` | 测试策略与质量门禁 |
| `docs/glossary.md` | 项目术语表 |
| `docs/runbook.md` | 故障排查手册 |
| `docs/reference-projects.md` | 三个既有 Go 项目的可借鉴点（lanmei-dream / ClaranAIM / ClaranCloudDisk） |
| `contracts/` | 模块接口契约（event/collector/sender/brain） |
| `plans/` | Phase 0–8 开发计划与任务清单 |
| `plans/assignment.md` | **分工与文件所有权**（给开发 agent 读） |
| `plans/coordination.md` | **协作裁决、任务看板、通知与验收交接入口** |
| `contracts/persona.md` / `plans/phase-0.7b-dispatch.md` | **Pack接口基线与WorkBuddy加载器任务范围** |
| `prompts/` | 给 Codex / WorkBuddy / DSH-flash 的开工提示词（主人复制粘贴用） |

## 目标目录结构（实现期）

```
RaiseMyFish/
├─ cmd/                # 可执行入口：fishd（主服务）、fishctl（运维 CLI）
├─ internal/           # 内部包（禁止外部 import）
│  ├─ event/           # 事件模型、总线
│  ├─ scheduler/       # 调度器
│  ├─ brain/           # 大脑接口与实现
│  ├─ memory/          # 记忆系统
│  ├─ collector/       # 采集器（bili-live / bili-comment / bili-dm / qq）
│  ├─ sender/          # 发送器（同通道配套）
│  ├─ persona/         # 人格注入与回归
│  ├─ store/           # repository 接口
│  ├─ database/        # GORM/pgx与迁移、后续repository实现
│  ├─ infra/           # 连接、日志与进程生命周期
│  └─ config/          # 配置
├─ pkg/                # 对外可复用包（如 OneBot 客户端）
├─ docs/ contracts/ plans/
└─ deployment/         # docker-compose 等交付物
```

## 当前状态

- [x] 架构与选型讨论完成
- [x] 设计文档与接口契约（本目录）
- [x] Phase 0–8 计划拆解
- [ ] Phase 0 实现（由开发 agent 执行）

## 开发入口

新接手的开发 agent 请先读：

1. `AGENTS.md`（硬规则）
2. `docs/02-architecture.md` + `contracts/`
3. `plans/phase-0.md`（当前唯一可开工的阶段）

不要跳阶段、不要提前实现后续 Phase 的功能。

## 0.1 本机运行

需要 Go 1.25+、PostgreSQL 17+，并在本机 `.env` 填写 `FISH_DB_DSN`；`.env` 不入仓。当前只运行基础设施，不需要 LLM/B站凭据，也不会发送消息。命令从仓库根目录执行：

```powershell
go mod download
go run ./cmd/fishd migrate up
go run ./cmd/fishd
# 另一个终端：
go run ./cmd/fishctl status
go run ./cmd/fishctl tail
```

`Ctrl+C` 优雅停止；status 只有 PG/迁移及服务心跳均正常时返回0，未启动/停止/过期返回非零。tail 从当日日志开头读取并跟随追加，跨日自动换文件，Ctrl+C退出。

强杀不会刷新状态文件；当前0.1按心跳时效判断，默认最多20秒可能仍报告在线，过期后 `service_alive=false`。`service.state` 是最后一次记录，可能仍为running，不能单独用它判断存活。即时实例身份校验作为0.11增强项，单查PID不能防止PID复用，本次不改变已验收0.1代码。

默认配置是 `config/fishd.toml`，通道全部关闭；可在命令前传 `--config 路径 --env-file 路径`。进程环境变量覆盖本地 dotenv，随后才是 TOML 和默认值；`FISH_LOG_LEVEL`、`FISH_LOG_DIR`、`FISH_STATE_FILE` 可覆盖日志与状态位置。加载不会修改全局环境。

迁移不会随启动自动执行；`migrate up` 可重复执行并检查checksum，失败整体回滚。初始事实表不开放自动down；不要在业务库手动DROP。日志为 `logs/fishd-YYYY-MM-DD.log`（JSON、文件+stderr、保留7天）；本机心跳位于 `data/fishd-status.json`，不包含凭据。当前没有HTTP监听，通道开关误开会直接报错。

```powershell
go test ./...
go vet ./...
go build ./...
# PG集成测试：使用FISH_TEST_DSN，未设置时读本机.env；创建随机独立schema并清理
go test -tags=integration ./... -count=1
# Windows需64位C编译器；CI已配置race和PG17
go test -race -tags=integration ./... -count=1
```

本地 lint：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...`。CI固定Go1.27与lint2.15.0；模块最低Go1.25。未接入的人格Go草稿仍在工作区，不能据全仓本地测试把0.7b判为验收完成。
