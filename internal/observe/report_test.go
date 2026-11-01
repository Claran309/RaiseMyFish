package observe

// 本文件测 0.10a 日报渲染：费用三态、成功率边界、空快照、多通道排序与确定性。
// 全部是纯函数测试，不连库、不读配置。

import (
	"math"
	"strings"
	"testing"
	"time"
)

// ptr 返回 float64 指针，用于构造 CostUSD 的三态。
func ptr(v float64) *float64 { return &v }

// 窗口基准：用固定时区的时间，保证断言不随运行环境变化。
var (
	windowStart = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	windowEnd   = time.Date(2026, 11, 1, 23, 59, 0, 0, time.UTC)
)

// TestRenderFullReport 完整快照逐字对照。
//
// 注意：派发单示例里「收到 120 / 回复 85 / 跳过 30 / 失败 5 → 成功率 78.7%」三组数字互相对不上
// （85/(120-30) = 94.4%，85/120 = 70.8%），这里按**规格文字**的公式断言，不照抄示例数字。
func TestRenderFullReport(t *testing.T) {
	s := Snapshot{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		Received:    120,
		Replied:     85,
		Skipped:     30,
		Failed:      5,
		CostUSD:     ptr(0.0123),
		Channels: map[string]ChannelStat{
			"live":    {Received: 100, Replied: 70, Skipped: 25, Failed: 5},
			"comment": {Received: 20, Replied: 15, Skipped: 5, Failed: 0},
		},
	}

	want := "# 日报 2026-11-01 00:00 ~ 2026-11-01 23:59\n" +
		"- 收到 120 / 回复 85 / 跳过 30 / 失败 5\n" +
		"- 成功率 94.4%\n" +
		"- 费用：$0.0123\n" +
		"- 通道：comment 20/15/5/0；live 100/70/25/5"

	if got := Render(s); got != want {
		t.Errorf("渲染结果不符\n实际：\n%s\n期望：\n%s", got, want)
	}
}

// TestRenderCostStates 费用三态：nil 是未知、0 是免费、其余是数值——绝不能把未知写成 0。
func TestRenderCostStates(t *testing.T) {
	cases := []struct {
		name string
		cost *float64
		want string
	}{
		{"未知(nil)", nil, "- 费用：未知（未配置报价）"},
		{"明确免费(0)", ptr(0), "- 费用：$0"},
		{"负数零", ptr(math.Copysign(0, -1)), "- 费用：$0"},
		{"小额", ptr(0.0123), "- 费用：$0.0123"},
		{"整数", ptr(1), "- 费用：$1"},
		{"半美元", ptr(0.5), "- 费用：$0.5"},
		{"较大值", ptr(12.345678), "- 费用：$12.345678"},
		{"NaN", ptr(math.NaN()), "- 费用：未知（报价异常）"},
		{"正无穷", ptr(math.Inf(1)), "- 费用：未知（报价异常）"},
		{"负无穷", ptr(math.Inf(-1)), "- 费用：未知（报价异常）"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Render(Snapshot{CostUSD: tc.cost})
			if !strings.Contains(out, tc.want) {
				t.Errorf("缺少 %q\n实际输出：\n%s", tc.want, out)
			}
			// 关键防线：nil 绝不能被渲染成 $0
			if tc.cost == nil && strings.Contains(out, "$0") {
				t.Errorf("未知费用被渲染成了 0：\n%s", out)
			}
		})
	}
}

// TestRenderRateBoundaries 成功率：0 回复、满分、分母为 0、负数分母都不能崩也不能骗人。
func TestRenderRateBoundaries(t *testing.T) {
	cases := []struct {
		name     string
		received int
		replied  int
		skipped  int
		want     string
	}{
		{"全部回复(满分)", 10, 10, 0, "- 成功率 100.0%"},
		{"一个没回(0%)", 10, 0, 0, "- 成功率 0.0%"},
		{"跳过不算分母", 10, 5, 5, "- 成功率 100.0%"},
		{"一半", 10, 5, 0, "- 成功率 50.0%"},
		{"分母为0(全跳过)", 5, 0, 5, "- 成功率 —"},
		{"没有事件", 0, 0, 0, "- 成功率 —"},
		{"跳过多于收到", 3, 1, 5, "- 成功率 —"},
		{"四舍五入到一位", 3, 1, 0, "- 成功率 33.3%"},
		{"回复多于分母", 5, 7, 0, "- 成功率 140.0%"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := Render(Snapshot{Received: tc.received, Replied: tc.replied, Skipped: tc.skipped})
			if !strings.Contains(out, tc.want) {
				t.Errorf("缺少 %q\n实际输出：\n%s", tc.want, out)
			}
			// 分母为 0 时不能出现 NaN / +Inf 之类的伪百分比
			for _, bad := range []string{"NaN", "Inf", "%!"} {
				if strings.Contains(out, bad) {
					t.Errorf("输出包含非法数值 %q：\n%s", bad, out)
				}
			}
		})
	}
}

