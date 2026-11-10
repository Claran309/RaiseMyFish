package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/store"
	"RaiseMyFish/internal/viewer"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type scriptedViewerSummaryWorker struct {
	mu    sync.Mutex
	calls int
	seen  chan int
}

func (w *scriptedViewerSummaryWorker) RunOnce(context.Context) (viewer.SummaryStats, error) {
	w.mu.Lock()
	w.calls++
	n := w.calls
	w.mu.Unlock()
	select {
	case w.seen <- n:
	default:
	}
	return viewer.SummaryStats{Candidates: 2, Summarized: 1, Fallback: 1}, nil
}

func TestRunViewerSummaryImmediateAndStop(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	worker := &scriptedViewerSummaryWorker{seen: make(chan int, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 5ms 间隔：验证启动立即跑一轮且 ticker 会继续触发。
		runViewerSummary(ctx, worker, 5*time.Millisecond, zap.New(core))
	}()
	for i := 1; i <= 2; i++ {
		select {
		case n := <-worker.seen:
			if n != i {
				cancel()
				<-done
				t.Fatalf("第 %d 轮实际收到 %d", i, n)
			}
		case <-time.After(2 * time.Second):
			cancel()
			<-done
			t.Fatalf("只执行了 %d 轮", i-1)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后侧写循环未退出")
	}
	if logs.FilterMessage("观众侧写完成").Len() < 2 {
		t.Fatalf("缺少完成日志：%v", logs.All())
	}
}

func TestStartViewerSummaryDisabled(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{Memory: config.Memory{ViewerSummaryIntervalSeconds: 0}}
	stop := startViewerSummary(context.Background(), cfg, nil, knowledgeAssembly{}, zap.New(core))
	stop()
	if logs.Len() != 0 {
		t.Fatalf("关闭时不应启动或记日志：%v", logs.All())
	}
}

func TestStartViewerSummaryInvalidInterval(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	cfg := config.Config{Memory: config.Memory{ViewerSummaryIntervalSeconds: 30}}
	stop := startViewerSummary(context.Background(), cfg, nil, knowledgeAssembly{}, zap.New(core))
	stop()
	if logs.FilterMessage("观众侧写间隔无效，跳过").Len() == 0 {
		t.Fatalf("非法间隔未记跳过日志：%v", logs.All())
	}
}

func TestNewViewerSummarizerNeedsDB(t *testing.T) {
	if _, _, err := newViewerSummarizer(config.Config{}, nil, knowledgeAssembly{}, nil); err == nil {
		t.Fatal("缺少数据库应拒绝装配")
	}
}

func viewerTestProfile() store.ViewerProfile {
	lastSeen := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	return store.ViewerProfile{
		UID: "42", Name: "白饭哥", Hits: 12, Replies: 3, LastSeen: lastSeen,
		RecentTexts:    []string{"一", "二", "三", "四", "五", "六", "七", "八"},
		RecentReplies:  []string{"甲", "乙", "丙", "丁"},
		ProfileSummary: "性格：话痨；偏好：猫猫；口头禅：绷不住了；关系：常被鱼回；最近话题：白饭",
	}
}

func TestFormatViewerProfileIncludesSummary(t *testing.T) {
	got := formatViewerProfile(viewerTestProfile())
	if !strings.Contains(got, "【对方档案 · 老熟人】") || !strings.Contains(got, "说过 12 次") {
		t.Fatalf("档案头或统计丢失：%q", got)
	}
	if !strings.Contains(got, "- 侧写：性格：话痨；偏好：猫猫") {
		t.Fatalf("侧写未注入：%q", got)
	}
	// 有侧写时原始样本缩减为 5 条发言、3 条回复。
	if strings.Contains(got, "- 六") || strings.Contains(got, "- 丁") {
		t.Fatalf("有侧写时原始样本未缩减：%q", got)
	}
	if !strings.Contains(got, "- 五") || !strings.Contains(got, "- 丙") {
		t.Fatalf("缩减后样本缺失：%q", got)
	}
	if len([]rune(got)) > 400 {
		t.Fatalf("注入片段超过 400 字：%d", len([]rune(got)))
	}
}

func TestFormatViewerProfileWithoutSummary(t *testing.T) {
	profile := viewerTestProfile()
	profile.ProfileSummary = ""
	got := formatViewerProfile(profile)
	if strings.Contains(got, "侧写：") {
		t.Fatalf("空侧写不应显示：%q", got)
	}
	if !strings.Contains(got, "- 八") || !strings.Contains(got, "- 丁") {
		t.Fatalf("无侧写时应保留完整样本：%q", got)
	}
}

func TestFormatViewerProfileCompactsSummary(t *testing.T) {
	profile := viewerTestProfile()
	profile.ProfileSummary = "性格：话痨\r\n偏好：猫猫\n\n口头禅：" + strings.Repeat("绷", 300)
	got := formatViewerProfile(profile)
	if strings.ContainsAny(got, "\r") {
		t.Fatalf("侧写换行未折叠：%q", got)
	}
	if !strings.Contains(got, "性格：话痨 偏好：猫猫") {
		t.Fatalf("侧写空白未折叠：%q", got)
	}
	line := ""
	for _, part := range strings.Split(got, "\n") {
		if strings.HasPrefix(part, "- 侧写：") {
			line = strings.TrimPrefix(part, "- 侧写：")
		}
	}
	if n := len([]rune(line)); n == 0 || n > viewerSummaryInjectRunes {
		t.Fatalf("侧写长度不符：%d", n)
	}
}
