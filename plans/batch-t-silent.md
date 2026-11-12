> ⚠️ **撤销（2026-11-12）**：非 bug——根因是主人关闭 fishd 未重启，非 ws 假死。本批不执行。

# 批 T：#90 直播弹幕静默假死（紧急）

## 现象（已实测）

- 20:30-23:11 期间直播间弹幕**一条都没进**（21:00/22:00 两小时 = 0 条）
- 同期 ws 心跳正常（Node 持续发 heartbeat），`collector_reconnect` 只出现在 20:29-20:36
- 23:11 重启后立刻恢复（23:13 起 @ 鱼弹幕正常回复）

## 根因

`internal/collector/live/process.go`：
- `case "heartbeat": if room != "" { reset() }` —— **心跳会重置 IdleTimeout**
- ws 半开（TCP 活着、心跳在发、服务器不再推弹幕）时，IdleTimeout 永不触发 → 不重连 → 静默空转
- 心跳把「收不到弹幕」伪装成「健康」

## 修法（治本）

1. **静默弹幕检测**：新增 `SilentLimit`（默认 10 分钟）——从 `LastEventAt` 起算，
   即使心跳正常，超过阈值无 event → 主动重建连接（report KindRuntime 触发退避重连）
2. **心跳不能再无条件 reset**：区分「心跳」与「事件」两个时钟；idle 只由 event 推进
3. **可观测**：静默触发时打 `live_silent_reconnect` 日志（含距上次事件秒数）
4. 配置：`[collector.live] silent_limit_seconds`（默认 600，范围 120-3600）

## 验收

- 单测：心跳持续但无 event → 超过 SilentLimit 触发重连
- 真机：断流场景恢复（可用模拟：暂停 Node 输出 event 只发 heartbeat）
- 不得误杀「直播间真没人说话」：默认 10 分钟阈值 + 日志可观测

## 执行规则

fishd 不重启（交付后 DSH 验证）；不执行 git。
