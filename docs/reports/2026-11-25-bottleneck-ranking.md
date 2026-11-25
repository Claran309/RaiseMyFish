# RaiseMyFish staging 瓶颈排序（2026-11-25）

> 依据：R1/R2/R3/R4/R6/R7/R8 原始 JSONL（`bench/raw/`）。
> 只排序有实测证据的瓶颈；未测到的项不写成“可能”。

## 1. 同一 live session 串行（P0）

**证据**：R3 600 条/分钟、同一用户、同一直播间，3 组中位数
P50 **203.7s**、P99 **415.7s**、最大 pending **1,730**，180 秒 drain 后仍有
约 **1,600** 条未完成（`bench/raw/R3-20261125-125010/drain.json` 等）。

**代码位置**：`internal/scheduler/scheduler.go:415`，事件按 `session(c.Event)`
检查 busy；同一房间同一时间只允许一个 active，其它事件 `deferClaim`。

**影响**：单房间 10 条/秒不是当前架构的容量；R1 240 条/分钟（4 条/秒）能过，
不代表 600 条/分钟能过。解决方向是“每会话串行、跨会话并行”已有，但单会话内
要做合并/丢弃策略或异步生成，不能靠加 goroutine 绕过顺序。

## 2. kill -9 后租约等待（P1）

**证据**：R7 进程恢复 5.15–5.20 秒（watchdog 检测 PID 消失），但测量窗口内
168 条 `queued/retry` 未完成；长 drain 组在旧租约过期后清零
（`R7-20261125-151315`，306/306）；`recovery.json` 与 `counts.json`。

**代码位置**：`internal/event/bus/bus.go:26` 默认租约 **5 分钟**；
重启只恢复过期租约，不立即抢回旧进程持有的 lease。

**影响**：观众看到的是“进程活了但鱼几十秒不回”；需要在恢复路径中缩短
孤儿租约/主动 Recover，或把 worker 租约与优雅退出绑定。

## 3. DB 连接中断不自愈（P0）

**证据**：R8 终止 bench DB 连接 30 秒，fishd 在 4–5 秒内记录
`数据库健康检查失败，退出服务`，之后 19091 `/metrics` 消失；3 组均如此。

**代码位置**：`cmd/fishd/main.go:396-401`，健康检查失败直接 `return err`。

**影响**：没有 watchdog 时一次 DB 抖动就会让服务退出；有 watchdog 时依赖
外部进程。需要明确“退出+watchdog”还是“进程内重连”，不能把两者混为自愈。

## 4. 记忆提炼吞吐约 10 条/秒（P1）

**证据**：R6 1k 在 90 秒内 done 1,000；5k 在 300 秒内 done 3,100、
pending 1,900，延长到 600 秒后 done 5,000、pending 0，吞吐约 8–10 条/秒
（`memory-backlog.txt`）。

**位置**：`internal/memory/extractworker/worker.go` 的任务领取/批处理；
当前是单 worker 的批循环。

**影响**：历史回填 5k 需要约 8–10 分钟；不是发送失败，但会拉长“记忆可见”延迟。

## 5. mock LLM 不是瓶颈（排除项）

R4 直接并发：10 并发 peak=10，50 并发 peak=50，3 组 0 错误，
P99 约 1.01s（固定 1s 延迟）。因此 R3 的长尾不能归因于模型。

## 6. DB 连接池不是当前瓶颈（排除项）

R1/R2 的 `fishd_db_pool_in_use` 为 1–2，`db_pool_wait_count=0`；
R3 积压时也没有池等待证据（`snapshots.jsonl`）。当前瓶颈在会话串行与
逐事件发送事务，不是连接数耗尽。

## 建议优先级

1. 单会话背压：合并/丢弃策略、异步生成、发送与生成解耦（对应 `leftovers #59`）。
2. 重启恢复：缩短孤儿租约或启动时主动 Recover，避免“进程恢复但事件等 5 分钟”。
3. DB 故障策略：明确退出+watchdog 或进程内重连，并加故障演练。
4. 记忆提炼：按需增加 worker/批量，但要先做 DB 写入压测。

## 原始数据索引

- R1 四档：`bench/raw/R1-20261125-*`
- R2 多 worker：`bench/raw/R2-20261125-*`
- R3 同用户刷屏：`bench/raw/R3-20261125-*`
- R4 mock 并发：`bench/raw/R4-20261125-131831/`
- R6 记忆积压：`bench/raw/R6-20261125-*`
- R7/R8 故障：`bench/raw/R7-20261125-*`、`bench/raw/R8-20261125-*`
