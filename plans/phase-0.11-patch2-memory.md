# 0.11 补丁批次 2 · 记忆召回 + 知识接线

> 背景：0.9 记忆模块与 K0 知识库都验收了，但 serve.go 的 ContextInput 里 Memory/Knowledge 是空的——本批把数据真正喂给 Brain。
> 本批 + 顺带修 Failed 口径（批次 1 遗留）。

## 任务 1：记忆召回接线

### 现状

`cmd/fishd/serve.go`：

```go
input := brain.ContextInput{
    Event:   ev,
    Persona: pack,
    Memory:    memory.Context{Scope: scope},   // 空
    Shared:    memory.SharedState{Scope: scope},
    Knowledge: nil,
}
```

### 要求

1. 装配 `internal/memory` 的 Reader（0.9c 的 `reader.go`：Recent/CrossChannel/Records/Facts）
2. Generate 里按事件 Scope 调 Reader：
   - `Recent`（同会话滑窗，N 条）
   - `CrossChannel`（其他通道公开消息，隐私过滤）
   - `Records`（memory_records 摘要）
   - `Facts`（事实，当前有效）
3. 结果填入 `ContextInput.Memory` / `Shared.Facts`；**失败时降级为空**（不能因召回失败阻塞回复），但记日志
4. Reader 初始化失败 → fishd 启动时报错（配置问题早暴露）
5. 测试：mock Reader 注入、Scope 传递正确、召回失败降级、隐私不越界

## 任务 2：知识库接线（默认关闭）

1. `[knowledge] enabled=true` 时装配 K0 Reader（`internal/knowledge`）
2. Generate 里按事件 Scope+文本调 `Recall`，结果填入 `ContextInput.Knowledge`
3. 默认 false（配置已存在）；关闭时保持 nil
4. 测试：启用时 Knowledge 非 nil 且 Scope 一致；关闭时 nil

## 任务 3：Failed 口径对称（批次 1 遗留）

`internal/database/stats.go`：`Failed` 改为与 `Replied` 对称——窗口内 dir='in' 事件的 send_log ok IS FALSE 去重数；渲染 clamp。

## 禁止

- 不改 contracts、不碰人格、不调真实模型（mock Reader）
- 不改变 Brain/Scheduler/Sender 内部语义

## 交付格式

- 文件列表、测试结果、装配说明（Reader 构造参数）、未做项、是否暂停写入
