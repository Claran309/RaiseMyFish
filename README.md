# RaiseMyFish · 自研养鱼框架（Super-agent 单主体实例 · 开发中）

> 一条 AI 肥鱼的多通道陪伴系统：看直播、回弹幕、回私信、回评论、上 QQ，
> 有共享记忆、有人格一致性、能无人值守跑。
>
> **状态：Phase 0 收尾中（0.1–0.19 已验收）。**
> - 已验收：0.1–0.13（采集/调度/Brain/发送/记忆/知识/观测/看直播/Presence/送礼）+ 0.16 ReAct + 0.17 @过滤+主动弹幕 + 0.18 @回复带@ + 0.19 主动弹幕重构（复用 Brain + Visual 注入）
> - 真实冒烟：弹幕 95 收/87 发全核验；@ 回复、拉拉梗、状态注入、记忆提炼、知识库均实测
> - 剩余：2 小时正式验收、架构重构（P0 后）、梗库扩充、ReAct 工具扩充

---

## 一句话定位

不是「又一个 QQ 机器人」，而是：

> **一个事件驱动的、多通道、带人格与记忆的 super-agent（单主体实例）。**

一个「肥鱼」主体——Persona（怎么说）+ Presence（现在在做什么）+ Memory（记得什么）——通过直播/评论/私信/QQ 多通道持续在线、并行处理任务；会话本身无状态，每次回复完即散。

与 DeepSeek Super-agent 架构的对照与演进计划见 [plans/superagent-alignment.md](plans/superagent-alignment.md)（Thread 归组、引用链召回、Watchdog、异步工具、Router/Worker 编排层按阶段推进）；完整讨论沉淀见 [docs/update/2026-11-01-super-agent讨论总览.md](docs/update/2026-11-01-super-agent讨论总览.md)。

## Super-agent 讨论沉淀（2026-11-01）

| 维度 | 结论 |
|---|---|
| 定位 | super-agent 的单主体实例（开发中） |
| 当前差距 | 无 Router/Worker、Thread、Watchdog、异步、编排 → 按阶段补 |
| 五个决策 | 知识库（K0→K2）、Presence 状态表、主体/会话分离、工具复用、MVP 对齐 dsh-bili-whale |
| 演进路线 | P0 看直播+弹幕 → P1 评论私信+Thread → P2 记忆+动态 → P4 Watchdog → P6 异步 → P6.5 编排层 |
| 无痛切换 | P0+P1+P2（约 5~7 天）+ 双轨验证 |
| 两把尺子 | 护栏：契约/安全/旧证据/验收；放松：实现细节/探索先做再评/四种情况才请示 |

## 核心决策（详见 `docs/14-decisions.md`）

| 决策 | 结论 |
|---|---|
| 语言 | Go |
| 数据库 | PostgreSQL（从 Phase 0 起），pgvector 备用 |
| 中间件 | 不引入 Redis / 消息队列 / Milvus / 分布式（单机多进程足够） |
| 架构 | 事件驱动：Collector → Event Bus → Scheduler → Memory/Brain → Sender |
| 人格 | Phase 0 就做，配回归测试集，人设包化可热更新；判断器 v5 定稿（安全硬线唯一失败条件、风格仅观察） |
| 通道顺序 | Phase 0 弹幕单线闭环 → Phase 1 评论 → Phase 1 私信 → Phase 3 QQ（支线可延后） |
| MCP | 只用于对外暴露能力（给 Codex/Claude 等外部 agent）；内部模块用 Go interface |
| 知识库 | 独立「知识层」：K0 文件版（梗/设定/观众/游戏）→ K1 结构化表 → K2 RAG，见 `docs/knowledge.md` |
| Presence | 「三心二意状态表」：主体状态注册表（直播中/看直播中/剪视频中），多值、摘要每轮查、详情按需，见 `docs/presence.md` |
| 工具复用 | 别重复造轮子：采集/发送/QQ/MCP/Web/渲染用现成轮子（dsh-bili-whale、bili-cli、ffmpeg 等），只自写人格/调度/记忆/状态/Brain |
| MVP 范围 | 对齐 dsh-bili-whale 全套：弹幕收发 + 看直播（截图/ASR）+ 配套（送礼/常驻 Edge/活动流） |
| 部署 | 开发期本机直跑；交付 Docker Compose |

## 项目亮点

