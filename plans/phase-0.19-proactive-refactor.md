# 主动弹幕重构派发单（复用直播回复管线 + 注入视觉上下文）

> 用户架构决策：**「直播回复」整条管线独立解耦；主动弹幕只是多注入一段「画面描述」上下文**，走同一套回复管线（人设/记忆/知识/Brain/发送）。
> 现有 proactive 是独立简化管线（截图→变化→视觉→发送），要改成：截图→**详细识图**→构造 ContextInput（注入 Visual）→ **复用 Brain 管线**→发送。

## 现状 vs 目标

### 现状（傻）

```
20s tick → limit(小时窗口+30s gap) → Snapshot → 变化检测 → 
视觉模型（泛泛 prompt）→ 清洗/去重 → 发送
```

问题：视觉 prompt 泛泛、无人设/记忆/知识、独立管线。

### 目标（解耦复用）

```
20s tick
  → 1. limit.Check（**取消固定时间窗口**；只留 send_gap 30s）→ gap 跳过
  → 2. Snapshot：ffmpeg 截 1 帧
  → 3. **VisionDescribe**：专门视觉模型，详细识图
        prompt：「你是视图模型，专门用于识图，请你详细观察图片，并详细说明解释图中内容」
  → 4. 构造 ContextInput（**注入 Visual**）+ 会话信息（房间/主播/状态）
  → 5. **Brain.Reply**（复用：人设/记忆/知识/预算截断）——与弹幕回复同一管线
  → 6. 清洗/15字截断
  → 7. 去重（最近 20 条 Levenshtein ≥0.75 跳过）
  → 8. 发送（out 事件 + POST）
```

## 规格

### 1. Brain 上下文加 Visual（依赖注入）

`internal/brain/types.go`：

```go
type ContextInput struct {
    ...（现有字段）
    Visual string // 画面描述（主动弹幕/看直播注入）；空则不出现
}
```

`context.go`：Visual 非空时，在 user prompt 注入一段：
```
<visual>画面内容（的详细描述）</visual>
```
（与 State/记忆/知识并列；计入预算，超预算按现有 knowledge 降级逻辑处理）

### 2. VisionDescribe（详细识图）

`internal/proactive/vision.go`（新）：

```go
type VisionDescriber interface {
    Describe(ctx, framePath string) (string, error)
}
// prompt 固定：「你是视图模型，专门用于识图，请你详细观察图片，并详细说明解释图中内容」
// 输出多句详细描述（人物/动作/环境/文字/氛围），不限 15 字（这是上下文，不是弹幕）
```

### 3. 主动弹幕 Runner 重构

`internal/proactive/proactive.go`：
- 去掉 change.go 的像素变化检测（用户：没啥用，跳过）
- 去掉 max_per_hour 固定窗口（用户：取消）；保留 send_gap=30s
- Runner 依赖注入：`Snapshotter`、`VisionDescriber`、`Brain`（brain.Brain）、`Persona`（persona.Pack）、`memory.Reader`（可选）、`Sender`
- RunOnce：
  1. gap 检查
  2. Snapshot
  3. VisionDescribe（详细）
  4. 构造 ContextInput：`Visual: 描述`、`Persona: pack`、`Event: 合成事件`（Channel=live、Session=房间、Text="[主动] 看直播"）
  5. Brain.Reply → 清洗/15 字/去重
  6. 发送

### 4. 合成事件

主动弹幕没有弹幕事件——构造一个合成事件：
```go
ev := event.Event{
    ID:     "proactive:" + room + ":" + timestamp, // 稳定唯一
    Channel: event.ChLive, Session: room, Dir: event.DirIn,
    Text: "", FromName: "", At: now,
}
```
（Brain 校验要求 Text 非空？——检查：Text 空会拒绝；用固定文本「[画面] 正在看直播」作为事件文本，并注入 Visual）

### 5. 配置

```toml
[proactive]
enabled = false
interval_seconds = 20
send_gap_seconds = 30
# max_per_hour / min_change_percent 删除（或保留但不再使用？删除）
```

### 6. 测试

- VisionDescribe：mock 返回详细描述 → 注入 ContextInput.Visual
- Runner：复用 Brain（mock Brain 断言收到 Visual）、gap 跳过、去重、发送
- Brain：Visual 非空时 prompt 含 <visual> 段；空不出现；超预算降级
- 主动弹幕不再调用像素变化/小时窗口

## 禁止

- 不改 contracts、不碰调度/发送语义、不调真实模型/平台（mock）
- 不执行 git

## 交付格式

- 文件列表、测试结果、配置说明、链路图、未做项、是否暂停写入
