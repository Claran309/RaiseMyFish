# Phase 6.5 · 编排层（Router/Worker 双层）

> Super-agent 对齐计划的编排阶段（见 [superagent-alignment.md](superagent-alignment.md)）。
> **仅在单机单会话形态撑不住时启动**，需要主人明确放行；不要为了架构漂亮提前拆。

## 依赖

Phase 0–6 完成，且出现以下信号之一：
- 多通道会话并行时，全局上下文（Presence）不足以协调
- 同一任务需要在多个 Worker 间流转（评论修复 → PR → 回复）
- 单 Brain 串行成为瓶颈

## 架构

```
人类交互（多通道）
     ↓
Router（全局上下文、任务分派、对外统一回复）
     ↓ 分派
Worker A（会话隔离）  Worker B（会话隔离）  Worker C（会话隔离）
     ↓ 汇报结果
Router 统一回复用户
```

- Router 不执行具体任务，只路由/编排/汇总
- Worker 隔离执行，不直接回复用户
- 复用现有 Event Bus/Scheduler/Brain/Persona/Presence；不重写底层

## 任务清单

### 6.5.1 Router 骨架
- [ ] Router 会话：接收 Trigger、判断是否分派、汇总 Worker 结果
- [ ] 全局上下文：Persona + Presence + Memory 摘要（复 use 现有层）
- [ ] 任务分派：按通道/任务类型路由到 Worker
- [ ] 统一对外回复：Worker 结果经 Router 发出

### 6.5.2 Worker 隔离
- [ ] Worker 会话隔离（独立上下文，共享主体状态）
- [ ] Worker 订阅 Thread 后续事件（复用 P1 Thread）
- [ ] Worker 汇报状态/结果给 Router

### 6.5.3 并发与恢复
- [ ] 多 Worker 并发上限
- [ ] 崩溃恢复：Worker 中断后 Router 重新分派
- [ ] Watchdog 加强：独立监控 Worker 是否卡住/偏离

### 6.5.4 测试
- [ ] 多 Worker 并行不串上下文
- [ ] Router 汇总不重复回复
- [ ] 崩溃恢复
- [ ] 与现有通道/人格/记忆兼容

## 退出标准

1. 同一任务在多通道连续推进（IM + 评论），上下文不丢
2. 多 Worker 并行不串话、不重复回复
3. Worker 崩溃可恢复
4. 未破坏既有契约（Event/Collector/Sender/Brain/Persona/Memory/Knowledge/Presence）

## 风险

| 风险 | 应对 |
|---|---|
| 过早引入双层 | 必须出现规模信号才放行 |
| Router 成为单点 | 单机先接受；P7+ 再考虑多实例 |
| 上下文预算膨胀 | 复用 Inline & Spill 思路，摘要+按需 |
