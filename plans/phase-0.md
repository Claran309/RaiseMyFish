# Phase 0 · 弹幕单线闭环

> 目标：**在真实直播间稳定回弹幕**，同时把人格、记忆、观测的最小版立起来。
> 这是唯一可开工的阶段。三线顺序：先弹幕，评论/私信留到 Phase 1。

## 依赖

无（从零开始）。

## 开工前置（环境检查）

- [ ] Go 1.25+（本机 1.27 可用）
- [ ] PostgreSQL 17+ 已安装并运行在 5432（**当前未检测到，需先装/启动**）
- [ ] `golangci-lint` 已安装（CI 用；本地可先用 `go vet` 代替）
- [ ] B站凭据可用：`bili` 在 `~/.dsh/bili-cli/.venv/Scripts/bili.exe`（配置路径，不必进 PATH）；弹幕监听用 `node <dsh-bili-whale>/tools/commands/danmaku.mjs`
- [ ] `.env` 从 `.env.example` 复制，填 `FISH_DB_DSN` / `FISH_LLM_KEY`
- [ ] 分支：远端默认是 `master`，CI 已监听 `main` / `master` / `dev` 任一

## 任务清单

### 0.1 仓库骨架与基础设施
- [x] Go module 初始化，目录结构按 `README.md`
- [x] 配置加载（TOML + 环境变量覆盖 + 校验）
- [x] 结构化日志（zap，JSON，落文件 + stderr）
- [x] `cmd/fishd` 入口：装配依赖、优雅退出
- [x] `cmd/fishctl` 入口：至少 `status` / `tail`
- [x] `migrations/0001_init.sql` 建表（events/tasks/memory_sources/memory_records/llm_usage/send_log/dlq）

0.1结果：364dcb3已推送；DSH COMM-004确认manifest31/31及单测/真实PG/迁移/运行路径通过，Codex另有Linux race和lint证据。强杀后的即时存活检测留到0.11；当前默认20秒心跳判定不变。

### 0.2 store 层
- [ ] repository 接口（EventRepo / TaskRepo / MemoryRepo / UsageRepo）
- [ ] PG 实现（GORM + pgx 驱动；幂等/状态机用 Raw SQL）
- [ ] 幂等插入测试
- [ ] 状态迁移测试

### 0.3 事件总线
- [ ] `Publish` 幂等入库
- [ ] `ClaimPending`（单进程版；预留 FOR UPDATE SKIP LOCKED）
- [ ] `Mark` 带条件状态迁移
- [ ] `Notify` 进程内 channel
- [ ] 崩溃恢复：replying 超时 → queued

### 0.4 调度器
- [ ] 五道闸：ACL / 噪音 / 幂等并发 / 节流 / 优先级
- [ ] 优先队列（container/heap）
- [ ] 冷却（time.AfterFunc）
- [ ] 令牌桶（x/time/rate，按通道配置）
- [ ] 会话锁（同 session 串行）
- [ ] 每日配额
- [ ] 单测覆盖每一闸

### 0.5 弹幕采集器（live）
- [ ] 复用 `danmaku-await` 子进程或自写 WS（二选一，先验证可用性）
- [ ] 标准化事件 + 幂等键
- [ ] 自消息过滤（self id）
- [ ] 断线重连（指数退避）
- [ ] Health 上报

### 0.6 Brain（OpenAI 兼容）
- [ ] `Brain` 接口实现
- [ ] 上下文组装：system(persona) + user(context/event/instructions)
- [ ] 预算截断（先裁记忆、后裁 persona，soul 保留）
- [ ] 超时/重试/失败分类
- [ ] `llm_usage` 记账

### 0.7 人格包与回归
- [ ] `personas/dafeiyu/` 包结构（meta/soul/rules/samples/context）
- [ ] 从 `~/.bili-ai/persona.md` + `persona-shared.md` + `persona-live.md` 迁移
- [ ] `eval/cases.jsonl` ≥ 30 条（直播场景为主）
- [ ] `fishctl persona eval` 命令
- [ ] 回归通过率 ≥ 90%

### 0.8 弹幕发送器
- [ ] 发送（复用 bili-cli 或自写）
- [ ] 核验（回执/回读）
- [ ] 限流（令牌桶）
- [ ] `send_log` + `out` 事件
- [ ] 失败分类（retry / dead / skipped）

