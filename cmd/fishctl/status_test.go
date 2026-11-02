package main

// 本文件测 0.10c：status 的统计装配（mock repository 注入）与 JSON 输出兼容性。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/database"
	"RaiseMyFish/internal/infra"
	"RaiseMyFish/internal/presence"
)

// statNow 是固定的「当前时间」，避免用例依赖真实时钟。
var statNow = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

// mockStats 是注入用的统计来源，记录收到的窗口参数。
type mockStats struct {
	counts    database.WindowCounts
	lastSeen  map[string]time.Time
	countsErr error
	lastErr   error

	gotFrom, gotTo time.Time
	countCalls     int
	lastCalls      int
}

func (m *mockStats) Counts(_ context.Context, from, to time.Time) (database.WindowCounts, error) {
	m.countCalls++
	m.gotFrom, m.gotTo = from, to
	if m.countsErr != nil {
		return database.WindowCounts{}, m.countsErr
	}
	return m.counts, nil
}

func (m *mockStats) LastSeen(_ context.Context) (map[string]time.Time, error) {
	m.lastCalls++
	if m.lastErr != nil {
		return nil, m.lastErr
	}
	return m.lastSeen, nil
}

// floatPtr 便于构造费用三态。
func floatPtr(v float64) *float64 { return &v }

// TestCollectStatsWithData 有数据：日报三段齐全，心跳告警按通道排序。
func TestCollectStatsWithData(t *testing.T) {
	src := &mockStats{
		counts: database.WindowCounts{
			Received: 8, Replied: 6, Skipped: 1, Failed: 1,
			CostUSD:  floatPtr(0.02),
			Channels: map[string]database.ChannelCounts{"live": {Received: 8, Replied: 6, Skipped: 1, Failed: 1}},
		},
		lastSeen: map[string]time.Time{
			"live":    statNow.Add(-5 * time.Minute),  // 正常
			"comment": statNow.Add(-45 * time.Minute), // 静默
			"dm":      statNow.Add(-2 * time.Hour),    // 静默
		},
	}

	got, err := collectStats(context.Background(), src, statNow, 0)
	if err != nil {
		t.Fatalf("collectStats 失败：%v", err)
	}

	// 默认窗口 24h：from = now-24h，to = now
	if !src.gotTo.Equal(statNow) || !src.gotFrom.Equal(statNow.Add(-DefaultStatsWindow)) {
		t.Errorf("窗口 = [%v, %v]，期望 [%v, %v]", src.gotFrom, src.gotTo, statNow.Add(-DefaultStatsWindow), statNow)
	}

	for _, want := range []string{
		"# 日报 " + statNow.Add(-DefaultStatsWindow).Format("2006-01-02 15:04"),
		"- 收到 8 / 回复 6 / 跳过 1 / 失败 1",
		"- 成功率 85.7%", // 6/(8-1)
		"- 费用：$0.02",
		"- 通道：live 8/6/1/1",
	} {
		if !strings.Contains(got.Report, want) {
			t.Errorf("日报缺少 %q\n实际：\n%s", want, got.Report)
		}
	}

	if len(got.HeartbeatWarnings) != 2 {
		t.Fatalf("告警数 = %d，期望 2：%v", len(got.HeartbeatWarnings), got.HeartbeatWarnings)
	}
	if !strings.Contains(got.HeartbeatWarnings[0], "comment") || !strings.Contains(got.HeartbeatWarnings[1], "dm") {
		t.Errorf("告警顺序不符（应通道名升序）：%v", got.HeartbeatWarnings)
	}
	for _, w := range got.HeartbeatWarnings {
		if !strings.Contains(w, "≥") {
			t.Errorf("静默时长缺少「≥」前缀：%q", w)
		}
	}
}

// TestCollectStatsEmptyData 空数据：不 panic、不伪造数值。
func TestCollectStatsEmptyData(t *testing.T) {
	src := &mockStats{
		counts:   database.WindowCounts{}, // 全 0，费用 nil
		lastSeen: map[string]time.Time{},
	}

	got, err := collectStats(context.Background(), src, statNow, DefaultStatsWindow)
	if err != nil {
		t.Fatalf("collectStats 失败：%v", err)
	}
	for _, want := range []string{
		"- 收到 0 / 回复 0 / 跳过 0 / 失败 0",
		"- 成功率 —",
		"- 费用：未知（未配置报价）",
		"- 通道：无",
	} {
		if !strings.Contains(got.Report, want) {
			t.Errorf("空数据日报缺少 %q\n实际：\n%s", want, got.Report)
		}
	}
	if strings.Contains(got.Report, "$0") {
		t.Errorf("未知费用被渲染成了 0：\n%s", got.Report)
	}
	if len(got.HeartbeatWarnings) != 0 {
		t.Errorf("没有通道数据时不应告警，实际 %v", got.HeartbeatWarnings)
	}
}

// TestCollectStatsNilSource 未装配统计来源：返回零值且不报错（保持 0.1 输出不变）。
func TestCollectStatsNilSource(t *testing.T) {
	got, err := collectStats(context.Background(), nil, statNow, DefaultStatsWindow)
	if err != nil {
		t.Fatalf("nil 来源不应报错：%v", err)
	}
	if !got.empty() {
		t.Errorf("nil 来源应返回零值，实际 %+v", got)
	}

	// nil ctx 是调用方 bug，要明确报错而不是 panic
	//nolint:staticcheck // 刻意传 nil ctx 验证防御分支（SA1012）
	if _, err := collectStats(nil, &mockStats{}, statNow, DefaultStatsWindow); err == nil {
		t.Error("nil ctx 应报错")
	}
}

