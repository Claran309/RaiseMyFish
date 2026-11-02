# 状态注入派发单（Presence → Brain prompt）

> 背景：Presence 状态机只做了记录（watching/replying/sending），**没注入 Brain 的 prompt**——所以鱼不知道「自己在看娇娇的直播」，说出「种地那条也是本鱼」这种身份混淆。
> 本批把状态注入 prompt，让鱼知道：此刻谁在播、自己在哪、在干什么。

## 允许路径

- `internal/brain/types.go`（ContextInput 加 State 字段）
- `internal/brain/context.go`（拼 prompt）
- `cmd/fishd/serve.go`（Generate 时读 Presence）
- `internal/config/watch.go` + `config/fishd.toml`（current_room/streamer_name 或复用 collector）
- 测试文件随实现
- 不碰 contracts、调度/发送/人格/知识库

## 规格

### 1. 配置

`[watch]` 加：
```toml
current_room = "1992099976" # 当前观看的直播间
streamer_name = "娇娇" # 主播名（映射；也可放知识库 viewers）
```
或复用 `[collector.live] room_id` + 新增 `[watch] streamer_name`。**DSH 裁决：用 [watch] current_room + streamer_name**（显式，不动采集配置）。

### 2. Brain 上下文

`ContextInput` 加：
```go
State string // 当前状态摘要，如「你正在看娇娇（房间 1992099976）的直播，你在她弹幕区发弹幕」
```

`context.go` 组装：把 State 拼进 user prompt 的合适位置（如「[当前状态] ...」），空则跳过。

### 3. serve.go 组装

Generate 时：
- 从 `presence.Table.Active(now)` 取 watching 状态（Detail=房间号）
- 结合配置 `[watch] current_room/streamer_name` → 生成 State 文本
- 若配置有 current_room 但 Presence 无 watching → 用配置兜底（「你在看 joy（房间）的直播」）
- 注入 `ContextInput.State`

### 4. 测试

- 断言 prompt 含「看」（streamer 名/房间）
- 空状态/无配置 → State 空、prompt 不出现该段
- Presence watching 有值时优先用

## 禁止

- 不改 contracts、不碰调度/发送/人格/知识库
- 不调真实模型（mock）
- 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、prompt 拼装位置说明、未做项、是否暂停写入
