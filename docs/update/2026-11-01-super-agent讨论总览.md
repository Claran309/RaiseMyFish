# Super-agent 讨论总览

> 2026-10-31 ~ 11-01，主人与 DSH 围绕「养鱼是不是 super-agent」的完整讨论。
> 本文把分散在 README / superagent-alignment / presence / knowledge 里的线索串成一个整体。

## 1. 一句话

**养鱼是一个 super-agent 的单主体实例（开发中）**：一个「肥鱼」主体，通过多通道持续在线、并行处理任务；会话无状态，主体状态挂在 Persona + Presence + Memory 上。

## 2. 为什么是 super-agent

DeepSeek 的 [Super-agent 设计探索](https://m.ithome.com/html/1020615.htm) 提出四特征：

| 特征 | 养鱼现状 |
|---|---|
| 连续（多渠道同一 Agent） | ✅ 直播/评论/私信/QQ |
| 高效（并行多任务） | ⏳ 多通道并行采集；Brain 串行 |
| 主动（观察事件自主推进） | ⏳ 评论巡逻（P1） |
| 持久（在线 + 异常恢复） | ✅ PG + 状态恢复 + 心跳 |

## 3. 目标 vs 当前（诚实版）

| 项 | 实际 |
|---|---|
| 目标形态 | super-agent 单主体实例（连续/高效/主动/持久） |
| 当前形态 | 事件驱动单会话多通道 Agent：没有 Router/Worker、Thread、Watchdog、异步工具、编排 |
| 差距 | 按阶段补：Presence（P0.6）→ Thread（P1）→ 引用链（P2）→ Watchdog（P0.10/P4）→ 异步（P6）→ 编排层（P6.5） |

## 4. 讨论沉淀的五个设计决策

| 决策 | 内容 | 文档 |
|---|---|---|
| **知识库** | 独立知识层：K0 文件版（梗/设定/观众/游戏）→ K1 结构化表 → K2 RAG | `docs/knowledge.md` |
| **Presence** | 三心二意状态表：主体状态注册表（直播中/看直播中/剪视频中），多值并存、摘要每轮查、详情按需（P1 工具）、可见性服务端过滤 | `docs/presence.md` |
| **主体/会话分离** | 主体 = Persona + Presence + Memory；会话无状态；每次回复结束即散 | `docs/presence.md` §1 |
| **工具复用** | 别重复造轮子：dsh-bili-whale/bili-cli/ffmpeg 等外部进程调用；只自写人格/调度/记忆/状态/Brain | `README.md` 决策表 |
| **MVP 范围** | 对齐 dsh-bili-whale 全套：弹幕收发 + 看直播（截图/ASR）+ 配套（送礼/常驻 Edge/活动流） | `plans/phase-0.md` 0.12/0.13 |

## 5. 演进路线（阶段映射）

```
P0   看直播 + 弹幕收发（dsh-bili-whale 全套）
P1   评论 + 私信 + 巡检 + Thread 归组
P2   全局记忆 + 引用链 + 动态总结发动态
P3   QQ（支线，可延后）
P4   成本 + 观测 + Watchdog
P6   MCP 对外工具 + 异步工具
P6.5 编排层（Router/Worker，规模信号才放行）
```

## 6. 无痛切换（P0+P1+P2 = 约 5~7 天）

- 弹幕收发、看直播、评论/私信巡检、全局记忆、动态总结全部覆盖
- **双轨验证**：与 DSH 并行跑同一批事件，对比相似度/延迟/失败率，达标再切
- 切换 checklist 见 `docs/update/2026-11-01-架构演进讨论总结.md` §6

## 7. 两把尺子（防止过度设计）

| 保留（护栏） | 放松（束缚） |
|---|---|
| 契约真源、安全硬线 | 实现细节自己定 |
| 旧证据保留、不越权写 | 探索任务先做再评 |
| DSH 独立验收、双轨验证 | 范围明确后一路推到交付 |
| 人格 v5 定稿不再迭代 | 每步签收 → 四种情况才请示 |

## 8. 当前进度

- 已完成：0.1/0.2/0.3 基础设施、0.6 Brain、0.7 人格（v5）、K0-1~6 知识库
- 进行中：0.4 调度 / 0.5 采集 / 0.8 发送（MVP 主线）
- 待放行：Presence 0.6、P1 评论私信、P2 记忆+动态

## 9. 文档索引

- 定位：`README.md` 标题与一句话定位
- 对齐：`plans/superagent-alignment.md`、`docs/02-architecture.md` §9
- 设计：`docs/knowledge.md`、`docs/presence.md`、`docs/07-channels.md` §9
- 计划：`plans/phase-0.md`（0.12/0.13）、`plans/phase-1.md`（1.6/1.7）、`plans/phase-2.md`（2.12/2.13）
- 讨论：`docs/update/2026-11-01-架构演进讨论总结.md`