// TestRenderEmptySnapshot 空快照不 panic、结构完整。
func TestRenderEmptySnapshot(t *testing.T) {
	out := Render(Snapshot{})

	// 零值时间按固定格式原样输出（不做「未知」之类的额外加工，避免隐藏上游漏传窗口）
	want := "# 日报 0001-01-01 00:00 ~ 0001-01-01 00:00\n" +
		"- 收到 0 / 回复 0 / 跳过 0 / 失败 0\n" +
		"- 成功率 —\n" +
		"- 费用：未知（未配置报价）\n" +
		"- 通道：无"
	if out != want {
		t.Errorf("空快照输出不符\n实际：\n%s\n期望：\n%s", out, want)
	}

	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Errorf("行数 = %d，期望 5（标题 + 4 条统计）", len(lines))
	}
}

// TestRenderChannelOrdering 多通道按名称排序，且与 map 插入顺序无关。
func TestRenderChannelOrdering(t *testing.T) {
	// 故意按「非字典序」插入，Go 的 map 遍历本身也是随机的
	channels := map[string]ChannelStat{
		"qq":      {Received: 4, Replied: 3, Skipped: 1},
		"live":    {Received: 100, Replied: 70, Skipped: 25, Failed: 5},
		"dm":      {Received: 7, Replied: 6, Skipped: 0, Failed: 1},
		"comment": {Received: 20, Replied: 15, Skipped: 5},
	}

	want := "- 通道：comment 20/15/5/0；dm 7/6/0/1；live 100/70/25/5；qq 4/3/1/0"
	out := Render(Snapshot{Channels: channels})
	if !strings.Contains(out, want) {
		t.Errorf("通道排序不符\n实际：\n%s\n期望包含：%s", out, want)
	}

	// 空 map 与 nil map 都渲染成「无」
	if got := Render(Snapshot{Channels: map[string]ChannelStat{}}); !strings.HasSuffix(got, "- 通道：无") {
		t.Errorf("空 map 应渲染为「无」：\n%s", got)
	}
	if got := Render(Snapshot{Channels: nil}); !strings.HasSuffix(got, "- 通道：无") {
		t.Errorf("nil map 应渲染为「无」：\n%s", got)
	}
}

// TestRenderDeterministic 同输入同输出：连渲多次必须完全一致（map 顺序不能泄漏到结果里）。
func TestRenderDeterministic(t *testing.T) {
	s := Snapshot{
		WindowStart: windowStart,
		WindowEnd:   windowEnd,
		Received:    42,
		Replied:     30,
		Skipped:     10,
		Failed:      2,
		CostUSD:     ptr(0.04),
		Channels: map[string]ChannelStat{
			"live":    {Received: 30, Replied: 22, Skipped: 6, Failed: 2},
			"comment": {Received: 12, Replied: 8, Skipped: 4},
			"qq":      {Received: 0},
		},
	}

	first := Render(s)
	for i := 0; i < 20; i++ {
		if got := Render(s); got != first {
			t.Fatalf("第 %d 次渲染不一致\n第一次：\n%s\n本次：\n%s", i+1, first, got)
		}
	}
}

// TestRenderUsesGivenTimeZone 时间按传入时间的时区字面渲染，不做本地时区转换。
func TestRenderUsesGivenTimeZone(t *testing.T) {
	zone := time.FixedZone("CST", 8*3600)
	instant := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	utcOut := Render(Snapshot{WindowStart: instant, WindowEnd: instant})
	if !strings.Contains(utcOut, "# 日报 2026-11-01 00:00 ~ 2026-11-01 00:00") {
		t.Errorf("UTC 渲染不符：\n%s", utcOut)
	}

	// 同一时刻换成 +08:00，应按传入时区显示 08:00（而不是被转成本地时区）
	shifted := instant.In(zone)
	zoneOut := Render(Snapshot{WindowStart: shifted, WindowEnd: shifted})
	if !strings.Contains(zoneOut, "# 日报 2026-11-01 08:00 ~ 2026-11-01 08:00") {
		t.Errorf("+08:00 渲染不符（疑似做了时区转换）：\n%s", zoneOut)
	}
}

// TestRenderNoTrailingNewline 末尾不带换行，方便调用方拼接与写文件。
func TestRenderNoTrailingNewline(t *testing.T) {
	out := Render(Snapshot{WindowStart: windowStart, WindowEnd: windowEnd})
	if strings.HasSuffix(out, "\n") {
		t.Errorf("输出不应以换行结尾：%q", out)
	}
}
