# 契约 · Sender（发送器）

## 1. 接口

```go
type Sender interface {
    Name() string
    // Send 发送一条回复。不负责重试（重试由上层任务队列负责）。
    // 必须做核验，核验结果如实在 Verified 中返回。
    Send(ctx context.Context, reply Reply, ev event.Event) (Result, error)
}

type Reply struct {
    Text    string
    Actions []Action // P0 只支持文本
    Meta    json.RawMessage
}

type Result struct {
    OK       bool
    MsgID    string
    Verified bool   // 核验通过
    Error    string
}
```

## 2. 语义（硬要求）

1. **不重试**：sender 只发一次，失败返回 error，由任务队列决定重试
2. **必须核验**：发送后确认（回读/回执），`Verified=false` 表示未确认
3. **必须记账**：无论成功失败，写 `send_log` 和 `out` 事件
4. **必须限流**：发送前从令牌桶取令牌（或由 scheduler 保证，二选一但必须明确）
5. **隐私**：公开通道的 sender 不得接收 private 内容

## 3. 通道差异

| 通道 | 核验方式 | 特殊 |
|---|---|---|
| live | 回执/回读 | 长度 ≤40 字 |
| comment | 回读 state == 0 | state 17 视为未通过，不重发 |
| dm | 回读最后一条 | 长度 ≤500 |
| qq | 协议端回执 | 群聊需 @ |

## 4. 错误分类

| 错误 | 上层处理 |
|---|---|
| 网络失败 | retry |
| 平台限流 | 退避后 retry |
| 内容违规（state 17 / 12006） | 记 dead，不重发 |
| 评论区关闭（12002） | skipped |
| 鉴权失效 | 暂停通道，告警 |

## 5. 记账

```sql
INSERT INTO send_log (event_id, channel, ok, verified, msg_id, error)
```

同时写 `out` 事件（status=done/retry/dead）。

## 6. 测试

- 成功→verified
- 失败→error，不重试
- 核验不通过→verified=false，error 说明
- 记账：每次发送都有 send_log 行
- 限流：令牌不足时返回明确错误
