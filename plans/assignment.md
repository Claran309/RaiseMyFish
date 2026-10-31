# 分工与协作契约（Agent Assignment）

> **2026-10-31 补充裁决**：开工同时阅读 [coordination.md](coordination.md)。主人已明确 Codex 首席开发、DSH 独立验收与会话辅助、WorkBuddy 轻量开发；本文件下列分工、接口冻结、各自提交/push 等旧表述与补充裁决不一致时，以补充裁决为准，由 DSH 持续同步旧文案。

> 本文件是**给开发 agent 的分工说明书**。当前阶段：Phase 0。
> 阅读顺序：本文件 → `AGENTS.md` → `contracts/` → `plans/phase-0.md`。
> 规则冲突时：契约 > 本文件 > 计划细节。**技术/架构/契约争议由 Codex 最终裁决**；
> 账号、额度、环境问题找主人。不自行发明方向。

---

## 一、角色与决策权

| 角色 | 身份 | 职责 |
|---|---|---|
| **Codex** | **首席开发 / 最终裁决** | 架构关键模块；裁决技术争议、契约变更、任务与文件冲突；可创建协作文档并通知其他 agent |
| **WorkBuddy** | 副手开发 agent | **耗 token 但不重的实现类任务**：通道适配、发送器、人格包迁移与回归、记忆/观测样板；遇争议升级给 Codex |
| **DSH + flash** | 零散辅助 | 只做小任务：测试补齐、文档同步、小修、脚本；不承担模块开发；遇争议升级给 Codex |
| **本鱼（DSH 主会话）** | 执行与验收 | 执行 Codex 裁决；维护契约与文档；跑全量测试、验收、更新计划状态、联调 |

**铁律**：`contracts/` 由本鱼维护，但**任何契约变更必须经 Codex 批准**后由本鱼执行；
其他 agent 不得直接改契约，发现问题报 Codex。

## 二、文件所有权（避免并行冲突）

| 路径 | 所有者 | 说明 |
|---|---|---|
| `go.mod` / `go.sum` | **Codex** | 其他 agent 需要新依赖 → 报给 Codex 加，禁止自己改 |
| `cmd/` | **Codex** | `fishd` 装配与 `fishctl` 入口 |
| `internal/event/` | **Codex** | 事件模型与总线 |
| `internal/store/` | **Codex** | repository 接口 |
| `internal/database/` | **Codex** | GORM 连接与实现、迁移 |
| `internal/config/` | **Codex** | 配置加载 |
| `internal/infra/` | **Codex** | 外部连接统一管理 |
| `internal/scheduler/` | **Codex** | 调度器 |
| `internal/brain/` | **Codex** | Brain + Provider 抽象 |
| `internal/collector/` | **WorkBuddy** | 采集器（bili-live 等） |
| `internal/sender/` | **WorkBuddy** | 发送器 |
| `internal/persona/` | **WorkBuddy** | 人格包加载与回归 |
| `personas/` | **WorkBuddy** | 人格包内容（纯数据） |
| `internal/memory/` | **WorkBuddy** | 记忆最小版（耗 token 的实现类） |
| `internal/observability/` | **WorkBuddy** | 日志聚合、心跳、日报（样板类） |
| `migrations/` | **Codex**（0.1 建表）+ **WorkBuddy**（后续补充迁移） | 建表 SQL 由 Codex 首发，后续由 WorkBuddy 加 |
| `scripts/` | **DSH flash** | dev.ps1 等 |
| `contracts/` | **本鱼** | 只有本鱼能改 |
| `plans/` | **本鱼** | 状态勾选由本鱼统一更新；agent 只读 |
| `docs/` | **本鱼** | 设计文档 |

**没有列出的新目录**：先报本鱼，确认归属后再建，不要私自加。

## 三、任务分派（Phase 0）

**分配原则（成本 vs 难度）**：

| 类型 | 判据 | 给谁 |
|---|---|---|
| 架构 / 契约 / 并发 / 状态机 | 错了要返工全项目、需要全局判断 | **Codex** |
| 代码量大但逻辑直 / 重复性高 / 读文档多人设 | 耗 token，不改架构 | **WorkBuddy** |
| 跑测试 / 补用例 / 改文档 / 小修 | 单点小活 | **DSH flash** |
| 验收 / 契约维护 / 计划状态 | 把关 | **本鱼** |

判定示例：「写采集器/发送器/记忆/日报」→ WorkBuddy；「设计事件总线/调度器/接口」→ Codex。

