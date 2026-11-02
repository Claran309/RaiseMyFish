# 回复 @ 时带 @ 对方 派发单

> 背景：有人 @ 大肥鱼（reply_to_id=大肥鱼）时，鱼的回复应该 @ 回对方（「@昵称 内容」），像真人对话。
> B站弹幕文本 @ 可用（实测显示正常）；只对「被 @ 的弹幕」加，其他不@。

## 要求

### 触发条件

- 事件被 `structuredMention` 命中（reply_to_id == self_ids 或 reply_to_name == self_names）
- 且 `[scheduler.channels.live] reply_mention_back = true`（默认 true）

### 组装（serve.go Generate）

```go
// 回复文本：如果事件是 @ 我们的，前缀加 @对方昵称
if replyMentionBack && structuredMention(ev, policy) {
    target := ev.Meta reply_to_name ?? ev.FromName  // 对方昵称
    reply.Text = "@" + target + " " + reply.Text
}
```

### 约束

1. **长度**：B站弹幕 ≤40 字；`@昵称 ` 占 N 字，内容截断到 `40 - len(@昵称 )`（rune 计）
2. **昵称来源**：优先 `reply_to_name`（B站给的对方显示名），兜底 `from_name`
3. **空昵称**：跳过 @ 前缀（正常回复）
4. **不重复**：回复正文若已以「@」开头则不重复加
5. **只 live**：其他通道不加 @

### 配置

```toml
[scheduler.channels.live]
reply_mention_back = true # 回复 @ 时带 @对方（默认 true）
max_reply_runes = 40 # 弹幕长度上限（含 @）
```

### 测试

- @ 命中 → 回复前缀「@昵称 」
- 长度截断（昵称长时内容截短）
- 空昵称/重复 @ → 不加
- 非 @ 弹幕 → 不加

## 禁止

- 不改 contracts、不碰调度状态机语义
- 不真实调用（mock）
- 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、截断行为、未做项、是否暂停写入
