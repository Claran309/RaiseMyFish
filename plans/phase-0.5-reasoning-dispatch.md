# P0.5 两段式轻量思考派发单

> 背景：当前每轮都固定召回记忆/知识（serve.go 直接调 recallMemory/recallKnowledge），简单弹幕也查一遍——又慢又费 token。
> 两段式：先判断「这条需要查记忆/知识吗」，需要才查，不需要直接单步生成。
> 判断先做**规则版**（关键词，零成本）；后续可换 LLM 判断（另批）。

## 允许路径

- 新增 `internal/reasoning/`（Decide 判断）
- `cmd/fishd/serve.go`（Generate 里接判断）
- 测试文件随实现
- 不碰 contracts、调度/发送/人格/知识库

## 规格

### 1. 判断器（internal/reasoning/decide.go）

```go
package reasoning

// Decision 是两段式第一段的输出。
type Decision struct {
    NeedMemory    bool // 需要查记忆（最近会话/跨通道/记忆记录/事实）
    NeedKnowledge bool // 需要查知识库（梗/设定）
    Reason        string // 命中原因（日志用）
}

// Decide 判断事件是否需要额外召回；纯函数、无网络、无模型。
func Decide(text string) Decision
```

规则（命中即 true）：
- `NeedMemory`：文本含「刚才/之前/上次/上回/你说/你提/你记得/那个/还记得/又」等追问指代词
- `NeedKnowledge`：文本含「梗/什么意思/什么梗/啥意思/知道吗/那个梗」等梗相关词
- 默认：都 false（简单弹幕不查）

### 2. 接入（serve.go）

- Generate 里：先 `dec := reasoning.Decide(ev.Text)`
- `dec.NeedMemory == true` → 调 `recallMemory`；否则 `Memory` 传空 Scope（同现状的空值）
- `dec.NeedKnowledge == true` → 调 `recallKnowledge`；否则 `Knowledge: nil`
- 判断本身失败不可能（纯函数）；召回失败仍按现有降级
- 日志：`action="reasoning"`、need_memory/need_knowledge/reason

### 3. 测试

- Decide：追问/指代/梗词命中；普通弹幕不命中；空文本不命中
- serve 接入：mock 断言「需要记忆才调 Recall」「不需要不调」
- 延迟：纯函数零成本（无 Go 网络调用）

## 禁止

- ❌ 不调模型（判断是规则版；LLM 判断留后续）
- ❌ 不改 Brain 接口；不碰 contracts/调度/发送/人格/知识库
- ❌ 不执行 git

## 交付格式

- 文件列表、测试结果、规则表、未做项、是否暂停写入
