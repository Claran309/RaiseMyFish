# 0.10 可观测最小版 · WorkBuddy 派发单（分批）

> 背景：Codex 长期下线，WorkBuddy 主力开发；DSH 出规格 + 逐项验收。
> 本单只放行 **0.10a**；后续批次验收后再发。

## 0.10a：日报渲染纯函数（本批唯一任务）

### 目标

实现「日报渲染」纯函数：给定一段时间窗口的统计数据，渲染成人类可读的日报文本。不查库、不调模型、不写文件。

### 精确规格

文件：`internal/observe/report.go`（新增），测试 `internal/observe/report_test.go`（新增）。

```go
package observe

// Snapshot 是一段时间窗口内的统计快照（由调用方从 repository 查询后传入）。
type Snapshot struct {
    WindowStart time.Time
    WindowEnd   time.Time
    Received    int     // 收到事件数
    Replied     int     // 已回复数
    Skipped     int     // 跳过数
    Failed      int     // 失败数
    CostUSD     *float64 // 费用（nil=未知，不显示 0）
    Channels    map[string]ChannelStat // 按通道细分
}

type ChannelStat struct {
    Received int
    Replied  int
    Skipped  int
    Failed   int
}

// Render 渲染日报文本（Markdown 或纯文本）；纯函数、确定性（固定时钟格式）、不读配置。
// 输出示例：
//   # 日报 2026-11-01 00:00 ~ 24:00
//   - 收到 120 / 回复 85 / 跳过 30 / 失败 5
//   - 成功率 78.7%
//   - 费用：未知（未配置报价） 或  $0.0123
//   - 通道：live 100/70/25/5；comment 20/15/5/0
func Render(s Snapshot) string
```

- 费用为 nil 时显示「未知」，**不能显示 0**（0 是明确免费，nil 是未知）。
- 成功率 = 回复/(收到-跳过)，分母为 0 时不显示百分比或显示「—」。
- 时间格式固定 `2006-01-02 15:04`，不依赖本地时区（用传入时间）。
- 不读环境变量、不写文件、无全局状态。

### 禁止

- ❌ 不改 `internal/database/`、`internal/sender/`、`cmd/`、`contracts/`
- ❌ 不实现心跳（0.10b）、不接 fishctl（0.10c）
- ❌ 不调用模型/PG、不新增依赖

### 交付格式

- 文件列表 + 测试命令与结果（`go test ./internal/observe/`）
- 边界用例：费用 nil/0/有值、成功率为 0/满分、空快照、多通道
- 未做项与原因、是否暂停写入

### DSH 验收标准（逐条）

1. `go test ./internal/observe/` 通过
2. 费用 nil 显示「未知」、0 显示 0、有值显示数值（不伪造）
3. 成功率分母为 0 不崩
4. 输出确定性（同输入同输出）
5. 无越界改动、无新依赖、无模型/PG 调用

## 后续批次（本单不放行）

- 0.10b：心跳检测（注入时钟，30 分钟无事件 → warn）
- 0.10c：`fishctl status` 接入（读 repository 统计 → Render）
- 0.10d：测试（真实 PG 统计、心跳边界）

每批验收通过后才发下一批。
