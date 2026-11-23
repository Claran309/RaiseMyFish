# 01 · 一条弹幕从 WSS 到发送

## 问题

直播弹幕是实时流：同一房间几十条/分钟，平台会断线、会重复、会乱序。要保证「不漏、不重、不刷屏」，同时不能让模型把过期弹幕当新消息回。

## 方案

```
WSS 帧 → 归一化 → 幂等入库(events) → 抢占(queued) → 五道闸 → Task(generate)
      → Brain/ReAct → 发送计划 → POST → 核验 → 记账(send_log/out)
```

- 幂等键 `live:{room}:{msg_id}`：重复帧直接 `ON CONFLICT DO NOTHING`。
- 领取用 `FOR UPDATE SKIP LOCKED` + 租约（owner/version/lease_until）；崩溃恢复只处理过期租约。
- 五道闸：ACL/噪音、冷却、令牌桶、会话锁、每日配额；任一不过就 `skipped` 或 `defer`，不进模型。
- 发送必须先落「不可变计划」再 arm，POST 后只核验、不重投；`send_log` 与 out 事件同事务记账。

## 关键代码路径

- `internal/collector/live/normalize.go`：帧校验、自消息过滤、幂等 ID。
- `internal/event/bus/bus.go` + `internal/database/queue.go`：Claim/Mark/Defer/Recover。
- `internal/scheduler/scheduler.go`：五道闸与 stage 分流。
- `internal/database/send.go`：live 发送计划、arm、核验与 `send_log`。

## 失败路径

- 平台断线：采集器指数退避重连；未落库的帧不推进水位。
- 租约过期：`RecoverExpired` 把 `sent/replying(verify)` 交回核验，不重发。
- 发送未知：按 `unknown_manual` 结清，人工核对，不自动重投。
- 积压超窗：按通道 `max_event_age_seconds` 标 `stale`，不进模型。

## 三个可回答问题

1. 为什么不用 Redis 做队列？—— PG 已有事务、租约与 `SKIP LOCKED`，单机规模不需要第二套一致性来源。
2. 为什么发送要「计划 → arm → 核验」？—— 平台没有幂等键，崩溃后只能核验不能重投。
3. 如何保证同一会话不并发回复？—— 调度器会话锁 + 仓储层租约条件更新，两层都不能省。
