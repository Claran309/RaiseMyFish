# 0.13 配套能力派发单（分批）

> 背景：P0 收尾倒数第二块。按 dsh-bili-whale 对齐：活动流/送礼/常驻 Edge。
> 本批只放行 **0.13a 活动流 + Presence 最小版**；送礼（0.13b）、常驻 Edge（0.13c）验收后再发。

## 0.13a：活动流 + Presence 最小版

### 目标

大肥鱼能记录并展示「此刻在做什么」：在看直播 / 在回弹幕 / 在送礼 / 在剪视频。多值状态、带 TTL、摘要。

### 精确规格

文件：`internal/presence/presence.go`（新增），测试 `presence_test.go`。

```go
package presence

// State 是主体状态（多值：一个人可以同时"在看"和"在回"）。
type State struct {
    Kind    string    // watching | replying | sending | gift | editing
    Channel string    // live/comment/dm/qq
    Detail  string    // 例如 "直播间 30308220"
    Since   time.Time // 开始时间
    TTL     time.Duration // 过期时长（无则用默认 5m）
}

// Table 是状态表（内存，进程内）。
type Table struct {
    // ... 并发安全
}

func NewTable() *Table
func (t *Table) Set(s State)                // 写入/更新（同 Kind+Channel 覆盖）
func (t *Table) Clear(kind, channel string) // 移除
func (t *Table) Active(now time.Time) []State // 返回未过期状态（按 Since 排序）
func (t *Table) Summary(now time.Time) string // 摘要一句话：「在看直播 30308220 / 在回弹幕」
```

- 并发安全（sync.RWMutex）
- TTL 到期自动不返回（不删除也行，Active 过滤）
- 空表返回空列表 + 摘要空字符串（不 panic）

### 接入（本批只接采集侧，最小）

`internal/collector/bridge.go` 或 `cmd/fishd/serve.go`：
- 采集收到事件 → `table.Set(State{Kind:"watching", Channel:"live", Detail:房间})`（TTL 5m）
- Generate 开始 → `Set(replying)`；结束 → `Clear(replying)`
- 发送成功 → `Set(sending)`（TTL 1m）
- 只挂最小接入，**不改调度/发送语义**

### 禁止

- ❌ 不写数据库（P0 内存态）；不碰 contracts
- ❌ 不接 0.12 看直播（watch 是独立）
- ❌ 不执行 git

### 交付格式

- 文件列表、测试结果（并发/过期/多值/摘要）、接入点、未做项、是否暂停写入

## 后续批次（本单不放行）

- 0.13b：送礼（gift.mjs 搬：小花花 1 电池，B站 send_gift API）
- 0.13c：常驻 Edge（presence.mjs 搬：截图/点赞）——可选
- 0.13d：梗白学习（meme-learn.mjs）——P2 知识库联动
