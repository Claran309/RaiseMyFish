# 04 · 调度器设计

> 调度器回答四个问题：**回不回？什么时候回？用谁来拼？会不会重复？**
> 不拼 prompt、不发送、不做记忆提炼。

## 1. 输入与输出

| 输入 | 输出 |
|---|---|
| 新事件（pending） | Task（或 skipped） |
| 定时器（生物钟/冷却到期） | 主动 Task（可选） |
| 当前状态（会话忙/频控余量/预算） | 决策结果 |

```go
type Task struct {
    ID        string
    EventID   string
    Channel   string
    Session   string
    Brain     string   // 用哪个大脑实现
    Priority  int
    CreatedAt time.Time
    Deadline  time.Time
}
```

## 2. 决策模型

事件到达后按顺序过**五道闸**：

```
1. 公开/私密 ACL     → 该通道不允许处理的，跳过
2. 噪音过滤          → 关键词/长度/重复/机器人 → skipped
3. 幂等与并发        → 已处理/同会话处理中 → 等待或跳过
4. 节流              → 冷却中/令牌桶空/超预算 → 延后（不丢）
5. 优先级排序        → 进入优先队列
```

### 2.1 公开/私密 ACL
- `private=true` 的事件只允许进私密通道的上下文（查询层过滤，见 memory）
- 公开通道的 brain 只能拿到公开事件

### 2.2 噪音过滤（可配置）
- 纯表情/单字（可配白名单）
- 广告关键词
- 同用户短时间重复相同内容（哈希去重窗口 60s）
- 自己发的消息（`from` 命中 self id 直接 skipped）

### 2.3 幂等与并发
- 同一 `EventID` 只生成一个 Task
- 同一 `session` 同一时刻只允许一个 Task 在 brain 处理（会话锁）
- 锁实现：`sync.Mutex` map（单进程）或 PG advisory lock（跨进程预留）

### 2.4 节流
| 机制 | 参数 | 说明 |
|---|---|---|
| 冷却 | 同 session 回复后 N 秒内不再回 | 防刷屏 |
| 令牌桶 | 每通道频率 | B站弹幕 10s/条；评论按平台限制；QQ 自定义 |
| 每日配额 | 每通道每天上限 | 防账号被风控 |
| 预算 | 每通道每日 token/费用上限 | 超限降级或停 |
| 生物钟 | 时间窗（如 20:00–24:00） | 主动行为（自主发弹幕/看视频） |

### 2.5 优先级
| 优先级 | 来源 | 说明 |
|---|---|---|
| P0 | 主人私信/指令 | 立刻处理 |
| P1 | 直播间被叫（@/点名） | 优先 |
| P2 | 普通弹幕/评论 | 正常 |
| P3 | 主动行为（自主发弹幕） | 低，可跳过 |

同一优先级内按 `At` 升序。

## 3. 调度器状态机

```
事件 pending
   ├─ ACL/噪音 → skipped
   ├─ 重复/处理中 → 保持 pending（稍后重试）或 skipped
   ├─ 节流 → 保持 pending，设置 retry_at
   └─ 通过 → 创建 Task → queued
                ├─ brain 成功 → replying → (交给 sender)
                └─ brain 失败 → retry/dead
```

## 4. Go 实现建议

单 goroutine 主循环 + 优先队列，避免锁竞争：

```go
type Scheduler struct {
    in     <-chan Event
    timer  *time.Timer
    pq     *PriorityQueue // container/heap
    rules  Rules
    state  *StateStore
}

func (s *Scheduler) Run(ctx context.Context) {
    for {
        select {
        case ev := <-s.in:
            s.onEvent(ctx, ev)
        case <-s.timer.C:
            s.onTick(ctx)
        case <-ctx.Done():
            return
        }
    }
}
```

- 冷却/retry_at 用 `time.AfterFunc` 或最小堆定时器
- 会话锁用 `map[string]struct{}` + 释放回调
- 令牌桶用 `golang.org/x/time/rate`（标准库生态，不算引入新组件）

## 5. 配置项（`config/scheduler.toml`）

```toml
[global]
max_concurrent_tasks = 4

[throttle.live]
cooldown_seconds = 15
rate_per_minute  = 6
daily_quota      = 300

[throttle.comment]
cooldown_seconds = 30
daily_quota      = 50

[throttle.dm]
cooldown_seconds = 10
daily_quota      = 100

[throttle.qq]
cooldown_seconds = 5
daily_quota      = 500

[quiet_hours]
windows = ["02:00-08:00"]
```

## 6. 与大脑的交接

调度器只产出 Task（含 Event 引用），**不组装上下文**。
brain worker 拿到 Task 后自行：
1. 读 persona 包
2. 读 memory（按 channel + session 召回）
3. 读共享状态
4. 调 LLM / 外部 agent
5. 返回 Reply

## 7. 防影分身清单

- [ ] 自己发出的消息必须有 self id，采集端过滤
- [ ] 同一 session 会话锁
- [ ] 同一事件幂等
- [ ] 主动行为（自主发弹幕）与被动回复共享同一个冷却/令牌桶
- [ ] 发送后写 `out` 事件，scheduler 看到 `from=self` 的 `in` 事件直接 skipped

## 8. 测试要求

- 单元测试：五道闸每一闸的通过/拦截
- 并发测试：同 session 并发事件只产生一个 Task
- 恢复测试：模拟崩溃后 pending/queued 事件能重新调度
- 节流测试：令牌桶边界（第 N 条通过、N+1 延后）
