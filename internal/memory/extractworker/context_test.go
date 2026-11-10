package extractworker

// F09：任务队列模式的提炼子 context 必须在 handleJob 返回前释放。
//
// 可观测面：RecentEventsSource 收到的是子 context；正常返回后它应已被取消。
// 失租取消由心跳负责，这里固定的是普通成功路径上的配对释放。

import (
	"context"
	"errors"
	"testing"

	"RaiseMyFish/internal/event"
	"RaiseMyFish/internal/memory"
)

type captureRecent struct {
	ctx context.Context
}

func (c *captureRecent) RecentEvents(ctx context.Context, _ memory.Scope, _ int) ([]event.Event, error) {
	c.ctx = ctx
	return nil, nil
}

func TestJobQueueReleasesExtractionContextOnReturn(t *testing.T) {
	ev := mkEvent("live:1:context-release", baseTime, "我保证明天带话", "路人甲", false, event.ChLive, "1")
	ev.Status = event.StatusDone
	jobs := &fakeJobSource{}
	jobs.add(ev)
	recent := &captureRecent{}

	stats, err := newJobWorker(t, jobs, &fakeRepo{}, Options{Recent: recent}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce 失败：%v", err)
	}
	if stats.Scanned != 1 || stats.Inserted != 1 {
		t.Fatalf("统计不符：%+v", stats)
	}
	if recent.ctx == nil {
		t.Fatal("未捕获提炼子 context")
	}
	if !errors.Is(recent.ctx.Err(), context.Canceled) {
		t.Fatalf("提炼返回后子 context 未释放：%v", recent.ctx.Err())
	}
}
