# 记忆 LLM 提炼计划（P0.5 / P1）

> 决策（2026-11-02 主人定）：记忆归档/提炼应由**大模型判断**，不再只用规则。
> 当前 extractworker 是规则版（承诺/主人/首次出现），规则抓不到语义（「A 说自己是学生」「A 爱吃辣」）。

## 目标

```
events → Extractor（可插拔）
  ├── ruleExtractor（现在：承诺/主人/首次）
  └── llmExtractor（新：语义判断归档/提炼/重要度）
```

## 设计

### 1. 接口（可插拔）

```go
// internal/memory/extract.go
type Extractor interface {
    Extract(ctx context.Context, ev event.Event, opts ExtractOptions) ([]Candidate, error)
}
```

- `ruleExtractor`：现有规则版（零成本）
- `llmExtractor`：LLM 判断（语义理解 + 重要度 + 归档建议）

### 2. LLM 提炼的输入/输出

**输入**：事件文本 + from_name + 上下文（最近 N 条）+ 已有记忆（可选）

**输出**（JSON）：
```json
{
  "archive": true,           // 是否值得归档
  "records": [
    {"kind": "fact", "title": "A 是学生", "summary": "...", "importance": 0.7}
  ],
  "confidence": 0.8
}
```

### 3. 触发策略（成本控制）

不是每条事件都跑 LLM：
- 只对「@ 鱼的弹幕」「长文本」「互动强（礼物/亲密行为）」跑 LLM 提炼
- 普通弹幕只走规则
- 或者：规则版先筛（命中承诺/主人）→ 再 LLM 精炼

### 4. 存储

- `memory_records`（现有）+ `kind='fact'` 支持事实归档
- `memory_sources` 记录来源（事件 ID）

### 5. 预算/降级

- LLM 提炼失败 → 降级规则版（不阻塞）
- 每事件 LLM 成本记账（llm_usage）

## 时机

- P0 结束后（与「架构重构」一起讨论）或 P1 早期
- 依赖：ReAct 工具化（llmExtractor 可作为工具 `memory_archive`）
