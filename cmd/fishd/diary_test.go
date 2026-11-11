package main

// 本文件测每日总结装配（#76）：时区调度、事件顺序与仓储/模型错误路径。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/config"
	"RaiseMyFish/internal/diary"
	"RaiseMyFish/internal/store"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type fakeDiaryRepo struct {
	rows      []store.DiaryEvent
	dayErr    error
	saveErr   error
	saved     []store.DailySummary
	gotFrom   time.Time
	gotTo     time.Time
	gotLimit  int
	dayCalls  int
	saveCalls int
}

func (f *fakeDiaryRepo) DayEvents(_ context.Context, from, to time.Time, limit int) ([]store.DiaryEvent, error) {
	f.dayCalls++
	f.gotFrom, f.gotTo, f.gotLimit = from, to, limit
	return f.rows, f.dayErr
}

func (f *fakeDiaryRepo) SaveDailySummary(_ context.Context, summary store.DailySummary) error {
	f.saveCalls++
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, summary)
	return nil
}

func (f *fakeDiaryRepo) DailySummary(context.Context, string) (store.DailySummary, error) {
	return store.DailySummary{}, store.ErrDiaryNotFound
}

func (f *fakeDiaryRepo) DailySummaries(context.Context, int) ([]store.DailySummary, error) {
	return nil, nil
}

type fakeDiaryLLM struct {
	reply        string
	err          error
	gotSystem    string
	gotUser      string
	completeCall int
}

func (f *fakeDiaryLLM) Complete(_ context.Context, system, user string) (string, error) {
	f.completeCall++
	f.gotSystem, f.gotUser = system, user
	return f.reply, f.err
}

func newTestDigest(t *testing.T, repo store.DiaryRepo, llm diary.LLM, maxEvents int) *dailyDigest {
	t.Helper()
	summarizer, err := diary.New(llm, diary.Options{MaxEvents: maxEvents})
	if err != nil {
		t.Fatal(err)
	}
	return &dailyDigest{repo: repo, summarizer: summarizer, model: "test-model", maxEvents: maxEvents}
}

func TestDailyDigestGenerate(t *testing.T) {
	early := time.Date(2026, 11, 10, 1, 0, 0, 0, diary.Zone)
	late := time.Date(2026, 11, 10, 9, 0, 0, 0, diary.Zone)
	repo := &fakeDiaryRepo{rows: []store.DiaryEvent{
		{Channel: "live", Dir: "in", FromName: "乙", Text: "晚上再来", At: late},
		{Channel: "live", Dir: "in", FromName: "甲", Text: "早上好", At: early},
	}}
	llm := &fakeDiaryLLM{reply: "## 概览\n今天很热闹"}
	digest := newTestDigest(t, repo, llm, 300)

	summary, err := digest.Generate(context.Background(), "2026-11-10")
	if err != nil {
		t.Fatal(err)
	}
	// 东八区自然日对应 UTC 前一天 16:00 到当天 16:00。
	wantFrom := time.Date(2026, 11, 9, 16, 0, 0, 0, time.UTC)
	if !repo.gotFrom.Equal(wantFrom) || !repo.gotTo.Equal(wantFrom.AddDate(0, 0, 1)) {
		t.Fatalf("日期区间错误：%v ~ %v", repo.gotFrom, repo.gotTo)
	}
	if repo.gotLimit != 300 {
		t.Fatalf("事件上限未透传：%d", repo.gotLimit)
	}
	// 仓储按新到旧返回，提示词必须还原成时间正序。
	if strings.Index(llm.gotUser, "早上好") > strings.Index(llm.gotUser, "晚上再来") {
		t.Fatalf("提示词事件顺序不是正序：%s", llm.gotUser)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("未保存日报：%d", len(repo.saved))
	}
	saved := repo.saved[0]
	if saved.Day != "2026-11-10" || saved.Content != llm.reply || saved.Model != "test-model" || saved.EventCount != 2 {
		t.Fatalf("日报字段错误：%+v", saved)
	}
	if summary.Day != saved.Day || summary.CreatedAt.IsZero() || summary.UpdatedAt.IsZero() {
		t.Fatalf("返回日报缺少时间：%+v", summary)
	}
}

func TestDailyDigestGenerateErrors(t *testing.T) {
	repo := &fakeDiaryRepo{}
	llm := &fakeDiaryLLM{reply: "ok"}
	digest := newTestDigest(t, repo, llm, 300)

	if _, err := digest.Generate(context.Background(), "2026-13-01"); err == nil {
		t.Fatal("非法日期应失败")
	}
	if repo.dayCalls != 0 || llm.completeCall != 0 {
		t.Fatal("非法日期不应访问仓储或模型")
	}

	repo.dayErr = errors.New("db down")
	if _, err := digest.Generate(context.Background(), "2026-11-10"); err == nil || !strings.Contains(err.Error(), "读取日报事件失败") {
		t.Fatalf("仓储错误未包装：%v", err)
	}

	repo.dayErr = nil
	if _, err := digest.Generate(context.Background(), "2026-11-10"); !errors.Is(err, diary.ErrNoEvents) {
		t.Fatalf("空事件应返回 ErrNoEvents：%v", err)
	}
	if repo.saveCalls != 0 || llm.completeCall != 0 {
		t.Fatal("空事件不应调用模型或保存")
	}

	repo.rows = []store.DiaryEvent{{Channel: "live", Dir: "in", FromName: "甲", Text: "在"}}
	repo.saveErr = errors.New("write failed")
	if _, err := digest.Generate(context.Background(), "2026-11-10"); err == nil || !strings.Contains(err.Error(), "保存日报失败") {
		t.Fatalf("保存错误未包装：%v", err)
	}
}

