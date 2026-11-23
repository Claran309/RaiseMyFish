# 开发计划总览

> 当前唯一可开工：**Phase 0**。其余阶段未到，不得提前实现。
> 开工/恢复时读本机协作文件 `plans/coordination.md` 与 `plans/assignment.md`（仅本地保留，不入库）；Codex 首席开发、DSH 独立验收与会话辅助、WorkBuddy 轻量开发。
> **分工与文件所有权见本机 `plans/assignment.md`**（Codex / WorkBuddy / DSH flash / 本鱼），开工前先读它。
> **Super-agent 对齐见 [superagent-alignment.md](superagent-alignment.md)**：养鱼是 super-agent 的单主体实例，当前是事件驱动单会话形态；按对齐表逐阶段演进，不在 P0 堆双层架构。
> **2026-11-23 改进清单（B1–B11）见 [b-optimization-2026-11-23.md](b-optimization-2026-11-23.md)**：独立改进工作包，不改变既有 Phase 状态与技术栈；按 P0→P1→P2 分批派发。
> **2026-11-23 延迟诊断补充清单（L 系列）见 [latency-optimization-2026-11-23.md](latency-optimization-2026-11-23.md)**：先修延迟口径与评论 dead，再谈轮询/排队优化；与 B2 trace 合并实现。
> **2026-11-23 长期记忆评审（M 系列）见 [memory-review-2026-11-23.md](memory-review-2026-11-23.md)**：版本链、实体关系、写入质量、混合召回、memory usage 与评测；与 B5 合并，不重复实现。
> **2026-11-23 Reflection 补充（R 系列）见 [reflection-optimization-2026-11-23.md](reflection-optimization-2026-11-23.md)**：只覆盖本仓库 R1/R2/R3/R7；ClaranAIM 的 R4–R6 不在本仓库。
> **2026-11-23 会话状态升级（C 系列）见 [conversation-state-2026-11-23.md](conversation-state-2026-11-23.md)**：私信按 peer、评论按 thread root、直播短窗口；默认关闭、按会话键灰度。

## 阶段状态

| Phase | 名称 | 状态 | 依赖 |
|---|---|---|---|
| 0 | 弹幕单线闭环 + 人格 + 观测 | 进行中：0.1–0.13 已验收；真实冒烟（弹幕 95收/87发）通过；看直播/送礼待真跑 | — |
| 0.5 | **知识库 K0**（文件知识库：梗/设定/观众/游戏） | ✅ K0-1~5 验收通过；K0-6 验收；K0-7 覆盖未过（留 P2 RAG） | 0 |
| 0.6 | **Presence 三心二意状态表**（主体状态注册表） | ✅ 0.13a 验收通过（内存态；持久化/暴露留后续） | 0 |
| **0.7** | **思考链（两段式轻量思考）** | ⬜ 待放行（P0 完成后；见 [phase-0.5-reasoning.md](phase-0.5-reasoning.md)） | 0 |
| 1 | 评论 + 私信通道（含 Thread 归组） | ⬜ 未开始 | 0 |
| 2 | 记忆正式化 + 知识库/RAG（K1/K2）+ 引用链召回 | ⬜ 未开始 | 0、1 |
| 3 | QQ 接入 | ⬜ 支线，**可延后**（主人 2026-11-01 明确） | 1 |
| 4 | 成本优化 + 可观测强化 + Watchdog | ⬜ 未开始 | 0–3 |
| 5 | Web 面板 | ⬜ 未开始 | 0–4 |
| 6 | MCP 对外工具（含异步 start/stop）+ **ReAct 思考链**（工具调用循环，见 [phase-0.5-reasoning.md](phase-0.5-reasoning.md)） | ⬜ 未开始 | 3 |
| 6.5 | **编排层**（Router/Worker 双层，多会话并行） | ⬜ 待放行（单机规模足够后） | 0–6 |
| 7 | 玩游戏（SMAPI） | ⬜ 未开始 | 6 |
| 8 | 开播（渲染管线） | ⬜ 未开始 | 5 |

## 执行规则

1. 每个 Phase 有「退出标准」，不达标不进下一个
2. 任务按编号顺序做；有依赖的标注
3. 每完成一个任务：更新本表 + 写一句结果
4. 不跨 Phase 提前实现（接口留缝即可）
5. 设计缺口记录在该 Phase 的「待定」区，不自行发明方向
6. **别重复造轮子**：采集/发送/QQ/MCP/Web/渲染优先用现成轮子（bililive-go、lanmei 的 gateway、官方 MCP SDK、ffmpeg/OBS、EAC render-template）；只自写人格/调度/记忆/状态/Brain 五个业务核心。选型拿不准先报主人。

## 阶段间的「可用」原则（ADR-0014）

每个通道闭环完成后即可上线使用，不等全项目：
- Phase 0 完成 → 直播弹幕可以真实运行
- Phase 1 完成 → 评论、私信可用
- 通道通过配置独立开关

## 里程碑

| 里程碑 | 含义 |
|---|---|
| M1 | 弹幕线在真实直播间稳定跑 2 小时 |
| M2 | 三通道（弹幕/评论/私信）共存，互不污染 |
| M3 | 记忆跨天、重启不丢 |
| M4 | QQ 与 B站 是同一条鱼 |
| M5 | 有面板、有日报、成本可控 |
| M6 | 外部 agent 能调用养鱼工具 |
| M7 | 星露谷可玩 |
| M8 | 能开播 |
