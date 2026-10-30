# Phase 0 · 弹幕单线闭环

> 目标：**在真实直播间稳定回弹幕**，同时把人格、记忆、观测的最小版立起来。
> 这是唯一可开工的阶段。三线顺序：先弹幕，评论/私信留到 Phase 1。

## 依赖

无（从零开始）。

## 任务清单

### 0.1 仓库骨架与基础设施
- [ ] Go module 初始化，目录结构按 `README.md`
- [ ] 配置加载（TOML + 环境变量覆盖 + 校验）
- [ ] 结构化日志（zap，JSON，落文件 + stderr）
- [ ] `cmd/fishd` 入口：装配依赖、优雅退出
- [ ] `cmd/fishctl` 入口：至少 `status` / `tail`
- [ ] `migrations/0001_init.sql` 建表（events/tasks/memory_sources/llm_usage/send_log/dlq）

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

- 弹幕采集用子进程还是自写 WS：0.5 先验证子进程，不行再自写
- 提炼 worker 的触发频率：先每 5 分钟扫一次

## 完成记录

> 每完成一项，勾选并写一句结果。格式：`[x] 任务 — 结果（验证方式）`
