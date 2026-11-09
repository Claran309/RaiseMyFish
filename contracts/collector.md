# 契约 · Collector（采集器）

## 1. 接口

```go
type Collector interface {
    Name() string
    // Start 启动采集循环，阻塞直到 ctx 取消。
    // 事件通过 out 发送（调用方保证不阻塞）；错误通过 errc 报告后继续重连。
    Start(ctx context.Context, out chan<- event.Event, errc chan<- error) error
    // Health 供可观测层探活。
    Health() Health
}

type Health struct {
    OK          bool
    LastEventAt time.Time
    LastError   string
    Reconnects  int
}
```

## 2. 生命周期与语义

- `Start` 在一个 goroutine 里跑；内部负责重连（指数退避）
- 断线不影响进程；重连后继续产出事件
- 同一消息重复产出时最终由 Event 幂等去重；私信采集额外做进程内有界去重（见下），不改变总线的幂等责任
- 私信进程内已见集合的键是完整事件身份 `dm:{recv_actor}:{peer_uid}:{msg_id}`，不是裸 `msg_id`；同一 Collector 切换接收账号后，相同 peer/msg_id 属于不同事件，必须分别产出
- 私信批次有消息时接收账号（SelfID）必须非空；缺失时拒绝整批且不提交水位，绝不产出归属不明的事件
- 自己发出的消息必须过滤（避免影分身）
- `errc` 用于非致命错误上报（如解析失败），不因单条失败退出

## 3. 错误分级

| 错误 | 处理 |
|---|---|
| 网络断开 | 重连（退避 1s→2s→4s→…上限 60s） |
| 鉴权失效 | 上报 error，暂停该通道，等待人工/自动刷新 |
| 消息解析失败 | 记日志，跳过该条 |
| 平台限流 | 退避，降低频率 |
| 协议端不可用（QQ） | 上报，降级：该通道挂起，其他通道不受影响 |

## 4. 健康检查

可观测层每 30 秒读 `Health()`：
- `LastEventAt` 超过阈值（配置）→ 静默失败告警
- `Reconnects` 快速增长 → 通道不稳定告警

## 5. 实现要求

- 每通道一个 collector，可独立开关（`channels.<name>.enabled`）
- 不直接访问数据库（只产出事件）
- 不调 LLM
- 不拼 prompt

## 6. 测试

- mock 平台：正常消息 → 事件正确
- 断线重连：断开 3 次后恢复
- 自消息过滤：self id 消息不产出
- 私信：进程内已见键含接收账号，同一 Collector 切号后相同 peer/msg_id 仍产出两条事件
- 私信：有消息但缺接收账号的批次被拒绝，不产出事件也不提交水位
- 幂等：重复消息由总线去重（collector 不重复处理）
