# 10 · 可观测性与成本工程

> 核心信条：**静默失败比崩溃更可怕。** 进程活着但不回消息，比崩溃难发现一百倍。

## 1. 结构化日志

每条日志必备字段：

```
event_id, ch, session, action, result, cost_ms, detail
```

- 用 `zap`，JSON 输出到文件 + stderr
- 日志滚动：按天，保留 7 天
- **禁止**打印完整 prompt、cookie、token

## 2. 心跳与静默失败检测

| 指标 | 阈值 | 动作 |
|---|---|---|
| 单通道无入站事件 | 30 分钟 | warn |
| 单通道无出站消息 | 2 小时（直播中） | warn + 告警 |
| 采集器健康检查失败 | 连续 3 次 | error |
| 发送失败率 | > 30% / 10 分钟 | 告警 |
| 数据库不可用 | 立即 | 进程退出（让守护重启） |

告警渠道：先写日志 + `fishctl status` 显示；后续可加 IM 通知。

## 3. 日报（每天自动生成）

内容：

```
日期 | 通道 | 收/发条数 | 跳过原因分布 | 失败数 | LLM 调用次数 | token | 估算费用 | 活跃会话数
```

P0：输出 `logs/daily-YYYY-MM-DD.md`（文件，先不落库）。
P1 起：落库 `daily_stats`，同时保留文件。

## 4. 运维命令（`fishctl`）

| 命令 | 作用 |
|---|---|
| `fishctl status` | 各通道健康、最近事件、未处理数 |
| `fishctl tail` | 实时日志 |
| `fishctl dlq list/retry` | 死信查看与重放 |
| `fishctl events --ch live --n 20` | 最近事件 |
| `fishctl memory search "关键词"` | 记忆查询 |
| `fishctl persona eval` | 人格回归 |
| `fishctl cost --today` | 今日花费 |

## 5. 成本工程

### 5.1 预算层级

```
全局日预算 → 通道日预算 → 单次请求预算
```

超限行为：`降级`（换便宜模型/缩短上下文）或 `熔断`（停止非关键回复）。

### 5.2 模型分级

| 用途 | 模型档位 |
|---|---|
| 路由/分类 | 最便宜（或规则） |
| 弹幕回复 | 便宜 |
| 评论/私信 | 中档 |
| 记忆提炼 | 便宜（批量） |
| 复杂任务 | 高档（少量） |

### 5.3 缓存

- 人格包（soul/rules/samples）→ system 前缀缓存
- 会话最近对话 → 前缀缓存（保持稳定顺序）
- 梗库/人设 → 内存缓存，热更新

### 5.4 账本

`llm_usage` 表记录每次调用：模型、token、费用、耗时。
`fishctl cost --today` 输出当日汇总，日报里带。

## 6. 测试要求

- 静默失败检测：mock 无事件 30 分钟 → 触发 warn
- 预算熔断：mock 费用超限 → 降级/熔断路径正确
- 日报生成：给定事件数据 → 统计正确

## 7. 防坑清单

- ❌ 只记录崩溃不记录「不回消息」
- ❌ 日志里打完整 prompt（隐私 + 体积）
- ❌ 没有预算上限（某天突然烧穿）
- ❌ 成本账本不落库（无法复盘）

## 8. 延迟口径（2026-11-23 补充）

> 延迟报表必须区分「平台时间」与「我们看见的时间」；否则补历史评论会把 P90 算成几小时，优化就变成优化假指标。
> 说明：`collected_at`、`collect_mode` 与 `event_timings` 已在 L0 落地（迁移 `0025_event_timing.sql`）。旧 `Latency` 口径继续保留；新 `LatencyDetail` 同时输出 `all`/`fresh` 与五段拆分，历史事件缺失阶段保持 `NULL`，不伪造 0。

| 字段 | 定义 | 用途 |
|---|---|---|
| `at` | 平台发布时间（评论 `ctime` / 私信 `timestamp`） | 旧口径；只表示内容在平台上存在多久 |
| `collected_at` | 采集器第一次看到并落库的时间 | 用户体验口径的起点 |
| `enqueued_at` | 调度器把事件放入队列的时间 | 排队延迟 |
| `claimed_at` | worker 领取任务的时间 | 排队延迟终点 |
| `generated_at` | 回复生成完成的时间 | 生成延迟 |
| `send_started_at` | 发送器开始投递的时间 | 发送延迟起点 |
| `sent_at` | 首次成功发送时间 | 发送延迟终点 |
| `verified_at` | 回执/核验完成时间 | 投递确认 |

计算公式：

```
采集延迟   = collected_at - at
排队延迟   = claimed_at - enqueued_at（或 claimed_at - collected_at）
生成延迟   = generated_at - claimed_at
发送延迟   = sent_at - send_started_at
端到端旧口径 = sent_at - at
端到端用户口径 = sent_at - collected_at
```

统计规则：

- `all` 与 `fresh(<5m)` 必须分开；`fresh` 只统计 `collected_at` 在 5 分钟内、且 `collect_mode != 'backfill'` 的事件。
- 计时写入：事件落库自动写 `collected_at`；状态/租约变化自动写 `enqueued_at`、`claimed_at`、`verified_at`；`send_log.ok=true` 写 `sent_at`；`send_attempts/delivery_attempts` 进入 `armed` 写 `send_started_at`；`generated_at` 由生成 handler 写。
- 样本 `n < 30` 时只显示「样本不足」，不展示 P50/P90；`n=0` 显示 `—`。
- `backfill`（首次补 24h、批量补处理、旧评论回复）不进入 fresh SLO；只要求不丢、不重、可观测。
- `live` 的“1 秒级”只适用于短句单步；ReAct 多步与 30 天全量数字必须分开写，不能混用口径。
- 延迟统计只读时间与元数据，不把消息正文、prompt、cookie/token 写进报表或 trace。