### 0.9 记忆最小版
- [ ] 发送后写 `out` 事件（必写）
- [ ] 简单提炼 worker（先规则版：主人相关/承诺/首次出现）
- [ ] 写入 `memory_records`（importance/confidence）
- [ ] 召回：最近会话滑窗 + 跨通道事件（P0 不做语义）

### 0.10 可观测
- [ ] 结构化日志（event_id/ch/action/result/cost_ms）
- [ ] 心跳：单通道 30 分钟无事件 → warn
- [ ] 日报：收/发/跳过/失败/费用
- [ ] `fishctl status` 显示各组件健康

### 0.11 运行与收尾
- [ ] `scripts/dev.ps1` 一键启停
- [ ] 内存占用 < 300MB（不含 PG/协议端）
- [ ] README 补充「如何跑起来」
- [ ] 本阶段任务状态更新

## 退出标准（必须全部满足）

1. 真实直播间连续运行 **2 小时**：不回重、不漏、不崩
2. 重启进程：待处理事件不丢、已发消息不重发
3. 人格回归通过率 **≥ 90%**
4. 日报可读，能看出「回了多少、跳过多少、花了多少」
5. 静默失败检测生效（人为断开采集 30 分钟 → warn）
6. `go test ./...` 全绿

## 风险与应对

| 风险 | 应对 |
|---|---|
| bili-cli 子进程不稳定 | 优先子进程复用；失败则评估自写 WS |
| B站弹幕频控 | 令牌桶 + 冷却 + 每日配额 |
| prompt 缓存失效导致成本高 | 严格按「静态 system / 动态 user」组装 |
| 人格迁移后语气漂移 | 先原样迁移，再按回归集微调 |

## 待定（实现中记录）

- P0-G01（0.2/0.3/0.4 前，Codex 裁决）：`ClaimPending` 已把事件改为 queued，但调度文档仍要求 pending 上过滤/延期，且合法迁移无 queued→skipped；需统一抢占、过滤、retry_at 和任务原子创建的语义后再冻结接口。
- P0-G02（0.2/0.3 前，Codex 裁决）：恢复要求重新领取 queued/retry，而 Bus 只有 ClaimPending；超时恢复也缺少明确的状态起始时间/持有权依据。需确定领取、超时字段与补偿阶段标识，不得仅按事件平台时间判断 worker 已失效。
- P0-G03（0.5/0.8 前，Codex 设计，WorkBuddy 验证平台）：稳定平台消息 ID、失败 out 事件幂等键、发送回执/核验信息的持久化，以及「平台已收但本地尚未标 sent」的崩溃窗口仍未定义；只补 sent 超时规则不足以证明重启不重发。out 唯一写入责任按 coordination，memory 不重复写。
- P0-G04（部分已裁决）：Persona静态Pack接口已冻结为contracts/persona.md的P0-PACK-1，0.7b已放行；Memory最小类型与回归执行接口仍须0.6/0.9前确认。0.6不等待完整回归，真实人格≥90%仍等0.6，mock仅测试执行器。
- P0-G05（0.1 验收前，Codex 负责，DSH 验证）：CI 的 job 级 hashFiles 检查在 checkout 前执行，需核实表达式支持与空仓保护位置；lint action/latest 与现有配置版本需对齐；真实 PG 集成 job 仍被注释，功能验收前启用并实际执行。
- P0-G06（0.11 前，Codex 拆任务、WorkBuddy 修脚本、DSH 验收）：dev.ps1 已修 PID 命名，但仍需创建日志目录、避免 go run 父子进程生命周期遗漏、落实优雅退出/重复启动检查；补充验证命令并记录未通过项。
- P0-G07（0.11增强，Codex裁决，DSH复验）：0.1暂不追加单独PID存活校验，强杀后在线判断按默认20秒心跳时效；service.state是最后记录。0.11增强需同时考虑PID复用/进程创建身份与心跳/PG锁信息，设计方案另行明确，不能仅用os.FindProcess成功认定为同一实例。
- 弹幕采集用子进程还是自写 WS：0.5 先验证子进程，不行再自写
- 提炼 worker 的触发频率：先每 5 分钟扫一次

## 完成记录

> 每完成一项，勾选并写一句结果。格式：`[x] 任务 — 结果（验证方式）`

- [x] 0.1 — 已实现基础设施并推送364dcb3（DSH COMM-004独立验收；单测、真实PG、迁移幂等和运行检查）。