| 任务 | 内容 | 负责人 | 依赖 | 验收 |
|---|---|---|---|---|
| 0.1 | 仓库骨架、配置、zap 日志、fishd/fishctl、迁移建表 | **Codex** | 无 | `go build` 通过；`fishd` 能启动并打印配置；迁移可执行 |
| 0.2 | repository 接口 + GORM 实现 | **Codex** | 0.1 | 单测覆盖幂等插入/状态迁移 |
| 0.3 | 事件总线（Publish/ClaimPending/Mark/Notify + 恢复） | **Codex** | 0.2 | 并发 Claim、幂等、崩溃恢复单测全过 |
| 0.4 | 调度器（五道闸、优先队列、冷却、令牌桶、会话锁、配额） | **Codex** | 0.3 | 每道闸有单测；同 session 并发只产一个 Task |
| 0.5 | 弹幕采集器（live） | **WorkBuddy** | 0.3（事件接口冻结） | mock 平台测试；断线重连；自消息过滤 |
| 0.6 | Brain（OpenAI 兼容 + Provider 抽象 + 上下文组装 + 预算） | **Codex** | 0.2、0.7 | 组装顺序/预算截断/失败分类单测；mock LLM 集成 |
| 0.7 | 人格包迁移 + 回归集（≥30 条） | **WorkBuddy** | 无（可最早开始） | 回归通过率 ≥90% |
| 0.8 | 弹幕发送器（发送/核验/记账） | **WorkBuddy** | 0.3 | 成功/失败/核验失败三条路径单测；记账落库 |
| 0.9 | 记忆最小版（out 事件必写 + 简单提炼） | **WorkBuddy** | 0.3 | 事件落库；提炼 worker 单测 |
| 0.10 | 可观测（日志/心跳/日报文件） | **WorkBuddy** | 0.1 | 日报生成；30 分钟无事件告警单测 |
| 0.11 | 联调、验收、dev.ps1 收尾 | **本鱼** | 全部 | P0 退出标准逐条通过 |

## 四、并行节奏

```
第 1 轮（串行，Codex 独占）
  0.1 ──► 0.2 ──► 0.3
  0.3 完成后冻结事件契约，通知 WorkBuddy 可以开工
  （WorkBuddy 可提前并行做 0.7 人格包；flash 只做补测试/文档等零散活）

第 2 轮（并行）
  Codex: 0.4 ∥ WorkBuddy: 0.5 + 0.7 + 0.10

第 3 轮（并行）
  Codex: 0.6 ∥ WorkBuddy: 0.8 + 0.9

第 4 轮（串行，本鱼）
  0.11 联调 + 全量测试 + 更新 plans/phase-0.md 状态
```

**原则**：Codex 只留架构重活（0.1–0.4、0.6）；耗 token 不重的实现类（0.5、0.7–0.10）全部给 WorkBuddy；flash 只做零散辅助。

## 五、接口冻结与变更

- `contracts/event.md` 在 0.3 完成后**冻结**；任何字段/状态变更必须先报 Codex 批准，由本鱼执行。
- 0.2 的 repository 接口在 0.2 完成后冻结。
- 0.6 的 `Brain` 接口（`contracts/brain.md`）在 0.6 开工前冻结。
- 需要新增接口 → 报 Codex 裁决，由本鱼改契约并通知所有受影响方。

## 六、交接格式（每个任务完成后）

每个 agent 完成任务后必须产出（提示词模板见 `prompts/`）：

1. **代码**：符合 `AGENTS.md` 约定（中文注释、构造函数注入、`%w` 错误、zap、GORM 参数化）
2. **测试**：`go test ./...` 通过；关键路径有集成测试
3. **自检**：`gofmt -l .` 无输出、`golangci-lint run`（或 `go vet`）通过
4. **提交**：`phase0: <任务>（验证方式）`，直接 push（个人项目，不走 PR）
5. **汇报**：给主人一段话，说明：改了什么、怎么验证、有什么风险/未决项；由主人转给本鱼验收

**禁止**：没跑测试就交接；改了契约不报；碰了不属于自己的目录。

## 七、决策升级路径

```
实现层争议（怎么写、用什么模式）      → 自己定，按 AGENTS.md
模块/接口/契约争议                    → 报 Codex 裁决，本鱼执行修改
任务归属 / 文件所有权冲突             → 报 Codex 裁定
环境 / 账号 / 额度 / 平台风控          → 报主人（Codex 也只提建议，不替主人决定）
Codex 需要新增协作文档                → 可自建，创建后通知 WorkBuddy 与 DSH
```

- Codex 的裁决是最终结论；除非主人明确推翻，否则其他 agent 不再争论
- 本鱼不自行裁决技术问题，只负责执行、验证与记录

## 八、共同禁止事项

- ❌ 直接修改 `contracts/`（报 Codex，由本鱼执行）
- ❌ 修改别人的目录（见所有权表）
- ❌ 改 `go.mod`（Codex 除外）
- ❌ 提前实现后续 Phase
- ❌ 引入 Redis / MQ / Milvus / 分布式
- ❌ 内部调用写成 MCP / HTTP / gRPC
- ❌ 凭据写进代码或日志
- ❌ 无测试交付

## 九、环境前置（开工前确认）

- Go 1.25+（本机 1.27 可用）
- PostgreSQL 17+ 运行在 5432（**需要先安装/启动**）
- `.env` 已从 `.env.example` 复制并填好
- `golangci-lint` 可用（本地可用 `go vet` 代替）

## 十、状态追踪

- 任务推进节奏由 **Codex** 决定（可要求本鱼调整计划与优先级）
- 任务状态由**本鱼**在 `plans/phase-0.md` 统一勾选
- agent 不直接改 `plans/`；完成后按交接格式汇报给本鱼，由本鱼更新
- 设计缺口：写进汇报，由 Codex 裁决，本鱼记录