// TestCollectStatsErrors 查询失败必须上抛，不能吞成「没有数据」。
func TestCollectStatsErrors(t *testing.T) {
	boom := errors.New("查询炸了")

	src := &mockStats{countsErr: boom}
	if _, err := collectStats(context.Background(), src, statNow, DefaultStatsWindow); !errors.Is(err, boom) {
		t.Errorf("Counts 错误未透传：%v", err)
	}

	src2 := &mockStats{lastErr: boom}
	if _, err := collectStats(context.Background(), src2, statNow, DefaultStatsWindow); !errors.Is(err, boom) {
		t.Errorf("LastSeen 错误未透传：%v", err)
	}
}

// TestCollectStatsCanceledContext 取消要保留 context.Canceled，且不发起查询。
func TestCollectStatsCanceledContext(t *testing.T) {
	src := &mockStats{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := collectStats(ctx, src, statNow, DefaultStatsWindow); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
	if src.countCalls != 0 || src.lastCalls != 0 {
		t.Errorf("取消后不应查询：counts=%d last=%d", src.countCalls, src.lastCalls)
	}
}

// TestPrintStatusWithStats 有统计时追加 stats 字段，报告与告警都能从 JSON 读回。
func TestPrintStatusWithStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := infra.WriteStatus(path, infra.Status{
		PID: 1, State: "running", Database: "ready", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	stats := StatusStats{
		Report:            "# 日报 2026-11-02 00:00 ~ 2026-11-02 00:00",
		HeartbeatWarnings: []string{"通道 live 已静默 ≥45 分钟（最后事件 2026-11-01 23:15）"},
	}
	var out bytes.Buffer
	if err := printStatus(path, 20*time.Second, true, &out, stats); err != nil {
		t.Fatalf("printStatus 失败：%v", err)
	}

	var decoded struct {
		Alive bool `json:"service_alive"`
		Stats struct {
			Report   string   `json:"report"`
			Warnings []string `json:"heartbeat_warnings"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON：%v\n%s", err, out.String())
	}
	if !decoded.Alive {
		t.Error("service_alive 应为 true")
	}
	if !strings.Contains(decoded.Stats.Report, "# 日报") {
		t.Errorf("stats.report 不符：%q", decoded.Stats.Report)
	}
	if len(decoded.Stats.Warnings) != 1 || !strings.Contains(decoded.Stats.Warnings[0], "≥") {
		t.Errorf("stats.heartbeat_warnings 不符：%v", decoded.Stats.Warnings)
	}
}

// TestPrintStatusWithPresence 多值摘要从状态文件读到顶层 presence 字段。
func TestPrintStatusWithPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	table := presence.NewTable()
	table.Set(presence.State{
		Kind: presence.KindWatching, Channel: "live", Detail: "30308220",
		Since: statNow.Add(-2 * time.Minute), TTL: time.Hour,
	})
	table.Set(presence.State{
		Kind: presence.KindReplying, Channel: "live",
		Since: statNow.Add(-time.Minute), TTL: time.Hour,
	})
	want := table.Summary(statNow)
	if want != "在看直播 30308220 / 在回弹幕" {
		t.Fatalf("Table 摘要 = %q", want)
	}
	if err := infra.WriteStatus(path, infra.Status{
		PID: 1, State: "running", Database: "ready", Presence: want, UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := printStatus(path, 20*time.Second, true, &out); err != nil {
		t.Fatalf("printStatus 失败：%v", err)
	}
	var decoded struct {
		Presence string       `json:"presence"`
		Service  infra.Status `json:"service"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("输出不是合法 JSON：%v\n%s", err, out.String())
	}
	if decoded.Presence != want {
		t.Errorf("presence = %q，期望 %q", decoded.Presence, want)
	}
	if decoded.Service.Presence != want {
		t.Errorf("service.presence = %q，期望 %q", decoded.Service.Presence, want)
	}
}

// TestPrintStatusOmitsEmptyPresence 空表或未存活时不输出 presence 字段。
func TestPrintStatusOmitsEmptyPresence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	cases := []struct {
		name  string
		state infra.Status
	}{
		{"空表", infra.Status{PID: 1, State: "running", Database: "ready", UpdatedAt: time.Now().UTC()}},
		{"已停止", infra.Status{PID: 1, State: "stopped", Database: "ready", Presence: "在看直播 1", UpdatedAt: time.Now().UTC()}},
		{"心跳过期", infra.Status{PID: 1, State: "running", Database: "ready", Presence: "在看直播 1", UpdatedAt: time.Now().UTC().Add(-time.Hour)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := infra.WriteStatus(path, tc.state); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			_ = printStatus(path, 20*time.Second, true, &out)
			if bytes.Contains(out.Bytes(), []byte(`"presence"`)) {
				t.Fatalf("不应输出 presence：%s", out.String())
			}
		})
	}
}

// TestPrintStatusWithoutStats 不传统计时输出与 0.1 一致：不出现 stats 字段。
func TestPrintStatusWithoutStats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := infra.WriteStatus(path, infra.Status{
		PID: 1, State: "running", Database: "ready", UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"不传参数", "传空统计"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			var err error
			if name == "不传参数" {
				err = printStatus(path, 20*time.Second, true, &out)
			} else {
				err = printStatus(path, 20*time.Second, true, &out, StatusStats{})
			}
			if err != nil {
				t.Fatalf("printStatus 失败：%v", err)
			}
			if bytes.Contains(out.Bytes(), []byte(`"stats"`)) {
				t.Errorf("不应输出 stats 字段：\n%s", out.String())
			}
			if !bytes.Contains(out.Bytes(), []byte(`"service_alive":true`)) {
				t.Errorf("缺少既有字段：\n%s", out.String())
			}
		})
	}
}
