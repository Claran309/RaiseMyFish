# 0.9 记忆最小版 · WorkBuddy 派发单（分批）

> 背景：Codex 长期下线，WorkBuddy 主力开发；DSH 出规格 + 逐项验收。
> 本单只放行 **0.9a**；后续批次验收后再发。前置：0.8 发送器验收通过（待 Codex 完稿）。

## 0.9a：提炼规则纯函数（本批唯一任务）

### 目标

实现「规则版记忆提炼」纯函数：把一条已完成事件转成 0 条或多条 memory.Record 候选。不读写数据库、不调用模型、不依赖 Brain。

### 精确规格

文件：`internal/memory/extract.go`（新增），测试 `internal/memory/extract_test.go`（新增）。

```go
package memory

// Candidate 是提炼候选，尚未落库。
type Candidate struct {
    Kind       string // fact | promise | preference | first_seen
    Title      string // 一句话标题（≤60 字）
    Summary    string // 摘要（≤200 字）
    Importance float64 // 0..1
    Confidence float64 // 0..1
}

// Extract 按规则从一条事件提炼候选；orders 是规则优先级，返回按此顺序排列。
// 规则（只做这四条，不做语义理解）：
//   1. 承诺：文本含「我答应/我保证/一定/包在我身上」→ kind=promise, importance 0.9
//   2. 主人相关：文本含「主人」或 from_name 等于配置的 owner 名 → kind=preference, importance 0.8
//   3. 首次出现：该 session 下第一次出现的昵称（由调用方传入 isFirstNick bool）→ kind=first_seen, importance 0.4
//   4. 默认：其余不提炼（返回空）
// 过滤：空文本、纯符号、长度>4000 字 → 返回空；不提炼带 private 标记且调用方未授权的事件。
func Extract(ev Event, opts ExtractOptions) []Candidate

type ExtractOptions struct {
    OwnerName   string
    IsFirstNick bool
}
```

- 只做规则匹配，不做 LLM 调用、不读库、不写库。
- 文本截断/摘要都用 rune 计数，不切坏中文。
- 全部纯函数，无全局状态，测试可注入固定时钟（如需时间字段）。

### 禁止

- ❌ 不改 `internal/database/`、`internal/sender/`、`cmd/`、`contracts/`
- ❌ 不实现 worker（定时扫描/写库是 0.9b）
- ❌ 不调用模型、不连 PG、不读真实记忆目录
- ❌ 不新增依赖

### 交付格式

- 文件列表 + 测试命令与结果（`go test ./internal/memory/`）
- 每条规则的测试用例（至少：承诺/主人/首次/默认/空文本/超长/中文截断）
- 未做项与原因、是否暂停写入

### DSH 验收标准（逐条）

1. `go test ./internal/memory/` 通过
2. 四类规则各有正例与反例；空/纯符号/超长返回空
3. 承诺 importance 0.9、主人 0.8、首次 0.4；Confidence 有合理值
4. 中文截断按 rune，不出现半字符
5. 无越界改动、无新依赖、无模型/PG 调用

## 后续批次（本单不放行）

- 0.9b：提炼 worker（定时扫描 done 事件 → Extract → 写 memory_records；幂等）
- 0.9c：召回装配（Reader：Recent 滑窗 + CrossChannel，复用 0.2 MemoryRepo）
- 0.9d：测试（提炼幂等、隐私隔离、滑窗召回）

每批验收通过后才发下一批。
