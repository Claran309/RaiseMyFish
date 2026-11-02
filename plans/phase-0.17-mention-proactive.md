# @ 过滤 + 主动发弹幕派发单

> 背景：当前鱼对每条弹幕都回复（可能刷屏/招烦）；要改成「只回 @ 鱼的弹幕」+「每 20 秒主动看画面说一句」。
> ⚠️ 主动发弹幕有频控/封号风险：必须限流 + 画面无变化就不说。

## 任务 1：只回复 @ 鱼的弹幕

### 配置

`[scheduler.channels.live]` 或新 `[live]` 段：
```toml
reply_only_mention = true # 只回 @ 鱼的弹幕（默认 true）
self_names = ["大肥鱼", "Claranの大肥鱼"] # 自己的显示名（用于匹配 @）
```

### 逻辑（serve.go Generate）

- `live` 事件：检测文本是否含 `@<self_name>`（任一），或 B站 mentions 字段含 self（若采集有）
- 不是 @ 的 → **Mark skipped**（不生成回复，省 LLM 费用）
- 是 @ 的 → 正常流程
- 其他通道（comment/dm/qq）不受影响

### 测试

- @ 命中 → 正常回复；未 @ → skipped 且无 LLM 调用
- 配置关闭 → 全部回复（保持现状）

## 任务 2：主动发弹幕（每 20 秒看画面说一句）

### 配置

```toml
[proactive]
enabled = false # 默认关（开之前先短信测试）
interval_seconds = 20
max_per_hour = 6 # 每小时最多主动说几条（防刷屏）
min_change_percent = 5 # 画面变化小于 5% 就不说（避免静态画面重复）
```

### 流程

```
每 interval_seconds：
  → 截 1 帧（ffmpeg 抽当前画面，1 秒内）
  → 与上一帧对比（像素变化百分比）→ 变化 < min_change_percent 跳过
  → deepseek 读帧：描述画面 + 生成一句观众视角弹幕（≤15 字）
  → 去重（与最近 N 条主动弹幕相似度过高跳过）
  → 发送（走 sender，构造 out 事件；不依赖 input 事件）
  → 限流：max_per_hour 检查
```

### 说明

- 主动弹幕不回复任何事件，是「观众发言」——走 sender 直接发
- **必须**用 event.DirOut 事件记账（唯一写入责任在 sender）
- 画面无变化 / 相似度高 / 超限 → 跳过（日志 action="proactive"、reason=no_change/similar/limited）
- 失败降级：不阻塞采集/回复

### 测试

- mock 截图/读帧/发送：变化不足跳过、相似度高跳过、超限跳过、正常发送
- 限流：60 分钟内最多 max_per_hour 条

## 禁止

- 不改 contracts、不碰调度状态机语义（主动弹幕走新路径）
- 不真实调用（测试 mock）；不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、限流/去重逻辑、未做项、是否暂停写入
