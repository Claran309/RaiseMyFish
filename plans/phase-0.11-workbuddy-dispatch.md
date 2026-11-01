# 0.11 集成装配 · WorkBuddy 派发单

> 背景：0.5 采集、0.6 Brain、0.8 发送、0.4 调度器各自验收通过，但 fishd 主循环没串起来——0.11「运行与收尾」要补「消费循环」。
> 本单是集成任务，**WorkBuddy 按本规格实现**，不独立设计状态机；DSH 逐项验收。

## 唯一任务：fishd 消费循环（Claim → Scheduler → Brain → Sender）

### 允许路径

- 新增 `internal/config/scheduler.go`（调度策略配置）
- 新增 `cmd/fishd/serve.go`（消费循环装配）
- 修改 `cmd/fishd/main.go`（启动循环）、`config/fishd.toml`（加 `[scheduler]` 段）
- 测试文件随实现
- 不碰 `contracts/`、迁移、人格/知识/评测、其他 internal 模块

### 精确规格

#### 1. 配置（internal/config/scheduler.go + fishd.toml）

```go
type Scheduler struct {
    MaxConcurrent      int
    MaxPending         int
    ClaimBatch         int
    PollIntervalMS     int
    RetryDelayMS       int
    OperationTimeoutMS int
    WorkerTimeoutMS    int
    Channels           map[event.Channel]ChannelPolicy
}
type ChannelPolicy struct {
    Brain          string
    RatePerMinute  float64
    Burst          int
    DailyQuota     int
    CooldownMS     int
    MinRunes       int
    RepeatWindowMS int
    OwnerIDs       []string
    SelfIDs        []string
}
```

- 默认值：MaxConcurrent=4、MaxPending=64、ClaimBatch=8、PollInterval=500ms、RetryDelay=5s、OperationTimeout=10s、WorkerTimeout=30s
- 校验：并发 1–100、pending≥concurrent、批次 1–100、各通道 brain 非空/rate>0/burst≥1/quota≥1
- `fishd.toml` 加 `[scheduler]` + `[scheduler.channels.live]`（brain="default"、rate_per_minute=6、burst=2、daily_quota=200、cooldown_ms=10000、min_runes=2）

#### 2. 消费循环（cmd/fishd/serve.go）

装配顺序：
1. `scheduler.New(bus, schedulerRepo, options, workers, logger)`
2. `workers.Generate = func(ctx, w) (scheduler.Result, error)`：
   - 若 `w.Task == nil` → 返回错误（不伪造任务）
   - 组 `brain.ContextInput{Event: w.Claim.Event, Persona: pack, Memory: 空, Shared: 空, Knowledge: nil}`
   - `reply, err := brainRoute.Reply(ctx, brain.Task{...}, input)`；err 原样返回
   - `res, err := sender.Send(ctx, w.Claim, sender.Reply{Text: reply.Text})`；err 原样返回
   - 返回 `scheduler.Result{Replied: res.OK}`
3. `workers.Verify = func(ctx, w) (scheduler.Result, error)`：调 `sender.Resume(ctx, w.Claim)`
4. `workers.Deliver` 可置 nil（Generate 内已投递）
5. `sched.Run(ctx)` 启动；与采集器一起在 serveWithCollector 下运行

#### 3. main.go 接入

- `[llm] enabled=true` 且有路由时，装配 Brain + Sender + Scheduler；否则只采集（保持现状）
- 发送开关：`[scheduler]` 存在且 `[channels.live] enabled` 时接发送
- 日志：`action=consume_loop_start`（含并发/批次/通道数）

### 禁止

- ❌ 不伪造 Task/Memory/Presence；不调真实模型测试（用 mock Brain/Sender）
- ❌ 不改 `contracts/`、迁移、人格/知识/评测
- ❌ 不设计新的状态机；沿用 0.3/0.4/0.8 的接口语义
- ❌ 不做 Presence/知识注入（那是后续批次）

### 交付格式

- 文件列表（精确路径）
- 测试命令与结果（含真实 PG？本批可只用 mock；真实联调归 0.11 冒烟）
- 未做项与原因、是否暂停写入

### DSH 验收标准

1. `go build ./...` + `go test ./...` 通过（含新测试）
2. 装配测试：mock bus/repo/brain/sender 能跑通 Claim→Generate→Sender→Result 链路
3. 配置校验：非法值被拒
4. 不越界、不伪造数据、不改契约
5. `fishd check` 诊断无模型调用
