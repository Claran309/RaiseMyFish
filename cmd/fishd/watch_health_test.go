package main

// 本文件测 O09：看直播循环持续业务失败时，面板要能看出「循环在跑但不健康」。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"RaiseMyFish/internal/watch"
	"go.uber.org/zap"
)

// failingWatchRunner 每轮都返回一个降级结果（录制失败）。
type failingWatchRunner struct{ calls int }

func (r *failingWatchRunner) Run(context.Context) (watch.Result, error) {
	r.calls++
	return watch.Result{Failures: []watch.Failure{{Stage: watch.StageRecord, Err: errors.New("录不到流")}}}, nil
}

// TestWatchAttemptSemantics OK 只看「有没有失败环节」，firstStage 把整轮错误归到 run。
func TestWatchAttemptSemantics(t *testing.T) {
	if !(watchAttempt{}).OK() {
		t.Fatal("无失败环节应算成功")
	}
	degraded := watchAttempt{Degraded: true, Stages: []string{watch.StageFrames}}
	if degraded.OK() {
		t.Fatal("有降级环节不应算成功")
	}
	if got := degraded.firstStage(); got != watch.StageFrames {
		t.Fatalf("失败环节 = %q", got)
	}
	if got := (watchAttempt{Err: errors.New("boom")}).firstStage(); got != "run" {
		t.Fatalf("整轮错误应归到 run，实际 %q", got)
	}
}

// TestWatchControllerReportsUnhealthy 连续失败 → running 仍在但 healthy=false，
// 且面板能看到连续次数与失败环节（这正是 O09 要修的状态失真）。
func TestWatchControllerReportsUnhealthy(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	ctl := &watchController{ctx: context.Background(), log: zap.NewNop(), now: func() time.Time { return now }, available: true}
	ctl.mu.Lock()
	ctl.running = true
	ctl.startedAt = now
	ctl.mu.Unlock()

	for i := 0; i < 3; i++ {
		ctl.recordAttempt(watchAttempt{
			At:     now.Add(time.Duration(i) * time.Minute),
			Stages: []string{watch.StageRecord},
		})
	}

	st := ctl.status()
	if !st.Running {
		t.Fatal("循环还在跑，running 应为 true")
	}
	if st.Healthy {
		t.Fatal("持续失败时不能显示健康")
	}
	if st.ConsecutiveFailures != 3 {
		t.Fatalf("连续失败次数 = %d，期望 3", st.ConsecutiveFailures)
	}
	if st.LastFailureStage != watch.StageRecord {
		t.Fatalf("失败环节 = %q", st.LastFailureStage)
	}
	if st.LastAttemptAt == "" {
		t.Fatal("缺少最后尝试时间")
	}
	if st.LastSuccessAt != "" {
		t.Fatalf("从未成功过，last_success_at 应为空：%q", st.LastSuccessAt)
	}

	// 一次成功即归零并变健康。
	ctl.recordAttempt(watchAttempt{At: now.Add(10 * time.Minute)})
	st = ctl.status()
	if !st.Healthy || st.ConsecutiveFailures != 0 || st.LastFailureStage != "" {
		t.Fatalf("成功后应恢复健康：%+v", st)
	}
	if st.LastSuccessAt == "" {
		t.Fatal("缺少最后成功时间")
	}
}

// TestWatchControllerNotHealthyBeforeFirstSuccess 刚启动还没跑过一轮：running 但不算 healthy。
func TestWatchControllerNotHealthyBeforeFirstSuccess(t *testing.T) {
	ctl := &watchController{ctx: context.Background(), log: zap.NewNop(), now: time.Now, available: true}
	ctl.mu.Lock()
	ctl.running = true
	ctl.startedAt = time.Now()
	ctl.mu.Unlock()

	if st := ctl.status(); st.Healthy {
		t.Fatalf("没成功过一轮不能算健康：%+v", st)
	}
}

// TestWatchControllerStopClearsHealth 停止后不再残留「连续失败 N 次」。
func TestWatchControllerStopClearsHealth(t *testing.T) {
	ctl := &watchController{ctx: context.Background(), log: zap.NewNop(), now: time.Now, available: true}
	ctl.mu.Lock()
	ctl.running = true
	ctl.mu.Unlock()
	ctl.recordAttempt(watchAttempt{At: time.Now(), Stages: []string{watch.StageRecord}})
	if st := ctl.status(); st.ConsecutiveFailures != 1 {
		t.Fatalf("前置失败未记录：%+v", st)
	}

	if err := ctl.set(false, ""); err != nil {
		t.Fatal(err)
	}
	st := ctl.status()
	if st.Running || st.Healthy || st.ConsecutiveFailures != 0 || st.LastFailureStage != "" {
		t.Fatalf("停止后应清空健康状态：%+v", st)
	}
}

// TestRunWatchReportsEveryAttempt 业务失败也要每轮上报（不只是打日志）。
func TestRunWatchReportsEveryAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	attempts := make(chan watchAttempt, 4)
	go runWatch(ctx, &failingWatchRunner{}, ticks, time.Now, zap.NewNop(), func(a watchAttempt) {
		attempts <- a
	})

	for i := 0; i < 3; i++ {
		ticks <- time.Now()
		select {
		case a := <-attempts:
			if a.OK() {
				t.Fatalf("失败轮次被上报成成功：%+v", a)
			}
			if len(a.Stages) == 0 || a.Stages[0] != watch.StageRecord {
				t.Fatalf("失败环节不符：%+v", a)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("失败轮次未上报")
		}
	}
}

// TestWatchStateHealthFieldsExposed 状态里的健康字段要能被 API 序列化出来。
func TestWatchStateHealthFieldsExposed(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	ctl := &watchController{ctx: context.Background(), log: zap.NewNop(), now: func() time.Time { return now }, available: true}
	ctl.mu.Lock()
	ctl.running = true
	ctl.mu.Unlock()
	ctl.recordAttempt(watchAttempt{At: now, Stages: []string{watch.StageRecord}})

	st := ctl.status()
	// 这里只验证内部字段被填上；JSON 标签由 web 包的 httptest 覆盖。
	if st.ConsecutiveFailures == 0 || st.LastFailureStage == "" {
		t.Fatalf("健康字段未填充：%+v", st)
	}
	if !strings.HasPrefix(st.LastAttemptAt, "2026-11-08") {
		t.Fatalf("时间格式不符：%q", st.LastAttemptAt)
	}
}