- **秒回**：真实直播冒烟中，从收到弹幕到回复发出 **1 秒内**（LLM 359–926ms + 调度/发送链），对比旧 DSH 方案 30~40 秒；32 分钟连续跑 87 条、全部发送核验通过（见 `plans/reports/0.11-live-smoke.md`）
- **安全硬线**：人格 v5 判断器只保留隐私/资产红线，风格不误杀
- **主体状态**：Presence 三心二意状态表让多会话互知「肥鱼现在在做什么」，状态注入 prompt（「那是娇娇种的，本鱼只负责看」实测）
- **两段式思考 + ReAct**：简单弹幕 1 秒单步；需要查事实（记忆/知识）时走 ReAct（MaxSteps=3、8 秒预算、超时降级），工具按事件 Scope 隔离
- **结构化记忆**：events 全量 + memory_records 提炼 + Reader 按需召回（不是把记忆塞提示词）
- **可观测**：日报/心跳/status/费用统计 + 错误链可见化
- **@ 回复带 @**：结构化 reply_mid 匹配（B站 @ 不在正文里），回复时 @ 发弹幕的人

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
| `docs/knowledge.md` | **知识库与 RAG**：人格/知识/记忆分层、知识源、K0/K1/K2 阶段 |
| `docs/presence.md` | **Presence 三心二意状态表**：主体/会话分离、状态注册表、租约机制 |
| `plans/superagent-alignment.md` | **Super-agent 对齐**：四特征/五组成对照、阶段映射、演进原则 |
| `docs/update/2026-11-01-架构演进讨论总结.md` | 最近讨论事项汇总（11 主题 + 切换 checklist） |
| `contracts/` | 模块接口契约（event/collector/sender/brain） |
| `plans/` | Phase 0–8 开发计划与任务清单 |
| `plans/assignment.md` | **分工与文件所有权**（给开发 agent 读） |
| `plans/coordination.md` | **协作裁决、任务看板、通知与验收交接入口** |
| `contracts/persona.md` / `plans/phase-0.7b-dispatch.md` | **Pack接口基线与WorkBuddy加载器任务范围** |
| `contracts/memory.md` / `plans/phase-0.6-dispatch.md` | **Memory最小上下文基线与Codex Brain任务范围** |
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

本地 lint：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...`。CI固定Go1.27与lint2.15.0；模块最低Go1.25。历史0.7c草稿留在已忽略的.workbuddy目录，不参与构建；0.6/0.7b已接收，0.7c新实现的自测仍不能替代DSH独立复核。

## 0.6 Brain装配与无调用诊断

`config/fishd.toml` 默认 `llm.enabled=false`，不要求模型密钥。启用后配置 `[llm.providers.<name>]` 的 type=openai、base_url、api_key_env、models与 `[llm.routing]` 的 default/live等路由；密钥仍只从环境/dotenv读。配置换Provider/model，代码不变。未实现的通道仍必须关闭。

```powershell
# 在本机配置副本启用llm并填写可信端点/模型清单后：
go run ./cmd/fishd --config config.local.toml --brain-route default check-brain
```

诊断只校验PG/迁移、人格和路由装配，输出provider_called=false，不调用模型、不写虚假的LLM账本，也不声称验证了远端服务或Memory Reader。正式生成由Brain接口接受显式Task/ContextInput；本轮不提供会话消费、发送器或人格eval CLI。

`FISH_LLM_BASE_URL` 只覆盖default路由对应Provider端点；`FISH_LLM_MODEL` 只覆盖default路由的模型且必须在models清单内，显式live等路由不改。每个唯一provider:model共享一个Brain并发上限，别名路由复用实例；这不是后续scheduler的全局并发控制。

保守prompt预算为UTF-8消息字节数+64框架余量，默认8192；超过预算先裁记忆，再裁samples/rules后缀，soul/场景/当前事件保留，不可能容纳时拒绝调用。输入另限4096条/引用和4MiB文本，准备阶段也受总超时/并发约束。每次实际Provider调用写一行llm_usage，未知统计/报价为NULL，绝不把未知费用当0；报价按model放在provider.prices，只有明确免费才填0。

网络/超时/5xx统一由Brain最多3次实际调用，空回复最多补1次且共享总次数；4xx含429不重试，协议错误不重试。取消后对已经发生的调用有独立5秒上限的记账；记账失败立即返回错误，不重复生成。HTTP成功但正文不支持时仍保存有效usage；完整JSON或计数无效时不伪造用量。0.6模块测试不等于人格评测，真实结果见下节0.7c。

## 0.7c 真实人格评估

```powershell
# 在本地配置副本启用llm，并使用.env中的真实key；这会调用配置模型并产生费用
go run ./cmd/fishctl --config config.local.eval.toml persona eval --pack dafeiyu --route default --timeout 20m
```

执行前冻结35条用例、人格/代码hash、persona-lexical-v1规则和生效参数；不把expect/forbid或baseline回复传给模型。每条独立空记忆Scope，顺序调用实际Brain/Provider并写PG账本。主分数按原始模型文本、次分数按Brain投递文本，错误不从分母移除，停止/致命错误不报告完整通过。无baseline回放模式，证据目录独占创建不覆盖。

本轮真实结果见 [原始报告](plans/reports/persona-eval/20261031T105248Z-501d0282/report.md) 与 [核对/解释](plans/reports/persona-eval/20261031T105248Z-501d0282/analysis.md)：请求deepseek:deepseek-chat，服务端标识deepseek-flash；dafeiyu@v1；35/35完成、35次实际调用；原始/投递均15/35=42.9%，未达90%。输入80,878、输出495 tokens，报价未配置，费用NULL。没有改人设/用例/词表去美化结果，也没有重抽样挑选输出。

评分是可重现的严格词法指标；如「不给。密码这种东西哪能往外掏。」会因词“密码”触发泄露凭据标签。这类误报保留原分，后续若修订标准必须另开新版本/run。当前8192保守字节预算也会裁掉samples并截短rules，具体影响需另行验证，不覆盖本轮结论。
