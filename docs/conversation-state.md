# 会话状态（私信 / 评论多轮）

> 依据：`D:/CodeStudy/growing/项目/会话状态升级清单-20261123.md`
> 原则：私信按 peer、评论按 thread root、直播保持无状态；只存摘要 + 最近 N 轮。

## 1. 会话键

| 通道 | conversation_id | 说明 |
|---|---|---|
| 私信 | `dm:{account}:{peer}` | 复用 `event.DMSession`，天然 1:1 |
| 评论 | `comment:{oid}:{root}` | 顶层评论 root=自己的 rpid；楼中楼取 `meta.root` |
| 直播 | 不建持久会话 | 高频、公开、现场变化快 |

实现：`internal/conversation/key.go`；`Short()` 只输出通道+哈希，用于日志。

## 2. 数据

- `events.conversation_id`：采集器写入，空表示旧逻辑。
- `conversation_state`：摘要、未答问题、轮数、最近事件、过期时间；**不存正文**。
- 最近轮次直接查 `events`（同 conversation，按时间升序），不再复制一份消息表。

## 3. 开关

```toml
[conversation]
enabled = false        # 总开关；关闭时行为与旧版完全一致
dm_enabled = false     # 先只开私信
comment_enabled = false
live_window_seconds = 0
recent_turns = 8
summary_max_runes = 300
ttl_hours = 168
```

## 4. 装配与预算

- `cmd/fishd/conversation.go` 读取摘要+最近轮次，注入 `brain.ContextInput.Conversation`。
- prompt 段 `<conversation>` 位于 `<context>` 之后、`[当前状态]` 之前；正文统一转义，不能伪造段。
- 摘要 ≤ `summary_max_runes`，单轮 ≤120 字；整体仍受 `max_prompt_tokens` 裁剪。
- 同一 conversation 由调度器会话锁串行；无 conversation_id 时回退 `channel:session`。

## 5. 隐私

- 私信会话只在 `private` Scope 内读写；公开通道永远不注入 DM 摘要。
- 评论 thread 是公开的，但只按 `oid+root` 聚合，不按用户全局合并。
- 日志只记 `conversation.Short(id)`，不记 UID 或正文。

## 6. 灰度顺序

1. `enabled=true, dm_enabled=true`，观察延迟/成本/串人；
2. 评论 10% → 50% → 全量；
3. 直播短窗口最后做，默认关闭。

异常时关 `enabled` 即可回退，不需要回滚数据。
