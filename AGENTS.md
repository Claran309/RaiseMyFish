# AGENTS.md · 开发 agent 工作规则

> 本文件是给**执行开发的 AI agent** 的硬规则，同时适用于 Claude Code / Codex / Cursor 等工具。
> 项目：RaiseMyFish（自研养鱼框架）。当前阶段：Phase 0 待实现。
> 配套：Claude Code 读 `CLAUDE.md`（指向本文件）。

---

## 一、项目概况

一条 AI 肥鱼的多通道陪伴系统，事件驱动：

```
采集(Collector) → 事件总线(Event Bus) → 调度(Scheduler) → 记忆/大脑(Memory/Brain) → 发送(Sender)
```

目标：无人值守下持续在 B站直播弹幕、B站评论、B站私信、QQ 上互动，
**是同一条鱼**（共享人格与记忆），而不是四个机器人。

先读：`README.md` → `docs/02-architecture.md` → `contracts/` → 当前 Phase 计划。

## 二、技术栈（锁定）

| 项 | 值 |
|---|---|
| 语言 | Go **1.25+** |
| 模块名 | `RaiseMyFish`（与 ClaranAIM / ClaranCloudDisk 一致，直接项目名） |
| 数据库 | PostgreSQL 17+（pgvector 备用） |
| 数据访问 | **GORM 为主**（参数化查询）+ pgx 驱动；幂等/状态机/锁用 `Raw`/`Exec` 手写 SQL |
| 日志 | zap（跟随既有项目习惯；JSON，落文件 + stderr） |
| HTTP | 标准库 `net/http`（P5 面板之前不需要 HTTP 框架） |
| WebSocket | P3 QQ 通道需要时再选型（可参考 lanmei 的 `lxzan/gws`），P0 不锁 |
| LLM 编排 | **P0 自写 OpenAI 兼容客户端 + Provider 抽象**；换模型只改配置；eino 按需引入（ADR-0016） |
| 部署 | Docker Compose |

**禁止引入**：Redis、Kafka/RabbitMQ/NATS、Milvus、服务发现、K8s、微服务拆分。
详见 `docs/14-decisions.md`。

## 三、目录结构

```
cmd/
  fishd/             主服务入口
  fishctl/           运维 CLI
internal/
  event/             事件模型与总线
  scheduler/         调度器
  brain/             大脑接口与实现
  memory/            记忆系统
  collector/         采集器（bili-live / bili-comment / bili-dm / qq）
  sender/            发送器
  persona/           人格包加载与回归
  gateway/           QQ 协议端网关（OneBot 11/12）
  infra/             基础设施统一管理（PG、协议端连接，Setup/Close）
  model/             数据模型
  store/             repository 接口
  database/          GORM 初始化与迁移（repository 实现）
  config/            配置加载
pkg/                 可复用包（如 OneBot 客户端）
docs/                设计文档
contracts/           接口契约
plans/               阶段计划
scripts/             开发脚本（dev.ps1 等）
deployment/          docker-compose 等交付物
```

## 四、约定（沿用主人既有项目风格）

- **语言**：代码注释、文档、commit message 用中文
- **数据库表名/字段**：英文蛇形（snake_case）
- **Go 导出符号**：大驼峰
- **错误处理**：`fmt.Errorf` 带 `%w` 包装，zap 记录，不吞错
- **SQL 安全**：GORM 参数化查询，绝不拼接用户输入；幂等用 `clause.OnConflict`，状态机用条件更新，`FOR UPDATE SKIP LOCKED` 用 `Raw`/`Exec`
- **依赖注入**：构造函数注入（`NewXxx`），不用全局变量
- **基础设施**：统一放 `internal/infra/`，`Setup()/Close()` 生命周期
- **日志**：zap；不打印完整 prompt、cookie、token
- **并发**：每个 goroutine 有明确退出路径；共享状态用 channel 或 mutex
- **超时**：所有外部调用（B站、LLM、协议端）必须设超时
- **重试**：指数退避 + 上限，重试必须幂等

## 五、架构边界（违反即打回）

1. **分层不跨层**：handler/scheduler 不直接写 SQL，走 repository
2. **依赖接口**：内部模块不 import 具体实现，`cmd/fishd` 统一装配
3. **内部调用不用 MCP**：MCP 只对外（`docs/11-mcp.md`）
4. **不提前抽象**：Phase 0 不实现 Phase 2 的功能
5. **事件必须幂等**：消费前查重（`contracts/event.md`）
6. **发送必须「投递→确认→记账」**
7. **公开/私密隔离在查询层**：不靠 prompt 自觉

## 六、工作方式

- 每次只做一个 Phase 内的任务；跨 Phase 需用户明确授权
- 开始前读当前 Phase 计划，完成后勾掉任务、写一句结果
- **提交直接 push 到 `main`（或 `dev`），个人项目不走 PR 流程**
- commit message：`phaseN: 做了什么（验证方式）`
- 实现与文档冲突：以文档为准，或先改文档并说明原因
- 设计缺口：记在计划文档「待定」区，不自行发明方向
- 不修改 `docs/14-decisions.md` 既有决策，除非用户明确要求

## 七、测试与验收

每个任务完成必须：

1. 对应单元测试通过（`go test ./...`）
2. 关键路径集成测试（真实 PG）
3. 该 Phase 退出标准逐条满足
4. 更新计划任务状态

**没有测试的功能视为未完成。**

## 八、硬性禁止清单

- ❌ 引入 Redis / MQ / Milvus / 分布式中间件
- ❌ 内部模块调用写成 MCP / HTTP / gRPC
- ❌ 靠 prompt 做隐私隔离
- ❌ 调用方直接写 SQL 绕过 repository
- ❌ 提前实现后续 Phase
- ❌ 无测试交付
- ❌ 凭据写进代码或日志
- ❌ 强制 PR 流程（单人项目直接 push）

## 九、环境变量

见 `.env.example`。关键项：

| 变量 | 说明 |
|---|---|
| `FISH_DB_DSN` | PostgreSQL 连接串 |
| `FISH_LLM_KEY` | LLM API Key |
| `FISH_BILI_COOKIE` / `_FILE` | B站凭据 |
| `FISH_QQ_TOKEN` | QQ 协议端 token |
| `FISH_LOG_LEVEL` | 日志级别 |

## 十、变更记录

- 2026-10-31：初版，吸收 ClaranAIM / lanmei-dream / ClaranCloudDisk 的**码风**（中文注释、构造函数注入、`%w` 错误包装、zap 日志、infra 统一、目录结构、Makefile）。
- 2026-10-31：提交改为直接 push，不走 PR 流程（个人项目）。
- 2026-10-31：明确「码风参考，不照搬选型」——数据访问用 GORM（关键 SQL 手写），LLM 编排 P0 自写，WS 库与 eino 按需再评估。