func TestNextDailyRun(t *testing.T) {
	cases := []struct {
		name       string
		now        time.Time
		hour, min  int
		wantDay    int
		wantHour   int
		wantMinute int
	}{
		{"凌晨前", time.Date(2026, 11, 10, 0, 9, 59, 0, diary.Zone), 0, 10, 10, 0, 10},
		{"已过时刻", time.Date(2026, 11, 10, 8, 0, 0, 0, diary.Zone), 0, 10, 11, 0, 10},
		{"恰好等于", time.Date(2026, 11, 10, 0, 10, 0, 0, diary.Zone), 0, 10, 11, 0, 10},
		{"UTC输入", time.Date(2026, 11, 10, 20, 0, 0, 0, time.UTC), 8, 30, 11, 8, 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := nextDailyRun(tc.now, tc.hour, tc.min)
			local := next.In(diary.Zone)
			if local.Day() != tc.wantDay || local.Hour() != tc.wantHour || local.Minute() != tc.wantMinute {
				t.Fatalf("nextDailyRun = %v，期望 11月%d日 %02d:%02d", local, tc.wantDay, tc.wantHour, tc.wantMinute)
			}
			if !next.After(tc.now) {
				t.Fatalf("下次执行不在将来：%v <= %v", next, tc.now)
			}
		})
	}
}

type fakeDiaryGenerator struct {
	day    string
	err    error
	called chan string
}

func (f *fakeDiaryGenerator) Generate(_ context.Context, day string) (store.DailySummary, error) {
	if f.called != nil {
		// 非阻塞：循环可能在测试读取后再触发一次，不能因此卡死。
		select {
		case f.called <- day:
		default:
		}
	}
	if f.err != nil {
		return store.DailySummary{}, f.err
	}
	return store.DailySummary{Day: day, EventCount: 3}, nil
}

func TestDailySummaryLoopFires(t *testing.T) {
	// 前两次取「一分钟边界前 0.5 秒」：首次排定时器立即触发，第二次算昨天；
	// 之后把时间拨过边界，下一轮排到次日，循环不会空转。
	base := time.Date(2026, 1, 1, 0, 9, 59, 500_000_000, diary.Zone)
	calls := 0
	now := func() time.Time {
		calls++
		if calls > 2 {
			return base.Add(2 * time.Second)
		}
		return base
	}
	generator := &fakeDiaryGenerator{called: make(chan string, 1)}
	stop := startDailySummaryLoop(context.Background(), generator, config.Summary{Hour: 0, Minute: 10}, now, zap.NewNop())
	select {
	case day := <-generator.called:
		if day != "2025-12-31" {
			t.Fatalf("生成的日期错误：%s", day)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("定时器未触发")
	}
	stop()

	// 取消后循环必须立即退出；再次调用 stop 不能阻塞（闭包幂等不由本函数保证，仅验证一次）。
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stop 未等待循环退出")
	}
}

func TestDailySummaryLoopSurvivesFailure(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 9, 59, 500_000_000, diary.Zone)
	calls := 0
	now := func() time.Time {
		calls++
		if calls > 2 {
			return base.Add(2 * time.Second)
		}
		return base
	}
	generator := &fakeDiaryGenerator{called: make(chan string, 1), err: errors.New("llm down")}
	stop := startDailySummaryLoop(context.Background(), generator, config.Summary{Hour: 0, Minute: 10}, now, zap.NewNop())
	select {
	case <-generator.called:
	case <-time.After(5 * time.Second):
		t.Fatal("定时器未触发")
	}
	// 失败只记日志，循环仍存活；stop 应能正常回收。
	stop()
}

func TestStartDailySummaryDisabled(t *testing.T) {
	generator, stop := startDailySummary(context.Background(), config.Config{}, nil, nil, zap.NewNop())
	if generator != nil {
		t.Fatalf("LLM 未启用时不应有生成器：%T", generator)
	}
	stop()
}

func TestBuildDailyDigest(t *testing.T) {
	if _, err := buildDailyDigest(config.Config{}, nil, nil, zap.NewNop()); err == nil {
		t.Fatal("缺少数据库应失败")
	}
	cfg := config.Config{}
	cfg.LLM.Enabled = true
	cfg.LLM.AttemptTimeoutMS = 1000
	cfg.LLM.Providers = map[string]config.Provider{
		"local": {BaseURL: "http://127.0.0.1:1", Models: []string{"m"}},
	}
	cfg.LLM.Routing = map[string]string{"default": "local:m"}
	cfg.Summary = config.Summary{MaxEvents: 300, MaxTokens: 900}
	digest, err := buildDailyDigest(cfg, &gorm.DB{}, nil, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if digest.model != "m" || digest.maxEvents != 300 {
		t.Fatalf("装配字段错误：%+v", digest)
	}
}
